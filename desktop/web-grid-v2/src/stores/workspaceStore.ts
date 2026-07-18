import { defineStore } from "pinia";
import { ref } from "vue";
import type { CollectionSummary } from "@/contracts";

export type WorkspacePhase = "idle" | "opening" | "opened" | "failed";

export const useWorkspaceStore = defineStore("workspace", () => {
  const phase = ref<WorkspacePhase>("idle");
  const collections = ref<readonly CollectionSummary[]>([]);
  const currentTable = ref<string | null>(null);
  const lastError = ref<string | null>(null);

  function beginOpen(): void {
    phase.value = "opening";
    lastError.value = null;
  }

  function setOpened(cols: readonly CollectionSummary[]): void {
    collections.value = cols;
    phase.value = "opened";
    lastError.value = null;
  }

  function setCollections(cols: readonly CollectionSummary[]): void {
    collections.value = cols;
  }

  function selectTable(name: string): void {
    currentTable.value = name;
  }

  function setFailed(message: string): void {
    phase.value = "failed";
    lastError.value = message;
  }

  function clear(): void {
    phase.value = "idle";
    collections.value = [];
    currentTable.value = null;
    lastError.value = null;
  }

  return {
    phase,
    collections,
    currentTable,
    lastError,
    beginOpen,
    setOpened,
    setCollections,
    selectTable,
    setFailed,
    clear,
  };
});
