import type { DocumentAuthority } from "@/stores/documentWorkspaceStore";
import type { DocumentCapability, DocumentEntry } from "@/stores/documentWorkspaceStore";
import { useDocumentWorkspaceStore } from "@/stores/documentWorkspaceStore";
import type {
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
  | { readonly type: "document.revealRequested"; readonly entryHandle: string }
  | { readonly type: "document.relinkRequested"; readonly handle: string };

export interface DocumentWorkspaceService {
  list(scope: DocumentWorkspaceScope, authority: DocumentAuthority): void;
  importFiles(scope: DocumentWorkspaceScope): void;
  externalDrop(scope: DocumentWorkspaceScope, files: readonly File[]): void;
  dragOut(handle: string): void;
  open(entryHandle: string): void;
  preview(entryHandle: string): void;
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
  return {
    list: (scope, authority) => dispatch({ type: "document.listRequested", scope, authority }),
    importFiles: (scope) => dispatch({ type: "document.importRequested", scope }),
    externalDrop: (scope, files) => dispatch({ type: "document.externalDropRequested", scope, files }),
    dragOut: (handle) => dispatch({ type: "document.dragOutRequested", handle }),
    open: (entryHandle) => dispatch({ type: "document.openRequested", entryHandle }),
    preview: (entryHandle) => dispatch({ type: "document.previewRequested", entryHandle }),
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
      value === "unlink"
    ) {
      result.add(value);
    } else if (value === "relocate") {
      result.add("relink");
    }
  }
  return [...result];
}
