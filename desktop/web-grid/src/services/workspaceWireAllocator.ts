import type { WorkspaceWireScope } from "@/contracts/workspaceV2";

interface ActiveWorkspaceWire {
  readonly workspaceId: string;
  readonly sessionEpoch: number;
}

let active: ActiveWorkspaceWire | null = null;
let sequence = 0;

export function configureWorkspaceWire(
  workspaceId: string | null,
  sessionEpoch: number,
): void {
  if (!workspaceId || sessionEpoch < 1) {
    active = null;
    sequence = 0;
    return;
  }
  if (
    active?.workspaceId === workspaceId
    && active.sessionEpoch === sessionEpoch
  ) return;
  active = { workspaceId, sessionEpoch };
  // Keep renderer-issued sequences comfortably ahead of host bootstrap
  // reservations while remaining below Number.MAX_SAFE_INTEGER.
  sequence = Date.now() * 1_000;
}

export function observeWorkspaceWire(wire: WorkspaceWireScope): void {
  configureWorkspaceWire(wire.workspaceId, wire.sessionEpoch);
  sequence = Math.max(sequence, wire.sequence);
}

export function nextWorkspaceWire(operationId: string): WorkspaceWireScope {
  if (!active) throw new Error("workspace wire allocator has no active session");
  sequence = Math.max(sequence + 1, Date.now() * 1_000);
  return {
    scope: "workspace",
    workspaceId: active.workspaceId,
    sessionEpoch: active.sessionEpoch,
    operationId,
    sequence,
  };
}
