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
  } | {
    /** Deletion requires the explicit `delete` data operation grant and always triggers Host destructive confirmation. */
    readonly kind: "delete"; readonly primaryKey: string; readonly expectedDigest: string;
  })[];
}
export interface SurfaceMutationPreview { readonly planId: string; readonly affectedCount: number }
export interface SurfaceMutationOutcome { readonly status: "succeeded" | "rejected"; readonly taskId: string }
export type SurfaceDataEvent = { readonly type: "invalidated"; readonly collection: string; readonly sequence: number }
  | { readonly type: "closed"; readonly code: string };
export interface SurfaceSubscription { readonly subscriptionId: string }
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
    subscribe(request: { readonly collection: string }, listener: (event: SurfaceDataEvent) => void, signal?: AbortSignal): Promise<SurfaceSubscription>;
    /** Unknown handles are a no-op; repeated in-flight releases share the same acknowledgement.
     * Normal requests and owned-handle releases each have eight slots. Failed releases retain ownership for explicit retry/dispose. */
    unsubscribe(subscriptionId: string): Promise<void>;
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
  const subscriptions = new Map<string, { collection: string; listener: (event: SurfaceDataEvent) => void; sequence: number; cleanup: () => void; stopped: boolean; release?: Promise<void> }>();
  type Notice = { event?: string; subscriptionId?: string; collection?: string; sequence?: number; code?: string };
  // A Host notification may beat the subscribe reply; retain one per pending handle, at most eight.
  const early = new Map<string, Notice>();
  let subscribing = 0;
  function notice(value: Notice): void {
    const id = value.subscriptionId;
    if (!id) return;
    const subscription = subscriptions.get(id);
    if (!subscription) {
      if (subscribing > 0 && (early.has(id) || early.size < 8)) {
        const prior = early.get(id);
        if (prior?.event !== "data.subscription.closed" && (!prior || value.event === "data.subscription.closed" || (value.sequence ?? 0) > (prior.sequence ?? 0))) early.set(id, value);
      }
      return;
    }
    let event: SurfaceDataEvent;
    if (value.event === "data.subscription.closed" && typeof value.code === "string") {
      subscriptions.delete(id); subscription.cleanup();
      if (subscription.stopped) return;
      event = { type: "closed", code: value.code };
    } else if (!subscription.stopped && value.event === "data.invalidated" && value.collection === subscription.collection
      && Number.isSafeInteger(value.sequence) && value.sequence! > subscription.sequence) {
      subscription.sequence = value.sequence!;
      event = { type: "invalidated", collection: value.collection, sequence: value.sequence! };
    } else return;
    try { subscription.listener(event); } catch { /* A consumer callback cannot break transport settlement. */ }
  }
  function release(method: string, params: unknown): void {
    try { channel!.postMessage({ contract: SURFACE_API_CONTRACT, surfaceToken, requestId: crypto.randomUUID(), method, params }); }
    catch { /* Host closes all owned subscriptions when the frame retires. */ }
  }
  const pending = new Map<string, { method: string; resolve: (value: unknown) => void; reject: (error: Error) => void; timer: ReturnType<typeof setTimeout>; cleanup: () => void }>();
  const receive = (event: { readonly data: unknown }): void => {
    if (!event.data || typeof event.data !== "object") return;
    const reply = event.data as Notice & { surfaceToken?: string; contract?: string; requestId?: string; result?: unknown; error?: { code?: string; message?: string } };
    if (closed || reply.contract !== SURFACE_API_CONTRACT) return;
    if ((reply.event === "data.invalidated" || reply.event === "data.subscription.closed") && reply.surfaceToken === surfaceToken) { notice(reply); return; }
    if (!reply.requestId) return;
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
    const releases = [...pending.values()].filter(request => request.method === "data.unsubscribe").length;
    if (method === "data.unsubscribe" ? releases >= 8 : pending.size - releases >= 8)
      return Promise.reject(new PluginCapabilityError("plugin_concurrency_limit", "Too many requests."));
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
      pending.set(requestId, { method, resolve: value => resolve(value as T), reject, timer, cleanup });
      signal?.addEventListener("abort", cancel, { once: true });
      try { channel!.postMessage({ contract: SURFACE_API_CONTRACT, surfaceToken, requestId, method, params }); }
      catch { pending.delete(requestId); clearTimeout(timer); cleanup(); reject(new PluginCapabilityError("plugin_transport_failed", "Plugin transport failed.")); }
    });
  }
  async function unsubscribe(subscriptionId: string): Promise<void> {
    const prior = subscriptions.get(subscriptionId);
    if (!prior || closed) return;
    if (prior.release) return prior.release;
    prior.stopped = true; early.delete(subscriptionId); prior.cleanup();
    prior.release = call<void>("data.unsubscribe", { subscriptionId }).then(() => {
      if (subscriptions.get(subscriptionId) === prior) subscriptions.delete(subscriptionId);
    }, error => { delete prior.release; throw error; });
    return prior.release;
  }
  async function subscribe(request: { readonly collection: string }, listener: (event: SurfaceDataEvent) => void,
    signal?: AbortSignal): Promise<SurfaceSubscription> {
    if (subscriptions.size + subscribing >= 8) throw new PluginCapabilityError("plugin_subscription_limit", "Too many subscriptions.");
    subscribing++;
    try {
      const result = await call<SurfaceSubscription>("data.subscribe", request, signal);
      if (closed || signal?.aborted) {
        release("data.unsubscribe", { subscriptionId: result.subscriptionId });
        throw new PluginCapabilityError(closed ? "plugin_surface_closed" : "plugin_cancel_requested", "Subscription cancelled.");
      }
      const abort = (): void => {
        const prior = subscriptions.get(result.subscriptionId);
        subscriptions.delete(result.subscriptionId); prior?.cleanup();
        release("data.unsubscribe", { subscriptionId: result.subscriptionId });
      };
      const cleanup = (): void => signal?.removeEventListener("abort", abort);
      subscriptions.set(result.subscriptionId, { collection: request.collection, listener, sequence: 0, cleanup, stopped: false });
      signal?.addEventListener("abort", abort, { once: true });
      const queued = early.get(result.subscriptionId);
      early.delete(result.subscriptionId);
      if (queued) notice(queued);
      return result;
    } finally { subscribing--; if (subscribing === 0) early.clear(); }
  }
  return {
    context: { read: () => call("context.read", {}) },
    data: { describe: request => call("data.describe", request), query: request => call("data.query", request), subscribe, unsubscribe },
    storage: { get: key => call("storage.get", { key }), set: (key, value) => call("storage.set", { key, value }), delete: key => call("storage.delete", { key }) },
    mutation: { preview: plan => call("mutation.preview", plan), submit: (planId, signal) => call("mutation.submit", { planId }, signal) },
    dispose: () => {
      if (closed) return;
      closed = true;
      for (const [subscriptionId, subscription] of subscriptions) {
        subscription.cleanup(); release("data.unsubscribe", { subscriptionId });
      }
      subscriptions.clear(); early.clear();
      channel.removeEventListener("message", receive);
      // A sent submit may already be committing; dispose reports unknown and resends nothing.
      for (const [requestId, request] of pending) {
        if (request.method === "data.subscribe") release("request.cancel", { requestId });
        clearTimeout(request.timer);
        request.cleanup();
        request.reject(request.method === "mutation.submit"
          ? new PluginCapabilityError("plugin_commit_unknown", "Plugin surface closed before the commit receipt; outcome unknown; no automatic retry.")
          : new PluginCapabilityError("plugin_surface_closed", "Plugin surface closed."));
      }
      pending.clear();
    },
  };
}

const TrustedParentOrigin = "https://app.vibetable.local";

/**
 * Signals the terminal outcome of a headless run to the trusted Host page.
 * The Host shows the status and releases the surface token/frame; this never
 * grants or approves anything by itself.
 */
export function finishHeadlessRun(surfaceToken: string, status: "succeeded" | "failed", summary: string): void {
  const globalWindow = globalThis as typeof globalThis & { parent?: Window };
  try {
    globalWindow.parent?.postMessage({
      contract: "vibetable.plugin-surface.v1",
      surfaceToken,
      event: "close",
      payload: { status, summary },
    }, TrustedParentOrigin);
  } catch {
    // Finishing must never throw into plugin logic; the Host cancel path
    // still retires the surface when the message cannot be delivered.
  }
}
