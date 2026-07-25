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

  const canUndo = computed(() => !busy.value && undoStack.value.length > 0);
  const canRedo = computed(() => !busy.value && redoStack.value.length > 0);
  const undoStackSize = computed(() => undoStack.value.length);

  function push(entry: HistoryEntry): void {
    undoStack.value.push(entry);
    // Clear redo on new action (standard undo semantics).
    redoStack.value = [];
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
    busy.value = true;
    try {
      await entry.undo();
      if (entry.redo && generation === startGeneration) {
        redoStack.value.push(entry);
      }
    } catch (err) {
      lastError.value = err instanceof Error ? err.message : "undo failed";
      // Put it back so the user can retry after resolving the conflict.
      if (generation === startGeneration) {
        undoStack.value.push(entry);
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
        undoStack.value.push(entry);
      }
    } catch (err) {
      lastError.value = err instanceof Error ? err.message : "redo failed";
      if (generation === startGeneration) {
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
    push,
    undo,
    redo,
    clear,
  };
});
