import { useHostBridge } from "./bridgeContext";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useTableStore } from "@/stores/tableStore";
import { useHistoryStore } from "@/stores/historyStore";
import { useRealtimeStore } from "@/stores/realtimeStore";
import { useViewQueryStore } from "@/stores/viewQueryStore";
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
 *   - `table.pageLoaded` carries one completed bounded query window.
 *   - `table.datasetReady` carries the initial revision-bound window.
 *   - `table.windowLoaded` appends a bounded cursor window after an explicit
 *     scroll-boundary request.
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
  refresh: (options?: TableRefreshOptions) => void;
  loadNextWindow: () => void;
} {
  const bridge = useHostBridge();
  const tableStore = useTableStore();
  const workspaceStore = useWorkspaceStore();
  const history = useHistoryStore();
  const realtimeStore = useRealtimeStore();
  const viewQueryStore = useViewQueryStore();
  const taskTracker = new RealtimeTaskTracker();
  const unsubscribe: Array<() => void> = [];
  let initialized = false;
  let pendingDataChange: DataChangedEvent | null = null;
  let refreshAfterLoad: TableRefreshOptions | null = null;
  let staleSnapshotRetries = 0;
  const maxStaleSnapshotRetries = 3;
  // `table.selected` is a fire-and-forget notify whose failure path posts
  // `operation.failed` without any load-scoped handler: a load that dies
  // inside a sidecar-crash session recycle would leave the grid in a
  // permanent loading state with no retry. Supervise the notify instead.
  let loadWatchdogTimer: ReturnType<typeof setTimeout> | null = null;
  let loadWatchdogTable: string | null = null;
  let loadWatchdogRetries = 0;
  const loadWatchdogTimeoutMs = 3_000;
  const maxLoadWatchdogRetries = 5;
  const realtime = new RealtimeReconciler(
    createBridgeRealtimeReconcilePort(bridge),
    {
      refreshData: () => invalidateAndRefresh("refresh-data"),
      reloadSchema: () => invalidateAndRefresh("reload-schema"),
    },
  );

  function disarmLoadWatchdog(): void {
    if (loadWatchdogTimer !== null) {
      clearTimeout(loadWatchdogTimer);
      loadWatchdogTimer = null;
    }
    loadWatchdogTable = null;
    loadWatchdogRetries = 0;
  }

  function armLoadWatchdog(table: string): void {
    if (loadWatchdogTimer !== null) clearTimeout(loadWatchdogTimer);
    if (loadWatchdogTable !== table) loadWatchdogRetries = 0;
    loadWatchdogTable = table;
    loadWatchdogTimer = setTimeout(() => {
      loadWatchdogTimer = null;
      const supervised = loadWatchdogTable;
      if (
        supervised === null
        || !tableStore.loading
        || workspaceStore.currentTable !== supervised
        || loadWatchdogRetries >= maxLoadWatchdogRetries
      ) {
        return;
      }
      loadWatchdogRetries += 1;
      bridge.notify("table.selected", { table: supervised });
      armLoadWatchdog(supervised);
    }, loadWatchdogTimeoutMs);
  }

  function completeLoad(): void {
    disarmLoadWatchdog();
    if (refreshAfterLoad) {
      const options = refreshAfterLoad;
      refreshAfterLoad = null;
      refresh(options);
      return;
    }
    const pending = pendingDataChange;
    pendingDataChange = null;
    if (pending) reconcileDataChange(pending);
  }

  function retryStaleSnapshot(): void {
    if (staleSnapshotRetries >= maxStaleSnapshotRetries) {
      tableStore.setError(
        "The data source kept returning an older snapshot. Refresh and try again.",
      );
      return;
    }
    staleSnapshotRetries += 1;
    refresh({ preserveHistory: true });
  }

  function init(): void {
    if (initialized) return;
    initialized = true;
    unsubscribe.push(bridge.on("table.pageLoaded", (payload: TablePageLoadedPayload) => {
      const accepted = tableStore.appendPage(payload);
      if (!accepted) {
        retryStaleSnapshot();
        return;
      }
      staleSnapshotRetries = 0;
      completeLoad();
    }));
    unsubscribe.push(bridge.on("table.datasetReady", (payload: DatasetReadyPayload) => {
      // DatasetReadyPayload extends TablePage — it IS the authoritative page.
      if (!tableStore.setDatasetReady(payload)) {
        retryStaleSnapshot();
        return;
      }
      staleSnapshotRetries = 0;
      completeLoad();
    }));
    unsubscribe.push(bridge.on("table.windowLoaded", (payload: TablePage) => {
      if (!tableStore.appendWindow(payload)) {
        retryStaleSnapshot();
      }
    }));
    unsubscribe.push(bridge.on("operation.failed", (payload) => {
      if (payload.operation !== "query.cursor") return;
      tableStore.cancelNextWindow();
      if (payload.code !== "query.cursor_stale") return;
      const table = workspaceStore.currentTable;
      if (!table) return;
      tableStore.beginCursorReopen();
      bridge.notify("table.queryRequested", {
        table,
        query: viewQueryStore.toQuery(),
      });
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
    refreshAfterLoad = null;
    staleSnapshotRetries = 0;
    disarmLoadWatchdog();
  }

  function selectTable(name: string): void {
    if (!name) return;
    pendingDataChange = null;
    refreshAfterLoad = null;
    staleSnapshotRetries = 0;
    workspaceStore.selectTable(name);
    tableStore.reset();
    // A table switch invalidates the undo stack: history entries reference
    // rowKeys / columns / schemaRevision that no longer apply. Spec §7.3.
    // Clearing here (not just in WorkspaceView.onSelect) covers every code
    // path that resets the table context — including any future caller.
    history.clear();
    tableStore.beginLoad();
    bridge.notify("table.selected", { table: name });
    armLoadWatchdog(name);
  }

  function refresh(options: TableRefreshOptions = {}): void {
    const current = workspaceStore.currentTable;
    if (!current) return;
    if (!options.preserveHistory) staleSnapshotRetries = 0;
    tableStore.reset({
      // `preserveHistory` is reserved for data-only reconciliation where the
      // schema revision is known not to have changed. Keeping the matching
      // edit schema prevents an SSE event that races its own mutation result
      // from temporarily (or, after a superseded host fetch, permanently)
      // rebuilding the grid as read-only.
      preserveEditSchema: options.preserveHistory === true,
    });
    // Explicit refreshes and schema reloads invalidate data history. A
    // same-schema background refresh (realtime/Lookup) preserves it: every
    // inverse write remains protected by schemaRevision and row digest, so an
    // external conflicting change will be rejected rather than overwritten.
    // Without this distinction, the data.changed event emitted for the user's
    // own edit clears that edit before Ctrl+Z can act on it.
    if (!options.preserveHistory) history.clear();
    tableStore.beginLoad();
    bridge.notify("table.selected", { table: current });
    armLoadWatchdog(current);
  }

  function loadNextWindow(): void {
    const cursor = tableStore.beginNextWindow();
    if (!cursor) return;
    bridge.notify("table.cursorRequested", { cursor });
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
      refreshAfterLoad = mergeDeferredRefreshOptions(refreshAfterLoad, {
        preserveHistory: action === "refresh-data",
      });
      return;
    }
    refresh({ preserveHistory: action === "refresh-data" });
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
      refreshAfterLoad = mergeDeferredRefreshOptions(refreshAfterLoad, {
        preserveHistory: true,
      });
    } else {
      refresh({ preserveHistory: true });
    }
  }

  return { init, dispose, selectTable, refresh, loadNextWindow };
}

export interface TableRefreshOptions {
  readonly preserveHistory?: boolean;
}

/**
 * Merge refreshes queued during one load. Clearing history is the stricter
 * policy and is therefore monotonic: a later data-only refresh must never
 * weaken an already queued schema reload.
 */
export function mergeDeferredRefreshOptions(
  current: TableRefreshOptions | null,
  next: TableRefreshOptions,
): TableRefreshOptions {
  if (!current) return { preserveHistory: next.preserveHistory === true };
  return {
    preserveHistory:
      current.preserveHistory === true && next.preserveHistory === true,
  };
}

/** Convert the desktop mutation revision to the frozen PocketBase revision ID. */
export function formatProductDataRevision(revision: number): string {
  if (!Number.isSafeInteger(revision) || revision < 0) {
    throw new Error("Invalid product data revision.");
  }
  return `data_${String(revision).padStart(4, "0")}`;
}
