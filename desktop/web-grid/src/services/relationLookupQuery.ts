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
 * groups. Unknown/malformed nodes are dropped; the backend still validates all
 * accepted nodes.
 */
export function buildAuthoritativeLookupViewQuery(
  normalized: Readonly<Record<string, unknown>>,
  fieldRefByName: ReadonlyMap<string, string>,
): {
  filters: FilterExpression[];
  sorts: SortCondition[];
  groups: LookupGroup[];
} {
  const filters = array(normalized.filters).flatMap((item) => {
    const mapped = mapFilterExpression(item, fieldRefByName);
    return mapped ? [mapped] : [];
  });
  const sorts = array(normalized.sorts).flatMap((item) => {
    if (!record(item) || typeof item.field !== "string") return [];
    return [{ ...item, field: fieldRefByName.get(item.field) ?? item.field }] as unknown as SortCondition[];
  });
  const rawGroups = array(normalized.groups).length ? array(normalized.groups) : array(normalized.groupBy);
  const groups = rawGroups.flatMap((item) => {
    if (typeof item === "string") return [{ fieldRef: fieldRefByName.get(item) ?? item, direction: "asc" as const }];
    if (!record(item)) return [];
    const field = typeof item.fieldRef === "string" ? item.fieldRef : typeof item.field === "string" ? item.field : null;
    if (!field) return [];
    return [{
      fieldRef: fieldRefByName.get(field) ?? field,
      direction: item.direction === "desc" ? "desc" as const : "asc" as const,
    }];
  });
  return { filters, sorts, groups };
}

function mapFilterExpression(
  value: unknown,
  fieldRefByName: ReadonlyMap<string, string>,
): FilterExpression | null {
  if (!record(value)) return null;
  if (Array.isArray(value.filters)) {
    const filters = value.filters.flatMap((child) => {
      const mapped = mapFilterExpression(child, fieldRefByName);
      return mapped ? [mapped] : [];
    });
    if (filters.length === 0) return null;
    return {
      groupLogic: value.groupLogic === "OR" ? "OR" : "AND",
      ...(value.logic === "OR" ? { logic: "OR" as const } : {}),
      filters,
    };
  }
  if (typeof value.field !== "string" || typeof value.operator !== "string") return null;
  return {
    ...value,
    field: fieldRefByName.get(value.field) ?? value.field,
  } as unknown as FilterCondition;
}

function array(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
