import type { OperationFailedPayload } from "@/contracts";
import { useHostBridge } from "./bridgeContext";
import { useTableStore } from "@/stores/tableStore";

/** Uncorrelated failures are general diagnostics; active flows own their requests. */
export function useErrorRouter(options: { onRealtimeFailure?: () => void } = {}): { init: () => void; dispose: () => void } {
  const bridge = useHostBridge();
  const table = useTableStore();
  const unsubscribe: Array<() => void> = [];
  let initialized = false;
  let disposed = false;

  function init(): void {
    // bridge.on appends a Set entry per subscription: a repeated init would
    // route every failure twice, and a disposed instance never re-subscribes.
    if (initialized || disposed) return;
    initialized = true;
    unsubscribe.push(bridge.on("operation.failed", (payload: OperationFailedPayload) => {
      if (payload.operation === "realtime.stream") {
        options.onRealtimeFailure?.();
        return;
      }
      if (payload.code === "query.cursor_stale") return;
      table.setError(payload.message);
    }));
  }

  function dispose(): void {
    if (disposed) return;
    disposed = true;
    for (const stop of unsubscribe.splice(0)) stop();
  }

  return { init, dispose };
}
