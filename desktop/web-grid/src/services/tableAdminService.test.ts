import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { HostBridge } from "@/bridge/hostBridge";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useUiStore } from "@/stores/uiStore";
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
  const handlers = new Map<string, (payload: any) => void>();
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
        handlers.set(type, handler);
        return () => handlers.delete(type);
      }),
    } as unknown as HostBridge,
    notify,
    request,
    emit: (type, payload) => handlers.get(type)?.(payload),
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
