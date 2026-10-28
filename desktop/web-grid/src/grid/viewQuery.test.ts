import { describe, expect, it } from "vitest";
import type { ColumnSchema, FilterCondition, FilterExpression } from "@/contracts";
import { headerFilterConditions, ungroupedFilterConditions } from "./viewQuery";
import { buildQuery } from "./queryAdapter";

/** 与 dataSourceViewState.applyDataSourceView 相同的镜像回填算法。 */
function headerInputsFromMirror(
  mirror: readonly FilterCondition[],
): { field: string; value: unknown }[] {
  const values = new Map<string, unknown>();
  for (const leaf of mirror) {
    if (leaf.operator === "contains" && typeof leaf.value === "string") {
      const previous = values.get(leaf.field);
      values.set(leaf.field, typeof previous === "string" ? `${previous} ${leaf.value}` : leaf.value);
    } else {
      values.set(leaf.field, leaf.value);
    }
  }
  return [...values].map(([field, value]) => ({ field, value }));
}

describe("headerFilterConditions", () => {
  it("镜像单字段唯一 eq 叶子（既有语义）", () => {
    const filters: FilterExpression[] = [
      { field: "status", operator: "eq", value: "open" },
      { field: "amount", operator: "gt", value: 5 },
    ];
    expect(headerFilterConditions(filters)).toEqual([
      { field: "status", operator: "eq", value: "open" },
    ]);
  });

  it("镜像列级 OR token 组（query adapter 产出的分组形态）", () => {
    const tokenGroup: FilterExpression = {
      groupLogic: "OR",
      filters: [
        { field: "note", operator: "contains", value: "foo" },
        { field: "note", operator: "contains", value: "bar" },
      ],
    };
    const filters: FilterExpression[] = [
      tokenGroup,
      { field: "status", operator: "eq", value: "open" },
    ];
    expect(headerFilterConditions(filters)).toEqual([
      { field: "note", operator: "contains", value: "foo" },
      { field: "note", operator: "contains", value: "bar" },
      { field: "status", operator: "eq", value: "open" },
    ]);
  });

  it("平铺单 contains 叶子不镜像（高级编辑器条件）；字段在平铺层与组同时出现时也不镜像", () => {
    // 高级编辑器创建的同字段 contains 条件不属于表头输入：镜像它会让
    // 后续表头输入变化误清除该高级条件。
    expect(headerFilterConditions([
      { field: "note", operator: "contains", value: "foo" },
    ])).toEqual([]);

    expect(headerFilterConditions([
      { field: "note", operator: "contains", value: "stray", logic: "AND" },
      {
        groupLogic: "OR",
        filters: [{ field: "note", operator: "contains", value: "foo" }],
      },
    ])).toEqual([]);
  });

  it("跨字段 OR 链不镜像：列的 OR 不得绕过相邻列约束", () => {
    expect(headerFilterConditions([
      { field: "status", operator: "eq", value: "open" },
      { field: "priority", operator: "eq", value: "urgent", logic: "OR" },
    ])).toEqual([]);
    expect(headerFilterConditions([
      { field: "status", operator: "eq", value: "open" },
      {
        groupLogic: "OR",
        logic: "OR",
        filters: [{ field: "note", operator: "contains", value: "foo" }],
      },
    ])).toEqual([]);
  });

  it("in 勾选叶子与空白/类型化列组不镜像：输入无法表达", () => {
    expect(headerFilterConditions([
      { field: "status", operator: "in", value: ["open", "done"] },
    ])).toEqual([]);
    expect(headerFilterConditions([
      {
        groupLogic: "OR",
        filters: [
          { field: "status", operator: "is_null", logic: "AND" },
          { field: "status", operator: "eq", value: "open", logic: "AND" },
        ],
      },
    ])).toEqual([]);
  });

  it("同一字段拆成不连续叶子时不镜像：单一输入无法表达两段条件", () => {
    expect(headerFilterConditions([
      { field: "status", operator: "eq", value: "open" },
      { field: "amount", operator: "eq", value: 1 },
      { field: "status", operator: "eq", value: "done" },
    ])).toEqual([
      { field: "amount", operator: "eq", value: 1 },
    ]);
  });

  it("嵌套/跨字段组保持不镜像，但不阻塞其他字段的镜像", () => {
    expect(headerFilterConditions([
      {
        groupLogic: "OR",
        filters: [
          { field: "status", operator: "eq", value: "open" },
          { field: "priority", operator: "eq", value: "urgent" },
        ],
      },
      { field: "amount", operator: "eq", value: 1 },
    ])).toEqual([
      { field: "amount", operator: "eq", value: 1 },
    ]);
  });

  it("分组树保持不展平（既有语义）", () => {
    const filters: FilterExpression[] = [{
      groupLogic: "OR",
      filters: [{ field: "status", operator: "eq", value: "open" }],
    }];
    expect(ungroupedFilterConditions(filters)).toEqual([]);
  });
});

describe("headerFilterConditions（列感知往返）", () => {
  const noteColumn: ColumnSchema = {
    name: "note", title: "备注", dataType: "text", editable: true, nullable: true,
  };
  const statusColumn: ColumnSchema = {
    name: "status", title: "状态", dataType: "text", editable: true, nullable: true,
    filterInput: "select", filterOptions: [{ value: "open", label: "进行中" }],
  };
  const amountColumn: ColumnSchema = {
    name: "amount", title: "金额", dataType: "decimal", editable: true, nullable: true,
  };
  const payloadColumn: ColumnSchema = {
    name: "payload", title: "载荷", dataType: "json", editable: true, nullable: true,
  };
  const tagsColumn: ColumnSchema = {
    name: "tags", title: "标签", dataType: "json", editable: true, nullable: true,
    filterInput: "multiSelect", filterOperators: ["contains", "is_null", "is_not_null"],
  };
  const columns = [noteColumn, statusColumn, amountColumn, payloadColumn, tagsColumn];

  it("普通文本/JSON/多选列与未知字段的 eq 不镜像：表头输入是 contains 语义，往返必然改写", () => {
    expect(headerFilterConditions([
      { field: "note", operator: "eq", value: "foo bar" },
      { field: "payload", operator: "eq", value: "x" },
      { field: "tags", operator: "eq", value: "x" },
      { field: "missing", operator: "eq", value: "x" },
    ], columns)).toEqual([]);
  });

  it("select 枚举与数字列的 eq 仍镜像：输入往返保持 eq", () => {
    expect(headerFilterConditions([
      { field: "status", operator: "eq", value: "open" },
      { field: "amount", operator: "eq", value: 5 },
    ], columns)).toEqual([
      { field: "status", operator: "eq", value: "open" },
      { field: "amount", operator: "eq", value: 5 },
    ]);
  });

  it("JSON contains only mirrors a unique textual document condition", () => {
    const condition: FilterCondition = { field: "payload", operator: "contains", value: "two words" };
    expect(headerFilterConditions([condition], columns)).toEqual([condition]);
    expect(headerFilterConditions([condition, { ...condition, value: "other" }], columns)).toEqual([]);
    expect(headerFilterConditions([{ ...condition, value: "" }], columns)).toEqual([]);
    expect(headerFilterConditions([{ ...condition, value: ["value"] }], columns)).toEqual([]);
    expect(headerFilterConditions([condition], [{ ...payloadColumn, filterInput: "multiSelect" }])).toEqual([]);
    expect(headerFilterConditions([condition], [{ ...payloadColumn, filterOperators: ["contains"] }])).toEqual([]);
  });

  it("contains token 组只在普通文本列镜像；其他列与输入往返不一致不镜像", () => {
    const noteGroup: FilterExpression = {
      groupLogic: "OR",
      filters: [
        { field: "note", operator: "contains", value: "foo" },
        { field: "note", operator: "contains", value: "bar" },
      ],
    };
    const payloadGroup: FilterExpression = {
      groupLogic: "OR",
      filters: [
        { field: "payload", operator: "contains", value: "foo" },
        { field: "payload", operator: "contains", value: "bar" },
      ],
    };
    expect(headerFilterConditions([noteGroup], columns)).toEqual([
      { field: "note", operator: "contains", value: "foo" },
      { field: "note", operator: "contains", value: "bar" },
    ]);
    expect(headerFilterConditions([payloadGroup], columns)).toEqual([]);
  });

  it("含内部/首尾空白的 contains 组不镜像：仅排序不会扩大匹配", () => {
    // 高级筛选可创建 OR(contains "foo bar", contains "baz")；旧实现镜像后
    // 回填输入 "foo bar baz"，仅排序的 buildQuery 就会分词成 3 个 OR 叶子。
    const advanced: FilterExpression[] = [{
      groupLogic: "OR",
      filters: [
        { field: "note", operator: "contains", value: "foo bar" },
        { field: "note", operator: "contains", value: "baz" },
      ],
    }];
    expect(headerFilterConditions(advanced)).toEqual([]);
    expect(headerFilterConditions(advanced, [noteColumn])).toEqual([]);
    // 单叶含空白组与混合空白 token 组同样不可往返。
    expect(headerFilterConditions([{
      groupLogic: "OR",
      filters: [{ field: "note", operator: "contains", value: "foo bar" }],
    }], [noteColumn])).toEqual([]);
    expect(headerFilterConditions([{
      groupLogic: "OR",
      filters: [
        { field: "note", operator: "contains", value: "foo" },
        { field: "note", operator: "contains", value: " bar" },
      ],
    }], [noteColumn])).toEqual([]);

    // 不可镜像 → 输入为空 → 仅排序的 runtime 捕获不产生该字段条件，
    // 保存的权威 filters 原样保留，不会被改写成扩大匹配的 token 组。
    const mirror = headerFilterConditions(advanced, [noteColumn]);
    expect(buildQuery({
      columns: [noteColumn],
      headerFilters: headerInputsFromMirror(mirror),
    }).filters).toEqual([]);

    // 对照：无空白 token 组仍镜像且经输入往返还原为等价 OR 组。
    const clean: FilterExpression[] = [{
      groupLogic: "OR",
      filters: [
        { field: "note", operator: "contains", value: "foo" },
        { field: "note", operator: "contains", value: "bar" },
      ],
    }];
    const cleanMirror = headerFilterConditions(clean, [noteColumn]);
    expect(buildQuery({
      columns: [noteColumn],
      headerFilters: headerInputsFromMirror(cleanMirror),
    }).filters).toEqual(clean);
  });

  it("镜像条件经表头输入往返必须还原：普通文本 eq 不得被改写成 contains/OR", () => {
    const filters: FilterExpression[] = [
      { field: "note", operator: "eq", value: "foo bar" },
      { field: "status", operator: "eq", value: "open" },
      { field: "amount", operator: "eq", value: 5 },
    ];
    const mirror = headerFilterConditions(filters, columns);
    const captured = buildQuery({
      columns,
      headerFilters: headerInputsFromMirror(mirror),
    }).filters ?? [];
    // 仅排序等 runtime 事件也会触发同一次捕获：语义必须与镜像完全一致。
    expect(captured).toEqual([
      { field: "status", operator: "eq", value: "open", logic: "AND" },
      { field: "amount", operator: "eq", value: 5, logic: "AND" },
    ]);
    // 镜像 → 输入 → 捕获 → 再镜像 幂等（仅比较语义字段：捕获叶子会带上
    // 权威归一化的 logic）：不能往返的条件一开始就不镜像。
    const semantic = (leaves: readonly FilterCondition[]) =>
      leaves.map(leaf => ({ field: leaf.field, operator: leaf.operator, value: leaf.value }));
    expect(semantic(headerFilterConditions(captured, columns))).toEqual(semantic(mirror));
  });

  it("普通文本 token 组镜像经输入往返还原为等价 OR 组", () => {
    const group: FilterExpression = {
      groupLogic: "OR",
      filters: [
        { field: "note", operator: "contains", value: "foo" },
        { field: "note", operator: "contains", value: "bar" },
      ],
    };
    const mirror = headerFilterConditions([group], columns);
    expect(buildQuery({
      columns,
      headerFilters: headerInputsFromMirror(mirror),
    }).filters).toEqual([group]);
  });
});
