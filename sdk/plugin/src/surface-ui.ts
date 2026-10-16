import { PluginCapabilityError } from "./capabilities.js";

export const SURFACE_UI_METHODS = ["context.current", "tasks.list", "tasks.cancel", "notification.show", "navigation.back"] as const;
export const SURFACE_UI_UNSUPPORTED = ["navigation.table", "navigation.record", "navigation.url", "task.create", "task.approve"] as const;
export interface SurfacePresentation {
  readonly contract: "vibetable.plugin-theme.v1";
  readonly mode: "light" | "dark";
  readonly locale: string;
  readonly density: "compact" | "comfortable";
  readonly variables: Readonly<Record<string, string>>;
}
export interface SurfaceTask {
  readonly taskId: string;
  readonly actionId?: string;
  readonly revision: number;
  readonly state: "queued" | "running" | "succeeded" | "failed" | "cancelled" | "aborted";
  readonly cancelRequested: boolean;
  readonly progress?: { readonly current: number; readonly total: number | null; readonly message: string; readonly cancellable: boolean } | null;
  readonly result?: { readonly summary: string } | null;
  readonly error?: { readonly code: string; readonly message: string; readonly recoverability: string } | null;
}
export interface SurfaceParentTransport {
  readonly parent: { postMessage(message: unknown, targetOrigin: string): void };
  addEventListener(type: "message", listener: (event: { readonly data: unknown; readonly origin: string; readonly source: unknown }) => void): void;
  removeEventListener(type: "message", listener: (event: { readonly data: unknown; readonly origin: string; readonly source: unknown }) => void): void;
}
const origin = "https://app.vibetable.local";
const terminal = (task: SurfaceTask) => ["succeeded", "failed", "cancelled", "aborted"].includes(task.state);
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);

/** Presentation only: never an authority/grant, task creator or approval channel. */
export function createSurfacePresentationClient(surfaceToken: string, supplied?: SurfaceParentTransport) {
  const window = globalThis as typeof globalThis & Partial<SurfaceParentTransport>;
  const channel = supplied ?? (window.parent && (window.parent as unknown) !== globalThis ? window as SurfaceParentTransport : undefined);
  let closed = false;
  let presentation: SurfacePresentation | undefined;
  const themes = new Set<(value: SurfacePresentation) => void>();
  const listeners = new Set<(tasks: readonly SurfaceTask[]) => void>();
  const tasks = new Map<string, SurfaceTask>();
  const pending = new Map<string, { resolve(value: unknown): void; reject(error: Error): void; timer: ReturnType<typeof setTimeout> }>();
  const notify = <T>(set: Set<(value: T) => void>, value: T) => { for (const callback of set) try { callback(value); } catch { /* Consumer callbacks cannot block receipts. */ } };
  function applyTheme(value: unknown): boolean {
    if (!object(value) || value.contract !== "vibetable.plugin-theme.v1" || !["light", "dark"].includes(String(value.mode))
      || typeof value.locale !== "string" || value.locale.length > 32 || !["compact", "comfortable"].includes(String(value.density)) || !object(value.variables)) return false;
    const variables: Record<string, string> = {};
    for (const [key, item] of Object.entries(value.variables))
      if (key.startsWith("--vt-plugin-") && typeof item === "string" && item.length <= 256) variables[key] = item;
    presentation = Object.freeze({ contract: "vibetable.plugin-theme.v1", mode: value.mode as SurfacePresentation["mode"],
      locale: value.locale, density: value.density as SurfacePresentation["density"], variables: Object.freeze(variables) });
    notify(themes, presentation);
    return true;
  }
  function applyTasks(value: unknown): boolean {
    if (!Array.isArray(value) || value.length > 256) return false;
    for (const item of value) {
      if (!object(item) || typeof item.taskId !== "string" || !item.taskId || item.taskId.length > 128
        || !Number.isSafeInteger(item.revision) || !["queued", "running", "succeeded", "failed", "cancelled", "aborted"].includes(String(item.state))) continue;
      const prior = tasks.get(item.taskId);
      if (prior && (terminal(prior) || prior.revision >= (item.revision as number))) continue;
      const progress = object(item.progress) && typeof item.progress.current === "number" && typeof item.progress.message === "string"
        ? Object.freeze({ current: item.progress.current, total: typeof item.progress.total === "number" && item.progress.total > 0 ? item.progress.total : null,
          message: item.progress.message.slice(0, 500), cancellable: item.progress.cancellable === true }) : null;
      const result = object(item.result) && typeof item.result.summary === "string" ? Object.freeze({ summary: item.result.summary.slice(0, 1000) }) : null;
      const error = object(item.error) && typeof item.error.code === "string" && typeof item.error.message === "string"
        ? Object.freeze({ code: item.error.code.slice(0, 128), message: item.error.message.slice(0, 1000), recoverability: String(item.error.recoverability).slice(0, 32) }) : null;
      if (!prior && tasks.size >= 256) {
        const oldest = [...tasks].find(([, task]) => terminal(task));
        if (oldest) tasks.delete(oldest[0]); else continue;
      }
      // A late completion of an old active task is a recent receipt, not the oldest history.
      tasks.delete(item.taskId);
      tasks.set(item.taskId, Object.freeze({ taskId: item.taskId, ...(typeof item.actionId === "string" ? { actionId: item.actionId } : {}),
        revision: item.revision as number, state: item.state as SurfaceTask["state"], cancelRequested: prior?.cancelRequested === true || item.cancelRequested === true, progress, result, error }));
    }
    for (const [id, task] of tasks) if (tasks.size > 256 && terminal(task)) tasks.delete(id);
    notify(listeners, Object.freeze([...tasks.values()]));
    return true;
  }
  const receive = (event: { readonly data: unknown; readonly origin: string; readonly source: unknown }): void => {
    if (closed || event.origin !== origin || event.source !== channel?.parent || !object(event.data)) return;
    const message = event.data;
    if (message.contract !== "vibetable.plugin-surface.v1" || message.surfaceToken !== surfaceToken) return;
    if (message.event === "themeChanged") { applyTheme(message.payload); return; }
    if (message.event === "tasksChanged") { applyTasks(message.payload); return; }
    if (message.event !== "uiResponse" || !object(message.payload) || typeof message.payload.requestId !== "string") return;
    const request = pending.get(message.payload.requestId);
    if (!request) return;
    pending.delete(message.payload.requestId); clearTimeout(request.timer);
    if (object(message.payload.error)) request.reject(new PluginCapabilityError(String(message.payload.error.code), "Plugin UI request rejected."));
    else request.resolve(message.payload.result);
  };
  channel?.addEventListener("message", receive);
  // Ask the already existing producer for initial snapshots even when the SDK
  // is loaded lazily after the iframe load event.
  try { channel?.parent.postMessage({ contract: "vibetable.plugin-surface.v1", surfaceToken, event: "ready", payload: {} }, origin); }
  catch { /* Explicit current/list requests retain their own failure receipts. */ }
  function call<T>(method: string, params: unknown = {}): Promise<T> {
    if (closed) return Promise.reject(new PluginCapabilityError("plugin_surface_closed", "Surface closed."));
    if (!channel) return Promise.reject(new PluginCapabilityError("plugin_api_unsupported", "Trusted parent presentation unavailable."));
    if (pending.size >= 8) return Promise.reject(new PluginCapabilityError("plugin_concurrency_limit", "Too many UI requests."));
    const requestId = crypto.randomUUID();
    return new Promise<T>((resolve, reject) => {
      const timer = setTimeout(() => { pending.delete(requestId); reject(new PluginCapabilityError("plugin_timeout", "UI request expired; no automatic retry.")); }, 20_000);
      pending.set(requestId, { resolve: value => resolve(value as T), reject, timer });
      try { channel.parent.postMessage({ contract: "vibetable.plugin-surface.v1", surfaceToken, event: "action",
        payload: { service: "ui", requestId, method, params } }, origin); }
      catch { clearTimeout(timer); pending.delete(requestId); reject(new PluginCapabilityError("plugin_transport_failed", "UI transport unavailable.")); }
    });
  }
  return {
    async current(): Promise<SurfacePresentation> {
      const value = await call<unknown>("context.current");
      if (!applyTheme(value) || !presentation) throw new PluginCapabilityError("plugin_ui_invalid", "Invalid presentation snapshot.");
      return presentation;
    },
    onChanged(listener: (value: SurfacePresentation) => void): () => void {
      if (closed) throw new PluginCapabilityError("plugin_surface_closed", "Surface closed.");
      if (!channel) throw new PluginCapabilityError("plugin_api_unsupported", "Trusted parent presentation unavailable.");
      themes.add(listener); if (presentation) notify(new Set([listener]), presentation);
      return () => { themes.delete(listener); };
    },
    tasks: {
      async list(): Promise<readonly SurfaceTask[]> {
        if (!applyTasks(await call("tasks.list"))) throw new PluginCapabilityError("plugin_ui_invalid", "Invalid task snapshot.");
        return Object.freeze([...tasks.values()]);
      },
      onChanged(listener: (value: readonly SurfaceTask[]) => void): () => void {
        if (closed) throw new PluginCapabilityError("plugin_surface_closed", "Surface closed.");
        if (!channel) throw new PluginCapabilityError("plugin_api_unsupported", "Trusted parent presentation unavailable.");
        listeners.add(listener); notify(new Set([listener]), Object.freeze([...tasks.values()]));
        return () => { listeners.delete(listener); };
      },
      cancel: (taskId: string) => call<{ readonly cancelRequested: boolean }>("tasks.cancel", { taskId }),
    },
    ui: {
      capabilities: () => ({ methods: SURFACE_UI_METHODS, unsupported: SURFACE_UI_UNSUPPORTED }),
      navigation: { back: () => call<{ readonly navigated: boolean }>("navigation.back") },
      notifications: { show: (message: string) => call<{ readonly shown: boolean }>("notification.show", { message }) },
    },
    get available(): boolean { return channel !== undefined; },
    dispose(): void {
      if (closed) return; closed = true;
      channel?.removeEventListener("message", receive);
      for (const request of pending.values()) { clearTimeout(request.timer); request.reject(new PluginCapabilityError("plugin_surface_closed", "Surface closed.")); }
      pending.clear(); tasks.clear(); themes.clear(); listeners.clear(); presentation = undefined;
    },
  };
}
