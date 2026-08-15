import { useHostBridge } from "./bridgeContext";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { usePluginStore } from "@/stores/pluginStore";
import { usePluginService } from "./pluginService";
import type { CollectionSummary } from "@/stores/workspaceStore";
import type { CollectionsChangedPayload, DatabaseOpenedPayload } from "@/contracts";

/**
 * Translate the wire-level `database.opened` payload (which carries separate
 * `tables`/`views` string lists) into the workspace's `CollectionSummary[]`
 * shape. The metadata bag is left empty here; capability metadata is layered
 * in later via `database.collectionsChanged`.
 */
function toCollections(payload: DatabaseOpenedPayload): readonly CollectionSummary[] {
  const tables = payload.tables.map((t) => ({
    collection: t,
    metadata: {},
  }));
  const views = payload.views.map((v) => ({
    collection: v,
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
  return payload.tables.map((t) => ({
    collection: t,
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
  const pluginStore = usePluginStore();
  const pluginService = usePluginService();

  function init(): void {
    bridge.on("database.opened", (payload: DatabaseOpenedPayload) => {
      store.setOpened(toCollections(payload), payload.displayNames);
      if (payload.projectKey?.trim()) {
        pluginStore.setProjectContext(
          payload.projectKey.trim(),
          payload.projectRevision?.trim() || "0",
        );
      }
      pluginStore.setHostContext(payload.currentUser, payload.hostVersion);
      void pluginService.list().catch(() => undefined);
    });
    bridge.on("database.collectionsChanged", (payload) => {
      store.setCollections(
        toCollectionsFromChanged(payload),
        payload.displayNames,
      );
      if (payload.projectRevision?.trim()) {
        pluginStore.setProjectContext(
          pluginStore.projectKey,
          payload.projectRevision.trim(),
        );
      }
      void pluginService.list().catch(() => undefined);
    });
  }

  function openDatabase(): void {
    store.beginOpen();
    bridge.notify("database.openRequested", { path: "" });
  }

  return { init, openDatabase };
}
