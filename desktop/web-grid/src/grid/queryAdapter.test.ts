import { describe, expect, it } from "vitest";
import {
  buildQuery,
  isNullaryOperator,
  queryToTabulator,
} from "./queryAdapter";
import type { TableQuery } from "@/contracts";

describe("buildQuery", () => {
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
