/**
 * B3 Task 5: map Tabulator sorts/filters/search to the typed `TableQuery` AST.
 *
 * The adapter is pure: it takes Tabulator's current sort/filter/header-filter
 * state plus a keyword and produces a `TableQuery` the host forwards to the
 * backend. It NEVER sends formatter text as stored values — only the raw field
 * values the backend can compile to parameterized SQL.
 *
 * The backend re-validates every column/operator; the adapter only constructs
 * the AST from Tabulator's known state. Stable ordering is enforced by the
 * backend (the row key is always appended as the final sort).
 */

import type {
  ColumnSchema,
  FilterCondition,
  FilterOperator,
  SortCondition,
  TableQuery,
} from "@/contracts";
import { ungroupedFilterConditions } from "./viewQuery";

/**
 * A Tabulator sort descriptor as exposed by `table.getSorters()`.
 * Each entry has a `field` and a `dir` of `"asc"` or `"desc"`.
 */
export interface TabulatorSorter {
  readonly field: string;
  readonly dir: "asc" | "desc";
}

/**
 * A Tabulator header filter value as exposed by `table.getHeaderFilters()`.
 * Each entry has a `field` and a `value` (string by default).
 */
export interface TabulatorHeaderFilter {
  readonly field: string;
  readonly value: unknown;
}

export interface QueryAdapterOptions {
  /** The current keyword search text (empty/whitespace treated as absent). */
  readonly keyword?: string | null;
  /** Tabulator's active sorters (highest priority first). */
  readonly sorters?: readonly TabulatorSorter[];
  /** Tabulator's header filters. */
  readonly headerFilters?: readonly TabulatorHeaderFilter[];
  /** Current schema, used to select the product-valid operator per field. */
  readonly columns?: readonly ColumnSchema[];
  /** Page offset (0-based). */
  readonly offset?: number;
  /** Page size. */
  readonly limit?: number;
}

/**
 * Build a typed `TableQuery` from Tabulator's current sort/filter state.
 *
 * Header filters map to `eq` operators by default (Tabulator header filters are
 * exact-match text inputs unless a custom comparator is registered). The
 * adapter never interprets formatter output — only the raw filter value.
 */
export function buildQuery(options: QueryAdapterOptions): TableQuery {
  const keyword = normalizeKeyword(options.keyword);
  const filters: FilterCondition[] = [];
  const columns = new Map((options.columns ?? []).map((column) => [column.name, column]));
  for (const hf of options.headerFilters ?? []) {
    if (hf.value === null || hf.value === undefined || hf.value === "") {
      continue;
    }
    filters.push({
      field: hf.field,
      // Whole JSON documents support textual containment, not scalar
      // equality, in the authoritative QueryPort contract.
      operator: columns.get(hf.field)?.dataType === "json" ? "contains" : "eq",
      value: hf.value,
      logic: "AND",
    });
  }

  const sorts: SortCondition[] = (options.sorters ?? []).map((s) => ({
    field: s.field,
    direction: s.dir,
    nullsLast: true,
  }));

  return {
    ...(keyword !== null ? { keyword } : {}),
    filters,
    sorts,
    offset: options.offset ?? 0,
    limit: options.limit ?? 100,
  };
}

/**
 * Map a `TableQuery` back to Tabulator's sorter/header-filter shape so the
 * grid can restore the exact view the user left (used by `gridState` restore).
 */
export function queryToTabulator(query: TableQuery): {
  readonly sorters: readonly TabulatorSorter[];
  readonly headerFilters: readonly TabulatorHeaderFilter[];
} {
  const sorters: TabulatorSorter[] = (query.sorts ?? []).map((s) => ({
    field: s.field,
    dir: s.direction ?? "asc",
  }));
  const headerFilters: TabulatorHeaderFilter[] = ungroupedFilterConditions(
    query.filters ?? [],
  )
    .filter((f) => f.operator === "eq" || f.operator === "contains")
    .map((f) => ({ field: f.field, value: f.value }));
  return { sorters, headerFilters };
}

/** Normalize a keyword: trim, return null when empty/whitespace. */
function normalizeKeyword(keyword: string | null | undefined): string | null {
  if (keyword === null || keyword === undefined) {
    return null;
  }
  const trimmed = keyword.trim();
  return trimmed.length === 0 ? null : trimmed;
}

/**
 * Type guard: is the given operator a nullary operator (no value)?
 */
export function isNullaryOperator(op: FilterOperator): boolean {
  return op === "is_null" || op === "is_not_null";
}
