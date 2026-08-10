import type { DocumentAuthority } from "@/stores/documentWorkspaceStore";
import type { DocumentCapability, DocumentEntry } from "@/stores/documentWorkspaceStore";
import { useDocumentWorkspaceStore } from "@/stores/documentWorkspaceStore";
import type {
  DocumentDiffCompletedPayload,
  DocumentListLoadedPayload,
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
  | { readonly type: "document.listRequested"; readonly scope: DocumentWorkspaceScope; readonly authority: DocumentAuthority }
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
  list(scope: DocumentWorkspaceScope, authority: DocumentAuthority): void;
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
    list: (scope, authority) => dispatch({ type: "document.listRequested", scope, authority }),
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

  async function execute(intent: DocumentWorkspaceIntent): Promise<void> {
    try {
      switch (intent.type) {
        case "document.listRequested": {
          lastScope = intent.scope;
          store.beginLoad();
          const payload = await bridge.request(intent.type, {
            scope: intent.scope,
            authority: intent.authority,
          }) as DocumentListLoadedPayload;
          store.setEntries(payload.entries.map((entry) => toStoreEntry(entry, intent.authority)));
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
            store.completeDiff(generation, parseDocumentDiffResult(raw));
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
    mimeType: entry.mimeType ?? undefined,
    versionLabel: entry.currentRevision ?? undefined,
    effectiveRevisionId: entry.effectiveRevisionId ?? undefined,
    capabilities: normalizeCapabilities(entry.capabilities),
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

const canonicalUuid =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

function parseDocumentDiffResult(value: unknown): DocumentDiffCompletedPayload {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error("document.diffCompleted returned an invalid result");
  }
  const source = value as Record<string, unknown>;
  const keys = Object.keys(source).sort();
  const expected = [
    "addedLines",
    "effectiveRevisionId",
    "entryHandle",
    "failure",
    "historicalRevisionId",
    "outcome",
    "removedLines",
  ].sort();
  const outcomes = ["identical", "changed", "changedWithDetails", "failure"];
  const failures = ["unsupported", "invalidContent", "io", "cancelled", "stale", null];
  if (JSON.stringify(keys) !== JSON.stringify(expected) ||
    typeof source.entryHandle !== "string" || !source.entryHandle ||
    typeof source.historicalRevisionId !== "string" ||
    !canonicalUuid.test(source.historicalRevisionId) ||
    typeof source.effectiveRevisionId !== "string" ||
    !canonicalUuid.test(source.effectiveRevisionId) ||
    typeof source.outcome !== "string" || !outcomes.includes(source.outcome) ||
    !failures.includes(source.failure as string | null) ||
    !(source.addedLines === null ||
      (typeof source.addedLines === "number" && Number.isInteger(source.addedLines)
        && source.addedLines >= 0)) ||
    !(source.removedLines === null ||
      (typeof source.removedLines === "number" && Number.isInteger(source.removedLines)
        && source.removedLines >= 0))) {
    throw new Error("document.diffCompleted returned an invalid result");
  }
  return source as unknown as DocumentDiffCompletedPayload;
}
