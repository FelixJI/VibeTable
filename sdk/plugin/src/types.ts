export type JsonPrimitive = string | number | boolean | null;
export type JsonValue = JsonPrimitive | JsonValue[] | { readonly [key: string]: JsonValue };
export type JsonObject = { readonly [key: string]: JsonValue };

export type ThemeMode = "light" | "dark";
export type Density = "compact" | "comfortable";

export interface CommandContext {
  readonly contract: "vibetable.command-context.v1";
  readonly projectKey: string;
  readonly collection: string | null;
  readonly selectedKeys: readonly (string | number)[];
  readonly querySnapshot: JsonObject | null;
  readonly locale: string;
  readonly theme: ThemeMode;
  readonly density: string;
  readonly user: JsonObject;
  readonly hostVersion: string;
}

export interface PluginIntent {
  readonly type: "refresh" | "export" | "navigate-record";
  readonly payload?: JsonObject;
}

export interface PluginMetric {
  readonly label: string;
  readonly value: string | number;
}

/** Exact wire shape accepted by the Python PluginResult contract. */
export interface PluginResult<T = JsonValue> {
  readonly contract: "vibetable.plugin-result.v1";
  readonly status: "success" | "warning" | "error";
  readonly summary: string;
  readonly metrics?: readonly PluginMetric[];
  readonly table?: JsonObject | null;
  readonly artifacts?: readonly JsonObject[];
  readonly refresh?: JsonObject | null;
  readonly warnings?: readonly string[];
  /** Compile-time payload marker only; never serialized. */
  readonly __output?: T;
}

export type PluginSuccess<T = JsonValue> = PluginResult<T> & { readonly status: "success" };
export type PluginFailure = PluginResult<never> & { readonly status: "error" };

export type PluginActionReturn<TOutput = JsonValue> = PluginResult<TOutput> | import("./capabilities.js").MutationPlan;

export type PluginReadAction<TInput = JsonValue, TOutput = JsonValue> = (
  input: TInput,
  capabilities: import("./capabilities.js").PluginCapabilities,
) => Promise<PluginResult<TOutput>>;

export type PluginWriteAction<TInput = JsonValue> = (
  input: TInput,
  capabilities: import("./capabilities.js").PluginCapabilities,
) => Promise<import("./capabilities.js").MutationPlan>;

/** @deprecated A compatibility snapshot, not an AbortSignal. Use progress receipts. */
export interface LegacyCancellationSnapshot {
  readonly aborted: boolean;
  readonly reason?: string;
  throwIfAborted(): void;
}

/** Compatible action shape; prefer PluginReadAction or PluginWriteAction. */
export type PluginAction<TInput = JsonValue, TOutput = JsonValue> = (
  input: TInput,
  capabilities: import("./capabilities.js").PluginCapabilities,
  signal: LegacyCancellationSnapshot,
) => Promise<PluginActionReturn<TOutput>>;
