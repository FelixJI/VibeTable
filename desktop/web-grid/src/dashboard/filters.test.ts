import { describe, expect, it } from "vitest";
import {
  mergePanelFilters,
  resolveSelectionTargets,
  toDirectusFilter,
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
    expect(toDirectusFilter(merged)).toEqual({
      _and: [
        { status: { _eq: "open" } },
        { created_at: { _between: ["2026-01-01", "2026-01-31"] } },
        { owner: { _in: ["u1", "u2"] } },
        { amount: { _between: [10, 50] } },
        { region: { _eq: "east" } },
      ],
    });
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
    expect(toDirectusFilter(result)).toEqual({
      _and: [
        { owner: { _eq: "u1" } },
        { state: { _eq: "active" } },
        { account: { _in: ["a", "b"] } },
      ],
    });
  });

  it("drops self, missing and duplicate link targets", () => {
    expect(resolveSelectionTargets({
      sourcePanelId: "a",
      targetPanels: ["a", "b", "b", "missing"],
      targetField: "status",
      value: "open",
    }, ["a", "b", "c"])).toEqual(["b"]);
  });

  it("treats explicit null as cleared and maps null operators to Directus", () => {
    const result = mergePanelFilters("p", null, [{
      key: "state",
      label: "State",
      type: "enum",
      defaultValue: "active",
      allowedFields: ["state"],
      targetPanels: [],
    }], { state: null });
    expect(result).toBeNull();
    expect(toDirectusFilter({ field: "deleted_at", operator: "is_null" })).toEqual({
      deleted_at: { _null: true },
    });
    expect(toDirectusFilter({ field: "deleted_at", operator: "is_not_null" })).toEqual({
      deleted_at: { _nnull: true },
    });
  });
});
