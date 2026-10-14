import type { DataCatalog, DataDescription, DataDescribeRequest, DataQueryPage, DataQueryRequest } from "./data.js";
import type { CommandContext, JsonObject, JsonValue } from "./types.js";
import { PluginCapabilityError } from "./capabilities.js";

export const SURFACE_API_CONTRACT = "vibetable.plugin-api.v1";
export interface SurfaceMutationPlan {
  readonly contract: "vibetable.surface-mutation-plan.v1";
  readonly collection: string;
  readonly schemaRevision: string;
  readonly operations: readonly ({ readonly kind: "create"; readonly values: JsonObject } | {
    readonly kind: "update"; readonly primaryKey: string; readonly expectedDigest: string; readonly values: JsonObject;
  })[];
}
export interface SurfaceMutationPreview { readonly planId: string; readonly affectedCount: number }
export interface SurfaceMutationOutcome { readonly status: "succeeded" | "rejected"; readonly taskId: string }
export interface SurfaceTransport {
  postMessage(message: unknown): void;
  addEventListener(type: "message", listener: (event: { readonly data: unknown }) => void): void;
  removeEventListener(type: "message", listener: (event: { readonly data: unknown }) => void): void;
}
export interface SurfaceCapabilities {
  readonly context: { read(): Promise<CommandContext> };
  readonly data: {
    describe(request: DataDescribeRequest): Promise<DataCatalog | DataDescription>;
    query(request: DataQueryRequest): Promise<DataQueryPage & { readonly rowGuards: Readonly<Record<string, string>> }>;
  };
  readonly storage: { get(key: string): Promise<JsonValue>; set(key: string, value: JsonValue): Promise<void>; delete(key: string): Promise<void> };
  readonly mutation: {
    preview(plan: SurfaceMutationPlan): Promise<SurfaceMutationPreview>;
    /** Requests one Host confirmation and commit. The plugin never supplies an approval. */
    submit(planId: string, signal?: AbortSignal): Promise<SurfaceMutationOutcome>;
  };
  dispose(): void;
}

/** Runs only inside an isolated WebView2 iframe; never forwards through the parent Product bridge. */
export function createSurfaceClient(surfaceToken: string, transport?: SurfaceTransport): SurfaceCapabilities {
  const browser = globalThis as typeof globalThis & { chrome?: { webview?: SurfaceTransport } };
  const channel = transport ?? browser.chrome?.webview;
  if (!channel || !surfaceToken) throw new PluginCapabilityError("plugin_api_unsupported", "A plugin surface Host is required.");
  let closed = false;
  const pending = new Map<string, { resolve: (value: unknown) => void; reject: (error: Error) => void; timer: ReturnType<typeof setTimeout>; cleanup: () => void }>();
  const receive = (event: { readonly data: unknown }): void => {
    if (!event.data || typeof event.data !== "object") return;
    const reply = event.data as { contract?: string; requestId?: string; result?: unknown; error?: { code?: string; message?: string } };
    if (reply.contract !== SURFACE_API_CONTRACT || !reply.requestId) return;
    const request = pending.get(reply.requestId);
    if (!request) return;
    pending.delete(reply.requestId);
    clearTimeout(request.timer);
    request.cleanup();
    if (reply.error) request.reject(new PluginCapabilityError(reply.error.code ?? "plugin_request_failed", reply.error.message ?? "Plugin request failed."));
    else request.resolve(reply.result);
  };
  channel.addEventListener("message", receive);
  function call<T>(method: string, params: unknown, signal?: AbortSignal): Promise<T> {
    if (closed) return Promise.reject(new PluginCapabilityError("plugin_surface_closed", "Plugin surface closed."));
    if (pending.size >= 8) return Promise.reject(new PluginCapabilityError("plugin_concurrency_limit", "Too many requests."));
    if (signal?.aborted) return Promise.reject(new PluginCapabilityError("plugin_cancel_requested", "Request cancelled."));
    const requestId = crypto.randomUUID();
    return new Promise<T>((resolve, reject) => {
      const cancel = (): void => {
        try { channel!.postMessage({ contract: SURFACE_API_CONTRACT, surfaceToken, requestId: crypto.randomUUID(), method: "request.cancel", params: { requestId } }); }
        catch { pending.delete(requestId); clearTimeout(timer); cleanup(); reject(new PluginCapabilityError(method === "mutation.submit" ? "plugin_commit_unknown" : "plugin_transport_failed", "Cancellation transport failed; no automatic retry.")); }
      };
      const cleanup = (): void => signal?.removeEventListener("abort", cancel);
      const timer = setTimeout(() => {
        pending.delete(requestId);
        cleanup();
        cancel();
        reject(new PluginCapabilityError(method === "mutation.submit" ? "plugin_commit_unknown" : "plugin_timeout", "Request deadline exceeded; no automatic retry."));
      }, method === "mutation.submit" ? 125_000 : 20_000);
      pending.set(requestId, { resolve: value => resolve(value as T), reject, timer, cleanup });
      signal?.addEventListener("abort", cancel, { once: true });
      try { channel!.postMessage({ contract: SURFACE_API_CONTRACT, surfaceToken, requestId, method, params }); }
      catch { pending.delete(requestId); clearTimeout(timer); cleanup(); reject(new PluginCapabilityError("plugin_transport_failed", "Plugin transport failed.")); }
    });
  }
  return {
    context: { read: () => call("context.read", {}) },
    data: { describe: request => call("data.describe", request), query: request => call("data.query", request) },
    storage: { get: key => call("storage.get", { key }), set: (key, value) => call("storage.set", { key, value }), delete: key => call("storage.delete", { key }) },
    mutation: { preview: plan => call("mutation.preview", plan), submit: (planId, signal) => call("mutation.submit", { planId }, signal) },
    dispose: () => {
      closed = true;
      channel.removeEventListener("message", receive);
      for (const request of pending.values()) { clearTimeout(request.timer); request.cleanup(); request.reject(new PluginCapabilityError("plugin_surface_closed", "Plugin surface closed.")); }
      pending.clear();
    },
  };
}
