import type { CommandContext, JsonObject, JsonValue, PluginResult } from "./types.js";

export interface DataPage<T extends JsonObject = JsonObject> {
  readonly items: readonly T[];
  readonly nextCursor: string | null;
  /** Existing Go row guards, separate from the requested business fields. */
  readonly rowGuards: Readonly<Record<string, string>>;
}

export interface DataReadRequest {
  readonly collection: string;
  readonly fields: readonly string[];
  /** API v1 only accepts an empty filter. */
  readonly filter?: Readonly<Record<string, never>> | null;
  readonly cursor?: string;
  readonly pageSize?: number;
}

export interface MutationOperation {
  readonly kind: "create" | "update";
  readonly primaryKey?: string | number | null;
  /** @deprecated Only a legacy row_ revision is accepted; never a timestamp. */
  readonly expectedDateUpdated?: string | null;
  readonly expectedDigest?: string | null;
  readonly values: JsonObject;
}

export interface MutationPlan {
  readonly contract: "vibetable.mutation-plan.v1";
  readonly collection: string;
  readonly operations: readonly MutationOperation[];
  readonly preview: {
    readonly summary?: readonly JsonObject[];
    readonly sampleRows?: readonly JsonObject[];
    readonly affectedCount: number;
    readonly warnings?: readonly string[];
  };
  readonly idempotencyKey?: string | null;
}

export interface MutationResult {
  readonly applied: number;
  readonly skipped: number;
  readonly conflicts: number;
}

export interface PluginProgress {
  readonly current: number;
  readonly total: number;
  readonly message?: string;
  readonly cancellable?: boolean;
}

export interface ProgressReceipt {
  readonly cancelRequested: boolean;
}

/** Construct the plan returned by a write action; this does not submit it. */
export function mutationPlan(
  collection: string,
  operations: readonly MutationOperation[],
  preview: MutationPlan["preview"],
): MutationPlan {
  return { contract: "vibetable.mutation-plan.v1", collection, operations, preview };
}

export class PluginCapabilityError extends Error {
  constructor(readonly code: string, message: string) {
    super(message);
    this.name = "PluginCapabilityError";
  }
}

export interface ReadGrant {
  readonly grantId: string;
  readonly displayName: string;
  readonly mediaType: string;
  read(): Promise<Uint8Array>;
}

export interface WriteGrant {
  readonly grantId: string;
  readonly displayName: string;
  write(content: Uint8Array): Promise<void>;
}

export interface PluginCapabilities {
  readonly data: {
    read<T extends JsonObject = JsonObject>(request: DataReadRequest): Promise<DataPage<T>>;
    /** @deprecated Unsupported. Return a MutationPlan from a write action. */
    mutate(plan: MutationPlan): Promise<never>;
  };
  readonly file: {
    pickRead(options?: { readonly mediaTypes?: readonly string[] }): Promise<ReadGrant | null>;
    pickWrite(options: { readonly suggestedName: string; readonly mediaType: string }): Promise<WriteGrant | null>;
  };
  readonly storage: {
    get<T extends JsonValue = JsonValue>(key: string): Promise<T | null>;
    set(key: string, value: JsonValue): Promise<void>;
    delete(key: string): Promise<void>;
  };
  readonly ui: {
    /** @deprecated Unsupported. Return the final PluginResult from the action. */
    emitResult(result: PluginResult): Promise<never>;
    reportProgress(progress: PluginProgress): Promise<ProgressReceipt>;
  };
  readonly context: {
    read(): Promise<CommandContext>;
  };
}

/** Closed host adapter: every callable capability is explicitly named and typed. */
export interface CapabilityAdapter {
  dataRead<T extends JsonObject = JsonObject>(request: DataReadRequest): Promise<DataPage<T>>;
  dataMutate(plan: MutationPlan): Promise<never>;
  filePickRead(options?: { readonly mediaTypes?: readonly string[] }): Promise<ReadGrant | null>;
  filePickWrite(options: { readonly suggestedName: string; readonly mediaType: string }): Promise<WriteGrant | null>;
  storageGet<T extends JsonValue = JsonValue>(key: string): Promise<T | null>;
  storageSet(key: string, value: JsonValue): Promise<void>;
  storageDelete(key: string): Promise<void>;
  uiEmitResult(result: PluginResult): Promise<never>;
  uiReportProgress(progress: PluginProgress): Promise<ProgressReceipt>;
  contextRead(): Promise<CommandContext>;
}

export function createCapabilityClient(adapter: CapabilityAdapter): PluginCapabilities {
  return {
    data: {
      read: (request) => adapter.dataRead(request),
      mutate: (plan) => adapter.dataMutate(plan),
    },
    file: {
      pickRead: (options) => adapter.filePickRead(options),
      pickWrite: (options) => adapter.filePickWrite(options),
    },
    storage: {
      get: (key) => adapter.storageGet(key),
      set: (key, value) => adapter.storageSet(key, value),
      delete: (key) => adapter.storageDelete(key),
    },
    ui: {
      emitResult: (result) => adapter.uiEmitResult(result),
      reportProgress: (progress) => adapter.uiReportProgress(progress),
    },
    context: { read: () => adapter.contextRead() },
  };
}
