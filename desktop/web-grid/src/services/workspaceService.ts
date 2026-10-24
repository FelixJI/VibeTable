import { useHostBridge } from "./bridgeContext";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { usePluginStore } from "@/stores/pluginStore";
import { usePluginService } from "./pluginService";
import { collectionsFromChanged, collectionsFromOpened } from "./collectionsProjection";
import type { DatabaseOpenedPayload } from "@/contracts";

// The wire -> `CollectionSummary[]` projection for both `database.opened` and
// `database.collectionsChanged` lives in `collectionsProjection.ts`; this
// service is its only subscription owner (#508) and writes the single shared
// catalog in `workspaceStore` (#509).

/** Subscribe to inbound host events for the workspace. Call once at app boot. */
export function useWorkspaceService(): {
  init: () => void;
  dispose: () => void;
  openDatabase: () => Promise<DatabaseOpenOutcome>;
} {
  const bridge = useHostBridge();
  const store = useWorkspaceStore();
  const pluginStore = usePluginStore();
  const pluginService = usePluginService();
  const unsubscribe: Array<() => void> = [];
  let initialized = false;
  let disposed = false;
  let activeOpen: {
    readonly openId: string;
    readonly settle: (outcome: DatabaseOpenOutcome) => void;
  } | null = null;

  function init(): void {
    // bridge.on appends a Set entry per subscription, so a repeated init must
    // not re-subscribe; a disposed instance never re-subscribes either.
    if (initialized || disposed) return;
    initialized = true;
    unsubscribe.push(bridge.on("database.opened", (payload: DatabaseOpenedPayload) => {
      const terminal = takeOpenTerminal(payload.openId);
      if (!terminal) return;
      store.setOpened(collectionsFromOpened(payload), payload.displayNames);
      if (payload.projectKey?.trim()) {
        pluginService.openProjectContext(
          payload.projectKey.trim(),
          payload.projectRevision?.trim() || "",
        );
      }
      pluginStore.setHostContext(payload.currentUser, payload.hostVersion);
      if (pluginStore.projectContextReady) void pluginService.list().catch(() => undefined);
      terminal.settle?.("opened");
    }));
    unsubscribe.push(bridge.on("database.openCancelled", (payload) => {
      const terminal = takeOpenTerminal(payload.openId);
      if (!terminal) return;
      store.cancelOpen();
      terminal.settle?.("not-opened");
    }));
    unsubscribe.push(bridge.on("operation.failed", (payload) => {
      if (payload.operation !== "database.openRequested") return;
      const terminal = takeOpenTerminal(payload.operationId);
      if (!terminal) return;
      store.setFailed(payload.message);
      terminal.settle?.("not-opened");
    }));
    unsubscribe.push(bridge.on("plugin.projectContext.unavailable", () => {
      pluginService.openProjectContext("", "");
    }));
    unsubscribe.push(bridge.on("database.collectionsChanged", (payload) => {
      store.setCollections(
        collectionsFromChanged(payload),
        payload.displayNames,
      );
      if (payload.projectRevision?.trim()) {
        pluginService.updateProjectRevision(payload.projectRevision.trim());
      }
      void pluginService.list().catch(() => undefined);
    }));
  }

  function dispose(): void {
    if (disposed) return;
    disposed = true;
    for (const stop of unsubscribe.splice(0)) stop();
    // Retire the helper this instance owns so its in-flight plugin catalog
    // reads cannot write the shared plugin store after this owner is gone.
    pluginService.dispose();
    // Local retirement only finishes the local waiter: the host may still
    // complete (or have completed) the open remotely, which we cannot prove
    // or cancel from here.
    activeOpen?.settle("not-opened");
    activeOpen = null;
  }

  function openDatabase(): Promise<DatabaseOpenOutcome> {
    if (disposed) return Promise.resolve("not-opened");
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

  return { init, dispose, openDatabase };
}

export type DatabaseOpenOutcome = "opened" | "not-opened";
