import type { WorkspaceWireScope } from "@/contracts/workspaceV2";
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
}

type ReadActiveWorkspaceSession = () => ActiveWorkspaceSession | null;

declare global {
  interface Window {
    __vibetableE2EWorkspaceWirePort?: WorkspaceWireE2EPort;
  }
}

export function createWorkspaceWireE2EPort(
  readSession: ReadActiveWorkspaceSession,
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
  };
}

export function installWorkspaceWireE2EPort(
  target: Window,
  readSession: ReadActiveWorkspaceSession,
): () => void {
  Object.defineProperty(target, "__vibetableE2EWorkspaceWirePort", {
    configurable: true,
    enumerable: false,
    value: createWorkspaceWireE2EPort(readSession),
  });
  return () => {
    Reflect.deleteProperty(target, "__vibetableE2EWorkspaceWirePort");
  };
}
