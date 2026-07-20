import type {
  JsonObject,
  JsonValue,
  PluginFailure,
  PluginIntent,
  PluginSuccess,
} from "./types.js";

export function ok<T extends JsonValue>(
  data: T,
  options: { readonly message?: string; readonly intents?: readonly PluginIntent[] } = {},
): PluginSuccess<T> {
  const refresh = options.intents?.find((intent) => intent.type === "refresh");
  return {
    contract: "vibetable.plugin-result.v1",
    status: "success",
    summary: options.message ?? "插件动作已完成",
    table: { data } as JsonObject,
    ...(refresh === undefined ? {} : { refresh: refresh.payload ?? { requested: true } }),
  };
}

export function cancelled(reason = "插件动作已请求取消"): PluginFailure {
  return {
    contract: "vibetable.plugin-result.v1",
    status: "error",
    summary: reason,
    warnings: ["cancel_requested"],
  };
}

export function failure(
  code: string,
  message: string,
  options: { readonly retryable?: boolean; readonly details?: JsonObject } = {},
): PluginFailure {
  return {
    contract: "vibetable.plugin-result.v1",
    status: "error",
    summary: message,
    table: {
      code,
      retryable: options.retryable ?? false,
      ...(options.details === undefined ? {} : { details: options.details }),
    },
  };
}
