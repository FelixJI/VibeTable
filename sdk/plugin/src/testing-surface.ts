import { PluginCapabilityError } from "./capabilities.js";
import { createOfflineHost, type OfflineHostOptions } from "./testing.js";
import { SURFACE_API_CONTRACT, type SurfaceTransport, type SurfaceMutationPlan, type SurfaceMutationOutcome } from "./surface.js";
import type { JsonValue } from "./types.js";

export interface OfflineSurfaceOptions extends OfflineHostOptions {
  /** Trusted synthetic identities, never accepted from request payloads. */
  readonly pluginId: string;
  readonly workspaceId: string;
  readonly surfaceToken: string;
  readonly storage?: Map<string, JsonValue>;
  readonly risk?: "read" | "write" | "destructive";
  /** Explicit synthetic authority ports. No default successful mutation or query. */
  readonly previewMutation?: (plan: SurfaceMutationPlan) => Promise<void>;
  readonly submitMutation?: (plan: SurfaceMutationPlan, signal: AbortSignal) => Promise<SurfaceMutationOutcome>;
}
function fail(code: string): never { throw new PluginCapabilityError(code, "Synthetic surface request rejected."); }
const object = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === "object" && !Array.isArray(value);

/** Test-only message transport. Query validation/cursors reuse the independently
 * conformed data host; callers supply real producer corpus pages, not a second query engine.
 * Does not model native files, UI approval, SQL, or production authorization itself. */
export function createOfflineSurfaceHost(options: OfflineSurfaceOptions) {
  const host = createOfflineHost({ ...options, pluginApi: options.pluginApi ?? "3.x" });
  const listeners = new Set<(event: { readonly data: unknown }) => void>();
  const requests = new Map<string, AbortController>();
  const plans = new Map<string, SurfaceMutationPlan>();
  const cursors = new Map<string, string>();
  const storage = options.storage ?? new Map<string, JsonValue>();
  let retired = false;
  const reply = (requestId: string, result?: unknown, error?: unknown): void => {
    const data = { contract: SURFACE_API_CONTRACT, requestId, ...(error ? { error } : { result }) };
    for (const listener of listeners) listener({ data });
  };
  async function dispatch(method: string, params: Record<string, unknown>, signal: AbortSignal, requestId: string): Promise<unknown> {
    if ((options.pluginApi ?? "3.x") !== "3.x") fail("plugin_api_unsupported");
    if (method === "context.read") return host.capabilities.context.read();
    if (method === "data.describe") return host.capabilities.data.describe(params as unknown as Parameters<typeof host.capabilities.data.describe>[0]);
    if (method === "data.releaseCursor") {
      if (Object.keys(params).length !== 1 || typeof params.cursor !== "string" || !params.cursor.trim()) fail("plugin_capability_invalid");
      host.releaseCursor(params.cursor); cursors.delete(params.cursor); return { released: true };
    }
    if (method === "data.query") {
      const page = await host.capabilities.data.query(params as unknown as Parameters<typeof host.capabilities.data.query>[0]);
      if (typeof params.cursor === "string") cursors.delete(params.cursor);
      if (page.nextCursor) cursors.set(page.nextCursor, requestId);
      if (signal.aborted || retired) {
        if (page.nextCursor) { host.releaseCursor(page.nextCursor); cursors.delete(page.nextCursor); }
        fail("plugin_cancel_requested");
      }
      const guards = options.rowGuards?.[String(params.collection)] ?? {};
      return { ...page, rowGuards: Array.isArray(params.fields) && params.fields.includes("id")
        ? Object.fromEntries(page.items.flatMap(row => typeof row.id === "string" && guards[row.id] ? [[row.id, guards[row.id]]] : [])) : {} };
    }
    if (method.startsWith("storage.")) {
      if (!options.permissions?.privateStorage) fail("plugin_storage_denied");
      if (typeof params.key !== "string" || !/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/u.test(params.key)) fail("plugin_capability_invalid");
      const key = JSON.stringify([options.workspaceId, options.pluginId, params.key]);
      if (method === "storage.get" && Object.keys(params).length === 1) return structuredClone(storage.get(key) ?? null);
      if (method === "storage.delete" && Object.keys(params).length === 1) { storage.delete(key); return null; }
      if (method === "storage.set" && Object.keys(params).length === 2 && Object.hasOwn(params, "value")) {
        if (new TextEncoder().encode(JSON.stringify(params.value)).length > 65536) fail("plugin_capability_invalid");
        storage.set(key, structuredClone(params.value) as JsonValue); return null;
      }
      fail("plugin_capability_invalid");
    }
    if (method === "mutation.preview") {
      if (!["write", "destructive"].includes(options.risk ?? "read")) fail("plugin_write_denied");
      if (Object.keys(params).some(key => !["contract", "collection", "schemaRevision", "operations"].includes(key))
        || params.contract !== "vibetable.surface-mutation-plan.v1" || typeof params.collection !== "string"
        || typeof params.schemaRevision !== "string" || !Array.isArray(params.operations)
        || params.operations.length < 1 || params.operations.length > 1000) fail("plugin_capability_invalid");
      const description = options.descriptions?.[params.collection];
      if (!description || description.schemaRevision !== params.schemaRevision) fail("plugin_cursor_stale");
      for (const operation of params.operations) {
        if (!object(operation) || !["create", "update", "delete"].includes(String(operation.kind))) fail("plugin_capability_invalid");
        const permission = options.permissions?.data?.find(item => (item.collection === params.collection || item.collection === "$active" && params.collection === options.context?.collection)
          && item.operations.some(kind => kind === operation.kind));
        if (!permission || operation.kind !== "create" && !permission.fields.some(field => ["id", "*", "$configured"].includes(field))) fail("plugin_write_denied");
        if (operation.kind !== "create" && (typeof operation.primaryKey !== "string" || !operation.primaryKey || typeof operation.expectedDigest !== "string" || !operation.expectedDigest)) fail("plugin_capability_invalid");
        if (Object.keys(operation).some(key => !(operation.kind === "delete" ? ["kind", "primaryKey", "expectedDigest"] : ["kind", "primaryKey", "expectedDigest", "values"]).includes(key))) fail("plugin_capability_invalid");
        if (operation.kind !== "delete" && (!object(operation.values) || Object.keys(operation.values).some(field => !permission.fields.some(grant => grant === "*" || grant === "$configured" || grant === field)))) fail("plugin_write_denied");
        if (operation.kind !== "delete" && object(operation.values)) {
          for (const fieldId of Object.keys(operation.values)) {
            const field = description.fields.find(item => item.fieldId === fieldId);
            if (!field || field.readonly) fail("plugin_write_denied");
          }
        }
      }
      if (plans.size >= 8) fail("plugin_plan_limit");
      if (!options.previewMutation) fail("plugin_capability_invalid");
      const plan = params as unknown as SurfaceMutationPlan;
      await options.previewMutation(plan);
      if (signal.aborted) fail("plugin_cancel_requested");
      const planId = crypto.randomUUID(); plans.set(planId, structuredClone(plan));
      return { planId, affectedCount: plan.operations.length };
    }
    if (method === "mutation.submit") {
      if (Object.keys(params).length !== 1 || typeof params.planId !== "string") fail("plugin_capability_invalid");
      const plan = plans.get(params.planId); plans.delete(params.planId);
      if (!plan) fail("plugin_plan_stale");
      if (!options.submitMutation) fail("plugin_capability_invalid");
      return options.submitMutation(plan, signal);
    }
    return fail("plugin_capability_invalid");
  }
  const transport: SurfaceTransport = {
    postMessage(message) {
      if (!object(message) || message.contract !== SURFACE_API_CONTRACT || typeof message.requestId !== "string") return;
      const requestId = message.requestId;
      if (retired || message.surfaceToken !== options.surfaceToken) { reply(requestId, undefined, { code: "plugin_source_denied", message: "Surface retired or source invalid." }); return; }
      if (Object.keys(message).some(key => !["contract", "surfaceToken", "requestId", "method", "params"].includes(key)) || !object(message.params)) { reply(requestId, undefined, { code: "plugin_capability_invalid", message: "Invalid envelope." }); return; }
      if (message.method === "request.cancel") {
        const target = String(message.params.requestId); requests.get(target)?.abort();
        for (const [cursor, owner] of cursors) if (owner === target) { host.releaseCursor(cursor); cursors.delete(cursor); }
        return;
      }
      const controller = new AbortController(); requests.set(requestId, controller);
      void dispatch(String(message.method), message.params, controller.signal, requestId).then(result => reply(requestId, result), error => reply(requestId, undefined, { code: object(error) && typeof error.code === "string" ? error.code : "plugin_request_failed", message: "Synthetic authority rejected request." })).finally(() => requests.delete(requestId));
    },
    addEventListener(_type, listener) { listeners.add(listener); },
    removeEventListener(_type, listener) { listeners.delete(listener); },
  };
  return { transport, get cursorCount() { return host.cursorCount; }, retire() { retired = true; for (const cursor of cursors.keys()) host.releaseCursor(cursor); cursors.clear(); plans.clear(); for (const request of requests.values()) request.abort(); } };
}
