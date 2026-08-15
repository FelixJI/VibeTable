import type { WorkspaceWireScope } from "@/contracts/workspaceV2";
import type { WorkspaceV2UiPort } from "@/services/workspaceV2UiPort";
import {
  configureWorkspaceWire,
  nextWorkspaceWire,
} from "@/services/workspaceWireAllocator";

interface ActiveWorkspaceSession {
  readonly workspaceId: string;
  readonly sessionEpoch: number;
}

export interface WorkspaceWireE2EPort {
  reserve(operationId: string): WorkspaceWireScope;
  request: WorkspaceV2UiPort["request"];
}

type ReadActiveWorkspaceSession = () => ActiveWorkspaceSession | null;

declare global {
  interface Window {
    __vibetableE2EWorkspaceWirePort?: WorkspaceWireE2EPort;
  }
}

export function createWorkspaceWireE2EPort(
  readSession: ReadActiveWorkspaceSession,
  workspaceV2: WorkspaceV2UiPort,
): WorkspaceWireE2EPort {
  return {
    reserve(operationId: string): WorkspaceWireScope {
      const session = readSession();
      configureWorkspaceWire(
        session?.workspaceId ?? null,
        session?.sessionEpoch ?? 0,
      );
      return nextWorkspaceWire(operationId);
    },
    request: workspaceV2.request,
  };
}

export function installWorkspaceWireE2EPort(
  target: Window,
  readSession: ReadActiveWorkspaceSession,
  workspaceV2: WorkspaceV2UiPort,
): () => void {
  Object.defineProperty(target, "__vibetableE2EWorkspaceWirePort", {
    configurable: true,
    enumerable: false,
    value: createWorkspaceWireE2EPort(readSession, workspaceV2),
  });
  return () => {
    Reflect.deleteProperty(target, "__vibetableE2EWorkspaceWirePort");
  };
}
