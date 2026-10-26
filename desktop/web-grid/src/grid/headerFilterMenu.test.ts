import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import type { ColumnSchema } from "@/contracts";
import type { DisplaySpec } from "@/contracts/generated/schemaV2";
import {
  headerMenuOptionsFromPage,
  canEditHeaderValueFilter,
  headerMenuSelection,
  headerMenuSource,
  headerValueFilterLeaves,
  headerValueFilterError,
  isFieldValueGroup,
  resolveMenuSelectionValues,
  searchMenuOptions,
} from "./headerFilterMenu";
import { filterEditorInput, filterEditorOptions, isPlainTextColumn } from "./filterFieldEditor";
import { headerValueKey } from "@/services/headerFilterValues";
import { buildQuery } from "./queryAdapter";

function selectDisplay(): DisplaySpec {
  return {
    kind: "select",
    preset: "standard",
    displayScale: 0,
    scaleMode: "fixed",
    trimTrailingZeros: false,
    useGrouping: false,
    currency: "",
    percentStorage: "ratio",
    unit: null,
    precision: "exact",
    timezone: "system",
    mode: "standard",
    trueLabel: "",
    falseLabel: "",
  };
}

const optionColumn: ColumnSchema = {
  name: "status",
  title: "状态",
  dataType: "text",
  editable: true,
  nullable: true,
  display: selectDisplay(),
  enumOptions: [
    { optionId: "open", label: "进行中", color: "", order: 0, state: "active" },
    { optionId: "done", label: "完成", color: "", order: 1, state: "active" },
    { optionId: "old", label: "旧值", color: "", order: 2, state: "retired" },
  ],
};

const textColumn: ColumnSchema = {
  name: "note",
  title: "备注",
  dataType: "text",
  editable: true,
  nullable: true,
};

describe("filterFieldEditor", () => {
  it("未发布 filterInput 时从 select 显示契约派生封闭枚举编辑器", () => {
    expect(filterEditorInput(optionColumn)).toBe("select");
    expect(filterEditorOptions(optionColumn)).toEqual([
      { value: "open", label: "进行中" },
      { value: "done", label: "完成" },
      { value: "old", label: "旧值（已停用）" },
    ]);
  });

  it("显式 filterOptions/filterInput 优先于派生", () => {
    const explicit: ColumnSchema = {
      ...optionColumn,
      filterInput: "select",
      filterOptions: [{ value: "x", label: "显式" }],
    };
    expect(filterEditorInput(explicit)).toBe("select");
    expect(filterEditorOptions(explicit)).toEqual([{ value: "x", label: "显式" }]);
  });

  it("普通文本列与无选项列保持文本编辑器", () => {
    expect(filterEditorInput(textColumn)).toBe("text");
    expect(filterEditorOptions(textColumn)).toEqual([]);
    expect(filterEditorInput(undefined)).toBe("text");
  });

  it("isPlainTextColumn 只认非枚举文本列", () => {
    expect(isPlainTextColumn(textColumn)).toBe(true);
    expect(isPlainTextColumn(optionColumn)).toBe(false);
    expect(isPlainTextColumn({ ...textColumn, filterInput: "select" })).toBe(false);
    expect(isPlainTextColumn({ ...textColumn, dataType: "decimal" })).toBe(false);
    expect(isPlainTextColumn(undefined)).toBe(false);
  });
});

describe("headerFilterMenu", () => {
  it("enforces QueryPort IN and total-leaf budgets without treating partial pages as all values", () => {
    const values=Array.from({length:300},(_,i)=>`v${i}`);
    expect(headerValueFilterError([],"status",values.slice(0,200))).toBeNull();
    expect(headerValueFilterError([],"status",values.slice(0,299))).toContain("200");
    const options=values.map(value=>({key:headerValueKey(value),label:value,rawValue:value}));
    const all=resolveMenuSelectionValues(options,options.map(option=>option.key),true);
    expect(headerValueFilterError([],"status",all)).toBeNull();
    const other=Array.from({length:49},(_,i)=>({field:`other${i}`,operator:"eq" as const,value:i}));
    const multi:ColumnSchema={...optionColumn,filterInput:"multiSelect",filterOperators:["contains","is_null","is_not_null"]};
    expect(headerValueFilterError(other,"tags",["a"],multi)).toBeNull();
    expect(headerValueFilterError(other,"tags",["a","b"],multi)).toContain("50");
    expect(headerValueFilterError([...other,{field:"tags",operator:"contains",value:"old"}],"tags",["a"],multi)).toBeNull();
  });
  it("matches the shared compiler fixture for blank-or-value constrained by another column", () => {
    const cases = JSON.parse(readFileSync("../../tests/contract/fixtures/header-value-filter-cases.json", "utf8")) as {name:string;query:unknown}[];
    const query = buildQuery({headerFilters:[{field:"status",value:"open"}]});
    expect({...query,filters:[...query.filters!,...headerValueFilterLeaves("notes",[null,"ok"])]})
      .toEqual(cases.find(item => item.name === "blank-or-value-with-another-column")!.query);
  });

  it("multi-select emits membership contains OR leaves and restores their checked values", () => {
    const column: ColumnSchema = {...optionColumn,filterInput:"multiSelect",filterOperators:["contains","is_null","is_not_null"]};
    const filters=headerValueFilterLeaves("tags",[null,"open","done"],column);
    expect(filters).toEqual([{groupLogic:"OR",filters:[
      {field:"tags",operator:"is_null"},{field:"tags",operator:"contains",value:"open"},{field:"tags",operator:"contains",value:"done"},
    ]}]);
    expect(headerMenuSelection(filters,"tags",column)).toEqual(["null","s:open","s:done"]);
    expect(headerValueFilterLeaves("tags",["open"],column)).toEqual([{field:"tags",operator:"contains",value:"open"}]);
  });

  it("advanced AND groups and cross-column OR chains cannot masquerade as independent checked values", () => {
    const andGroup = [{groupLogic:"AND" as const,filters:[{field:"status",operator:"eq" as const,value:"a"},{field:"status",operator:"eq" as const,value:"b"}]}];
    expect(headerMenuSelection(andGroup,"status")).toBeNull();
    expect(canEditHeaderValueFilter(andGroup,"status")).toBe(false);
    const coupled = [{field:"status",operator:"eq" as const,value:"a"},{field:"notes",operator:"eq" as const,value:"b",logic:"OR" as const}];
    expect(headerMenuSelection(coupled,"status")).toBeNull();
    expect(canEditHeaderValueFilter(coupled,"notes")).toBe(false);
  });

  it("selecting every nonblank enum value still excludes blank rows", () => {
    const source=headerMenuSource(optionColumn);
    if(source?.kind!=="enum") throw new Error("expected enum");
    expect(resolveMenuSelectionValues(source.options,["s:open","s:done","s:old"],true)).toEqual(["open","done","old"]);
  });
  it("枚举字段映射为静态面板选项（含空白）；其余字段声明动态取值来源", () => {
    const source = headerMenuSource(optionColumn);
    expect(source).toEqual({
      kind: "enum",
      options: [
        { key: "s:open", label: "进行中", rawValue: "open" },
        { key: "s:done", label: "完成", rawValue: "done" },
        { key: "s:old", label: "旧值（已停用）", rawValue: "old" },
        { key: "null", label: "（空白）", rawValue: null },
      ],
    });
    expect(headerMenuSource(textColumn)).toEqual({ kind: "dynamic" });
    expect(headerMenuSource(undefined)).toBeNull();
  });

  it("动态取值页保留原始类型：null/0/false 不混为字符串", () => {
    const options = headerMenuOptionsFromPage([
      { key: headerValueKey(null), label: "（空白）", rawValue: null, count: 2 },
      { key: headerValueKey(0), label: "0", rawValue: 0, count: 3 },
      { key: headerValueKey(false), label: "否", rawValue: false, count: 1 },
    ]);
    expect(options.map(option => option.key)).toEqual(["null", "n:0", "b:false"]);
    expect(options.map(option => option.rawValue)).toEqual([null, 0, false]);
    expect(options.map(option => option.count)).toEqual([2, 3, 1]);
  });

  it("搜索按空白分词 OR 匹配标签，不创建条件", () => {
    const options = headerMenuSource(optionColumn)!.kind === "enum"
      ? (headerMenuSource(optionColumn) as { options: ReturnType<typeof headerMenuOptionsFromPage> }).options
      : [];
    expect(searchMenuOptions(options, "进行 旧")).toEqual([
      { key: "s:open", label: "进行中", rawValue: "open" },
      { key: "s:old", label: "旧值（已停用）", rawValue: "old" },
    ]);
    expect(searchMenuOptions(options, "OPEN")).toEqual([
      { key: "s:open", label: "进行中", rawValue: "open" },
    ]);
    expect(searchMenuOptions(options, "  ")).toHaveLength(4);
    expect(searchMenuOptions(options, "不存在的词")).toHaveLength(0);
  });

  it("字段级条件：标量 eq/in，空白独立 is_null，并存封装为列级 OR 组（子叶省略 logic）", () => {
    expect(headerValueFilterLeaves("amount", [])).toEqual([]);
    expect(headerValueFilterLeaves("amount", [0])).toEqual([
      { field: "amount", operator: "eq", value: 0 },
    ]);
    expect(headerValueFilterLeaves("amount", [0, 5])).toEqual([
      { field: "amount", operator: "in", value: [0, 5] },
    ]);
    expect(headerValueFilterLeaves("amount", [null])).toEqual([
      { field: "amount", operator: "is_null" },
    ]);
    // 空白与标量并存：按列封装 OR 组；子叶省略 logic（权威编译器要求
    // index>=1 子叶 logic 与 groupLogic 一致，省略由权威归一化）。
    expect(headerValueFilterLeaves("amount", [null, 0])).toEqual([{
      groupLogic: "OR",
      filters: [
        { field: "amount", operator: "is_null" },
        { field: "amount", operator: "eq", value: 0 },
      ],
    }]);
    // 数组/对象值（如 multiple select 成员）绝不能当标量进 eq/in。
    expect(headerValueFilterLeaves("tags", [["a", "b"] as unknown])).toEqual([]);
    expect(headerValueFilterLeaves("tags", [["a"], null])).toEqual([
      { field: "tags", operator: "is_null" },
    ]);
  });

  it("单列条件组判定：仅同字段叶子组；跨字段/嵌套不算", () => {
    expect(isFieldValueGroup({
      groupLogic: "OR",
      filters: [{ field: "a", operator: "eq", value: 1, logic: "AND" }],
    })).toBe(true);
    expect(isFieldValueGroup({
      groupLogic: "OR",
      filters: [
        { field: "a", operator: "eq", value: 1, logic: "AND" },
        { field: "b", operator: "eq", value: 2, logic: "AND" },
      ],
    })).toBe(false);
    expect(isFieldValueGroup({ field: "a", operator: "eq", value: 1 })).toBe(false);
  });

  it("枚举源按列可空性补“（空白）”选项；非空列不补", () => {
    const source = headerMenuSource(optionColumn)!;
    expect(source.kind).toBe("enum");
    if (source.kind === "enum") {
      expect(source.options.map(option => option.rawValue)).toEqual(["open", "done", "old", null]);
      expect(source.options.at(-1)?.label).toBe("（空白）");
    }
    const required = headerMenuSource({ ...optionColumn, nullable: false });
    if (required?.kind === "enum") {
      expect(required.options.map(option => option.rawValue)).toEqual(["open", "done", "old"]);
    }
  });

  it("初始勾选从现有条件恢复：eq/in/空白/列级 OR 组；高级 AND 组拒绝", () => {
    expect(headerMenuSelection(
      [{ field: "amount", operator: "eq", value: 0 }],
      "amount",
    )).toEqual(["n:0"]);
    expect(headerMenuSelection(
      [{ field: "amount", operator: "in", value: [0, 5] }],
      "amount",
    )).toEqual(["n:0", "n:5"]);
    expect(headerMenuSelection(
      [{ field: "amount", operator: "is_null" }],
      "amount",
    )).toEqual(["null"]);
    expect(headerMenuSelection([
      {
        groupLogic: "OR",
        filters: [
          { field: "amount", operator: "is_null" },
          { field: "amount", operator: "in", value: [0, 5] },
        ],
      },
    ], "amount")).toEqual(["null", "n:0", "n:5"]);
    // 高级单列 AND 组不能伪装等价勾选：未筛选态而非错误勾选。
    expect(headerMenuSelection([
      {
        groupLogic: "AND",
        filters: [
          { field: "amount", operator: "eq", value: 0 },
          { field: "amount", operator: "eq", value: 5 },
        ],
      },
    ], "amount")).toBeNull();
    // 顶层 OR 耦合的组同样不反推。
    expect(headerMenuSelection([
      {
        groupLogic: "OR",
        logic: "OR",
        filters: [{ field: "amount", operator: "is_null" }],
      },
    ], "amount")).toBeNull();
    expect(headerMenuSelection(
      [{ field: "amount", operator: "contains", value: "op" }],
      "amount",
    )).toBeNull();
  });

  it("完整枚举勾满才清除；分页未取完时勾满是明确的部分选择", () => {
    const options = headerMenuOptionsFromPage([
      { key: headerValueKey(null), label: "（空白）", rawValue: null, count: 1 },
      { key: "s:a", label: "A", rawValue: "a", count: 1 },
      { key: "s:b", label: "B", rawValue: "b", count: 1 },
    ]);
    expect(resolveMenuSelectionValues(options, [], true)).toEqual([]);
    // 完整枚举：勾满等同未筛选，返回空集清除。
    expect(resolveMenuSelectionValues(options, ["null", "s:a", "s:b"], true)).toEqual([]);
    expect(resolveMenuSelectionValues(options, ["null", "s:a"], true)).toEqual([null, "a"]);
    // 分页未取完（hasMore）：勾满已加载项不得当作全表全选而误清除。
    expect(resolveMenuSelectionValues(options, ["null", "s:a", "s:b"], false))
      .toEqual([null, "a", "b"]);
    expect(resolveMenuSelectionValues(options, ["s:a", "s:unknown"], true)).toEqual(["a"]);
  });
});
