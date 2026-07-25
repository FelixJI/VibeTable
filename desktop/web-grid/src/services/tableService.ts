import { useHostBridge } from "./bridgeContext";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useTableStore } from "@/stores/tableStore";
import { useHistoryStore } from "@/stores/historyStore";
import { useRealtimeStore } from "@/stores/realtimeStore";
import type {
  DataChangedEvent,
  DatasetReadyPayload,
  EditSchemaResult,
  TablePage,
  TablePageLoadedPayload,
  TaskChangedEvent,
} from "@/contracts";
import {
  createBridgeRealtimeReconcilePort,
  RealtimeReconciler,
  RealtimeTaskTracker,
} from "./realtimeReconciler";

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
  dispose: () => void;
  selectTable: (name: string) => void;
  refresh: () => void;
} {
  const bridge = useHostBridge();
  const tableStore = useTableStore();
  const workspaceStore = useWorkspaceStore();
  const history = useHistoryStore();
  const realtimeStore = useRealtimeStore();
  const taskTracker = new RealtimeTaskTracker();
  const unsubscribe: Array<() => void> = [];
  let initialized = false;
  let pendingDataChange: DataChangedEvent | null = null;
  let refreshAfterLoad = false;
  const realtime = new RealtimeReconciler(
    createBridgeRealtimeReconcilePort(bridge),
    {
      refreshData: () => invalidateAndRefresh("refresh-data"),
      reloadSchema: () => invalidateAndRefresh("reload-schema"),
    },
  );

  function init(): void {
    if (initialized) return;
    initialized = true;
    unsubscribe.push(bridge.on("table.pageLoaded", (payload: TablePageLoadedPayload) => {
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
    }));
    unsubscribe.push(bridge.on("table.datasetReady", (payload: DatasetReadyPayload) => {
      // DatasetReadyPayload extends TablePage — it IS the authoritative page.
      tableStore.setDatasetReady(payload);
      if (refreshAfterLoad) {
        refreshAfterLoad = false;
        refresh();
        return;
      }
      const pending = pendingDataChange;
      pendingDataChange = null;
      if (pending) reconcileDataChange(pending);
    }));
    unsubscribe.push(bridge.on("table.editSchemaLoaded", (payload: EditSchemaResult) => {
      // EditSchemaResult only carries schemaRevision; the full MutationRevision
      // (with real databaseSessionId/dataRevision) arrives later via
      // datasetReady, whose handler overrides this placeholder revision.
      tableStore.setEditSchema(payload.columns, {
        databaseSessionId: "",
        schemaRevision: payload.schemaRevision,
        dataRevision: 0,
      });
    }));
    unsubscribe.push(bridge.on("data.changed", (payload) => {
      if (payload.tableId !== workspaceStore.currentTable) return;
      if (tableStore.loading || !tableStore.revision) {
        if (
          !pendingDataChange
          || payload.occurredAt > pendingDataChange.occurredAt
          || (payload.occurredAt === pendingDataChange.occurredAt
            && payload.sequence > pendingDataChange.sequence)
        ) {
          pendingDataChange = payload;
        }
        return;
      }
      reconcileDataChange(payload);
    }));
    unsubscribe.push(bridge.on("task.changed", applyTaskChange));
  }

  function dispose(): void {
    for (const stop of unsubscribe.splice(0)) stop();
    initialized = false;
    pendingDataChange = null;
    refreshAfterLoad = false;
  }

  function selectTable(name: string): void {
    if (!name) return;
    pendingDataChange = null;
    refreshAfterLoad = false;
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

  function reconcileDataChange(event: DataChangedEvent): void {
    const revision = tableStore.revision;
    if (!revision || event.tableId !== workspaceStore.currentTable) return;
    void realtime.handle(
      event,
      revision.schemaRevision,
      formatProductDataRevision(revision.dataRevision),
    ).catch((error: unknown) => realtimeStore.failReconcile(error));
  }

  function invalidateAndRefresh(action: "refresh-data" | "reload-schema"): void {
    realtimeStore.markInvalidated(action);
    if (tableStore.loading) {
      refreshAfterLoad = true;
      return;
    }
    refresh();
  }

  function applyTaskChange(event: TaskChangedEvent): void {
    if (!taskTracker.accept(event)) return;
    realtimeStore.applyTask(event);
    if (
      event.taskType !== "formulaBackfill"
      || (event.state !== "succeeded"
        && event.state !== "failed"
        && event.state !== "cancelled")
    ) return;
    if (tableStore.loading) {
      refreshAfterLoad = true;
    } else {
      refresh();
    }
  }

  return { init, dispose, selectTable, refresh };
}

/** Convert the desktop mutation revision to the frozen PocketBase revision ID. */
export function formatProductDataRevision(revision: number): string {
  if (!Number.isSafeInteger(revision) || revision < 0) {
    throw new Error("Invalid product data revision.");
  }
  return `data_${String(revision).padStart(4, "0")}`;
}
