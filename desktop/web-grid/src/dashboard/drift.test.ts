import { describe, expect, it } from "vitest";
import {
  compatibleReplacementFields,
  diagnoseFieldDrift,
  fieldTypesCompatible,
  referencesForQuery,
  type FieldReference,
} from "./drift";

describe("dashboard field drift", () => {
  it("collects structured query references without inventing raw JSON paths", () => {
    const references = referencesForQuery("p1", {
      collection: "orders",
      dimensions: ["status"],
      metrics: [{ field: "amount", aggregate: "sum" }, { field: "*", aggregate: "count" }],
      groupBy: ["status"],
      sorts: [{ field: "amount", direction: "desc" }],
      timeField: "created_at",
      timeGranularity: "day",
      filter: { and: [
        { field: "status", operator: "eq", value: "open" },
        { field: "region", operator: "in", value: ["east"] },
      ] },
    });
    expect(references.map((item) => `${item.role}:${item.field}`)).toEqual([
      "dimension:status",
      "metric:amount",
      "group:status",
      "sort:amount",
      "time:created_at",
      "filter:status",
      "filter:region",
    ]);
  });

  it("diagnoses missing collections, missing fields, permission loss and type changes locally", () => {
    const references: FieldReference[] = [
      { panelId: "a", collection: "gone", field: "id", role: "metric" },
      { panelId: "b", collection: "orders", field: "missing", role: "dimension", expectedType: "text" },
      { panelId: "c", collection: "orders", field: "owner", role: "filter", expectedType: "user" },
      { panelId: "d", collection: "orders", field: "amount", role: "metric", expectedType: "text" },
    ];
    const diagnostics = diagnoseFieldDrift(references, [{
      collection: "orders",
      fields: [
        { name: "title", type: "text" },
        { name: "owner", type: "user", readable: false },
        { name: "amount", type: "decimal" },
      ],
    }]);
    expect(diagnostics.map((item) => item.code)).toEqual([
      "dashboard_collection_missing",
      "dashboard_field_missing",
      "dashboard_field_unreadable",
      "dashboard_field_type_changed",
    ]);
    expect(diagnostics[1]?.compatibleFields).toEqual(["title"]);
  });

  it("suggests only readable compatible fields and never remaps automatically", () => {
    const reference: FieldReference = {
      panelId: "p",
      collection: "orders",
      field: "amount_old",
      role: "metric",
      expectedType: "decimal",
    };
    expect(compatibleReplacementFields(reference, [
      { name: "count", type: "integer" },
      { name: "amount_new", type: "decimal" },
      { name: "secret", type: "decimal", readable: false },
      { name: "title", type: "text" },
    ])).toEqual(["amount_new", "count"]);
    expect(fieldTypesCompatible("date", "date-time")).toBe(true);
    expect(fieldTypesCompatible("text", "decimal")).toBe(false);
  });

  it("does not flag a display-label-only change when the internal field name survives", () => {
    expect(diagnoseFieldDrift([{
      panelId: "p",
      collection: "orders",
      field: "amount",
      role: "metric",
      expectedType: "decimal",
    }], [{ collection: "orders", fields: [{ name: "amount", type: "decimal" }] }])).toEqual([]);
  });
});
