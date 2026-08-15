import type { DashboardPanel } from "../types";
import { lttb, prepareBarData, preparePieData, type CategoryValue } from "../sampling";
import type { DashboardChartOption } from "./echartsCore";

const LIGHT_PALETTE = ["#2864dc", "#0f8f78", "#d17b0f", "#7556d8", "#d0496f", "#4f759b"];
const DARK_PALETTE = ["#6f9cff", "#42c6aa", "#f5ab4d", "#a98cff", "#ef7d9c", "#84a9cf"];

export interface DashboardChartSelection {
  readonly primaryField: string | null;
  readonly primaryValue: unknown;
  readonly values: Readonly<Record<string, unknown>>;
}

export function buildDashboardChartOption(
  panel: DashboardPanel,
  rows: readonly Record<string, unknown>[],
  dark: boolean,
  otherLabel = "Other",
): DashboardChartOption {
  const palette = dark ? DARK_PALETTE : LIGHT_PALETTE;
  const measureKeys = dashboardMeasureKeys(panel);
  const categories = extractCategories(panel, rows, measureKeys[0]);
  if (panel.productType === "pie" || panel.productType === "donut") {
    // A multi-dimensional "Other" would require OR-of-tuples filtering.
    // Keep exact Top items instead of approximating it with a Cartesian IN.
    const prepared = preparePieData(categories, otherLabel, dimensionKeys(panel).length <= 1);
    return {
      color: palette,
      animation: rows.length < 500,
      tooltip: { trigger: "item" },
      legend: { type: "scroll", bottom: 0 },
      series: [{
        type: "pie",
        radius: panel.productType === "donut" ? ["44%", "70%"] : "70%",
        center: ["50%", "43%"],
        data: prepared.data.map((item) => ({
          name: item.label,
          value: item.value,
          selectionValue: item.selectionValue,
        })),
        label: { show: prepared.data.length <= 12 },
        emphasis: { scaleSize: 5 },
      }],
    };
  }
  if (panel.productType === "bar") {
    const prepared = prepareBarData(categories);
    return {
      color: palette,
      animation: rows.length < 500,
      grid: { left: 44, right: 18, top: 18, bottom: 58, containLabel: true },
      tooltip: { trigger: "axis", axisPointer: { type: "shadow" } },
      xAxis: { type: "category", data: prepared.data.map((item) => item.label), axisLabel: { hideOverlap: true } },
      yAxis: { type: "value", splitLine: { lineStyle: { opacity: 0.18 } } },
      dataZoom: prepared.data.length > 30 ? [{ type: "inside" }, { type: "slider", height: 14 }] : [],
      series: [{
        type: "bar",
        data: prepared.data.map((item) => ({ value: item.value, selectionValue: item.selectionValue })),
        large: true,
        largeThreshold: 600,
      }],
    };
  }

  if (panel.productType === "line") {
    const labels = rows.map((row, index) => categoryLabel(panel, row, index, new Set(measureKeys)));
    const selections = rows.map((row, index) => rowSelection(panel, row, index, new Set(measureKeys)));
    return {
      color: palette,
      animation: rows.length < 2_000,
      grid: { left: 50, right: 18, top: 24, bottom: 48, containLabel: true },
      tooltip: { trigger: "axis" },
      legend: { type: "scroll", top: 0 },
      xAxis: { type: "category", data: labels, axisLabel: { hideOverlap: true } },
      yAxis: { type: "value", scale: true, splitLine: { lineStyle: { opacity: 0.18 } } },
      dataZoom: rows.length > 200 ? [{ type: "inside" }, { type: "slider", height: 14 }] : [],
      series: measureKeys.map((key) => ({
        type: "line" as const,
        name: key,
        data: rows.map((row, index) => ({ value: measureValue(row, key), selectionValue: selections[index] })),
        showSymbol: rows.length < 200,
        sampling: "lttb" as const,
        lineStyle: { width: 2 },
      })),
    };
  }

  const dimensions = Array.isArray(panel.query.dimensions)
    ? panel.query.dimensions.filter((item): item is string => typeof item === "string")
    : [];
  const seriesFields = dimensions;
  const groups = new Map<string, Array<{ x: number; y: number; selectionValue: DashboardChartSelection }>>();
  for (let index = 0; index < rows.length; index += 1) {
    const row = rows[index]!;
    for (const key of measureKeys) {
      const seriesValues = seriesFields.flatMap((field) => row[field] === undefined ? [] : [String(row[field])]);
      const group = seriesValues.length > 0 ? `${seriesValues.join(" / ")} · ${key}` : key;
      const points = groups.get(group) ?? [];
      const x = numericX(panel, row, index);
      points.push({ x, y: measureValue(row, key), selectionValue: timeRowSelection(panel, row, x) });
      groups.set(group, points);
    }
  }
  return {
    color: palette,
    animation: rows.length < 2_000,
    grid: { left: 50, right: 18, top: 18, bottom: 42, containLabel: true },
    tooltip: { trigger: "axis" },
    xAxis: { type: "time", splitLine: { show: false } },
    yAxis: { type: "value", scale: true, splitLine: { lineStyle: { opacity: 0.18 } } },
    legend: { type: "scroll", top: 0 },
    dataZoom: rows.length > 200 ? [{ type: "inside" }, { type: "slider", height: 14 }] : [],
    series: [...groups.entries()].map(([name, points]) => {
      const selectionByX = new Map(points.map((point) => [point.x, point.selectionValue]));
      const sampled = points.length > 10_000 ? lttb(points, 10_000) : points;
      return {
        type: "line" as const,
        name,
        data: sampled.map((point) => ({
          value: [point.x, point.y],
          selectionValue: selectionByX.get(point.x) ?? {
            primaryField: null,
            primaryValue: new Date(point.x).toISOString(),
            values: {},
          },
        })),
        showSymbol: sampled.length < 200,
        sampling: "lttb" as const,
        smooth: true,
        lineStyle: { width: 2 },
        areaStyle: panel.options.fillType === "gradient" ? { opacity: 0.12 } : undefined,
      };
    }),
  };
}

export function dashboardMeasureKeys(panel: DashboardPanel): string[] {
  if (Array.isArray(panel.query.measures)) {
    const keys = panel.query.measures.flatMap((item) => isRecord(item) && typeof item.key === "string" ? [item.key] : []);
    if (keys.length > 0) return keys;
  }
  return [];
}

export function extractCategories(
  panel: DashboardPanel,
  rows: readonly Record<string, unknown>[],
  measureKey?: string,
): CategoryValue[] {
  const measures = new Set(dashboardMeasureKeys(panel));
  return rows.map((row, index) => ({
    label: categoryLabel(panel, row, index, measures),
    value: measureKey ? measureValue(row, measureKey) : 0,
    selectionValue: rowSelection(panel, row, index, measures),
  }));
}

export function numericValue(row: Record<string, unknown>): number {
  for (const value of Object.values(row)) {
    const direct = numberValue(value);
    if (direct !== null) return direct;
    if (isRecord(value)) {
      for (const nested of Object.values(value)) {
        const parsed = numberValue(nested);
        if (parsed !== null) return parsed;
      }
    }
  }
  return 0;
}

function categoryLabel(
  panel: DashboardPanel,
  row: Record<string, unknown>,
  index: number,
  measureKeys = new Set<string>(),
): string {
  const selection = categorySelectionValue(panel, row, index, measureKeys);
  return selection === null || selection === undefined ? String(index + 1) : String(selection);
}

function categorySelectionValue(
  panel: DashboardPanel,
  row: Record<string, unknown>,
  index: number,
  measureKeys = new Set<string>(),
): unknown {
  const dimension = dimensionKeys(panel)[0];
  if (dimension && row[dimension] !== undefined) return row[dimension];
  for (const [key, value] of Object.entries(row)) {
    if (measureKeys.has(key)) continue;
    if (["count", "countDistinct", "sum", "avg", "min", "max"].includes(key)) continue;
    if (value !== null && typeof value !== "object") return value;
    if (isRecord(value)) {
      const candidate = Object.values(value).find((item) => typeof item === "string");
      if (candidate !== undefined) return candidate;
    }
  }
  return index + 1;
}

function numericX(panel: DashboardPanel, row: Record<string, unknown>, index: number): number {
  const timeBucket = isRecord(panel.query.timeBucket) ? panel.query.timeBucket : null;
  const timeField = typeof timeBucket?.field === "string" ? timeBucket.field : null;
  if (timeField) {
    const parsed = parseTimeValue(row[timeField]);
    if (parsed !== null) return parsed;
  }
  for (const value of Object.values(row)) {
    const parsed = parseTimeValue(value);
    if (parsed !== null) return parsed;
  }
  return index;
}

function parseTimeValue(value: unknown): number | null {
  if (typeof value !== "string") return null;
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : null;
}

function timeSelectionValue(panel: DashboardPanel, row: Record<string, unknown>, x: number): string {
  const timeBucket = isRecord(panel.query.timeBucket) ? panel.query.timeBucket : null;
  const field = typeof timeBucket?.field === "string" ? timeBucket.field : null;
  const parsed = parseTimeValue(field ? row[field] : undefined);
  return new Date(parsed ?? x).toISOString();
}

function rowSelection(
  panel: DashboardPanel,
  row: Record<string, unknown>,
  index: number,
  measureKeys = new Set<string>(),
): DashboardChartSelection {
  const dimensions = dimensionKeys(panel);
  const values = Object.fromEntries(
    dimensions.flatMap((field) => row[field] === undefined ? [] : [[field, row[field]]]),
  );
  const primaryField = dimensions[0] ?? null;
  const primaryValue = primaryField
    ? values[primaryField]
    : categorySelectionValue(panel, row, index, measureKeys);
  return { primaryField, primaryValue, values };
}

function timeRowSelection(
  panel: DashboardPanel,
  row: Record<string, unknown>,
  x: number,
): DashboardChartSelection {
  const timeBucket = isRecord(panel.query.timeBucket) ? panel.query.timeBucket : null;
  const timeField = typeof timeBucket?.field === "string" ? timeBucket.field : null;
  const timeValue = timeSelectionValue(panel, row, x);
  const values: Record<string, unknown> = Object.fromEntries(
    dimensionKeys(panel).flatMap((field) => row[field] === undefined ? [] : [[field, row[field]]]),
  );
  if (timeField) values[timeField] = timeValue;
  return { primaryField: timeField, primaryValue: timeValue, values };
}

function dimensionKeys(panel: DashboardPanel): string[] {
  return Array.isArray(panel.query.dimensions)
    ? panel.query.dimensions.filter((item): item is string => typeof item === "string")
    : [];
}

function measureValue(row: Record<string, unknown>, key: string): number {
  return numberValue(row[key]) ?? 0;
}

export function dashboardChartSelectionValue(event: unknown): unknown {
  if (!isRecord(event)) return undefined;
  if (isRecord(event.data) && Object.prototype.hasOwnProperty.call(event.data, "selectionValue")) {
    return event.data.selectionValue;
  }
  if (Array.isArray(event.value)) return event.value[0];
  return typeof event.name === "string" ? event.name : undefined;
}

export function dashboardChartKeyboardSelections(
  panel: DashboardPanel,
  rows: readonly Record<string, unknown>[],
  limit = 200,
): Array<{ label: string; value: unknown }> {
  const measures = new Set(dashboardMeasureKeys(panel));
  return rows.slice(0, limit).map((row, index) => {
    if (panel.productType === "time-series") {
      const x = numericX(panel, row, index);
      const selection = timeRowSelection(panel, row, x);
      return {
        label: String(selection.primaryValue),
        value: selection,
      };
    }
    const selection = rowSelection(panel, row, index, measures);
    return { label: String(selection.primaryValue ?? index + 1), value: selection };
  });
}

function numberValue(value: unknown): number | null {
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string" && value.trim() !== "") {
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : null;
  }
  return null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
