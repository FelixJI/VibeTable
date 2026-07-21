import {
  buildMonthDays,
  formatDateKey,
  monthLabel,
  parseDateKey,
  shiftMonthKey,
} from "@/calendar/workCalendar";
import { getLocale } from "@/i18n";
import { readStoredWorkCalendar } from "@/stores/workCalendarStore";

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

function valueDate(value: unknown): string {
  const match = /^(\d{4}-\d{2}-\d{2})/.exec(String(value ?? ""));
  return match && parseDateKey(match[1]) ? match[1] : formatDateKey(new Date());
}

function valueTime(value: unknown): string {
  return /T(\d{2}:\d{2})/.exec(String(value ?? ""))?.[1] ?? "09:00";
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
      for (const day of buildMonthDays(visibleMonth, readStoredWorkCalendar())) {
        const dayButton = button(String(day.day), `work-calendar__day work-calendar__day--${day.kind}`, () => {
          selectedDate = day.date;
          input.value = dateType === "datetime" ? `${selectedDate}T${selectedTime}` : selectedDate;
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
        if (day.marker && day.inCurrentMonth) {
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
          finish(`${selectedDate}T${selectedTime}`);
        }));
      }
      popup.append(footer);
    };

    input.addEventListener("keydown", (event) => {
      if (event.key === "Escape") finish();
      if (event.key === "Enter") {
        finish(dateType === "datetime" ? `${selectedDate}T${selectedTime}` : selectedDate);
      }
    });

    onRendered(() => {
      render();
      document.body.append(popup);
      const rect = input.getBoundingClientRect();
      const maxLeft = Math.max(8, window.innerWidth - 318);
      popup.style.left = `${Math.max(8, Math.min(rect.left, maxLeft))}px`;
      popup.style.top = `${Math.max(8, Math.min(rect.bottom + 4, window.innerHeight - 360))}px`;
      input.focus({ preventScroll: true });
      document.addEventListener("pointerdown", onDocumentPointerDown, true);
    });

    return input;
  };
}
