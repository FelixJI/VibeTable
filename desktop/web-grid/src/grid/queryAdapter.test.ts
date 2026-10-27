import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { isFilterCondition } from "./viewQuery";
import {
  buildQuery,
  isNullaryOperator,
  queryToTabulator,
} from "./queryAdapter";
import type { TableQuery } from "@/contracts";

describe("buildQuery", () => {
  it("uses only allowed membership operators for multi-select header filters", () => {
    const query=buildQuery({columns:[{name:"tags",title:"标签",dataType:"json",editable:true,nullable:true,filterInput:"multiSelect",filterOperators:["contains","is_null","is_not_null"]}],headerFilters:[{field:"tags",value:["a","b"]}]});
    expect(query.filters).toEqual([{groupLogic:"OR",filters:[{field:"tags",operator:"contains",value:"a"},{field:"tags",operator:"contains",value:"b"}]}]);
  });
  it("matches the shared Go compiler fixture for column-local OR terms", () => {
    const cases = JSON.parse(readFileSync("../../tests/contract/fixtures/header-value-filter-cases.json", "utf8")) as {name:string;query:TableQuery}[];
    expect(buildQuery({
      columns: ["name", "status"].map(name => ({name,title:name,dataType:"text" as const,editable:true,nullable:true})),
      headerFilters: [{field:"name",value:"foo bar"},{field:"status",value:"open paused"}],
    })).toEqual(cases.find(item => item.name === "multiple-columns-with-or-terms")!.query);
  });
  it("maps Tabulator sorters to the AST with nullsLast", () => {
    const q = buildQuery({
      sorters: [{ field: "amount", dir: "desc" }],
    });
    expect(q.sorts).toEqual([
      { field: "amount", direction: "desc", nullsLast: true },
    ]);
  });

  it("maps header filters to eq operators", () => {
    const q = buildQuery({
      headerFilters: [{ field: "status", value: "open" }],
    });
    expect(q.filters).toEqual([
      { field: "status", operator: "eq", value: "open", logic: "AND" },
    ]);
  });

  it("maps whole-JSON header filters to authoritative contains operators", () => {
    const q = buildQuery({
      columns: [
        { name: "payload", title: "Payload", dataType: "json", editable: true, nullable: true },
      ],
      headerFilters: [{ field: "payload", value: "8" }],
    });
    expect(q.filters).toEqual([
      { field: "payload", operator: "contains", value: "8", logic: "AND" },
    ]);
  });

  it("纯文本字段空白分词为列级 OR 条件组，单 token 为普通叶子", () => {
    const q = buildQuery({
      columns: [
        { name: "note", title: "备注", dataType: "text", editable: true, nullable: true },
      ],
      headerFilters: [{ field: "note", value: "memo" }],
    });
    expect(q.filters).toEqual([
      { field: "note", operator: "contains", value: "memo", logic: "AND" },
    ]);

    const multi = buildQuery({
      columns: [
        { name: "note", title: "备注", dataType: "text", editable: true, nullable: true },
      ],
      headerFilters: [{ field: "note", value: "  foo   bar " }],
    });
    expect(multi.filters).toEqual([{
      groupLogic: "OR",
      filters: [
        { field: "note", operator: "contains", value: "foo" },
        { field: "note", operator: "contains", value: "bar" },
      ],
    }]);
    // 权威编译器契约（query/compiler.go）：index>=1 的子叶 logic 必须与
    // groupLogic 一致或省略；省略由权威归一化，绝不发送 logic:"AND"。
    const group = multi.filters![0]!;
    expect(isFilterCondition(group)).toBe(false);
    if (!isFilterCondition(group)) expect(group.filters.every(leaf => leaf.logic === undefined)).toBe(true);
  });

  it("两列各自分词的 OR 组之间保持 AND：任一列的 OR 不得泄漏到另一列", () => {
    const q = buildQuery({
      columns: [
        { name: "note", title: "备注", dataType: "text", editable: true, nullable: true },
        { name: "title", title: "标题", dataType: "text", editable: true, nullable: true },
        { name: "amount", title: "金额", dataType: "decimal", editable: true, nullable: true },
      ],
      headerFilters: [
        { field: "note", value: "foo bar" },
        { field: "title", value: "x" },
        { field: "amount", value: 5 },
      ],
    });
    expect(q.filters).toEqual([
      {
        groupLogic: "OR",
        filters: [
          { field: "note", operator: "contains", value: "foo" },
          { field: "note", operator: "contains", value: "bar" },
        ],
      },
      { field: "title", operator: "contains", value: "x", logic: "AND" },
      { field: "amount", operator: "eq", value: 5, logic: "AND" },
    ]);
    // 列间 AND：note 的组内 OR 不会绕过 title/amount 的约束。
    const [noteGroup] = q.filters as [{ filters: unknown[] }];
    expect(noteGroup.filters.every(leaf => (leaf as { field: string }).field === "note"))
      .toBe(true);
  });

  it("枚举 select 字段保持 eq：自由文本不回退包含语义", () => {
    const enumColumn = {
      name: "status", title: "状态", dataType: "text" as const, editable: true, nullable: true,
      display: {
        kind: "select" as const, preset: "standard", displayScale: 0, scaleMode: "fixed" as const,
        trimTrailingZeros: false, useGrouping: false, currency: "", percentStorage: "ratio" as const,
        unit: null, precision: "exact" as const, timezone: "system", mode: "standard",
        trueLabel: "", falseLabel: "",
      },
      enumOptions: [{ optionId: "open", label: "进行中", color: "", order: 0, state: "active" as const }],
    };
    const q = buildQuery({
      columns: [enumColumn],
      headerFilters: [{ field: "status", value: "open" }],
    });
    expect(q.filters).toEqual([
      { field: "status", operator: "eq", value: "open", logic: "AND" },
    ]);
  });

  it("非文本字段（数字/布尔）仍为 eq 且不分词", () => {
    const q = buildQuery({
      columns: [
        { name: "amount", title: "金额", dataType: "decimal", editable: true, nullable: true },
      ],
      headerFilters: [{ field: "amount", value: "10 20" }],
    });
    expect(q.filters).toEqual([
      { field: "amount", operator: "eq", value: "10 20", logic: "AND" },
    ]);
  });

  it("skips empty header filter values", () => {
    const q = buildQuery({
      headerFilters: [
        { field: "status", value: "" },
        { field: "name", value: "abc" },
      ],
    });
    expect(q.filters).toHaveLength(1);
    expect(q.filters).toEqual([
      { field: "name", operator: "eq", value: "abc", logic: "AND" },
    ]);
  });

  it("normalizes a keyword (trims, keeps non-empty)", () => {
    const q = buildQuery({ keyword: "  abc  " });
    expect(q.keyword).toBe("abc");
  });

  it("treats whitespace-only keyword as absent", () => {
    const q = buildQuery({ keyword: "   " });
    expect(q.keyword).toBeUndefined();
  });

  it("defaults offset and limit", () => {
    const q = buildQuery({});
    expect(q.offset).toBe(0);
    expect(q.limit).toBe(100);
  });

  it("never sends formatter text as stored values", () => {
    // The adapter only forwards raw filter values, never formatter output.
    const q = buildQuery({
      headerFilters: [{ field: "amount", value: 100 }],
    });
    expect(q.filters).toEqual([
      { field: "amount", operator: "eq", value: 100, logic: "AND" },
    ]);
  });
});

describe("queryToTabulator", () => {
  it("round-trips sorts and eq filters", () => {
    const q: TableQuery = {
      filters: [{ field: "status", operator: "eq", value: "open" }],
      sorts: [{ field: "amount", direction: "desc" }],
    };
    const { sorters, headerFilters } = queryToTabulator(q);
    expect(sorters).toEqual([{ field: "amount", dir: "desc" }]);
    expect(headerFilters).toEqual([{ field: "status", value: "open" }]);
  });

  it("restores eq/contains filters and drops non-header operators", () => {
    const q: TableQuery = {
      filters: [
        { field: "amount", operator: "gt", value: 10 },
        { field: "status", operator: "eq", value: "open" },
        { field: "payload", operator: "contains", value: "8" },
      ],
    };
    const { headerFilters } = queryToTabulator(q);
    expect(headerFilters).toEqual([
      { field: "status", value: "open" },
      { field: "payload", value: "8" },
    ]);
  });

  it("does not flatten a grouped filter tree into misleading header filters", () => {
    const q: TableQuery = {
      filters: [
        {
          groupLogic: "OR",
          filters: [
            { field: "status", operator: "eq", value: "open" },
            { field: "priority", operator: "eq", value: "urgent" },
          ],
        },
      ],
    };

    expect(queryToTabulator(q).headerFilters).toEqual([]);
  });
});

describe("isNullaryOperator", () => {
  it("returns true for is_null and is_not_null", () => {
    expect(isNullaryOperator("is_null")).toBe(true);
    expect(isNullaryOperator("is_not_null")).toBe(true);
  });

  it("returns false for value-bearing operators", () => {
    expect(isNullaryOperator("eq")).toBe(false);
    expect(isNullaryOperator("contains")).toBe(false);
  });
});
