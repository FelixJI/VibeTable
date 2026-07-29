import { defineStore } from "pinia";
import { computed, ref } from "vue";
import type {
  ColumnEditSchema,
  ColumnSchema,
  DatasetReadyPayload,
  DeleteRowsResult,
  InsertRowResult,
  MutationRevision,
  LookupQueryResult,
  TablePage,
  UpdateCellResult,
} from "@/contracts";

const LOOKUP_STABLE_KEY_ERROR =
  "Lookup query returned a row without a stable key.";

/**
 * tableStore — renderer-side bookkeeping for the loaded table.
 *
 * Tracks the loaded table's pages (accumulated INCREMENTALLY from
 * `table.pageLoaded` events — never destroy + rebuild per page), schema,
 * rowCount, and loading/error state. The host drives the multi-page client-mode
 * fetch: one `table.pageLoaded` per page, then a single `table.datasetReady`
 * once `loadedRows == totalRows`. `tableService` wires the inbound events to
 * this store; see `src/services/tableService.ts`.
 *
 * DatasetReady nuance: `DatasetReadyPayload extends TablePage`, so the
 * `datasetReady` payload IS a page carrying the authoritative full dataset
 * (per `contracts/index.ts`: "the complete client-mode dataset"). Appending it
 * on top of accumulated pages would double-count rows. Instead — mirroring the
 * legacy `desktop/web-grid/src/tableFlow.ts` (`rows: payload.rows` replacement
 * on datasetReady) — `setDatasetReady` REPLACES accumulated pages with this one
 * authoritative page. `allRows` then flattens a single page, yielding the full
 * dataset with no double-count.
 */
export const useTableStore = defineStore("table", () => {
  /** True while the host is fetching pages for the current table. */
  const loading = ref(false);
  /** True after `table.datasetReady` arrived (full dataset loaded). */
  const datasetReady = ref(false);
  /** Accumulated pages (incremental). Replaced wholesale by datasetReady. */
  const pages = ref<TablePage[]>([]);
  /** Schema columns learned from the host; null before the first page. */
  const schema = ref<readonly ColumnSchema[] | null>(null);
  /** Total rows in the table (from `TablePage.totalRows`). */
  const rowCount = ref(0);
  /** Last error message, surfaced in the error overlay. Cleared on success. */
  const error = ref<string | null>(null);
  /** Editable column schema learned from `table.editSchemaLoaded`; null until then. */
  const editSchema = ref<readonly ColumnEditSchema[] | null>(null);
  /** Schema revision to which the retained edit definitions belong. */
  const editSchemaRevision = ref<string | null>(null);
  /** Current mutation revision (databaseSessionId/schemaRevision/dataRevision). */
  const revision = ref<MutationRevision | null>(null);
  /**
   * Monotonic accepted revision retained across same-schema background loads.
   * `revision` is cleared while loading to block writes; this separate floor
   * still prevents late pages from rolling committed rows back.
   */
  const revisionFloor = ref<MutationRevision | null>(null);
  /**
   * Monotonic table-load identity. A refresh may return an identical schema
   * signature, but the existing Tabulator columns can still have crossed a
   * reset boundary and must be rebound to the newly loaded edit schema.
   */
  const loadGeneration = ref(0);
  /** Authoritative group nodes returned by the server-side Lookup executor. */
  const lookupGroups = ref<LookupQueryResult["groups"]>([]);

  /** All accumulated rows across pages, flattened in order. */
  const allRows = computed<ReadonlyArray<Record<string, unknown>>>(() =>
    pages.value.flatMap((p) => p.rows),
  );

  /** Begin a load: clear stale state and mark loading. */
  function beginLoad(): void {
    loadGeneration.value += 1;
    loading.value = true;
    datasetReady.value = false;
    pages.value = [];
    schema.value = null;
    rowCount.value = 0;
    error.value = null;
    lookupGroups.value = [];
  }

  /**
   * Accumulate one incremental page from `table.pageLoaded`. Pages are pushed
   * in arrival order; `allRows` flattens them. Also learn the schema/count from
   * each page so the grid can render incrementally before datasetReady.
   */
  function appendPage(page: TablePage): boolean {
    discardEditSchemaForDifferentRevision(page.revision);
    const incoming = page.revision;
    if (isBelowRevisionFloor(incoming)) return false;
    // Offset zero starts a new read attempt. This matters when the desktop
    // host restarts a client-mode multi-page fetch after detecting that the
    // database revision changed between pages. Remote mode also retains only
    // the requested page rather than accumulating unrelated windows.
    pages.value = page.offset === 0 || page.mode === "remote"
      ? [page]
      : [...pages.value, page];
    // Adopt the page's schema/count if present so the grid can render during
    // the incremental load (host advertises the same schema/count per page).
    if (page.columns.length > 0) {
      schema.value = page.columns;
    }
    rowCount.value = page.totalRows;
    // Remote mode never emits datasetReady. Each page therefore carries the
    // only authoritative mutation revision available to the renderer.
    if (page.revision) {
      adoptRevision(page.revision);
    }
    return true;
  }

  /**
   * End a page-oriented load that intentionally has no datasetReady event
   * (remote mode). The page remains incremental/not-full, so datasetReady
   * stays false.
   */
  function finishPageLoad(): void {
    loading.value = false;
  }

  /**
   * Handle `table.datasetReady`. The payload extends `TablePage` and carries
   * the authoritative full dataset; accumulated incremental pages are replaced
   * by this single page to avoid double-counting rows. Stores the schema, final
   * row count, ends loading, and marks the dataset ready.
   */
  function setDatasetReady(payload: DatasetReadyPayload): boolean {
    discardEditSchemaForDifferentRevision(payload.revision);
    const incoming = payload.revision;
    // A refresh can finish after a newer edit/undo has already committed.
    // Never let that older snapshot roll the rendered rows and revision back.
    if (isBelowRevisionFloor(incoming)) {
      loading.value = false;
      return false;
    }
    pages.value = [payload];
    schema.value = payload.columns;
    rowCount.value = payload.totalRows;
    datasetReady.value = true;
    loading.value = false;
    // DatasetReadyPayload extends TablePage which carries the authoritative
    // MutationRevision. If the host supplied one, adopt it (overriding the
    // placeholder databaseSessionId/dataRevision set by setEditSchema).
    if (payload.revision) {
      adoptRevision(payload.revision);
    }
    return true;
  }

  /** Record an error and end loading. */
  function setError(message: string): void {
    error.value = message;
    loading.value = false;
  }

  /** Reset all state to idle (used when switching tables/databases). */
  function reset(options: { readonly preserveEditSchema?: boolean } = {}): void {
    loading.value = false;
    datasetReady.value = false;
    pages.value = [];
    schema.value = null;
    rowCount.value = 0;
    error.value = null;
    if (!options.preserveEditSchema) {
      editSchema.value = null;
      editSchemaRevision.value = null;
    }
    revision.value = null;
    if (!options.preserveEditSchema) {
      revisionFloor.value = null;
    }
    lookupGroups.value = [];
  }

  /**
   * Store the editable column schema + initial revision, learned from
   * `table.editSchemaLoaded`. The full MutationRevision (with real
   * databaseSessionId/dataRevision) typically arrives later via
   * `table.datasetReady`, which overrides the revision here.
   */
  function setEditSchema(
    cols: readonly ColumnEditSchema[],
    rev: MutationRevision,
  ): void {
    if (
      revisionFloor.value
      && revisionFloor.value.schemaRevision !== rev.schemaRevision
    ) {
      revisionFloor.value = null;
    }
    editSchema.value = cols;
    editSchemaRevision.value = rev.schemaRevision;
    // The desktop host currently emits datasetReady before editSchemaLoaded.
    // Do not let the edit-schema placeholder erase the authoritative database
    // session / data revision that datasetReady already supplied.
    if (revision.value?.schemaRevision !== rev.schemaRevision) {
      // During a same-schema data refresh, keep writes blocked until a page
      // reaches the retained floor; do not publish the edit-schema placeholder
      // revision (session="", data=0) as if it were authoritative.
      revision.value = revisionFloor.value?.schemaRevision === rev.schemaRevision
        ? null
        : rev;
    }
  }

  function isBelowRevisionFloor(
    incoming: MutationRevision | null | undefined,
  ): boolean {
    const floor = revisionFloor.value;
    return !!(
      floor
      && incoming
      && floor.databaseSessionId === incoming.databaseSessionId
      && floor.schemaRevision === incoming.schemaRevision
      && incoming.dataRevision < floor.dataRevision
    );
  }

  function adoptRevision(incoming: MutationRevision): boolean {
    if (isBelowRevisionFloor(incoming)) return false;
    revisionFloor.value = incoming;
    revision.value = incoming;
    return true;
  }

  function discardEditSchemaForDifferentRevision(
    incoming: MutationRevision | null | undefined,
  ): void {
    if (
      editSchemaRevision.value
      && incoming
      && incoming.schemaRevision !== editSchemaRevision.value
    ) {
      editSchema.value = null;
      editSchemaRevision.value = null;
    }
  }

  /**
   * Apply an inbound `table.editCommitted` result. Replace the affected row so
   * the grid's identity-based data watcher cannot mistake an undo/redo commit
   * for a no-op. The host may return the full updated row in `currentRow`,
   * which we merge in, but the authoritative stored value for the edited
   * column is `storedValue`.
   */
  function applyCellEdit(result: UpdateCellResult): void {
    pages.value = pages.value.map((page) => {
      let changed = false;
      const rows = page.rows.map((row) => {
        if (row.rowKey !== result.rowKey) return row;
        changed = true;
        return {
          ...row,
          ...result.currentRow,
          rowKey: result.rowKey,
          [result.column]: result.storedValue,
        };
      });
      return changed ? { ...page, rows } : page;
    });
    adoptRevision(result.revision);
  }

  /**
   * Restore only the cell/row affected by a rejected optimistic edit. Replacing
   * the page object makes useTabulator push the corrected row back into the
   * grid without putting the whole table into an error state.
   */
  function rollbackCellEdit(
    rowKey: number | string,
    column: string,
    oldValue: unknown,
    currentRow?: Readonly<Record<string, unknown>> | null,
  ): void {
    pages.value = pages.value.map((page) => {
      let changed = false;
      const rows = page.rows.map((row) => {
        if (row.rowKey !== rowKey) return row;
        changed = true;
        const authoritativeValue = currentRow
          && Object.prototype.hasOwnProperty.call(currentRow, column)
          ? currentRow[column]
          : oldValue;
        return {
          ...row,
          ...(currentRow ?? {}),
          rowKey,
          [column]: authoritativeValue,
        };
      });
      return changed ? { ...page, rows } : page;
    });
  }

  /**
   * Apply an inbound `table.rowsInserted` result. Appends the new row to the
   * last page's rows array in-place (mutating the existing page object to keep
   * reactivity); creates no new page.
   */
  function applyInsert(result: InsertRowResult): void {
    for (const page of pages.value) {
      const existing = (page.rows as Record<string, unknown>[]).find(
        (row) => row.rowKey === result.rowKey,
      );
      if (existing) {
        Object.assign(existing, result.row);
        adoptRevision(result.revision);
        return;
      }
    }
    const last = pages.value[pages.value.length - 1];
    if (last) {
      (last.rows as Record<string, unknown>[]).push(result.row);
    }
    adoptRevision(result.revision);
  }

  /**
   * Apply an inbound `table.rowsDeleted` result. Filters the deleted rowKeys
   * out of each page's rows IN-PLACE (clearing + repushing on the same array
   * reference) so Vue reactivity and Tabulator observe the change.
   */
  function applyDelete(result: DeleteRowsResult): void {
    const dead = new Set(result.deletedRowKeys);
    for (const page of pages.value) {
      const rows = page.rows as Record<string, unknown>[];
      const kept = rows.filter((r) => !dead.has(r.rowKey as number | string));
      rows.length = 0;
      rows.push(...kept);
    }
    adoptRevision(result.revision);
  }

  /**
   * Snapshot full row data (shallow-cloned dicts) for the given rowKeys. Used
   * by mutationService to cache undo state before a deleteRows request.
   */
  function snapshotRows(
    rowKeys: readonly (number | string)[],
  ): Record<string, unknown>[] {
    const want = new Set(rowKeys);
    const out: Record<string, unknown>[] = [];
    for (const page of pages.value) {
      for (const row of page.rows as Record<string, unknown>[]) {
        if (want.has(row.rowKey as number | string)) {
          out.push({ ...row });
        }
      }
    }
    return out;
  }

  /** Apply a committed relation value without routing it through scalar editing. */
  function applyRelationValue(
    rowKey: string | number,
    field: string,
    value: unknown,
  ): void {
    for (const page of pages.value) {
      const row = (page.rows as Record<string, unknown>[]).find((item) => item.rowKey === rowKey);
      if (row) row[field] = value;
    }
  }

  /**
   * Merge authoritative Lookup-query rows into the grid in server order.
   * Existing scalar values are retained only when the query projection omits
   * them; Lookup values are never derived from the visible page.
   */
  function applyLookupQueryResult(result: LookupQueryResult): void {
    const currentPage = pages.value[0];
    const currentSchema = schema.value;
    if (!currentPage || !currentSchema) return;
    const lookupDataRevision = result.snapshot?.dataRevision;
    const currentDataRevision = revision.value?.dataRevision;
    if (
      typeof lookupDataRevision === "number"
      && typeof currentDataRevision === "number"
      && lookupDataRevision < currentDataRevision
    ) {
      // A Lookup read can overlap an inline edit. Its request generation is
      // still current, but its QueryPort snapshot predates the committed
      // mutation. Ignore it so stale scalar columns cannot make the UI appear
      // to have reverted before undo/update confirmation actually arrives.
      return;
    }
    const previous = new Map(allRows.value.map((row) => [String(row.rowKey), row]));
    const fieldNames = new Map(currentSchema.flatMap((column) => [
      [column.fieldId ?? column.name, column.name] as const,
      [column.name, column.name] as const,
    ]));
    const rows: Record<string, unknown>[] = [];
    for (const wireRow of result.rows) {
      // The sidecar Lookup executor returns PocketBase-native query rows whose
      // stable key is `id`; the desktop table gateway uses the renderer alias
      // `rowKey`. Normalize both at this projection boundary. Never replace a
      // healthy editable dataset with keyless rows: JSON serialization would
      // omit an undefined rowKey and the host would correctly reject the next
      // mutation as a malformed payload.
      const rowKey = wireRow.rowKey ?? wireRow.id;
      if (typeof rowKey !== "string" && typeof rowKey !== "number") {
        error.value = LOOKUP_STABLE_KEY_ERROR;
        return;
      }
      const row: Record<string, unknown> = {
        ...(previous.get(String(rowKey)) ?? {}),
        rowKey,
      };
      for (const [wireField, value] of Object.entries(wireRow)) {
        if (wireField === "rowKey") continue;
        const field = fieldNames.get(wireField);
        if (field) row[field] = value;
      }
      rows.push(row);
    }
    if (error.value === LOOKUP_STABLE_KEY_ERROR) error.value = null;
    pages.value = [{
      ...currentPage,
      rows,
      offset: result.offset,
      limit: result.limit,
      totalRows: result.totalRows,
      filteredRows: result.filteredRows,
    }];
    rowCount.value = result.totalRows;
    lookupGroups.value = result.groups;
  }

  return {
    loading,
    datasetReady,
    pages,
    schema,
    rowCount,
    error,
    editSchema,
    revision,
    loadGeneration,
    lookupGroups,
    allRows,
    beginLoad,
    appendPage,
    finishPageLoad,
    setDatasetReady,
    setError,
    reset,
    setEditSchema,
    applyCellEdit,
    rollbackCellEdit,
    applyInsert,
    applyDelete,
    snapshotRows,
    applyRelationValue,
    applyLookupQueryResult,
  };
});
