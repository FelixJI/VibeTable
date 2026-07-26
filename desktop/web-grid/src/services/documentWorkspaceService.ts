import type { DocumentAuthority } from "@/stores/documentWorkspaceStore";
import type { DocumentCapability, DocumentEntry } from "@/stores/documentWorkspaceStore";
import { useDocumentWorkspaceStore } from "@/stores/documentWorkspaceStore";
import type {
  DocumentHistoryLoadedPayload,
  DocumentListLoadedPayload,
  DocumentSchemeListResultPayload,
  DocumentVersionOperationResultPayload,
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
  | { readonly type: "document.historyRequested"; readonly entryHandle: string }
  | { readonly type: "document.relinkRequested"; readonly handle: string }
  | { readonly type: "document.commitRevisionRequested"; readonly entryHandle: string; readonly note?: string; readonly schemeHandle?: string | null }
  | { readonly type: "document.promoteVersionRequested"; readonly entryHandle: string; readonly versionLabel: string; readonly note?: string; readonly schemeHandle?: string | null }
  | { readonly type: "document.revisionPreviewRequested"; readonly entryHandle: string; readonly revisionHandle: string }
  | { readonly type: "document.revisionRestoreRequested"; readonly entryHandle: string; readonly revisionHandle: string }
  | { readonly type: "document.schemeListRequested"; readonly entryHandle: string }
  | { readonly type: "document.schemeCreateRequested"; readonly entryHandle: string; readonly name: string; readonly baseRevisionHandle?: string | null }
  | { readonly type: "document.schemeRenameRequested"; readonly entryHandle: string; readonly schemeHandle: string; readonly name: string }
  | { readonly type: "document.schemeArchiveRequested"; readonly entryHandle: string; readonly schemeHandle: string };

export interface DocumentWorkspaceService {
  list(scope: DocumentWorkspaceScope, authority: DocumentAuthority): void;
  importFiles(scope: DocumentWorkspaceScope): void;
  externalDrop(scope: DocumentWorkspaceScope, files: readonly File[]): void;
  dragOut(handle: string): void;
  open(entryHandle: string): void;
  preview(entryHandle: string): void;
  reveal(entryHandle: string): void;
  history(entryHandle: string): void;
  relink(handle: string): void;
  commitRevision(entryHandle: string, note?: string, schemeHandle?: string | null): void;
  promoteVersion(entryHandle: string, versionLabel: string, note?: string, schemeHandle?: string | null): void;
  previewRevision(entryHandle: string, revisionHandle: string): void;
  restoreRevision(entryHandle: string, revisionHandle: string): void;
  listSchemes(entryHandle: string): void;
  createScheme(entryHandle: string, name: string, baseRevisionHandle?: string | null): void;
  renameScheme(entryHandle: string, schemeHandle: string, name: string): void;
  archiveScheme(entryHandle: string, schemeHandle: string): void;
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
    history: (entryHandle) => dispatch({ type: "document.historyRequested", entryHandle }),
    relink: (handle) => dispatch({ type: "document.relinkRequested", handle }),
    commitRevision: (entryHandle, note, schemeHandle) =>
      dispatch({ type: "document.commitRevisionRequested", entryHandle, note, schemeHandle }),
    promoteVersion: (entryHandle, versionLabel, note, schemeHandle) =>
      dispatch({ type: "document.promoteVersionRequested", entryHandle, versionLabel, note, schemeHandle }),
    previewRevision: (entryHandle, revisionHandle) =>
      dispatch({ type: "document.revisionPreviewRequested", entryHandle, revisionHandle }),
    restoreRevision: (entryHandle, revisionHandle) =>
      dispatch({ type: "document.revisionRestoreRequested", entryHandle, revisionHandle }),
    listSchemes: (entryHandle) => dispatch({ type: "document.schemeListRequested", entryHandle }),
    createScheme: (entryHandle, name, baseRevisionHandle) =>
      dispatch({ type: "document.schemeCreateRequested", entryHandle, name, baseRevisionHandle }),
    renameScheme: (entryHandle, schemeHandle, name) =>
      dispatch({ type: "document.schemeRenameRequested", entryHandle, schemeHandle, name }),
    archiveScheme: (entryHandle, schemeHandle) =>
      dispatch({ type: "document.schemeArchiveRequested", entryHandle, schemeHandle }),
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
        case "document.historyRequested": {
          const payload = await bridge.request(intent.type, {
            entryHandle: intent.entryHandle,
            limit: 50,
            offset: 0,
          }) as DocumentHistoryLoadedPayload;
          store.setHistory(payload.entryHandle, payload.revisions.map((revision) => ({
            revisionHandle: revision.revisionHandle,
            label: revision.label,
            createdAt: revision.createdAt,
            size: revision.size,
            author: revision.author ?? undefined,
          })));
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
        case "document.revisionPreviewRequested":
          await bridge.request(intent.type, {
            entryHandle: intent.entryHandle,
            revisionHandle: intent.revisionHandle,
          });
          return;
        case "document.schemeListRequested": {
          store.beginSchemes(intent.entryHandle);
          const payload = await bridge.request(intent.type, {
            entryHandle: intent.entryHandle,
          }) as DocumentSchemeListResultPayload;
          store.setSchemes(payload.entryHandle, payload.schemes);
          return;
        }
        case "document.commitRevisionRequested":
        case "document.promoteVersionRequested":
        case "document.revisionRestoreRequested": {
          store.beginOperation(intent.type);
          const payload = await bridge.request(intent.type, intent) as DocumentVersionOperationResultPayload;
          store.updateCurrentRevision(payload.entryHandle, payload.currentRevision);
          store.finishOperation();
          await execute({
            type: "document.historyRequested",
            entryHandle: payload.entryHandle,
          });
          if (payload.schemeHandle) {
            await execute({
              type: "document.schemeListRequested",
              entryHandle: payload.entryHandle,
            });
          }
          return;
        }
        case "document.schemeCreateRequested":
        case "document.schemeRenameRequested":
        case "document.schemeArchiveRequested":
          store.beginOperation(intent.type);
          await bridge.request(intent.type, intent);
          store.finishOperation();
          await execute({
            type: "document.schemeListRequested",
            entryHandle: intent.entryHandle,
          });
          return;
      }
    } catch (error) {
      store.finishOperation();
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
    store.finishOperation();
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
  return {
    entryHandle: entry.entryHandle,
    displayName: entry.displayName,
    authority,
    availability: entry.availability,
    mimeType: entry.mimeType ?? undefined,
    versionLabel: entry.currentRevision ?? undefined,
    capabilities: normalizeCapabilities(entry.capabilities),
  };
}

function normalizeCapabilities(values: readonly string[]): readonly DocumentCapability[] {
  const result = new Set<DocumentCapability>();
  for (const value of values) {
    if (
      value === "open" || value === "preview" || value === "reveal" ||
      value === "history" || value === "relink" || value === "dragOut" ||
      value === "commitRevision" || value === "promoteVersion" || value === "schemes"
    ) {
      result.add(value);
    } else if (value === "relocate") {
      result.add("relink");
    }
  }
  return [...result];
}
