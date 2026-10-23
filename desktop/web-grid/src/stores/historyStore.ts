import { defineStore } from "pinia";
import { computed, ref, watch } from "vue";
import { useWorkspaceStore } from "./workspaceStore";
import { useWorkspaceSessionStore } from "./workspaceSessionStore";

export type HistoryEntryKind =
  | "updateCell"
  | "insertRow"
  | "deleteRows"
  | "applyPaste";

export interface HistoryEntry {
  readonly id: string;
  readonly kind: HistoryEntryKind;
  readonly label: string;
  readonly timestamp: number;
  /** Authority commit order, when the mutation receipt provides one. */
  readonly dataRevision?: number;
  readonly canReplay?: () => boolean;
  readonly undo: () => Promise<void>;
  /**
   * Omit when the operation cannot be replayed faithfully. In particular,
   * paste tokens are single-use, so a successful paste undo must not advertise
   * a redo that performs no data change.
   */
  readonly redo?: () => Promise<void>;
}

const MAX_STACK = 50;

export const useHistoryStore = defineStore("history", () => {
  const undoStack = ref<HistoryEntry[]>([]);
  // Candidates stay in undo order; action cutoffs only hide a prefix while writes are pending.
  const redoEntries = ref<{ entry: HistoryEntry; action: number }[]>([]);
  const pendingActions = ref(new Set<number>());
  const redoStack = computed(() => {
    let latestPending = 0;
    for (const action of pendingActions.value) latestPending = Math.max(latestPending, action);
    return redoEntries.value.filter((item) => item.action >= latestPending).map((item) => item.entry);
  });
  const lastError = ref<string | null>(null);
  const busy = ref(false);
  const workspace = useWorkspaceStore();
  const session = useWorkspaceSessionStore();
  const uncertainTables = ref(new Map<string, number>());
  const tableKey = () => workspace.currentTable ?? "";
  const writeUncertain = computed(() => uncertainTables.value.has(tableKey()));
  let authorityGeneration = 0;
  let generation = 0;
  let actionSequence = 0;
  let lastInvalidatingAction = 0;
  let uncertaintyGeneration = 0;

  const canUndo = computed(() => !busy.value && !writeUncertain.value && undoStack.value.length > 0
    && (undoStack.value.at(-1)?.canReplay?.() ?? true));
  const canRedo = computed(() => !busy.value && !writeUncertain.value && redoStack.value.length > 0
    && (redoStack.value.at(-1)?.canReplay?.() ?? true));
  const undoStackSize = computed(() => undoStack.value.length);

  function captureWriteUncertainty(): () => void {
    const table = tableKey();
    const authority = authorityGeneration;
    return () => {
      if (authority !== authorityGeneration) return;
      if (table === tableKey()) clear();
      uncertaintyGeneration += 1;
      uncertainTables.value.set(table, uncertaintyGeneration);
    };
  }

  function markWriteUncertain(): void {
    captureWriteUncertainty()();
  }

  // Only a query started after this table's unresolved write can acknowledge recovery.
  function captureWriteRefresh(): () => void {
    const table = tableKey();
    const captured = uncertainTables.value.get(table);
    const authority = authorityGeneration;
    return () => {
      if (captured !== undefined && authority === authorityGeneration && table === tableKey()
        && captured === uncertainTables.value.get(table)) uncertainTables.value.delete(table);
    };
  }

  function retireWriteScope(): void {
    clear();
    authorityGeneration += 1;
    uncertainTables.value.clear();
  }

  // A table/null/transition UI change does not end the PocketBase authority.
  watch(() => [session.activeWorkspaceId, session.sessionEpoch], retireWriteScope, { flush: "sync" });

  function captureGeneration(): () => boolean {
    const captured = generation;
    return () => generation === captured;
  }

  // Keep at most MAX_STACK redo candidates; only in-flight requests retain action IDs.
  function beginAction(): (notSubmitted: boolean) => void {
    const action = ++actionSequence;
    const historyGeneration = generation;
    pendingActions.value.add(action);
    return (notSubmitted) => {
      if (historyGeneration !== generation || !pendingActions.value.has(action)) return;
      if (!notSubmitted) {
        lastInvalidatingAction = Math.max(lastInvalidatingAction, action);
        redoEntries.value = redoEntries.value.filter((item) => item.action >= lastInvalidatingAction);
      }
      pendingActions.value.delete(action);
    };
  }

  function retainRedo(entry: HistoryEntry, action: number): void {
    if (action < lastInvalidatingAction) return;
    redoEntries.value.push({ entry, action });
    if (redoEntries.value.length > MAX_STACK) redoEntries.value.shift();
  }

  function push(entry: HistoryEntry, preserveRedo = false): void {
    const index = entry.dataRevision === undefined ? -1 : undoStack.value.findIndex(
      (existing) => existing.dataRevision !== undefined && existing.dataRevision > entry.dataRevision!,
    );
    if (index < 0) undoStack.value.push(entry);
    else undoStack.value.splice(index, 0, entry);
    // Clear redo on new action (standard undo semantics).
    if (!preserveRedo) beginAction()(false);
    // FIFO cap.
    if (undoStack.value.length > MAX_STACK) {
      undoStack.value.shift();
    }
  }

  async function undo(): Promise<void> {
    if (!canUndo.value) return;
    lastError.value = null;
    const entry = undoStack.value.pop();
    if (!entry) return;
    const startGeneration = generation;
    const startAction = actionSequence;
    busy.value = true;
    try {
      await entry.undo();
      if (entry.redo && generation === startGeneration) {
        retainRedo(entry, startAction);
      }
    } catch (err) {
      // Put it back so the user can retry after resolving the conflict.
      if (generation === startGeneration) {
        lastError.value = err instanceof Error ? err.message : "undo failed";
        push(entry, true);
      }
    } finally {
      busy.value = false;
    }
  }

  async function redo(): Promise<void> {
    if (!canRedo.value) return;
    lastError.value = null;
    const entry = redoEntries.value.pop()?.entry;
    if (!entry) return;
    if (!entry.redo) return;
    const startGeneration = generation;
    const startAction = actionSequence;
    busy.value = true;
    try {
      await entry.redo();
      if (generation === startGeneration) {
        push(entry, true);
      }
    } catch (err) {
      if (generation === startGeneration) {
        lastError.value = err instanceof Error ? err.message : "redo failed";
        retainRedo(entry, startAction);
      }
    } finally {
      busy.value = false;
    }
  }

  function clear(): void {
    generation += 1;
    undoStack.value = [];
    redoEntries.value = [];
    pendingActions.value.clear();
    lastError.value = null;
  }

  return {
    undoStack,
    redoStack,
    lastError,
    busy,
    canUndo,
    canRedo,
    undoStackSize,
    writeUncertain,
    markWriteUncertain,
    captureWriteUncertainty,
    captureWriteRefresh,
    retireWriteScope,
    captureGeneration,
    beginAction,
    push,
    undo,
    redo,
    clear,
  };
});
