import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { defaultVisibleFields, useViewQueryStore } from "./viewQueryStore";

describe("viewQueryStore", () => {
  it("replaces a managed OR group again without retaining the previous selection", () => {
    const store=useViewQueryStore();
    store.reset("orders",["note"]);
    store.applyHeaderValueFilter("note",[null,"a"]);
    store.updateRuntime({headerFilters:store.headerFilters,sorts:[],groups:[]});
    expect(store.filters).toEqual([{groupLogic:"OR",filters:[{field:"note",operator:"is_null"},{field:"note",operator:"eq",value:"a"}]}]);
    store.applyHeaderValueFilter("note",[null,"b"]);
    store.updateRuntime({headerFilters:store.headerFilters,sorts:[],groups:[]});
    expect(store.filters).toEqual([{groupLogic:"OR",filters:[{field:"note",operator:"is_null"},{field:"note",operator:"eq",value:"b"}]}]);
    store.applyHeaderValueFilter("note",[]);
    store.updateRuntime({headerFilters:store.headerFilters,sorts:[],groups:[]});
    expect(store.filters).toEqual([]);
  });
  beforeEach(() => setActivePinia(createPinia()));

  it("keeps one normalized query for every layout and limits groups/summaries", () => {
    const store = useViewQueryStore();
    store.replace("orders", {
      kind: "kanban",
      layout: "kanban",
      filters: [{ groupLogic: "OR", filters: [
        { field: "status", operator: "eq", value: "open" },
        { field: "priority", operator: "eq", value: "urgent" },
      ] }],
      sorts: [{ field: "amount", direction: "desc" }],
      groups: [
        { field: "status" }, { field: "priority" }, { field: "owner" },
      ],
      summaries: [
        { field: "amount", function: "sum" },
        { field: "amount", function: "avg" },
        { field: "amount", function: "min" },
        { field: "amount", function: "max" },
      ],
      search: "north",
      visibleFields: ["status", "missing"],
      collapsedGroupKeys: ['["open"]'],
    }, ["status", "priority", "amount"]);

    expect(store.visibleFields).toEqual(["status"]);
    expect(store.groups).toHaveLength(2);
    expect(store.summaries).toHaveLength(3);
    expect(store.collapsedGroupKeys).toEqual(['["open"]']);
    store.toggleGroup('["open"]');
    expect(store.collapsedGroupKeys).toEqual([]);
    expect(store.toQuery()).toMatchObject({
      keyword: "north",
      offset: 0,
      limit: 500,
      groupLimit: 100,
    });
  });

  it("defaults visibility to all non-row-id fields when a view has no saved list", () => {
    const store = useViewQueryStore();
    store.replace("orders", {
      kind: "table",
      layout: "table",
      filters: [],
      sorts: [],
      search: "",
      visibleFields: [],
    }, ["id", "status"]);

    // 行 ID 默认隐藏，与网格列构建规则（isRowIdColumn）保持同步。
    expect(store.visibleFields).toEqual(["status"]);
    expect(defaultVisibleFields(["id", "status", "amount"])).toEqual(["status", "amount"]);
  });

  it("keeps the row id hidden on reset but honors a saved view that re-shows it", () => {
    const store = useViewQueryStore();
    store.reset("orders", ["id", "status"]);
    expect(store.visibleFields).toEqual(["status"]);

    store.replace("orders", {
      kind: "table",
      layout: "table",
      filters: [],
      sorts: [],
      search: "",
      visibleFields: ["id", "status"],
    }, ["id", "status"]);
    expect(store.visibleFields).toEqual(["id", "status"]);
  });

  it("preserves a nested advanced filter tree when header filters or sorts change", () => {
    const store = useViewQueryStore();
    const nested = [{
      groupLogic: "OR" as const,
      filters: [
        { field: "status", operator: "eq" as const, value: "open" },
        { field: "priority", operator: "eq" as const, value: "urgent" },
      ],
    }];
    store.replace("orders", {
      kind: "table",
      layout: "table",
      filters: nested,
      sorts: [],
      search: "",
      visibleFields: ["status", "priority", "owner"],
    }, ["status", "priority", "owner"]);

    store.updateRuntime({
      headerFilters: [{ field: "owner", operator: "contains", value: "王" }],
      sorts: [{ field: "priority", direction: "desc" }],
      groups: [],
    });

    expect(store.filters).toEqual([
      ...nested,
      { field: "owner", operator: "contains", value: "王" },
    ]);
    expect(store.toQuery().filters).toEqual(store.filters);
  });

  it("replaces and clears a root condition mirrored into a header filter", () => {
    const store = useViewQueryStore();
    store.replace("orders", {
      kind: "table",
      layout: "table",
      filters: [{ field: "status", operator: "eq", value: "open" }],
      sorts: [],
      search: "",
      visibleFields: ["status"],
    }, ["status"]);

    store.updateRuntime({
      headerFilters: [{ field: "status", operator: "eq", value: "closed" }],
      sorts: [],
      groups: [],
    });
    expect(store.filters).toEqual([
      { field: "status", operator: "eq", value: "closed" },
    ]);

    store.updateRuntime({ headerFilters: [], sorts: [], groups: [] });
    expect(store.filters).toEqual([]);
    expect(store.toQuery().filters).toEqual([]);
  });

  it("replaces only the mirrored leaf when an advanced condition uses the same field", () => {
    const store = useViewQueryStore();
    store.replace("orders", {
      kind: "table",
      layout: "table",
      filters: [
        { field: "status", operator: "eq", value: "open" },
        { field: "status", operator: "contains", value: "op" },
      ],
      sorts: [], search: "", visibleFields: ["status"],
    }, ["status"]);

    store.updateRuntime({
      headerFilters: [{ field: "status", operator: "eq", value: "closed" }],
      sorts: [], groups: [],
    });

    expect(store.filters).toEqual([
      { field: "status", operator: "contains", value: "op" },
      { field: "status", operator: "eq", value: "closed" },
    ]);
  });

  it("重载视图后替换同字段 token 组：不残留旧分词条件", () => {
    const store = useViewQueryStore();
    // 模拟保存的列级 OR token 组（上一会话表头输入 "foo bar"）。
    store.replace("orders", {
      kind: "table",
      layout: "table",
      filters: [{
        groupLogic: "OR",
        filters: [
          { field: "note", operator: "contains", value: "foo" },
          { field: "note", operator: "contains", value: "bar" },
        ],
      }],
      sorts: [], search: "", visibleFields: ["note"],
    }, ["note"]);
    expect(store.headerFilters).toEqual([
      { field: "note", operator: "contains", value: "foo" },
      { field: "note", operator: "contains", value: "bar" },
    ]);

    store.updateRuntime({
      headerFilters: [{ field: "note", operator: "contains", value: "baz", logic: "AND" }],
      sorts: [], groups: [],
    });

    expect(store.filters).toEqual([
      { field: "note", operator: "contains", value: "baz", logic: "AND" },
    ]);
    expect(store.toQuery().filters).toEqual(store.filters);
  });

  it("两列各自 token 组叠加：列间 AND，更新一列不重写另一列", () => {
    const store = useViewQueryStore();
    const noteGroup = {
      groupLogic: "OR" as const,
      filters: [
        { field: "note", operator: "contains" as const, value: "foo" },
        { field: "note", operator: "contains" as const, value: "bar" },
      ],
    };
    const titleGroup = {
      groupLogic: "OR" as const,
      filters: [
        { field: "title", operator: "contains" as const, value: "x" },
        { field: "title", operator: "contains" as const, value: "y" },
      ],
    };
    store.replace("orders", {
      kind: "table", layout: "table",
      filters: [noteGroup, titleGroup],
      sorts: [], search: "", visibleFields: ["note", "title"],
    }, ["note", "title"]);

    // 用户改了 note 列的表头输入；title 列的组必须原样保留。
    // （runtime 载荷由 buildQuery 产出：多 token 列是列级 OR 组。）
    store.updateRuntime({
      headerFilters: [
        { field: "note", operator: "contains", value: "baz", logic: "AND" },
        {
          groupLogic: "OR",
          filters: titleGroup.filters.map(filter => ({ ...filter })),
        },
      ],
      sorts: [], groups: [],
    });

    expect(store.filters).toEqual([
      { field: "note", operator: "contains", value: "baz", logic: "AND" },
      titleGroup,
    ]);
  });

  it("applyHeaderValueFilter 接管字段顶层条件：单值 eq、多值 in、空白组、空集清除", () => {
    const store = useViewQueryStore();
    const otherGroup = {
      groupLogic: "AND" as const,
      filters: [
        { field: "other", operator: "eq" as const, value: 1, logic: "AND" as const },
        { field: "other2", operator: "eq" as const, value: 2, logic: "AND" as const },
      ],
    };
    store.replace("orders", {
      kind: "table",
      layout: "table",
      filters: [
        { field: "note", operator: "contains", value: "foo" },
        otherGroup,
      ],
      sorts: [], search: "", visibleFields: ["note", "other", "other2"],
    }, ["note", "other", "other2"]);

    store.applyHeaderValueFilter("note", ["open"]);
    expect(store.filters).toEqual([
      otherGroup,
      { field: "note", operator: "eq", value: "open" },
    ]);

    store.applyHeaderValueFilter("note", ["open", "done"]);
    expect(store.filters).toEqual([
      otherGroup,
      { field: "note", operator: "in", value: ["open", "done"] },
    ]);

    store.applyHeaderValueFilter("note", [null, "open"]);
    expect(store.filters).toEqual([
      otherGroup,
      {
        groupLogic: "OR",
        filters: [
          { field: "note", operator: "is_null" },
          { field: "note", operator: "eq", value: "open" },
        ],
      },
    ]);

    store.applyHeaderValueFilter("note", []);
    expect(store.filters).toEqual([otherGroup]);
  });

  it("勾选面板同步 managed 镜像：外层 runtime.changed 不会把旧表头叶子加回", () => {
    const store = useViewQueryStore();
    // 既有状态：eq status=a 既是高级条件也是表头镜像。
    store.replace("orders", {
      kind: "table", layout: "table",
      filters: [{ field: "status", operator: "eq", value: "a", logic: "AND" }],
      sorts: [], search: "", visibleFields: ["status"],
    }, ["status"]);
    expect(store.headerFilters).toEqual([
      { field: "status", operator: "eq", value: "a", logic: "AND" },
    ]);

    // 面板改选 b：filters 与镜像同步替换。
    store.applyHeaderValueFilter("status", ["b"]);
    expect(store.filters).toEqual([{ field: "status", operator: "eq", value: "b" }]);
    // 模拟真实父链：GridHost emit 的 payload 携带同步后的镜像，
    // presetViewController 调 updateRuntime —— 不得出现 a AND b。
    store.updateRuntime({
      headerFilters: store.headerFilters.map(filter => ({ ...filter })),
      sorts: [], groups: [],
    });
    expect(store.filters).toEqual([{ field: "status", operator: "eq", value: "b" }]);

    // 清除后同样不得把 a/b 加回。
    store.applyHeaderValueFilter("status", []);
    expect(store.filters).toEqual([]);
    store.updateRuntime({
      headerFilters: store.headerFilters.map(filter => ({ ...filter })),
      sorts: [], groups: [],
    });
    expect(store.filters).toEqual([]);
    expect(store.toQuery().filters).toEqual([]);
  });
});
