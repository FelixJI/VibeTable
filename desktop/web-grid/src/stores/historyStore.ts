import { defineStore } from "pinia";
import { computed, ref } from "vue";

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
  const redoStack = ref<HistoryEntry[]>([]);
  const lastError = ref<string | null>(null);
  const busy = ref(false);
  let generation = 0;
  let actionGeneration = 0;

  const canUndo = computed(() => !busy.value && undoStack.value.length > 0);
  const canRedo = computed(() => !busy.value && redoStack.value.length > 0);
  const undoStackSize = computed(() => undoStack.value.length);

  function captureGeneration(): () => boolean {
    const captured = generation;
    return () => generation === captured;
  }

  function beginAction(): void {
    actionGeneration += 1;
    redoStack.value = [];
  }

  function push(entry: HistoryEntry, preserveRedo = false): void {
    const index = entry.dataRevision === undefined ? -1 : undoStack.value.findIndex(
      (existing) => existing.dataRevision !== undefined && existing.dataRevision > entry.dataRevision!,
    );
    if (index < 0) undoStack.value.push(entry);
    else undoStack.value.splice(index, 0, entry);
    // Clear redo on new action (standard undo semantics).
    if (!preserveRedo) beginAction();
    // FIFO cap.
    if (undoStack.value.length > MAX_STACK) {
      undoStack.value.shift();
    }
  }

  async function undo(): Promise<void> {
    if (busy.value) return;
    lastError.value = null;
    const entry = undoStack.value.pop();
    if (!entry) return;
    const startGeneration = generation;
    const startActionGeneration = actionGeneration;
    busy.value = true;
    try {
      await entry.undo();
      if (entry.redo && generation === startGeneration && actionGeneration === startActionGeneration) {
        redoStack.value.push(entry);
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
    if (busy.value) return;
    lastError.value = null;
    const entry = redoStack.value.pop();
    if (!entry) return;
    if (!entry.redo) return;
    const startGeneration = generation;
    busy.value = true;
    try {
      await entry.redo();
      if (generation === startGeneration) {
        push(entry, true);
      }
    } catch (err) {
      if (generation === startGeneration) {
        lastError.value = err instanceof Error ? err.message : "redo failed";
        redoStack.value.push(entry);
      }
    } finally {
      busy.value = false;
    }
  }

  function clear(): void {
    generation += 1;
    undoStack.value = [];
    redoStack.value = [];
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
    captureGeneration,
    beginAction,
    push,
    undo,
    redo,
    clear,
  };
});
