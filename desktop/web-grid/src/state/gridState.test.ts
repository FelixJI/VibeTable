import { describe, expect, it } from "vitest";
import {
  buildRestorePlan,
  defaultGridState,
  isStateConflict,
  reconcileState,
} from "./gridState";
import type { ColumnSchema, GridState } from "../contracts";

function col(name: string): ColumnSchema {
  return { name, title: name, dataType: "text", editable: false, nullable: true };
}

describe("reconcileState", () => {
  it("prunes saved columns no longer in the schema", () => {
    const saved: GridState = {
      columns: [
        { name: "amount", width: 120 },
        { name: "dropped", width: 50 },
      ],
    };
    const reconciled = reconcileState(saved, [col("amount"), col("name")]);
    expect(reconciled.columns.map((c) => c.name)).toEqual(["amount"]);
  });

  it("reports newly-added columns absent from saved state", () => {
    const saved: GridState = {
      columns: [{ name: "amount", width: 120 }],
    };
    const reconciled = reconcileState(saved, [col("amount"), col("name")]);
    expect(reconciled.newlyAdded).toEqual(["name"]);
  });

  it("drops sorts referencing pruned columns", () => {
    const saved: GridState = {
      columns: [],
      sorts: [
        { field: "amount", direction: "desc" },
        { field: "dropped", direction: "asc" },
      ],
    };
    const reconciled = reconcileState(saved, [col("amount")]);
    expect(reconciled.sorts).toEqual([{ field: "amount", direction: "desc" }]);
  });

  it("drops filters referencing pruned columns", () => {
    const saved: GridState = {
      columns: [],
      filters: [
        { field: "amount", operator: "gt", value: 10 },
        { field: "dropped", operator: "eq", value: "x" },
      ],
    };
    const reconciled = reconcileState(saved, [col("amount")]);
    expect(reconciled.filters).toHaveLength(1);
    expect(reconciled.filters[0].field).toBe("amount");
  });

  it("normalizes a whitespace keyword to null", () => {
    const saved: GridState = { columns: [], keyword: "   " };
    const reconciled = reconcileState(saved, []);
    expect(reconciled.keyword).toBeNull();
  });

  it("keeps a non-empty keyword", () => {
    const saved: GridState = { columns: [], keyword: "abc" };
    const reconciled = reconcileState(saved, []);
    expect(reconciled.keyword).toBe("abc");
  });

  it("defaults density and forcedRemote", () => {
    const saved: GridState = { columns: [] };
    const reconciled = reconcileState(saved, []);
    expect(reconciled.density).toBe("comfortable");
    expect(reconciled.forcedRemote).toBe(false);
  });

  it("preserves density and forcedRemote from saved state", () => {
    const saved: GridState = { columns: [], density: "compact", forcedRemote: true };
    const reconciled = reconcileState(saved, []);
    expect(reconciled.density).toBe("compact");
    expect(reconciled.forcedRemote).toBe(true);
  });
});

describe("buildRestorePlan", () => {
  it("builds column layout, sorters and header filters", () => {
    const reconciled = reconcileState(
      {
        columns: [{ name: "amount", width: 120, visible: true, frozen: false }],
        sorts: [{ field: "amount", direction: "desc" }],
        filters: [{ field: "status", operator: "eq", value: "open" }],
      },
      [col("amount"), col("status")],
    );
    const plan = buildRestorePlan(reconciled);
    expect(plan.columnLayout).toEqual([
      { field: "amount", width: 120, visible: true, frozen: false },
    ]);
    expect(plan.sorters).toEqual([{ field: "amount", dir: "desc" }]);
    expect(plan.headerFilters).toEqual([{ field: "status", value: "open" }]);
  });

  it("omits width when null/undefined", () => {
    const reconciled = reconcileState(
      { columns: [{ name: "amount" }] },
      [col("amount")],
    );
    const plan = buildRestorePlan(reconciled);
    expect(plan.columnLayout[0]).not.toHaveProperty("width");
  });
});

describe("defaultGridState", () => {
  it("returns empty default state", () => {
    const s = defaultGridState();
    expect(s.columns).toEqual([]);
    expect(s.sorts).toEqual([]);
    expect(s.filters).toEqual([]);
    expect(s.density).toBe("comfortable");
    expect(s.forcedRemote).toBe(false);
  });
});

describe("isStateConflict", () => {
  it("returns true when conflict is true", () => {
    expect(isStateConflict({ conflict: true })).toBe(true);
  });

  it("returns false when conflict is false or absent", () => {
    expect(isStateConflict({ conflict: false })).toBe(false);
    expect(isStateConflict({})).toBe(false);
  });
});
