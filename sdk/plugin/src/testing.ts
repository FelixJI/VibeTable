import type {
  CapabilityAdapter,
  DataPage,
  DataReadRequest,
  MutationPlan,
  MutationResult,
  PluginCapabilities,
  ReadGrant,
  WriteGrant,
} from "./capabilities.js";
import { createCapabilityClient } from "./capabilities.js";
import { cancelled, failure } from "./results.js";
import type {
  CommandContext,
  JsonObject,
  JsonValue,
  PluginAction,
  PluginResult,
} from "./types.js";

export interface OfflineHostOptions {
  readonly context?: Partial<CommandContext>;
  readonly collections?: Readonly<Record<string, readonly JsonObject[]>>;
  readonly readFiles?: readonly { readonly name: string; readonly mediaType: string; readonly content: Uint8Array }[];
  readonly approveMutation?: boolean | ((plan: MutationPlan) => boolean | Promise<boolean>);
  readonly pageSizeLimit?: number;
}

export interface OfflineHost {
  readonly capabilities: PluginCapabilities;
  readonly mutationPlans: readonly MutationPlan[];
  readonly progressEvents: readonly { readonly current: number; readonly total: number; readonly message?: string; readonly cancellable?: boolean }[];
  readonly emittedResults: readonly PluginResult[];
  readonly writtenFiles: ReadonlyMap<string, Uint8Array>;
  setContext(patch: Partial<CommandContext>): void;
}

export interface OfflineRun<TOutput> {
  readonly result: Promise<PluginResult<TOutput>>;
  cancel(reason?: string): void;
}

export function createOfflineHost(options: OfflineHostOptions = {}): OfflineHost {
  let context: CommandContext = {
    projectKey: "offline:test",
    contract: "vibetable.command-context.v1",
    collection: null,
    selectedKeys: [],
    querySnapshot: null,
    locale: "zh-CN",
    theme: "light",
    density: "comfortable",
    user: {},
    hostVersion: "1.0.0",
    ...options.context,
  };
  const storage = new Map<string, JsonValue>();
  const mutationPlans: MutationPlan[] = [];
  const progressEvents: { current: number; total: number; message?: string; cancellable?: boolean }[] = [];
  const emittedResults: PluginResult[] = [];
  const writtenFiles = new Map<string, Uint8Array>();
  let readIndex = 0;

  const adapter: CapabilityAdapter = {
    async dataRead<T extends JsonObject>(request: DataReadRequest): Promise<DataPage<T>> {
      const source = options.collections?.[request.collection] ?? [];
      const offset = request.cursor === undefined ? 0 : Number.parseInt(request.cursor, 10);
      const requestedSize = request.pageSize ?? options.pageSizeLimit ?? 100;
      const pageSize = Math.min(requestedSize, options.pageSizeLimit ?? 500);
      const items = source.slice(offset, offset + pageSize) as unknown as readonly T[];
      const next = offset + items.length;
      return { items, nextCursor: next < source.length ? String(next) : null };
    },
    async dataMutate(plan: MutationPlan): Promise<MutationResult> {
      const decision = options.approveMutation ?? true;
      const approved = typeof decision === "function" ? await decision(plan) : decision;
      if (!approved) throw new DOMException("mutation rejected", "AbortError");
      mutationPlans.push(plan);
      return { applied: plan.operations.length, skipped: 0, conflicts: 0 };
    },
    async filePickRead(): Promise<ReadGrant | null> {
      const file = options.readFiles?.[readIndex++];
      if (file === undefined) return null;
      return {
        grantId: `offline-read-${readIndex}`,
        displayName: file.name,
        mediaType: file.mediaType,
        async read() { return file.content.slice(); },
      };
    },
    async filePickWrite(request): Promise<WriteGrant> {
      const grantId = `offline-write-${writtenFiles.size + 1}`;
      return {
        grantId,
        displayName: request.suggestedName,
        async write(content) { writtenFiles.set(request.suggestedName, content.slice()); },
      };
    },
    async storageGet<T extends JsonValue>(key: string): Promise<T | null> {
      return (storage.get(key) as T | undefined) ?? null;
    },
    async storageSet(key, value) { storage.set(key, value); },
    async storageDelete(key) { storage.delete(key); },
    async uiEmitResult(result) { emittedResults.push(result); },
    async uiReportProgress(progress) { progressEvents.push(progress); },
    async contextRead() { return { ...context, selectedKeys: [...context.selectedKeys] }; },
  };
  return {
    capabilities: createCapabilityClient(adapter),
    mutationPlans,
    progressEvents,
    emittedResults,
    writtenFiles,
    setContext(patch) { context = { ...context, ...patch }; },
  };
}

/** Run an action with deterministic cancellation and an optional timeout boundary. */
export function startOfflineAction<TInput, TOutput>(
  action: PluginAction<TInput, TOutput>,
  input: TInput,
  host: OfflineHost,
  options: { readonly timeoutMs?: number } = {},
): OfflineRun<TOutput> {
  const controller = new AbortController();
  let cancellationReason = "cancelled by offline host";
  let timedOut = false;
  const timeout = options.timeoutMs === undefined
    ? undefined
    : setTimeout(() => {
        timedOut = true;
        controller.abort(new DOMException("offline action timed out", "TimeoutError"));
      }, options.timeoutMs);

  const result = new Promise<PluginResult<TOutput>>((resolve) => {
    void action(input, host.capabilities, controller.signal).then(resolve, (error: unknown) => {
      if (controller.signal.aborted) {
        resolve(
          timedOut
            ? failure("plugin_timeout", "offline action timed out", { retryable: true })
            : cancelled(cancellationReason),
        );
      } else {
        resolve(failure("plugin_test_error", error instanceof Error ? error.message : String(error)));
      }
    }).finally(() => {
      if (timeout !== undefined) clearTimeout(timeout);
    });
  });

  return {
    result,
    cancel(reason = cancellationReason) {
      cancellationReason = reason;
      controller.abort(new DOMException(reason, "AbortError"));
    },
  };
}
