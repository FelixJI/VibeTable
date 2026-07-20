export type WorkCalendarOverrideKind = "holiday" | "workday";

export interface WorkCalendarOverride {
  readonly date: string;
  readonly kind: WorkCalendarOverrideKind;
  readonly name: string;
}

export type WorkCalendarDayKind = "weekday" | "weekend" | WorkCalendarOverrideKind;

export interface WorkCalendarDay {
  readonly date: string;
  readonly day: number;
  readonly inCurrentMonth: boolean;
  readonly isToday: boolean;
  readonly kind: WorkCalendarDayKind;
  readonly isWorkingDay: boolean;
  readonly marker: "" | "休" | "班";
  readonly name: string;
  readonly overridden: boolean;
}

const DATE_KEY = /^(\d{4})-(\d{2})-(\d{2})$/;
const MONTH_KEY = /^(\d{4})-(\d{2})$/;

function pad(value: number): string {
  return String(value).padStart(2, "0");
}

export function formatDateKey(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

export function formatMonthKey(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}`;
}

export function parseDateKey(value: string): Date | null {
  const match = DATE_KEY.exec(value);
  if (!match) return null;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const parsed = new Date(year, month - 1, day);
  if (
    parsed.getFullYear() !== year
    || parsed.getMonth() !== month - 1
    || parsed.getDate() !== day
  ) return null;
  return parsed;
}

export function parseMonthKey(value: string): Date | null {
  const match = MONTH_KEY.exec(value);
  if (!match) return null;
  const year = Number(match[1]);
  const month = Number(match[2]);
  if (month < 1 || month > 12) return null;
  return new Date(year, month - 1, 1);
}

export function shiftMonthKey(monthKey: string, amount: number): string {
  const month = parseMonthKey(monthKey) ?? new Date();
  return formatMonthKey(new Date(month.getFullYear(), month.getMonth() + amount, 1));
}

export function sanitizeOverrides(value: unknown): WorkCalendarOverride[] {
  if (!Array.isArray(value)) return [];
  const byDate = new Map<string, WorkCalendarOverride>();
  for (const item of value) {
    if (!item || typeof item !== "object") continue;
    const candidate = item as Record<string, unknown>;
    if (typeof candidate.date !== "string" || !parseDateKey(candidate.date)) continue;
    if (candidate.kind !== "holiday" && candidate.kind !== "workday") continue;
    const name = typeof candidate.name === "string" ? candidate.name.trim().slice(0, 40) : "";
    byDate.set(candidate.date, { date: candidate.date, kind: candidate.kind, name });
  }
  return [...byDate.values()].sort((a, b) => a.date.localeCompare(b.date));
}

export function resolveWorkCalendarDay(
  dateKey: string,
  overrides: readonly WorkCalendarOverride[],
  today = new Date(),
  inCurrentMonth = true,
): WorkCalendarDay {
  const date = parseDateKey(dateKey);
  if (!date) throw new Error(`Invalid date key: ${dateKey}`);
  const override = overrides.find((item) => item.date === dateKey);
  const weekend = date.getDay() === 0 || date.getDay() === 6;
  const kind: WorkCalendarDayKind = override?.kind ?? (weekend ? "weekend" : "weekday");
  const isWorkingDay = kind === "weekday" || kind === "workday";
  return {
    date: dateKey,
    day: date.getDate(),
    inCurrentMonth,
    isToday: dateKey === formatDateKey(today),
    kind,
    isWorkingDay,
    marker: kind === "workday" ? "班" : isWorkingDay ? "" : "休",
    name: override?.name ?? "",
    overridden: Boolean(override),
  };
}

export function buildMonthDays(
  monthKey: string,
  overrides: readonly WorkCalendarOverride[],
  today = new Date(),
): WorkCalendarDay[] {
  const month = parseMonthKey(monthKey) ?? new Date(today.getFullYear(), today.getMonth(), 1);
  const gridStart = new Date(month.getFullYear(), month.getMonth(), 1 - month.getDay());
  return Array.from({ length: 42 }, (_, index) => {
    const date = new Date(gridStart.getFullYear(), gridStart.getMonth(), gridStart.getDate() + index);
    return resolveWorkCalendarDay(
      formatDateKey(date),
      overrides,
      today,
      date.getFullYear() === month.getFullYear() && date.getMonth() === month.getMonth(),
    );
  });
}

export function monthLabel(monthKey: string, locale: string): string {
  const month = parseMonthKey(monthKey) ?? new Date();
  return new Intl.DateTimeFormat(locale, { year: "numeric", month: "long" }).format(month);
}
