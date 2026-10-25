import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { mount } from "@vue/test-utils";
import WorkCalendarMonth from "./WorkCalendarMonth.vue";
import { formatDateKey, formatMonthKey } from "@/calendar/workCalendar";

describe("WorkCalendarMonth", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
  });

  it("keeps the compact today marker identical to the settings calendar", () => {
    // 首页迷你日历的“今日”必须与设置页工作日历使用同一视觉语言：同一
    // inset 主色描边，而不是独立的“实心主色块 + 白字”样式。
    const css = readFileSync(resolve(import.meta.dirname, "work-calendar.css"), "utf8");
    const compactToday = /\.work-calendar--compact \.work-calendar__day--today\s*\{[^}]*\}/
      .exec(css)?.[0];
    expect(compactToday, "compact today rule should exist").toBeTruthy();
    expect(compactToday!).toContain("box-shadow: inset 0 0 0 1px var(--vt-color-primary-500)");
    expect(compactToday!).not.toContain("color: #fff");
    expect(compactToday!).not.toMatch(/background:\s*var\(--vt-color-primary-500\)/);
  });

  it("renders today and selected day states from the month grid", () => {
    const today = formatDateKey(new Date());
    const wrapper = mount(WorkCalendarMonth, {
      props: {
        monthKey: formatMonthKey(new Date()),
        overrides: [{ date: today, kind: "workday", name: "调休上班" }],
        locale: "zh-CN",
        selectedDate: today,
        interactive: true,
      },
      global: { stubs: { transition: false } },
    });
    const todayCell = wrapper.get(`[data-date="${today}"]`);
    expect(todayCell.classes()).toContain("work-calendar__day--today");
    expect(todayCell.classes()).toContain("work-calendar__day--selected");
    expect(todayCell.attributes("aria-selected")).toBe("true");
    expect(todayCell.text()).toContain("班");
  });

  it("marks non-interactive days as disabled but still renders them", () => {
    const wrapper = mount(WorkCalendarMonth, {
      props: { monthKey: "2026-07", overrides: [], locale: "zh-CN", compact: true },
    });
    const cells = wrapper.findAll(".work-calendar__day");
    expect(cells).toHaveLength(42);
    const other = cells.find((cell) => cell.classes().includes("work-calendar__day--other"));
    expect(other?.attributes("disabled")).toBeDefined();
  });
});
