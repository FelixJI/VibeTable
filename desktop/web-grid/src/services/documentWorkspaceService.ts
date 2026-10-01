import { watch } from "vue";
import type { DocumentAuthority } from "@/stores/documentWorkspaceStore";
import { parseDocumentDiffSessionResult, parseDocumentDiffChangePageResult } from "@/contracts/documentDiffV2";
import type { DocumentCapability, DocumentEntry } from "@/stores/documentWorkspaceStore";
import { useDocumentWorkspaceStore } from "@/stores/documentWorkspaceStore";
import type {
  DocumentListLoadedPayload,
  FileDocumentQuery,
} from "@/contracts";
import { useHostBridge } from "./bridgeContext";

export type DocumentWorkspaceScope =
  | { readonly kind: "global" }
  | {
      readonly kind: "record";
      readonly collection: string;
      readonly itemId: string | number;
    };

export type DocumentWorkspaceIntent =
  | { readonly type: "document.diffPageRequested"; readonly sessionId: string; readonly cursor: string | null; readonly limit: number }
  | { readonly type: "document.diffCloseRequested"; readonly sessionId: string }
  | { readonly type: "document.listRequested"; readonly scope: DocumentWorkspaceScope; readonly authority: DocumentAuthority; readonly query: FileDocumentQuery }
  | { readonly type: "document.importRequested"; readonly scope: DocumentWorkspaceScope }
  | { readonly type: "document.externalDropRequested"; readonly scope: DocumentWorkspaceScope; readonly files: readonly File[] }
  | { readonly type: "document.dragOutRequested"; readonly handle: string }
  | { readonly type: "document.openRequested"; readonly entryHandle: string }
  | { readonly type: "document.previewRequested"; readonly entryHandle: string }
  | {
      readonly type: "document.diffRequested";
      readonly entryHandle: string;
      readonly operationId: string;
      readonly historicalRevisionId: string;
      readonly expectedEffectiveRevisionId: string;
    }
  | {
      readonly type: "document.diffCancelRequested";
      readonly entryHandle: string;
      readonly operationId: string;
    }
  | { readonly type: "document.revealRequested"; readonly entryHandle: string }
  | { readonly type: "document.relinkRequested"; readonly handle: string };

export interface DocumentWorkspaceService {
  list(scope: DocumentWorkspaceScope, authority: DocumentAuthority, query?: FileDocumentQuery): void;
  importFiles(scope: DocumentWorkspaceScope): void;
  externalDrop(scope: DocumentWorkspaceScope, files: readonly File[]): void;
  dragOut(handle: string): void;
  open(entryHandle: string): void;
  preview(entryHandle: string): void;
  compare(
    entryHandle: string,
    historicalRevisionId: string,
    expectedEffectiveRevisionId: string,
  ): void;
  cancelDiff(entryHandle: string, operationId?: string): void;
  diffPage(sessionId: string, cursor: string | null): void;
  closeDiff(sessionId: string): void;
  reveal(entryHandle: string): void;
  relink(handle: string): void;
}

/**
 * Intent-only adapter for the future typed host bridge integration.
 * The caller owns dispatch, so this layer cannot imply that a host operation succeeded.
 */
export function createDocumentWorkspaceService(
  dispatch: (intent: DocumentWorkspaceIntent) => void,
): DocumentWorkspaceService {
  const diffOperations = new Map<string, string>();
  return {
    diffPage: (sessionId, cursor) => dispatch({ type: "document.diffPageRequested", sessionId, cursor, limit: 50 }),
    closeDiff: (sessionId) => dispatch({ type: "document.diffCloseRequested", sessionId }),
    list: (scope, authority, query = defaultDocumentQuery()) => dispatch({
      type: "document.listRequested", scope, authority, query,
    }),
    importFiles: (scope) => dispatch({ type: "document.importRequested", scope }),
    externalDrop: (scope, files) => dispatch({ type: "document.externalDropRequested", scope, files }),
    dragOut: (handle) => dispatch({ type: "document.dragOutRequested", handle }),
    open: (entryHandle) => dispatch({ type: "document.openRequested", entryHandle }),
    preview: (entryHandle) => dispatch({ type: "document.previewRequested", entryHandle }),
    compare: (entryHandle, historicalRevisionId, expectedEffectiveRevisionId) => {
      const operationId = crypto.randomUUID();
      diffOperations.set(entryHandle, operationId);
      dispatch({
        type: "document.diffRequested",
        entryHandle,
        operationId,
        historicalRevisionId,
        expectedEffectiveRevisionId,
      });
    },
    cancelDiff: (entryHandle, requestedOperationId) => {
      const operationId = requestedOperationId ?? diffOperations.get(entryHandle);
      if (!operationId) return;
      dispatch({
        type: "document.diffCancelRequested",
        entryHandle,
        operationId,
      });
    },
    reveal: (entryHandle) => dispatch({ type: "document.revealRequested", entryHandle }),
    relink: (handle) => dispatch({ type: "document.relinkRequested", handle }),
  };
}

/** Bridge-backed integration used by WorkspaceView. */
export function useDocumentWorkspaceService(): {
  dispatch: (intent: DocumentWorkspaceIntent) => void;
} {
  const bridge = useHostBridge();
  const store = useDocumentWorkspaceStore();
  let lastScope: DocumentWorkspaceScope = { kind: "global" };
  let lastListQuery: FileDocumentQuery | null = null;
  let listGeneration = 0;
  watch(() => store.diffResult?.session?.sessionId, (next, old) => {
    if (old && old !== next) void bridge.request("document.diffCloseRequested", { sessionId: old })
      .catch(error => store.setFailed(error instanceof Error ? error.message : String(error)));
  }, { flush: "sync" });

  async function readPage(sessionId: string, cursor: string | null, limit: number): Promise<void> {
    if (store.diffPageBusy) return;
    const generation = store.currentDiffGeneration();
    store.diffPageBusy = true;
    const request = { sessionId, cursor, limit };
    try {
      const raw = await bridge.request("document.diffPageRequested", request);
      store.completeDiffPage(generation, parseDocumentDiffChangePageResult(raw, request));
    } catch (error) {
      if (store.failDiff(generation, error instanceof Error ? error.message : String(error))) {
        store.diffPageBusy = false;
        await bridge.request("document.diffCloseRequested", { sessionId });
      }
    }
  }

  async function execute(intent: DocumentWorkspaceIntent): Promise<void> {
    try {
      switch (intent.type) {
        case "document.listRequested": {
          const generation = ++listGeneration;
          lastScope = intent.scope;
          lastListQuery = intent.query;
          store.beginLoad();
          let payload: DocumentListLoadedPayload;
          try {
            payload = await bridge.request(intent.type, {
              scope: intent.scope,
              authority: intent.authority,
              query: intent.query,
            }) as DocumentListLoadedPayload;
          } catch (error) {
            if (generation === listGeneration) {
              store.setFailed(error instanceof Error ? error.message : String(error));
            }
            return;
          }
          if (generation !== listGeneration) return;
          store.setPage(
            payload.entries.map((entry) => toStoreEntry(entry, intent.authority)),
            payload.nextCursor,
            payload.topologyRevision,
            intent.query.cursor !== null,
          );
          return;
        }
        case "document.importRequested":
          bridge.notify(intent.type, { scope: intent.scope });
          return;
        case "document.externalDropRequested": {
          const sentWithFiles = intent.files.length > 0 &&
            bridge.notifyWithAdditionalObjects(
              intent.type,
              { scope: intent.scope },
              intent.files,
            );
          if (!sentWithFiles) bridge.notify(intent.type, { scope: intent.scope });
          return;
        }
        case "document.dragOutRequested":
        case "document.relinkRequested":
          bridge.notify(intent.type, { handle: intent.handle });
          return;
        case "document.openRequested":
        case "document.previewRequested":
        case "document.revealRequested":
          await bridge.request(intent.type, { entryHandle: intent.entryHandle });
          return;
        case "document.diffRequested": {
          const generation = store.beginDiff(
            intent.entryHandle,
            intent.historicalRevisionId,
            intent.expectedEffectiveRevisionId,
            intent.operationId,
          );
          try {
            const raw = await bridge.request(intent.type, {
              entryHandle: intent.entryHandle,
              operationId: intent.operationId,
              historicalRevisionId: intent.historicalRevisionId,
              expectedEffectiveRevisionId: intent.expectedEffectiveRevisionId,
            });
            const result = parseDocumentDiffSessionResult(raw);
            if (!store.completeDiff(generation, result)) {
              if (result.outcome === "ready") await bridge.request("document.diffCloseRequested", { sessionId: result.session.sessionId });
            } else if (result.outcome === "ready") {
              await readPage(result.session.sessionId, null, 50);
            }
          } catch (error) {
            store.failDiff(
              generation,
              error instanceof Error ? error.message : String(error),
            );
          }
          return;
        }
        case "document.diffCancelRequested":
          store.cancelDiff();
          await bridge.request(intent.type, {
            entryHandle: intent.entryHandle,
            operationId: intent.operationId,
          });
          return;
        case "document.diffPageRequested":
          await readPage(intent.sessionId, intent.cursor, intent.limit);
          return;
        case "document.diffCloseRequested":
          store.cancelDiff();
          await bridge.request(intent.type, { sessionId: intent.sessionId });
          return;
      }
    } catch (error) {
      store.setFailed(error instanceof Error ? error.message : String(error));
    }
  }

  bridge.on("document.workspaceChanged", () => {
    void execute({
      type: "document.listRequested",
      scope: lastScope,
      authority: store.authorityFilter,
      // A change notice only resets pagination to the first page. It reuses
      // the last real list intent's complete query so a successful change
      // never silently drops the user's active filters, and it creates no
      // second query authority of its own.
      query: lastListQuery === null
        ? defaultDocumentQuery(store.query)
        : { ...lastListQuery, cursor: null },
    });
  });
  bridge.on("document.operationFailed", (payload) => {
    store.setFailed(payload.message, payload.code ?? null);
  });

  return {
    dispatch: (intent) => { void execute(intent); },
  };
}

function toStoreEntry(
  entry: DocumentListLoadedPayload["entries"][number],
  authority: DocumentAuthority,
): DocumentEntry {
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(entry.documentId)) {
    throw new Error("document.listLoaded returned a non-canonical documentId");
  }
  return {
    documentId: entry.documentId,
    entryHandle: entry.entryHandle,
    displayName: entry.displayName,
    authority,
    availability: entry.availability,
    relativePath: entry.relativePath,
    extension: entry.extension,
    mimeType: entry.mimeType,
    sizeBytes: entry.sizeBytes,
    effectiveRevisionCreatedAt: entry.effectiveRevisionCreatedAt,
    formalVersion: entry.formalVersion,
    status: entry.status,
    versionLabel: entry.currentRevision ?? undefined,
    effectiveRevisionId: entry.effectiveRevisionId ?? undefined,
    capabilities: normalizeCapabilities(entry.capabilities),
  };
}

export function defaultDocumentQuery(search = "", cursor: string | null = null): FileDocumentQuery {
  const needle = search.trim();
  return {
    logic: "and",
    filters: [
      { field: "status", operator: "eq", value: "active" },
      ...(needle ? [{ field: "displayName" as const, operator: "contains" as const, value: needle }] : []),
    ],
    sort: [{ field: "effectiveRevisionCreatedAt", direction: "desc" }],
    limit: 100,
    cursor,
  };
}

function normalizeCapabilities(values: readonly string[]): readonly DocumentCapability[] {
  const result = new Set<DocumentCapability>();
  for (const value of values) {
    if (
      value === "open" || value === "preview" || value === "reveal" ||
      value === "history" || value === "relink" || value === "dragOut" ||
      value === "unlink" || value === "diff"
    ) {
      result.add(value);
    } else if (value === "relocate") {
      result.add("relink");
    }
  }
  return [...result];
}
