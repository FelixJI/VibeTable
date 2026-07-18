import { useHostBridge } from "./bridgeContext";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useTableStore } from "@/stores/tableStore";
import { useHistoryStore } from "@/stores/historyStore";
import type {
  DatasetReadyPayload,
  EditSchemaResult,
  TablePage,
  TablePageLoadedPayload,
} from "@/contracts";

/**
 * tableService — wires inbound host events to `tableStore` and exposes the
 * outbound `table.selected` notify (selectTable / refresh).
 *
 * Inbound flow (host -> web):
 *   - `table.pageLoaded` (payload type `TablePageLoadedPayload`): the host emits
 *     one of these per incremental page during a client-mode multi-page fetch.
 *     The payload is FLATTENED — it carries `table`/`columns`/`rows`/`offset`/
 *     `limit`/`totalRows`/`mode` directly (there is NO `.page` field). It adds a
 *     `loadedRows` cumulative counter that the store does not need, so we
 *     project the payload onto a plain `TablePage` and append it.
 *   - `table.datasetReady` (payload type `DatasetReadyPayload`): emitted ONCE
 *     when the full client-mode dataset has loaded. `DatasetReadyPayload extends
 *     TablePage`, so the payload itself is the authoritative final page — we
 *     forward it directly to `tableStore.setDatasetReady`, which replaces the
 *     accumulated incremental pages with this single page (mirrors the legacy
 *     `desktop/web-grid/src/tableFlow.ts` replacement behavior, avoiding
 *     double-counted rows).
 *
 * Outbound flow (web -> host):
 *   - `table.selected` (notify, fire-and-forget): posted on selectTable/refresh.
 *
 * Call `init()` once at app boot to subscribe to inbound events.
 */
export function useTableService(): {
  init: () => void;
  selectTable: (name: string) => void;
  refresh: () => void;
} {
  const bridge = useHostBridge();
  const tableStore = useTableStore();
  const workspaceStore = useWorkspaceStore();
  const history = useHistoryStore();

  function init(): void {
    bridge.on("table.pageLoaded", (payload: TablePageLoadedPayload) => {
      // The pageLoaded payload is flattened (no `.page` field); project it onto
      // a `TablePage`, dropping the transport-only `loadedRows` counter.
      const page: TablePage = {
        table: payload.table,
        columns: payload.columns,
        rows: payload.rows,
        offset: payload.offset,
        limit: payload.limit,
        totalRows: payload.totalRows,
        mode: payload.mode,
        filteredRows: payload.filteredRows,
        querySnapshot: payload.querySnapshot,
        revision: payload.revision,
      };
      tableStore.appendPage(page);
    });
    bridge.on("table.datasetReady", (payload: DatasetReadyPayload) => {
      // DatasetReadyPayload extends TablePage — it IS the authoritative page.
      tableStore.setDatasetReady(payload);
    });
    bridge.on("table.editSchemaLoaded", (payload: EditSchemaResult) => {
      // EditSchemaResult only carries schemaRevision; the full MutationRevision
      // (with real databaseSessionId/dataRevision) arrives later via
      // datasetReady, whose handler overrides this placeholder revision.
      tableStore.setEditSchema(payload.columns, {
        databaseSessionId: "",
        schemaRevision: payload.schemaRevision,
        dataRevision: 0,
      });
    });
  }

  function selectTable(name: string): void {
    if (!name) return;
    workspaceStore.selectTable(name);
    tableStore.reset();
    // A table switch invalidates the undo stack: history entries reference
    // rowKeys / columns / schemaRevision that no longer apply. Spec §7.3.
    // Clearing here (not just in WorkspaceView.onSelect) covers every code
    // path that resets the table context — including any future caller.
    history.clear();
    tableStore.beginLoad();
    bridge.notify("table.selected", { table: name });
  }

  function refresh(): void {
    const current = workspaceStore.currentTable;
    if (!current) return;
    tableStore.reset();
    // Refresh re-fetches the full dataset; pending edits / undo entries are
    // no longer valid against the freshly loaded data.
    history.clear();
    tableStore.beginLoad();
    bridge.notify("table.selected", { table: current });
  }

  return { init, selectTable, refresh };
}
