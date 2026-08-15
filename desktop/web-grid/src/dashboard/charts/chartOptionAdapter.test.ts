import { describe, expect, it } from "vitest";
import { parseWirePanel } from "../model";
import {
  buildDashboardChartOption,
  dashboardChartKeyboardSelections,
  dashboardChartSelectionValue,
  dashboardMeasureKeys,
  extractCategories,
  numericValue,
} from "./chartOptionAdapter";

function series(option: ReturnType<typeof buildDashboardChartOption>): Array<Record<string, unknown>> {
  const value = (option as Record<string, unknown>).series;
  return (Array.isArray(value) ? value : [value]).filter((item): item is Record<string, unknown> => typeof item === "object" && item !== null);
}

describe("dashboard chart option adapter", () => {
  it("renders every stable measure key as a separate categorical line series", () => {
    const panel = parseWirePanel({
      id: "p", dashboardId: "d", name: "Compare", type: "line",
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
      id: "p", dashboardId: "d", name: "By year", type: "bar",
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

  it.each(["pie", "donut"] as const)("builds selectable %s slices with stable visual limits", (type) => {
    const panel = parseWirePanel({
      id: "p", dashboardId: "d", name: "Share", type,
      position: { x: 0, y: 0, width: 8, height: 4 }, options: {},
      query: { kind: "aggregate", collection: "orders", dimensions: ["status"], measures: [{ key: "orders", op: "count", field: null }] },
    });
    const option = buildDashboardChartOption(panel, Array.from({ length: 21 }, (_, index) => ({
      status: `s${index}`,
      orders: index + 1,
    })), true, "其余");
    expect((option.color as string[])[0]).toBe("#6f9cff");
    expect(option.animation).toBe(true);
    expect(series(option)[0]?.radius).toEqual(type === "donut" ? ["44%", "70%"] : "70%");
    expect(series(option)[0]?.label).toEqual({ show: false });
    expect(series(option)[0]?.data).toEqual(expect.arrayContaining([
      expect.objectContaining({ name: "其余", selectionValue: expect.any(Array) }),
    ]));
  });

  it("enables category zoom and disables animation for a large bar result", () => {
    const panel = parseWirePanel({
      id: "p", dashboardId: "d", name: "Large", type: "bar",
      position: { x: 0, y: 0, width: 8, height: 4 }, options: {},
      query: { kind: "aggregate", collection: "orders", dimensions: ["status"], measures: [{ key: "orders", op: "count", field: null }] },
    });
    const option = buildDashboardChartOption(panel, Array.from({ length: 600 }, (_, index) => ({
      status: `s${index}`,
      orders: index,
    })), false);
    expect(option.animation).toBe(false);
    expect(option.dataZoom).toHaveLength(2);
  });

  it("uses ordinal and nested fallback labels when dimensions are absent", () => {
    const panel = parseWirePanel({
      id: "p", dashboardId: "d", name: "Fallback", type: "line",
      position: { x: 0, y: 0, width: 8, height: 4 }, options: {},
      query: { kind: "aggregate", collection: "orders", dimensions: [], measures: [{ key: "orders", op: "count", field: null }] },
    });
    const option = buildDashboardChartOption(panel, [
      { count: 9, orders: "4", meta: { label: "嵌套标签" } },
      { count: 3, orders: Number.NaN, meta: {} },
    ], false);
    expect(option.xAxis).toMatchObject({ data: ["嵌套标签", "2"] });
    expect(series(option)[0]?.data).toEqual([
      expect.objectContaining({ value: 4 }),
      expect.objectContaining({ value: 0 }),
    ]);
  });

  it("groups, zooms, and samples very long time series with fallback timestamps", () => {
    const panel = parseWirePanel({
      id: "p", dashboardId: "d", name: "Long trend", type: "time-series",
      position: { x: 0, y: 0, width: 8, height: 4 }, options: { fillType: "gradient" },
      query: { kind: "aggregate", collection: "orders", dimensions: [], measures: [{ key: "revenue", op: "sum", field: "amount" }], timeBucket: { field: "created_at", unit: "day" } },
    });
    const rows = Array.from({ length: 10_001 }, (_, index) => ({
      created_at: index === 0 ? "not-a-date" : new Date(Date.UTC(2026, 0, 1, 0, index)).toISOString(),
      revenue: index,
    }));
    const option = buildDashboardChartOption(panel, rows, false);
    expect(option.animation).toBe(false);
    expect(option.dataZoom).toHaveLength(2);
    expect(series(option)[0]?.data).toHaveLength(10_000);
    expect(series(option)[0]?.areaStyle).toEqual({ opacity: 0.12 });
    expect((series(option)[0]?.data as Array<Record<string, unknown>>)[0]?.selectionValue)
      .toMatchObject({ primaryField: "created_at" });
  });

  it("normalizes measures, numeric values, keyboard selections, and chart events", () => {
    const bar = parseWirePanel({
      id: "p", dashboardId: "d", name: "Keyboard", type: "bar",
      position: { x: 0, y: 0, width: 8, height: 4 }, options: {},
      query: { kind: "aggregate", collection: "orders", dimensions: ["status", 7], measures: [{ key: "orders", op: "count", field: null }, { op: "sum" }, null] },
    });
    expect(dashboardMeasureKeys(bar)).toEqual(["orders"]);
    expect(extractCategories(bar, [{ status: "paid", orders: 2 }], "orders"))
      .toEqual([{ label: "paid", value: 2, selectionValue: { primaryField: "status", primaryValue: "paid", values: { status: "paid" } } }]);
    expect(numericValue({ first: "", nested: { bad: "x", amount: "12.5" } })).toBe(12.5);
    expect(numericValue({ first: Infinity, nested: null })).toBe(0);
    expect(dashboardChartKeyboardSelections(bar, [{ status: "paid", orders: 2 }], 1)[0]?.label).toBe("paid");
    expect(dashboardChartSelectionValue(null)).toBeUndefined();
    expect(dashboardChartSelectionValue({ value: [123, 2] })).toBe(123);
    expect(dashboardChartSelectionValue({ name: "paid" })).toBe("paid");

    const time = parseWirePanel({
      id: "t", dashboardId: "d", name: "Time keyboard", type: "time-series",
      position: { x: 0, y: 0, width: 8, height: 4 }, options: {},
      query: { kind: "aggregate", collection: "orders", dimensions: [], measures: [{ key: "orders", op: "count", field: null }], timeBucket: { field: "created_at", unit: "day" } },
    });
    expect(dashboardChartKeyboardSelections(time, [{ created_at: "2026-01-01", orders: 1 }])[0]?.label)
      .toBe("2026-01-01T00:00:00.000Z");
  });
});
