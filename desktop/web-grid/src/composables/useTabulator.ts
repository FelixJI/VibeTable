import { onBeforeUnmount, ref, watch, type Ref } from "vue";
import type { TabulatorFull } from "tabulator-tables";

import { useTableStore } from "@/stores/tableStore";
import { buildColumns, createGrid } from "@/grid/createGrid";
import type { CellEditedHandler, CellValidationErrorHandler } from "@/grid/createGrid";
import type { ColumnEditSchema, ColumnSchema, TablePage } from "@/contracts";

// Lazy CSS import — Tabulator's own stylesheet, bundled by Vite. Importing at
// module load guarantees the styles are present before the grid mounts.
// Verified path: tabulator-tables@6.5.2 ships dist/css/tabulator.min.css.
import "tabulator-tables/dist/css/tabulator.min.css";

/**
 * Stable signature for a column set, used to detect *real* schema changes
 * (column added/removed/renamed/reordered) and skip the expensive
 * `setColumns` call when only row data changed.
 */
function colSignature(columns: readonly ColumnSchema[]): string {
  return columns.map((c) => `${c.name}:${c.dataType}`).join("|");
}

/**
 * Stable signature for an edit schema, used to detect when the editable column
 * set actually changed (different editors / editable flags) vs. a no-op
 * re-emit of the same schema. Drives a `setColumns` refresh so newly-arrived
 * editors attach without a full grid rebuild.
 */
function editSchemaSignature(
  columns: readonly ColumnEditSchema[] | null | undefined,
): string {
  if (!columns) return "";
  return columns
    .map(
      (c) =>
        `${c.name}:${c.editable ? 1 : 0}:${c.editor.kind}:${
          c.editor.kind === "multi_select" ? 1 : 0
        }`,
    )
    .join("|");
}

/**
 * Shallow-compare two row arrays by element identity. Used to skip a
 * redundant `setData` when the data watcher fires in the same flush as init
 * (the rows were just embedded into the grid via `createGrid`).
 *
 * O(n) by reference; values are never deep-compared (the store flattens pages
 * into a fresh array, so any real data change produces new row references OR a
 * different length).
 */
function sameRows(
  a: ReadonlyArray<Record<string, unknown>>,
  b: ReadonlyArray<Record<string, unknown>>,
): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false;
  }
  return true;
}

/** Options accepted by `useTabulator` (Task M3: editable grid wiring). */
export interface UseTabulatorOptions {
  /**
   * Invoked when the user commits an inline cell edit, with the row's
   * `rowKey`, the column name, the pre-edit value (captured in `cellEditing`),
   * and the new value. The caller (WorkspaceView) routes this to
   * `mutationService.updateCell`.
   */
  readonly onCellEdited?: CellEditedHandler;
  /**
   * Invoked when an inline edit fails local validation (e.g. a value with too
   * many fractional digits). The grid has already rolled the cell back; the
   * caller (WorkspaceView) surfaces the error as a toast/banner.
   */
  readonly onValidationError?: CellValidationErrorHandler;
  /**
   * Optional EXTERNAL ref to populate with the Tabulator instance. When
   * provided (Task M5: WorkspaceView creates the ref, provides it via
   * inject, and GridHost forwards it here), useTabulator populates THIS ref
   * instead of a fresh internal one — so the caller can read the active range
   * for copy/paste/delete shortcuts. When omitted, useTabulator creates its
   * own ref (the historical behavior).
   */
  readonly tabulator?: Ref<TabulatorFull | null>;
}

/**
 * useTabulator — owns the Tabulator lifecycle for a single grid host.
 *
 * Architecture-debt fix #5: we instantiate Tabulator ONCE and update its data
 * incrementally via `setData`, instead of destroy+rebuild on every change.
 *
 * Init rule (adapts to `createGrid`'s real signature):
 *   `createGrid(element, page, opts?)` REQUIRES a full `TablePage` to
 *   initialize — it cannot build an empty grid first. So we wait until BOTH:
 *     (a) the host element is mounted (template ref populated), AND
 *     (b) the first `TablePage` has arrived in `store.pages`.
 *   Only then do we call `createGrid(el, pages[0], { editSchema, onCellEdited })`.
 *
 * After init:
 *   - Row-data changes -> `tabulator.setData(store.allRows)` (incremental).
 *     The first post-init data flush is skipped (createGrid already seeded
 *     those exact rows); detection is by row-reference identity, which is
 *     robust to flush-ordering quirks.
 *   - Column-schema changes -> `tabulator.setColumns(buildColumns(...))`
 *     (rare; Tabulator's `setColumns` exists per the env.d.ts type shim).
 *   - editSchema arrival/change -> `tabulator.setColumns(buildColumns(...,
 *     editSchema))` so editors attach without a grid rebuild (Task M3).
 *   - Unmount -> `tabulator.destroy()`.
 *
 * `onCellEdited` is captured in a ref-like holder so the latest caller-provided
 * callback always runs even if the composable is re-invoked with a new function
 * identity (closures built once at init must not go stale).
 */
export function useTabulator(
  gridEl: Ref<HTMLElement | null>,
  options?: UseTabulatorOptions,
) {
  const store = useTableStore();
  // Use the caller-provided ref if present (Task M5: WorkspaceView shares the
  // ref with the keyboard shortcuts via provide/inject). Otherwise create a
  // fresh internal ref (historical behavior).
  const tabulator = options?.tabulator ?? ref<TabulatorFull | null>(null);
  let lastColSignature: string | null = null;
  let lastEditSignature = editSchemaSignature(store.editSchema);

  /**
   * Holder for the latest `onCellEdited` callback. We read this inside the
   * `createGrid` init closure so the callback passed at init time forwards to
   * whatever the caller currently provides (avoids stale-capture when the
   * parent re-renders with a new function identity).
   */
  let currentOnCellEdited: CellEditedHandler | undefined = options?.onCellEdited;

  /**
   * Holder for the latest `onValidationError` callback. Same rationale as
   * `currentOnCellEdited`: keeps the caller's latest closure alive across
   * re-renders without re-initializing the grid.
   */
  let currentOnValidationError: CellValidationErrorHandler | undefined =
    options?.onValidationError;

  /**
   * Snapshot of the row array last handed to Tabulator (either via createGrid
   * at init or via setData). Used to skip no-op setData calls and, crucially,
   * to avoid re-pushing the seeded rows on the init flush.
   */
  let lastSeededRows: ReadonlyArray<Record<string, unknown>> = [];

  // Keep currentOnCellEdited fresh if the caller passes a new callback after
  // mount (defensive — typically the same function identity is stable across
  // re-renders, but we should not assume that).
  watch(
    () => options?.onCellEdited,
    (cb) => {
      currentOnCellEdited = cb;
    },
  );

  // Init: wait for the host element AND the first page. Fires immediately so
  // an already-mounted element with an existing page (e.g. a table reselect
  // while the component re-uses its element) initializes without an extra
  // tick. Pass the current editSchema + onCellEdited so editors attach on
  // first paint if the schema arrived before the page.
  watch(
    [() => gridEl.value, () => store.pages.length],
    ([el, pageCount]) => {
      if (!el || tabulator.value || pageCount === 0) return;
      const firstPage = store.pages[0];
      if (!firstPage) return;
      tabulator.value = createGrid(el, firstPage, {
        editSchema: store.editSchema,
        onCellEdited: (rk, col, old, nw) => currentOnCellEdited?.(rk, col, old, nw),
        onValidationError: (rk, col, err) => currentOnValidationError?.(rk, col, err),
      });
      lastColSignature = colSignature(firstPage.columns);
      lastEditSignature = editSchemaSignature(store.editSchema);
      // Record the rows we just seeded so the data watcher can recognize and
      // skip them (createGrid embedded these in its `data` option).
      lastSeededRows = firstPage.rows;
    },
    { immediate: true },
  );

  // Incremental row updates. setData is a no-op until the grid exists, and is
  // skipped when the rows are unchanged from what was last seeded/set
  // (covers the init flush, which embeds the first page's rows via createGrid).
  watch(
    () => store.allRows,
    (rows) => {
      if (!tabulator.value) return;
      if (sameRows(rows, lastSeededRows)) return;
      lastSeededRows = rows;
      // Flatten the readonly row array into a plain array for Tabulator.
      void tabulator.value.setData([...rows]);
    },
    { deep: false },
  );

  // Schema changes (column add/remove/rename) -> swap column definitions in
  // place. Rare, so a sync setColumns is acceptable; if Tabulator's real
  // runtime rejects it (version quirk), fall back to a data refresh.
  watch(
    () => store.schema,
    (schema) => {
      if (!tabulator.value || !schema) return;
      const sig = colSignature(schema);
      if (sig === lastColSignature) return;
      lastColSignature = sig;
      try {
        // buildColumns reads only `page.columns`; construct a minimal carrier
        // so the call stays type-safe without a full TablePage.
        const carrier = { columns: schema } as TablePage;
        tabulator.value.setColumns(
          buildColumns(carrier, store.editSchema) as unknown[],
        );
      } catch {
        // setColumns failed (Tabulator version quirk) -> refresh data only.
        void tabulator.value.setData([...store.allRows]);
      }
    },
  );

  // editSchema arrival/change (Task M3): rebuild columns IN PLACE via
  // setColumns so the per-column editors attach without a grid rebuild. The
  // editSchema typically arrives AFTER the first page (table.editSchemaLoaded
  // is a separate host event), so this is the primary path by which editable
  // columns get their editors.
  watch(
    () => store.editSchema,
    (editSchema) => {
      if (!tabulator.value) return;
      const sig = editSchemaSignature(editSchema);
      if (sig === lastEditSignature) return;
      lastEditSignature = sig;
      const schema = store.schema ?? store.pages[0]?.columns;
      if (!schema) return;
      try {
        const carrier = { columns: schema } as TablePage;
        tabulator.value.setColumns(
          buildColumns(carrier, editSchema) as unknown[],
        );
      } catch {
        // setColumns rejected (Tabulator version quirk) -> no-op; the grid
        // stays read-only until the next schema change forces a rebuild.
      }
    },
  );

  onBeforeUnmount(() => {
    tabulator.value?.destroy?.();
    tabulator.value = null;
  });

  return { tabulator };
}
