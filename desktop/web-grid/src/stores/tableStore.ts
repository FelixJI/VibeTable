import { defineStore } from "pinia";
import { computed, ref } from "vue";
import type {
  ColumnSchema,
  DatasetReadyPayload,
  TablePage,
} from "@/contracts";

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

  /** All accumulated rows across pages, flattened in order. */
  const allRows = computed<ReadonlyArray<Record<string, unknown>>>(() =>
    pages.value.flatMap((p) => p.rows),
  );

  /** Begin a load: clear stale state and mark loading. */
  function beginLoad(): void {
    loading.value = true;
    datasetReady.value = false;
    pages.value = [];
    schema.value = null;
    rowCount.value = 0;
    error.value = null;
  }

  /**
   * Accumulate one incremental page from `table.pageLoaded`. Pages are pushed
   * in arrival order; `allRows` flattens them. Also learn the schema/count from
   * each page so the grid can render incrementally before datasetReady.
   */
  function appendPage(page: TablePage): void {
    pages.value.push(page);
    // Adopt the page's schema/count if present so the grid can render during
    // the incremental load (host advertises the same schema/count per page).
    if (page.columns.length > 0) {
      schema.value = page.columns;
    }
    rowCount.value = page.totalRows;
  }

  /**
   * Handle `table.datasetReady`. The payload extends `TablePage` and carries
   * the authoritative full dataset; accumulated incremental pages are replaced
   * by this single page to avoid double-counting rows. Stores the schema, final
   * row count, ends loading, and marks the dataset ready.
   */
  function setDatasetReady(payload: DatasetReadyPayload): void {
    pages.value = [payload];
    schema.value = payload.columns;
    rowCount.value = payload.totalRows;
    datasetReady.value = true;
    loading.value = false;
    error.value = null;
  }

  /** Record an error and end loading. */
  function setError(message: string): void {
    error.value = message;
    loading.value = false;
  }

  /** Reset all state to idle (used when switching tables/databases). */
  function reset(): void {
    loading.value = false;
    datasetReady.value = false;
    pages.value = [];
    schema.value = null;
    rowCount.value = 0;
    error.value = null;
  }

  return {
    loading,
    datasetReady,
    pages,
    schema,
    rowCount,
    error,
    allRows,
    beginLoad,
    appendPage,
    setDatasetReady,
    setError,
    reset,
  };
});
