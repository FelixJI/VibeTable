import { describe, expect, it } from "vitest";
import type { FilterExpression } from "@/contracts";
import { headerFilterConditions, ungroupedFilterConditions } from "./viewQuery";

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
