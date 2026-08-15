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
    const saves: Array<{ id: string | null | undefined; isDefault: boolean }> = [];
    const listPresets = vi.fn(async () => ({
      collection: "orders",
      presets: [first, second],
    }));
    const savePreset = vi.fn(async (
      collection: string,
      name: string,
      view: PresetEntry["view"],
      id?: string | null,
    ) => {
      saves.push({ id, isDefault: !!view.isDefault });
      if (id === first.id && !view.isDefault) throw new Error("demotion failed");
      return {
        ...(id === first.id ? first : second),
        collection,
        name,
        view,
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
      { id: "second", isDefault: true },
      { id: "first", isDefault: false },
      { id: "second", isDefault: false },
    ]);
    expect(listPresets).toHaveBeenCalledTimes(2);
    expect(deps.presets.presets.find(item => item.id === "first")?.view.isDefault).toBe(true);
    expect(deps.presets.activePresetId).toBe("first");
    scope.stop();
  });
});
