import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { HostBridge } from "@/bridge/hostBridge";
import type {
  DataChangedEvent,
  DatasetReadyPayload,
  TaskChangedEvent,
} from "@/contracts";
import { setHostBridgeForTesting } from "./bridgeContext";
import { formatProductDataRevision, useTableService } from "./tableService";
import { useRealtimeStore } from "@/stores/realtimeStore";
import { useTableStore } from "@/stores/tableStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";

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
    mode: "client",
    loadedRows: 0,
    revision: {
      databaseSessionId: "pocketbase",
      schemaRevision: "schema_0007",
      dataRevision: revision,
    },
  };
}

function dataEvent(sequence: number): DataChangedEvent {
  return {
    contractVersion: "1.0",
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
    contractVersion: "1.0",
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
