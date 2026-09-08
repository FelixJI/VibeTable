import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import recoveryFixtureJson from "../../../../contracts/v2/fixtures/realtime-recovery-event.json";
import { useRealtimeStore } from "./realtimeStore";
import type { FormulaTaskState, TaskChangedEvent } from "@/contracts";
import { RealtimeTaskTracker } from "@/services/realtimeReconciler";

describe("realtimeStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("projects running backfill progress and keeps the terminal snapshot", () => {
    const store = useRealtimeStore();
    store.applyTask(task("running", 0.47));
    expect(store.activeTask?.progress).toBe(0.47);
    expect(store.activeFormulaBackfill?.progress).toBe(0.47);

    store.applyTask(task("succeeded", 1));
    expect(store.activeTask).toBeNull();
    expect(store.activeFormulaBackfill).toBeNull();
    expect(store.latestTask?.state).toBe("succeeded");
  });

  it.each([false, true])("updates the general active-task view after recovery (empty: %s)", (empty) => {
    const store = useRealtimeStore();
    store.applyTask(task("running", 0.1));
    const frame = recoveryFixture();
    if (empty) frame.activeFormulaTasks = [];
    const delivery = new RealtimeTaskTracker().acceptRecovery(frame);
    store.replaceFormulaTaskProjection(delivery.activeFormulaTasks);

    expect(store.activeTask?.taskId ?? null).toBe(empty ? null : "formula-resumed");
    expect(store.tasksById["formula-orders"]).toBeUndefined();
  });

  it("keeps a formula backfill visible while another task starts and finishes", () => {
    const store = useRealtimeStore();
    store.applyTask(task("running", 0.47));
    store.applyTask({
      ...task("running", 0.2),
      eventId: "evt-export-running",
      sequence: 2,
      taskId: "export-orders",
      taskType: "export",
    });
    store.applyTask({
      ...task("succeeded", 1),
      eventId: "evt-export-succeeded",
      sequence: 3,
      taskId: "export-orders",
      taskType: "export",
    });

    expect(store.latestTask?.taskId).toBe("export-orders");
    expect(store.activeFormulaBackfill?.taskId).toBe("formula-orders");
    expect(store.activeFormulaBackfill?.progress).toBe(0.47);
    expect(store.tasksById["export-orders"]?.state).toBe("succeeded");
  });

  it("prefers the most recently accepted concurrent backfill instead of comparing task-local sequences", () => {
    const store = useRealtimeStore();
    store.applyTask(formulaTask(
      "formula-old",
      "running",
      90,
      "2026-07-24T08:30:00Z",
      0.75,
    ));
    store.applyTask(formulaTask(
      "formula-new",
      "running",
      1,
      "2026-07-24T08:31:00Z",
      0.1,
    ));

    expect(store.activeFormulaBackfill?.taskId).toBe("formula-new");

    store.applyTask(formulaTask(
      "formula-old",
      "running",
      91,
      "2026-07-24T08:32:00Z",
      0.8,
    ));
    expect(store.activeFormulaBackfill?.taskId).toBe("formula-old");
    expect(store.activeFormulaBackfill?.progress).toBe(0.8);
  });

  it("falls back to the remaining active backfill after the newest task terminates and rejects stale delivery", () => {
    const store = useRealtimeStore();
    store.applyTask(formulaTask(
      "formula-old",
      "running",
      40,
      "2026-07-24T08:30:00Z",
      0.4,
    ));
    store.applyTask(formulaTask(
      "formula-new",
      "running",
      1,
      "2026-07-24T08:31:00Z",
      0.15,
    ));
    store.applyTask(formulaTask(
      "formula-new",
      "succeeded",
      2,
      "2026-07-24T08:32:00Z",
      1,
    ));

    expect(store.activeFormulaBackfill?.taskId).toBe("formula-old");

    store.applyTask(formulaTask(
      "formula-old",
      "running",
      39,
      "2026-07-24T08:29:00Z",
      0.2,
    ));
    expect(store.activeFormulaBackfill?.taskId).toBe("formula-old");
    expect(store.activeFormulaBackfill?.progress).toBe(0.4);
    expect(store.activeFormulaBackfill).not.toHaveProperty("sequence");
  });

  it("projects a resumed Go formula set without letting its retained cancellation replace current work", () => {
    const store = useRealtimeStore();
    store.applyTask({
      ...task("running", 0.2),
      eventId: "evt-python-import",
      taskId: "python-import",
      taskType: "import",
    });

    const recovery = new RealtimeTaskTracker().acceptRecovery(recoveryFixture());
    store.replaceFormulaTaskProjection(recovery.activeFormulaTasks);

    expect(store.activeFormulaBackfill).toMatchObject({
      taskId: "formula-resumed",
      state: "running",
      progress: 0.5,
    });
    expect(store.tasksById["python-import"]?.state).toBe("running");
    expect(recovery.terminalNotifications.map((event) => event.eventId)).toEqual([
      "evt_formula_cancelled_before_resume",
      "evt_formula_succeeded",
    ]);
    expect(store.latestTask?.eventId).toBe("evt-python-import");
  });

  it("accepts a repeated recovery frame without replacing its current formula projection", () => {
    const store = useRealtimeStore();
    const frame = recoveryFixture();
    const tracker = new RealtimeTaskTracker();

    store.replaceFormulaTaskProjection(tracker.acceptRecovery(frame).activeFormulaTasks);
    const repeated = tracker.acceptRecovery(frame);
    store.replaceFormulaTaskProjection(repeated.activeFormulaTasks);

    expect(store.activeFormulaBackfill).toMatchObject({
      taskId: "formula-resumed",
      state: "running",
    });
    expect(repeated.terminalNotifications).toEqual([]);
  });

  it("clears only Go formula activity when the recovered active set is empty", () => {
    const store = useRealtimeStore();
    store.applyTask(formulaTask("formula-before-recovery", "running", 1, "2026-09-03T08:00:00Z", 0.2));
    store.applyTask({
      ...task("running", 0.3),
      eventId: "evt-python-export",
      taskId: "python-export",
      taskType: "export",
    });
    const frame = recoveryFixture();
    frame.activeFormulaTasks = [];

    store.replaceFormulaTaskProjection(new RealtimeTaskTracker().acceptRecovery(frame).activeFormulaTasks);

    expect(store.activeFormulaBackfill).toBeNull();
    expect(store.tasksById["python-export"]?.state).toBe("running");
    expect(store.activeTask?.taskId).toBe("python-export");
  });

  it("updates and retires recovered formula activity when a live task snapshot arrives", () => {
    const store = useRealtimeStore();
    store.applyTask(formulaTask("formula-resumed", "running", 90, "2026-09-03T08:31:00Z", 0.2));
    store.replaceFormulaTaskProjection(
      new RealtimeTaskTracker().acceptRecovery(recoveryFixture()).activeFormulaTasks,
    );
    store.applyTask(formulaTask("formula-resumed", "running", 41, "2026-09-03T08:31:00Z", 0.75));
    expect(store.activeFormulaBackfill).toMatchObject({ progress: 0.75, cursor: "0.75" });
    expect(store.activeTask).toMatchObject({ progress: 0.75, cursor: "0.75" });

    store.applyTask(formulaTask("formula-resumed", "succeeded", 42, "2026-09-03T08:32:00Z", 1));
    expect(store.activeFormulaBackfill).toBeNull();
  });

  it("retires recovered formula activity on reset", () => {
    const store = useRealtimeStore();
    store.replaceFormulaTaskProjection(
      new RealtimeTaskTracker().acceptRecovery(recoveryFixture()).activeFormulaTasks,
    );

    store.reset();

    expect(store.activeFormulaBackfill).toBeNull();
  });

  it("records invalidation and clears an earlier reconcile failure", () => {
    const store = useRealtimeStore();
    store.failReconcile(new Error("offline"));
    store.markInvalidated("reload-schema");
    expect(store.reconcileError).toBeNull();
    expect(store.lastInvalidation?.action).toBe("reload-schema");
  });
});

function task(
  state: TaskChangedEvent["state"],
  progress: number,
): TaskChangedEvent {
  return {
    contractVersion: "2.0",
    topic: "task.changed",
    eventId: `evt-${state}`,
    sequence: state === "running" ? 1 : 2,
    occurredAt: "2026-07-24T08:30:00Z",
    taskId: "formula-orders",
    taskType: "formulaBackfill",
    state,
    progress,
    cursor: String(progress),
    error: null,
  };
}

function formulaTask(
  taskId: string,
  state: TaskChangedEvent["state"],
  sequence: number,
  occurredAt: string,
  progress: number,
): TaskChangedEvent {
  return {
    ...task(state, progress),
    eventId: `${taskId}-${sequence}-${state}`,
    taskId,
    sequence,
    occurredAt,
  };
}

interface MutableRecoverySnapshot {
  contractVersion: "2.0";
  topic: "realtime.recovered";
  activeFormulaTasks: FormulaTaskState[];
  terminalNotifications: TaskChangedEvent[];
}

function recoveryFixture(): MutableRecoverySnapshot {
  return structuredClone(recoveryFixtureJson) as MutableRecoverySnapshot;
}
