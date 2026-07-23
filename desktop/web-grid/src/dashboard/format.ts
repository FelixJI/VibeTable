export type DashboardLocale = "zh-CN" | "en-US";
export type NumberDisplayStyle = "number" | "percent" | "currency" | "compact";

export interface NumberFormatSpec {
  readonly style?: NumberDisplayStyle;
  readonly currency?: string;
  readonly minimumFractionDigits?: number;
  readonly maximumFractionDigits?: number;
  readonly prefix?: string;
  readonly suffix?: string;
  /** Percentage values use a 0..1 ratio unless this is true. */
  readonly percentIsWhole?: boolean;
}

export function formatDashboardNumber(
  value: number,
  locale: DashboardLocale,
  spec: NumberFormatSpec = {},
): string {
  if (!Number.isFinite(value)) return "—";
  const style = spec.style ?? "number";
  const numericValue = style === "percent" && spec.percentIsWhole ? value / 100 : value;
  const common: Intl.NumberFormatOptions = {
    minimumFractionDigits: spec.minimumFractionDigits,
    maximumFractionDigits: spec.maximumFractionDigits,
  };
  let options: Intl.NumberFormatOptions;
  if (style === "currency") {
    options = { ...common, style: "currency", currency: spec.currency ?? "CNY" };
  } else if (style === "percent") {
    options = { ...common, style: "percent" };
  } else if (style === "compact") {
    options = { ...common, notation: "compact", compactDisplay: "short" };
  } else {
    options = common;
  }
  const formatted = new Intl.NumberFormat(locale, options).format(numericValue);
  return `${spec.prefix ?? ""}${formatted}${spec.suffix ?? ""}`;
}

export type DateDisplayStyle = "date" | "time" | "date-time";

export function formatDashboardDate(
  value: string | number | Date,
  locale: DashboardLocale,
  style: DateDisplayStyle = "date-time",
  timeZone?: string,
): string {
  const date = value instanceof Date ? new Date(value.getTime()) : new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  const dateParts: Intl.DateTimeFormatOptions = {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  };
  const timeParts: Intl.DateTimeFormatOptions = {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  };
  const options = style === "date"
    ? dateParts
    : style === "time" ? timeParts : { ...dateParts, ...timeParts };
  return new Intl.DateTimeFormat(locale, { ...options, timeZone }).format(date);
}
