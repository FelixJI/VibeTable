import { onBeforeUnmount, ref, watch, type Ref } from "vue";
import type { TabulatorFull } from "tabulator-tables";

import { useTableStore } from "@/stores/tableStore";
import { buildColumns, createGrid } from "@/grid/createGrid";
import type { ColumnSchema, TablePage } from "@/contracts";

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

/**
 * useTabulator — owns the Tabulator lifecycle for a single grid host.
 *
 * Architecture-debt fix #5: we instantiate Tabulator ONCE and update its data
 * incrementally via `setData`, instead of destroy+rebuild on every change.
 *
 * Init rule (adapts to `createGrid`'s real signature):
 *   `createGrid(element, page)` REQUIRES a full `TablePage` to initialize —
 *   it cannot build an empty grid first. So we wait until BOTH:
 *     (a) the host element is mounted (template ref populated), AND
 *     (b) the first `TablePage` has arrived in `store.pages`.
 *   Only then do we call `createGrid(el, pages[0])`.
 *
 * After init:
 *   - Row-data changes -> `tabulator.setData(store.allRows)` (incremental).
 *     The first post-init data flush is skipped (createGrid already seeded
 *     those exact rows); detection is by row-reference identity, which is
 *     robust to flush-ordering quirks.
 *   - Column-schema changes -> `tabulator.setColumns(buildColumns(...))`
 *     (rare; Tabulator's `setColumns` exists per the env.d.ts type shim).
 *   - Unmount -> `tabulator.destroy()`.
 */
export function useTabulator(gridEl: Ref<HTMLElement | null>) {
  const store = useTableStore();
  const tabulator = ref<TabulatorFull | null>(null);
  let lastColSignature: string | null = null;
  /**
   * Snapshot of the row array last handed to Tabulator (either via createGrid
   * at init or via setData). Used to skip no-op setData calls and, crucially,
   * to avoid re-pushing the seeded rows on the init flush.
   */
  let lastSeededRows: ReadonlyArray<Record<string, unknown>> = [];

  // Init: wait for the host element AND the first page. Fires immediately so
  // an already-mounted element with an existing page (e.g. a table reselect
  // while the component re-uses its element) initializes without an extra
  // tick.
  watch(
    [() => gridEl.value, () => store.pages.length],
    ([el, pageCount]) => {
      if (!el || tabulator.value || pageCount === 0) return;
      const firstPage = store.pages[0];
      if (!firstPage) return;
      tabulator.value = createGrid(el, firstPage);
      lastColSignature = colSignature(firstPage.columns);
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
        tabulator.value.setColumns(buildColumns(carrier) as unknown[]);
      } catch {
        // setColumns failed (Tabulator version quirk) -> refresh data only.
        void tabulator.value.setData([...store.allRows]);
      }
    },
  );

  onBeforeUnmount(() => {
    tabulator.value?.destroy?.();
    tabulator.value = null;
  });

  return { tabulator };
}
