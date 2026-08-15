import type { HostBridge } from "@/bridge/hostBridge";
import type {
  InterfaceCommitRequest,
  InterfaceDeleteResult,
  InterfaceListResult,
  InterfaceSnapshot,
} from "@/contracts/generated/workbench";
import type {
  SurfaceWebMessageType,
  SurfaceWebPayloadMap,
} from "@/contracts/surfaceBridgeContracts";
import type { SurfaceListEntry, SurfaceRepository } from "./surfaceCore";

/** Closed HostBridge adapter for Interface authoring persistence. */
export class SurfaceHostRepository implements SurfaceRepository {
  constructor(private readonly bridge: HostBridge) {}

  async list(signal: AbortSignal): Promise<readonly SurfaceListEntry[]> {
    const result = await requestWithCancellation<
      InterfaceListResult,
      "interface.listRequested"
    >(
      this.bridge,
      "interface.listRequested",
      {},
      signal,
    );
    return result.items.map((item) => ({ ...item }));
  }

  load(interfaceId: string, signal: AbortSignal): Promise<InterfaceSnapshot> {
    return requestWithCancellation<InterfaceSnapshot, "interface.loadRequested">(
      this.bridge,
      "interface.loadRequested",
      { interfaceId },
      signal,
    );
  }

  commit(request: InterfaceCommitRequest, signal: AbortSignal): Promise<InterfaceSnapshot> {
    return requestWithCancellation<InterfaceSnapshot, "interface.commitRequested">(
      this.bridge,
      "interface.commitRequested",
      request,
      signal,
    );
  }

  async delete(
    interfaceId: string,
    expectedRevision: string,
    signal: AbortSignal,
  ): Promise<void> {
    const result = await requestWithCancellation<
      InterfaceDeleteResult,
      "interface.deleteRequested"
    >(
      this.bridge,
      "interface.deleteRequested",
      {
        interfaceId,
        expectedRevision,
        idempotencyKey: crypto.randomUUID(),
      },
      signal,
    );
    if (result.interfaceId !== interfaceId) {
      throw productError("surface.response_invalid", "Deleted Interface identity did not match.");
    }
  }
}

type SurfaceRequestType = Exclude<SurfaceWebMessageType, "interface.cancelRequested">;

async function requestWithCancellation<T, K extends SurfaceRequestType>(
  bridge: HostBridge,
  type: K,
  payload: SurfaceWebPayloadMap[K],
  signal: AbortSignal,
): Promise<T> {
  signal.throwIfAborted();
  const handle = bridge.requestWithHandle(type, payload);
  const cancel = () => bridge.notify(
    "interface.cancelRequested",
    { targetRequestId: handle.requestId },
  );
  signal.addEventListener("abort", cancel, { once: true });
  try {
    return await handle.promise as T;
  } finally {
    signal.removeEventListener("abort", cancel);
  }
}

function productError(code: string, message: string): Error & { code: string } {
  return Object.assign(new Error(message), { code });
}
