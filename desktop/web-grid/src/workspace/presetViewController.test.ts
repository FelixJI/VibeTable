import { flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { effectScope, ref } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { PresetEntry, PresetsResult } from "@/contracts";
import type { HostBridge } from "@/bridge/hostBridge";
import type { DataSourceViewGrid } from "@/grid/dataSourceViewState";
import type { GridPresentationService } from "@/services/gridPresentationService";
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
    grid: { current: ref<DataSourceViewGrid | null>(null) },
    executeQuery: vi.fn(),
    refreshLookups: vi.fn(),
    reportError: vi.fn(),
    defaultChangeIncompleteError: (cause: unknown) => new Error("default change incomplete", { cause }),
    presentation: undefined as PresetViewDependencies["presentation"] | undefined,
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

  const idColumn = { name: "id", title: "行 ID", kind: "system", dataType: "text", editable: false, nullable: false } as const;
  const column = (name: string, title: string) => ({
    name, title, dataType: "text", editable: true, nullable: true,
  } as const);
  const gridColumn = (field: string, visible: boolean) => ({
    getField: () => field,
    getWidth: () => 120,
    isVisible: () => visible,
    getDefinition: () => ({ frozen: false }),
  });
  const gridStub = (
    columns: Array<ReturnType<typeof gridColumn>>,
    applied?: Array<Array<{ readonly field: string; readonly visible: boolean }>>,
  ): DataSourceViewGrid => ({
    initialized: true,
    getColumns: () => columns,
    getSorters: () => [],
    getHeaderFilters: () => [],
    ...(applied ? {
      applyPresentation: (presentation: Parameters<NonNullable<DataSourceViewGrid["applyPresentation"]>>[0]) => {
        applied.push(presentation.columns.map(item => ({ field: item.field, visible: item.visible })));
      },
    } : {}),
  });

  it("字段创建后的 schema 增量保持新列默认可见并随视图捕获持久", async () => {
    const savePreset = vi.fn(async (collection: string, name: string, view: PresetEntry["view"]) => ({
      ...preset("custom", collection), name, view,
    }));
    const service: PresetServicePort = {
      listPresets: vi.fn(async () => ({ collection: "orders", presets: [] })),
      savePreset,
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    deps.table.schema = [idColumn, column("title", "标题")];
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;
    deps.workspace.selectTable("orders");
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title"]);

    // 字段抽屉 apply 后 schema 增量：新列必须默认可见，否则捕获会把实际
    // 可见的新列重写成 visible:false，重开后还原为隐藏列。
    deps.table.schema = [idColumn, column("title", "标题"), column("amount", "金额")];
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title", "amount"]);

    await controller.dispatch({ type: "view.create", request: { name: "组合",
      kind: "table", dateField: null, endDateField: null, titleField: null, groupField: null, coverField: null } });
    const captured = savePreset.mock.calls[0]?.[2] as PresetEntry["view"];
    expect(captured.visibleFields).toEqual(["title", "amount"]);
    scope.stop();
  });

  it("捕获的视图列在新字段增量后保持 visible 并在重开时重新应用", async () => {
    // 最小 DataSourceViewGrid stub：提供新旧列的真实 layout（字段/可见性）。
    const applied: Array<Array<{ readonly field: string; readonly visible: boolean }>> = [];
    const gridRef = ref<DataSourceViewGrid | null>(null);
    let savedPreset: PresetEntry | null = null;
    const savePreset = vi.fn(async (collection: string, name: string, view: PresetEntry["view"]) => {
      savedPreset = { ...preset("custom", collection), name, view };
      return savedPreset;
    });
    const service: PresetServicePort = {
      listPresets: vi.fn(async () => ({
        collection: "orders",
        presets: savedPreset ? [savedPreset] : [],
      })),
      savePreset,
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    deps.grid = { current: gridRef };
    deps.table.schema = [idColumn, column("title", "标题")];
    gridRef.value = gridStub([gridColumn("id", false), gridColumn("title", true)], applied);
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;
    deps.workspace.selectTable("orders");
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title"]);

    // 字段创建后 schema 与网格同步通量：新列实际可见。
    deps.table.schema = [idColumn, column("title", "标题"), column("amount", "金额")];
    gridRef.value = gridStub([
      gridColumn("id", false), gridColumn("title", true), gridColumn("amount", true),
    ], applied);
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title", "amount"]);

    await controller.dispatch({ type: "view.create", request: { name: "组合",
      kind: "table", dateField: null, endDateField: null, titleField: null, groupField: null, coverField: null } });
    // 关键回归：捕获视图不得把实际可见的新列重写成 visible:false，
    // 行 ID 保持默认隐藏。
    const captured = savePreset.mock.calls[0]?.[2] as PresetEntry["view"];
    expect(captured.columns?.find(item => item.name === "amount")?.visible).toBe(true);
    expect(captured.columns?.find(item => item.name === "id")?.visible).toBe(false);

    // 重开：默认预设携带捕获列，重新应用后新列仍可见（真实时序：切表
    // 先置空 schema，schema 在 load 期间到达）。
    savedPreset = { ...savedPreset!, view: { ...savedPreset!.view, isDefault: true } };
    applied.length = 0;
    deps.table.schema = null;
    deps.workspace.currentTable = null;
    await flushPromises();
    gridRef.value = gridStub([
      gridColumn("id", false), gridColumn("title", true), gridColumn("amount", true),
    ], applied);
    deps.workspace.selectTable("orders");
    await flushPromises();
    deps.table.schema = [idColumn, column("title", "标题"), column("amount", "金额")];
    await flushPromises();
    const restored = applied.at(-1) ?? [];
    expect(restored.find(item => item.field === "amount")?.visible).toBe(true);
    expect(deps.query.visibleFields).toEqual(["title", "amount"]);
    scope.stop();
  });

  it("建表初期 id-only 的 memoryDefaults 不得在重载时锁死增长后 schema 的可见集", async () => {
    const gridRef = ref<DataSourceViewGrid | null>(null);
    const service: PresetServicePort = {
      listPresets: vi.fn(async (collection: string) => ({ collection, presets: [] })),
      savePreset: vi.fn(async () => { throw new Error("unexpected save"); }),
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    deps.grid = { current: gridRef };
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;

    // 真实时序：选表后（reset 清空可见集、loadCollection 尚未 applyView、
    // pendingView 为空）网格就绪，memoryDefaults 捕获空可见集 + id 列
    // visible:false。单一网格实例，列随 schema 原地增长。
    const gridColumns = [gridColumn("id", false)];
    deps.table.schema = [idColumn];
    deps.workspace.selectTable("orders");
    gridRef.value = gridStub(gridColumns);
    await flushPromises();
    expect(deps.query.visibleFields).toEqual([]);

    // 首个真实页面到达：schema 增长，可见集自愈为全部非 ID 字段。
    gridColumns.push(gridColumn("title", true), gridColumn("quantity", true));
    deps.table.schema = [idColumn, column("title", "标题"), column("quantity", "数量")];
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title", "quantity"]);

    // 无预设时 presentation.reload 回退到陈旧 memoryDefaults：空可见集
    // + id visible:false 不得把已增长的 schema 锁成全部隐藏。
    await controller.dispatch({ type: "presentation.reload" });
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title", "quantity"]);

    // 后续字段创建（S05 连续公式列）不得把可见集重置为仅最新列。
    gridColumns.push(gridColumn("formula_4", true));
    deps.table.schema = [
      idColumn, column("title", "标题"), column("quantity", "数量"), column("formula_4", "日期文本"),
    ];
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title", "quantity", "formula_4"]);
    scope.stop();
  });

  it("旧默认预设在重载时保留显式隐藏并让新增字段默认可见", async () => {
    const gridRef = ref<DataSourceViewGrid | null>(null);
    // 保存时带列布局：title 可见，业务字段 secret 被用户显式隐藏。
    const savedDefault = {
      ...preset("saved-default", "orders", true),
      view: {
        ...preset("saved-default", "orders", true).view,
        columns: [
          { name: "id", order: 0, visible: false, frozen: false, width: null },
          { name: "title", order: 1, visible: true, frozen: false, width: null },
          { name: "secret", order: 2, visible: false, frozen: false, width: null },
        ],
        visibleFields: ["title"],
      },
    };
    const service: PresetServicePort = {
      listPresets: vi.fn(async (collection: string) => ({ collection, presets: [savedDefault] })),
      savePreset: vi.fn(async () => { throw new Error("unexpected save"); }),
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    deps.grid = { current: gridRef };
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;

    const gridColumns = [gridColumn("id", false), gridColumn("title", true), gridColumn("secret", false)];
    deps.table.schema = [idColumn, column("title", "标题"), column("secret", "机密")];
    gridRef.value = gridStub(gridColumns);
    await flushPromises();
    deps.workspace.selectTable("orders");
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title"]);

    // 预设保存后新增 amount（无本地 presentation 匹配时走 baseline 回退路径）。
    gridColumns.push(gridColumn("amount", true));
    deps.table.schema = [
      idColumn, column("title", "标题"), column("secret", "机密"), column("amount", "金额"),
    ];
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title", "amount"]);

    // reload：旧预设的显式隐藏（secret/id）保留，新增字段（amount）默认可见。
    await controller.dispatch({ type: "presentation.reload" });
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title", "amount"]);
    scope.stop();
  });

  it("切换到旧预设时新增字段默认可见且捕获列不落 visible:false", async () => {
    const gridRef = ref<DataSourceViewGrid | null>(null);
    const applied: Array<Array<{ readonly field: string; readonly visible: boolean }>> = [];
    const withColumns = (id: string, isDefault = false) => ({
      ...preset(id, "orders", isDefault),
      view: {
        ...preset(id, "orders", isDefault).view,
        columns: [
          { name: "id", order: 0, visible: false, frozen: false, width: null },
          { name: "title", order: 1, visible: true, frozen: false, width: null },
        ],
        visibleFields: ["title"],
      },
    });
    const first = withColumns("old-default", true);
    const second = withColumns("old-switch");
    const service: PresetServicePort = {
      listPresets: vi.fn(async () => ({ collection: "orders", presets: [first, second] })),
      savePreset: vi.fn(async () => { throw new Error("unexpected save"); }),
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    deps.grid = { current: gridRef };
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;

    // 预设保存时只有 id/title；当前 schema 已增长出 amount。
    const gridColumns = [gridColumn("id", false), gridColumn("title", true), gridColumn("amount", true)];
    deps.table.schema = [idColumn, column("title", "标题"), column("amount", "金额")];
    gridRef.value = gridStub(gridColumns, applied);
    await flushPromises();
    deps.workspace.selectTable("orders");
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title", "amount"]);

    // 切换到另一旧预设：query 不得排除 amount，应用到网格的捕获列
    // amount 必须 visible:true（否则随后的 savePresentation 会把实际可见
    // 的新列持久化为 visible:false）。
    applied.length = 0;
    await controller.dispatch({ type: "view.switch", view: second });
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title", "amount"]);
    const restored = applied.at(-1) ?? [];
    expect(restored.find(item => item.field === "amount")?.visible).toBe(true);
    expect(restored.find(item => item.field === "id")?.visible).toBe(false);
    scope.stop();
  });

  it("schema 未知时切换旧预设在 schema 回来后保留旧隐藏并让新增字段可见", async () => {
    const gridRef = ref<DataSourceViewGrid | null>(null);
    const applied: Array<Array<{ readonly field: string; readonly visible: boolean }>> = [];
    const savedStates: Array<{ columns?: Array<{ name: string; visible: boolean }> }> = [];
    const persistenceService = {
      read: vi.fn(async () => ({ state: null, revision: "r1", conflict: false })),
      save: vi.fn(async (_table: string, state: { columns?: Array<{ name: string; visible: boolean }> }) => {
        savedStates.push(state);
        return { state, revision: "r-next", conflict: false };
      }),
    } as unknown as GridPresentationService;
    const withColumns = (id: string, isDefault = false) => ({
      ...preset(id, "orders", isDefault),
      view: {
        ...preset(id, "orders", isDefault).view,
        columns: [
          { name: "id", order: 0, visible: false, frozen: false, width: null },
          { name: "title", order: 1, visible: true, frozen: false, width: null },
          { name: "secret", order: 2, visible: false, frozen: false, width: null },
        ],
        visibleFields: ["title"],
      },
    });
    const first = withColumns("old-default", true);
    const second = withColumns("old-switch");
    // 无列布局的合法旧预设：既有 visibleFields 精确集合语义。
    const third = {
      ...preset("legacy-no-columns", "orders"),
      view: { ...preset("legacy-no-columns", "orders").view, visibleFields: ["title"] },
    };
    const service: PresetServicePort = {
      listPresets: vi.fn(async () => ({ collection: "orders", presets: [first, second, third] })),
      savePreset: vi.fn(async () => { throw new Error("unexpected save"); }),
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    deps.grid = { current: gridRef };
    deps.presentation = { service: persistenceService, identity: ref<string | null>("device-1") };
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;

    const gridColumns = [gridColumn("id", false), gridColumn("title", true), gridColumn("secret", false)];
    deps.table.schema = [idColumn, column("title", "标题"), column("secret", "机密")];
    gridRef.value = gridStub(gridColumns, applied);
    await flushPromises();
    deps.workspace.selectTable("orders");
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title"]);

    // 真实 beginLoad：schema 置空；view.switch 只查 presets.loading，可在此
    // 窗口触发。不得把待应用视图按空 schema 清空。
    deps.table.schema = null;
    await flushPromises();
    applied.length = 0;
    savedStates.length = 0;
    await controller.dispatch({ type: "view.switch", view: second });
    await flushPromises();
    // 等待期间不得把旧 grid/query 捕获成新预设布局持久化。
    expect(savedStates).toHaveLength(0);

    // schema 回来且已增长 amount：旧隐藏（secret/id）保留，新增默认可见。
    gridColumns.push(gridColumn("amount", true));
    deps.table.schema = [
      idColumn, column("title", "标题"), column("secret", "机密"), column("amount", "金额"),
    ];
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title", "amount"]);
    const restored = applied.at(-1) ?? [];
    expect(restored.find(item => item.field === "amount")?.visible).toBe(true);
    expect(restored.find(item => item.field === "secret")?.visible).toBe(false);
    expect(restored.find(item => item.field === "id")?.visible).toBe(false);

    // schema 回来后的后续保存携带 reconcile 后的布局（新列可见、旧隐藏保留）。
    await controller.dispatch({ type: "keyword.changed", keyword: "稳定" });
    await flushPromises();
    const saved = savedStates.at(-1);
    expect(saved?.columns?.find(item => item.name === "amount")?.visible).toBe(true);
    expect(saved?.columns?.find(item => item.name === "secret")?.visible).toBe(false);
    expect(saved?.columns?.find(item => item.name === "id")?.visible).toBe(false);

    // latest-intent：schema 再次未知时先暂存带列布局的 first，再切到无列
    // 布局的 third 直接应用；schema 回来后不得让遗留的 first 覆盖 third。
    deps.presets.markSaved();
    deps.table.schema = null;
    await flushPromises();
    await controller.dispatch({ type: "view.switch", view: first });
    await controller.dispatch({ type: "view.switch", view: third });
    await flushPromises();
    deps.table.schema = [
      idColumn, column("title", "标题"), column("secret", "机密"), column("amount", "金额"),
    ];
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title"]);
    scope.stop();
  });

  it.each(["kanban", "gallery"])("%s 视图隐藏字段保存后切回保留新选择且列同步", async k => {
    const gridRef = ref<DataSourceViewGrid | null>(null);
    const applied: Array<Array<{ readonly field: string; readonly visible: boolean }>> = [];
    const baseView = (kind: string): PresetEntry["view"] => ({
      ...preset(`old-${kind}`, "orders", true).view,
      kind: kind as "kanban" | "gallery",
      layout: kind,
      columns: [
        { name: "id", order: 0, visible: false, frozen: false, width: null },
        { name: "title", order: 1, visible: true, frozen: false, width: null },
        { name: "amount", order: 2, visible: true, frozen: false, width: null },
      ],
      visibleFields: ["title", "amount"],
    });
    const savedViews: Array<PresetEntry["view"]> = [];
    const savedLocal: Array<{ columns?: Array<{ name: string; visible: boolean }> }> = [];
    const persistenceService = {
      read: vi.fn(async () => ({
        state: savedLocal.at(-1) ?? null,
        revision: "r1", conflict: false,
      })),
      save: vi.fn(async (_table: string, state: { columns?: Array<{ name: string; visible: boolean }> }) => {
        savedLocal.push(state);
        return { state, revision: "r-next", conflict: false };
      }),
    } as unknown as GridPresentationService;
    let revision = 0;
    const kanbanEntry0 = { ...preset(`old-${k}`, "orders", true), view: baseView(k) };
    let kanbanListed = kanbanEntry0;
    const plainEntry = { ...preset("plain-table", "orders"), view: {
      ...preset("plain-table", "orders").view,
      visibleFields: ["title", "amount"],
    } };
    const service: PresetServicePort = {
      listPresets: vi.fn(async () => ({ collection: "orders", presets: [kanbanListed, plainEntry] })),
      savePreset: vi.fn(async (collection: string, _name: string, view: PresetEntry["view"], target: { id: string } | null) => {
        savedViews.push(view);
        revision += 1;
        const receipt = {
          id: target?.id ?? `saved-${revision}`, collection, name: `saved-${revision}`,
          scope: "system", revision: `rev-${revision}`, emittedEvents: [],
          view,
        } as PresetEntry;
        if (target?.id === kanbanListed.id) kanbanListed = receipt;
        return receipt;
      }),
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    deps.grid = { current: gridRef };
    deps.presentation = { service: persistenceService, identity: ref<string | null>("device-1") };
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;

    const gridColumns = [gridColumn("id", false), gridColumn("title", true), gridColumn("amount", true)];
    deps.table.schema = [idColumn, column("title", "标题"), column("amount", "金额")];
    gridRef.value = gridStub(gridColumns, applied);
    await flushPromises();
    deps.workspace.selectTable("orders");
    await flushPromises();
    expect(controller.activeKind.value).toBe(k);
    expect(deps.query.visibleFields).toEqual(["title", "amount"]);

    // 看板/画廊里用户合法隐藏 amount（显示字段面板）并保存当前视图。
    await controller.dispatch({ type: "definition.changed", definition: {
      filters: [], groups: [], summaries: [], visibleFields: ["title"],
    } });
    const active = () => deps.presets.presets.find(item => item.id === deps.presets.activePresetId)!;
    await controller.dispatch({ type: "view.save", view: active() });
    expect(savedViews[0]?.visibleFields).toEqual(["title"]);
    // 本地保存渠道（savePresentation 只持久 columns）：columns 必须同步
    // 当前选择，否则本地恢复也会丢掉用户选择。
    expect(savedLocal[0]?.columns?.find(item => item.name === "amount")?.visible).toBe(false);
    expect(savedLocal[0]?.columns?.find(item => item.name === "title")?.visible).toBe(true);
    // 本地 restore：重载后用户选择保留。
    await controller.dispatch({ type: "presentation.reload" });
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title"]);
    expect(controller.activeKind.value).toBe(k);

    // 切走（表视图）并新增 note 字段，再切回：%s 视图的新选择（amount 隐藏）
    // 保留，新增字段默认可见，且 columns.visible 与最终选择同步。
    const plain = () => deps.presets.presets.find(item => item.id === "plain-table")!;
    await controller.dispatch({ type: "view.switch", view: plain() });
    await flushPromises();
    gridColumns.push(gridColumn("note", true));
    deps.table.schema = [
      idColumn, column("title", "标题"), column("amount", "金额"), column("note", "备注"),
    ];
    await flushPromises();
    const kanbanEntry = () => deps.presets.presets.find(item => item.id === `old-${k}`)!;
    await controller.dispatch({ type: "view.switch", view: kanbanEntry() });
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title", "note"]);
    expect(controller.activeKind.value).toBe(k);

    await controller.dispatch({ type: "view.save", view: kanbanEntry() });
    const resaved = savedViews.at(-1)!;
    expect(resaved.visibleFields).toEqual(["title", "note"]);
    expect(resaved.columns?.find(item => item.name === "amount")?.visible).toBe(false);
    expect(resaved.columns?.find(item => item.name === "title")?.visible).toBe(true);

    // 新增字段 note 被用户显式隐藏并保存：captureCurrent 必须把 note 补进
    // columns 布局（否则后续 applyView 永远把它当新增默认显示）。
    deps.presets.markSaved();
    await controller.dispatch({ type: "definition.changed", definition: {
      filters: [], groups: [], summaries: [], visibleFields: ["title"],
    } });
    await controller.dispatch({ type: "view.save", view: kanbanEntry() });
    const hiddenNew = savedViews.at(-1)!;
    expect(hiddenNew.visibleFields).toEqual(["title"]);
    expect(hiddenNew.columns?.find(item => item.name === "note")?.visible).toBe(false);
    expect(savedLocal.at(-1)?.columns?.find(item => item.name === "note")?.visible).toBe(false);
    // 切走再切回：note 保持隐藏，不再被当作新增默认显示。
    await controller.dispatch({ type: "view.switch", view: plain() });
    await flushPromises();
    await controller.dispatch({ type: "view.switch", view: kanbanEntry() });
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title"]);
    scope.stop();
  });

  it("无列布局的看板预设隐藏字段后本地恢复仍保持隐藏", async () => {
    const gridRef = ref<DataSourceViewGrid | null>(null);
    // 合法契约：无 columns，仅顶层 visibleFields 精确集合。
    // 合法契约：无 columns，仅顶层 visibleFields 精确集合。
    let kanbanListed: PresetEntry = {
      ...preset("bare-kanban", "orders", true),
      view: {
        ...preset("bare-kanban", "orders", true).view,
        kind: "kanban",
        layout: "kanban",
        visibleFields: ["title", "amount"],
      },
    };
    const savedViews: Array<PresetEntry["view"]> = [];
    const savedLocal: Array<{ columns?: Array<{ name: string; visible: boolean }> }> = [];
    const persistenceService = {
      read: vi.fn(async () => ({ state: savedLocal.at(-1) ?? null, revision: "r1", conflict: false })),
      save: vi.fn(async (_table: string, state: { columns?: Array<{ name: string; visible: boolean }> }) => {
        savedLocal.push(state);
        return { state, revision: "r-next", conflict: false };
      }),
    } as unknown as GridPresentationService;
    const service: PresetServicePort = {
      listPresets: vi.fn(async () => ({ collection: "orders", presets: [kanbanListed] })),
      savePreset: vi.fn(async (collection: string, _name: string, view: PresetEntry["view"], target: { id: string } | null) => {
        savedViews.push(view);
        const receipt = {
          id: target?.id ?? "saved-1", collection, name: "saved", scope: "system" as const,
          revision: "rev-2", emittedEvents: [], view,
        } as PresetEntry;
        if (target?.id === kanbanListed.id) kanbanListed = receipt;
        return receipt;
      }),
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    deps.grid = { current: gridRef };
    deps.presentation = { service: persistenceService, identity: ref<string | null>("device-1") };
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;

    deps.table.schema = [idColumn, column("title", "标题"), column("amount", "金额")];
    gridRef.value = gridStub([gridColumn("id", false), gridColumn("title", true), gridColumn("amount", true)]);
    await flushPromises();
    deps.workspace.selectTable("orders");
    await flushPromises();
    expect(controller.activeKind.value).toBe("kanban");
    expect(deps.query.visibleFields).toEqual(["title", "amount"]);

    // 用户隐藏 amount 并保存：本地持久化不得留下空 columns（否则重载把
    // 全部 schema 判为新增恢复全显）。
    await controller.dispatch({ type: "definition.changed", definition: {
      filters: [], groups: [], summaries: [], visibleFields: ["title"],
    } });
    const active = () => deps.presets.presets.find(item => item.id === deps.presets.activePresetId)!;
    await controller.dispatch({ type: "view.save", view: active() });
    const savedLayout = savedLocal.at(-1)?.columns ?? [];
    expect(savedLayout.find(item => item.name === "amount")?.visible).toBe(false);
    expect(savedLayout.find(item => item.name === "title")?.visible).toBe(true);
    expect(savedViews.at(-1)?.columns?.find(item => item.name === "amount")?.visible).toBe(false);

    await controller.dispatch({ type: "presentation.reload" });
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title"]);
    scope.stop();
  });

  it("空表首个用户字段在增量后默认可见", async () => {
    const service: PresetServicePort = {
      listPresets: vi.fn(async (collection: string) => ({ collection, presets: [] })),
      savePreset: vi.fn(async () => { throw new Error("unexpected save"); }),
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    // 空表只有默认隐藏的行 ID：可见集为空但不代表“未初始化”。
    deps.table.schema = [idColumn];
    const scope = effectScope();
    scope.run(() => createPresetViewController(deps));
    deps.workspace.selectTable("orders");
    await flushPromises();
    expect(deps.query.visibleFields).toEqual([]);

    deps.table.schema = [idColumn, column("title", "标题")];
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title"]);
    scope.stop();
  });

  it("恢复的预设隐藏列在 schema 增量后保持隐藏", async () => {
    const hidden = {
      ...preset("saved", "orders", true),
      view: { ...preset("saved", "orders", true).view, visibleFields: ["title"] },
    };
    const savePresetHidden = vi.fn(async (collection: string, name: string, view: PresetEntry["view"]) => ({
      ...preset("custom", collection), name, view,
    }));
    const service: PresetServicePort = {
      listPresets: vi.fn(async () => ({ collection: "orders", presets: [hidden] })),
      savePreset: savePresetHidden,
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    // 初始 schema 在 controller 创建前已存在：已知字段（含预设隐藏的
    // amount）不能在后续增量中被当作新增揭开。
    deps.table.schema = [idColumn, column("title", "标题"), column("amount", "金额")];
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;
    deps.workspace.selectTable("orders");
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title"]);

    deps.table.schema = [
      idColumn, column("title", "标题"), column("amount", "金额"), column("note", "备注"),
    ];
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title", "note"]);

    await controller.dispatch({ type: "view.create", request: { name: "组合",
      kind: "table", dateField: null, endDateField: null, titleField: null, groupField: null, coverField: null } });
    const captured = savePresetHidden.mock.calls[0]?.[2] as PresetEntry["view"];
    expect(captured.visibleFields).toEqual(["title", "note"]);
    scope.stop();
  });

  it("显式隐藏与已删字段的增量语义独立，隐藏全部在增量后保持", async () => {
    const service: PresetServicePort = {
      listPresets: vi.fn(async (collection: string) => ({ collection, presets: [] })),
      savePreset: vi.fn(async () => { throw new Error("unexpected save"); }),
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    deps.table.schema = [idColumn, column("title", "标题"), column("amount", "金额")];
    const scope = effectScope();
    scope.run(() => createPresetViewController(deps));
    deps.workspace.selectTable("orders");
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title", "amount"]);

    // 用户显式隐藏仍存在的 amount；新增 note 默认可见。
    deps.query.updateDefinition({ filters: [], groups: [], summaries: [], visibleFields: ["title"] });
    deps.table.schema = [
      idColumn, column("title", "标题"), column("amount", "金额"), column("note", "备注"),
    ];
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title", "note"]);

    // 删除 amount：从可见集清退，不得回添。
    deps.table.schema = [idColumn, column("title", "标题"), column("note", "备注")];
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title", "note"]);

    // 隐藏全部：仅针对已知字段；无新字段的 schema 更新（metadata-only）
    // 保持全隐，真正的新字段仍按默认可见。
    deps.query.updateDefinition({ filters: [], groups: [], summaries: [], visibleFields: [] });
    deps.table.schema = [idColumn, column("title", "标题"), column("note", "备注")];
    await flushPromises();
    expect(deps.query.visibleFields).toEqual([]);
    deps.table.schema = [
      idColumn, column("title", "标题"), column("note", "备注"), column("phone", "电话"),
    ];
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["phone"]);
    scope.stop();
  });

  it("字段变更刷新期间返回完整视图不会把保留网格持久化为全隐藏", async () => {
    const service: PresetServicePort = {
      listPresets: vi.fn(async () => ({ collection: "orders", presets: [] })),
      savePreset: vi.fn(async () => { throw new Error("unexpected save"); }),
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    const gridColumns = [gridColumn("id", false), gridColumn("quantity", true)];
    deps.grid.current.value = gridStub(gridColumns, []);
    deps.table.schema = [idColumn, column("quantity", "数量")];
    const scope = effectScope();
    const controller = scope.run(() => createPresetViewController(deps))!;
    deps.workspace.selectTable("orders");
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["quantity"]);

    // apply 的后台刷新先 beginLoad；抽屉关闭后“全部记录”仍可点击，
    // 此时 Tabulator 保留旧列，但最新 schema 尚未送达。
    deps.table.schema = null;
    await flushPromises();
    await controller.dispatch({ type: "view.reset" });
    await flushPromises();
    gridColumns.push(gridColumn("amount", true));
    deps.table.schema = [idColumn, column("quantity", "数量"), column("amount", "金额")];
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["quantity", "amount"]);
    scope.stop();
  });

  it("跨表同名字段不携带上一表的隐藏语义", async () => {
    const customersHiddenTitle = {
      ...preset("customers-view", "customers", true),
      view: { ...preset("customers-view", "customers", true).view, visibleFields: ["cname"] },
    };
    const service: PresetServicePort = {
      listPresets: vi.fn(async (collection: string) => ({
        collection,
        presets: collection === "customers" ? [customersHiddenTitle] : [],
      })),
      savePreset: vi.fn(async () => { throw new Error("unexpected save"); }),
      deletePreset: vi.fn(async () => undefined),
    };
    const deps = dependencies(service);
    deps.table.schema = [idColumn, column("title", "标题"), column("amount", "金额")];
    const scope = effectScope();
    scope.run(() => createPresetViewController(deps));
    deps.workspace.selectTable("orders");
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["title", "amount"]);

    // customers 与 orders 同名字段 title 被其默认预设隐藏；orders 的可见集
    // 与跟踪集不得让 title 在 customers 的增量中被当作已知可见/新增揭开。
    // 真实时序：切表先把 schema 置空，schema 在 loadCollection 期间到达，
    // 由 pendingRestore 分支同步跟踪集并完成恢复。
    deps.table.schema = null;
    deps.workspace.selectTable("customers");
    await flushPromises();
    deps.table.schema = [idColumn, column("title", "标题"), column("cname", "客户")];
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["cname"]);

    deps.table.schema = [
      idColumn, column("title", "标题"), column("cname", "客户"), column("phone", "电话"),
    ];
    await flushPromises();
    expect(deps.query.visibleFields).toEqual(["cname", "phone"]);
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
