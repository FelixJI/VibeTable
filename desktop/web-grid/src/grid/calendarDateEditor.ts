import {
  buildMonthDays,
  formatDateKey,
  monthLabel,
  parseDateKey,
  shiftMonthKey,
} from "@/calendar/workCalendar";
import { getLocale } from "@/i18n";
import type { WorkCalendarOverride } from "@/calendar/workCalendar";
import { replaceCalendarDateValue } from "./calendarDateValue";

interface DateEditorCell {
  getValue(): unknown;
}

type RenderedCallback = (callback: () => void) => void;
type SuccessCallback = (value: unknown) => boolean | void;
type CancelCallback = () => void;

export type CalendarDateEditor = (
  cell: DateEditorCell,
  onRendered: RenderedCallback,
  success: SuccessCallback,
  cancel: CancelCallback,
) => HTMLElement;

/** 带时间成分的存储值（date[ T]HH:mm…）；纯日期/纯时间不匹配。 */
const DATETIME_PREFIX = /^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}/u;

/**
 * 把带时间的存储值解码为本地日历日 + 本地 HH:mm。
 * 提交端（selectedValue）按本地时间解释并写入权威 UTC；编辑初值必须
 * 用同一坐标系解码，否则 UTC 存储串被直接截取会把 05:45Z 当作本地
 * 05:45 展示，回读时问午/日期错位。无时区的朴素串按本地解释，
 * 纯日期值不匹配（不破 date-only 语义），解析失败返回 null。
 */
function localDateTimeParts(value: unknown): { date: string; time: string; fullTime: string } | null {
  const raw = String(value ?? "");
  if (!DATETIME_PREFIX.test(raw)) return null;
  const instant = new Date(raw.replace(/^(\d{4}-\d{2}-\d{2}) (?=\d{2}:\d{2})/u, "$1T"));
  if (Number.isNaN(instant.getTime())) return null;
  const pad = (part: number) => String(part).padStart(2, "0");
  return {
    date: `${instant.getFullYear()}-${pad(instant.getMonth() + 1)}-${pad(instant.getDate())}`,
    time: `${pad(instant.getHours())}:${pad(instant.getMinutes())}`,
    fullTime: `${pad(instant.getHours())}:${pad(instant.getMinutes())}`
      + `:${pad(instant.getSeconds())}.${String(instant.getMilliseconds()).padStart(3, "0")}`,
  };
}

function valueDate(value: unknown): string {
  // datetime 存储（含时区/朴素）按本地日历日展示；date-only 走原逻辑。
  const local = localDateTimeParts(value);
  if (local) return local.date;
  const match = /^(\d{4}-\d{2}-\d{2})/.exec(String(value ?? ""));
  return match && parseDateKey(match[1]) ? match[1] : formatDateKey(new Date());
}

function valueTime(value: unknown): string {
  return localDateTimeParts(value)?.time ?? "09:00";
}

function button(label: string, className: string, onClick: () => void): HTMLButtonElement {
  const element = document.createElement("button");
  element.type = "button";
  element.className = className;
  element.textContent = label;
  element.addEventListener("click", onClick);
  return element;
}

function createTimeEditor(): CalendarDateEditor {
  return (cell, onRendered, success, cancel) => {
    const input = document.createElement("input");
    input.type = "time";
    input.step = "1";
    input.className = "work-date-input";
    input.setAttribute("aria-label", getLocale() === "zh-CN" ? "选择时间" : "Choose time");
    const match = /^(\d{2}:\d{2}(?::\d{2})?)/.exec(String(cell.getValue() ?? ""));
    input.value = match?.[1] ?? "";
    let finished = false;
    const finish = (value?: string): void => {
      if (finished) return;
      finished = true;
      if (value === undefined) cancel();
      else success(value);
    };
    input.addEventListener("change", () => finish(input.value));
    input.addEventListener("keydown", (event) => {
      if (event.key === "Escape") finish();
      if (event.key === "Enter") finish(input.value);
    });
    onRendered(() => input.focus({ preventScroll: true }));
    return input;
  };
}

export function createCalendarDateEditor(
  dateType: "date" | "datetime" | "time",
  readOverrides: () => readonly WorkCalendarOverride[] | null = () => [],
): CalendarDateEditor {
  if (dateType === "time") return createTimeEditor();
  return (cell, onRendered, success, cancel) => {
    const locale = getLocale();
    const input = document.createElement("input");
    input.type = "text";
    input.readOnly = true;
    input.className = "work-date-input";
    input.setAttribute("aria-label", locale === "zh-CN" ? "选择日期" : "Choose date");
    input.value = String(cell.getValue() ?? "");

    let selectedDate = valueDate(cell.getValue());
    let selectedTime = valueTime(cell.getValue());
    let visibleMonth = selectedDate.slice(0, 7);
    let finished = false;
    const popup = document.createElement("div");
    popup.className = "work-date-popup";
    popup.setAttribute("role", "dialog");
    popup.setAttribute("aria-label", locale === "zh-CN" ? "工作日历日期选择器" : "Work calendar date picker");
    popup.addEventListener("pointerdown", (event) => event.preventDefault());

    const finish = (value?: string): void => {
      if (finished) return;
      finished = true;
      document.removeEventListener("pointerdown", onDocumentPointerDown, true);
      popup.remove();
      if (value === undefined) cancel();
      else success(value);
    };

    const onDocumentPointerDown = (event: PointerEvent): void => {
      const target = event.target as Node | null;
      if (target && (popup.contains(target) || input.contains(target))) return;
      finish();
    };

    const selectedValue = (): string => {
      if (dateType === "date") return selectedDate;
      const original = localDateTimeParts(cell.getValue());
      // 仅改日期：保留原值的秒与毫秒精度（时间未被动过）。
      const timeUntouched = !!original && original.time === selectedTime;
      const fullLocalTime = timeUntouched ? original!.fullTime : selectedTime;
      const preserved = replaceCalendarDateValue(cell.getValue(), selectedDate, "datetime");
      // 字符串日期替换保持原格式，但只有当替换结果解码回本地后仍是用户
      // 所选的本地日期与时间（无跨午夜偏移）时才安全：否则 UTC 存储串的
      // 日期替换会把“选 27”提交成“本地 28”。
      if (preserved && valueDate(preserved) === selectedDate
        && valueTime(preserved) === selectedTime) {
        return preserved;
      }
      // 跨午夜或时间已改：按本地所选日期 + 完整本地时间转权威 UTC RFC3339。
      const instant = new Date(`${selectedDate}T${fullLocalTime}`);
      return Number.isNaN(instant.getTime())
        ? `${selectedDate}T${fullLocalTime}`
        : instant.toISOString();
    };

    const render = (): void => {
      popup.replaceChildren();
      const header = document.createElement("div");
      header.className = "work-date-popup__header";
      header.append(
        button("‹", "work-date-popup__nav", () => { visibleMonth = shiftMonthKey(visibleMonth, -1); render(); }),
      );
      const title = document.createElement("strong");
      title.textContent = monthLabel(visibleMonth, locale);
      header.append(title);
      header.append(
        button("›", "work-date-popup__nav", () => { visibleMonth = shiftMonthKey(visibleMonth, 1); render(); }),
      );
      popup.append(header);

      const calendar = document.createElement("div");
      calendar.className = "work-calendar";
      const week = document.createElement("div");
      week.className = "work-calendar__week";
      const labels = locale === "zh-CN"
        ? ["日", "一", "二", "三", "四", "五", "六"]
        : ["S", "M", "T", "W", "T", "F", "S"];
      for (const label of labels) {
        const span = document.createElement("span");
        span.textContent = label;
        week.append(span);
      }
      calendar.append(week);

      const days = document.createElement("div");
      days.className = "work-calendar__days";
      const calendarRules = readOverrides();
      if (calendarRules === null) {
        const warning = document.createElement("p");
        warning.textContent = locale === "zh-CN" ? "共享工作日历暂不可用" : "Shared work calendar unavailable";
        warning.setAttribute("role", "status");
        calendar.append(warning);
      }
      for (const day of buildMonthDays(visibleMonth, calendarRules ?? [])) {
        const dayButton = button(String(day.day), `work-calendar__day${calendarRules === null ? "" : ` work-calendar__day--${day.kind}`}`, () => {
          selectedDate = day.date;
          input.value = selectedValue();
          if (dateType === "date") finish(selectedDate);
          else render();
        });
        dayButton.dataset.date = day.date;
        dayButton.disabled = !day.inCurrentMonth;
        dayButton.title = day.name ? `${day.date} · ${day.name}` : day.date;
        if (!day.inCurrentMonth) dayButton.classList.add("work-calendar__day--other");
        if (day.isToday) dayButton.classList.add("work-calendar__day--today");
        if (day.date === selectedDate) dayButton.classList.add("work-calendar__day--selected");
        dayButton.replaceChildren();
        const number = document.createElement("span");
        number.className = "work-calendar__number";
        number.textContent = String(day.day);
        dayButton.append(number);
        if (calendarRules !== null && day.marker && day.inCurrentMonth) {
          const marker = document.createElement("span");
          marker.className = "work-calendar__marker";
          marker.textContent = day.marker;
          dayButton.append(marker);
        }
        days.append(dayButton);
      }
      calendar.append(days);
      popup.append(calendar);

      const footer = document.createElement("div");
      footer.className = "work-date-popup__footer";
      footer.append(button(locale === "zh-CN" ? "清除" : "Clear", "work-date-popup__action", () => finish("")));
      if (dateType === "datetime") {
        const time = document.createElement("input");
        time.type = "time";
        time.className = "work-date-popup__time";
        time.value = selectedTime;
        time.addEventListener("input", () => { selectedTime = time.value || "09:00"; });
        footer.append(time);
        footer.append(button(locale === "zh-CN" ? "确定" : "Apply", "work-date-popup__action", () => {
          finish(selectedValue());
        }));
      }
      popup.append(footer);
    };

    input.addEventListener("keydown", (event) => {
      if (event.key === "Escape") finish();
      if (event.key === "Enter") {
        finish(selectedValue());
      }
    });

    onRendered(() => {
      render();
      // 先定位再挂载，保证弹层首次出现即在目标位置（同步回调内完成）。
      const rect = input.getBoundingClientRect();
      const maxLeft = Math.max(8, window.innerWidth - 318);
      popup.style.left = `${Math.max(8, Math.min(rect.left, maxLeft))}px`;
      popup.style.top = `${Math.max(8, Math.min(rect.bottom + 4, window.innerHeight - 360))}px`;
      document.body.append(popup);
      input.focus({ preventScroll: true });
      document.addEventListener("pointerdown", onDocumentPointerDown, true);
    });

    return input;
  };
}
