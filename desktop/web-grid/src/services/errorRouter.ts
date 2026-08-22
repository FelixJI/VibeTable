import type { OperationFailedPayload } from "@/contracts";
import { useHostBridge } from "./bridgeContext";
import { usePasteStore } from "@/stores/pasteStore";
import { useTableStore } from "@/stores/tableStore";

/**
 * Centralized router for `operation.failed`. Replaces the inline if/else chain
 * that was scattered in `main.ts:524-538` of the old web-grid
 * (architecture-debt fix #4).
 *
 * Correlated requests consume their own failures before handler fan-out. This
 * router owns only uncorrelated broadcast failures:
 *
 *   - correlated table-admin requests reject their own Promise and never
 *     reach this broadcast-only router.
 *   - `pasteStore` in `applying` -> `paste.setError`
 *     (paste-in-progress owns the error surface).
 *   - otherwise -> `tableStore.setError`
 *     (data ops are the most common failure source, so the table store is the
 *     fallback owner when no specific flow is active).
 *
 * The router subscribes exactly once per `init()` call; the caller (typically
 * `main.ts` during bootstrap) is responsible for invoking `init()` once.
 */
export function useErrorRouter(): { init: () => void } {
  const bridge = useHostBridge();
  const table = useTableStore();
  const paste = usePasteStore();

  function init(): void {
    bridge.on("operation.failed", (payload: OperationFailedPayload) => {
      if (payload.code === "query.cursor_stale") return;
      if (paste.phase === "applying") {
        paste.setError(payload.message);
      } else {
        table.setError(payload.message);
      }
    });
  }

  return { init };
}
