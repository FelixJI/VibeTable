import type { PluginSurfaceThemeSnapshot, PluginTaskViewSnapshot } from "@/contracts";

export const SURFACE_UI_METHODS = ["context.current", "tasks.list", "tasks.cancel", "notification.show", "navigation.back"] as const;
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
const exact = (value: Record<string, unknown>, keys: readonly string[]) => Object.keys(value).length === keys.length && keys.every(key => key in value);
const terminal = (task: PluginTaskViewSnapshot) => ["succeeded", "failed", "cancelled", "aborted"].includes(task.state);
export function projectSurfaceTasks(tasks: readonly PluginTaskViewSnapshot[], token: string) {
  // Reports have per-task revisions, not a shared clock. Use the existing store's
  // accepted-report order so an old active task's new terminal remains recoverable.
  return tasks.map((task, index) => ({ task, order: task.observationOrder ?? index }))
    .filter(({ task }) => task.surfaceToken === token)
    .sort((left, right) => Number(terminal(left.task)) - Number(terminal(right.task)) || right.order - left.order)
    .slice(0, 256)
    .sort((left, right) => left.order - right.order)
    .map(({ task }) => ({
      taskId: task.taskId, actionId: task.actionId, revision: task.revision, state: task.state, cancelRequested: task.cancelRequested,
      progress: task.progress ? { current: task.progress.current, total: task.progress.total > 0 ? task.progress.total : null,
        message: task.progress.message.slice(0, 500), cancellable: task.progress.cancellable } : null,
      result: task.result ? { summary: task.result.summary.slice(0, 1000) } : null,
      error: task.error ? { code: task.error.code, message: task.error.message.slice(0, 1000), recoverability: task.error.recoverability } : null,
    }));
}
/** Trusted frame origin/source/token is checked by the caller before this closed presentation seam. */
export function createPluginSurfaceUi(port: {
  token(): string;
  theme(): PluginSurfaceThemeSnapshot;
  tasks(): readonly PluginTaskViewSnapshot[];
  cancel(taskId: string): void;
  back(): boolean;
  show(message: string): void;
}) {
  let shown = 0;
  return {
    reset(): void { shown = 0; },
    handle(value: unknown): { requestId: string; result?: unknown; error?: { code: string } } | null {
      if (!object(value) || value.service !== "ui") return null;
      const requestId = typeof value.requestId === "string" && value.requestId.length <= 128 ? value.requestId : "";
      const failure = (code: string) => ({ requestId, error: { code } });
      if (!requestId || !exact(value, ["service", "requestId", "method", "params"]) || !object(value.params)) return failure("plugin_capability_invalid");
      const params = value.params;
      let result: unknown;
      switch (value.method) {
        case "context.current":
          if (!exact(params, [])) return failure("plugin_capability_invalid");
          result = port.theme(); break;
        case "tasks.list":
          if (!exact(params, [])) return failure("plugin_capability_invalid");
          result = projectSurfaceTasks(port.tasks(), port.token()); break;
        case "tasks.cancel": {
          if (!exact(params, ["taskId"]) || typeof params.taskId !== "string" || params.taskId.length > 128) return failure("plugin_capability_invalid");
          const task = port.tasks().find(task => task.taskId === params.taskId && task.surfaceToken === port.token());
          if (!task) return failure("plugin_task_not_owned");
          if (terminal(task)) { result = { cancelRequested: false }; break; }
          if (task.progress?.cancellable === false) return failure("plugin_task_not_cancellable");
          if (!task.cancelRequested) port.cancel(task.taskId);
          result = { cancelRequested: true }; break;
        }
        case "notification.show":
          if (!exact(params, ["message"]) || typeof params.message !== "string" || !params.message.trim() || params.message.length > 500) return failure("plugin_capability_invalid");
          if (shown >= 8) return failure("plugin_notification_limit");
          shown++; port.show(params.message); result = { shown: true }; break;
        case "navigation.back":
          if (!exact(params, [])) return failure("plugin_capability_invalid");
          result = { navigated: port.back() }; break;
        default: return failure("plugin_api_unsupported");
      }
      return { requestId, result };
    },
  };
}
