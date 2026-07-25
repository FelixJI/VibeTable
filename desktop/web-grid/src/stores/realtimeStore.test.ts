import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useRealtimeStore } from "./realtimeStore";
import type { TaskChangedEvent } from "@/contracts";

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
    expect(store.activeFormulaBackfill?.sequence).toBe(40);
    expect(store.activeFormulaBackfill?.progress).toBe(0.4);
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
    contractVersion: "1.0",
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
