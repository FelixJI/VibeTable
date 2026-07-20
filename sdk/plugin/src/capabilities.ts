import type { CommandContext, JsonObject, JsonValue, PluginResult } from "./types.js";

export interface DataPage<T extends JsonObject = JsonObject> {
  readonly items: readonly T[];
  readonly nextCursor: string | null;
}

export interface DataReadRequest {
  readonly collection: string;
  readonly fields: readonly string[];
  readonly filter?: JsonObject;
  readonly cursor?: string;
  readonly pageSize?: number;
}

export interface MutationOperation {
  readonly kind: "create" | "update";
  readonly primaryKey?: string | number | null;
  readonly expectedDateUpdated?: string | null;
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
    mutate(plan: MutationPlan): Promise<MutationResult>;
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
    emitResult(result: PluginResult): Promise<void>;
    reportProgress(progress: { readonly current: number; readonly total: number; readonly message?: string; readonly cancellable?: boolean }): Promise<void>;
  };
  readonly context: {
    read(): Promise<CommandContext>;
  };
}

/** Closed host adapter: every callable capability is explicitly named and typed. */
export interface CapabilityAdapter {
  dataRead<T extends JsonObject = JsonObject>(request: DataReadRequest): Promise<DataPage<T>>;
  dataMutate(plan: MutationPlan): Promise<MutationResult>;
  filePickRead(options?: { readonly mediaTypes?: readonly string[] }): Promise<ReadGrant | null>;
  filePickWrite(options: { readonly suggestedName: string; readonly mediaType: string }): Promise<WriteGrant | null>;
  storageGet<T extends JsonValue = JsonValue>(key: string): Promise<T | null>;
  storageSet(key: string, value: JsonValue): Promise<void>;
  storageDelete(key: string): Promise<void>;
  uiEmitResult(result: PluginResult): Promise<void>;
  uiReportProgress(progress: { readonly current: number; readonly total: number; readonly message?: string; readonly cancellable?: boolean }): Promise<void>;
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
