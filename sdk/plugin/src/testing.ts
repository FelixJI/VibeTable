import {
  createCapabilityClient, PluginCapabilityError,
  type DataPage, type DataReadRequest, type MutationPlan, type PluginCapabilities,
  type PluginProgress,
} from "./capabilities.js";
import { cancelled, failure } from "./results.js";
import type { CommandContext, JsonObject, JsonValue, PluginAction, PluginResult } from "./types.js";

export interface DataGrant {
  readonly collection: string;
  readonly operations: readonly ("read" | "create" | "update")[];
  readonly fields: readonly string[];
}

export interface OfflineHostOptions {
  readonly context?: Partial<CommandContext>;
  readonly permissions?: {
    readonly data?: readonly DataGrant[];
    readonly files?: readonly ("pickRead" | "pickWrite")[];
    readonly privateStorage?: boolean;
  };
  readonly collections?: Readonly<Record<string, readonly JsonObject[]>>;
  readonly fields?: Readonly<Record<string, readonly string[]>>;
  /** Synthetic product profile; write tests must configure each used operation. */
  readonly writableFields?: Readonly<Record<string, {
    readonly create?: readonly string[];
    readonly update?: readonly string[];
  }>>;
  /** Go-owned guards supplied as test inputs; this helper never hashes rows. */
  readonly rowGuards?: Readonly<Record<string, Readonly<Record<string, string>>>>;
  readonly readFiles?: readonly { readonly name: string; readonly mediaType: string; readonly content: Uint8Array }[];
  readonly approveMutation?: boolean | ((plan: MutationPlan) => boolean | Promise<boolean>);
  /** Synthetic commit boundary: never connect this helper to a business database. */
  readonly applyMutation?: (plan: MutationPlan) => Promise<PluginResult>;
}

export interface OfflineHost {
  readonly capabilities: PluginCapabilities;
  readonly mutationPlans: readonly MutationPlan[];
  readonly progressEvents: readonly PluginProgress[];
  readonly writtenFiles: ReadonlyMap<string, Uint8Array>;
  setContext(patch: Partial<CommandContext>): void;
  /** Host-level cancel; it only feeds capabilities used outside an execution. */
  requestCancel(): void;
  /** One isolated execution: cancel/progress state never crosses executions. */
  createExecution(): OfflineExecution;
  finalize(plan: MutationPlan, onSubmit: () => void, risk?: "read" | "write" | "destructive"): Promise<PluginResult>;
}

export interface OfflineExecution {
  readonly capabilities: PluginCapabilities;
  requestCancel(): void;
  finalize(plan: MutationPlan, onSubmit: () => void, risk?: "read" | "write" | "destructive"): Promise<PluginResult>;
}

/** Cancel flag and monotonic progress baseline, scoped to a single execution. */
interface ExecutionState {
  cancelRequested: boolean;
  lastProgress: number;
}

export interface OfflineRun {
  readonly result: Promise<PluginResult>;
  cancel(reason?: string): void;
}

function reject(code: string, message: string): never {
  throw new PluginCapabilityError(code, message);
}

// Match the closed Python return models after the Worker's JSON transport.
function wireObject(value: unknown, fields: readonly string[]): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    && Object.keys(value).every(key => fields.includes(key));
}

function objectValue(value: unknown): value is JsonObject {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function arrayOf(value: unknown, member: (item: unknown) => boolean): boolean {
  return value === undefined || (Array.isArray(value) && value.every(member));
}

function wireInteger(value: unknown): number | undefined {
  // Pydantic's non-strict int fields also accept booleans and decimal strings.
  const number = typeof value === "boolean" ? Number(value)
    : typeof value === "string" && /^[+-]?\d+(?:_\d+)*(?:\.0+)?$/u.test(value.trim())
      ? Number(value.replaceAll("_", "")) : value;
  return typeof number === "number" && Number.isInteger(number) ? number : undefined;
}

function resultFromWire(value: unknown): PluginResult {
  if (!wireObject(value, ["contract", "status", "summary", "metrics", "table", "artifacts", "refresh", "warnings"])
    || (value.contract !== undefined && value.contract !== "vibetable.plugin-result.v1")
    || (value.status !== "success" && value.status !== "warning" && value.status !== "error") || typeof value.summary !== "string"
    || !arrayOf(value.metrics, metric => wireObject(metric, ["label", "value"])
      && typeof metric.label === "string" && ["string", "number", "boolean"].includes(typeof metric.value))
    || (value.table !== undefined && value.table !== null && !objectValue(value.table))
    || !arrayOf(value.artifacts, objectValue)
    || (value.refresh !== undefined && value.refresh !== null && !objectValue(value.refresh))
    || !arrayOf(value.warnings, item => typeof item === "string")) {
    reject("plugin_action_failed", "invalid plugin action result");
  }
  return { contract: "vibetable.plugin-result.v1", ...value,
    ...(Array.isArray(value.metrics) ? { metrics: value.metrics.map((metric: { label: string; value: string | number | boolean }) => ({
      ...metric, value: typeof metric.value === "boolean" ? Number(metric.value) : metric.value,
    })) } : {}),
  } as unknown as PluginResult;
}

function planFromWire(value: unknown): MutationPlan {
  const invalid = () => reject("plugin_worker_failed", "invalid plugin mutation plan");
  // populate_by_name accepts Python field names too; duplicate aliases are extras.
  const field = (record: Record<string, unknown>, camel: string, snake: string) => {
    if (Object.hasOwn(record, camel) && Object.hasOwn(record, snake)) invalid();
    return Object.hasOwn(record, camel) ? record[camel] : record[snake];
  };
  if (!wireObject(value, ["contract", "collection", "operations", "preview", "idempotencyKey", "idempotency_key"])
    || (value.contract !== undefined && value.contract !== "vibetable.mutation-plan.v1")
    || typeof value.collection !== "string" || !Array.isArray(value.operations)
    || value.operations.length > 10_000
    || !wireObject(value.preview, ["summary", "sampleRows", "sample_rows", "affectedCount", "affected_count", "warnings"])) {
    return invalid();
  }
  const idempotencyKey = field(value, "idempotencyKey", "idempotency_key") ?? null;
  const sampleRows = field(value.preview, "sampleRows", "sample_rows");
  const count = field(value.preview, "affectedCount", "affected_count");
  const affectedCount = wireInteger(count === undefined ? 0 : count);
  if (typeof idempotencyKey !== "string" && idempotencyKey !== null
    || affectedCount === undefined || affectedCount < 0 || affectedCount !== value.operations.length
    || !arrayOf(value.preview.summary, objectValue) || !arrayOf(sampleRows, objectValue)
    || !arrayOf(value.preview.warnings, item => typeof item === "string")) return invalid();
  const operations = value.operations.map(operation => {
    if (!wireObject(operation, ["kind", "primaryKey", "primary_key", "expectedDateUpdated", "expected_date_updated", "expectedDigest", "expected_digest", "values"])
      || (operation.kind !== "create" && operation.kind !== "update") || !objectValue(operation.values)) return invalid();
    let primaryKey = field(operation, "primaryKey", "primary_key") ?? null;
    const expectedDateUpdated = field(operation, "expectedDateUpdated", "expected_date_updated") ?? null;
    const expectedDigest = field(operation, "expectedDigest", "expected_digest") ?? null;
    if (typeof primaryKey === "boolean") primaryKey = Number(primaryKey);
    if (primaryKey !== null && typeof primaryKey !== "string" && wireInteger(primaryKey) === undefined
      || expectedDateUpdated !== null && (typeof expectedDateUpdated !== "string" || !/^row_[0-9]{4,}$/u.test(expectedDateUpdated))
      || expectedDigest !== null && (typeof expectedDigest !== "string" || !/^sha256:[0-9a-f]{64}$/u.test(expectedDigest))) return invalid();
    return { kind: operation.kind, primaryKey, expectedDateUpdated, expectedDigest, values: operation.values };
  });
  return { contract: "vibetable.mutation-plan.v1", collection: value.collection, operations,
    preview: { summary: value.preview.summary ?? [], affectedCount,
      sampleRows: sampleRows ?? [], warnings: value.preview.warnings ?? [] }, idempotencyKey,
  } as unknown as MutationPlan;
}

export function createOfflineHost(options: OfflineHostOptions = {}): OfflineHost {
  let context: CommandContext = {
    contract: "vibetable.command-context.v1", projectKey: "offline:test", collection: null,
    selectedKeys: [], querySnapshot: null, locale: "zh-CN", theme: "light",
    density: "comfortable", user: {}, hostVersion: "unknown", ...options.context,
  };
  let readIndex = 0;
  const storage = new Map<string, JsonValue>();
  const mutationPlans: MutationPlan[] = [];
  const progressEvents: PluginProgress[] = [];
  const writtenFiles = new Map<string, Uint8Array>();
  const grant = (collection: string, operation: "read" | "create" | "update" | "write") =>
    options.permissions?.data?.find(item => (operation === "write"
      ? item.operations.some(kind => kind === "create" || kind === "update")
      : item.operations.includes(operation))
      && (item.collection === collection || (item.collection === "$active" && collection === context.collection)));
  const schemaFields = (collection: string) => options.fields?.[collection]
    ?? [...new Set(options.collections?.[collection]?.flatMap(row => Object.keys(row)) ?? [])];
  const allowedFields = (collection: string, fields: readonly string[]) =>
    fields.includes("*") || fields.includes("$configured")
      ? schemaFields(collection) : fields.filter(field => schemaFields(collection).includes(field));
  const requireStorage = (key: string) => {
    if (options.permissions?.privateStorage !== true) reject("plugin_worker_failed", "plugin did not declare privateStorage");
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/u.test(key)) reject("plugin_worker_failed", "private storage key is invalid");
  };
  const requireFile = (operation: "pickRead" | "pickWrite") => {
    if (!options.permissions?.files?.includes(operation)) reject("plugin_worker_failed", `plugin did not declare file.${operation}`);
  };
  const capabilitiesFor = (state: ExecutionState): PluginCapabilities => createCapabilityClient({
    async dataRead<T extends JsonObject>(request: DataReadRequest): Promise<DataPage<T>> {
      if (Object.keys(request).some(key => !["collection", "fields", "filter", "cursor", "pageSize"].includes(key))
        || typeof request.collection !== "string" || !request.collection
        || !Array.isArray(request.fields) || request.fields.some(field => typeof field !== "string")) {
        reject("plugin_worker_failed", "data.read request is invalid");
      }
      const permission = grant(request.collection, "read");
      if (!permission) reject("plugin_read_denied", `collection '${request.collection}' was not declared for read`);
      const fields = request.fields.length === 1 && request.fields[0] === "*"
        ? schemaFields(request.collection) : request.fields;
      const allowed = allowedFields(request.collection, permission.fields);
      if (fields.some(field => !allowed.includes(field))) reject("plugin_read_denied", "data.read fields were not declared");
      if (request.filter !== undefined && request.filter !== null
        && (typeof request.filter !== "object" || Array.isArray(request.filter) || Object.keys(request.filter).length)) {
        reject("plugin_filter_unsupported", "data.read filter is unavailable in plugin API v1");
      }
      const size = request.pageSize === undefined ? 100 : request.pageSize;
      if (!Number.isInteger(size)) reject("plugin_worker_failed", "data.read pageSize must be an integer");
      const pageSize = Math.min(Math.max(size, 1), 200);
      const cursor = request.cursor;
      if (cursor !== undefined && cursor !== null && cursor !== ""
        && ((typeof cursor !== "string" && typeof cursor !== "number") || !/^\s*[+-]?\d+(?:_\d+)*\s*$/u.test(String(cursor)))) {
        reject("plugin_worker_failed", "data.read cursor is invalid");
      }
      const offset = Number(String(cursor || 0).replaceAll("_", ""));
      if (!Number.isInteger(offset) || offset < 0) reject("plugin_worker_failed", "data.read cursor is invalid");
      const rows = (options.collections?.[request.collection] ?? []).slice(offset, offset + pageSize);
      const items = rows.map(row => Object.fromEntries(fields.map(field => [field, row[field] ?? null]))) as T[];
      const rowGuards = Object.fromEntries(rows.flatMap(row => {
        const guard = options.rowGuards?.[request.collection]?.[String(row.id)];
        return guard === undefined ? [] : [[String(row.id), guard]];
      }));
      return { items, nextCursor: items.length === pageSize ? String(offset + items.length) : null,
        totalRows: (options.collections?.[request.collection] ?? []).length, rowGuards };
    },
    async dataMutate() { return reject("plugin_direct_mutation_unsupported", "data.mutate cannot write directly; return a mutation plan from the action"); },
    async filePickRead() {
      requireFile("pickRead");
      const file = options.readFiles?.[readIndex++];
      if (!file) return null;
      return {
        grantId: `offline-read-${readIndex}`, displayName: file.name, mediaType: file.mediaType,
        async read() {
          if (file.content.length > 1_048_576) reject("plugin_capability_invalid", "selected plugin input file exceeds the host limit");
          return file.content.slice();
        },
      };
    },
    async filePickWrite(request) {
      requireFile("pickWrite");
      if (!request.suggestedName || !request.mediaType) reject("plugin_capability_invalid", "plugin write picker requires suggestedName and mediaType");
      return {
        grantId: `offline-write-${writtenFiles.size + 1}`, displayName: request.suggestedName,
        async write(content) {
          if (content.length > 1_048_576) reject("plugin_capability_invalid", "plugin file output exceeds the host limit");
          writtenFiles.set(request.suggestedName, content.slice());
        },
      };
    },
    async storageGet<T extends JsonValue>(key: string): Promise<T | null> {
      requireStorage(key);
      return structuredClone(storage.get(key) ?? null) as T | null;
    },
    async storageSet(key, value) {
      requireStorage(key);
      if (new TextEncoder().encode(JSON.stringify(value)).length > 65_536) reject("plugin_worker_failed", "private storage value exceeds the size limit");
      storage.set(key, structuredClone(value));
    },
    async storageDelete(key) { requireStorage(key); storage.delete(key); },
    async uiEmitResult() { return reject("plugin_emit_result_unsupported", "ui.emitResult is unsupported; return the final result from the action"); },
    async uiReportProgress(progress) {
      if (!Number.isInteger(progress.current) || !Number.isInteger(progress.total)
        || progress.current < 0 || progress.total < 0 || (progress.total > 0 && progress.current > progress.total)) {
        reject("plugin_capability_invalid", "progress is out of bounds");
      }
      state.lastProgress = Math.max(state.lastProgress, progress.current);
      progressEvents.push({ ...progress, current: state.lastProgress });
      return { cancelRequested: state.cancelRequested };
    },
    async contextRead() { return structuredClone(context); },
  });
  const finalizeFor = (state: ExecutionState) => async (
    raw: MutationPlan, onSubmit: () => void, risk: "read" | "write" | "destructive" = "write",
  ): Promise<PluginResult> => {
    const plan = planFromWire(raw);
    const permission = grant(plan.collection, "write");
    if (!permission) reject("plugin_worker_failed", "mutation permission was not declared");
    const writableProfile = options.writableFields?.[plan.collection];
    for (const operation of plan.operations) {
      if (!permission.operations.includes(operation.kind)
        || Object.keys(operation.values).some(field => !allowedFields(plan.collection, permission.fields).includes(field))) reject("plugin_worker_failed", "mutation permission was not declared");
      const writable = writableProfile?.[operation.kind];
      if (!writable) reject("plugin_action_failed", `synthetic writable profile is unavailable for ${operation.kind}`);
      if (Object.keys(operation.values).some(field => !writable.includes(field))) reject("plugin_action_failed", `fields are not allowed for ${operation.kind}`);
    }
    if (!writableProfile) reject("plugin_action_failed", "synthetic writable profile is unavailable");
    if (risk === "read") reject("plugin_action_failed", "read plugin must return a plugin result");
    if (context.collection !== null && plan.collection !== context.collection) reject("plugin_action_failed", "mutation plan collection is outside the action context");
    mutationPlans.push(structuredClone(plan));
    const decision = options.approveMutation ?? false;
    const approved = typeof decision === "function" ? await decision(plan) : decision;
    if (state.cancelRequested) reject("plugin_cancel_requested", "plugin cancellation requested");
    if (!approved) reject("plugin_mutation_rejected", "mutation plan was rejected");
    if (!options.applyMutation) reject("plugin_action_failed", "synthetic mutation adapter is unavailable");
    onSubmit();
    return options.applyMutation(plan);
  };
  const hostState: ExecutionState = { cancelRequested: false, lastProgress: 0 };
  return {
    capabilities: capabilitiesFor(hostState), mutationPlans, progressEvents, writtenFiles,
    setContext(patch) { context = { ...context, ...patch }; },
    requestCancel() { hostState.cancelRequested = true; },
    createExecution() {
      const state: ExecutionState = { cancelRequested: false, lastProgress: 0 };
      return {
        capabilities: capabilitiesFor(state),
        requestCancel() { state.cancelRequested = true; },
        finalize: finalizeFor(state),
      };
    },
    finalize: finalizeFor(hostState),
  };
}

/** Test execution only; risk defaults to read and must match the manifest action. */
export function startOfflineAction<TInput, TOutput extends JsonValue>(
  action: PluginAction<TInput, TOutput>, input: TInput, host: OfflineHost,
  options: { readonly timeoutMs?: number; readonly risk?: "read" | "write" | "destructive" } = {},
): OfflineRun {
  // Each run owns its cancel/progress scope: a cancelled or timed-out action
  // that settles late only ever observes its own execution state.
  const execution = host.createExecution();
  let settled = false;
  let committing = false;
  let resolveResult: (result: PluginResult) => void;
  const result = new Promise<PluginResult>(resolve => { resolveResult = resolve; });
  const finish = (value: PluginResult) => {
    if (settled) return;
    settled = true;
    if (timeout !== undefined) clearTimeout(timeout);
    resolveResult(value);
  };
  const timeout = options.timeoutMs === undefined ? undefined : setTimeout(() => {
    execution.requestCancel();
    finish(failure(committing ? "plugin_commit_unknown" : "plugin_timeout", "offline action timed out"));
  }, options.timeoutMs);
  const snapshot = {
    aborted: false,
    throwIfAborted() { if (snapshot.aborted) reject("plugin_cancel_requested", "plugin cancellation requested"); },
  };
  void (async () => {
    try {
      const returned = await action(input, execution.capabilities, snapshot);
      if (settled) return;
      let raw: unknown;
      try { raw = JSON.parse(JSON.stringify(returned)); }
      catch { reject("plugin_worker_failed", "plugin action return is not valid JSON"); }
      if (!objectValue(raw)) reject("plugin_worker_failed", "plugin action return must be a JSON object");
      if (raw.contract === "vibetable.mutation-plan.v1") {
        finish(resultFromWire(await execution.finalize(raw as unknown as MutationPlan,
          () => { committing = true; }, options.risk ?? "read")));
      } else {
        if ((options.risk ?? "read") !== "read") reject("plugin_action_failed", "write plugin must return a mutation plan");
        finish(resultFromWire(raw));
      }
    } catch (error) {
      const code = error instanceof PluginCapabilityError ? error.code : "plugin_test_error";
      finish(failure(code, error instanceof Error ? error.message : String(error)));
    }
  })();
  return {
    result,
    cancel(reason = "cancelled by offline host") {
      if (settled) return;
      execution.requestCancel();
      snapshot.aborted = true;
      finish(committing ? failure("plugin_commit_unknown", reason) : cancelled(reason));
    },
  };
}
