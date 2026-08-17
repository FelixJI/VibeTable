import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { HostBridge } from "@/bridge/hostBridge";
import type {
  DataChangedEvent,
  DatasetReadyPayload,
  TaskChangedEvent,
} from "@/contracts";
import { setHostBridgeForTesting } from "./bridgeContext";
import {
  formatProductDataRevision,
  mergeDeferredRefreshOptions,
  useTableService,
} from "./tableService";
import { useRealtimeStore } from "@/stores/realtimeStore";
import { useTableStore } from "@/stores/tableStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useHistoryStore } from "@/stores/historyStore";

describe("tableService realtime product wiring", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("reconciles current-table data events and actively refreshes on a revision gap", async () => {
    const harness = bridgeHarness({ action: "refresh-data" });
    setHostBridgeForTesting(harness.bridge);
    const workspace = useWorkspaceStore();
    const table = useTableStore();
    workspace.selectTable("orders");
    table.setDatasetReady(dataset(11));
    const service = useTableService();
    service.init();

    harness.emit("data.changed", dataEvent(15));
    harness.emit("data.changed", dataEvent(15));
    await vi.waitFor(() => expect(harness.request).toHaveBeenCalledTimes(1));

    expect(harness.request).toHaveBeenCalledWith("events.reconcile", {
      tableId: "orders",
      schemaRevision: "schema_0007",
      dataRevision: "data_0011",
    });
    await vi.waitFor(() => expect(harness.notify).toHaveBeenCalledWith(
      "table.selected",
      { table: "orders" },
    ));
    expect(useRealtimeStore().lastInvalidation?.action).toBe("refresh-data");
    service.dispose();
  });

  it("defers reconciliation until the in-flight dataset has an authoritative revision", async () => {
    const harness = bridgeHarness({ action: "reload-schema" });
    setHostBridgeForTesting(harness.bridge);
    const workspace = useWorkspaceStore();
    const table = useTableStore();
    workspace.selectTable("orders");
    table.beginLoad();
    const service = useTableService();
    service.init();

    harness.emit("data.changed", dataEvent(16));
    expect(harness.request).not.toHaveBeenCalled();
    harness.emit("table.datasetReady", dataset(12));
    await vi.waitFor(() => expect(harness.request).toHaveBeenCalledWith(
      "events.reconcile",
      expect.objectContaining({ dataRevision: "data_0012" }),
    ));
    await vi.waitFor(() => expect(harness.notify).toHaveBeenCalledWith(
      "table.selected",
      { table: "orders" },
    ));
    service.dispose();
  });

  it("finishes a remote first-page load and consumes a queued data change", async () => {
    const harness = bridgeHarness({ action: "none" });
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const table = useTableStore();
    table.beginLoad();
    const service = useTableService();
    service.init();

    harness.emit("data.changed", dataEvent(16));
    harness.emit("table.pageLoaded", {
      ...dataset(12),
      mode: "remote",
      rows: [{ rowKey: 1 }],
      totalRows: 25_001,
    });

    expect(table.loading).toBe(false);
    expect(table.datasetReady).toBe(false);
    await vi.waitFor(() => expect(harness.request).toHaveBeenCalledWith(
      "events.reconcile",
      expect.objectContaining({ dataRevision: "data_0012" }),
    ));
    service.dispose();
  });

  it("requests and appends the next opaque cursor window", () => {
    const harness = bridgeHarness({ action: "none" });
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const table = useTableStore();
    const snapshot = querySnapshot(12);
    table.setDatasetReady({
      ...dataset(12),
      rows: [{ rowKey: 1 }],
      totalRows: 50_000,
      mode: "remote",
      querySnapshot: snapshot,
      nextCursor: "opaque-window-2",
      hasMore: true,
    });
    const service = useTableService();
    service.init();

    service.loadNextWindow();
    service.loadNextWindow();
    expect(harness.notify).toHaveBeenCalledTimes(1);
    expect(harness.notify).toHaveBeenCalledWith(
      "table.cursorRequested",
      { cursor: "opaque-window-2" },
    );

    harness.emit("table.windowLoaded", {
      ...dataset(12),
      rows: [{ rowKey: 2 }],
      totalRows: 50_000,
      mode: "remote",
      querySnapshot: snapshot,
      nextCursor: null,
      hasMore: false,
    });
    expect(table.allRows.map(row => row.rowKey)).toEqual([1, 2]);
    expect(table.windowLoading).toBe(false);
    expect(table.hasMoreWindows).toBe(false);
    service.dispose();
  });

  it("unlocks cursor loading after a classified cursor failure", () => {
    const harness = bridgeHarness({ action: "none" });
    setHostBridgeForTesting(harness.bridge);
    const table = useTableStore();
    table.setDatasetReady({
      ...dataset(12),
      querySnapshot: querySnapshot(12),
      nextCursor: "retry-cursor",
      hasMore: true,
    });
    const service = useTableService();
    service.init();

    service.loadNextWindow();
    expect(table.windowLoading).toBe(true);
    harness.emit("operation.failed", {
      message: "cursor expired",
      operation: "query.cursor",
    });
    expect(table.windowLoading).toBe(false);
    service.loadNextWindow();
    expect(harness.notify).toHaveBeenCalledTimes(2);
    service.dispose();
  });

  it("re-issues table.selected when a load dies inside a session recycle", async () => {
    vi.useFakeTimers();
    try {
      const harness = bridgeHarness({ action: "none" });
      setHostBridgeForTesting(harness.bridge);
      const table = useTableStore();
      const service = useTableService();
      service.init();

      service.selectTable("orders");
      // selectTable itself notifies once; the load never completes because the
      // backend pipeline failed during the sidecar session recycle.
      expect(harness.notify).toHaveBeenCalledWith(
        "table.selected",
        { table: "orders" },
      );
      expect(table.loading).toBe(true);
      harness.notify.mockClear();

      await vi.advanceTimersByTimeAsync(3_100);
      expect(harness.notify).toHaveBeenCalledWith(
        "table.selected",
        { table: "orders" },
      );
      expect(table.loading).toBe(true);

      // The recycled backend serves the re-issued load and the watchdog stops.
      harness.emit("table.datasetReady", dataset(12));
      expect(table.loading).toBe(false);
      harness.notify.mockClear();
      await vi.advanceTimersByTimeAsync(10_000);
      expect(harness.notify).not.toHaveBeenCalled();
      service.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  it("restores the last selected table when database.opened rebuilds the catalog", async () => {
    const harness = bridgeHarness({ action: "none" });
    setHostBridgeForTesting(harness.bridge);
    const workspace = useWorkspaceStore();
    const table = useTableStore();
    const service = useTableService();
    service.init();

    service.selectTable("orders");
    workspace.clear();
    table.reset();
    harness.notify.mockClear();

    harness.emit("database.opened", {
      tables: ["orders"],
      views: [],
      displayNames: {},
    });
    expect(harness.notify).toHaveBeenCalledWith(
      "table.selected",
      { table: "orders" },
    );
    service.dispose();
  });

  it("reselects the current table when a recycled session lost its dataset revision", () => {
    const harness = bridgeHarness({ action: "none" });
    setHostBridgeForTesting(harness.bridge);
    const table = useTableStore();
    const service = useTableService();
    service.init();

    service.selectTable("orders");
    harness.emit("table.datasetReady", dataset(12));
    expect(table.revision?.dataRevision).toBe(12);

    // A session recycle clears the dataset projection before the rebuilt
    // catalog arrives, but the user's selected table remains current.
    table.reset();
    harness.notify.mockClear();
    harness.emit("database.opened", {
      tables: ["orders"],
      views: [],
      displayNames: {},
    });

    expect(harness.notify).toHaveBeenCalledWith(
      "table.selected",
      { table: "orders" },
    );
    service.dispose();
  });

  it("does not restore a table that no longer exists after a recycle", async () => {
    const harness = bridgeHarness({ action: "none" });
    setHostBridgeForTesting(harness.bridge);
    const workspace = useWorkspaceStore();
    const table = useTableStore();
    const service = useTableService();
    service.init();

    service.selectTable("orders");
    workspace.clear();
    table.reset();
    harness.notify.mockClear();

    harness.emit("database.opened", {
      tables: ["other"],
      views: [],
      displayNames: {},
    });
    expect(harness.notify).not.toHaveBeenCalled();
    service.dispose();
  });

  it("stops re-issuing table.selected after the bounded retry budget", async () => {
    vi.useFakeTimers();
    try {
      const harness = bridgeHarness({ action: "none" });
      setHostBridgeForTesting(harness.bridge);
      useWorkspaceStore().selectTable("orders");
      const service = useTableService();
      service.init();

      service.selectTable("orders");
      harness.notify.mockClear();
      for (let i = 0; i < 8; i += 1) {
        await vi.advanceTimersByTimeAsync(3_100);
      }
      expect(harness.notify).toHaveBeenCalledTimes(5);
      service.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  it("reopens the current canonical query after query.cursor_stale", () => {
    const harness = bridgeHarness({ action: "none" });
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const table = useTableStore();
    table.setDatasetReady({
      ...dataset(12),
      rows: [{ rowKey: 1 }],
      querySnapshot: querySnapshot(12),
      nextCursor: "stale-cursor",
      hasMore: true,
    });
    const service = useTableService();
    service.init();
    service.loadNextWindow();
    harness.notify.mockClear();

    harness.emit("operation.failed", {
      message: "cursor revision changed",
      operation: "query.cursor",
      code: "query.cursor_stale",
    });

    expect(table.loading).toBe(true);
    expect(table.allRows).toEqual([{ rowKey: 1 }]);
    expect(harness.notify).toHaveBeenCalledWith("table.queryRequested", {
      table: "orders",
      query: expect.objectContaining({
        filters: [],
        sorts: [],
        offset: 0,
        limit: 500,
      }),
    });
    service.dispose();
  });

  it("retries when a same-schema refresh completes below the committed revision floor", () => {
    const harness = bridgeHarness({ action: "none" });
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const table = useTableStore();
    table.setDatasetReady(dataset(12));
    const service = useTableService();
    service.init();

    service.refresh({ preserveHistory: true });
    harness.notify.mockClear();
    harness.emit("table.datasetReady", dataset(11));

    expect(table.loading).toBe(true);
    expect(harness.notify).toHaveBeenCalledWith(
      "table.selected",
      { table: "orders" },
    );
    service.dispose();
  });

  it("fails closed after bounded stale-snapshot retries", () => {
    const harness = bridgeHarness({ action: "none" });
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const table = useTableStore();
    table.setDatasetReady(dataset(12));
    const service = useTableService();
    service.init();

    service.refresh({ preserveHistory: true });
    harness.notify.mockClear();
    for (let attempt = 0; attempt < 4; attempt += 1) {
      harness.emit("table.datasetReady", dataset(11));
    }

    expect(harness.notify).toHaveBeenCalledTimes(3);
    expect(table.loading).toBe(false);
    expect(table.error).toContain("older snapshot");
    service.dispose();
  });

  it("shows monotonic backfill progress and refreshes once on its terminal snapshot", async () => {
    const harness = bridgeHarness({ action: "none" });
    setHostBridgeForTesting(harness.bridge);
    const workspace = useWorkspaceStore();
    const table = useTableStore();
    workspace.selectTable("orders");
    table.setDatasetReady(dataset(11));
    const service = useTableService();
    service.init();

    harness.emit("task.changed", taskEvent(20, "running", 0.42));
    harness.emit("task.changed", taskEvent(20, "running", 0.42));
    harness.emit("task.changed", taskEvent(19, "running", 0.3));
    expect(useRealtimeStore().activeTask?.progress).toBe(0.42);

    harness.emit("task.changed", taskEvent(24, "succeeded", 1));
    expect(useRealtimeStore().activeTask).toBeNull();
    expect(useRealtimeStore().latestTask?.state).toBe("succeeded");
    expect(harness.notify).toHaveBeenCalledTimes(1);
    expect(harness.notify).toHaveBeenCalledWith("table.selected", { table: "orders" });
    service.dispose();
  });

  it("ignores data changes for a table that is not open", () => {
    const harness = bridgeHarness({ action: "refresh-data" });
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("customers");
    useTableStore().setDatasetReady(dataset(11, "customers"));
    const service = useTableService();
    service.init();
    harness.emit("data.changed", dataEvent(15));
    expect(harness.request).not.toHaveBeenCalled();
    service.dispose();
  });

  it("formats the frozen PocketBase data revision identity", () => {
    expect(formatProductDataRevision(7)).toBe("data_0007");
    expect(formatProductDataRevision(12_345)).toBe("data_12345");
    expect(() => formatProductDataRevision(-1)).toThrow("Invalid product data revision");
  });

  it("preserves guarded data history only for same-schema background refreshes", () => {
    const harness = bridgeHarness({ action: "none" });
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const table = useTableStore();
    table.setDatasetReady(dataset(11));
    const history = useHistoryStore();
    table.setEditSchema([{
      name: "name",
      storageName: "name",
      dataType: "text",
      editable: true,
      nullable: true,
      primaryKey: false,
      editor: { kind: "text" },
      validation: [],
    }], {
      databaseSessionId: "pocketbase",
      schemaRevision: "schema_0007",
      dataRevision: 11,
    });
    const entry = {
      id: "edit-1",
      kind: "updateCell" as const,
      label: "edit",
      timestamp: 1,
      undo: async () => {},
    };
    history.push(entry);
    const service = useTableService();

    service.refresh({ preserveHistory: true });
    expect(history.undoStackSize).toBe(1);
    expect(table.editSchema?.[0]?.name).toBe("name");

    history.push({ ...entry, id: "edit-2" });
    service.refresh();
    expect(history.undoStackSize).toBe(0);
    expect(table.editSchema).toBeNull();
  });

  it("never lets a later background refresh weaken a queued schema reload", () => {
    const reload = mergeDeferredRefreshOptions(null, {
      preserveHistory: false,
    });
    expect(mergeDeferredRefreshOptions(reload, {
      preserveHistory: true,
    })).toEqual({ preserveHistory: false });
    expect(mergeDeferredRefreshOptions(
      { preserveHistory: true },
      { preserveHistory: true },
    )).toEqual({ preserveHistory: true });
  });
});

function bridgeHarness(reconcileResult: { action: "none" | "refresh-data" | "reload-schema" }) {
  const handlers = new Map<string, Set<(payload: unknown) => void>>();
  const request = vi.fn(async () => reconcileResult);
  const notify = vi.fn();
  const bridge = {
    request,
    notify,
    on(type: string, handler: (payload: unknown) => void) {
      const listeners = handlers.get(type) ?? new Set();
      listeners.add(handler);
      handlers.set(type, listeners);
      return () => listeners.delete(handler);
    },
  } as unknown as HostBridge;
  return {
    bridge,
    request,
    notify,
    emit(type: string, payload: unknown) {
      for (const handler of handlers.get(type) ?? []) handler(payload);
    },
  };
}

function dataset(revision: number, table = "orders"): DatasetReadyPayload {
  return {
    table,
    columns: [],
    rows: [],
    offset: 0,
    limit: 100,
    totalRows: 0,
    mode: "remote",
    revision: {
      databaseSessionId: "pocketbase",
      schemaRevision: "schema_0007",
      dataRevision: revision,
    },
  };
}

function querySnapshot(dataRevision: number) {
  return {
    snapshotId: `snapshot-${dataRevision}`,
    digest: `sha256:${"a".repeat(64)}`,
    databaseId: "database-1",
    table: "orders",
    schemaRevision: "schema_0007",
    dataRevision,
    normalizedQuery: {},
  };
}

function dataEvent(sequence: number): DataChangedEvent {
  return {
    contractVersion: "2.0",
    topic: "data.changed",
    eventId: `evt-${sequence}`,
    sequence,
    occurredAt: "2026-07-24T08:30:00Z",
    schemaRevision: "schema_0007",
    dataRevision: `data_${String(sequence).padStart(4, "0")}`,
    changeSetId: `change-${sequence}`,
    tableId: "orders",
    recordIds: ["order-1"],
    operation: "update",
  };
}

function taskEvent(
  sequence: number,
  state: TaskChangedEvent["state"],
  progress: number,
): TaskChangedEvent {
  return {
    contractVersion: "2.0",
    topic: "task.changed",
    eventId: `task-${sequence}`,
    sequence,
    occurredAt: "2026-07-24T08:30:00Z",
    taskId: "formula-orders",
    taskType: "formulaBackfill",
    state,
    progress,
    cursor: String(progress),
    error: null,
  };
}
