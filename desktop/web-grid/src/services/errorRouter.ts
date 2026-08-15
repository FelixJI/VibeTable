import type { OperationFailedPayload } from "@/contracts";
import { useHostBridge } from "./bridgeContext";
import { usePasteStore } from "@/stores/pasteStore";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useTableStore } from "@/stores/tableStore";

/**
 * Centralized router for `operation.failed`. Replaces the inline if/else chain
 * that was scattered in `main.ts:524-538` of the old web-grid
 * (architecture-debt fix #4).
 *
 * Decision key: the currently-active operation phase across the three business
 * stores. Whoever is "in flight" claims the failure:
 *
 *   - `tableAdminStore` in `submitting` or `deleting` -> `admin.fail`
 *     (create-table / delete-table flow owns the error surface).
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
  const admin = useTableAdminStore();
  const table = useTableStore();
  const paste = usePasteStore();

  function init(): void {
    bridge.on("operation.failed", (payload: OperationFailedPayload) => {
      if (payload.code === "query.cursor_stale") return;
      if (admin.phase === "submitting" || admin.phase === "deleting") {
        admin.fail(payload.message);
      } else if (paste.phase === "applying") {
        paste.setError(payload.message);
      } else {
        table.setError(payload.message);
      }
    });
  }

  return { init };
}
