import { watch } from "vue";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import { useTableStore } from "@/stores/tableStore";

/** Local ownership also covers table switches, which do not retire bridge RPCs. */
export function useMutationScope(onRetire: () => void) {
  const ws = useWorkspaceStore();
  const session = useWorkspaceSessionStore();
  const table = useTableStore();
  let generation = 0;
  let disposed = false;
  let databaseSessionId = table.revision?.databaseSessionId ?? "";
  const retire = (): void => {
    generation += 1;
    onRetire();
  };
  const stopContext = watch(
    () => [ws.currentTable, ws.phase === "opening", session.activeWorkspaceId,
      session.sessionEpoch, session.isTransitioning],
    retire,
    { flush: "sync" },
  );
  const stopSession = watch(() => table.revision?.databaseSessionId, (next) => {
    if (!next) return; // A refresh can temporarily withhold the revision.
    if (databaseSessionId && next !== databaseSessionId) retire();
    databaseSessionId = next;
  }, { flush: "sync" });
  return {
    capture: () => {
      const captured = generation;
      return () => !disposed && captured === generation;
    },
    dispose: () => {
      if (disposed) return;
      disposed = true;
      stopContext();
      stopSession();
      retire();
    },
  };
}
