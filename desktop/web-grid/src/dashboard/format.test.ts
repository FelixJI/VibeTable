import { describe, expect, it } from "vitest";
import { formatDashboardDate, formatDashboardNumber } from "./format";

describe("dashboard Intl formatting", () => {
  it("formats numbers, percentages, currencies and compact notation by locale", () => {
    expect(formatDashboardNumber(12_345.678, "en-US", { maximumFractionDigits: 1 })).toBe("12,345.7");
    expect(formatDashboardNumber(12_345.678, "zh-CN", { maximumFractionDigits: 1 })).toBe("12,345.7");
    expect(formatDashboardNumber(0.125, "en-US", { style: "percent", maximumFractionDigits: 1 })).toBe("12.5%");
    expect(formatDashboardNumber(12.5, "en-US", { style: "percent", percentIsWhole: true })).toBe("13%");
    expect(formatDashboardNumber(12, "zh-CN", { style: "currency", currency: "CNY" })).toContain("12.00");
    expect(formatDashboardNumber(12_000, "en-US", { style: "compact" })).toBe("12K");
  });

  it("supports prefix/suffix and invalid-value fallback", () => {
    expect(formatDashboardNumber(42, "zh-CN", { prefix: "≈", suffix: " 件" })).toBe("≈42 件");
    expect(formatDashboardNumber(Number.NaN, "en-US")).toBe("—");
  });

  it("formats dates deterministically with an explicit timezone", () => {
    const instant = "2026-07-22T08:09:10Z";
    expect(formatDashboardDate(instant, "en-US", "date", "UTC")).toBe("07/22/2026");
    expect(formatDashboardDate(instant, "zh-CN", "date", "UTC")).toBe("2026/07/22");
    expect(formatDashboardDate("invalid", "en-US", "date", "UTC")).toBe("—");
  });
});
