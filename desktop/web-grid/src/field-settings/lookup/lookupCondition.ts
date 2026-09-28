import type { FieldDraftV2 } from "@/contracts";

type LookupDefinition = NonNullable<FieldDraftV2["lookup"]>;
type LookupConditionRule = NonNullable<LookupDefinition["condition"]>["rules"][number];

export type LookupConditionOperator = LookupConditionRule["operator"];
export type LookupConditionOperandKind = NonNullable<LookupConditionRule["operand"]>["kind"];

/** `value` 是稳定 fieldId；编辑器从不按显示名解析。 */
export interface LookupConditionFieldOption {
  readonly label: string;
  readonly value: string;
  readonly logicalType: string;
  readonly filterOperators: readonly string[];
  readonly selectOptions?: readonly { readonly optionId: string; readonly label: string }[];
}

export const LOOKUP_CONDITION_MAX_RULES = 50;

/** 条件基础类型闭集；unsupported 类型不提供。 */
export const CONDITION_LOGICAL_TYPES = [
  "text", "number", "bool", "date", "dateTime", "select",
] as const;
export type ConditionLogicalType = (typeof CONDITION_LOGICAL_TYPES)[number];

export const CONDITION_OPERATOR_LABELS: Readonly<Record<LookupConditionOperator, string>> = {
  eq: "等于",
  ne: "不等于",
  gt: "大于",
  gte: "大于等于",
  lt: "小于",
  lte: "小于等于",
  contains: "包含",
  is_null: "为空",
  is_not_null: "不为空",
};

const TYPE_OPERATORS: Record<ConditionLogicalType, readonly LookupConditionOperator[]> = {
  text: ["eq", "ne", "contains", "is_null", "is_not_null"],
  number: ["eq", "ne", "gt", "gte", "lt", "lte", "is_null", "is_not_null"],
  bool: ["eq", "ne", "is_null", "is_not_null"],
  date: ["eq", "ne", "gt", "gte", "lt", "lte", "is_null", "is_not_null"],
  dateTime: ["eq", "ne", "gt", "gte", "lt", "lte", "is_null", "is_not_null"],
  select: ["eq", "ne", "is_null", "is_not_null"],
};

export function isConditionLogicalType(value: string): value is ConditionLogicalType {
  return (CONDITION_LOGICAL_TYPES as readonly string[]).includes(value);
}

/** `is_null`/`is_not_null` 无 operand。 */
export function isNullOperator(operator: string): boolean {
  return operator === "is_null" || operator === "is_not_null";
}

/** 运算符 = 类型闭集 ∩ 字段公开 filterOperators，顺序按闭集。 */
export function conditionOperators(
  logicalType: string,
  filterOperators: readonly string[],
): LookupConditionOperator[] {
  if (!isConditionLogicalType(logicalType)) return [];
  const published = new Set(filterOperators);
  return TYPE_OPERATORS[logicalType].filter(operator => published.has(operator));
}

const DATE_PATTERN = /^\d{4}-\d{2}-\d{2}$/;
const DATETIME_LOCAL_PATTERN = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d{1,3})?)?$/;

function isRealCalendarDate(year: number, month: number, day: number): boolean {
  if (month < 1 || month > 12 || day < 1) return false;
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  return day <= [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][month - 1]!;
}

/** date 常量必须是真实日历日（YYYY-MM-DD），不做宽松解析。 */
export function isValidDateValue(value: string): boolean {
  if (!DATE_PATTERN.test(value)) return false;
  const [year, month, day] = value.split("-").map(Number);
  return isRealCalendarDate(year!, month!, day!);
}

/** datetime-local 本地值（无 zone）需通过真实日历与时分秒范围。 */
export function isValidDateTimeLocalValue(value: string): boolean {
  if (!DATETIME_LOCAL_PATTERN.test(value)) return false;
  if (!isValidDateValue(value.slice(0, 10))) return false;
  const [hour, minute, second] = value.slice(11).split(":");
  return Number(hour!) < 24 && Number(minute!) < 60 && (second === undefined || Number(second) < 60);
}

/** datetime-local → RFC3339 UTC；Go/PB 布局只接受带 zone 的时间。 */
export function dateTimeLocalToRfc3339(value: string): string | null {
  if (!isValidDateTimeLocalValue(value)) return null;
  const instant = new Date(value);
  return Number.isNaN(instant.getTime()) ? null : instant.toISOString();
}

/** 已保存 RFC3339 → 本地 datetime-local 显示串，保持同一时点（秒非 0 时保留秒）。 */
export function rfc3339ToDateTimeLocal(value: string): string {
  const instant = new Date(value);
  if (Number.isNaN(instant.getTime())) return "";
  const pad = (part: number) => String(part).padStart(2, "0");
  const base = `${instant.getFullYear()}-${pad(instant.getMonth() + 1)}-${pad(instant.getDate())}`
    + `T${pad(instant.getHours())}:${pad(instant.getMinutes())}`;
  const seconds = instant.getSeconds();
  const milliseconds = instant.getMilliseconds();
  return seconds === 0 && milliseconds === 0 ? base
    : base + ":" + pad(seconds) + (milliseconds ? "." + String(milliseconds).padStart(3, "0") : "");
}
