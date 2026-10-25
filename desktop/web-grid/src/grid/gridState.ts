/**
 * B3 Task 5: durable grid-state restore and reconcile.
 *
 * The host loads saved grid state (column width/order/visibility/frozen,
 * sort/filter/search and density) through the Host's device-local store.
 * This module reconciles that state for a Tabulator grid
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
  FilterExpression,
} from "@/contracts";
import { headerFilterConditions } from "./viewQuery";
import type { TabulatorSorter, TabulatorHeaderFilter } from "./queryAdapter";

/** 行 ID（主键）列的物理名；与权威 SchemaSnapshot.primaryKey 一致。 */
export const ROW_ID_COLUMN_NAME = "id";

/**
 * 行 ID 列判定：物理名 `id` 是保留的记录主键名（PocketBase 记录 ID，
 * 用户字段不可能占用），直接按名字识别，与 defaultVisibleFields 同一规则。
 * 保留 kind 参数仅为 ColumnSchema 调用形状兼容。
 */
export function isRowIdColumn(
  column: { readonly name: string; readonly kind?: string | null },
): boolean {
  return isRowIdFieldName(column.name);
}

/** 仅知列名时的行 ID 判定（默认可见列列表只有列名）。 */
export function isRowIdFieldName(name: string): boolean {
  return name === ROW_ID_COLUMN_NAME;
}

/** A reconciled column state after pruning against the live schema. */
export interface ReconciledGridState {
  /** Column states for columns that still exist in the schema. */
  readonly columns: readonly ColumnState[];
  /** Column names present in the schema but absent from the saved state. */
  readonly newlyAdded: readonly string[];
  /** The sort conditions to restore. */
  readonly sorts: readonly SortCondition[];
  /** The filter conditions to restore. */
  readonly filters: readonly FilterExpression[];
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
  const filters = reconcileFilters(saved.filters ?? [], liveNames);

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
export function orderSavedColumns(columns: readonly ColumnState[]): ColumnState[] {
  return [...columns].sort((left, right) => {
    if (left.order == null) return right.order == null ? 0 : 1;
    if (right.order == null) return -1;
    return left.order - right.order;
  });
}

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
  const orderedColumns = orderSavedColumns(state.columns);
  const columnLayout = orderedColumns.map((c) => ({
    field: c.name,
    ...(c.width !== null && c.width !== undefined ? { width: c.width } : {}),
    visible: c.visible ?? true,
    frozen: c.frozen ?? false,
  }));
  const sorters: TabulatorSorter[] = state.sorts.map((s) => ({
    field: s.field,
    dir: s.direction ?? "asc",
  }));
  const headerFilters: TabulatorHeaderFilter[] = headerFilterConditions(state.filters)
    .map((f) => ({ field: f.field, value: f.value }));
  return { columnLayout, sorters, headerFilters };
}

function reconcileFilters(filters: readonly FilterExpression[], fields: ReadonlySet<string>): FilterExpression[] {
  return filters.flatMap((filter): FilterExpression[] => {
    if ("field" in filter) return fields.has(filter.field) ? [{ ...filter }] : [];
    const children = reconcileFilters(filter.filters, fields);
    return children.length ? [{ ...filter, filters: children }] : [];
  });
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
