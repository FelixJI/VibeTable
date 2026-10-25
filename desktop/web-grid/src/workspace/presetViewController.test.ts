import { flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { effectScope, ref } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { PresetEntry, PresetsResult } from "@/contracts";
import type { HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import { usePresetVersionService } from "@/services/presetVersionService";
import { ProductRpcError, ProductRpcProtocolError } from "@/services/productRpcResult";
import { BridgeOperationError, BridgeRequestRetiredError, BridgeTimeoutError } from "@/bridge/hostBridge";
import { usePresetVersionStore } from "@/stores/presetVersionStore";
import { useTableStore } from "@/stores/tableStore";
import { useUiStore } from "@/stores/uiStore";
import { useViewQueryStore } from "@/stores/viewQueryStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import {
  createPresetViewController,
  type PresetViewDependencies,
  type PresetServicePort,
} from "./presetViewController";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

function preset(
  id: string,
  collection: string,
  isDefault = false,
): PresetEntry {
  return {
    id,
    collection,
    name: id,
    scope: "personal",
    revision: `revision-${id}`,
    emittedEvents: [],
    view: {
      kind: "table",
      layout: "table",
      filters: [],
      sorts: [],
      search: "",
      visibleFields: [],
      isDefault,
    },
  };
}

function dependencies(service: PresetViewDependencies["service"]) {
  return {
    workspace: useWorkspaceStore(),
    table: useTableStore(),
    ui: useUiStore(),
    presets: usePresetVersionStore(),
    query: useViewQueryStore(),
    service,
    grid: { current: ref(null) },
    executeQuery: vi.fn(),
    refreshLookups: vi.fn(),
    reportError: vi.fn(),
    defaultChangeIncompleteError: (cause: unknown) => new Error("default change incomplete", { cause }),
  };
}

describe("presetViewController", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("keeps the built-in complete view available after creating a custom view", async () => {
    const service: PresetServicePort = {
      listPresets: vi.fn(async () => ({ collection: "orders", presets: [] })),
      savePreset: vi.fn(async (collection, name, view) => ({ ...preset("custom", collection), name, view })),
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    deps.table.schema = [
      { name: "id", title: "行 ID", kind: "system", dataType: "text", editable: false, nullable: false },
      { name: "due", title: "日期", dataType: "date", editable: true, nullable: true },
    ];
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;
    deps.workspace.selectTable("orders");
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["due"]);
    await controller.dispatch({ type: "view.create", request: { name: "日历", kind: "calendar",
      dateField: "due", endDateField: null, titleField: null, groupField: null, coverField: null } });
    expect(deps.presets.presets[0]?.view.isDefault).toBe(false);
    expect(controller.activeKind.value).toBe("calendar");
    deps.query.search = "needle";
    await controller.dispatch({ type: "view.reset" });
    expect(deps.presets.activePresetId).toBeNull();
    expect(controller.activeKind.value).toBe("table");
    expect(deps.query.search).toBe("");
    expect(deps.query.visibleFields).toEqual(["due"]);
    expect(deps.presets.presets).toHaveLength(1);
    scope.stop();
  });

  it.each([null, "customers", "orders"])("关闭或切表至 %s 后旧列表错误不污染当前呈现", async (nextTable) => {
    const oldList = deferred<PresetsResult>();
    const currentList = deferred<PresetsResult>();
    const service: PresetServicePort = {
      listPresets: vi.fn().mockReturnValueOnce(oldList.promise).mockReturnValue(currentList.promise),
      savePreset: vi.fn(async () => { throw new Error("unexpected save"); }),
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    const fail = vi.spyOn(deps.presets, "fail");
    const scope = effectScope();
    scope.run(() => createPresetViewController(deps));
    deps.workspace.selectTable("orders");
    await flushPromises();
    deps.workspace.currentTable = null;
    await flushPromises();
    if (nextTable) {
      deps.workspace.selectTable(nextTable);
      await flushPromises();
    }
    oldList.reject(new Error("retired request"));
    await flushPromises();
    expect(fail).not.toHaveBeenCalled();
    expect(deps.reportError).not.toHaveBeenCalled();
    expect(deps.presets.collection).toBe(nextTable ?? "");
    if (nextTable) {
      currentList.resolve({ collection: nextTable, presets: [preset("current", nextTable)] });
      await flushPromises();
      expect(deps.presets.presets.map(item => item.id)).toEqual(["current"]);
    }
    scope.stop();
  });

  it("当前列表的真实错误仍显示", async () => {
    const request = deferred<PresetsResult>();
    const service: PresetServicePort = {
      listPresets: vi.fn(() => request.promise),
      savePreset: vi.fn(async () => { throw new Error("unexpected save"); }),
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    const fail = vi.spyOn(deps.presets, "fail");
    const scope = effectScope();
    scope.run(() => createPresetViewController(deps));
    deps.workspace.selectTable("orders");
    await flushPromises();
    const error = new Error("current list failed");
    request.reject(error);
    await flushPromises();
    expect(fail).toHaveBeenCalledWith(error);
    expect(deps.reportError).toHaveBeenCalledWith(error);
    scope.stop();
  });

  it("丢弃切表前尚未完成的预设列表响应", async () => {
    const orders = deferred<PresetsResult>();
    const customers = deferred<PresetsResult>();
    const service: PresetServicePort = {
      listPresets: vi.fn((collection: string) => (
        collection === "orders" ? orders.promise : customers.promise
      )),
      savePreset: vi.fn(async () => { throw new Error("unexpected save"); }),
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    const scope = effectScope();
    scope.run(() => createPresetViewController(deps));

    deps.workspace.selectTable("orders");
    await flushPromises();
    deps.workspace.selectTable("customers");
    await flushPromises();

    orders.resolve({ collection: "orders", presets: [preset("orders-view", "orders")] });
    await flushPromises();
    expect(deps.presets.collection).toBe("customers");
    expect(deps.presets.presets).toEqual([]);

    customers.resolve({
      collection: "customers",
      presets: [preset("customers-view", "customers", true)],
    });
    await flushPromises();
    expect(deps.presets.presets.map(item => item.id)).toEqual(["customers-view"]);
    scope.stop();
  });

  it("默认视图降级失败时停止补偿并保留核实提示直到显式重载", async () => {
    const first = preset("first", "orders", true);
    const second = preset("second", "orders");
    const saves: Array<{
      target: unknown;
      isDefault: boolean;
    }> = [];
    const listPresets = vi.fn(async () => ({
      collection: "orders",
      presets: [first, second],
    }));
    const savePreset = vi.fn(async (
      collection: string,
      name: string,
      view: PresetEntry["view"],
      target?: unknown,
    ) => {
      saves.push({ target, isDefault: !!view.isDefault });
      const id = typeof target === "object" && target !== null && "id" in target
        ? String(target.id)
        : typeof target === "string" ? target : null;
      if (id === first.id && !view.isDefault) {
        throw new ProductRpcError("preset_edit_conflict", "expectedRevision", "Preset changed elsewhere.", null, false);
      }
      return {
        ...(id === first.id ? first : second),
        collection,
        name,
        view,
        revision: `${id}-saved-${saves.length}`,
      };
    });
    const service: PresetServicePort = {
      listPresets,
      savePreset,
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;

    deps.workspace.selectTable("orders");
    await flushPromises();
    await controller.dispatch({ type: "view.setDefault", view: second });
    await flushPromises();

    expect(saves).toEqual([
      { target: { id: "second", revision: "revision-second" }, isDefault: true },
      { target: { id: "first", revision: "revision-first" }, isDefault: false },
    ]);
    expect(listPresets).toHaveBeenCalledTimes(1);
    expect(deps.presets.error).toContain("default change incomplete");
    // The promotion of `second` is a definite committed fact and stays; the
    // hint stays visible until the user explicitly reloads.
    expect(deps.presets.presets.find(item => item.id === "second")?.view.isDefault).toBe(true);
    await controller.dispatch({ type: "view.reload" });
    expect(listPresets).toHaveBeenCalledTimes(2);
    scope.stop();
  });

  it("首写回执与请求 view 不符时不发降级旧默认的第二写", async () => {
    const current = preset("current", "orders", true);
    const target = preset("target", "orders");
    const posted: Array<{ type: string; payload: Record<string, unknown> }> = [];
    // Real service over a recording bridge: the first preset.save receipt is
    // structurally legal but demotes the promoted default in its view echo.
    const request = vi.fn(async (type: string, payload: Record<string, unknown>) => {
      posted.push({ type, payload });
      if (type === "preset.list") return { collection: "orders", presets: [current, target] };
      if (type === "preset.save") {
        return {
          id: (payload.presetId as string | null) ?? "p-new", collection: "orders",
          name: String(payload.name), scope: "system",
          view: { ...(payload.view as object), isDefault: false },
          userId: null, revision: "rev-new", changeSetId: "change-1", emittedEvents: [],
        };
      }
      throw new Error(`unexpected ${type}`);
    });
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    try {
      const deps = dependencies(usePresetVersionService());
      const scope = effectScope();
      const controller = scope.run(() => createPresetViewController(deps))!;

      deps.workspace.selectTable("orders");
      await flushPromises();

      await controller.dispatch({ type: "view.setDefault", view: target });

      // The service rejects the contradictory view echo, so the controller
      // stops before demoting the old default: exactly one save was posted.
      expect(posted.filter(item => item.type === "preset.save")).toHaveLength(1);
      expect(deps.presets.presets.find(item => item.id === "current")?.view.isDefault).toBe(true);
      expect(deps.presets.error).toBeTruthy();
      scope.stop();
    } finally {
      setHostBridgeForTesting(null);
    }
  });

  it("切换视图时等待当前视图保存完成后再激活目标视图", async () => {
    const current = preset("current", "orders", true);
    const target = preset("target", "orders");
    const save = deferred<PresetEntry>();
    const service: PresetServicePort = {
      listPresets: vi.fn(async () => ({ collection: "orders", presets: [current, target] })),
      savePreset: vi.fn(() => save.promise),
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;

    deps.workspace.selectTable("orders");
    await flushPromises();

    deps.presets.markDirty();
    const switching = controller.dispatch({ type: "view.switch", view: target });
    expect(deps.presets.activePresetId).toBe("current");

    save.resolve({ ...current, revision: "revision-current-saved" });
    await switching;

    expect(service.savePreset).toHaveBeenCalledWith(
      "orders",
      "current",
      expect.any(Object),
      { id: "current", revision: "revision-current" },
    );
    expect(deps.presets.activePresetId).toBe("target");
    scope.stop();
  });

  it.each([
    ["unverifiable reply", () => new ProductRpcProtocolError(
      "invalid-success-payload",
      'Product RPC "preset.save" replied with a success value that violates its DTO contract; the request outcome is unknown. Re-read the authoritative state instead of retrying.')],
    ["bridge timeout", () => new BridgeTimeoutError("preset.save", "req-1", 10_000)],
    ["bridge retirement", () => new BridgeRequestRetiredError()],
    ["operation.failed after dispatch", () => new BridgeOperationError({ message: "backend unavailable", code: "BACKEND_UNAVAILABLE" })],
    ["unexpected transport failure", () => new Error("connection reset")],
    ["unrecognized domain failure", () => new ProductRpcError("preset_storage_failed", "", "storage failed", null, false)],
  ] as const)("默认切换第二写结果未知（%s）时停止补偿写并保留核实提示", async (_label, makeFailure) => {
    const current = preset("current", "orders", true);
    const target = preset("target", "orders");
    const listPresets = vi.fn(async () => ({ collection: "orders", presets: [current, target] }));
    const savePreset = vi.fn(async (_collection: string, _name: string, _view: unknown, target2: { id: string } | null) => {
      if (target2?.id === "target") {
        return { ...target, view: { ...target.view, isDefault: true }, revision: "revision-target-default" };
      }
      throw makeFailure();
    });
    const service: PresetServicePort = {
      listPresets, savePreset: savePreset as unknown as PresetServicePort["savePreset"],
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;

    deps.workspace.selectTable("orders");
    await flushPromises();
    const listCallsAfterLoad = listPresets.mock.calls.length;

    await controller.dispatch({ type: "view.setDefault", view: target });

    // Two writes only: target→default, current→non-default. No blind third
    // compensating write may follow an unverifiable reply.
    expect(savePreset).toHaveBeenCalledTimes(2);
    expect(listPresets.mock.calls.length).toBe(listCallsAfterLoad);
    expect(deps.presets.error).toContain("default change incomplete");

    // The verification hint survives until the user explicitly reloads.
    await controller.dispatch({ type: "view.reload" });
    expect(listPresets.mock.calls.length).toBeGreaterThan(listCallsAfterLoad);
    scope.stop();
  });

  it("另一进程已降级旧默认（preset_edit_conflict）时也不目动补偿", async () => {
    const current = preset("current", "orders", true);
    const target = preset("target", "orders");
    const listPresets = vi.fn(async () => ({ collection: "orders", presets: [current, target] }));
    const savePreset = vi.fn(async (_collection: string, _name: string, view: unknown, target2: { id: string } | null) => {
      if (target2?.id === "target") {
        return { ...target, view: { ...(view as object) }, revision: "revision-target-default" };
      }
      // Another process already demoted the old default: the CAS rejects our
      // stale demotion, but that does not prove the old default is still set.
      throw new ProductRpcError("preset_edit_conflict", "expectedRevision", "Preset changed elsewhere.", null, false);
    });
    const service: PresetServicePort = {
      listPresets, savePreset: savePreset as unknown as PresetServicePort["savePreset"],
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;

    deps.workspace.selectTable("orders");
    await flushPromises();
    const listCallsAfterLoad = listPresets.mock.calls.length;

    await controller.dispatch({ type: "view.setDefault", view: target });

    // No third write: compensating B back to non-default could leave both
    // views non-default when another process already demoted A.
    expect(savePreset).toHaveBeenCalledTimes(2);
    expect(listPresets.mock.calls.length).toBe(listCallsAfterLoad);
    expect(deps.presets.error).toContain("default change incomplete");
    scope.stop();
  });

  it("当前视图保存失败时保持原视图激活", async () => {
    const current = preset("current", "orders", true);
    const target = preset("target", "orders");
    const service: PresetServicePort = {
      listPresets: vi.fn(async () => ({ collection: "orders", presets: [current, target] })),
      savePreset: vi.fn(async () => { throw new Error("save failed"); }),
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;

    deps.workspace.selectTable("orders");
    await flushPromises();
    deps.presets.markDirty();
    await controller.dispatch({ type: "view.switch", view: target });

    expect(deps.presets.activePresetId).toBe("current");
    expect(deps.presets.error).toBe("save failed");
    scope.stop();
  });

  it("冲突后保留本地 dirty 状态，直到用户显式重载权威 revision", async () => {
    const initial = preset("calendar", "orders", true);
    const winner = { ...initial, revision: "revision-calendar-winner", name: "服务器视图" };
    const listPresets = vi.fn()
      .mockResolvedValueOnce({ collection: "orders", presets: [initial] })
      .mockResolvedValueOnce({ collection: "orders", presets: [winner] });
    const savePreset = vi.fn(async () => {
      throw new Error("Preset changed elsewhere.");
    });
    const service: PresetServicePort = {
      listPresets,
      savePreset,
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;

    deps.workspace.selectTable("orders");
    await flushPromises();
    deps.presets.markDirty();
    await controller.dispatch({ type: "view.save", view: initial });

    expect(savePreset).toHaveBeenCalledWith(
      "orders",
      "calendar",
      expect.any(Object),
      { id: "calendar", revision: "revision-calendar" },
    );
    expect(deps.presets.dirty).toBe(true);
    expect(deps.presets.error).toBe("Preset changed elsewhere.");

    await controller.dispatch({ type: "view.reload" });

    expect(listPresets).toHaveBeenCalledTimes(2);
    expect(deps.presets.activePresetId).toBe("calendar");
    expect(deps.presets.presets[0]?.revision).toBe("revision-calendar-winner");
    expect(deps.presets.dirty).toBe(false);
    expect(deps.presets.error).toBeNull();
    scope.stop();
  });

  it("权威重载失败后恢复可操作错误状态并保留本地 dirty", async () => {
    const initial = preset("calendar", "orders", true);
    const listPresets = vi.fn()
      .mockResolvedValueOnce({ collection: "orders", presets: [initial] })
      .mockRejectedValueOnce(new Error("authoritative reload failed"));
    const service: PresetServicePort = {
      listPresets,
      savePreset: vi.fn(async () => initial),
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;

    deps.workspace.selectTable("orders");
    await flushPromises();
    deps.presets.markDirty();
    await controller.dispatch({ type: "view.reload" });

    expect(deps.presets.loading).toBe(false);
    expect(deps.presets.error).toBe("authoritative reload failed");
    expect(deps.presets.dirty).toBe(true);
    scope.stop();
  });
});
