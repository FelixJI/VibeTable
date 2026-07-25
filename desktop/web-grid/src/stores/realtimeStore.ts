import { defineStore } from "pinia";
import { computed, ref } from "vue";
import type { ReconcileAction } from "@/services/realtimeReconciler";
import type { TaskChangedEvent } from "@/contracts";

export const useRealtimeStore = defineStore("realtime", () => {
  const latestTask = ref<TaskChangedEvent | null>(null);
  const tasksById = ref<Record<string, TaskChangedEvent>>({});
  const receiptOrderByTask = ref<Record<string, number>>({});
  let nextReceiptOrder = 0;
  const reconcileError = ref<string | null>(null);
  const lastInvalidation = ref<{
    readonly action: Exclude<ReconcileAction, "none">;
    readonly occurredAt: string;
  } | null>(null);

  const activeTasks = computed(() =>
    Object.values(tasksById.value)
      .filter((task) => task.state === "pending" || task.state === "running")
      .sort((a, b) =>
        (receiptOrderByTask.value[a.taskId] ?? 0)
        - (receiptOrderByTask.value[b.taskId] ?? 0)),
  );
  const activeTask = computed(() => activeTasks.value.at(-1) ?? null);
  const activeFormulaBackfill = computed(() =>
    activeTasks.value
      .filter((task) => task.taskType === "formulaBackfill")
      .at(-1) ?? null,
  );

  function applyTask(task: TaskChangedEvent): void {
    const previous = tasksById.value[task.taskId];
    if (
      previous
      && task.sequence <= previous.sequence
      && task.occurredAt <= previous.occurredAt
    ) return;

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

  return {
    latestTask,
    tasksById,
    activeTask,
    activeFormulaBackfill,
    reconcileError,
    lastInvalidation,
    applyTask,
    markInvalidated,
    failReconcile,
    clearReconcileError,
  };
});
