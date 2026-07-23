import type { DomainDiagnostic } from "./types";

export const TIME_SERIES_PER_SERIES_LIMIT = 50_000;
export const TIME_SERIES_PER_PANEL_LIMIT = 100_000;
export const BAR_HARD_LIMIT = 5_000;
export const BAR_DEFAULT_VISIBLE = 100;
export const PIE_HARD_LIMIT = 50;
export const PIE_DEFAULT_VISIBLE = 20;

export interface NumericPoint {
  readonly x: number;
  readonly y: number;
}

export interface CategoryValue {
  readonly label: string;
  readonly value: number;
  /** Original dimension value used for cross-panel filtering. */
  readonly selectionValue?: unknown;
}

export interface LimitedCategories {
  readonly data: readonly CategoryValue[];
  readonly inputCount: number;
  readonly hardLimitExceeded: boolean;
  readonly truncatedCount: number;
}

/** Largest-Triangle-Three-Buckets downsampling. First and last are retained. */
export function lttb(points: readonly NumericPoint[], threshold: number): NumericPoint[] {
  if (threshold >= points.length || threshold === 0) return points.map(copyPoint);
  if (!Number.isInteger(threshold) || threshold < 3) {
    throw new RangeError("LTTB threshold must be zero or an integer of at least 3.");
  }
  const sampled: NumericPoint[] = [copyPoint(points[0]!)];
  const every = (points.length - 2) / (threshold - 2);
  let anchorIndex = 0;
  for (let i = 0; i < threshold - 2; i += 1) {
    const avgStart = Math.floor((i + 1) * every) + 1;
    const avgEnd = Math.min(Math.floor((i + 2) * every) + 1, points.length);
    let avgX = 0;
    let avgY = 0;
    const avgLength = Math.max(1, avgEnd - avgStart);
    for (let index = avgStart; index < avgEnd; index += 1) {
      avgX += points[index]!.x;
      avgY += points[index]!.y;
    }
    if (avgStart >= points.length) {
      avgX = points.at(-1)!.x;
      avgY = points.at(-1)!.y;
    } else {
      avgX /= avgLength;
      avgY /= avgLength;
    }

    const rangeStart = Math.floor(i * every) + 1;
    const rangeEnd = Math.min(Math.floor((i + 1) * every) + 1, points.length - 1);
    const anchor = points[anchorIndex]!;
    let maxArea = -1;
    let selectedIndex = rangeStart;
    for (let index = rangeStart; index < rangeEnd; index += 1) {
      const point = points[index]!;
      const area = Math.abs(
        (anchor.x - avgX) * (point.y - anchor.y) -
        (anchor.x - point.x) * (avgY - anchor.y),
      );
      if (area > maxArea) {
        maxArea = area;
        selectedIndex = index;
      }
    }
    sampled.push(copyPoint(points[selectedIndex]!));
    anchorIndex = selectedIndex;
  }
  sampled.push(copyPoint(points.at(-1)!));
  return sampled;
}

/**
 * Bucket sampler that retains local minima and maxima in original x order.
 * Useful when spikes must remain visible even after canvas-width sampling.
 */
export function sampleExtrema(points: readonly NumericPoint[], maxPoints: number): NumericPoint[] {
  if (maxPoints >= points.length || maxPoints === 0) return points.map(copyPoint);
  if (!Number.isInteger(maxPoints) || maxPoints < 4) {
    throw new RangeError("Extrema sampling requires zero or at least 4 points.");
  }
  const interior = points.slice(1, -1);
  const bucketCount = Math.max(1, Math.floor((maxPoints - 2) / 2));
  const bucketSize = interior.length / bucketCount;
  const selected: Array<{ index: number; point: NumericPoint }> = [];
  for (let bucket = 0; bucket < bucketCount; bucket += 1) {
    const start = Math.floor(bucket * bucketSize);
    const end = Math.min(interior.length, Math.floor((bucket + 1) * bucketSize));
    if (start >= end) continue;
    let minIndex = start;
    let maxIndex = start;
    for (let index = start + 1; index < end; index += 1) {
      if (interior[index]!.y < interior[minIndex]!.y) minIndex = index;
      if (interior[index]!.y > interior[maxIndex]!.y) maxIndex = index;
    }
    selected.push({ index: minIndex, point: interior[minIndex]! });
    if (maxIndex !== minIndex) selected.push({ index: maxIndex, point: interior[maxIndex]! });
  }
  selected.sort((a, b) => a.index - b.index);
  return [
    copyPoint(points[0]!),
    ...selected.slice(0, maxPoints - 2).map((item) => copyPoint(item.point)),
    copyPoint(points.at(-1)!),
  ];
}

export function timeSeriesLimitDiagnostics(seriesLengths: readonly number[]): DomainDiagnostic[] {
  const diagnostics: DomainDiagnostic[] = [];
  seriesLengths.forEach((count, index) => {
    if (count > TIME_SERIES_PER_SERIES_LIMIT) {
      diagnostics.push({
        code: "time_series_limit_exceeded",
        message: `Series ${index + 1} exceeds ${TIME_SERIES_PER_SERIES_LIMIT} points.`,
        path: `series.${index}`,
        severity: "error",
      });
    }
  });
  const total = seriesLengths.reduce((sum, count) => sum + count, 0);
  if (total > TIME_SERIES_PER_PANEL_LIMIT) {
    diagnostics.push({
      code: "panel_point_limit_exceeded",
      message: `A panel may contain at most ${TIME_SERIES_PER_PANEL_LIMIT} time-series points.`,
      severity: "error",
    });
  }
  return diagnostics;
}

export function prepareBarData(values: readonly CategoryValue[]): LimitedCategories {
  const accepted = sortCategories(values).slice(0, BAR_HARD_LIMIT);
  const data = accepted.slice(0, BAR_DEFAULT_VISIBLE).map(copyCategory);
  return {
    data,
    inputCount: values.length,
    hardLimitExceeded: values.length > BAR_HARD_LIMIT,
    truncatedCount: Math.max(0, values.length - data.length),
  };
}

export function preparePieData(
  values: readonly CategoryValue[],
  otherLabel = "Other",
  aggregateOther = true,
): LimitedCategories {
  const accepted = sortCategories(values).slice(0, PIE_HARD_LIMIT);
  const visible = accepted.slice(0, PIE_DEFAULT_VISIBLE).map(copyCategory);
  const remaining = accepted.slice(PIE_DEFAULT_VISIBLE);
  if (aggregateOther && remaining.length > 0) {
    visible.push({
      label: otherLabel,
      value: remaining.reduce((sum, item) => sum + item.value, 0),
      selectionValue: remaining.flatMap((item) =>
        item.selectionValue === undefined ? [] : [item.selectionValue]),
    });
  }
  return {
    data: visible,
    inputCount: values.length,
    hardLimitExceeded: values.length > PIE_HARD_LIMIT,
    truncatedCount: Math.max(0, values.length - PIE_DEFAULT_VISIBLE),
  };
}

function sortCategories(values: readonly CategoryValue[]): CategoryValue[] {
  return values.map(copyCategory).sort((a, b) => b.value - a.value || a.label.localeCompare(b.label));
}

function copyPoint(point: NumericPoint): NumericPoint {
  return { x: point.x, y: point.y };
}

function copyCategory(item: CategoryValue): CategoryValue {
  return { label: item.label, value: item.value, selectionValue: item.selectionValue };
}
