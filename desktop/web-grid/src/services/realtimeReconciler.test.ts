import { describe, expect, it, vi } from "vitest";
import type { DataChangedEvent, TaskChangedEvent } from "@/contracts";
import { RealtimeReconciler, RealtimeTaskTracker } from "./realtimeReconciler";

describe("RealtimeReconciler", () => {
  it("deduplicates events and refreshes data after authoritative reconcile", async () => {
    const reconcile = vi.fn(async () => ({ action: "refresh-data" as const }));
    const refreshData = vi.fn();
    const reloader = vi.fn();
    const service = new RealtimeReconciler({ reconcile }, { refreshData, reloadSchema: reloader });

    await service.handle(event(12), "schema_0007", "data_0011");
    await service.handle(event(12), "schema_0007", "data_0011");

    expect(reconcile).toHaveBeenCalledTimes(1);
    expect(refreshData).toHaveBeenCalledTimes(1);
    expect(reloader).not.toHaveBeenCalled();
  });

  it("coalesces concurrent delivery of the same event", async () => {
    let resolve!: (value: { action: "refresh-data" }) => void;
    const reconcile = vi.fn(() => new Promise<{ action: "refresh-data" }>((done) => {
      resolve = done;
    }));
    const actions = { refreshData: vi.fn(), reloadSchema: vi.fn() };
    const service = new RealtimeReconciler({ reconcile }, actions);

    const first = service.handle(event(12), "schema_0007", "data_0011");
    const duplicate = service.handle(event(12), "schema_0007", "data_0011");
    expect(reconcile).toHaveBeenCalledTimes(1);
    resolve({ action: "refresh-data" });
    await Promise.all([first, duplicate]);

    expect(actions.refreshData).toHaveBeenCalledTimes(1);
  });

  it("allows a failed reconciliation event to be retried", async () => {
    const reconcile = vi.fn()
      .mockRejectedValueOnce(new Error("sidecar restarting"))
      .mockResolvedValueOnce({ action: "refresh-data" as const });
    const actions = { refreshData: vi.fn(), reloadSchema: vi.fn() };
    const service = new RealtimeReconciler({ reconcile }, actions);

    await expect(service.handle(event(16), "schema_0007", "data_0015"))
      .rejects.toThrow("sidecar restarting");
    await service.handle(event(16), "schema_0007", "data_0015");

    expect(reconcile).toHaveBeenCalledTimes(2);
    expect(actions.refreshData).toHaveBeenCalledTimes(1);
  });

  it("reloads schema when schema revision changed", async () => {
    const service = new RealtimeReconciler(
      { reconcile: vi.fn(async () => ({ action: "reload-schema" as const })) },
      { refreshData: vi.fn(), reloadSchema: vi.fn() },
    );
    await service.handle(event(13), "schema_0007", "data_0012");
    expect(service.actions.reloadSchema).toHaveBeenCalledTimes(1);
  });

  it("suppresses an older reconcile response", async () => {
    let firstResolve!: (value: { action: "reload-schema" }) => void;
    const first = new Promise<{ action: "reload-schema" }>((resolve) => {
      firstResolve = resolve;
    });
    const reconcile = vi.fn()
      .mockImplementationOnce(() => first)
      .mockResolvedValueOnce({ action: "refresh-data" as const });
    const actions = { refreshData: vi.fn(), reloadSchema: vi.fn() };
    const service = new RealtimeReconciler({ reconcile }, actions);

    const old = service.handle(event(14), "schema_0007", "data_0013");
    await service.handle(event(15), "schema_0007", "data_0014");
    firstResolve({ action: "reload-schema" });
    await old;

    expect(actions.refreshData).toHaveBeenCalledTimes(1);
    expect(actions.reloadSchema).not.toHaveBeenCalled();
  });
});

describe("RealtimeTaskTracker", () => {
  it("accepts only one copy and rejects stale task snapshots", () => {
    const tracker = new RealtimeTaskTracker();
    expect(tracker.accept(taskEvent(20, 0.25))).toBe(true);
    expect(tracker.accept(taskEvent(20, 0.25))).toBe(false);
    expect(tracker.accept(taskEvent(19, 0.2))).toBe(false);
    expect(tracker.accept(taskEvent(22, 0.75))).toBe(true);
    expect(tracker.accept(taskEvent(1, 0.8, "2026-07-24T08:31:00Z"))).toBe(true);
  });
});

function event(sequence: number): DataChangedEvent {
  return {
    contractVersion: "2.0",
    topic: "data.changed",
    eventId: `evt_${sequence}`,
    sequence,
    occurredAt: "2026-07-24T08:30:00Z",
    schemaRevision: "schema_0007",
    dataRevision: `data_${String(sequence).padStart(4, "0")}`,
    changeSetId: `chg_${sequence}`,
    tableId: "tbl_orders",
    recordIds: ["rec_1"],
    operation: "update",
  };
}

function taskEvent(
  sequence: number,
  progress: number,
  occurredAt = "2026-07-24T08:30:00Z",
): TaskChangedEvent {
  return {
    contractVersion: "2.0",
    topic: "task.changed",
    eventId: `task_evt_${sequence}`,
    sequence,
    occurredAt,
    taskId: "formula-orders",
    taskType: "formulaBackfill",
    state: "running",
    progress,
    cursor: String(progress),
    error: null,
  };
}
