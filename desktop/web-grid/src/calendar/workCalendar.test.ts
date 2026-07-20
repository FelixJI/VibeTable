import { describe, expect, it } from "vitest";
import {
  buildMonthDays,
  formatDateKey,
  parseDateKey,
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
});
