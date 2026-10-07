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
  requestCancel(): void;
  finalize(plan: MutationPlan, onSubmit: () => void): Promise<PluginResult>;
}

export interface OfflineRun {
  readonly result: Promise<PluginResult>;
  cancel(reason?: string): void;
}

function reject(code: string, message: string): never {
  throw new PluginCapabilityError(code, message);
}

export function createOfflineHost(options: OfflineHostOptions = {}): OfflineHost {
  let context: CommandContext = {
    contract: "vibetable.command-context.v1", projectKey: "offline:test", collection: null,
    selectedKeys: [], querySnapshot: null, locale: "zh-CN", theme: "light",
    density: "comfortable", user: {}, hostVersion: "unknown", ...options.context,
  };
  let cancelRequested = false;
  let lastProgress = 0;
  let readIndex = 0;
  const storage = new Map<string, JsonValue>();
  const mutationPlans: MutationPlan[] = [];
  const progressEvents: PluginProgress[] = [];
  const writtenFiles = new Map<string, Uint8Array>();
  const grant = (collection: string, operation: "read" | "create" | "update") =>
    options.permissions?.data?.find(item => item.operations.includes(operation)
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
  const capabilities: PluginCapabilities = createCapabilityClient({
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
      return { items, nextCursor: items.length === pageSize ? String(offset + items.length) : null, rowGuards };
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
      lastProgress = Math.max(lastProgress, progress.current);
      progressEvents.push({ ...progress, current: lastProgress });
      return { cancelRequested };
    },
    async contextRead() { return structuredClone(context); },
  });
  return {
    capabilities, mutationPlans, progressEvents, writtenFiles,
    setContext(patch) { context = { ...context, ...patch }; },
    requestCancel() { cancelRequested = true; },
    async finalize(plan, onSubmit) {
      if (plan.operations.length > 10_000 || plan.preview.affectedCount !== plan.operations.length) reject("plugin_worker_failed", "invalid mutation plan count");
      if (context.collection !== null && plan.collection !== context.collection) reject("plugin_action_failed", "mutation plan collection is outside the action context");
      for (const operation of plan.operations) {
        const permission = grant(plan.collection, operation.kind);
        if (!permission || Object.keys(operation.values).some(field => !allowedFields(plan.collection, permission.fields).includes(field))) reject("plugin_worker_failed", "mutation permission was not declared");
        if (operation.expectedDateUpdated !== undefined && operation.expectedDateUpdated !== null && !/^row_[0-9]{4,}$/u.test(operation.expectedDateUpdated)) reject("plugin_worker_failed", "invalid legacy revision guard");
        if (operation.expectedDigest !== undefined && operation.expectedDigest !== null && !/^sha256:[0-9a-f]{64}$/u.test(operation.expectedDigest)) reject("plugin_worker_failed", "invalid digest guard");
      }
      mutationPlans.push(structuredClone(plan));
      const decision = options.approveMutation ?? false;
      const approved = typeof decision === "function" ? await decision(plan) : decision;
      if (cancelRequested) reject("plugin_cancel_requested", "plugin cancellation requested");
      if (!approved) reject("plugin_mutation_rejected", "mutation plan was rejected");
      if (!options.applyMutation) reject("plugin_action_failed", "synthetic mutation adapter is unavailable");
      onSubmit();
      return options.applyMutation(plan);
    },
  };
}

/** Test execution only; production terminal state is always owned by Host. */
export function startOfflineAction<TInput, TOutput extends JsonValue>(
  action: PluginAction<TInput, TOutput>, input: TInput, host: OfflineHost,
  options: { readonly timeoutMs?: number } = {},
): OfflineRun {
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
    host.requestCancel();
    finish(failure(committing ? "plugin_commit_unknown" : "plugin_timeout", "offline action timed out"));
  }, options.timeoutMs);
  const snapshot = {
    aborted: false,
    throwIfAborted() { if (snapshot.aborted) reject("plugin_cancel_requested", "plugin cancellation requested"); },
  };
  void (async () => {
    try {
      const raw = await action(input, host.capabilities, snapshot);
      if (settled) return;
      if (raw.contract === "vibetable.mutation-plan.v1") {
        finish(await host.finalize(raw, () => { committing = true; }));
      } else if (raw.contract === "vibetable.plugin-result.v1" && ["success", "warning", "error"].includes(raw.status)
        && typeof raw.summary === "string") finish(raw);
      else finish(failure("plugin_action_failed", "invalid plugin action return"));
    } catch (error) {
      const code = error instanceof PluginCapabilityError ? error.code : "plugin_test_error";
      finish(failure(code, error instanceof Error ? error.message : String(error)));
    }
  })();
  return {
    result,
    cancel(reason = "cancelled by offline host") {
      if (settled) return;
      host.requestCancel();
      snapshot.aborted = true;
      finish(committing ? failure("plugin_commit_unknown", reason) : cancelled(reason));
    },
  };
}
