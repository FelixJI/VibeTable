import type { DataCatalog, DataDescription, DataDescribeRequest, DataQueryPage, DataQueryRequest } from "./data.js";
import { PluginCapabilityError } from "./capabilities.js";
import type { CommandContext } from "./types.js";

export interface DataGrant {
  readonly collection: string;
  readonly operations: readonly ("read" | "query" | "create" | "update" | "delete")[];
  readonly fields: readonly string[];
}

export interface OfflineHostOptions {
  readonly context?: Partial<CommandContext>;
  readonly permissions?: {
    readonly data?: readonly DataGrant[];
    readonly files?: readonly ("pickRead" | "pickWrite")[];
    readonly privateStorage?: boolean;
  };
  /** API the synthetic plugin declared; v2 data methods require "2.x"/"3.x". */
  readonly pluginApi?: "2.x" | "3.x";
  /** Synthetic Go-produced metadata; no logical type inference. */
  readonly descriptions?: Readonly<Record<string, DataDescription>>;
  /** Closed synthetic port: supply Go conformance outputs, never business data. */
  readonly queryAdapter?: (request: DataQueryRequest) => Promise<DataQueryPage>;
  readonly revision?: () => string;
  /** Go-owned guards supplied as test inputs; this helper never hashes rows. */
  readonly rowGuards?: Readonly<Record<string, Readonly<Record<string, string>>>>;
}

/** The conformed data surface consumed by `createOfflineSurfaceHost`. */
export interface OfflineDataCapabilities {
  readonly context: { read(): Promise<CommandContext> };
  readonly data: {
    describe(request: DataDescribeRequest): Promise<DataCatalog | DataDescription>;
    query(request: DataQueryRequest): Promise<DataQueryPage>;
  };
}

export interface OfflineHost {
  readonly capabilities: OfflineDataCapabilities;
  /** Test-only view/release of surface-owned query handles. */
  readonly cursorCount: number;
  releaseCursor(cursor: string): void;
  setContext(patch: Partial<CommandContext>): void;
  /** Host-level cancel; it only feeds capabilities used outside an execution. */
  requestCancel(): void;
  /** One isolated execution: cancel and cursor state never crosses executions. */
  createExecution(): OfflineExecution;
}

export interface OfflineExecution {
  readonly capabilities: OfflineDataCapabilities;
  requestCancel(): void;
}

/** Cancel flag and per-execution cursor ownership. */
interface ExecutionState {
  cancelRequested: boolean;
  cursors: Map<string, { binding: string; cursor: string; revision: string; expires: number }>;
}

function reject(code: string, message: string): never {
  throw new PluginCapabilityError(code, message);
}

function objectValue(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

// Closed request shape: every present key must be part of the wire contract.
function wireObject(value: unknown, fields: readonly string[]): value is Record<string, unknown> {
  return objectValue(value) && Object.keys(value).every(key => fields.includes(key));
}

function arrayOf(value: unknown, member: (item: unknown) => boolean): boolean {
  return value === undefined || (Array.isArray(value) && value.every(member));
}

// The real Host binds cursors with JsonNode.DeepEquals: object member order is
// irrelevant while array order and exact values stay significant. Mirror that
// structurally instead of comparing JSON.stringify insertion order.
function jsonEqual(left: unknown, right: unknown): boolean {
  if (left === right) return true;
  if (typeof left !== "object" || typeof right !== "object" || left === null || right === null) return false;
  if (Array.isArray(left) || Array.isArray(right)) {
    return Array.isArray(left) && Array.isArray(right) && left.length === right.length
      && left.every((item, index) => jsonEqual(item, right[index]));
  }
  const keys = Object.keys(left);
  return keys.length === Object.keys(right).length
    && keys.every(key => Object.hasOwn(right, key) && jsonEqual((left as Record<string, unknown>)[key], (right as Record<string, unknown>)[key]));
}

export function createOfflineHost(options: OfflineHostOptions = {}): OfflineHost {
  let context: CommandContext = {
    contract: "vibetable.command-context.v1", projectKey: "offline:test", collection: null,
    selectedKeys: [], querySnapshot: null, locale: "zh-CN", theme: "light",
    density: "comfortable", user: {}, hostVersion: "unknown", ...options.context,
  };
  const grant = (collection: string, operation: "read" | "query") =>
    options.permissions?.data?.find(item => item.operations.includes(operation)
      && (item.collection === collection || (item.collection === "$active" && collection === context.collection)));
  const requireV2 = (state: ExecutionState) => {
    if (!["2.x", "3.x"].includes(options.pluginApi ?? "1.x")) reject("plugin_api_unsupported", "plugin API v2 was not declared");
    if (state.cancelRequested) reject("plugin_cancel_requested", "plugin cancellation requested");
  };
  const requireQueryGrant = (collection: string) => {
    const permission = grant(collection, "query");
    if (!permission?.operations.includes("read")) reject("plugin_read_denied", "plugin read permission is unavailable");
    return permission;
  };
  const capabilitiesFor = (state: ExecutionState): OfflineDataCapabilities => ({
    context: { read: async () => structuredClone(context) },
    data: {
      async describe(request: DataDescribeRequest): Promise<DataCatalog | DataDescription> {
        requireV2(state);
        if (!wireObject(request, ["accepts", "collection"])
          || JSON.stringify(request.accepts) !== '["vibetable.plugin-data.v2"]') {
          reject("plugin_api_unsupported", "plugin data contract is unavailable");
        }
        const descriptions = Object.values(options.descriptions ?? {});
        if (request.collection === undefined) {
          return { contract: "vibetable.plugin-data.v2", tables: descriptions
            .filter(table => grant(table.collection, "query")?.operations.includes("read"))
            .map(({ collection, displayName, schemaRevision }) => ({ collection, displayName, schemaRevision })) };
        }
        const permission = requireQueryGrant(request.collection);
        const table = options.descriptions?.[request.collection];
        if (!table) reject("plugin_read_denied", "plugin read permission is unavailable");
        return { ...table!, fields: table!.fields.filter(field => permission.fields.includes("*")
          || permission.fields.includes("$configured") || permission.fields.includes(field.fieldId)) };
      },
      async query(request: DataQueryRequest): Promise<DataQueryPage> {
        requireV2(state);
        if (!wireObject(request, ["contract", "collection", "fields", "ids", "filters", "sorts", "pageSize", "cursor"])
          || request.contract !== "vibetable.plugin-query.v2" || !Array.isArray(request.fields)
          || request.fields.some(field => typeof field !== "string")) reject("plugin_capability_invalid", "plugin query request is invalid");
        const permission = requireQueryGrant(request.collection);
        if (["ids", "filters", "sorts", "pageSize"].some(key => (request as unknown as Record<string, unknown>)[key] === null)) reject("plugin_capability_invalid", "plugin query request is invalid");
        const size = request.pageSize ?? 100;
        if (!Number.isInteger(size) || size < 1 || size > 200 || request.ids !== undefined
          && (!Array.isArray(request.ids) || request.ids.length > 200)) reject("plugin_query_limit", "plugin query limit exceeded");
        const fields = options.descriptions?.[request.collection]?.fields ?? [];
        const requireField = (id: string) => {
          const field = fields.find(item => item.fieldId === id);
          if (!field || !(permission.fields.includes("*") || permission.fields.includes("$configured")
            || permission.fields.includes(id)) || id.includes(".")) reject("plugin_read_denied", "plugin read permission is unavailable");
          return field;
        };
        request.fields.forEach(requireField);
        if (request.ids !== undefined) requireField("id");
        const walk = (filters: NonNullable<DataQueryRequest["filters"]>) => {
          if (!Array.isArray(filters)) reject("plugin_capability_invalid", "plugin query filters are invalid");
          for (const filter of filters) {
            if (!wireObject(filter as unknown, ["field", "operator", "value", "logic", "filters", "groupLogic"])) reject("plugin_capability_invalid", "plugin query filter is invalid");
            if (filter.filters?.length) { walk(filter.filters); continue; }
            const field = requireField(filter.field ?? "");
            if (["formula", "lookup"].includes(field.logicalType)) reject("plugin_query_computed_unsupported", "computed predicates are unavailable");
            if (!field.filterOperators.includes(filter.operator!)) reject("query.operator.unsupported", "plugin query operator is unavailable");
          }
        };
        walk(request.filters ?? []);
        if (request.sorts !== undefined && !Array.isArray(request.sorts)) reject("plugin_capability_invalid", "plugin query sorts are invalid");
        for (const sort of request.sorts ?? []) {
          if (!wireObject(sort as unknown, ["field", "direction", "nullsLast"])) reject("plugin_capability_invalid", "plugin query sort is invalid");
          if (!requireField(sort.field).sortable) reject("query.cursor.unsupported_sort", "plugin query sort is unavailable");
        }
        for (const [handle, saved] of state.cursors) if (saved.expires < Date.now()) state.cursors.delete(handle);
        const { cursor, ...query } = request;
        const revision = `${context.projectKey}:${context.collection}:${options.descriptions?.[request.collection]?.schemaRevision}:${options.revision?.() ?? "1"}`;
        const binding = JSON.stringify(query);
        let previous: string | undefined;
        if (cursor !== undefined) {
          const saved = state.cursors.get(cursor);
          state.cursors.delete(cursor);
          if (!saved || !jsonEqual(JSON.parse(saved.binding), JSON.parse(binding))) reject("plugin_cursor_invalid", "plugin cursor is invalid");
          if (saved.revision !== revision) reject("plugin_cursor_stale", "plugin cursor is stale");
          previous = saved.cursor;
        }
        if (!options.queryAdapter) reject("plugin_query_failed", "synthetic query adapter is unavailable");
        const page = await options.queryAdapter(previous === undefined ? query : { ...query, cursor: previous });
        if (state.cancelRequested) reject("plugin_cancel_requested", "plugin cancellation requested");
        if (new TextEncoder().encode(JSON.stringify(page)).length > 1_048_576) reject("plugin_query_limit", "plugin response budget exceeded");
        let nextCursor: string | null = null;
        if (page.nextCursor !== null) {
          if (state.cursors.size >= 64) reject("plugin_query_limit", "plugin cursor handle budget exceeded");
          nextCursor = globalThis.crypto.randomUUID();
          state.cursors.set(nextCursor, { binding, cursor: page.nextCursor, revision, expires: Date.now() + 300000 });
        }
        return { ...page, nextCursor };
      },
    },
  });
  const hostState: ExecutionState = { cancelRequested: false, cursors: new Map() };
  return {
    capabilities: capabilitiesFor(hostState),
    get cursorCount() { return hostState.cursors.size; },
    releaseCursor(cursor) { hostState.cursors.delete(cursor); },
    setContext(patch) { context = { ...context, ...patch }; },
    requestCancel() { hostState.cancelRequested = true; },
    createExecution() {
      const state: ExecutionState = { cancelRequested: false, cursors: new Map() };
      return {
        capabilities: capabilitiesFor(state),
        requestCancel() { state.cancelRequested = true; },
      };
    },
  };
}

export { createOfflineSurfaceHost } from "./testing-surface.js";
export type { OfflineSurfaceOptions } from "./testing-surface.js";
