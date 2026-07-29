import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { HostBridge } from "@/bridge/hostBridge";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useUiStore } from "@/stores/uiStore";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useTableAdminService } from "./tableAdminService";

function bridgeHarness(): {
  bridge: HostBridge;
  notify: ReturnType<typeof vi.fn>;
  emit: (type: string, payload: unknown) => void;
} {
  const handlers = new Map<string, (payload: any) => void>();
  const notify = vi.fn();
  return {
    bridge: {
      notify,
      request: vi.fn(),
      on: vi.fn((type: string, handler: (payload: any) => void) => {
        handlers.set(type, handler);
        return () => handlers.delete(type);
      }),
    } as unknown as HostBridge,
    notify,
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

    expect(harness.notify).toHaveBeenCalledWith(
      "tableAdmin.createRequested",
      { displayName: "订单" },
    );
    const wire = JSON.stringify(harness.notify.mock.calls[0]?.[1]);
    expect(wire).not.toMatch(/fieldId|physicalName|providerFieldId|schema\.apply/);
    expect(admin.phase).toBe("submitting");
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

  it("opens the field drawer when the new opaque ID arrives before its display name", async () => {
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
    });

    expect(opened).toEqual(["tbl_opaque"]);
    expect(admin.phase).toBe("idle");
    expect(ui.createModalOpen).toBe(false);
  });

  it("keeps the create baseline until a later event identifies the new table", async () => {
    const harness = bridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    const opened: string[] = [];
    const service = useTableAdminService();
    service.init((tableId) => { opened.push(tableId); });
    const admin = useTableAdminStore();
    admin.openCreate();
    admin.form.name = "订单";
    await service.createTable();

    harness.emit("database.collectionsChanged", {
      tables: ["tbl_existing", "tbl_created"],
    });
    expect(opened).toEqual([]);
    expect(admin.phase).toBe("submitting");

    harness.emit("database.collectionsChanged", {
      tables: ["tbl_existing", "tbl_created"],
      displayNames: {
        tbl_existing: "客户",
        tbl_created: "订单",
      },
    });

    expect(opened).toEqual(["tbl_created"]);
    expect(admin.phase).toBe("idle");
  });

  it("does not mistake an existing table refresh for the pending create", async () => {
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
    await service.createTable();
    harness.emit("database.collectionsChanged", {
      tables: ["tbl_existing"],
      displayNames: { tbl_existing: "订单" },
    });

    expect(opened).toEqual([]);
    expect(admin.phase).toBe("submitting");
  });

  it("keeps table deletion on the separate host-owned lifecycle intent", () => {
    const harness = bridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    useTableAdminService().deleteTable("tbl_orders");
    expect(harness.notify).toHaveBeenCalledWith(
      "tableAdmin.deleteRequested",
      { collection: "tbl_orders" },
    );
  });
});
