import { parseDateKey } from "@/calendar/workCalendar";

const DATE_VALUE_PATTERN = /^(\d{4}-\d{2}-\d{2})(?: 00:00:00(?:\.\d+)?Z)?$/u;
const DATETIME_VALUE_PATTERN = /^(\d{4}-\d{2}-\d{2})[T ](\d{2}):(\d{2})(?::(\d{2})(?:\.\d+)?)?(?:(Z)|([+-])(\d{2}):(\d{2}))?$/u;

function calendarDateValueMatch(
  value: unknown,
  dateType: "date" | "datetime",
): RegExpExecArray | null {
  if (typeof value !== "string") return null;
  const match = (dateType === "date" ? DATE_VALUE_PATTERN : DATETIME_VALUE_PATTERN).exec(value);
  if (!match || !parseDateKey(match[1])) return null;
  if (dateType === "date") return match;
  const hour = Number(match[2]);
  const minute = Number(match[3]);
  const second = match[4] === undefined ? 0 : Number(match[4]);
  const offsetHour = match[7] === undefined ? 0 : Number(match[7]);
  const offsetMinute = match[8] === undefined ? 0 : Number(match[8]);
  return hour <= 23 && minute <= 59 && second <= 59 && offsetHour <= 23 && offsetMinute <= 59
    ? match
    : null;
}

export function isCalendarDateValue(
  value: unknown,
  dateType: "date" | "datetime",
): boolean {
  return calendarDateValueMatch(value, dateType) !== null;
}

export function calendarDateKey(
  value: unknown,
  dateType: "date" | "datetime",
): string | null {
  if (dateType === "date") return calendarDateValueMatch(value, "date")?.[1] ?? null;
  if (typeof value !== "string" && !(value instanceof Date)) return null;
  const parsed = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(parsed.valueOf())) return null;
  return `${parsed.getFullYear()}-${String(parsed.getMonth() + 1).padStart(2, "0")}-${String(parsed.getDate()).padStart(2, "0")}`;
}

export function replaceCalendarDateValue(
  value: unknown,
  targetDate: string,
  dateType: "date" | "datetime",
): string | null {
  if (!parseDateKey(targetDate)) return null;
  const match = calendarDateValueMatch(value, dateType);
  if (!match || typeof value !== "string") return null;
  if (dateType === "date") return targetDate;
  return `${targetDate}${value.slice(match[1].length)}`;
}
