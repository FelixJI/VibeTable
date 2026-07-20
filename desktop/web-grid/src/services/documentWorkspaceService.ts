import type { DocumentAuthority } from "@/stores/documentWorkspaceStore";
import type { DocumentCapability, DocumentEntry } from "@/stores/documentWorkspaceStore";
import { useDocumentWorkspaceStore } from "@/stores/documentWorkspaceStore";
import type {
  DocumentHistoryLoadedPayload,
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
  | { readonly type: "document.pickRequested"; readonly scope: DocumentWorkspaceScope }
  | { readonly type: "document.openRequested"; readonly entryHandle: string }
  | { readonly type: "document.previewRequested"; readonly entryHandle: string }
  | { readonly type: "document.revealRequested"; readonly entryHandle: string }
  | { readonly type: "document.historyRequested"; readonly entryHandle: string }
  | { readonly type: "document.relinkRequested"; readonly entryHandle: string };

export interface DocumentWorkspaceService {
  list(scope: DocumentWorkspaceScope, authority: DocumentAuthority): void;
  pick(scope: DocumentWorkspaceScope): void;
  open(entryHandle: string): void;
  preview(entryHandle: string): void;
  reveal(entryHandle: string): void;
  history(entryHandle: string): void;
  relink(entryHandle: string): void;
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
    pick: (scope) => dispatch({ type: "document.pickRequested", scope }),
    open: (entryHandle) => dispatch({ type: "document.openRequested", entryHandle }),
    preview: (entryHandle) => dispatch({ type: "document.previewRequested", entryHandle }),
    reveal: (entryHandle) => dispatch({ type: "document.revealRequested", entryHandle }),
    history: (entryHandle) => dispatch({ type: "document.historyRequested", entryHandle }),
    relink: (entryHandle) => dispatch({ type: "document.relinkRequested", entryHandle }),
  };
}

/** Bridge-backed integration used by WorkspaceView. */
export function useDocumentWorkspaceService(): {
  dispatch: (intent: DocumentWorkspaceIntent) => void;
} {
  const bridge = useHostBridge();
  const store = useDocumentWorkspaceStore();

  async function execute(intent: DocumentWorkspaceIntent): Promise<void> {
    try {
      switch (intent.type) {
        case "document.listRequested": {
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
        case "document.pickRequested":
          await bridge.request(intent.type, { scope: intent.scope });
          return;
        case "document.openRequested":
        case "document.previewRequested":
        case "document.revealRequested":
        case "document.relinkRequested":
          await bridge.request(intent.type, { entryHandle: intent.entryHandle });
          return;
      }
    } catch (error) {
      store.setFailed(error instanceof Error ? error.message : String(error));
    }
  }

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
    documentId: entry.documentId,
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
      value === "history" || value === "relink"
    ) {
      result.add(value);
    } else if (value === "relocate") {
      result.add("relink");
    }
  }
  return [...result];
}
