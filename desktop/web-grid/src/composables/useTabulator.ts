import { onBeforeUnmount, ref, watch, type Ref } from "vue";
import type { TabulatorFull } from "tabulator-tables";

import { useTableStore } from "@/stores/tableStore";
import { buildTabulatorColumns, createGrid } from "@/grid/createGrid";
import type { CellEditedHandler, CellValidationErrorHandler, RelationLookupGridContext } from "@/grid/createGrid";
import type { ColumnEditSchema, ColumnSchema, LookupValueProvenance, NormalizedRelationDescriptor, TablePage } from "@/contracts";
import type { FilterExpression, GroupCondition, SortCondition } from "@/contracts";
import { buildQuery } from "@/grid/queryAdapter";
import { useRelationLookupStore } from "@/stores/relationLookupStore";
import { currentLocale, t } from "@/i18n";

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
  /** Reports the latest Tabulator range so multi-cell history can be disabled. */
  readonly onRangeSelectionChanged?: (selection: {
    readonly rowKeys: readonly (string | number)[];
    readonly fields: readonly string[];
  }) => void;
  /**
   * Invoked when an inline edit fails local validation (e.g. a value with too
   * many fractional digits). The grid has already rolled the cell back; the
   * caller (WorkspaceView) surfaces the error as a toast/banner.
   */
  readonly onValidationError?: CellValidationErrorHandler;
  readonly onRelationEditRequested?: (
    rowKey: string | number,
    column: string,
    descriptor: NormalizedRelationDescriptor,
    value: unknown,
  ) => void;
  readonly onLookupSourceRequested?: (source: LookupValueProvenance) => void;
  readonly onAttachmentOpenRequested?: (
    rowKey: string | number,
    column: ColumnSchema,
  ) => void;
  /** User sort/filter/group intent; always executed against the full dataset. */
  readonly onViewQueryChanged?: (query: {
    readonly filters: readonly FilterExpression[];
    readonly sorts: readonly SortCondition[];
    readonly groups: readonly GroupCondition[];
  }) => void;
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
  const relationLookupStore = useRelationLookupStore();
  // Use the caller-provided ref if present (Task M5: WorkspaceView shares the
  // ref with the keyboard shortcuts via provide/inject). Otherwise create a
  // fresh internal ref (historical behavior).
  const tabulator = options?.tabulator ?? ref<TabulatorFull | null>(null);
  const dataApplying = ref(false);
  let lastColSignature: string | null = null;
  let lastSchemaGeneration = -1;
  let lastEditSignature = editSchemaSignature(store.editSchema);
  let lastRelationSignature = "";

  const relationContext = (): RelationLookupGridContext => ({
    relations: relationLookupStore.relationsById,
    lookups: relationLookupStore.lookupsById,
    relationEditAvailable: !!relationLookupStore.capabilities?.relationEditV1,
    lookupQueryAvailable: !!relationLookupStore.capabilities?.lookupQueryV1,
    lookupUnavailableReason: relationLookupStore.lookupUnavailableReason,
    onRelationEditRequested: options?.onRelationEditRequested,
    onLookupSourceRequested: options?.onLookupSourceRequested,
    onAttachmentOpenRequested: options?.onAttachmentOpenRequested,
  });

  /**
   * Holder for the latest `onCellEdited` callback. We read this inside the
   * `createGrid` init closure so the callback passed at init time forwards to
   * whatever the caller currently provides (avoids stale-capture when the
   * parent re-renders with a new function identity).
   */
  let currentOnCellEdited: CellEditedHandler | undefined = options?.onCellEdited;
  let currentOnRangeSelectionChanged = options?.onRangeSelectionChanged;
  let rangeChangedHandler: ((range: unknown) => void) | null = null;
  let tableBuiltHandler: (() => void) | null = null;
  let viewQueryHandler: (() => void) | null = null;
  let groupChangedHandler: ((groups: unknown) => void) | null = null;
  let cellEditingHandler: (() => void) | null = null;
  let cellEditFinishedHandler: (() => void) | null = null;
  let editing = false;
  let queuedColumnRefresh = false;
  let applyingColumns: Promise<void> | null = null;
  let queuedRows: ReadonlyArray<Record<string, unknown>> | null = null;
  let applyingRows: Promise<void> | null = null;
  let activeGroups: GroupCondition[] = [];
  let lastViewQuerySignature = "";
  let gridReady = false;
  let gridOperationsReady = false;

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

  // Column titles come from the schema, but filter placeholders, structured
  // cell labels and empty-state copy are localized at construction time.
  // Rebuild in place so an already-open table switches language immediately.
  watch(currentLocale, () => {
    queueColumnRefresh();
  });
  watch(
    () => options?.onRangeSelectionChanged,
    (callback) => {
      currentOnRangeSelectionChanged = callback;
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
        onCellEdited: (rk, col, old, nw, digest) =>
          currentOnCellEdited?.(rk, col, old, nw, digest),
        onValidationError: (rk, col, err) => currentOnValidationError?.(rk, col, err),
        relationLookup: relationContext(),
      });
      gridOperationsReady =
        (tabulator.value as unknown as { initialized?: boolean }).initialized !== false;
      const eventGrid = tabulator.value as unknown as {
        on?: (event: string, handler: (...args: unknown[]) => void) => void;
        getSorters?: () => Array<{ field: string; dir: "asc" | "desc" }>;
        getHeaderFilters?: () => Array<{ field: string; value: unknown }>;
      };
      rangeChangedHandler = (rawRange: unknown) => {
        const range = rawRange as {
          getRows?: () => Array<{ getData: () => Record<string, unknown> }>;
          getColumns?: () => Array<{ getField: () => string }>;
        };
        const rowKeys = (range.getRows?.() ?? []).flatMap((row) => {
          const key = row.getData().rowKey;
          return typeof key === "string" || typeof key === "number" ? [key] : [];
        });
        const fields = (range.getColumns?.() ?? []).map((column) => column.getField());
        currentOnRangeSelectionChanged?.({ rowKeys, fields });
      };
      eventGrid.on?.("rangeChanged", rangeChangedHandler);
      tableBuiltHandler = () => {
        gridReady = true;
        gridOperationsReady = true;
        void drainQueuedColumns();
        void drainQueuedRows();
      };
      eventGrid.on?.("tableBuilt", tableBuiltHandler);
      viewQueryHandler = () => emitViewQuery(eventGrid);
      groupChangedHandler = (rawGroups: unknown) => {
        activeGroups = collectGroupFields(rawGroups);
        emitViewQuery(eventGrid);
      };
      eventGrid.on?.("dataSorted", viewQueryHandler);
      eventGrid.on?.("dataFiltered", viewQueryHandler);
      eventGrid.on?.("dataGrouped", groupChangedHandler);
      cellEditingHandler = () => {
        editing = true;
      };
      cellEditFinishedHandler = () => {
        editing = false;
        void drainQueuedColumns();
        void drainQueuedRows();
      };
      eventGrid.on?.("cellEditing", cellEditingHandler);
      eventGrid.on?.("cellEdited", cellEditFinishedHandler);
      eventGrid.on?.("cellEditCancelled", cellEditFinishedHandler);
      lastColSignature = colSignature(firstPage.columns);
      lastEditSignature = editSchemaSignature(store.editSchema);
      lastSchemaGeneration = store.loadGeneration;
      lastRelationSignature = relationSignature();
      queuedColumnRefresh = false;
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
      queuedRows = rows;
      void drainQueuedRows();
    },
    { deep: false },
  );

  // Schema changes (column add/remove/rename) -> swap column definitions in
  // place. Rare, so a sync setColumns is acceptable; if Tabulator's real
  // runtime rejects it (version quirk), fall back to a data refresh.
  watch(
    [() => store.schema, () => store.loadGeneration],
    ([schema, generation]) => {
      if (!tabulator.value || !schema) return;
      const sig = colSignature(schema);
      if (sig === lastColSignature && generation === lastSchemaGeneration) return;
      queueColumnRefresh();
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
      const schema = store.schema ?? store.pages[0]?.columns;
      if (!schema) return;
      queueColumnRefresh();
    },
  );

  watch(
    [
      () => relationLookupStore.schema?.collection,
      () => relationLookupStore.schema?.schemaRevision,
      () => relationLookupStore.schema?.permissionRevision,
      () => relationLookupStore.schema?.lookupRevision,
      () => relationLookupStore.schema?.normalizedRelations
        .map((relation) => relation.relationId)
        .sort()
        .join("|"),
      () => relationLookupStore.lookups
        .map((lookup) => lookup.lookupId)
        .sort()
        .join("|"),
      () => relationLookupStore.capabilities?.relationEditV1,
      () => relationLookupStore.capabilities?.lookupQueryV1,
    ],
    () => {
      if (!tabulator.value) return;
      const signature = relationSignature();
      if (signature === lastRelationSignature) return;
      const schema = store.schema ?? store.pages[0]?.columns;
      if (!schema) return;
      queueColumnRefresh();
    },
  );

  onBeforeUnmount(() => {
    gridReady = false;
    gridOperationsReady = false;
    queuedRows = null;
    queuedColumnRefresh = false;
    const retiringGrid = tabulator.value;
    tabulator.value = null;
    if (rangeChangedHandler) {
      (retiringGrid as unknown as { off?: (event: string, handler: (range: unknown) => void) => void } | null)
        ?.off?.("rangeChanged", rangeChangedHandler);
    }
    const eventGrid = retiringGrid as unknown as {
      off?: (event: string, handler: (...args: unknown[]) => void) => void;
    } | null;
    if (tableBuiltHandler) eventGrid?.off?.("tableBuilt", tableBuiltHandler);
    if (viewQueryHandler) {
      eventGrid?.off?.("dataSorted", viewQueryHandler);
      eventGrid?.off?.("dataFiltered", viewQueryHandler);
    }
    if (groupChangedHandler) eventGrid?.off?.("dataGrouped", groupChangedHandler);
    if (cellEditingHandler) eventGrid?.off?.("cellEditing", cellEditingHandler);
    if (cellEditFinishedHandler) {
      eventGrid?.off?.("cellEdited", cellEditFinishedHandler);
      eventGrid?.off?.("cellEditCancelled", cellEditFinishedHandler);
    }
    const pendingApplications = [applyingRows, applyingColumns].filter(
      (pending): pending is Promise<void> => pending !== null,
    );
    if (pendingApplications.length === 0) {
      retiringGrid?.destroy?.();
    } else {
      void Promise.allSettled(pendingApplications).then(() => {
        retiringGrid?.destroy?.();
      });
    }
  });

  return { tabulator, dataApplying };

  async function drainQueuedRows(): Promise<void> {
    if (
      editing
      || applyingRows
      || applyingColumns
      || !gridOperationsReady
      || !tabulator.value
      || !queuedRows
    ) return;
    const grid = tabulator.value;
    applyingRows = (async () => {
      dataApplying.value = true;
      try {
        while (!editing && queuedRows && tabulator.value === grid) {
          const rows = queuedRows;
          queuedRows = null;
          lastSeededRows = rows;
          await Promise.resolve(grid.setData([...rows]));
        }
      } finally {
        dataApplying.value = false;
        applyingRows = null;
      }
      if (!editing && queuedColumnRefresh) await drainQueuedColumns();
      if (!editing && queuedRows) await drainQueuedRows();
    })();
    await applyingRows;
  }

  function queueColumnRefresh(): void {
    queuedColumnRefresh = true;
    void drainQueuedColumns();
  }

  async function drainQueuedColumns(): Promise<void> {
    if (
      editing
      || applyingColumns
      || applyingRows
      || !gridOperationsReady
      || !tabulator.value
      || !queuedColumnRefresh
    ) return;
    const grid = tabulator.value;
    applyingColumns = (async () => {
      while (
        !editing
        && queuedColumnRefresh
        && tabulator.value === grid
      ) {
        const schema = store.schema ?? store.pages[0]?.columns;
        if (!schema) {
          queuedColumnRefresh = false;
          break;
        }
        queuedColumnRefresh = false;
        const carrier = { columns: schema } as TablePage;
        retireDomSelectionBeforeColumnRefresh();
        try {
          // Tabulator may complete setColumns asynchronously. Serializing the
          // calls prevents a datasetReady rebuild from racing the immediately
          // following editSchemaLoaded rebuild and leaving the grid read-only.
          await Promise.resolve(grid.setColumns(
            buildTabulatorColumns(
              carrier,
              store.editSchema,
              relationContext(),
            ) as unknown[],
          ));
          lastColSignature = colSignature(schema);
          lastSchemaGeneration = store.loadGeneration;
          lastEditSignature = editSchemaSignature(store.editSchema);
          lastRelationSignature = relationSignature();
          refreshLocalizedPlaceholder(grid, gridEl.value);
        } catch {
          queuedRows = store.allRows;
        }
      }
    })();
    try {
      await applyingColumns;
    } finally {
      applyingColumns = null;
    }
    if (!editing && queuedColumnRefresh) await drainQueuedColumns();
    if (!editing && queuedRows) await drainQueuedRows();
  }

  function retireDomSelectionBeforeColumnRefresh(): void {
    const ranges = (tabulator.value as unknown as {
      getRanges?: () => Array<{ remove?: () => void }>;
    } | null)?.getRanges?.() ?? [];
    for (const range of ranges) range.remove?.();
    const host = gridEl.value;
    if (!host) return;
    const active = document.activeElement;
    if (active instanceof HTMLElement && host.contains(active)) active.blur();
    const selection = window.getSelection();
    if (selection?.rangeCount) selection.removeAllRanges();
  }

  function relationSignature(): string {
    const current = relationLookupStore.schema;
    return [
      current?.collection ?? "",
      current?.schemaRevision ?? "",
      current?.permissionRevision ?? "",
      current?.lookupRevision ?? "",
      (current?.normalizedRelations ?? [])
        .map((relation) => relation.relationId)
        .sort()
        .join(","),
      relationLookupStore.lookups
        .map((lookup) => lookup.lookupId)
        .sort()
        .join(","),
      relationLookupStore.capabilities?.relationEditV1 ? "e1" : "e0",
      relationLookupStore.capabilities?.lookupQueryV1 ? "l1" : "l0",
    ].join(":");
  }

  function emitViewQuery(grid: {
    getSorters?: () => Array<{ field: string; dir: "asc" | "desc" }>;
    getHeaderFilters?: () => Array<{ field: string; value: unknown }>;
  }): void {
    if (!gridReady) return;
    const query = buildQuery({
      sorters: grid.getSorters?.() ?? [],
      headerFilters: grid.getHeaderFilters?.() ?? [],
      columns: store.schema ?? [],
      offset: 0,
      limit: 10_000,
    });
    const view = {
      filters: [...(query.filters ?? [])],
      sorts: [...(query.sorts ?? [])],
      groups: activeGroups,
    };
    const signature = JSON.stringify(view);
    if (signature === lastViewQuerySignature) return;
    lastViewQuerySignature = signature;
    options?.onViewQueryChanged?.(view);
  }
}

function refreshLocalizedPlaceholder(
  grid: TabulatorFull,
  host: HTMLElement | null,
): void {
  const copy = t("grid.empty");
  const runtime = grid as unknown as {
    options?: Record<string, unknown>;
  };
  if (runtime.options) runtime.options.placeholder = copy;
  const contents = host?.querySelector<HTMLElement>(".tabulator-placeholder-contents");
  if (contents) contents.textContent = copy;
}

function collectGroupFields(raw: unknown): GroupCondition[] {
  if (!Array.isArray(raw)) return [];
  const fields = new Set<string>();
  const visit = (groups: unknown[]): void => {
    for (const group of groups) {
      if (!group || typeof group !== "object") continue;
      const component = group as { getField?: () => string; getSubGroups?: () => unknown[] };
      const field = component.getField?.();
      if (field) fields.add(field);
      visit(component.getSubGroups?.() ?? []);
    }
  };
  visit(raw);
  return [...fields].map((field) => ({ field, direction: "asc", bucket: "value" }));
}
