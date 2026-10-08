import type { ColumnSchema } from "@/contracts";
import type { DisplaySpec } from "@/contracts/generated/schemaV2";
import { calendarDateKey, isCalendarDateValue } from "./calendarDateValue";
import { formatNumberDisplay } from "@/number/numberDisplay";

export type FieldDisplayInfo = Pick<ColumnSchema, "dataType" | "display" | "enumOptions">;
export interface EnumDisplayPart { readonly text: string; readonly color: string | null }

export function enumDisplayParts(value: unknown, options: ColumnSchema["enumOptions"]): EnumDisplayPart[] {
  const values = Array.isArray(value) ? value : [value];
  return values.map((id) => {
    const option = options?.find((candidate) => candidate.optionId === id);
    return { text: option ? `${option.label}${option.state === "retired" ? "（已停用）" : ""}` : String(id),
      color: option && /^(?:#[0-9a-f]{3,8}|[a-z]{1,30})$/iu.test(option.color) ? option.color : null };
  });
}

export function progressDisplay(value: unknown, display: DisplaySpec): { width: number; outside: boolean } | null {
  if (display.preset !== "progress" || display.percentStorage !== "ratio" || typeof value !== "number" || !Number.isFinite(value)) return null;
  const start = display.progressStart ?? 0, target = display.progressTarget ?? 1;
  if (!Number.isFinite(start) || !Number.isFinite(target) || start >= target || !Number.isFinite(target - start)) return null;
  const ratio = value <= start ? 0 : value >= target ? 1 : (value - start) / (target - start);
  if (!Number.isFinite(ratio)) return null;
  return { width: Math.max(0, Math.min(1, ratio)) * 100, outside: value < start || value > target };
}

/** Bounded plain text only; user values never become markup. */
export function textSummary(value: unknown, rich = false): string {
  let text = String(value);
  if (rich) text = text.replace(/<(script|style)\b[^>]*>[\s\S]*?<\/\1\s*>/giu, " ")
    .replace(/<[^>]*>/gu, " ").replace(/&nbsp;/gu, " ").replace(/&lt;/gu, "<")
    .replace(/&gt;/gu, ">").replace(/&amp;/gu, "&");
  text = text.replace(/\s+/gu, " ").trim();
  return text.length > 240 ? `${text.slice(0, 239)}…` : text;
}

export function temporalDisplay(value: unknown, type: string, display: DisplaySpec | null | undefined, locale: string): string | null {
  if (typeof value !== "string") return null;
  if (type === "date") return calendarDateKey(value, "date");
  const precision = display?.precision ?? "millisecond";
  if (type === "time") {
    const match = /^(\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{1,3}))?)?$/u.exec(value);
    if (!match || Number(match[1]) > 23 || Number(match[2]) > 59 || Number(match[3] ?? 0) > 59) return null;
    if (precision === "minute" || precision === "day") return value.slice(0, 5);
    const seconds = `${match[1]}:${match[2]}:${match[3] ?? "00"}`;
    return precision === "millisecond" || precision === "exact" ? `${seconds}.${(match[4] ?? "").padEnd(3, "0")}` : seconds;
  }
  if (!isCalendarDateValue(value, "datetime")) return null;
  const instant = new Date(value.replace(/^(\d{4}-\d{2}-\d{2}) (?=\d{2}:\d{2})/u, "$1T"));
  if (!Number.isFinite(instant.valueOf())) return null;
  const timezone = display?.timezone;
  try {
    const parts = Object.fromEntries(new Intl.DateTimeFormat(locale, {
      year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit",
      second: "2-digit", fractionalSecondDigits: 3, hourCycle: "h23",
      ...(timezone && timezone !== "system" ? { timeZone: timezone } : {}),
    }).formatToParts(instant).filter(({ type: key }) => key !== "literal").map(({ type: key, value: part }) => [key, part]));
    const date = `${parts.year}-${parts.month}-${parts.day}`;
    if (precision === "day") return date;
    const minute = `${date} ${parts.hour}:${parts.minute}`;
    if (precision === "minute") return minute;
    const second = `${minute}:${parts.second}`;
    return precision === "second" ? second : `${second}.${parts.fractionalSecond}`;
  } catch { return null; }
}

/** One display-only entry point for grid, cards, settings and typed computed values. */
export function formatFieldDisplay(value: unknown, field: FieldDisplayInfo, locale: string): string {
  if (value === null || value === undefined || value === "") return "—";
  const display = field.display;
  if (field.dataType === "decimal" || field.dataType === "integer") {
    return formatNumberDisplay(value, display, locale) ?? String(value);
  }
  if (field.dataType === "date" || field.dataType === "datetime" || field.dataType === "time") {
    return temporalDisplay(value, field.dataType, display, locale) ?? String(value);
  }
  if (display?.kind === "select" && Array.isArray(value) && !value.length) return "—";
  if (display?.kind === "select") return enumDisplayParts(value, field.enumOptions).map((part) => part.text).join("、");
  if (field.dataType === "boolean" && typeof value === "boolean") {
    const label = value ? display?.trueLabel : display?.falseLabel;
    if (display?.mode === "text") return label ?? (value ? "是" : "否");
    if (display?.mode === "switch") return `${value ? "●" : "○"} ${label ?? (value ? "是" : "否")}`;
    return value ? "✓" : "✕";
  }
  if (typeof value === "object") { try { return JSON.stringify(value); } catch { return String(value); } }
  return textSummary(value, display?.kind === "editor");
}
