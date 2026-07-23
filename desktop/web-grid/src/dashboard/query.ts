import type { DomainDiagnostic } from "./types";
import type { FilterExpression, FilterOperator } from "./filters";

export const FIELD_TYPES = [
  "text",
  "integer",
  "decimal",
  "boolean",
  "date",
  "date-time",
  "time",
  "enum",
  "uuid",
  "user",
  "relation",
  "json",
] as const;
export type FieldType = (typeof FIELD_TYPES)[number];
export type Aggregate = "count" | "countDistinct" | "sum" | "avg" | "min" | "max";
export type TimeGranularity = "minute" | "hour" | "day" | "week" | "month" | "quarter" | "year";

export interface QueryFieldSchema {
  readonly name: string;
  readonly type: FieldType;
  readonly readable?: boolean;
}

export interface QueryMetric {
  readonly field: string;
  readonly aggregate: Aggregate;
  readonly alias?: string;
}

export interface QuerySort {
  readonly field: string;
  readonly direction: "asc" | "desc";
}

export interface DashboardQueryAst {
  readonly collection: string;
  readonly dimensions?: readonly string[];
  readonly metrics?: readonly QueryMetric[];
  readonly filter?: FilterExpression | null;
  readonly groupBy?: readonly string[];
  readonly sorts?: readonly QuerySort[];
  readonly limit?: number;
  readonly timeField?: string;
  readonly timeGranularity?: TimeGranularity;
}

const NULL_OPERATORS: readonly FilterOperator[] = ["is_null", "is_not_null"];
const EQUALITY_OPERATORS: readonly FilterOperator[] = ["eq", "ne", "in", ...NULL_OPERATORS];
const ORDERED_OPERATORS: readonly FilterOperator[] = [
  "eq", "ne", "in", "gt", "gte", "lt", "lte", "between", ...NULL_OPERATORS,
];

const OPERATORS: Readonly<Record<FieldType, readonly FilterOperator[]>> = {
  text: ["eq", "ne", "in", "contains", "starts_with", "ends_with", ...NULL_OPERATORS],
  integer: ORDERED_OPERATORS,
  decimal: ORDERED_OPERATORS,
  boolean: ["eq", "ne", ...NULL_OPERATORS],
  date: ORDERED_OPERATORS,
  "date-time": ORDERED_OPERATORS,
  time: ORDERED_OPERATORS,
  enum: EQUALITY_OPERATORS,
  uuid: EQUALITY_OPERATORS,
  user: EQUALITY_OPERATORS,
  relation: EQUALITY_OPERATORS,
  json: ["contains", ...NULL_OPERATORS],
};

export function operatorsForField(type: FieldType): readonly FilterOperator[] {
  return OPERATORS[type];
}

export function validateDashboardQuery(
  query: DashboardQueryAst,
  fields: readonly QueryFieldSchema[],
): DomainDiagnostic[] {
  const diagnostics: DomainDiagnostic[] = [];
  if (!query.collection.trim()) diagnostics.push(error("query_collection_missing", "A collection is required.", "collection"));
  const schema = new Map(fields.map((field) => [field.name, field]));
  const checkField = (name: string, path: string): QueryFieldSchema | null => {
    const field = schema.get(name);
    if (!field) {
      diagnostics.push(error("query_field_missing", `Field ${name} does not exist.`, path));
      return null;
    }
    if (field.readable === false) {
      diagnostics.push(error("query_field_unreadable", `Field ${name} is not readable.`, path));
    }
    return field;
  };

  query.dimensions?.forEach((field, index) => checkField(field, `dimensions.${index}`));
  query.groupBy?.forEach((field, index) => checkField(field, `groupBy.${index}`));
  query.sorts?.forEach((sort, index) => checkField(sort.field, `sorts.${index}.field`));
  query.metrics?.forEach((metric, index) => {
    if (metric.field === "*" && metric.aggregate === "count") return;
    const field = checkField(metric.field, `metrics.${index}.field`);
    if (field && !aggregateAllowed(field.type, metric.aggregate)) {
      diagnostics.push(error(
        "query_aggregate_incompatible",
        `${metric.aggregate} cannot be applied to ${field.type}.`,
        `metrics.${index}.aggregate`,
      ));
    }
  });
  if (query.filter) validateFilter(query.filter, schema, diagnostics, "filter");
  if (query.timeField) {
    const field = checkField(query.timeField, "timeField");
    if (field && !["date", "date-time", "time"].includes(field.type)) {
      diagnostics.push(error("query_time_field_incompatible", `${query.timeField} is not temporal.`, "timeField"));
    }
    if (!query.timeGranularity) {
      diagnostics.push(error("query_time_granularity_missing", "A time granularity is required.", "timeGranularity"));
    }
  } else if (query.timeGranularity) {
    diagnostics.push(error("query_time_field_missing", "A time field is required for granularity.", "timeField"));
  }
  if (query.limit !== undefined && (!Number.isInteger(query.limit) || query.limit < 1 || query.limit > 100_000)) {
    diagnostics.push(error("query_limit_invalid", "Limit must be an integer from 1 to 100000.", "limit"));
  }
  return dedupeDiagnostics(diagnostics);
}

function validateFilter(
  filter: FilterExpression,
  fields: ReadonlyMap<string, QueryFieldSchema>,
  diagnostics: DomainDiagnostic[],
  path: string,
): void {
  if ("and" in filter) {
    filter.and.forEach((item, index) => validateFilter(item, fields, diagnostics, `${path}.and.${index}`));
    return;
  }
  const field = fields.get(filter.field);
  if (!field) {
    diagnostics.push(error("query_field_missing", `Field ${filter.field} does not exist.`, `${path}.field`));
    return;
  }
  if (field.readable === false) {
    diagnostics.push(error("query_field_unreadable", `Field ${filter.field} is not readable.`, `${path}.field`));
  }
  if (!operatorsForField(field.type).includes(filter.operator)) {
    diagnostics.push(error(
      "query_operator_incompatible",
      `${filter.operator} cannot be applied to ${field.type}.`,
      `${path}.operator`,
    ));
  }
  if (!NULL_OPERATORS.includes(filter.operator) && filter.value === undefined) {
    diagnostics.push(error("query_filter_value_missing", "This operator requires a value.", `${path}.value`));
  }
  if (filter.operator === "between" && (!Array.isArray(filter.value) || filter.value.length !== 2)) {
    diagnostics.push(error("query_between_invalid", "Between requires exactly two values.", `${path}.value`));
  }
  if (filter.operator === "in" && !Array.isArray(filter.value)) {
    diagnostics.push(error("query_in_invalid", "In requires an array value.", `${path}.value`));
  }
}

function aggregateAllowed(type: FieldType, aggregate: Aggregate): boolean {
  if (aggregate === "count" || aggregate === "countDistinct") return true;
  if (aggregate === "sum" || aggregate === "avg") return type === "integer" || type === "decimal";
  return type !== "json" && type !== "relation" && type !== "user";
}

function error(code: string, message: string, path: string): DomainDiagnostic {
  return { code, message, path, severity: "error" };
}

function dedupeDiagnostics(values: readonly DomainDiagnostic[]): DomainDiagnostic[] {
  const seen = new Set<string>();
  return values.filter((item) => {
    const key = `${item.code}:${item.path ?? ""}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}
