import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import {
  buildMonthDays,
  formatDateKey,
  parseDateKey,
  parseFlexibleMonthKey,
  resolveWorkCalendarDay,
  sanitizeOverrides,
  shiftMonthKey,
} from "./workCalendar";

describe("workCalendar", () => {
  it("uses local date keys and rejects impossible dates", () => {
    expect(formatDateKey(new Date(2026, 6, 20))).toBe("2026-07-20");
    expect(parseDateKey("2026-02-29")).toBeNull();
    expect(parseDateKey("2024-02-29")).not.toBeNull();
  });

  it("defaults weekends to rest and lets manual overrides win", () => {
    expect(resolveWorkCalendarDay("2026-07-18", []).kind).toBe("weekend");
    expect(resolveWorkCalendarDay("2026-07-18", [
      { date: "2026-07-18", kind: "workday", name: "调休上班" },
    ])).toMatchObject({ kind: "workday", marker: "班", isWorkingDay: true });
    expect(resolveWorkCalendarDay("2026-07-20", [
      { date: "2026-07-20", kind: "holiday", name: "公司假日" },
    ])).toMatchObject({ kind: "holiday", marker: "休", isWorkingDay: false });
  });

  it("builds a stable six-week grid and shifts across years", () => {
    const days = buildMonthDays("2026-07", [], new Date(2026, 6, 20));
    expect(days).toHaveLength(42);
    expect(days.find((day) => day.date === "2026-07-20")?.isToday).toBe(true);
    expect(shiftMonthKey("2026-12", 1)).toBe("2027-01");
  });

  it("sanitizes, truncates and de-duplicates stored overrides", () => {
    expect(sanitizeOverrides([
      { date: "bad", kind: "holiday", name: "x" },
      { date: "2026-07-20", kind: "holiday", name: "  纪念日  " },
      { date: "2026-07-20", kind: "workday", name: "补班" },
    ])).toEqual([{ date: "2026-07-20", kind: "workday", name: "补班" }]);
  });

  it("parses flexible year-month and full-date inputs into month keys", () => {
    // 年月：分隔符 [-./\s] 任一 + 1-2 位月
    expect(parseFlexibleMonthKey("2026-7")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026-07")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026.7")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026.07")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026/7")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026 7")).toBe("2026-07");
    expect(parseFlexibleMonthKey("20267")).toBe("2026-07");   // 无分隔，4 位年 + 1 位月

    // 完整日期：忽略日，跳到对应月
    expect(parseFlexibleMonthKey("2026-7-15")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026-07-05")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026.07.05")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026/7/15")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026 7 15")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026-7.15")).toBe("2026-07");  // 分隔符可混用
    expect(parseFlexibleMonthKey("20260715")).toBe("2026-07");  // 8 位无分隔
  });

  it("rejects out-of-range or malformed flexible month inputs", () => {
    expect(parseFlexibleMonthKey("2026-2-30")).toBeNull();   // 日不合法（2 月无 30 日）
    expect(parseFlexibleMonthKey("2026-13")).toBeNull();     // 月超界（不能被误拆成 2026-1-3）
    expect(parseFlexibleMonthKey("2026-0")).toBeNull();      // 月为 0
    expect(parseFlexibleMonthKey("1899-7")).toBeNull();      // 年份低于 1900 下限
    expect(parseFlexibleMonthKey("2026")).toBeNull();        // 只有年
    expect(parseFlexibleMonthKey("hello")).toBeNull();
    expect(parseFlexibleMonthKey("")).toBeNull();
    expect(parseFlexibleMonthKey("  2026-7  ")).toBe("2026-07");  // 前后空格容错
  });

  it("uses the product primary color for selected dates instead of a purple override", () => {
    const css = readFileSync(resolve(import.meta.dirname, "../components/calendar/work-calendar.css"), "utf8");
    expect(css).toContain("--work-calendar-selected: var(--vt-color-primary-500)");
    expect(css).not.toContain("#7a5af8");
  });
});
