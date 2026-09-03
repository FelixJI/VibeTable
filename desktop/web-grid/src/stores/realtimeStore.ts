import { defineStore } from "pinia";
import { computed, ref } from "vue";
import type { ReconcileAction } from "@/services/realtimeReconciler";
import type { FormulaTaskState, TaskChangedEvent } from "@/contracts";

interface FormulaTaskProjection extends FormulaTaskState {
  readonly taskType: "formulaBackfill";
}

export const useRealtimeStore = defineStore("realtime", () => {
  const latestTask = ref<TaskChangedEvent | null>(null);
  const tasksById = ref<Record<string, TaskChangedEvent>>({});
  const formulaTaskProjectionById = ref<Record<string, FormulaTaskProjection>>({});
  const receiptOrderByTask = ref<Record<string, number>>({});
  let nextReceiptOrder = 0;
  const reconcileError = ref<string | null>(null);
  const lastInvalidation = ref<{
    readonly action: Exclude<ReconcileAction, "none">;
    readonly occurredAt: string;
  } | null>(null);

  const activeTasks = computed(() =>
    [
      ...Object.values(tasksById.value).filter((task) => task.taskType !== "formulaBackfill"),
      ...Object.values(formulaTaskProjectionById.value),
    ]
      .filter((task) => task.state === "pending" || task.state === "running")
      .sort((a, b) =>
        (receiptOrderByTask.value[a.taskId] ?? 0)
        - (receiptOrderByTask.value[b.taskId] ?? 0)),
  );
  const activeTask = computed(() => activeTasks.value.at(-1) ?? null);
  const activeFormulaBackfill = computed(() =>
    Object.values(formulaTaskProjectionById.value).at(-1) ?? null,
  );

  function applyTask(task: TaskChangedEvent): void {
    const previous = tasksById.value[task.taskId];
    if (
      previous
      && task.sequence <= previous.sequence
      && task.occurredAt <= previous.occurredAt
    ) return;

    updateFormulaTaskProjection(task);

    nextReceiptOrder += 1;
    latestTask.value = task;
    tasksById.value = {
      ...tasksById.value,
      [task.taskId]: task,
    };
    receiptOrderByTask.value = {
      ...receiptOrderByTask.value,
      [task.taskId]: nextReceiptOrder,
    };
  }

  /** Replaces only current Go formula work after recovery-frame validation. */
  function replaceFormulaTaskProjection(tasks: readonly FormulaTaskState[]): void {
    tasksById.value = Object.fromEntries(
      Object.entries(tasksById.value).filter(([, task]) => task.taskType !== "formulaBackfill"),
    );
    // Receipt order is local presentation state, never recovered event metadata.
    receiptOrderByTask.value = Object.fromEntries([
      ...Object.keys(tasksById.value).map((id) => [id, receiptOrderByTask.value[id]]),
      ...tasks.map((task) => [task.taskId, ++nextReceiptOrder]),
    ]);
    formulaTaskProjectionById.value = Object.fromEntries(
      tasks.map((task) => [task.taskId, { ...task, taskType: "formulaBackfill" }]),
    );
  }

  function updateFormulaTaskProjection(task: TaskChangedEvent): void {
    if (task.taskType !== "formulaBackfill") return;
    const next = { ...formulaTaskProjectionById.value };
    if (task.state === "pending" || task.state === "running") {
      delete next[task.taskId];
      next[task.taskId] = {
        taskId: task.taskId,
        taskType: "formulaBackfill",
        state: task.state,
        progress: task.progress,
        cursor: task.cursor,
        error: task.error,
      };
    } else {
      delete next[task.taskId];
    }
    formulaTaskProjectionById.value = next;
  }

  function markInvalidated(action: Exclude<ReconcileAction, "none">): void {
    lastInvalidation.value = {
      action,
      occurredAt: new Date().toISOString(),
    };
    reconcileError.value = null;
  }

  function failReconcile(error: unknown): void {
    reconcileError.value = error instanceof Error ? error.message : String(error);
  }

  function clearReconcileError(): void {
    reconcileError.value = null;
  }

  function reset(): void {
    latestTask.value = null;
    tasksById.value = {};
    formulaTaskProjectionById.value = {};
    receiptOrderByTask.value = {};
    nextReceiptOrder = 0;
    reconcileError.value = null;
    lastInvalidation.value = null;
  }

  return {
    latestTask,
    tasksById,
    formulaTaskProjectionById,
    activeTask,
    activeFormulaBackfill,
    reconcileError,
    lastInvalidation,
    applyTask,
    replaceFormulaTaskProjection,
    markInvalidated,
    failReconcile,
    clearReconcileError,
    reset,
  };
});
