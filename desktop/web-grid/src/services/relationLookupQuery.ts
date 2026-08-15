import type {
  FilterCondition,
  FilterExpression,
  LookupDefinition,
  LookupGroup,
  SortCondition,
} from "@/contracts";

/**
 * `lookup.query.fieldRefs` is a projection of Lookup outputs only. Regular
 * table columns still participate in the query AST, but sending them as
 * projection fields makes the backend reject the entire authoritative query.
 */
export function buildLookupProjectionFieldRefs(
  definitions: readonly Pick<LookupDefinition, "fieldKey">[],
): string[] {
  return [...new Set(definitions.map((definition) => definition.fieldKey))];
}

/**
 * Preserve the authoritative table view AST when switching to Lookup query.
 * Field names are translated to stable fieldRefs, including remote sorts and
 * groups. The entire projection is rejected when any node is malformed so the
 * backend never receives a weaker query than the visible table state.
 */
export function buildAuthoritativeLookupViewQuery(
  value: unknown,
  fieldRefByName: ReadonlyMap<string, string>,
): {
  filters: FilterExpression[];
  sorts: SortCondition[];
  groups: LookupGroup[];
} {
  if (!record(value)) invalid("");
  if (hasOwn(value, "groupBy")) invalid("groupBy");
  const filters = queryArray(value, "filters").map((item, index) =>
    mapFilterExpression(item, fieldRefByName, `filters[${index}]`));
  const sorts = queryArray(value, "sorts").map((item, index) => {
    const path = `sorts[${index}]`;
    if (!record(item)) invalid(path);
    const field = queryString(item.field, `${path}.field`);
    const direction = optionalDirection(item.direction, `${path}.direction`);
    if (item.nullsLast !== undefined && typeof item.nullsLast !== "boolean") {
      invalid(`${path}.nullsLast`);
    }
    return {
      ...item,
      field: fieldRefByName.get(field) ?? field,
      ...(direction === undefined ? {} : { direction }),
    } as SortCondition;
  });
  const groups = queryArray(value, "groups").map((item, index) => {
    const path = `groups[${index}]`;
    if (!record(item)) invalid(path);
    const field = queryString(item.field, `${path}.field`);
    const direction = optionalDirection(item.direction, `${path}.direction`);
    return {
      fieldRef: fieldRefByName.get(field) ?? field,
      ...(direction === undefined ? {} : { direction }),
    } as LookupGroup;
  });
  return { filters, sorts, groups };
}

function mapFilterExpression(
  value: unknown,
  fieldRefByName: ReadonlyMap<string, string>,
  path: string,
): FilterExpression {
  if (!record(value)) invalid(path);
  if (Array.isArray(value.filters)) {
    if (value.filters.length === 0) invalid(`${path}.filters`);
    const groupLogic = optionalLogic(value.groupLogic, `${path}.groupLogic`);
    const logic = optionalLogic(value.logic, `${path}.logic`);
    const filters = value.filters.map((child, index) =>
      mapFilterExpression(child, fieldRefByName, `${path}.filters[${index}]`));
    return {
      ...(groupLogic === undefined ? {} : { groupLogic }),
      ...(logic === undefined ? {} : { logic }),
      filters,
    };
  }
  if (hasOwn(value, "filters")) invalid(`${path}.filters`);
  const field = queryString(value.field, `${path}.field`);
  if (typeof value.operator !== "string" || !FILTER_OPERATORS.has(value.operator)) {
    invalid(`${path}.operator`);
  }
  const logic = optionalLogic(value.logic, `${path}.logic`);
  return {
    ...value,
    field: fieldRefByName.get(field) ?? field,
    ...(logic === undefined ? {} : { logic }),
  } as unknown as FilterCondition;
}

const FILTER_OPERATORS = new Set([
  "contains", "eq", "ne", "starts_with", "ends_with", "gt", "lt", "gte", "lte",
  "between", "in", "is_null", "is_not_null", "regex",
]);

function queryArray(source: Readonly<Record<string, unknown>>, key: string): unknown[] {
  const value = source[key];
  if (value === undefined) return [];
  if (!Array.isArray(value)) invalid(key);
  return value;
}

function queryString(value: unknown, path: string): string {
  if (typeof value !== "string") invalid(path);
  return value;
}

function optionalDirection(value: unknown, path: string): "asc" | "desc" | undefined {
  if (value !== undefined && value !== "asc" && value !== "desc") invalid(path);
  return value;
}

function optionalLogic(value: unknown, path: string): "AND" | "OR" | undefined {
  if (value !== undefined && value !== "AND" && value !== "OR") invalid(path);
  return value;
}

function hasOwn(source: Readonly<Record<string, unknown>>, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(source, key);
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function invalid(path: string): never {
  throw new TypeError(`Invalid Lookup view query${path ? `.${path}` : ""}`);
}
