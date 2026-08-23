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
  openDatabase: () => Promise<DatabaseOpenOutcome>;
} {
  const bridge = useHostBridge();
  const store = useWorkspaceStore();
  const pluginStore = usePluginStore();
  const pluginService = usePluginService();
  let activeOpen: {
    readonly openId: string;
    readonly settle: (outcome: DatabaseOpenOutcome) => void;
  } | null = null;

  function init(): void {
    bridge.on("database.opened", (payload: DatabaseOpenedPayload) => {
      const terminal = takeOpenTerminal(payload.openId);
      if (!terminal) return;
      store.setOpened(toCollections(payload), payload.displayNames);
      if (payload.projectKey?.trim()) {
        pluginService.openProjectContext(
          payload.projectKey.trim(),
          payload.projectRevision?.trim() || "",
        );
      }
      pluginStore.setHostContext(payload.currentUser, payload.hostVersion);
      if (pluginStore.projectContextReady) void pluginService.list().catch(() => undefined);
      terminal.settle?.("opened");
    });
    bridge.on("database.openCancelled", (payload) => {
      const terminal = takeOpenTerminal(payload.openId);
      if (!terminal) return;
      store.cancelOpen();
      terminal.settle?.("not-opened");
    });
    bridge.on("operation.failed", (payload) => {
      if (payload.operation !== "database.openRequested") return;
      const terminal = takeOpenTerminal(payload.operationId);
      if (!terminal) return;
      store.setFailed(payload.message);
      terminal.settle?.("not-opened");
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

  function openDatabase(): Promise<DatabaseOpenOutcome> {
    activeOpen?.settle("not-opened");
    const openId = `database-open:${globalThis.crypto.randomUUID()}`;
    const outcome = new Promise<DatabaseOpenOutcome>((settle) => {
      activeOpen = { openId, settle };
    });
    store.beginOpen();
    bridge.notify("database.openRequested", { path: "", openId });
    return outcome;
  }

  function takeOpenTerminal(openId: string | undefined): {
    readonly settle: ((outcome: DatabaseOpenOutcome) => void) | null;
  } | null {
    if (activeOpen === null) return openId === undefined ? { settle: null } : null;
    if (openId !== activeOpen.openId) return null;
    const terminal = activeOpen;
    activeOpen = null;
    return { settle: terminal.settle };
  }

  return { init, openDatabase };
}

export type DatabaseOpenOutcome = "opened" | "not-opened";
