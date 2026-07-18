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
  readonly redo: () => Promise<void>;
}

const MAX_STACK = 50;

export const useHistoryStore = defineStore("history", () => {
  const undoStack = ref<HistoryEntry[]>([]);
  const redoStack = ref<HistoryEntry[]>([]);
  const lastError = ref<string | null>(null);

  const canUndo = computed(() => undoStack.value.length > 0);
  const canRedo = computed(() => redoStack.value.length > 0);
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
    lastError.value = null;
    const entry = undoStack.value.pop();
    if (!entry) return;
    try {
      await entry.undo();
      redoStack.value.push(entry);
    } catch (err) {
      lastError.value = err instanceof Error ? err.message : "undo failed";
      // Put it back so the user can retry after resolving the conflict.
      undoStack.value.push(entry);
    }
  }

  async function redo(): Promise<void> {
    lastError.value = null;
    const entry = redoStack.value.pop();
    if (!entry) return;
    try {
      await entry.redo();
      undoStack.value.push(entry);
    } catch (err) {
      lastError.value = err instanceof Error ? err.message : "redo failed";
      redoStack.value.push(entry);
    }
  }

  function clear(): void {
    undoStack.value = [];
    redoStack.value = [];
    lastError.value = null;
  }

  return {
    undoStack,
    redoStack,
    lastError,
    canUndo,
    canRedo,
    undoStackSize,
    push,
    undo,
    redo,
    clear,
  };
});
