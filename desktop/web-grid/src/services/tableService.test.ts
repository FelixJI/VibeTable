import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { flushPromises } from "@vue/test-utils";
import { nextTick, watch } from "vue";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import type {
  BridgeMessage,
  DataChangedEvent,
  DatasetReadyPayload,
  TablePage,
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
import { useViewQueryStore } from "@/stores/viewQueryStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import { useHistoryStore } from "@/stores/historyStore";

describe("tableService realtime product wiring", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("returns applied only after the correlated page becomes the complete dataset window", async () => {
    const harness = correlatedBridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    openWorkspaceEpoch(1);
    useWorkspaceStore().selectTable("orders");
    const table = useTableStore();
    const service = useTableService();
    service.init();
    try {
      const reloaded = service.reloadCurrentQuery();
      const request = harness.lastRequest();
      expect(request).toMatchObject({
        type: "table.queryRequested",
        payload: { table: "orders" },
      });

      harness.reply(request, {
        ...dataset(12),
        rows: [{ rowKey: "order-12", status: "paid" }],
        groupRows: [{ key: ["paid"], count: 1, summaries: [1] }],
        groupOffset: 0,
        hasMoreGroups: true,
        nextCursor: "cursor-13",
        hasMore: true,
      });

      await expect(reloaded).resolves.toBe("applied");
      expect(table.datasetReady).toBe(true);
      expect(table.allRows).toEqual([{ rowKey: "order-12", status: "paid" }]);
      expect(table.revision?.dataRevision).toBe(12);
      expect(table.viewGroups).toEqual([{ key: ["paid"], count: 1, summaries: [1] }]);
      expect(table.nextCursor).toBe("cursor-13");
      expect(table.hasMoreWindows).toBe(true);
    } finally {
      service.dispose();
      harness.stop();
    }
  });

  it("retires a same-tick query ABA before its old correlated page can replace the current window", async () => {
    const harness = correlatedBridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    openWorkspaceEpoch(1);
    const workspace = useWorkspaceStore();
    const query = useViewQueryStore();
    const table = useTableStore();
    workspace.selectTable("orders");
    table.setDatasetReady({ ...dataset(12), rows: [{ rowKey: "current" }] });
    const service = useTableService();
    service.init();
    try {
      const reloaded = service.reloadCurrentQuery();
      const request = harness.lastRequest();

      // `flush: "sync"` must retire the request even though the query returns
      // to its original semantic shape before Vue can schedule a normal tick.
      query.search = "paid";
      query.search = "";
      harness.reply(request, { ...dataset(13), rows: [{ rowKey: "stale" }] });

      await expect(reloaded).resolves.toBe("retired");
      expect(table.allRows).toEqual([{ rowKey: "current" }]);
      expect(table.error).toBeNull();
    } finally {
      service.dispose();
      harness.stop();
    }
  });

  it("retires a table-switch ABA and leaves the rebuilt current table untouched", async () => {
    const harness = correlatedBridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    openWorkspaceEpoch(1);
    const table = useTableStore();
    const service = useTableService();
    service.init();
    try {
      service.selectTable("orders");
      const reloaded = service.reloadCurrentQuery();
      const request = harness.lastRequest();

      service.selectTable("customers");
      service.selectTable("orders");
      table.setDatasetReady({ ...dataset(22), rows: [{ rowKey: "rebuilt" }] });
      harness.reply(request, { ...dataset(13), rows: [{ rowKey: "stale" }] });

      await expect(reloaded).resolves.toBe("retired");
      expect(table.allRows).toEqual([{ rowKey: "rebuilt" }]);
      expect(table.revision?.dataRevision).toBe(22);
      expect(table.error).toBeNull();
    } finally {
      service.dispose();
      harness.stop();
    }
  });

  it("retires late correlated pages across an epoch reset, refresh, and disposal", async () => {
    const harness = correlatedBridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    openWorkspaceEpoch(1);
    useWorkspaceStore().selectTable("orders");
    const table = useTableStore();
    table.setDatasetReady({ ...dataset(12), rows: [{ rowKey: "current" }] });
    const service = useTableService();
    service.init();
    try {
      const beforeEpoch = service.reloadCurrentQuery();
      const epochRequest = harness.lastRequest();
      openWorkspaceEpoch(2);
      table.setDatasetReady({ ...dataset(22), rows: [{ rowKey: "epoch-current" }] });
      harness.reply(epochRequest, { ...dataset(13), rows: [{ rowKey: "epoch-stale" }] });
      await expect(beforeEpoch).resolves.toBe("retired");
      expect(table.allRows).toEqual([{ rowKey: "epoch-current" }]);

      const beforeRefresh = service.reloadCurrentQuery();
      const refreshRequest = harness.lastRequest();
      service.refresh({ preserveHistory: true });
      table.setDatasetReady({ ...dataset(23), rows: [{ rowKey: "refresh-current" }] });
      harness.reply(refreshRequest, { ...dataset(14), rows: [{ rowKey: "refresh-stale" }] });
      await expect(beforeRefresh).resolves.toBe("retired");
      expect(table.allRows).toEqual([{ rowKey: "refresh-current" }]);

      const beforeOtherLoad = service.reloadCurrentQuery();
      const otherLoadRequest = harness.lastRequest();
      table.beginLoad();
      table.setDatasetReady({ ...dataset(24), rows: [{ rowKey: "other-load-current" }] });
      harness.reply(otherLoadRequest, { ...dataset(15), rows: [{ rowKey: "other-load-stale" }] });
      await expect(beforeOtherLoad).resolves.toBe("retired");
      expect(table.allRows).toEqual([{ rowKey: "other-load-current" }]);

      const beforeDispose = service.reloadCurrentQuery();
      const disposeRequest = harness.lastRequest();
      service.dispose();
      await expect(service.reloadCurrentQuery()).resolves.toBe("retired");
      expect(harness.lastRequest()).toBe(disposeRequest);
      table.setDatasetReady({ ...dataset(25), rows: [{ rowKey: "disposed-current" }] });
      harness.reply(disposeRequest, { ...dataset(16), rows: [{ rowKey: "disposed-stale" }] });
      await expect(beforeDispose).resolves.toBe("retired");
      expect(table.allRows).toEqual([{ rowKey: "disposed-current" }]);
      expect(table.error).toBeNull();
    } finally {
      service.dispose();
      harness.stop();
    }
  });

  it("returns failed for a rejected or below-floor correlated page without changing the current window", async () => {
    const harness = correlatedBridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    openWorkspaceEpoch(1);
    useWorkspaceStore().selectTable("orders");
    const table = useTableStore();
    table.setDatasetReady({ ...dataset(12), rows: [{ rowKey: "current" }] });
    const service = useTableService();
    service.init();
    try {
      const belowFloor = service.reloadCurrentQuery();
      harness.reply(harness.lastRequest(), { ...dataset(11), rows: [{ rowKey: "stale" }] });
      await expect(belowFloor).resolves.toBe("failed");
      expect(table.allRows).toEqual([{ rowKey: "current" }]);
      expect(table.revision?.dataRevision).toBe(12);

      const rejected = service.reloadCurrentQuery();
      harness.fail(harness.lastRequest(), "query transport failed");
      await expect(rejected).resolves.toBe("failed");
      expect(table.allRows).toEqual([{ rowKey: "current" }]);
      expect(table.error).toBeNull();
    } finally {
      service.dispose();
      harness.stop();
    }
  });

  it("drains a queued data change after accepting the correlated replacement window", async () => {
    const harness = correlatedBridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    openWorkspaceEpoch(1);
    const service = useTableService();
    service.init();
    try {
      service.selectTable("orders");
      harness.emit("data.changed", dataEvent(13));
      const reloaded = service.reloadCurrentQuery();
      harness.reply(harness.lastRequest(), dataset(12));

      await expect(reloaded).resolves.toBe("applied");
      const reconcile = harness.lastRequest();
      expect(reconcile).toMatchObject({
        type: "events.reconcile",
        payload: { tableId: "orders", dataRevision: "data_0012" },
      });
      harness.replyAs(reconcile, "events.reconcile", { action: "none" });
      await flushPromises();
      expect(useTableStore().datasetReady).toBe(true);
    } finally {
      service.dispose();
      harness.stop();
    }
  });

  it("does not report applied when completing the correlated load starts a deferred refresh", async () => {
    const harness = correlatedBridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    openWorkspaceEpoch(1);
    const service = useTableService();
    service.init();
    try {
      service.selectTable("orders");
      harness.emit("task.changed", taskEvent(13, "succeeded", 1));
      const reloaded = service.reloadCurrentQuery();
      harness.reply(harness.lastRequest(), dataset(12));

      await expect(reloaded).resolves.toBe("retired");
      expect(useTableStore().loading).toBe(true);
      expect(harness.notifications("table.selected")).toHaveLength(2);
    } finally {
      service.dispose();
      harness.stop();
    }
  });

  it("uses its single task tracker to project recovery activity while delivering every terminal receipt", async () => {
    const harness = correlatedBridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    openWorkspaceEpoch(1);
    const service = useTableService();
    const realtime = useRealtimeStore();
    const recovered = vi.fn();
    const terminalReceipts: string[] = [];
    const stopWatchingReceipts = watch(
      () => realtime.latestTask?.eventId,
      (eventId) => { if (eventId) terminalReceipts.push(eventId); },
    );
    service.init(recovered);
    try {
      harness.emit("realtime.recovered", {
        contractVersion: "2.0",
        topic: "realtime.recovered",
        activeFormulaTasks: [{
          taskId: "formula-resumed",
          state: "running",
          progress: 0.6,
          cursor: "0.6",
          error: null,
        }],
        terminalNotifications: [
          {
            ...taskEvent(31, "succeeded", 1),
            taskId: "formula-resumed",
            eventId: "terminal-resumed",
          },
          {
            ...taskEvent(32, "failed", 1),
            taskId: "formula-finished",
            eventId: "terminal-finished",
          },
        ],
      });
      await nextTick();
      await flushPromises();

      expect(recovered).toHaveBeenCalledTimes(1);
      expect(realtime.activeFormulaBackfill).toMatchObject({ taskId: "formula-resumed" });
      // The same recovery frame can report an old terminal receipt and a
      // current active projection for one task. Both facts reach the UI.
      expect(realtime.latestTask).toMatchObject({ taskId: "formula-finished", state: "failed" });
      expect(realtime.tasksById["formula-resumed"]?.eventId).toBe("terminal-resumed");
      expect(terminalReceipts).toEqual(["terminal-resumed", "terminal-finished"]);
      // Recovery terminal notifications are historical UI receipts: they do
      // not trigger the regular task-completion table refresh path.
      expect(harness.notifications("table.selected")).toHaveLength(0);

      harness.emit("task.changed", {
        ...taskEvent(32, "failed", 1),
        taskId: "formula-finished",
        eventId: "terminal-finished",
      });
      expect(realtime.latestTask).toMatchObject({ taskId: "formula-finished", eventId: "terminal-finished" });
      expect(harness.notifications("table.selected")).toHaveLength(0);
    } finally {
      stopWatchingReceipts();
      service.dispose();
      harness.stop();
    }
  });

  it("keeps a queued terminal receipt when a second same-epoch recovery restores that task as active", async () => {
    const harness = correlatedBridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    openWorkspaceEpoch(1);
    const service = useTableService();
    service.init();
    try {
      harness.emit("realtime.recovered", {
        contractVersion: "2.0", topic: "realtime.recovered", activeFormulaTasks: [],
        terminalNotifications: [{
          ...taskEvent(41, "succeeded", 1), taskId: "formula-resumed-later", eventId: "queued-terminal",
        }],
      });
      // The earlier terminal is already accepted by the one shared Tracker,
      // but its UI delivery has yielded. A second frame must not erase it.
      harness.emit("realtime.recovered", {
        contractVersion: "2.0", topic: "realtime.recovered",
        activeFormulaTasks: [{
          taskId: "formula-resumed-later", state: "running", progress: 0.4, cursor: "0.4", error: null,
        }],
        terminalNotifications: [],
      });
      await flushPromises();

      expect(useRealtimeStore().latestTask?.eventId).toBe("queued-terminal");
      expect(useRealtimeStore().activeFormulaBackfill?.taskId).toBe("formula-resumed-later");
    } finally {
      service.dispose();
      harness.stop();
    }
  });

  it("reports a malformed recovery snapshot without blocking the next valid realtime delivery", async () => {
    const harness = correlatedBridgeHarness();
    setHostBridgeForTesting(harness.bridge);
    openWorkspaceEpoch(1);
    const service = useTableService();
    service.init();
    try {
      harness.emit("realtime.recovered", { topic: "realtime.recovered", activeFormulaTasks: "invalid" });
      expect(useRealtimeStore().reconcileError).toBe("Realtime recovery snapshot was rejected.");

      harness.emit("realtime.recovered", {
        contractVersion: "2.0", topic: "realtime.recovered", activeFormulaTasks: [],
        terminalNotifications: [{ ...taskEvent(42, "succeeded", 1), eventId: "valid-after-reject" }],
      });
      await flushPromises();
      expect(useRealtimeStore().latestTask?.eventId).toBe("valid-after-reject");
    } finally {
      service.dispose();
      harness.stop();
    }
  });

  it.each(["finishes", "fails"] as const)("keeps the new workspace dataset when an old epoch reconcile %s", async (outcome) => {
    const harness = bridgeHarness({ action: "refresh-data" });
    const previousRead = deferred<{ action: "refresh-data" }>();
    harness.request.mockReturnValueOnce(previousRead.promise);
    setHostBridgeForTesting(harness.bridge);
    openWorkspaceEpoch(1);
    const table = useTableStore();
    const service = useTableService();
    service.init();
    try {
      service.selectTable("orders");
      harness.emit("table.datasetReady", dataset(11));
      harness.emit("data.changed", dataEvent(15));
      expect(harness.request).toHaveBeenCalledOnce();

      openWorkspaceEpoch(2, "22222222-2222-4222-8222-222222222222");
      service.selectTable("customers");
      harness.emit("table.datasetReady", dataset(22, "customers"));
      await flushPromises();
      expect(table.revision?.dataRevision).toBe(22);

      if (outcome === "fails") previousRead.reject(new Error("retired workspace read failed"));
      else previousRead.resolve({ action: "refresh-data" });
      await flushPromises();

      expect(table.revision?.dataRevision).toBe(22);
      expect(useRealtimeStore().lastInvalidation).toBeNull();
      expect(useRealtimeStore().reconcileError).toBeNull();
    } finally {
      service.dispose();
    }
  });

  it("does not publish a settled transport failure after its workspace epoch retires", async () => {
    const harness = bridgeHarness({ action: "none" });
    const retired = deferred<{ action: "none" }>();
    harness.request.mockReturnValueOnce(retired.promise);
    setHostBridgeForTesting(harness.bridge);
    openWorkspaceEpoch(1);
    const service = useTableService();
    service.init();
    try {
      service.selectTable("orders");
      harness.emit("table.datasetReady", dataset(11));
      harness.emit("data.changed", dataEvent(15));
      retired.reject(new Error("transport failed before epoch retirement"));
      await retired.promise.catch(() => undefined);
      openWorkspaceEpoch(2);
      await flushPromises();
      expect(useRealtimeStore().reconcileError).toBeNull();
      harness.request.mockRejectedValueOnce(new Error("current workspace read failed"));
      harness.emit("data.changed", dataEvent(16));
      await flushPromises();
      expect(useRealtimeStore().reconcileError).toBe("current workspace read failed");
    } finally {
      service.dispose();
    }
  });

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

  it("keeps the replayed request owned until its new-epoch read finishes", async () => {
    const harness = bridgeHarness({ action: "refresh-data" });
    const retired = deferred<{ action: "refresh-data" }>();
    const current = deferred<{ action: "refresh-data" }>();
    harness.request.mockReturnValueOnce(retired.promise).mockReturnValueOnce(current.promise);
    setHostBridgeForTesting(harness.bridge);
    openWorkspaceEpoch(1);
    const service = useTableService();
    service.init();
    try {
      service.selectTable("orders");
      harness.emit("table.datasetReady", dataset(11));
      harness.emit("data.changed", dataEvent(15));
      openWorkspaceEpoch(2);
      harness.emit("data.changed", dataEvent(15));
      expect(harness.request).toHaveBeenCalledTimes(2);

      retired.resolve({ action: "refresh-data" });
      await flushPromises();
      harness.emit("data.changed", dataEvent(15));
      expect(harness.request).toHaveBeenCalledTimes(2);

      current.resolve({ action: "refresh-data" });
      await flushPromises();
      expect(useRealtimeStore().lastInvalidation?.action).toBe("refresh-data");
    } finally {
      current.resolve({ action: "refresh-data" });
      service.dispose();
    }
  });

  it("rebuilds task display from durable replay after the epoch projection reset", () => {
    const harness = bridgeHarness({ action: "none" });
    setHostBridgeForTesting(harness.bridge);
    openWorkspaceEpoch(1);
    const service = useTableService();
    const realtime = useRealtimeStore();
    service.init();
    try {
      const running = taskEvent(20, "running", 0.42);
      harness.emit("task.changed", running);
      expect(realtime.activeFormulaBackfill?.progress).toBe(0.42);
      openWorkspaceEpoch(2);
      // WorkspaceView's epoch owner resets the displayed projections separately.
      realtime.reset();
      harness.emit("task.changed", {
        ...running, taskId: "import-1", taskType: "import", eventId: "import-event",
      });
      harness.emit("task.changed", running);
      expect(realtime.activeFormulaBackfill?.progress).toBe(0.42);
      expect(realtime.tasksById["import-1"]?.state).toBe("running");
    } finally {
      service.dispose();
    }
  });

  it.each([true, false])("restores remembered selection only within the same workspace (%s)", async (sameWorkspace) => {
    vi.useFakeTimers();
    const harness = bridgeHarness({ action: "refresh-data" });
    setHostBridgeForTesting(harness.bridge);
    openWorkspaceEpoch(1);
    const workspace = useWorkspaceStore();
    const table = useTableStore();
    const service = useTableService();
    service.init();
    try {
      service.selectTable("orders");
      harness.emit("data.changed", dataEvent(15));
      harness.emit("task.changed", taskEvent(24, "succeeded", 1));
      openWorkspaceEpoch(2, sameWorkspace ? undefined : "22222222-2222-4222-8222-222222222222");
      workspace.clear();
      table.reset();
      harness.notify.mockClear();
      await vi.advanceTimersByTimeAsync(3_001);
      expect(harness.notify).not.toHaveBeenCalled();

      harness.emit("database.opened", { tables: ["orders"], views: [] });
      expect(workspace.currentTable).toBe(sameWorkspace ? "orders" : null);
      if (sameWorkspace) {
        harness.emit("table.datasetReady", dataset(22));
        expect(table.revision?.dataRevision).toBe(22);
        expect(harness.notify).toHaveBeenCalledOnce();
      } else {
        expect(harness.notify).not.toHaveBeenCalled();
      }
      expect(harness.request).not.toHaveBeenCalled();
    } finally {
      service.dispose();
      vi.useRealTimers();
    }
  });

  it("retires disposed requests and selection before reinitializing in another workspace", async () => {
    const harness = bridgeHarness({ action: "none" });
    const retired = deferred<{ action: "none" }>();
    harness.request.mockReturnValueOnce(retired.promise);
    setHostBridgeForTesting(harness.bridge);
    openWorkspaceEpoch(1);
    const service = useTableService();
    const realtime = useRealtimeStore();
    service.init();
    try {
      service.selectTable("orders");
      harness.emit("table.datasetReady", dataset(11));
      harness.emit("data.changed", dataEvent(15));
      harness.emit("task.changed", { ...taskEvent(20, "running", 0.4), taskType: "import" });
      service.dispose();
      expect(realtime.activeTask?.taskType).toBe("import");
      openWorkspaceEpoch(2, "22222222-2222-4222-8222-222222222222");
      useWorkspaceStore().clear();
      useTableStore().reset();
      realtime.reset();
      service.init();
      retired.reject(new Error("disposed read failed"));
      await flushPromises();
      expect(realtime.reconcileError).toBeNull();
      harness.emit("database.opened", { tables: ["orders"], views: [] });
      expect(useWorkspaceStore().currentTable).toBeNull();
    } finally {
      service.dispose();
    }
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

  it("starts a fresh bounded watchdog cycle when database.opened follows an exhausted session", async () => {
    vi.useFakeTimers();
    try {
      const harness = bridgeHarness({ action: "none" });
      setHostBridgeForTesting(harness.bridge);
      const table = useTableStore();
      const service = useTableService();
      service.init();

      service.selectTable("orders");
      harness.notify.mockClear();
      for (let i = 0; i < 8; i += 1) {
        await vi.advanceTimersByTimeAsync(3_100);
      }
      expect(harness.notify).toHaveBeenCalledTimes(5);

      // A sidecar session recycle clears the projection before it re-announces
      // the catalog. The recovered load is a new bounded supervision cycle,
      // even though it is for the same table.
      table.reset();
      harness.notify.mockClear();
      harness.emit("database.opened", {
        tables: ["orders"],
        views: [],
        displayNames: {},
      });
      expect(harness.notify).toHaveBeenCalledWith("table.selected", { table: "orders" });

      harness.notify.mockClear();
      await vi.advanceTimersByTimeAsync(3_100);
      expect(harness.notify).toHaveBeenCalledTimes(1);

      for (let i = 0; i < 7; i += 1) {
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

  it("ignores a late datasetReady from a superseded table selection", () => {
    const harness = bridgeHarness({ action: "none" });
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const table = useTableStore();
    table.setDatasetReady(dataset(1));
    const service = useTableService();
    service.init();

    service.refresh({ preserveHistory: true });
    harness.notify.mockClear();
    // A higher data revision belonging to the PREVIOUS table must not raise
    // the current table's revision floor and poison subsequent pages.
    harness.emit("table.datasetReady", dataset(9, "customers"));
    harness.emit("table.datasetReady", dataset(1));

    expect(harness.notify).not.toHaveBeenCalledWith(
      "table.selected",
      { table: "orders" },
    );
    expect(table.error).toBe(null);
    expect(table.loading).toBe(false);
    service.dispose();
  });

  it("ignores editSchemaLoaded and windowLoaded from other tables", () => {
    const harness = bridgeHarness({ action: "none" });
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const table = useTableStore();
    table.setDatasetReady(dataset(1));
    const service = useTableService();
    service.init();
    const schemaBefore = table.editSchema;

    harness.emit("table.editSchemaLoaded", {
      table: "customers",
      schemaRevision: "schema_0009",
      rowKeyKind: "primary_key",
      rowKeyStable: true,
      editable: true,
      columns: [],
    });
    harness.emit("table.windowLoaded", dataset(1, "customers") as unknown as TablePage);

    expect(table.editSchema).toBe(schemaBefore);
    expect(table.error).toBe(null);
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

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

function openWorkspaceEpoch(
  sessionEpoch: number,
  workspaceId = "11111111-1111-4111-8111-111111111111",
): void {
  const session = useWorkspaceSessionStore();
  session.configureCapabilities(["workspace.session.v2"]);
  session.applySession({
    contractVersion: "2.0", workspaceId, sessionEpoch,
    state: "openedWritable", openMode: "writable",
    writable: true, provisional: false, phase: "idle", errorCode: null,
  });
}

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

function correlatedBridgeHarness() {
  const posted: BridgeMessage[] = [];
  const listeners = new Set<(event: { readonly data: unknown }) => void>();
  const bridge = createHostBridge({
    webview: {
      postMessage(message: unknown) {
        posted.push(message as BridgeMessage);
      },
      addEventListener(_type, listener) {
        listeners.add(listener);
      },
      removeEventListener(_type, listener) {
        listeners.delete(listener);
      },
    },
    generateRequestId: (() => {
      let sequence = 0;
      return () => `query-${++sequence}`;
    })(),
  });
  bridge.start();

  function emit(message: BridgeMessage): void {
    for (const listener of listeners) listener({ data: message });
  }

  function lastRequest(): BridgeMessage {
    const request = posted.at(-1);
    if (!request?.requestId) throw new Error("Expected a correlated bridge request.");
    return request;
  }

  return {
    bridge,
    lastRequest,
    emit(type: string, payload: unknown): void {
      emit({ type, payload });
    },
    reply(request: BridgeMessage, payload: TablePage): void {
      emit({ type: "table.pageLoaded", requestId: request.requestId, payload });
    },
    replyAs(request: BridgeMessage, type: string, payload: unknown): void {
      emit({ type, requestId: request.requestId, payload });
    },
    fail(request: BridgeMessage, message: string): void {
      emit({
        type: "operation.failed",
        requestId: request.requestId,
        payload: { message, operation: "table.queryRequested" },
      });
    },
    stop(): void {
      bridge.stop();
      setHostBridgeForTesting(null);
    },
    notifications(type: string): BridgeMessage[] {
      return posted.filter((message) => message.type === type && !message.requestId);
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
