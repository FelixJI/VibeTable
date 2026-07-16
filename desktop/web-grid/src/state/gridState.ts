/**
 * B3 Task 5: durable grid-state restore and reconcile.
 *
 * The host loads saved grid state (column width/order/visibility/frozen,
 * sort/filter/search, density, forced-remote) through the backend's local
 * user-state database. This module restores that state onto a Tabulator grid
 * AFTER the schema has loaded, pruning columns that no longer exist and
 * leaving newly-added columns visible by default.
 *
 * State is NEVER restored before the schema is known — restoring onto a stale
 * column set would apply widths to the wrong columns.
 */

import type {
  ColumnSchema,
  ColumnState,
  GridState,
  SortCondition,
  FilterCondition,
} from "../contracts";
import type { TabulatorSorter, TabulatorHeaderFilter } from "../query/queryAdapter";

/** A reconciled column state after pruning against the live schema. */
export interface ReconciledGridState {
  /** Column states for columns that still exist in the schema. */
  readonly columns: readonly ColumnState[];
  /** Column names present in the schema but absent from the saved state. */
  readonly newlyAdded: readonly string[];
  /** The sort conditions to restore. */
  readonly sorts: readonly SortCondition[];
  /** The filter conditions to restore. */
  readonly filters: readonly FilterCondition[];
  /** The keyword to restore, or null. */
  readonly keyword: string | null;
  /** The density hint. */
  readonly density: "compact" | "comfortable" | "cozy";
  /** The forced-remote preference. */
  readonly forcedRemote: boolean;
}

/**
 * Reconcile saved grid state against the live schema.
 *
 * - Columns in the saved state that no longer exist in the schema are pruned.
 * - Columns in the schema that are absent from the saved state are reported as
 *   `newlyAdded` (the host applies its own default — visible).
 * - Sorts/filters referencing pruned columns are also dropped.
 */
export function reconcileState(
  saved: GridState,
  schemaColumns: readonly ColumnSchema[],
): ReconciledGridState {
  const liveNames = new Set(schemaColumns.map((c) => c.name));
  const savedByName = new Map<string, ColumnState>();
  for (const col of saved.columns ?? []) {
    savedByName.set(col.name, col);
  }

  const columns: ColumnState[] = [];
  const newlyAdded: string[] = [];
  for (const live of schemaColumns) {
    const existing = savedByName.get(live.name);
    if (existing) {
      columns.push(existing);
    } else {
      newlyAdded.push(live.name);
    }
  }

  const sorts = (saved.sorts ?? []).filter((s) => liveNames.has(s.field));
  const filters = (saved.filters ?? []).filter((f) => liveNames.has(f.field));

  return {
    columns,
    newlyAdded,
    sorts,
    filters,
    keyword: normalizeKeyword(saved.keyword),
    density: saved.density ?? "comfortable",
    forcedRemote: saved.forcedRemote ?? false,
  };
}

/**
 * Build the Tabulator restore shape from a reconciled state: the column
 * definitions (width/visible/frozen/order) and the sorter/header-filter lists.
 */
export function buildRestorePlan(state: ReconciledGridState): {
  readonly columnLayout: readonly {
    readonly field: string;
    readonly width?: number;
    readonly visible: boolean;
    readonly frozen: boolean;
  }[];
  readonly sorters: readonly TabulatorSorter[];
  readonly headerFilters: readonly TabulatorHeaderFilter[];
} {
  const columnLayout = state.columns.map((c) => ({
    field: c.name,
    ...(c.width !== null && c.width !== undefined ? { width: c.width } : {}),
    visible: c.visible ?? true,
    frozen: c.frozen ?? false,
  }));
  const sorters: TabulatorSorter[] = state.sorts.map((s) => ({
    field: s.field,
    dir: s.direction ?? "asc",
  }));
  const headerFilters: TabulatorHeaderFilter[] = state.filters
    .filter((f) => f.operator === "eq")
    .map((f) => ({ field: f.field, value: f.value }));
  return { columnLayout, sorters, headerFilters };
}

/**
 * Build a fresh default grid state (used after a reset).
 */
export function defaultGridState(): GridState {
  return {
    columns: [],
    sorts: [],
    filters: [],
    keyword: null,
    density: "comfortable",
    forcedRemote: false,
    revision: null,
  };
}

/**
 * Detect a state-revision conflict: the host returned `conflict=true` on save,
 * meaning another session saved newer state. The caller must re-read and merge.
 */
export function isStateConflict(result: {
  readonly conflict?: boolean;
}): boolean {
  return result.conflict === true;
}

/** Normalize a keyword: trim, return null when empty/whitespace. */
function normalizeKeyword(
  keyword: string | null | undefined,
): string | null {
  if (keyword === null || keyword === undefined) {
    return null;
  }
  const trimmed = keyword.trim();
  return trimmed.length === 0 ? null : trimmed;
}
