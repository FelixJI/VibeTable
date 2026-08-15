import { describe, expect, it, vi } from "vitest";
import type { DashboardPanelQueryPayload, DashboardQueryLimitsPayload } from "@/contracts";
import {
  BindingRuntime,
  canonicalDashboardQuery,
  type BindingCollectionSchema,
  type BindingQueryExecutor,
  type SchemaCatalog,
} from "./bindingRuntime";

const schema: BindingCollectionSchema = {
  collectionId: "orders",
  revision: "schema:7",
  fields: [
    field("status", "Status", "text", ["eq", "in"], true, ["count", "countDistinct"]),
    field("region", "Region", "text", ["eq", "in"], true, ["count", "countDistinct"]),
    field("amount", "Amount", "decimal", ["eq", "gt", "between"], true, ["count", "countDistinct", "sum", "avg", "min", "max"]),
    field("created_at", "Created", "datetime", ["eq", "gte", "lt", "between"], true, ["count", "countDistinct", "min", "max"]),
  ],
};

const limits: DashboardQueryLimitsPayload = {
  maxConcurrentRequests: 6,
  maxSeriesPoints: 50_000,
  maxPanelPoints: 100_000,
  maxCategoryPoints: 5_000,
  defaultTopN: 100,
  maxPieSlices: 50,
  maxListRows: 100,
};

function field(
  ref: string,
  label: string,
  dataType: BindingCollectionSchema["fields"][number]["dataType"],
  filterOperators: readonly string[],
  groupable: boolean,
  summaryOperations: readonly string[],
): BindingCollectionSchema["fields"][number] {
  return { ref, fieldId: `fld:${ref}`, label, dataType, filterOperators, groupable, summaryOperations };
}

function catalog(value = schema): SchemaCatalog {
  return { describe: vi.fn(async () => value) };
}

describe("BindingRuntime", () => {
  it("keeps every dimension, measure, filter and sort in canonical order", () => {
    const source = {
      kind: "aggregate",
      collection: "orders",
      dimensions: ["region", "status"],
      measures: [
        { key: "revenue", op: "sum", field: "amount" },
        { key: "orders", op: "count", field: null },
      ],
      filters: [
        { field: "status", operator: "in", value: ["paid", "open"] },
        { field: "amount", operator: "gt", value: 0 },
      ],
      timeBucket: { field: "created_at", unit: "week", timezone: "UTC" },
      limit: 4_000,
      topN: 40,
    } satisfies DashboardPanelQueryPayload;

    expect(canonicalDashboardQuery(source)).toEqual(source);
  });

  it("returns structured drift diagnostics and never executes an invalid binding", async () => {
    const execute = vi.fn<BindingQueryExecutor["execute"]>();
    const runtime = new BindingRuntime(catalog(), { execute });
    const result = await runtime.evaluate({
      panelId: "p1",
      panelType: "bar",
      query: {
        kind: "aggregate",
        collection: "orders",
        dimensions: ["deleted_region"],
        measures: [{ key: "value", op: "sum", field: "status" }],
        filters: [{ field: "amount", operator: "contains", value: "4" }],
      },
    }, { limits, runtimeFilters: [] }, new AbortController().signal);

    expect(result.state).toBe("drift");
    expect(result.diagnostics.map((item) => [item.code, item.path])).toEqual([
      ["binding.field_missing", "query.dimensions.0"],
      ["binding.summary_unsupported", "query.measures.0.op"],
      ["binding.operator_unsupported", "query.filters.0.operator"],
    ]);
    expect(execute).not.toHaveBeenCalled();
  });

  it("applies runtime filters, server limits, and forwards cancellation", async () => {
    let markStarted!: () => void;
    const started = new Promise<void>((resolve) => { markStarted = resolve; });
    const executor: BindingQueryExecutor = {
      execute: vi.fn(async (_panelType, query, signal) => {
        expect(query).toMatchObject({
          kind: "aggregate",
          filters: [{ field: "status", operator: "eq", value: "paid" }],
          limit: 50,
          topN: 50,
        });
        markStarted();
        await new Promise((_done, reject) => signal.addEventListener("abort", () => reject(signal.reason), { once: true }));
        return { rows: [], truncated: false, maxPoints: 50 };
      }),
    };
    const runtime = new BindingRuntime(catalog(), executor);
    const controller = new AbortController();
    const evaluating = runtime.evaluate({
      panelId: "p1",
      panelType: "pie",
      query: {
        kind: "aggregate",
        collection: "orders",
        dimensions: ["status"],
        measures: [{ key: "value", op: "count", field: null }],
        limit: 500,
        topN: 500,
      },
    }, {
      limits,
      runtimeFilters: [{ field: "status", operator: "eq", value: "paid" }],
    }, controller.signal);
    await started;
    controller.abort(new DOMException("cancelled", "AbortError"));
    await expect(evaluating).resolves.toMatchObject({ state: "cancelled" });
  });

  it("maps executor errors without losing their product code", async () => {
    const runtime = new BindingRuntime(catalog(), {
      execute: async () => {
        const error = new Error("query rejected") as Error & { code: string };
        error.code = "query.aggregate.type_mismatch";
        throw error;
      },
    });
    const result = await runtime.evaluate({
      panelId: "p1",
      panelType: "metric",
      query: {
        kind: "aggregate", collection: "orders", dimensions: [],
        measures: [{ key: "value", op: "sum", field: "amount" }],
      },
    }, { limits, runtimeFilters: [] }, new AbortController().signal);
    expect(result).toMatchObject({ state: "error", error: { code: "query.aggregate.type_mismatch", message: "query rejected" } });
  });
});
