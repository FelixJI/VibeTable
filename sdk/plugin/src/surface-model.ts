/** Model configuration always uses a native Host picker and exact-plan confirmation. */
export interface SurfaceModelBinding {
  readonly tableId: string;
  readonly fields: Readonly<Record<string, string>>;
}
export interface SurfaceModelStatus {
  readonly contract: "vibetable.plugin-model.v1";
  readonly status: "unconfigured" | "pending" | "partial" | "failed" | "completed";
  readonly requestId?: string;
  readonly completed?: number;
  readonly total?: number;
  readonly bindings: Readonly<Record<string, SurfaceModelBinding>>;
  readonly errorCode?: string;
}
export interface SurfaceModel {
  /** Cancellation or a lost reply may leave committed steps. Inspect read/status before retrying. */
  configure(request: { readonly mode: "create" | "map" }, signal?: AbortSignal): Promise<SurfaceModelStatus | null>;
  read(): Promise<SurfaceModelStatus>;
  status(request: { readonly requestId: string }): Promise<SurfaceModelStatus>;
}
export function createSurfaceModel(call: <T>(method: string, params: unknown, signal?: AbortSignal) => Promise<T>): SurfaceModel {
  return {
    configure: (request, signal) => call("model.configure", request, signal),
    read: () => call("model.read", {}),
    status: request => call("model.status", request),
  };
}
