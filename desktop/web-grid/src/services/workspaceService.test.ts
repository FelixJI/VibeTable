import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useWorkspaceService } from "./workspaceService";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { usePluginStore } from "@/stores/pluginStore";

function makeShimBridge(): {
  bridge: HostBridge;
  emit: (type: string, payload: unknown) => void;
  posted: unknown[];
} {
  let listener: ((event: { data: unknown }) => void) | null = null;
  const posted: unknown[] = [];
  const shim = {
    addEventListener: (_: string, fn: (event: { data: unknown }) => void) => { listener = fn; },
    removeEventListener: () => {},
    postMessage: (message: unknown) => { posted.push(message); },
  };
  const bridge = createHostBridge({ webview: shim });
  bridge.start();
  return {
    bridge,
    posted,
    emit: (type, payload) => listener?.({ data: JSON.stringify({ type, payload }) }),
  };
}

describe("workspaceService display names", () => {
  beforeEach(() => setActivePinia(createPinia()));
  afterEach(() => setHostBridgeForTesting(null));

  it("keeps fresh base and view metadata when recovery replaces the catalog", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceService().init();
    emit("database.opened", {
      tables: ["removed"], views: ["old_view"], displayNames: {},
    });
    emit("database.collectionsChanged", {
      tables: ["orders"], views: ["recent_orders"],
      displayNames: { orders: "订单", recent_orders: "近期订单" },
      capabilityHashes: { recent_orders: "view-capability" },
    });
    const store = useWorkspaceStore();
    expect(store.collections).toEqual([
      { collection: "orders", metadata: {} },
      { collection: "recent_orders", metadata: { kind: "view", capabilityHash: "view-capability" } },
    ]);
    expect(store.displayNames.recent_orders).toBe("近期订单");
  });

  it("projects database.opened displayNames without changing physical keys", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceService().init();
    emit("database.opened", {
      tables: ["vt_t_01"],
      views: [],
      displayNames: { vt_t_01: "客户清单" },
    });
    const store = useWorkspaceStore();
    expect(store.displayNames.vt_t_01).toBe("客户清单");
    expect(store.collections[0]).toMatchObject({
      collection: "vt_t_01",
    });
    expect(store.collections[0]).not.toHaveProperty("displayName");
  });

  it("replaces labels on collectionsChanged", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceService().init();
    emit("database.collectionsChanged", {
      tables: ["vt_t_02"],
      displayNames: { vt_t_02: "订单" },
    });
    const store = useWorkspaceStore();
    expect(store.displayNames).toEqual({ vt_t_02: "订单" });
    expect(store.collections[0].collection).toBe("vt_t_02");
  });

  it("switches plugin isolation to the opened product project", () => {
    const { bridge, emit, posted } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceService().init();

    emit("database.opened", {
      tables: ["orders"],
      views: [],
      displayNames: { orders: "Orders" },
      projectKey: "local:workspace-a",
      projectRevision: "workspace-r7",
      currentUser: { id: "user-7", displayName: "Alice" },
      hostVersion: "2.4.1",
    });

    expect(usePluginStore().projectKey).toBe("local:workspace-a");
    expect(usePluginStore().projectRevision).toBe("workspace-r7");
    expect(usePluginStore().currentUser).toMatchObject({ id: "user-7", displayName: "Alice" });
    expect(usePluginStore().hostVersion).toBe("2.4.1");
    expect(posted.at(-1)).toMatchObject({
      type: "plugin.catalog.list",
      payload: { projectKey: "local:workspace-a" },
    });
  });

  it("advances the plugin session generation on every database.opened event", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceService().init();
    const payload = {
      tables: [], views: [], displayNames: {},
      projectKey: "local:workspace-a", projectRevision: "workspace-r7",
    };

    emit("database.opened", payload);
    const firstGeneration = usePluginStore().projectContextGeneration;
    emit("database.opened", payload);

    expect(usePluginStore().projectContextGeneration).toBe(firstGeneration + 1);
  });

  it("does not reinterpret an incomplete database.opened as a context close", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceService().init();
    usePluginStore().setProjectContext("local:workspace-a", "workspace-r7");

    emit("database.opened", { tables: [], views: [], displayNames: {} });

    expect(usePluginStore().projectContextReady).toBe(true);
    expect(usePluginStore().projectKey).toBe("local:workspace-a");
  });

  it("closes plugin readiness only on the explicit host context event", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceService().init();
    usePluginStore().setProjectContext("local:workspace-a", "workspace-r7");

    emit("plugin.projectContext.unavailable", { reason: "workspace-session-unavailable" });

    expect(usePluginStore().projectContextReady).toBe(false);
    expect(usePluginStore().projectKey).toBe("");
  });

  it("restores opening state on picker cancellation without changing plugin context", () => {
    const { bridge, emit, posted } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const service = useWorkspaceService();
    service.init();
    const workspace = useWorkspaceStore();
    const pluginStore = usePluginStore();
    pluginStore.setProjectContext("local:workspace-a", "workspace-r7");
    workspace.setOpened([], {});
    service.openDatabase();
    const openId = (posted.at(-1) as {
      payload: { openId: string };
    }).payload.openId;
    pluginStore.setProjectContext("local:workspace-b", "workspace-r8");

    emit("database.openCancelled", { openId, reason: "project-context-changed" });

    expect(workspace.phase).toBe("opened");
    expect(pluginStore.projectContextReady).toBe(true);
    expect(pluginStore.projectKey).toBe("local:workspace-b");
    expect(pluginStore.projectRevision).toBe("workspace-r8");
  });

  it("settles each matching renderer open after projecting its terminal state", async () => {
    const { bridge, emit, posted } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const service = useWorkspaceService();
    service.init();
    const workspace = useWorkspaceStore();

    const cancelled = service.openDatabase();
    const cancelledId = (posted.at(-1) as { payload: { openId: string } }).payload.openId;
    emit("database.openCancelled", { openId: cancelledId, reason: "picker-cancelled" });
    await expect(cancelled).resolves.toBe("not-opened");
    expect(workspace.phase).toBe("idle");

    const failed = service.openDatabase();
    const failedId = (posted.at(-1) as { payload: { openId: string } }).payload.openId;
    emit("operation.failed", {
      operation: "database.openRequested",
      operationId: failedId,
      code: "WORKSPACE_ERROR",
      message: "Workspace operation failed.",
    });
    await expect(failed).resolves.toBe("not-opened");
    expect(workspace.phase).toBe("failed");

    const opened = service.openDatabase();
    const openedId = (posted.at(-1) as { payload: { openId: string } }).payload.openId;
    emit("database.opened", {
      openId: openedId,
      tables: ["orders"],
      views: [],
      displayNames: { orders: "Orders" },
    });
    await expect(opened).resolves.toBe("opened");
    expect(workspace.phase).toBe("opened");
    expect(workspace.collections.map(item => item.collection)).toEqual(["orders"]);
  });

  it("settles a superseded open without letting its late terminals finish the replacement", async () => {
    const { bridge, emit, posted } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const service = useWorkspaceService();
    service.init();

    const first = service.openDatabase();
    const firstId = (posted.at(-1) as { payload: { openId: string } }).payload.openId;
    const second = service.openDatabase();
    const secondId = (posted.at(-1) as { payload: { openId: string } }).payload.openId;
    await expect(first).resolves.toBe("not-opened");
    let secondOutcome: string | null = null;
    void second.then((outcome) => { secondOutcome = outcome; });

    emit("database.opened", {
      openId: firstId,
      tables: ["stale_records"],
      views: [],
      displayNames: { stale_records: "Stale" },
    });
    emit("database.openCancelled", { openId: firstId, reason: "late-stale-terminal" });
    await Promise.resolve();
    expect(secondOutcome).toBeNull();
    expect(useWorkspaceStore().phase).toBe("opening");

    emit("database.opened", {
      openId: secondId,
      tables: ["current_records"],
      views: [],
      displayNames: { current_records: "Current" },
    });
    await expect(second).resolves.toBe("opened");
    expect(secondOutcome).toBe("opened");
    expect(useWorkspaceStore().collections.map(item => item.collection)).toEqual([
      "current_records",
    ]);
  });

  it("lets only the latest overlapping open terminal change renderer state", () => {
    const { bridge, emit, posted } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const service = useWorkspaceService();
    service.init();
    const workspace = useWorkspaceStore();
    workspace.setOpened([], {});

    service.openDatabase();
    service.openDatabase();
    const requests = posted as Array<{ payload: { openId: string } }>;
    const firstOpenId = requests.at(-2)!.payload.openId;
    const secondOpenId = requests.at(-1)!.payload.openId;
    emit("database.openCancelled", {
      openId: firstOpenId,
      reason: "superseded",
    });
    emit("database.opened", {
      openId: firstOpenId,
      tables: ["stale_records"],
      views: [],
      displayNames: { stale_records: "Stale" },
      projectKey: "local:stale",
      projectRevision: "stale:1",
    });

    expect(workspace.phase).toBe("opening");
    expect(workspace.collections).toEqual([]);

    emit("database.opened", {
      openId: secondOpenId,
      tables: ["current_records"],
      views: [],
      displayNames: { current_records: "Current" },
      projectKey: "local:current",
      projectRevision: "current:2",
    });
    emit("database.openCancelled", {
      openId: firstOpenId,
      reason: "late-stale-terminal",
    });

    expect(workspace.phase).toBe("opened");
    expect(workspace.collections.map((item) => item.collection)).toEqual(["current_records"]);
    expect(usePluginStore().projectKey).toBe("local:current");
  });

  it("isolates a rebuilt renderer open from an old renderer terminal", () => {
    const firstBridge = makeShimBridge();
    setHostBridgeForTesting(firstBridge.bridge);
    const firstService = useWorkspaceService();
    firstService.init();
    firstService.openDatabase();
    const firstOpenId = (firstBridge.posted.at(-1) as {
      payload: { openId: string };
    }).payload.openId;

    const replacementBridge = makeShimBridge();
    setHostBridgeForTesting(replacementBridge.bridge);
    const replacementService = useWorkspaceService();
    replacementService.init();
    replacementService.openDatabase();
    const replacementOpenId = (replacementBridge.posted.at(-1) as {
      payload: { openId: string };
    }).payload.openId;

    expect(firstOpenId).toMatch(/^database-open:[0-9a-f-]{36}$/u);
    expect(replacementOpenId).not.toBe(firstOpenId);

    replacementBridge.emit("database.openCancelled", {
      openId: firstOpenId,
      reason: "old-renderer-retired",
    });
    expect(useWorkspaceStore().phase).toBe("opening");

    replacementBridge.emit("database.opened", {
      openId: replacementOpenId,
      tables: ["current_records"],
      views: [],
      displayNames: { current_records: "Current" },
      projectKey: "local:current",
      projectRevision: "current:2",
    });
    expect(useWorkspaceStore().phase).toBe("opened");
  });

  it("finishes only the matching open on the stable host operation failure", () => {
    const { bridge, emit, posted } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const service = useWorkspaceService();
    service.init();
    const workspace = useWorkspaceStore();
    service.openDatabase();
    const openId = (posted.at(-1) as {
      payload: { openId: string };
    }).payload.openId;

    emit("operation.failed", {
      operation: "database.openRequested",
      operationId: "stale-open",
      code: "WORKSPACE_ERROR",
      message: "Workspace operation failed.",
    });

    expect(workspace.phase).toBe("opening");

    emit("operation.failed", {
      operation: "database.openRequested",
      operationId: openId,
      code: "WORKSPACE_ERROR",
      message: "Workspace operation failed.",
    });

    expect(workspace.phase).toBe("failed");
    expect(workspace.lastError).toBe("Workspace operation failed.");
  });

  it("advances plugin project revision when product collections change", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceService().init();
    const pluginStore = usePluginStore();
    pluginStore.setProjectContext("local:workspace-a", "workspace-r7");

    emit("database.collectionsChanged", {
      tables: ["orders"],
      displayNames: { orders: "Orders" },
      projectRevision: "workspace-r8",
    });

    expect(pluginStore.projectRevision).toBe("workspace-r8");
  });
});

describe("workspaceService lifecycle ownership", () => {
  beforeEach(() => setActivePinia(createPinia()));
  afterEach(() => setHostBridgeForTesting(null));

  it("does not duplicate broadcast subscriptions when init runs twice", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const service = useWorkspaceService();
    service.init();
    service.init();
    const workspace = useWorkspaceStore();
    const setCollections = vi.spyOn(workspace, "setCollections");

    emit("database.collectionsChanged", {
      tables: ["orders"],
      displayNames: { orders: "Orders" },
    });

    expect(setCollections).toHaveBeenCalledTimes(1);
    expect(workspace.collections.map((item) => item.collection)).toEqual(["orders"]);
  });

  it("stops projecting host broadcasts after dispose (idempotent, non-revivable)", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const service = useWorkspaceService();
    service.init();
    service.dispose();
    service.dispose();
    service.init();
    const workspace = useWorkspaceStore();
    const pluginStore = usePluginStore();
    pluginStore.setProjectContext("local:workspace-a", "workspace-r7");

    emit("database.opened", { tables: ["late"], views: [], displayNames: { late: "Late" } });
    emit("database.collectionsChanged", { tables: ["late"], displayNames: { late: "Late" } });
    emit("database.openCancelled", { openId: "irrelevant", reason: "retired" });
    emit("operation.failed", {
      operation: "database.openRequested",
      operationId: "irrelevant",
      code: "WORKSPACE_ERROR",
      message: "Workspace operation failed.",
    });
    emit("plugin.projectContext.unavailable", { reason: "retired" });

    expect(workspace.phase).toBe("idle");
    expect(workspace.collections).toEqual([]);
    expect(workspace.displayNames).toEqual({});
    // The retired instance's plugin-context listener is released too.
    expect(pluginStore.projectContextReady).toBe(true);
    expect(pluginStore.projectKey).toBe("local:workspace-a");
  });

  it("retires a pending renderer open locally without claiming the host cancelled it", async () => {
    const { bridge, emit, posted } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const service = useWorkspaceService();
    service.init();
    const workspace = useWorkspaceStore();

    const pending = service.openDatabase();
    const openId = (posted.at(-1) as { payload: { openId: string } }).payload.openId;
    service.dispose();

    // Local retirement only finishes the waiter; it never asserts the host
    // skipped an open that may already have been performed remotely.
    await expect(pending).resolves.toBe("not-opened");
    expect(workspace.phase).toBe("opening");

    emit("database.opened", {
      openId,
      tables: ["late"],
      views: [],
      displayNames: { late: "Late" },
    });

    expect(workspace.collections).toEqual([]);
    expect(workspace.phase).toBe("opening");
  });

  it("refuses new opens from a disposed instance and lets the replacement own the bridge", async () => {
    const { bridge, emit, posted } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    const dead = useWorkspaceService();
    dead.init();
    dead.dispose();

    await expect(dead.openDatabase()).resolves.toBe("not-opened");
    expect(posted.filter((message) => (message as { type?: string }).type === "database.openRequested")).toEqual([]);

    const live = useWorkspaceService();
    live.init();
    const setCollections = vi.spyOn(workspace, "setCollections");
    emit("database.collectionsChanged", {
      tables: ["orders"],
      displayNames: { orders: "Orders" },
    });

    expect(setCollections).toHaveBeenCalledTimes(1);
    expect(workspace.collections.map((item) => item.collection)).toEqual(["orders"]);
  });

  it("retires its private plugin catalog reads on dispose; a replacement owner refreshes normally", async () => {
    // A local shim that can answer correlated plugin.catalog.list requests
    // after an arbitrary delay (makeShimBridge.emit cannot carry requestId).
    const posted: Array<{ type: string; requestId: string; payload?: unknown }> = [];
    let listener: ((event: { data: unknown }) => void) | null = null;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `ws-plugin-r${++sequence}`,
      webview: {
        addEventListener: (_: string, fn: (event: { data: unknown }) => void) => { listener = fn; },
        removeEventListener: () => { listener = null; },
        postMessage: (message: unknown) => { posted.push(message as { type: string; requestId: string }); },
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const emitHost = (type: string, payload: Record<string, unknown>) =>
      listener?.({ data: JSON.stringify({ type, payload }) });
    const replyPluginList = (requestId: string, plugins: Array<{ pluginId: string; revision: number }>) =>
      listener?.({ data: JSON.stringify({
        type: "plugin.catalog.list", requestId, payload: plugins,
      }) });
    const failPluginList = (requestId: string) =>
      listener?.({ data: JSON.stringify({
        type: "operation.failed", requestId,
        payload: { message: "host list failed", code: "X" },
      }) });
    const pluginStore = usePluginStore();

    const service = useWorkspaceService();
    service.init();
    emitHost("database.opened", {
      tables: [], views: [], displayNames: {},
      projectKey: "local:ws-a", projectRevision: "ws-r1",
    });
    emitHost("database.collectionsChanged", { tables: [], displayNames: {} });
    const reads = posted.filter((message) => message.type === "plugin.catalog.list");
    expect(reads).toHaveLength(2);

    service.dispose();
    // The retired owner's late success and failure must not write the shared
    // plugin store a replacement owner now consumes.
    replyPluginList(reads[0]!.requestId, [{ pluginId: "com.acme.retired", revision: 2 }]);
    failPluginList(reads[1]!.requestId);
    for (let i = 0; i < 8; i += 1) await Promise.resolve();
    expect(pluginStore.plugins).toEqual([]);
    expect(pluginStore.lastError).toBeNull();

    const live = useWorkspaceService();
    live.init();
    emitHost("database.opened", {
      tables: [], views: [], displayNames: {},
      projectKey: "local:ws-b", projectRevision: "ws-r2",
    });
    const liveRead = posted.filter((message) => message.type === "plugin.catalog.list").at(-1)!;
    replyPluginList(liveRead.requestId, [{ pluginId: "com.acme.live", revision: 1 }]);
    for (let i = 0; i < 8; i += 1) await Promise.resolve();
    expect(pluginStore.plugins.map((plugin) => plugin.pluginId)).toEqual(["com.acme.live"]);
  });
});
