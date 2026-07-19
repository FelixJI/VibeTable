import { useHostBridge } from "./bridgeContext";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import type { CollectionSummary } from "@/stores/workspaceStore";
import type { CollectionsChangedPayload, DatabaseOpenedPayload } from "@/contracts";

/**
 * Translate the wire-level `database.opened` payload (which carries separate
 * `tables`/`views` string lists) into the workspace's `CollectionSummary[]`
 * shape. The metadata bag is left empty here; capability metadata is layered
 * in later via `database.collectionsChanged`.
 */
function toCollections(payload: DatabaseOpenedPayload): readonly CollectionSummary[] {
  const displayNames = payload.displayNames ?? {};
  const tables = payload.tables.map((t) => ({
    collection: t,
    displayName: displayNames[t],
    metadata: {},
  }));
  const views = payload.views.map((v) => ({
    collection: v,
    displayName: displayNames[v],
    metadata: { kind: "view" } as const,
  }));
  return [...tables, ...views];
}

/**
 * Translate a `database.collectionsChanged` payload into `CollectionSummary[]`,
 * preserving capability hashes when the host includes them.
 */
function toCollectionsFromChanged(
  payload: CollectionsChangedPayload,
): readonly CollectionSummary[] {
  const hashes = payload.capabilityHashes ?? {};
  const displayNames = payload.displayNames ?? {};
  return payload.tables.map((t) => ({
    collection: t,
    displayName: displayNames[t],
    metadata:
      t in hashes ? { capabilityHash: hashes[t] } : {},
  }));
}

/** Subscribe to inbound host events for the workspace. Call once at app boot. */
export function useWorkspaceService(): {
  init: () => void;
  openDatabase: () => void;
} {
  const bridge = useHostBridge();
  const store = useWorkspaceStore();

  function init(): void {
    bridge.on("database.opened", (payload: DatabaseOpenedPayload) => {
      store.setOpened(toCollections(payload), payload.displayNames ?? {});
    });
    bridge.on("database.collectionsChanged", (payload) => {
      store.setCollections(
        toCollectionsFromChanged(payload),
        payload.displayNames ?? {},
      );
    });
  }

  function openDatabase(): void {
    store.beginOpen();
    bridge.notify("database.openRequested", { path: "" });
  }

  return { init, openDatabase };
}
