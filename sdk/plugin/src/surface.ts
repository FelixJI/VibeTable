import { createSurfacePresentationClient, type SurfaceParentTransport, type SurfacePresentation, type SurfaceTask } from "./surface-ui.js";
import type { DataCatalog, DataDescription, DataDescribeRequest, DataQueryPage, DataQueryRequest } from "./data.js";
import type { CommandContext, JsonObject, JsonValue } from "./types.js";
import type { ReadGrant, WriteGrant } from "./capabilities.js";
import { PluginCapabilityError } from "./capabilities.js";
import { createSurfaceModel, type SurfaceModel } from "./surface-model.js";

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
export interface SurfaceIoStatus<T> {
  readonly taskId: string;
  readonly state: "queued" | "running" | "succeeded" | "failed" | "cancelled" | "aborted";
  readonly progress: { readonly done: number; readonly total: number; readonly message: string };
  readonly result?: T;
  readonly outcome?: "unknown";
  /** Host-supplied stable terminal reason; never internal plan or grant details. */
  readonly error?: { readonly code: string; readonly message: string };
}
export interface SurfaceIoTask<T> {
  readonly taskId: string;
  status(): Promise<SurfaceIoStatus<T>>;
  /** Requests cancellation: awaits the Host acknowledgement, then polls until a stable
   * terminal is recorded and the handle is released. Resolving does not guarantee the
   * worker actually cancelled — it may already have succeeded or failed. Tracking
   * failures reject and keep the task owned for explicit cancel()/status()/result()
   * recovery while the surface is open. Closing before a terminal receipt rejects,
   * including repeated calls; an already recorded terminal still resolves.
   * The acknowledgement itself is never treated as the terminal. */
  cancel(): Promise<void>;
  result(signal?: AbortSignal, progress?: (value: SurfaceIoStatus<T>["progress"]) => void): Promise<T>;
}
export interface SurfaceImportPreview {
  readonly planId: string | null;
  readonly affectedCount: number;
  readonly summary: { readonly totalRows: number; readonly validRows: number; readonly errorRows: number };
  readonly rows: readonly { readonly sourceRow: number; readonly values: JsonObject; readonly diagnostics: readonly JsonObject[] }[];
}
export interface SurfaceAttachment { readonly attachmentId: string; readonly displayName: string; readonly mediaType: string; readonly sizeBytes: number }
export interface SurfaceAttachmentTarget { readonly collection: string; readonly recordId: string; readonly fieldId: string }
export interface SurfaceTransport {
  postMessage(message: unknown): void;
  addEventListener(type: "message", listener: (event: { readonly data: unknown }) => void): void;
  removeEventListener(type: "message", listener: (event: { readonly data: unknown }) => void): void;
}
export interface SurfaceCapabilities {
  readonly model: SurfaceModel;
  readonly context: { read(): Promise<CommandContext>; current(): Promise<SurfacePresentation>; onChanged(listener: (value: SurfacePresentation) => void): () => void };
  readonly tasks: { list(): Promise<readonly SurfaceTask[]>; onChanged(listener: (tasks: readonly SurfaceTask[]) => void): () => void; cancel(taskId: string): Promise<{ readonly cancelRequested: boolean }> };
  readonly ui: ReturnType<typeof createSurfacePresentationClient>["ui"];
  readonly data: {
    describe(request: DataDescribeRequest): Promise<DataCatalog | DataDescription>;
    /** The Host consumes the input cursor once at query admission. A failure that
     * definitively left it unconsumed keeps it owned and retryable; definitively
     * consumed failures drop it locally; unknown ownership converges through the
     * acknowledged releaseCursor, whose failure keeps it for explicit retry or
     * dispose. The 64-handle budget itself never widens. */
    query(request: DataQueryRequest, signal?: AbortSignal): Promise<DataQueryPage & { readonly rowGuards: Readonly<Record<string, string>> }>;
    /** Iterate an exact query lazily. Exhaustion, break, consumer failure and cancelled queries
     * release the remaining cursor; errors propagate and are never retried. */
    pages(request: Omit<DataQueryRequest, "cursor">, signal?: AbortSignal): AsyncGenerator<DataQueryPage & { readonly rowGuards: Readonly<Record<string, string>> }, void>;
    /** Release an owned opaque cursor; no existence disclosure. Failed releases retain ownership for explicit retry/dispose. */
    releaseCursor(cursor: string): Promise<void>;
    subscribe(request: { readonly collection: string }, listener: (event: SurfaceDataEvent) => void, signal?: AbortSignal): Promise<SurfaceSubscription>;
    /** Unknown handles are a no-op; repeated in-flight releases share the same acknowledgement.
     * Normal requests and owned-handle releases each have eight slots. Failed releases retain ownership for explicit retry/dispose. */
    unsubscribe(subscriptionId: string): Promise<void>;
    previewImport(request: { readonly collection: string; readonly schemaRevision: string; readonly mode?: "create_only"; readonly upsertKey?: string;
      readonly columnMapping?: readonly { readonly sourceColumn: string; readonly targetField: string; readonly relationId?: string; readonly matchField?: string }[] } | {
      /** Upsert matches rows by the approved unique key before preview starts; the key must be non-empty. */
      readonly collection: string; readonly schemaRevision: string; readonly mode: "upsert"; readonly upsertKey: string;
      readonly columnMapping?: readonly { readonly sourceColumn: string; readonly targetField: string; readonly relationId?: string; readonly matchField?: string }[] }): Promise<SurfaceIoTask<SurfaceImportPreview> | null>;
    export(request: { readonly collection: string; readonly fields: readonly string[]; readonly query?: Omit<DataQueryRequest, "contract" | "collection" | "fields" | "cursor" | "pageSize">;
      readonly format: "csv" | "xlsx"; readonly suggestedName: string }): Promise<SurfaceIoTask<{ readonly rowsWritten: number; readonly outputDisplayName: string }> | null>;
  };
  readonly file: {
    pickRead(options?: { readonly mediaTypes?: readonly string[] }): Promise<ReadGrant | null>;
    pickWrite(options: { readonly suggestedName: string; readonly mediaType: string }): Promise<WriteGrant | null>;
  };
  readonly attachments: {
    list(target: SurfaceAttachmentTarget): Promise<readonly SurfaceAttachment[]>;
    read(attachmentId: string): Promise<Uint8Array>;
    previewChange(request: SurfaceAttachmentTarget & { readonly expectedDigest: string; readonly addFile?: boolean; readonly removeAttachmentIds?: readonly string[] }): Promise<SurfaceMutationPreview | null>;
  };
  readonly storage: { get(key: string): Promise<JsonValue>; set(key: string, value: JsonValue): Promise<void>; delete(key: string): Promise<void> };
  readonly mutation: {
    preview(plan: SurfaceMutationPlan): Promise<SurfaceMutationPreview>;
    /** Requests one Host confirmation and commit. The plugin never supplies an approval. */
    submit(planId: string, signal?: AbortSignal): Promise<SurfaceMutationOutcome>;
  };
  /** Current visible application only. No print completion/cancellation receipt exists. */
  readonly output: { print(signal?: AbortSignal): Promise<{ readonly opened: true }> };
  dispose(): void;
}

/** Runs only inside an isolated WebView2 iframe; never forwards through the parent Product bridge. */
export function createSurfaceClient(surfaceToken: string, transport?: SurfaceTransport, parentTransport?: SurfaceParentTransport): SurfaceCapabilities {
  const browser = globalThis as typeof globalThis & { chrome?: { webview?: SurfaceTransport } };
  const channel = transport ?? browser.chrome?.webview;
  if (!channel || !surfaceToken) throw new PluginCapabilityError("plugin_api_unsupported", "A plugin surface Host is required.");
  const presentation = createSurfacePresentationClient(surfaceToken, parentTransport);
  let closed = false;
  const files = new Set<string>();
  const cursors = new Map<string, { release?: Promise<void> }>();
  let pickingFiles = 0;
  const ioTasks = new Map<string, SurfaceIoTask<unknown>>();
  let ioStarting = 0;
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
  function call<T>(method: string, params: unknown, signal?: AbortSignal, onSent?: () => void): Promise<T> {
    if (closed) return Promise.reject(new PluginCapabilityError("plugin_surface_closed", "Plugin surface closed."));
    const releaseMethod = method === "data.unsubscribe" || method === "data.releaseCursor";
    const releases = [...pending.values()].filter(request => request.method === "data.unsubscribe" || request.method === "data.releaseCursor").length;
    const ioCancels = [...pending.values()].filter(request => request.method === "data.io.cancel").length;
    if (releaseMethod ? releases >= 8 : method === "data.io.cancel" ? ioCancels >= 8 : pending.size - releases - ioCancels >= 8)
      return Promise.reject(new PluginCapabilityError("plugin_concurrency_limit", "Too many requests."));
    if (signal?.aborted) return Promise.reject(new PluginCapabilityError("plugin_cancel_requested", "Request cancelled."));
    const requestId = crypto.randomUUID();
    return new Promise<T>((resolve, reject) => {
      const cancel = (): void => {
        try { channel!.postMessage({ contract: SURFACE_API_CONTRACT, surfaceToken, requestId: crypto.randomUUID(), method: "request.cancel", params: { requestId } }); }
        catch { pending.delete(requestId); clearTimeout(timer); cleanup(); reject(new PluginCapabilityError(method === "mutation.submit" || method === "file.write" || method === "model.configure" ? "plugin_commit_unknown" : "plugin_transport_failed", "Cancellation transport failed; no automatic retry.")); }
      };
      const cleanup = (): void => signal?.removeEventListener("abort", cancel);
      const timer = setTimeout(() => {
        pending.delete(requestId);
        cleanup();
        cancel();
        reject(new PluginCapabilityError(method === "mutation.submit" || method === "file.write" || method === "model.configure" ? "plugin_commit_unknown" : "plugin_timeout", "Request deadline exceeded; no automatic retry."));
      }, method === "mutation.submit" || method === "model.configure" || method === "file.pickRead" || method === "file.pickWrite" || method === "data.import.start" || method === "data.export.start" || method === "attachments.previewChange" ? 125_000 : 20_000);
      pending.set(requestId, { method, resolve: value => resolve(value as T), reject, timer, cleanup });
      signal?.addEventListener("abort", cancel, { once: true });
      try { channel!.postMessage({ contract: SURFACE_API_CONTRACT, surfaceToken, requestId, method, params }); onSent?.(); }
      catch { pending.delete(requestId); clearTimeout(timer); cleanup(); reject(new PluginCapabilityError("plugin_transport_failed", "Plugin transport failed.")); }
    });
  }
  function releaseCursor(cursor: string): Promise<void> {
    const owned = cursors.get(cursor);
    if (!owned) return Promise.resolve();
    if (owned.release) return owned.release;
    owned.release = call<{ readonly released: true }>("data.releaseCursor", { cursor }).then(receipt => {
      if (receipt?.released !== true) throw new PluginCapabilityError("plugin_transport_failed", "Cursor release receipt is unknown; no automatic retry.");
      if (cursors.get(cursor) === owned) cursors.delete(cursor);
    }).catch(error => { delete owned.release; throw error; });
    return owned.release;
  }
  // Host replies produced before data.query admission never consumed the input
  // cursor: ownership stays intact on both sides and the query stays retryable.
  const CursorUnconsumedReplies = ["plugin_concurrency_limit", "plugin_request_duplicate", "plugin_api_unsupported", "plugin_source_denied"];
  // Failure boundaries where the Host may still own the input cursor (SDK
  // deadline, broken cancel transport, or admission-ambiguous Host codes:
  // an oversize request is rejected before Host consumption, for example).
  // These converge through the acknowledged releaseCursor — a no-op for handles
  // the Host no longer owns — so a failed cleanup keeps local ownership for
  // explicit retry/dispose; neither the quota nor retries are widened.
  const CursorOwnershipUnknown = ["plugin_timeout", "plugin_transport_failed", "plugin_capability_invalid", "plugin_session_stale", "plugin_message_too_large"];
  async function query(request: DataQueryRequest, signal?: AbortSignal): Promise<DataQueryPage & { readonly rowGuards: Readonly<Record<string, string>> }> {
    if (signal?.aborted) throw new PluginCapabilityError("plugin_cancel_requested", "Request cancelled.");
    if (cursors.size >= 64 && (!request.cursor || !cursors.has(request.cursor))) throw new PluginCapabilityError("plugin_query_limit", "Release outstanding cursors before querying.");
    const input = typeof request.cursor === "string" ? request.cursor : "";
    let sent = false;
    let page: DataQueryPage & { readonly rowGuards: Readonly<Record<string, string>> };
    try { page = await call<DataQueryPage & { readonly rowGuards: Readonly<Record<string, string>> }>("data.query", request, signal, () => { sent = true; }); }
    catch (error) {
      // The Host admits data.query by consuming the input cursor exactly once
      // (TryRemove at receipt). Converge local ownership to the same boundary:
      // a request that never left the frame, or a Host reply from before query
      // admission, keeps the handle retryable; definitively consumed failures
      // drop it locally. Unknown ownership converges through the acknowledged
      // releaseCursor — the local handle is removed only by its success receipt,
      // and a failed release keeps it for explicit retry/dispose. The release
      // outcome never replaces the original query error.
      const code = error instanceof PluginCapabilityError ? error.code : "";
      if (input && sent && code !== "" && cursors.has(input) && !CursorUnconsumedReplies.includes(code)) {
        if (CursorOwnershipUnknown.includes(code)) void releaseCursor(input).catch(() => { /* retained for explicit retry/dispose */ });
        else cursors.delete(input);
      }
      throw error;
    }
    if (input) cursors.delete(input);
    if (page.nextCursor) cursors.set(page.nextCursor, {});
    if (closed || signal?.aborted) {
      if (page.nextCursor) {
        if (closed) { release("data.releaseCursor", { cursor: page.nextCursor }); cursors.delete(page.nextCursor); }
        else await releaseCursor(page.nextCursor);
      }
      throw new PluginCapabilityError(closed ? "plugin_surface_closed" : "plugin_cancel_requested", "Query cancelled.");
    }
    return page;
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
  async function pick(write: boolean, options: unknown): Promise<ReadGrant | WriteGrant | null> {
    if (files.size + pickingFiles >= 8) throw new PluginCapabilityError("plugin_file_limit", "Too many file grants.");
    pickingFiles++;
    try {
      const grant = await call<{ grantId: string; displayName: string; mediaType: string } | null>(write ? "file.pickWrite" : "file.pickRead", options);
      if (!grant) return null;
      if (closed) { release("file.release", { grantId: grant.grantId }); throw new PluginCapabilityError("plugin_surface_closed", "Plugin surface closed."); }
      files.add(grant.grantId);
      let used = false;
      const begin = (): void => {
        if (used || !files.has(grant.grantId)) throw new PluginCapabilityError("plugin_file_denied", "File grant is unavailable.");
        used = true;
      };
      const finish = (): void => { files.delete(grant.grantId); release("file.release", { grantId: grant.grantId }); };
      if (write) return { ...grant, write: async (content: Uint8Array): Promise<void> => {
        begin();
        try {
          if (content.byteLength > 1024 * 1024) throw new PluginCapabilityError("plugin_file_limit", "File exceeds 1 MiB.");
          let offset = 0;
          do {
            const bytes = content.subarray(offset, offset + 256 * 1024);
            let binary = ""; for (const byte of bytes) binary += String.fromCharCode(byte);
            const end = offset + bytes.length;
            const receipt = await call<{ offset?: number; committed?: boolean; bytes?: number }>("file.write",
              { grantId: grant.grantId, offset, base64: btoa(binary), finish: end === content.byteLength });
            if (end === content.byteLength ? receipt.committed !== true || receipt.bytes !== end : receipt.offset !== end)
              throw new PluginCapabilityError("plugin_commit_unknown", "File receipt is unknown; do not replay.");
            offset = end;
          } while (offset < content.byteLength);
        } finally { finish(); }
      } };
      return { ...grant, read: async (): Promise<Uint8Array> => {
        begin();
        try {
          const blocks: Uint8Array[] = []; let length = 0;
          for (;;) {
            const block = await call<{ base64: string; eof: boolean }>("file.read", { grantId: grant.grantId });
            const binary = atob(block.base64);
            if (binary.length > 256 * 1024 || length + binary.length > 1024 * 1024 || typeof block.eof !== "boolean" || binary.length === 0 && !block.eof)
              throw new PluginCapabilityError("plugin_file_limit", "Invalid file block.");
            const bytes = Uint8Array.from(binary, value => value.charCodeAt(0)); blocks.push(bytes); length += bytes.length;
            if (block.eof) break;
          }
          const content = new Uint8Array(length); let offset = 0;
          for (const block of blocks) { content.set(block, offset); offset += block.length; }
          return content;
        } finally { finish(); }
      } };
    } finally { pickingFiles--; }
  }
  async function startIo<T>(method: string, params: unknown): Promise<SurfaceIoTask<T> | null> {
    if (ioTasks.size + ioStarting >= 8) throw new PluginCapabilityError("plugin_concurrency_limit", "Too many DataIO tasks.");
    ioStarting++;
    let started: { taskId: string } | null;
    try { started = await call<{ taskId: string } | null>(method, params); } finally { ioStarting--; }
    if (!started) return null;
    if (closed) { release("data.io.cancel", { taskId: started.taskId }); throw new PluginCapabilityError("plugin_surface_closed", "Plugin surface closed."); }
    let running: Promise<T> | undefined;
    let cancelling: Promise<void> | undefined;
    let cancellationRequest: Promise<void> | undefined;
    let terminal: SurfaceIoStatus<T> | undefined;
    const requestCancellation = (): Promise<void> => {
      if (terminal) return Promise.resolve();
      if (closed) return Promise.reject(new PluginCapabilityError("plugin_surface_closed", "Plugin surface closed."));
      // Share the acknowledgement without taking over result()'s progress polling.
      return cancellationRequest ??= call<void>("data.io.cancel", { taskId: started.taskId }).catch(error => {
        cancellationRequest = undefined; throw error;
      });
    };
    const task: SurfaceIoTask<T> = {
      taskId: started.taskId,
      status: async () => { if (terminal) return terminal;
        const status = await call<SurfaceIoStatus<T>>("data.io.status", { taskId: started.taskId });
        if (status.state !== "queued" && status.state !== "running") { terminal = status; ioTasks.delete(started.taskId); } return status; },
      cancel: () => {
        if (terminal) return Promise.resolve();
        return cancelling ??= (async (): Promise<void> => {
          let settled = false;
          try {
            // The Host acknowledgement settles the cancellation itself; it is never
            // treated as a terminal state of the worker.
            await requestCancellation();
            // Track to a stable terminal so status() records the terminal and releases
            // the handle exactly once; a cancel-only caller therefore cannot pin the
            // task slots. A failed poll surfaces here and keeps the handle owned — no
            // silent stop, no fake release, no retry loop and no extra budget: one
            // poll is in flight at a time. The coalesced promise resets so a later
            // explicit cancel() restarts the tracking after a reported failure.
            while (!closed && terminal === undefined && ioTasks.has(started.taskId)) {
              const status = await task.status();
              if (status.state !== "queued" && status.state !== "running") { settled = true; return; }
              await new Promise<void>(resolve => setTimeout(resolve, 100));
            }
            // Only a recorded terminal is a stable settlement; a surface that closed
            // mid-tracking (including during the poll sleep) never releases silently.
            // A terminal tracked by any path stays sticky: the resolved promise persists.
            if (terminal !== undefined) { settled = true; return; }
            throw new PluginCapabilityError("plugin_surface_closed", "Plugin surface closed before the DataIO terminal receipt.");
          } finally { if (!settled) { cancelling = undefined; cancellationRequest = undefined; } }
        })();
      },
      result: (signal, progress) => running ??= (async (): Promise<T> => {
        let cancelSent = false;
        for (;;) {
          if (signal?.aborted && !cancelSent) {
            cancelSent = true;
            try { await requestCancellation(); }
            catch (error) {
              // The cancel acknowledgement follows the same export-closure mapping as the status
              // poll: an export whose terminal receipt never arrived stays unknown
              // instead of surfacing the raw transport closure.
              if (method === "data.export.start" && closed) throw new PluginCapabilityError("plugin_commit_unknown", "Surface closed before output receipt; no automatic replay.");
              throw error;
            }
          }
          let status: SurfaceIoStatus<T>;
          try { status = await task.status(); }
          catch (error) { if (method === "data.export.start" && closed) throw new PluginCapabilityError("plugin_commit_unknown", "Surface closed before output receipt; no automatic replay."); throw error; }
          if (progress) { try { progress(status.progress); } catch { /* Progress callbacks do not own task execution. */ } }
          if (status.state === "succeeded") {
            ioTasks.delete(started.taskId);
            return status.result as T;
          }
          if (status.state !== "queued" && status.state !== "running") {
            ioTasks.delete(started.taskId);
            // The Host's terminal receipt carries a stable error code (for example
            // plugin_plan_limit); keep it and fall back only when it is absent.
            const code = status.state === "aborted" || status.outcome === "unknown" ? "plugin_commit_unknown"
              : status.state === "cancelled" ? "plugin_cancel_requested"
              : status.error?.code || "plugin_data_io_failed";
            throw new PluginCapabilityError(code, "DataIO task did not complete; no automatic replay.");
          }
          await new Promise<void>(resolve => setTimeout(resolve, 100));
        }
      })(),
    };
    ioTasks.set(started.taskId, task);
    return task;
  }
  async function readAttachment(attachmentId: string): Promise<Uint8Array> {
    const blocks: Uint8Array[] = []; let length = 0;
    for (;;) {
      const block = await call<{ base64: string; eof: boolean }>("attachments.read", { attachmentId, offset: length });
      const binary = atob(block.base64);
      if (binary.length > 256 * 1024 || length + binary.length > 1024 * 1024 || typeof block.eof !== "boolean" || binary.length === 0 && !block.eof)
        throw new PluginCapabilityError("plugin_file_limit", "Invalid attachment block.");
      blocks.push(Uint8Array.from(binary, value => value.charCodeAt(0))); length += binary.length;
      if (block.eof) break;
    }
    const output = new Uint8Array(length); let offset = 0;
    for (const block of blocks) { output.set(block, offset); offset += block.length; }
    return output;
  }
  return {
    model: createSurfaceModel(call),
    context: {
      read: async () => {
        const context = await call<CommandContext>("context.read", {});
        // Trusted parent supplies presentation only; Go remains the data authority.
        if (!presentation.available) return context;
        const current = await presentation.current();
        return { ...context, theme: current.mode, locale: current.locale, density: current.density };
      },
      current: presentation.current, onChanged: presentation.onChanged,
    },
    tasks: presentation.tasks,
    ui: presentation.ui,
    data: { describe: request => call("data.describe", request), query, releaseCursor, subscribe, unsubscribe,
      async *pages(request, signal) {
        const snapshot = structuredClone(request);
        let cursor: string | undefined;
        try {
          do {
            const page = await query({ ...snapshot, ...(cursor ? { cursor } : {}) }, signal);
            cursor = page.nextCursor ?? undefined;
            yield page;
          } while (cursor);
        } finally { if (cursor) await releaseCursor(cursor); }
      },
      previewImport: request => {
        // Upsert requires its approved key up front: a null key would otherwise be
        // silently normalized as updates until the authoritative mint rejects it.
        if ((request.mode ?? "create_only") === "upsert" && (typeof request.upsertKey !== "string" || request.upsertKey === ""))
          throw new PluginCapabilityError("plugin_capability_invalid", "Upsert imports require a non-empty upsertKey.");
        return startIo("data.import.start", { ...request, mode: request.mode ?? "create_only", upsertKey: request.upsertKey ?? null, columnMapping: request.columnMapping ?? [] });
      },
      export: request => startIo("data.export.start", { ...request, query: request.query ?? {} }) },
    file: { pickRead: options => pick(false, { mediaTypes: options?.mediaTypes ?? [] }) as Promise<ReadGrant | null>,
      pickWrite: options => pick(true, options) as Promise<WriteGrant | null> },
    attachments: { list: async target => (await call<{ items: SurfaceAttachment[] }>("attachments.list", target)).items,
      read: readAttachment, previewChange: request => call("attachments.previewChange", { ...request, addFile: request.addFile ?? false, removeAttachmentIds: request.removeAttachmentIds ?? [] }) },
    storage: { get: key => call("storage.get", { key }), set: (key, value) => call("storage.set", { key, value }), delete: key => call("storage.delete", { key }) },
    mutation: { preview: plan => call("mutation.preview", plan), submit: (planId, signal) => call("mutation.submit", { planId }, signal) },
    output: { print: signal => call("output.print", {}, signal) },
    dispose: () => {
      if (closed) return;
      closed = true;
      presentation.dispose();
      for (const [subscriptionId, subscription] of subscriptions) {
        subscription.cleanup(); release("data.unsubscribe", { subscriptionId });
      }
      subscriptions.clear(); early.clear();
      for (const grantId of files) release("file.release", { grantId });
      files.clear();
      for (const [cursor, owned] of cursors) if (!owned.release) release("data.releaseCursor", { cursor });
      cursors.clear();
      for (const taskId of ioTasks.keys()) release("data.io.cancel", { taskId });
      ioTasks.clear();
      channel.removeEventListener("message", receive);
      // A sent submit may already be committing; dispose reports unknown and resends nothing.
      for (const [requestId, request] of pending) {
        if (["data.query", "data.subscribe", "file.pickRead", "file.pickWrite", "data.import.start", "data.export.start", "attachments.previewChange"].includes(request.method)) release("request.cancel", { requestId });
        clearTimeout(request.timer);
        request.cleanup();
        request.reject((request.method === "mutation.submit" || request.method === "file.write" || request.method === "model.configure")
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
