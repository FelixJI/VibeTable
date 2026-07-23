import { describe, expect, it } from "vitest";
import { parseWirePanel } from "../model";
import { buildDashboardChartOption, dashboardChartSelectionValue } from "./chartOptionAdapter";

function series(option: ReturnType<typeof buildDashboardChartOption>): Array<Record<string, unknown>> {
  const value = (option as Record<string, unknown>).series;
  return (Array.isArray(value) ? value : [value]).filter((item): item is Record<string, unknown> => typeof item === "object" && item !== null);
}

describe("dashboard chart option adapter", () => {
  it("renders every stable measure key as a separate categorical line series", () => {
    const panel = parseWirePanel({
      id: "p", dashboardId: "d", name: "Compare", type: "line-chart",
      position: { x: 0, y: 0, width: 8, height: 4 }, options: {},
      query: { kind: "aggregate", collection: "orders", dimensions: ["status"], measures: [{ key: "revenue", op: "sum", field: "amount" }, { key: "orders", op: "count", field: null }] },
    });
    const option = buildDashboardChartOption(panel, [
      { status: "paid", revenue: 42, orders: 3 },
      { status: "pending", revenue: 10, orders: 2 },
    ], false);
    expect(series(option).map((item) => item.name)).toEqual(["revenue", "orders"]);
    expect(series(option)[0]?.data).toEqual([
      { value: 42, selectionValue: { primaryField: "status", primaryValue: "paid", values: { status: "paid" } } },
      { value: 10, selectionValue: { primaryField: "status", primaryValue: "pending", values: { status: "pending" } } },
    ]);
  });

  it("splits time-series rows by the declared series dimension", () => {
    const panel = parseWirePanel({
      id: "p", dashboardId: "d", name: "Trend", type: "time-series",
      position: { x: 0, y: 0, width: 8, height: 4 }, options: {},
      query: { kind: "aggregate", collection: "orders", dimensions: ["region", "status"], measures: [{ key: "revenue", op: "sum", field: "amount" }], timeBucket: { field: "created_at", unit: "day" } },
    });
    const option = buildDashboardChartOption(panel, [
      { created_at: "2026-07-01", region: "east", status: "paid", revenue: 42 },
      { created_at: "2026-07-01", region: "east", status: "pending", revenue: 30 },
      { created_at: "2026-07-02", region: "east", status: "paid", revenue: 48 },
    ], false);
    expect(series(option).map((item) => item.name)).toEqual(["east / paid · revenue", "east / pending · revenue"]);
    expect(series(option)[0]?.data).toHaveLength(2);
    expect((series(option)[0]?.data as Array<Record<string, unknown>>)[0]?.selectionValue).toEqual({
      primaryField: "created_at",
      primaryValue: "2026-07-01T00:00:00.000Z",
      values: { created_at: "2026-07-01T00:00:00.000Z", region: "east", status: "paid" },
    });
  });

  it("uses only stable query measure keys and keeps numeric dimensions as selections", () => {
    const panel = parseWirePanel({
      id: "p", dashboardId: "d", name: "By year", type: "bar-chart",
      position: { x: 0, y: 0, width: 8, height: 4 }, options: {},
      query: { kind: "aggregate", collection: "orders", dimensions: ["year"], measures: [{ key: "revenue", op: "sum", field: "amount" }] },
    });
    const option = buildDashboardChartOption(panel, [
      { year: 2025, revenue: 42 },
      { year: 2026, revenue: 90 },
    ], false);
    expect(series(option)[0]?.data).toEqual([
      { value: 90, selectionValue: { primaryField: "year", primaryValue: 2026, values: { year: 2026 } } },
      { value: 42, selectionValue: { primaryField: "year", primaryValue: 2025, values: { year: 2025 } } },
    ]);
  });

  it("extracts category and time selections instead of metric values", () => {
    expect(dashboardChartSelectionValue({ data: { value: 42, selectionValue: "paid" }, value: 42 })).toBe("paid");
    expect(dashboardChartSelectionValue({ data: { value: [123, 42], selectionValue: 123 }, value: [123, 42] })).toBe(123);
    expect(dashboardChartSelectionValue({ value: 42 })).toBeUndefined();
  });
});
