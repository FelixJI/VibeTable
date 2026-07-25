import { describe, expect, it } from "vitest";
import {
  mergePanelFilters,
  resolveSelectionTargets,
  toProductFilters,
  type DashboardFilterVariable,
} from "./filters";

const variables: DashboardFilterVariable[] = [
  {
    key: "period",
    label: "Period",
    type: "date-range",
    allowedFields: ["created_at"],
    targetPanels: ["chart"],
  },
  {
    key: "owners",
    label: "Owner",
    type: "user",
    allowedFields: ["owner"],
    targetPanels: [],
  },
  {
    key: "amount",
    label: "Amount",
    type: "number-range",
    allowedFields: ["amount"],
    targetPanels: ["chart"],
  },
];

describe("dashboard filters", () => {
  it("merges own, global and linked filters with explicit AND", () => {
    const merged = mergePanelFilters(
      "chart",
      { field: "status", operator: "eq", value: "open" },
      variables,
      { period: ["2026-01-01", "2026-01-31"], owners: ["u1", "u2"], amount: [10, 50] },
      [{
        sourcePanelId: "source",
        targetPanels: ["chart"],
        targetField: "region",
        value: "east",
      }],
    );
    expect(toProductFilters(merged)).toEqual([
      { field: "status", operator: "eq", value: "open" },
      { field: "created_at", operator: "between", value: ["2026-01-01", "2026-01-31"] },
      { field: "owner", operator: "in", value: ["u1", "u2"] },
      { field: "amount", operator: "between", value: [10, 50] },
      { field: "region", operator: "eq", value: "east" },
    ]);
  });

  it("supports all five variable types and skips inactive/untargeted values", () => {
    const all: DashboardFilterVariable[] = [
      ...variables,
      { key: "state", label: "State", type: "enum", allowedFields: ["state"], targetPanels: [] },
      { key: "account", label: "Account", type: "relation", allowedFields: ["account"], targetPanels: [] },
    ];
    const result = mergePanelFilters("other", null, all, {
      period: null,
      owners: "u1",
      amount: [0, 10],
      state: "active",
      account: ["a", "b"],
    });
    expect(toProductFilters(result)).toEqual([
      { field: "owner", operator: "eq", value: "u1" },
      { field: "state", operator: "eq", value: "active" },
      { field: "account", operator: "in", value: ["a", "b"] },
    ]);
  });

  it("drops self, missing and duplicate link targets", () => {
    expect(resolveSelectionTargets({
      sourcePanelId: "a",
      targetPanels: ["a", "b", "b", "missing"],
      targetField: "status",
      value: "open",
    }, ["a", "b", "c"])).toEqual(["b"]);
  });

  it("treats explicit null as cleared and preserves null operators", () => {
    const result = mergePanelFilters("p", null, [{
      key: "state",
      label: "State",
      type: "enum",
      defaultValue: "active",
      allowedFields: ["state"],
      targetPanels: [],
    }], { state: null });
    expect(result).toBeNull();
    expect(toProductFilters({ field: "deleted_at", operator: "is_null" })).toEqual([
      { field: "deleted_at", operator: "is_null" },
    ]);
    expect(toProductFilters({ field: "deleted_at", operator: "is_not_null" })).toEqual([
      { field: "deleted_at", operator: "is_not_null" },
    ]);
  });
});
