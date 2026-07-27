import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useUiStore } from "@/stores/uiStore";
import { useTableAdminService } from "./tableAdminService";
import { createSchemaEnumOptionDraft } from "./schemaFieldDraft";
import { vi } from "vitest";
import type {
  CollectionsChangedPayload,
  DatabaseOpenedPayload,
  SchemaChangePayload,
} from "@/contracts";

/**
 * Shim-bridge helper, mirroring `errorRouter.test.ts`. The host bridge wraps a
 * WebView-like object; we install a fake whose `addEventListener` captures the
 * listener so a test can drive inbound events via `emit(type, payload)`.
 */
function makeShimBridge(): {
  bridge: HostBridge;
  emit: (type: string, payload: unknown) => void;
} {
  let listener: ((e: { data: unknown }) => void) | null = null;
  const shim = {
    addEventListener: (_: string, fn: (e: { data: unknown }) => void) => {
      listener = fn;
    },
    removeEventListener: (
      _: string,
      fn: (e: { data: unknown }) => void,
    ) => {
      if (listener === fn) listener = null;
    },
    postMessage: () => {},
  };
  const bridge = createHostBridge({ webview: shim });
  bridge.start();
  return {
    bridge,
    emit: (type, payload) =>
      listener?.({
        data:
          typeof payload === "string"
            ? payload
            : JSON.stringify({ type, payload }),
      }),
  };
}

describe("tableAdminService", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    useTableAdminStore().setAutoDateProducerEnabled(true);
  });

  // CRITICAL: `useHostBridge` is a module singleton. Reset to null after each
  // test so the fake bridge does not leak into other test files (matches the
  // pattern + architecture-debt note in errorRouter.test.ts).
  afterEach(() => setHostBridgeForTesting(null));

  it("transitions phase submitting -> idle AND closes the create modal on collectionsChanged", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const admin = useTableAdminStore();
    const ui = useUiStore();
    const svc = useTableAdminService();
    svc.init();

    // Simulate the user opening the create modal and submitting.
    admin.openCreate();
    ui.openCreate();
    admin.beginSubmit();
    expect(admin.phase).toBe("submitting");
    expect(ui.createModalOpen).toBe(true);

    // Host signals success by re-announcing the (now changed) collection list.
    emit("database.collectionsChanged", {
      tables: ["users", "orders"],
    } as CollectionsChangedPayload);

    expect(admin.phase).toBe("idle");
    expect(admin.form.name).toBe("");
    expect(ui.createModalOpen).toBe(false);
  });

  it("keeps the autoDate producer behind the host kill switch", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const admin = useTableAdminStore();
    useTableAdminService().init();

    emit("database.opened", {
      tables: [],
      views: [],
      features: { dashboards: false, autoDateFields: true },
    } as DatabaseOpenedPayload);
    expect(admin.autoDateProducerEnabled).toBe(true);

    emit("database.opened", {
      tables: [],
      views: [],
      features: { dashboards: false, autoDateFields: false },
    } as DatabaseOpenedPayload);
    expect(admin.autoDateProducerEnabled).toBe(false);
  });

  it("transitions phase deleting -> idle AND closes the delete modal on collectionsChanged", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const admin = useTableAdminStore();
    const ui = useUiStore();
    const svc = useTableAdminService();
    svc.init();

    admin.requestDelete("orders");
    ui.openDelete("orders");
    expect(admin.phase).toBe("deleting");
    expect(ui.deleteModalOpen).toBe(true);

    emit("database.collectionsChanged", {
      tables: ["users"],
    } as CollectionsChangedPayload);

    expect(admin.phase).toBe("idle");
    expect(admin.pendingDelete).toBeNull();
    expect(ui.deleteModalOpen).toBe(false);
  });

  it("does NOT close an unsubmitted create form on collectionsChanged", () => {
    // Regression guard: collectionsChanged fires for ANY collection change,
    // including the initial load. Only an in-flight create/delete should be
    // resolved; an idle store must not be touched.
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const admin = useTableAdminStore();
    const ui = useUiStore();
    const svc = useTableAdminService();
    svc.init();

    // The form is open and editable, but no create request is in flight.
    admin.openCreate();
    ui.openCreate();
    expect(admin.phase).toBe("creating");
    expect(ui.createModalOpen).toBe(true);

    emit("database.collectionsChanged", {
      tables: ["users"],
    } as CollectionsChangedPayload);

    // Phase stays editable and modal stays open: no false success.
    expect(admin.phase).toBe("creating");
    expect(ui.createModalOpen).toBe(true);
  });

  it("resolves a pending create on database.opened as well", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const admin = useTableAdminStore();
    const ui = useUiStore();
    const svc = useTableAdminService();
    svc.init();

    admin.openCreate();
    ui.openCreate();
    admin.beginSubmit();

    emit("database.opened", {
      tables: ["users", "orders"],
      views: [],
    } as DatabaseOpenedPayload);

    expect(admin.phase).toBe("idle");
    expect(ui.createModalOpen).toBe(false);
  });

  it("still updates the collections list on collectionsChanged regardless of phase", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const admin = useTableAdminStore();
    const svc = useTableAdminService();
    svc.init();

    emit("database.collectionsChanged", {
      tables: ["vt_t_users", "vt_t_orders", "vt_t_items"],
      capabilityHashes: { vt_t_users: "abc" },
      displayNames: { vt_t_users: "用户", vt_t_orders: "订单" },
    } as CollectionsChangedPayload);

    expect(admin.collections).toHaveLength(3);
    expect(admin.collections[0]?.collection).toBe("vt_t_users");
    expect(admin.collections[0]?.metadata?.capabilityHash).toBe("abc");
    expect(admin.collections[0]?.displayName).toBe("用户");
  });

  it("validates then applies a normalized product schema without provider fields", async () => {
    const request = vi.fn(async (type: string, payload: unknown) => {
      if (type === "schema.validate") return payload;
      if (type === "schema.apply") return (payload as { definition: unknown }).definition;
      throw new Error(`unexpected ${type}`);
    });
    setHostBridgeForTesting({
      request,
      on: vi.fn(() => vi.fn()),
    } as unknown as HostBridge);
    const admin = useTableAdminStore();
    const ui = useUiStore();
    admin.openCreate();
    ui.openCreate();
    admin.form.name = "订单";
    const pending = createSchemaEnumOptionDraft();
    Object.assign(pending, { valueText: "pending", displayName: "待处理" });
    const completed = createSchemaEnumOptionDraft();
    Object.assign(completed, { valueText: "done", displayName: "已完成" });
    admin.updateField(0, {
      name: "status",
      type: "select",
      enumOptions: [pending, completed],
      required: true,
    });
    admin.addField("json");
    admin.updateField(1, { name: "元数据", jsonSchemaText: '{"type":"object"}' });
    admin.addField("formula");
    admin.updateField(2, { name: "总额", formulaSource: "quantity * unit_price" });
    admin.addField("dateTime");
    admin.updateField(3, { name: "event_time" });
    admin.addField("file");
    admin.updateField(4, {
      name: "photos",
      allowedMimeTypesText: "image/png, image/jpeg",
      thumbnailVariantsText: "320x240, 640x480",
    });
    admin.addField("multiSelect");
    const urgent = createSchemaEnumOptionDraft();
    Object.assign(urgent, { valueText: "1", displayName: "紧急" });
    const external = createSchemaEnumOptionDraft();
    Object.assign(external, { valueText: "external", displayName: "外部" });
    const reviewed = createSchemaEnumOptionDraft();
    Object.assign(reviewed, { valueText: "true", displayName: "已复核" });
    admin.updateField(5, {
      name: "labels",
      enumOptions: [urgent, external, reviewed],
      enumMinSelected: 1,
      enumMaxSelected: 2,
    });
    admin.addIndex();
    Object.assign(admin.form.indexes[0]!, {
      name: "idx_status",
      fieldClientIds: [admin.form.fields[0]!.clientId],
    });
    admin.addIndex();
    Object.assign(admin.form.indexes[1]!, {
      name: "uidx_status_created",
      fieldClientIds: [
        admin.form.fields[0]!.clientId,
        admin.form.fields[3]!.clientId,
      ],
      unique: true,
    });

    await useTableAdminService().createTable();

    expect(request.mock.calls.map(([type]) => type)).toEqual(["schema.validate", "schema.apply"]);
    const validatedDefinition = (
      request.mock.calls[0]?.[1] as SchemaChangePayload
    ).definition;
    expect(validatedDefinition.indexes).toEqual([
      { name: "idx_status", fieldIds: ["fld_status"], unique: false },
      {
        name: "uidx_status_created",
        fieldIds: ["fld_status", "fld_event_time"],
        unique: true,
      },
    ]);
    expect(validatedDefinition.fields[4]?.attachmentPolicy?.thumbnailVariants)
      .toEqual(["320x240", "640x480"]);
    expect(validatedDefinition.fields[0]?.constraints).toContainEqual({
      kind: "enum",
      multiple: false,
      minSelected: 1,
      maxSelected: 1,
      options: [
        { value: "pending", displayName: "待处理" },
        { value: "done", displayName: "已完成" },
      ],
    });
    expect(validatedDefinition.fields[5]?.constraints).toContainEqual({
      kind: "enum",
      multiple: true,
      minSelected: 1,
      maxSelected: 2,
      options: [
        { value: 1, displayName: "紧急" },
        { value: "external", displayName: "外部" },
        { value: true, displayName: "已复核" },
      ],
    });
    expect(validatedDefinition.fields.slice(-2)).toEqual([
      expect.objectContaining({
        physicalName: "created_at",
        dataType: "autoDate",
        autoDate: { role: "createdAt" },
        nullable: false,
        readOnly: true,
      }),
      expect.objectContaining({
        physicalName: "updated_at",
        dataType: "autoDate",
        autoDate: { role: "updatedAt" },
        nullable: false,
        readOnly: true,
      }),
    ]);
    expect((request.mock.calls[1]?.[1] as SchemaChangePayload).definition)
      .toEqual(validatedDefinition);
    const serialized = JSON.stringify(request.mock.calls[0]?.[1]);
    expect(serialized).toContain('"dataType":"json"');
    expect(serialized).toContain('"language":"cel-v1"');
    expect(serialized).not.toMatch(
      new RegExp(`${"dire" + "ctus"}|pocketbase|sessionSecret|accessToken`, "i"),
    );
    expect(admin.phase).toBe("idle");
    expect(ui.createModalOpen).toBe(false);
  });

  it("keeps the form open and locates an authoritative schema error by path", async () => {
    setHostBridgeForTesting({
      request: vi.fn(async () => ({
        error: {
          code: "schema.field.invalid_constraint",
          path: "fields[0].constraints.scale",
          message: "scale 不能大于 precision",
        },
      })),
      on: vi.fn(() => vi.fn()),
    } as unknown as HostBridge);
    const admin = useTableAdminStore();
    const ui = useUiStore();
    admin.openCreate();
    ui.openCreate();
    admin.form.name = "订单";
    admin.updateField(0, { name: "金额", type: "decimal", precision: 8, scale: 2 });

    await useTableAdminService().createTable();

    expect(admin.phase).toBe("failed");
    expect(admin.serverFieldErrors["fields[0].constraints.scale"])
      .toContain("scale 不能大于 precision");
    expect(ui.createModalOpen).toBe(true);
  });

  it("rejects a user field that collides with an enabled system time name", async () => {
    const request = vi.fn();
    setHostBridgeForTesting({
      request,
      on: vi.fn(() => vi.fn()),
    } as unknown as HostBridge);
    const admin = useTableAdminStore();
    admin.openCreate();
    admin.form.name = "Collision";
    admin.updateField(0, { name: "created_at" });

    await useTableAdminService().createTable();

    expect(request).not.toHaveBeenCalled();
    expect(admin.phase).toBe("failed");
    expect(admin.serverFieldErrors["fields[0].physicalName"])
      .toContain("created_at");
  });

  it("localizes the non-empty autoDate backfill error", async () => {
    setHostBridgeForTesting({
      request: vi.fn(async () => ({
        error: {
          code: "schema.field.autodate_backfill_required",
          path: "fields",
          message: "provider message",
        },
      })),
      on: vi.fn(() => vi.fn()),
    } as unknown as HostBridge);
    const admin = useTableAdminStore();
    admin.openCreate();
    admin.form.name = "Backfill";
    admin.updateField(0, { name: "title" });

    await useTableAdminService().createTable();

    expect(admin.error).toBe("现有记录没有可信历史时间，非空表不能直接新增该字段。");
  });
});
