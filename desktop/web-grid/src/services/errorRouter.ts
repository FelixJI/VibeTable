import type { OperationFailedPayload } from "@/contracts";
import { useHostBridge } from "./bridgeContext";
import { useTableStore } from "@/stores/tableStore";

/** Uncorrelated failures are general diagnostics; active flows own their requests. */
export function useErrorRouter(options: { onRealtimeFailure?: () => void } = {}): { init: () => void } {
  const bridge = useHostBridge();
  const table = useTableStore();

  function init(): void {
    bridge.on("operation.failed", (payload: OperationFailedPayload) => {
      if (payload.operation === "realtime.stream") {
        options.onRealtimeFailure?.();
        return;
      }
      if (payload.code === "query.cursor_stale") return;
      table.setError(payload.message);
    });
  }

  return { init };
}
