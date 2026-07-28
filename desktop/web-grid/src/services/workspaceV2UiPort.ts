import type {
  WorkspaceV2RpcMethod,
  WorkspaceV2RpcResult,
  WorkspaceV2UiAction,
} from "@/contracts/workspaceV2Bridge";

export type { WorkspaceV2UiAction } from "@/contracts/workspaceV2Bridge";

export interface WorkspaceV2UiPort {
  request<M extends WorkspaceV2RpcMethod>(
    action: WorkspaceV2UiAction<M>,
  ): Promise<WorkspaceV2RpcResult<M>>;
}

let port: WorkspaceV2UiPort | null = null;

/**
 * The UI and protocol rollout intentionally have separate switches. The host
 * adapter binds this port only after its v2 producer and parser are active.
 * Until then every action fails closed instead of falling back to v1 backup or
 * document-version operations.
 */
export function setWorkspaceV2UiPort(next: WorkspaceV2UiPort | null): void {
  port = next;
}

export function requestWorkspaceV2UiAction<M extends WorkspaceV2RpcMethod>(
  action: WorkspaceV2UiAction<M>,
): Promise<WorkspaceV2RpcResult<M>> {
  if (!port) return Promise.reject(new Error("workspace.v2_consumer_not_bound"));
  return port.request(action);
}
