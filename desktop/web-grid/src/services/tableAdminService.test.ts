import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge } from "@/bridge/hostBridge";
import type { HostBridge } from "@/bridge/hostBridge";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useUiStore } from "@/stores/uiStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useTableAdminService } from "./tableAdminService";

function deferred<T>(): {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (error: Error) => void;
} {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function bridgeHarness(): {
  bridge: HostBridge;
  notify: ReturnType<typeof vi.fn>;
  request: ReturnType<typeof vi.fn>;
  emit: (type: string, payload: unknown) => void;
} {
  // Mirror hostBridge's real Set semantics: multiple legal consumers may
  // subscribe to the same event type, and every subscription is dispatched.
  // A Map keyed by type would silently mask duplicate subscriptions.
  const handlers = new Map<string, Set<(payload: any) => void>>();
  const notify = vi.fn();
  const request = vi.fn(async () => ({
    tables: ["tbl_opaque"],
    displayNames: { tbl_opaque: "订单" },
    createdTableId: "tbl_opaque",
    deletedTableId: "tbl_orders",
  }));
  return {
    bridge: {
      notify,
      request,
      on: vi.fn((type: string, handler: (payload: any) => void) => {
        let set = handlers.get(type);
        if (!set) {
          set = new Set();
          handlers.set(type, set);
        }
        set.add(handler);
        return () => { set.delete(handler); };
      }),
    } as unknown as HostBridge,
    notify,
    request,
    emit: (type, payload) => {
      for (const handler of handlers.get(type) ?? []) handler(payload);
    },
  };
}

describe("tableAdminService Schema v2 bootstrap", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
  });

  it("submits only a display name, never renderer-generated schema identities", async () => {
    const harness = bridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    const admin = useTableAdminStore();
    admin.openCreate();
    admin.form.name = " 订单 ";

    await useTableAdminService().createTable();

    expect(harness.request).toHaveBeenCalledWith(
      "tableAdmin.createRequested",
      { displayName: "订单" },
    );
    expect(harness.notify).not.toHaveBeenCalled();
    const wire = JSON.stringify(harness.request.mock.calls[0]?.[1]);
    expect(wire).not.toMatch(/fieldId|physicalName|providerFieldId|schema\.apply/);
    expect(admin.phase).toBe("idle");
  });

  it("correlates create completion with the matching host request", async () => {
    const harness = bridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    const opened: string[] = [];
    const service = useTableAdminService();
    service.init((tableId) => { opened.push(tableId); });
    const admin = useTableAdminStore();
    const ui = useUiStore();
    admin.openCreate();
    ui.openCreate();
    admin.form.name = "订单";

    await service.createTable();

    expect(harness.request).toHaveBeenCalledWith(
      "tableAdmin.createRequested",
      { displayName: "订单" },
    );
    expect(harness.notify).not.toHaveBeenCalled();
    expect(opened).toEqual(["tbl_opaque"]);
    expect(admin.phase).toBe("idle");
    expect(ui.createModalOpen).toBe(false);
  });

  it("fails create only when its correlated host request rejects", async () => {
    const harness = bridgeHarness();
    harness.request.mockRejectedValueOnce(new Error("name already exists"));
    setHostBridgeForTesting(harness.bridge);
    const admin = useTableAdminStore();
    const ui = useUiStore();
    admin.openCreate();
    ui.openCreate();
    admin.form.name = "订单";

    await useTableAdminService().createTable();

    expect(admin.phase).toBe("failed");
    expect(admin.error).toBe("name already exists");
    expect(ui.createModalOpen).toBe(true);
    expect(harness.notify).not.toHaveBeenCalled();
  });

  it("does not revive a closed create flow when its request fails late", async () => {
    const harness = bridgeHarness();
    const response = deferred<never>();
    harness.request.mockImplementationOnce(() => response.promise);
    setHostBridgeForTesting(harness.bridge);
    const admin = useTableAdminStore();
    const ui = useUiStore();
    admin.openCreate();
    ui.openCreate();
    admin.form.name = "订单";

    const pending = useTableAdminService().createTable();
    await Promise.resolve();
    admin.close();
    ui.closeCreate();
    response.reject(new Error("late failure"));
    await pending;

    expect(admin.phase).toBe("idle");
    expect(admin.error).toBeNull();
    expect(ui.createModalOpen).toBe(false);
  });

  it("does not let collection broadcasts settle a pending create request", async () => {
    const harness = bridgeHarness();
    const response = deferred<{
      tables: string[];
      displayNames: Record<string, string>;
      createdTableId: string;
    }>();
    harness.request.mockImplementationOnce(() => response.promise);
    setHostBridgeForTesting(harness.bridge);
    const opened: string[] = [];
    const service = useTableAdminService();
    service.init((tableId) => { opened.push(tableId); });
    const admin = useTableAdminStore();
    const ui = useUiStore();
    admin.openCreate();
    ui.openCreate();
    admin.form.name = "订单";

    const pending = service.createTable();
    await Promise.resolve();
    harness.emit("database.opened", {
      tables: ["tbl_unrelated"],
      views: [],
      displayNames: { tbl_unrelated: "订单" },
    });
    harness.emit("database.collectionsChanged", {
      tables: ["tbl_unrelated"],
      displayNames: { tbl_unrelated: "订单" },
    });

    expect(admin.phase).toBe("submitting");
    expect(ui.createModalOpen).toBe(true);
    expect(opened).toEqual([]);

    response.resolve({
      tables: ["tbl_unrelated", "tbl_created"],
      displayNames: {
        tbl_unrelated: "订单",
        tbl_created: "订单",
      },
      createdTableId: "tbl_created",
    });
    await pending;
    harness.emit("database.collectionsChanged", {
      tables: ["tbl_unrelated", "tbl_created"],
      displayNames: {
        tbl_unrelated: "订单",
        tbl_created: "订单",
      },
    });

    expect(admin.phase).toBe("idle");
    expect(ui.createModalOpen).toBe(false);
    expect(opened).toEqual(["tbl_created"]);
  });

  it("opens the unified field drawer callback after the new table appears", async () => {
    const harness = bridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    const opened: string[] = [];
    const service = useTableAdminService();
    service.init((tableId) => { opened.push(tableId); });
    const admin = useTableAdminStore();
    const ui = useUiStore();
    admin.openCreate();
    ui.openCreate();
    admin.form.name = "订单";
    await service.createTable();

    harness.emit("database.collectionsChanged", {
      tables: ["tbl_opaque"],
      displayNames: { tbl_opaque: "订单" },
    });

    expect(opened).toEqual(["tbl_opaque"]);
    expect(admin.phase).toBe("idle");
    expect(ui.createModalOpen).toBe(false);
  });

  it("fails closed when a correlated create response lacks a new identity", async () => {
    const harness = bridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    const opened: string[] = [];
    const service = useTableAdminService();
    service.init((tableId) => { opened.push(tableId); });
    harness.emit("database.collectionsChanged", {
      tables: ["tbl_existing"],
      displayNames: { tbl_existing: "订单" },
    });
    const admin = useTableAdminStore();
    admin.openCreate();
    admin.form.name = "订单";
    harness.request.mockResolvedValueOnce({
      tables: ["tbl_existing"],
      displayNames: { tbl_existing: "订单" },
    });
    await service.createTable();
    harness.emit("database.collectionsChanged", {
      tables: ["tbl_existing"],
      displayNames: { tbl_existing: "订单" },
    });

    expect(opened).toEqual([]);
    expect(admin.phase).toBe("failed");
    expect(admin.error).toBe("主机未返回新建数据表的权威标识。");
  });

  it("keeps table deletion on the separate correlated host-owned lifecycle", async () => {
    const harness = bridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    await useTableAdminService().deleteTable("tbl_orders");
    expect(harness.request).toHaveBeenCalledWith(
      "tableAdmin.deleteRequested",
      { collection: "tbl_orders" },
    );
    expect(harness.notify).not.toHaveBeenCalled();
  });

  it("fails delete only when its correlated host request rejects", async () => {
    const harness = bridgeHarness();
    harness.request.mockRejectedValueOnce(new Error("table is protected"));
    setHostBridgeForTesting(harness.bridge);
    const admin = useTableAdminStore();

    await useTableAdminService().deleteTable("tbl_orders");

    expect(admin.phase).toBe("failed");
    expect(admin.error).toBe("table is protected");
    expect(harness.notify).not.toHaveBeenCalled();
  });

  it("does not let unrelated broadcasts settle or mask a pending delete failure", async () => {
    const harness = bridgeHarness();
    const response = deferred<never>();
    harness.request.mockImplementationOnce(() => response.promise);
    setHostBridgeForTesting(harness.bridge);
    const service = useTableAdminService();
    service.init();
    const admin = useTableAdminStore();
    const ui = useUiStore();
    admin.setCollections([
      { collection: "tbl_orders", metadata: {} },
    ]);
    ui.openDelete("tbl_orders");

    const pending = service.deleteTable("tbl_orders");
    await Promise.resolve();
    harness.emit("database.collectionsChanged", {
      tables: ["tbl_orders", "tbl_unrelated"],
      displayNames: {
        tbl_orders: "订单",
        tbl_unrelated: "其他",
      },
    });

    expect(admin.phase).toBe("deleting");
    expect(ui.deleteModalOpen).toBe(true);

    response.reject(new Error("table is protected"));
    await pending;

    expect(admin.phase).toBe("failed");
    expect(admin.error).toBe("table is protected");
    expect(ui.deleteModalOpen).toBe(true);
  });
});

describe("tableAdminService lifecycle ownership", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
  });

  it("subscribes each broadcast once across repeated init on the same instance", () => {
    const harness = bridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    const admin = useTableAdminStore();
    const service = useTableAdminService();
    service.init();
    service.init();
    const setCollections = vi.spyOn(admin, "setCollections");

    harness.emit("database.collectionsChanged", {
      tables: ["tbl_a"],
      displayNames: { tbl_a: "A" },
    });

    expect(setCollections).toHaveBeenCalledTimes(1);
    setCollections.mockClear();

    harness.emit("database.opened", {
      tables: ["tbl_a"],
      views: [],
      displayNames: {},
    });

    expect(setCollections).toHaveBeenCalledTimes(1);
  });

  it("stops consuming broadcasts after dispose and never re-subscribes (idempotent dispose)", () => {
    const harness = bridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    const opened: string[] = [];
    const service = useTableAdminService();
    service.init((tableId) => { opened.push(tableId); });
    service.dispose();
    service.dispose();
    service.init((tableId) => { opened.push(`revived:${tableId}`); });
    const admin = useTableAdminStore();

    harness.emit("database.collectionsChanged", {
      tables: ["tbl_retired"],
      displayNames: {},
    });
    harness.emit("database.opened", {
      tables: ["tbl_retired"],
      views: [],
      displayNames: {},
    });

    expect(admin.collections).toEqual([]);
    expect(opened).toEqual([]);
  });

  it("lets only a replacement instance consume broadcasts once after the old owner is disposed", () => {
    const harness = bridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    const admin = useTableAdminStore();
    const retired = useTableAdminService();
    retired.init();
    retired.dispose();
    const live = useTableAdminService();
    live.init();
    const setCollections = vi.spyOn(admin, "setCollections");

    harness.emit("database.collectionsChanged", {
      tables: ["tbl_live"],
      displayNames: { tbl_live: "Live" },
    });

    expect(setCollections).toHaveBeenCalledTimes(1);
    expect(admin.collections.map((item) => item.collection)).toEqual(["tbl_live"]);
  });

  it("retires a pending create continuation without touching the shared owner or created callback", async () => {
    const harness = bridgeHarness();
    const response = deferred<{
      tables: string[];
      displayNames: Record<string, string>;
      createdTableId: string;
    }>();
    harness.request.mockImplementationOnce(() => response.promise);
    setHostBridgeForTesting(harness.bridge);
    const opened: string[] = [];
    const service = useTableAdminService();
    service.init((tableId) => { opened.push(tableId); });
    const admin = useTableAdminStore();
    const ui = useUiStore();
    admin.openCreate();
    ui.openCreate();
    admin.form.name = "订单";

    const pending = service.createTable();
    await Promise.resolve();
    expect(admin.phase).toBe("submitting");
    service.dispose();

    response.resolve({
      tables: ["tbl_created"],
      displayNames: { tbl_created: "订单" },
      createdTableId: "tbl_created",
    });
    await pending;

    // The disposed instance's continuation must not settle the shared store,
    // close the modal on behalf of a new owner, or fire the retired callback.
    expect(admin.phase).toBe("submitting");
    expect(ui.createModalOpen).toBe(true);
    expect(opened).toEqual([]);
    expect(admin.collections).toEqual([]);
  });

  it("ignores an already-resolved create continuation that resumes only after dispose", async () => {
    const harness = bridgeHarness();
    const response = deferred<{
      tables: string[];
      displayNames: Record<string, string>;
      createdTableId: string;
    }>();
    harness.request.mockImplementationOnce(() => response.promise);
    setHostBridgeForTesting(harness.bridge);
    const opened: string[] = [];
    const service = useTableAdminService();
    service.init((tableId) => { opened.push(tableId); });
    const admin = useTableAdminStore();
    const ui = useUiStore();
    admin.openCreate();
    ui.openCreate();
    admin.form.name = "订单";

    const pending = service.createTable();
    await Promise.resolve();
    // The response is resolved but its awaiting continuation has not resumed
    // yet; dispose must retire that resumption, not just unsent requests.
    response.resolve({
      tables: ["tbl_created"],
      displayNames: { tbl_created: "订单" },
      createdTableId: "tbl_created",
    });
    service.dispose();
    // A replacement owner takes over the shared store and modal immediately.
    admin.openCreate();
    ui.openCreate();

    await pending;
    for (let i = 0; i < 8; i += 1) await Promise.resolve();

    expect(admin.phase).toBe("creating");
    expect(ui.createModalOpen).toBe(true);
    expect(opened).toEqual([]);
    expect(admin.collections).toEqual([]);
  });

  it("ignores public operations from a disposed instance", async () => {
    const harness = bridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    const admin = useTableAdminStore();
    const ui = useUiStore();
    const service = useTableAdminService();
    service.dispose();
    admin.openCreate();
    admin.form.name = "订单";

    await service.createTable();
    await service.deleteTable("tbl_orders");
    service.openAdmin();

    expect(harness.request).not.toHaveBeenCalled();
    expect(harness.notify).not.toHaveBeenCalled();
    expect(admin.phase).toBe("creating");
    expect(ui.createModalOpen).toBe(false);
  });
});

describe("tableAdminService workspace/session retirement", () => {
  const workspaceA = "11111111-1111-4111-8111-111111111111";
  const workspaceB = "22222222-2222-4222-8222-222222222222";

  function sessionBridgeHarness(): {
    posted: Array<{ type: string; requestId: string; payload?: unknown }>;
    reply: (requestId: string, payload: unknown) => void;
    fail: (requestId: string, message: string) => void;
  } {
    const posted: Array<{ type: string; requestId: string; payload?: unknown }> = [];
    let listener: ((event: { data: unknown }) => void) | null = null;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `admin-session-r${++sequence}`,
      webview: {
        addEventListener: (_: string, fn: (event: { data: unknown }) => void) => { listener = fn; },
        removeEventListener: () => { listener = null; },
        postMessage: (message: unknown) => { posted.push(message as { type: string; requestId: string }); },
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    return {
      posted,
      reply: (requestId, payload) => listener?.({
        data: { type: "database.collectionsChanged", requestId, payload },
      }),
      fail: (requestId, message) => listener?.({
        data: { type: "operation.failed", requestId, payload: { message, code: "X" } },
      }),
    };
  }

  function applySession(workspaceId: string, sessionEpoch: number): void {
    useWorkspaceSessionStore().applySession({
      contractVersion: "2.0",
      workspaceId,
      sessionEpoch,
      state: "openedWritable",
      openMode: "writable",
      writable: true,
      provisional: false,
      phase: "idle",
      errorCode: null,
    });
  }

  function openSessionA(): void {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    session.setWorkspaces([workspaceA, workspaceB].map((workspaceId, index) => ({
      contractVersion: "2.0" as const,
      workspaceId,
      displayName: `Workspace ${index + 1}`,
      selectedRoot: `D:\\Workspace-${index + 1}`,
      activityRoot: null,
      storageKind: "fixed" as const,
      coordinationStrength: "strong" as const,
      lastOpenedAt: null,
      lastKnownHealth: "healthy" as const,
      lastSnapshotAt: null,
      lastSyncAt: null,
      pendingSync: false,
    })));
    applySession(workspaceA, 1);
  }

  beforeEach(() => {
    setActivePinia(createPinia());
  });
  afterEach(() => setHostBridgeForTesting(null));

  it("retires an already-resolved create continuation when the session rotates before it resumes", async () => {
    const harness = sessionBridgeHarness();
    openSessionA();
    const created = vi.fn();
    const service = useTableAdminService();
    service.init(created);
    const admin = useTableAdminStore();
    const ui = useUiStore();
    admin.setCollections([{ collection: "tbl_old", metadata: {} }]);
    admin.openCreate();
    ui.openCreate();
    admin.form.name = "订单";

    const pending = service.createTable();
    await Promise.resolve();
    const request = harness.posted.find((item) => item.type === "tableAdmin.createRequested")!;
    // The reply resolves first; the session rotates synchronously before the
    // awaiting continuation resumes.
    harness.reply(request.requestId, {
      tables: ["tbl_old", "tbl_new"],
      displayNames: { tbl_old: "旧表", tbl_new: "订单" },
      createdTableId: "tbl_new",
    });
    applySession(workspaceB, 2);
    await pending;
    for (let i = 0; i < 8; i += 1) await Promise.resolve();

    expect(admin.collections.map((item) => item.collection)).toEqual(["tbl_old"]);
    expect(admin.phase).toBe("submitting");
    expect(ui.createModalOpen).toBe(true);
    expect(created).not.toHaveBeenCalled();
  });

  it("retires a create continuation whose failure arrives after the session rotated", async () => {
    const harness = sessionBridgeHarness();
    openSessionA();
    const service = useTableAdminService();
    const admin = useTableAdminStore();
    const ui = useUiStore();
    admin.openCreate();
    ui.openCreate();
    admin.form.name = "订单";

    const pending = service.createTable();
    await Promise.resolve();
    const request = harness.posted.find((item) => item.type === "tableAdmin.createRequested")!;
    applySession(workspaceB, 2);
    harness.fail(request.requestId, "host rejected the create");
    await pending;
    for (let i = 0; i < 8; i += 1) await Promise.resolve();

    expect(admin.phase).toBe("submitting");
    expect(admin.error).toBeNull();
    expect(ui.createModalOpen).toBe(true);
  });

  it("retires a delete continuation whose reply arrives after the session rotated", async () => {
    const harness = sessionBridgeHarness();
    openSessionA();
    const service = useTableAdminService();
    const admin = useTableAdminStore();
    const ui = useUiStore();
    admin.setCollections([{ collection: "tbl_orders", metadata: {} }]);
    ui.openDelete("tbl_orders");

    const pending = service.deleteTable("tbl_orders");
    await Promise.resolve();
    const request = harness.posted.find((item) => item.type === "tableAdmin.deleteRequested")!;
    applySession(workspaceB, 2);
    harness.reply(request.requestId, {
      tables: [],
      displayNames: {},
      deletedTableId: "tbl_orders",
    });
    await pending;
    for (let i = 0; i < 8; i += 1) await Promise.resolve();

    expect(admin.collections.map((item) => item.collection)).toEqual(["tbl_orders"]);
    expect(admin.phase).toBe("deleting");
    expect(ui.deleteModalOpen).toBe(true);
  });

  it("keeps the normal create flow while the session stays on one workspace and epoch", async () => {
    const harness = sessionBridgeHarness();
    openSessionA();
    const created = vi.fn();
    const service = useTableAdminService();
    service.init((tableId) => { created(tableId); });
    const admin = useTableAdminStore();
    const ui = useUiStore();
    admin.openCreate();
    ui.openCreate();
    admin.form.name = "订单";

    const pending = service.createTable();
    await Promise.resolve();
    const request = harness.posted.find((item) => item.type === "tableAdmin.createRequested")!;
    harness.reply(request.requestId, {
      tables: ["tbl_new"],
      displayNames: { tbl_new: "订单" },
      createdTableId: "tbl_new",
    });
    await pending;

    expect(admin.phase).toBe("idle");
    expect(ui.createModalOpen).toBe(false);
    expect(created).toHaveBeenCalledWith("tbl_new");
    expect(admin.collections.map((item) => item.collection)).toEqual(["tbl_new"]);
  });
});
