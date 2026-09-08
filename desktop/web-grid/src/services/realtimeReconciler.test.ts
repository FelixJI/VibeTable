import { describe, expect, it, vi } from "vitest";
import recoveryFixtureJson from "../../../../contracts/v2/fixtures/realtime-recovery-event.json";
import type { DataChangedEvent, FormulaTaskState, TaskChangedEvent } from "@/contracts";
import {
  MAX_RECOVERY_ACTIVE_FORMULA_TASKS,
  RealtimeReconciler,
  RealtimeTaskTracker,
} from "./realtimeReconciler";

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

  it("batch-delivers retained terminals without overwriting a resumed formula projection", () => {
    const tracker = new RealtimeTaskTracker();
    const frame = recoveryFixture();

    const delivery = tracker.acceptRecovery(frame);

    expect(delivery.activeFormulaTasks).toEqual([
      expect.objectContaining({ taskId: "formula-resumed", state: "running", progress: 0.5 }),
    ]);
    expect(delivery.terminalNotifications.map((event) => event.eventId)).toEqual([
      "evt_formula_cancelled_before_resume",
      "evt_formula_succeeded",
    ]);
    expect(delivery.terminalNotifications[0]?.taskId).toBe("formula-resumed");
    expect(tracker.accept(delivery.terminalNotifications[0]!)).toBe(false);
    expect(tracker.acceptRecovery(frame).terminalNotifications).toEqual([]);
  });

  it("accepts 10000 current formula tasks and rejects invalid frames before any delivery", () => {
    const tracker = new RealtimeTaskTracker();
    const valid = recoveryFixture();
    const duplicateTask = recoveryFixture();
    duplicateTask.activeFormulaTasks.push({ ...duplicateTask.activeFormulaTasks[0]! });
    const duplicateEvent = recoveryFixture();
    duplicateEvent.terminalNotifications.push({ ...duplicateEvent.terminalNotifications[0]! });
    const extraProperty = recoveryFixture() as unknown as {
      activeFormulaTasks: Array<Record<string, unknown>>;
    };
    extraProperty.activeFormulaTasks[0]!.unexpected = true;
    const nonJson = recoveryFixture() as unknown as {
      terminalNotifications: Array<{ error: { details: Record<string, unknown> } }>;
    };
    nonJson.terminalNotifications[0]!.error.details.nonJson = undefined;
    const invalidTerminal = recoveryFixture() as unknown as {
      terminalNotifications: Array<Record<string, unknown>>;
    };
    invalidTerminal.terminalNotifications[0]!.state = "running";
    const tooMany = recoveryFixture();
    tooMany.activeFormulaTasks = Array.from({ length: MAX_RECOVERY_ACTIVE_FORMULA_TASKS + 1 }, (_, index) => ({
      ...tooMany.activeFormulaTasks[0]!,
      taskId: `formula-${index}`,
    }));
    const oversized = recoveryFixture() as unknown as {
      terminalNotifications: Array<{ error: { details: Record<string, unknown> } }>;
    };
    oversized.terminalNotifications[0]!.error.details.note = "测".repeat(2 * 1024 * 1024);

    for (const frame of [duplicateTask, duplicateEvent, extraProperty, nonJson, invalidTerminal, tooMany, oversized]) {
      expect(() => tracker.acceptRecovery(frame)).toThrow();
    }

    expect(tracker.acceptRecovery(valid).terminalNotifications).toHaveLength(2);
    expect(tracker.acceptRecovery(valid).terminalNotifications).toEqual([]);

    const maximum = recoveryFixture();
    maximum.terminalNotifications = [];
    maximum.activeFormulaTasks = Array.from({ length: MAX_RECOVERY_ACTIVE_FORMULA_TASKS }, (_, index) => ({
      ...maximum.activeFormulaTasks[0]!,
      taskId: `formula-${index}`,
    }));
    expect(new RealtimeTaskTracker().acceptRecovery(maximum).activeFormulaTasks).toHaveLength(
      MAX_RECOVERY_ACTIVE_FORMULA_TASKS,
    );
  });

  it("deduplicates a full retained terminal window across subsequent live traffic", () => {
    const tracker = new RealtimeTaskTracker();
    const frame = recoveryFixture();
    frame.activeFormulaTasks = [];
    frame.terminalNotifications = Array.from({ length: 10_000 }, (_, index) => ({
      ...frame.terminalNotifications[0]!, eventId: `retained-${index}`,
      taskId: `formula-${index}`, sequence: index + 1, error: null,
    }));
    expect(tracker.acceptRecovery(frame).terminalNotifications).toHaveLength(10_000);
    for (let index = 0; index < 2_048; index += 1) {
      expect(tracker.accept(taskEvent(10_001 + index, 0.5))).toBe(true);
    }
    expect(tracker.acceptRecovery(frame).terminalNotifications).toHaveLength(0);
    tracker.reset();
    expect(tracker.acceptRecovery(frame).terminalNotifications).toHaveLength(10_000);
  });

  it.each(["2026-02-30T08:30:00Z", "2026-04-31T08:30:00Z", "2026-07-24T24:00:00Z"])(
    "rejects a timestamp that JavaScript normalizes instead of validating: %s", (occurredAt) => {
      const frame = recoveryFixture();
      frame.terminalNotifications[0] = { ...frame.terminalNotifications[0]!, occurredAt };
      expect(() => new RealtimeTaskTracker().acceptRecovery(frame)).toThrow();
    },
  );

  it("preserves a valid leap-day timestamp with an offset and fractional seconds", () => {
    const frame = recoveryFixture();
    const occurredAt = "2000-02-29T23:59:59.123456789-08:00";
    frame.terminalNotifications[0] = { ...frame.terminalNotifications[0]!, occurredAt };
    expect(new RealtimeTaskTracker().acceptRecovery(frame).terminalNotifications[0]?.occurredAt)
      .toBe(occurredAt);
  });

  it("retires only Go sequence gates when authoritative recovery replaces activity", () => {
    const tracker = new RealtimeTaskTracker();
    const previous = { ...taskEvent(90, 0.2), taskId: "formula-resumed" };
    const python = { ...previous, taskId: "python-import", taskType: "import" as const,
      eventId: "python-import-90" };
    expect(tracker.accept(previous)).toBe(true);
    expect(tracker.accept(python)).toBe(true);
    tracker.acceptRecovery(recoveryFixture());
    expect(tracker.accept({ ...previous, eventId: "resumed-progress", sequence: 41, progress: 0.75 }))
      .toBe(true);
    expect(tracker.accept({ ...python, eventId: "old-import", sequence: 89 })).toBe(false);
  });
});

interface MutableRecoverySnapshot {
  contractVersion: "2.0";
  topic: "realtime.recovered";
  activeFormulaTasks: FormulaTaskState[];
  terminalNotifications: TaskChangedEvent[];
}

function recoveryFixture(): MutableRecoverySnapshot {
  return structuredClone(recoveryFixtureJson) as MutableRecoverySnapshot;
}

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
