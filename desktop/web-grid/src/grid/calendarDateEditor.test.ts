import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createCalendarDateEditor } from "./calendarDateEditor";
import { WORK_CALENDAR_STORAGE_KEY } from "@/stores/workCalendarStore";

describe("calendarDateEditor", () => {
  beforeEach(() => {
    localStorage.clear();
    document.querySelectorAll(".work-date-popup").forEach((node) => node.remove());
  });
  afterEach(() => document.querySelectorAll(".work-date-popup").forEach((node) => node.remove()));

  it("shows shared holiday markers and commits a selected date", () => {
    localStorage.setItem(WORK_CALENDAR_STORAGE_KEY, JSON.stringify([
      { date: "2026-07-20", kind: "holiday", name: "公司假日" },
    ]));
    const success = vi.fn();
    const input = createCalendarDateEditor("date")(
      { getValue: () => "2026-07-01" },
      (callback) => callback(),
      success,
      vi.fn(),
    );
    document.body.append(input);
    const day = document.querySelector<HTMLButtonElement>('[data-date="2026-07-20"]')!;
    expect(day.textContent).toContain("休");
    expect(day.title).toContain("公司假日");
    day.click();
    expect(success).toHaveBeenCalledWith("2026-07-20");
  });

  it("preserves datetime editing with an explicit confirmation", () => {
    const success = vi.fn();
    createCalendarDateEditor("datetime")(
      { getValue: () => "2026-07-01T14:30" },
      (callback) => callback(),
      success,
      vi.fn(),
    );
    document.querySelector<HTMLButtonElement>('[data-date="2026-07-20"]')!.click();
    const apply = [...document.querySelectorAll<HTMLButtonElement>(".work-date-popup__action")]
      .find((item) => item.textContent === "确定")!;
    apply.click();
    expect(success).toHaveBeenCalledWith("2026-07-20T14:30");
  });

  it("preserves a time-only value without injecting a date", () => {
    const success = vi.fn();
    const input = createCalendarDateEditor("time")(
      { getValue: () => "14:30:00" },
      (callback) => callback(),
      success,
      vi.fn(),
    ) as HTMLInputElement;

    expect(input.type).toBe("time");
    expect(input.value).toBe("14:30:00");
    input.value = "16:45:00";
    input.dispatchEvent(new Event("change"));
    expect(success).toHaveBeenCalledWith("16:45:00");
  });
});
