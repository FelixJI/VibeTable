import { flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { effectScope, ref } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { PresetEntry, PresetsResult } from "@/contracts";
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
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
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
    defaultCompensationError: () => new Error("default compensation failed"),
  };
}

describe("presetViewController", () => {
  beforeEach(() => setActivePinia(createPinia()));

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

  it("默认视图降级失败时撤销刚完成的提升并重新加载权威列表", async () => {
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
      if (id === first.id && !view.isDefault) throw new Error("demotion failed");
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
      { target: { id: "second", revision: "second-saved-1" }, isDefault: false },
    ]);
    expect(listPresets).toHaveBeenCalledTimes(2);
    expect(deps.presets.presets.find(item => item.id === "first")?.view.isDefault).toBe(true);
    expect(deps.presets.activePresetId).toBe("first");
    scope.stop();
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
