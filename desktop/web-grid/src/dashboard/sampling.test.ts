import { describe, expect, it } from "vitest";
import {
  lttb,
  prepareBarData,
  preparePieData,
  sampleExtrema,
  timeSeriesLimitDiagnostics,
} from "./sampling";

describe("dashboard sampling and limits", () => {
  it("LTTB retains endpoints and a large spike", () => {
    const points = Array.from({ length: 100 }, (_, x) => ({ x, y: x === 50 ? 10_000 : Math.sin(x) }));
    const sampled = lttb(points, 12);
    expect(sampled).toHaveLength(12);
    expect(sampled[0]).toEqual(points[0]);
    expect(sampled.at(-1)).toEqual(points.at(-1));
    expect(sampled.some((point) => point.x === 50)).toBe(true);
  });

  it("extrema sampling preserves local minima and maxima in x order", () => {
    const points = Array.from({ length: 22 }, (_, x) => ({ x, y: x % 5 === 0 ? 100 : -x }));
    const sampled = sampleExtrema(points, 10);
    expect(sampled.length).toBeLessThanOrEqual(10);
    expect(sampled[0]).toEqual(points[0]);
    expect(sampled.at(-1)).toEqual(points.at(-1));
    expect(sampled.every((point, index) => index === 0 || point.x >= sampled[index - 1]!.x)).toBe(true);
    expect(sampled.some((point) => point.y === 100)).toBe(true);
  });

  it("enforces 50000 points per series and 100000 per panel", () => {
    expect(timeSeriesLimitDiagnostics([50_000, 50_000])).toEqual([]);
    expect(timeSeriesLimitDiagnostics([50_001, 50_000]).map((item) => item.code)).toEqual([
      "time_series_limit_exceeded",
      "panel_point_limit_exceeded",
    ]);
  });

  it("caps bar input at 5000 and displays Top 100", () => {
    const values = Array.from({ length: 5_001 }, (_, index) => ({ label: `c${index}`, value: index }));
    const result = prepareBarData(values);
    expect(result.hardLimitExceeded).toBe(true);
    expect(result.data).toHaveLength(100);
    expect(result.data[0]?.value).toBe(5_000);
  });

  it("caps pie input at 50 and groups after Top 20 into Other", () => {
    const values = Array.from({ length: 51 }, (_, index) => ({ label: `c${index}`, value: index + 1 }));
    const result = preparePieData(values, "其他");
    expect(result.hardLimitExceeded).toBe(true);
    expect(result.data).toHaveLength(21);
    expect(result.data.at(-1)).toEqual({
      label: "其他",
      value: Array.from({ length: 30 }, (_, index) => index + 2).reduce((a, b) => a + b, 0),
      selectionValue: [],
    });
  });

  it("keeps every grouped pie selection in Other", () => {
    const values = Array.from({ length: 22 }, (_, index) => ({
      label: `c${index}`,
      value: 22 - index,
      selectionValue: { primaryField: "category", primaryValue: `c${index}`, values: { category: `c${index}` } },
    }));
    const other = preparePieData(values).data.at(-1);
    expect(other?.label).toBe("Other");
    expect(other?.selectionValue).toEqual([
      { primaryField: "category", primaryValue: "c20", values: { category: "c20" } },
      { primaryField: "category", primaryValue: "c21", values: { category: "c21" } },
    ]);
  });

  it("does not create an inexact Other bucket when tuple semantics are unavailable", () => {
    const values = Array.from({ length: 22 }, (_, index) => ({
      label: `c${index}`,
      value: 22 - index,
      selectionValue: { values: { region: `r${index}`, status: `s${index}` } },
    }));
    const result = preparePieData(values, "Other", false);
    expect(result.data).toHaveLength(20);
    expect(result.data.some((item) => item.label === "Other")).toBe(false);
    expect(result.truncatedCount).toBe(2);
  });

  it("rejects unusable sampling thresholds", () => {
    expect(() => lttb([{ x: 0, y: 0 }, { x: 1, y: 1 }], 2)).not.toThrow();
    expect(() => lttb(Array.from({ length: 5 }, (_, x) => ({ x, y: x })), 2)).toThrow(RangeError);
    expect(() => sampleExtrema(Array.from({ length: 5 }, (_, x) => ({ x, y: x })), 3)).toThrow(RangeError);
  });

  it("samples mainstream-PC workloads of 10k, 50k, and 100k points within the interaction budget", () => {
    for (const size of [10_000, 50_000, 100_000]) {
      const points = Array.from({ length: size }, (_, x) => ({ x, y: Math.sin(x / 37) * 100 + (x % 97 === 0 ? 500 : 0) }));
      const started = performance.now();
      const sampled = lttb(points, Math.min(size, 10_000));
      const elapsed = performance.now() - started;
      expect(sampled).toHaveLength(Math.min(size, 10_000));
      // This is intentionally a broad regression ceiling, not a machine
      // benchmark: the target 4-core/8GB PC should normally finish far below it.
      expect(elapsed).toBeLessThan(2_000);
    }
  }, 10_000);
});
