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
  let activeOpenId: string | null = null;

  function init(): void {
    bridge.on("database.opened", (payload: DatabaseOpenedPayload) => {
      if (!acceptOpenTerminal(payload.openId)) return;
      store.setOpened(toCollections(payload), payload.displayNames);
      if (payload.projectKey?.trim()) {
        pluginService.openProjectContext(
          payload.projectKey.trim(),
          payload.projectRevision?.trim() || "",
        );
      }
      pluginStore.setHostContext(payload.currentUser, payload.hostVersion);
      if (pluginStore.projectContextReady) void pluginService.list().catch(() => undefined);
    });
    bridge.on("database.openCancelled", (payload) => {
      if (!acceptOpenTerminal(payload.openId)) return;
      store.cancelOpen();
    });
    bridge.on("operation.failed", (payload) => {
      if (payload.operation === "database.openRequested"
        && acceptOpenTerminal(payload.operationId)) {
        store.setFailed(payload.message);
      }
    });
    bridge.on("plugin.projectContext.unavailable", () => {
      pluginService.openProjectContext("", "");
    });
    bridge.on("database.collectionsChanged", (payload) => {
      store.setCollections(
        toCollectionsFromChanged(payload),
        payload.displayNames,
      );
      if (payload.projectRevision?.trim()) {
        pluginService.updateProjectRevision(payload.projectRevision.trim());
      }
      void pluginService.list().catch(() => undefined);
    });
  }

  function openDatabase(): void {
    const openId = `database-open:${globalThis.crypto.randomUUID()}`;
    activeOpenId = openId;
    store.beginOpen();
    bridge.notify("database.openRequested", { path: "", openId });
  }

  function acceptOpenTerminal(openId: string | undefined): boolean {
    if (activeOpenId === null) return openId === undefined;
    if (openId !== activeOpenId) return false;
    activeOpenId = null;
    return true;
  }

  return { init, openDatabase };
}
