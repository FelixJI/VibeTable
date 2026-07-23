import { describe, expect, it } from "vitest";
import {
  operatorsForField,
  validateDashboardQuery,
  type Aggregate,
  type QueryFieldSchema,
} from "./query";

const fields: QueryFieldSchema[] = [
  { name: "created_at", type: "date-time" },
  { name: "amount", type: "decimal" },
  { name: "status", type: "enum" },
  { name: "owner", type: "user", readable: false },
  { name: "payload", type: "json" },
];

describe("dashboard structured query", () => {
  it("uses the frozen cross-language aggregate vocabulary", () => {
    const aggregates: Aggregate[] = ["count", "countDistinct", "sum", "avg", "min", "max"];
    expect(aggregates).toEqual(["count", "countDistinct", "sum", "avg", "min", "max"]);
  });

  it("exposes a field-type/operator compatibility matrix", () => {
    expect(operatorsForField("text")).toContain("contains");
    expect(operatorsForField("decimal")).toContain("between");
    expect(operatorsForField("boolean")).not.toContain("gt");
    expect(operatorsForField("relation")).not.toContain("contains");
    expect(operatorsForField("json")).toEqual(["contains", "is_null", "is_not_null"]);
  });

  it("accepts a valid aggregate time-series query", () => {
    expect(validateDashboardQuery({
      collection: "orders",
      dimensions: ["status"],
      metrics: [{ field: "amount", aggregate: "sum", alias: "revenue" }],
      filter: { field: "amount", operator: "between", value: [10, 100] },
      groupBy: ["status"],
      sorts: [{ field: "amount", direction: "desc" }],
      timeField: "created_at",
      timeGranularity: "day",
      limit: 500,
    }, fields)).toEqual([]);
  });

  it("returns stable diagnostics for missing, unreadable and incompatible fields", () => {
    const diagnostics = validateDashboardQuery({
      collection: "",
      metrics: [
        { field: "status", aggregate: "sum" },
        { field: "missing", aggregate: "count" },
      ],
      filter: { and: [
        { field: "status", operator: "between", value: [1] },
        { field: "owner", operator: "eq", value: "u1" },
        { field: "status", operator: "eq" },
      ] },
      timeField: "amount",
      limit: 0,
    }, fields);
    expect(diagnostics.map((item) => item.code)).toEqual(expect.arrayContaining([
      "query_collection_missing",
      "query_aggregate_incompatible",
      "query_field_missing",
      "query_operator_incompatible",
      "query_between_invalid",
      "query_field_unreadable",
      "query_filter_value_missing",
      "query_time_field_incompatible",
      "query_time_granularity_missing",
      "query_limit_invalid",
    ]));
  });

  it("allows count star but rejects a granularity without a time field", () => {
    const diagnostics = validateDashboardQuery({
      collection: "orders",
      metrics: [{ field: "*", aggregate: "count" }],
      timeGranularity: "month",
    }, fields);
    expect(diagnostics).toEqual([
      expect.objectContaining({ code: "query_time_field_missing" }),
    ]);
  });
});
