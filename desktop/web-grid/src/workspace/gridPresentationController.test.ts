import { flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { effectScope, ref } from "vue";
import { beforeEach, expect, it, vi } from "vitest";
import type { ColumnState, GridState, GridStateResult, PresetEntry, PresetsResult } from "@/contracts";
import type { DataSourceViewGrid } from "@/grid/dataSourceViewState";
import { usePresetVersionStore } from "@/stores/presetVersionStore";
import { useTableStore } from "@/stores/tableStore";
import { useUiStore } from "@/stores/uiStore";
import { useViewQueryStore } from "@/stores/viewQueryStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { createPresetViewController } from "./presetViewController";

function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done; }); return { promise, resolve }; }
const preset: PresetEntry = { id: "default", collection: "orders", name: "Shared", revision: "p1", scope: "system", emittedEvents: [],
  view: { kind: "table", layout: "table", columns: [], filters: [], sorts: [], search: "shared", visibleFields: ["title", "status"] } };
const local: GridState = { columns: [{ name: "status", order: 0, width: 90, frozen: true, visible: false }, { name: "title", order: 1, width: 240, visible: true }],
  sorts: [{ field: "title", direction: "desc" }], filters: [{ groupLogic: "OR", filters: [{ field: "title", operator: "eq", value: "" }, { field: "status", operator: "eq", value: false }] }],
  keyword: "local", density: "compact", forcedRemote: true, presetId: "default", presetRevision: "p1" };

beforeEach(() => setActivePinia(createPinia()));
function fixture() {
  const workspace = useWorkspaceStore();
  const table = useTableStore();
  const query = useViewQueryStore();
  const ui = useUiStore();
  const presets = usePresetVersionStore();
  const identity = ref<string | null>("workspace:7");
  const hostRead = deferred<GridStateResult>();
  const presetRead = deferred<PresetsResult>();
  let columns: ColumnState[] = [{ name: "title", width: 120 }, { name: "status", width: 120 }];
  const layout = vi.fn((next: readonly { field: string; width?: number; visible: boolean; frozen: boolean }[]) => {
    columns = next.map((column, order) => ({ name: column.field, order, ...column }));
  });
  const grid: DataSourceViewGrid = {
    getColumns: () => columns.map(column => ({ getField: () => column.name, getWidth: () => column.width ?? 120,
      isVisible: () => column.visible !== false, getDefinition: () => ({ frozen: column.frozen }) })),
    setColumnLayout: layout, setSort: vi.fn(), clearHeaderFilter: vi.fn(), setHeaderFilterValue: vi.fn(),
  };
  const savePreset = vi.fn(async () => preset);
  const listPresets = vi.fn(() => presetRead.promise);
  const save = vi.fn(async (_table: string, state: GridState) => ({ state, revision: "r2", conflict: false }));
  const read = vi.fn(() => hostRead.promise);
  const lifetime = effectScope();
  const executeQuery = vi.fn();
  const controller = lifetime.run(() => createPresetViewController({ workspace, table, query, ui, presets,
    grid: { current: ref(grid) }, presentation: { identity, service: { read, save } },
    service: { listPresets, savePreset, deletePreset: async () => {} },
    executeQuery, refreshLookups: vi.fn(), reportError: vi.fn(), defaultCompensationError: () => new Error("compensation"),
  }))!;
  workspace.selectTable("orders");
  table.schema = ["title", "status"].map(name => ({ name, title: name, dataType: "text", editable: false, nullable: true }));
  return { workspace, table, query, ui, presets, identity, hostRead, presetRead, layout, grid, savePreset, listPresets, save, read, controller, lifetime, executeQuery };
}

it("abandons a preset switch whose flush finishes after another table has restored", async () => {
  const f = fixture();
  const next = { ...preset, id: "next", view: { ...preset.view, search: "A next" } };
  f.hostRead.resolve({ state: local, revision: "r1", conflict: false });
  f.presetRead.resolve({ collection: "orders", presets: [preset, next] });
  await flushPromises();
  const saving = deferred<GridStateResult>();
  f.save.mockImplementationOnce(() => saving.promise);
  await f.controller.dispatch({ type: "density.changed", density: "comfortable" });
  const switching = f.controller.dispatch({ type: "view.switch", view: next });
  const other = { ...preset, id: "other", collection: "other", view: { ...preset.view, search: "B current" } };
  f.listPresets.mockResolvedValue({ collection: "other", presets: [other] });
  f.read.mockResolvedValue({ state: {}, revision: "b1", conflict: false });
  f.workspace.selectTable("other");
  await flushPromises();
  expect(f.query.search).toBe("B current");
  const saveCount = f.save.mock.calls.length;
  saving.resolve({ state: local, revision: "r2", conflict: false });
  await switching;
  await flushPromises();
  expect(f.presets.activePresetId).toBe("other");
  expect(f.query.search).toBe("B current");
  expect(f.save).toHaveBeenCalledTimes(saveCount);
  expect(f.savePreset).not.toHaveBeenCalled();
  f.lifetime.stop();
});

it.each([true, false])("restores full local state only after both sources, hostFirst=%s", async hostFirst => {
  const f = fixture(); await flushPromises();
  if (hostFirst) f.hostRead.resolve({ state: local, revision: "r1", conflict: false });
  else f.presetRead.resolve({ collection: "orders", presets: [preset] });
  await flushPromises(); expect(f.layout).not.toHaveBeenCalled(); expect(f.save).not.toHaveBeenCalled();
  expect(f.controller.presentationLoading.value).toBe(true);
  await f.controller.dispatch({ type: "keyword.changed", keyword: "input before restore is ready" });
  expect(f.executeQuery).not.toHaveBeenCalled();
  f.hostRead.resolve({ state: local, revision: "r1", conflict: false });
  f.presetRead.resolve({ collection: "orders", presets: [preset] });
  await flushPromises();
  expect(f.controller.presentationLoading.value).toBe(false);
  expect(f.query.toQuery()).toMatchObject({ keyword: "local", filters: local.filters, sorts: local.sorts });
  expect(f.ui.density).toBe("compact"); expect(f.query.visibleFields).toEqual(["title"]);
  expect(f.layout).toHaveBeenLastCalledWith([{ field: "status", width: 90, frozen: true, visible: false }, { field: "title", width: 240, frozen: false, visible: true }]);
  expect(f.grid.setHeaderFilterValue).not.toHaveBeenCalled();
  expect(f.presets.dirty).toBe(false); expect(f.savePreset).not.toHaveBeenCalled(); expect(f.save).not.toHaveBeenCalled();
  await f.controller.dispatch({ type: "keyword.changed", keyword: "later" }); await f.controller.flush();
  expect(f.save).toHaveBeenLastCalledWith("orders", expect.objectContaining({ columns: expect.arrayContaining(local.columns!.map(column => expect.objectContaining(column))), keyword: "later", filters: local.filters, forcedRemote: true }), "r1");
  f.lifetime.stop();
});

it("keeps a newer shared baseline and does not implicitly save a restored preset on switch", async () => {
  const f = fixture();
  const newer = { ...preset, revision: "p2" };
  const other = { ...preset, id: "other", revision: "other1" };
  f.hostRead.resolve({ state: local, revision: "r1", conflict: false });
  f.presetRead.resolve({ collection: "orders", presets: [newer, other] }); await flushPromises();
  expect(f.query.search).toBe("shared");
  await f.controller.dispatch({ type: "view.switch", view: other });
  expect(f.savePreset).not.toHaveBeenCalled();
  expect(f.presets.activePresetId).toBe("other");
  f.lifetime.stop();
});

it("retires old session loads before scope publishes the new epoch", async () => {
  const f = fixture(); await flushPromises();
  f.identity.value = null;
  f.hostRead.resolve({ state: local, revision: "r1", conflict: false });
  f.presetRead.resolve({ collection: "orders", presets: [preset] }); await flushPromises();
  expect(f.layout).not.toHaveBeenCalled(); expect(f.query.search).toBe(""); expect(f.save).not.toHaveBeenCalled();
  f.identity.value = "workspace:8"; await flushPromises();
  expect(f.read).toHaveBeenCalledTimes(2);
  expect(f.query.search).toBe("local");
  f.lifetime.stop();
});

it("preserves legacy cozy data while rendering comfortable until an explicit density change", async () => {
  const f = fixture();
  f.hostRead.resolve({ state: { ...local, density: "cozy" }, revision: "r1", conflict: false });
  f.presetRead.resolve({ collection: "orders", presets: [preset] }); await flushPromises();
  expect(f.ui.density).toBe("comfortable");
  await f.controller.dispatch({ type: "keyword.changed", keyword: "keep cozy" }); await f.controller.flush();
  expect(f.save.mock.calls.at(-1)?.[1].density).toBe("cozy");
  await f.controller.dispatch({ type: "density.changed", density: "compact" }); await f.controller.flush();
  expect(f.save.mock.calls.at(-1)?.[1].density).toBe("compact");
  f.lifetime.stop();
});

it("finishes the captured table's latest queued edit across synchronous table switches before reopening", async () => {
  const f = fixture();
  f.hostRead.resolve({ state: local, revision: "r1", conflict: false });
  f.presetRead.resolve({ collection: "orders", presets: [preset] });
  await flushPromises();
  const firstSave = deferred<GridStateResult>();
  let stored = local;
  f.save.mockImplementationOnce(async (_table, state) => {
    stored = state;
    return await firstSave.promise;
  });
  f.save.mockImplementation(async (_table, state) => {
    stored = state;
    return { state, revision: "r3", conflict: false };
  });
  f.read.mockImplementation(async () => ({ state: stored, revision: "r3", conflict: false }));
  await f.controller.dispatch({ type: "keyword.changed", keyword: "first" });
  await f.controller.dispatch({ type: "keyword.changed", keyword: "latest" });
  f.workspace.selectTable("other");
  await flushPromises();
  f.workspace.selectTable("orders");
  await flushPromises();
  expect(f.query.search).toBe("");
  firstSave.resolve({ state: stored, revision: "r2", conflict: false });
  await flushPromises();
  expect(f.save).toHaveBeenLastCalledWith("orders", expect.objectContaining({ keyword: "latest" }), "r2");
  expect(f.query.search).toBe("latest");
  expect(f.savePreset).not.toHaveBeenCalled();
  f.lifetime.stop();
});
