import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createCalendarDateEditor } from "./calendarDateEditor";
import { formatDateKey } from "@/calendar/workCalendar";


describe("calendarDateEditor", () => {
  beforeEach(() => {
    localStorage.clear();
    document.querySelectorAll(".work-date-popup").forEach((node) => node.remove());
  });
  afterEach(() => document.querySelectorAll(".work-date-popup").forEach((node) => node.remove()));

  it("shows shared holiday markers and commits a selected date", () => {

    const success = vi.fn();
    const input = createCalendarDateEditor("date", () => [
      { date: "2026-07-20", kind: "holiday", name: "公司假日" },
    ])(
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

  it("preserves optional datetime seconds and zone when only the date changes", () => {
    const success = vi.fn();
    createCalendarDateEditor("datetime")(
      { getValue: () => "2026-07-01T14:30:45+08:00" },
      (callback) => callback(),
      success,
      vi.fn(),
    );
    document.querySelector<HTMLButtonElement>('[data-date="2026-07-20"]')!.click();
    const apply = [...document.querySelectorAll<HTMLButtonElement>(".work-date-popup__action")]
      .find((item) => item.textContent === "确定")!;
    apply.click();
    expect(success).toHaveBeenCalledWith("2026-07-20T14:30:45+08:00");
  });

  it("preserves PocketBase datetime storage formatting when only the date changes", () => {
    const success = vi.fn();
    createCalendarDateEditor("datetime")(
      { getValue: () => "2026-07-01 14:30:45.123Z" },
      (callback) => callback(),
      success,
      vi.fn(),
    );
    document.querySelector<HTMLButtonElement>('[data-date="2026-07-20"]')!.click();
    const apply = [...document.querySelectorAll<HTMLButtonElement>(".work-date-popup__action")]
      .find((item) => item.textContent === "确定")!;
    apply.click();
    expect(success).toHaveBeenCalledWith("2026-07-20 14:30:45.123Z");
  });

  it("commits an authoritative UTC RFC3339 value for empty and re-timed datetimes", () => {
    // 空值时日历默认展示当前月，用今天作为确定性的目标日期。
    const today = formatDateKey(new Date());
    const success = vi.fn();
    createCalendarDateEditor("datetime")(
      { getValue: () => "" },
      (callback) => callback(),
      success,
      vi.fn(),
    );
    document.querySelector<HTMLButtonElement>(`[data-date="${today}"]`)!.click();
    const apply = [...document.querySelectorAll<HTMLButtonElement>(".work-date-popup__action")]
      .find((item) => item.textContent === "确定")!;
    apply.click();
    // 空值首次写入：默认时间按本地解释，提交含秒/时区的 UTC RFC3339，
    // 与必填新行表单及 sidecar normalizeDateTime 一致。
    expect(success).toHaveBeenCalledWith(new Date(`${today}T09:00`).toISOString());

    // 修改时间也走同一 UTC 语义（旧格式不再被无时区回写污染）。
    const retime = vi.fn();
    createCalendarDateEditor("datetime")(
      { getValue: () => "" },
      (callback) => callback(),
      retime,
      vi.fn(),
    );
    document.querySelector<HTMLButtonElement>(`[data-date="${today}"]`)!.click();
    const time = document.querySelector<HTMLInputElement>(".work-date-popup__time")!;
    time.value = "16:45";
    time.dispatchEvent(new Event("input", { bubbles: true }));
    [...document.querySelectorAll<HTMLButtonElement>(".work-date-popup__action")]
      .find((item) => item.textContent === "确定")!.click();
    expect(retime).toHaveBeenCalledWith(new Date(`${today}T16:45`).toISOString());
  });

  it("decodes stored UTC and non-zero-offset datetimes to local editor values", () => {
    const pad = (part: number) => String(part).padStart(2, "0");
    const localTimeOf = (value: string) => {
      const instant = new Date(value);
      return `${pad(instant.getHours())}:${pad(instant.getMinutes())}`;
    };
    const localDateOf = (value: string) => {
      const instant = new Date(value);
      return `${instant.getFullYear()}-${pad(instant.getMonth() + 1)}-${pad(instant.getDate())}`;
    };

    // UTC 存储串：编辑初值必须按本地展示（脚本上报的 05:45Z → 本地 13:45）。
    const utcStored = "2026-10-26T05:45:00.000Z";
    createCalendarDateEditor("datetime")(
      { getValue: () => utcStored },
      (callback) => callback(),
      vi.fn(),
      vi.fn(),
    );
    const time = document.querySelector<HTMLInputElement>(".work-date-popup__time")!;
    expect(time.value).toBe(localTimeOf(utcStored));
    const selectedDay = document.querySelector<HTMLButtonElement>(
      `.work-calendar__day--selected`,
    )!;
    expect(selectedDay.dataset.date).toBe(localDateOf(utcStored));

    // 非零 offset 存储：同一本地坐标系解码。
    document.querySelectorAll(".work-date-popup").forEach(node => node.remove());
    const offsetStored = "2026-07-01T14:30:00+08:00";
    createCalendarDateEditor("datetime")(
      { getValue: () => offsetStored },
      (callback) => callback(),
      vi.fn(),
      vi.fn(),
    );
    const offsetTime = document.querySelector<HTMLInputElement>(".work-date-popup__time")!;
    expect(offsetTime.value).toBe(localTimeOf(offsetStored));
  });

  it("keeps the selected local date across UTC midnight and preserves seconds on date-only edits", () => {
    const pad = (part: number) => String(part).padStart(2, "0");
    const key = (instant: Date) => `${instant.getFullYear()}-${pad(instant.getMonth() + 1)}-${pad(instant.getDate())}`;
    const stored = "2026-10-25T20:00:30.123Z";
    const local = new Date(stored);
    // 选同月的相邻一天：无论本机时区把存储值解码到哪一天，提交后解码回
    // 本地必须是用户点选的那一天（不能把选 27 变成 28）。
    const next = new Date(local.getTime() + 86_400_000);
    const target = key(next).slice(0, 7) === key(local).slice(0, 7) ? key(next)
      : key(new Date(local.getTime() - 86_400_000));

    const success = vi.fn();
    createCalendarDateEditor("datetime")(
      { getValue: () => stored },
      (callback) => callback(),
      success,
      vi.fn(),
    );
    document.querySelector<HTMLButtonElement>(`[data-date="${target}"]`)!.click();
    [...document.querySelectorAll<HTMLButtonElement>(".work-date-popup__action")]
      .find((item) => item.textContent === "确定")!.click();

    const committed = new Date(String(success.mock.calls[0]?.[0]));
    expect(key(committed)).toBe(target);
    // 仅改日期：秒与毫秒精度保留。
    expect(committed.getSeconds()).toBe(30);
    expect(committed.getMilliseconds()).toBe(123);
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
