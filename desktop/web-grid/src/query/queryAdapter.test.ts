import { describe, expect, it } from "vitest";
import {
  buildQuery,
  isNullaryOperator,
  queryToTabulator,
  shouldUseRemoteMode,
} from "./queryAdapter";
import type { TableQuery } from "../contracts";

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

  it("skips empty header filter values", () => {
    const q = buildQuery({
      headerFilters: [
        { field: "status", value: "" },
        { field: "name", value: "abc" },
      ],
    });
    expect(q.filters).toHaveLength(1);
    expect(q.filters![0].field).toBe("name");
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
    expect(q.filters![0].value).toBe(100);
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

  it("drops non-eq filters from header-filter restore", () => {
    const q: TableQuery = {
      filters: [
        { field: "amount", operator: "gt", value: 10 },
        { field: "status", operator: "eq", value: "open" },
      ],
    };
    const { headerFilters } = queryToTabulator(q);
    expect(headerFilters).toHaveLength(1);
    expect(headerFilters[0].field).toBe("status");
  });
});

describe("shouldUseRemoteMode", () => {
  it("returns false at the 25k threshold", () => {
    expect(shouldUseRemoteMode(25_000)).toBe(false);
  });

  it("returns true above 25k", () => {
    expect(shouldUseRemoteMode(25_001)).toBe(true);
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
