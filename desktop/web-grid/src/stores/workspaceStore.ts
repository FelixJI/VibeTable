import { defineStore } from "pinia";
import { ref } from "vue";

export type WorkspacePhase = "idle" | "opening" | "opened" | "failed";

/**
 * Frontend-only view-model summarizing one collection (table or view) for the
 * workspace list. This is NOT a wire contract: the workspace service derives
 * these from the wire-level `database.opened`/`database.collectionsChanged`
 * payloads (see `@/contracts`) and layers in capability metadata as needed.
 */
export interface CollectionSummary {
  readonly collection: string;
  readonly metadata?: Readonly<Record<string, unknown>>;
}

export const useWorkspaceStore = defineStore("workspace", () => {
  const phase = ref<WorkspacePhase>("idle");
  const collections = ref<readonly CollectionSummary[]>([]);
  const displayNames = ref<Readonly<Record<string, string>>>({});
  const currentTable = ref<string | null>(null);
  const lastError = ref<string | null>(null);

  function beginOpen(): void {
    phase.value = "opening";
    lastError.value = null;
  }

  function setOpened(
    cols: readonly CollectionSummary[],
    names: Readonly<Record<string, string>>,
  ): void {
    collections.value = cols;
    displayNames.value = names;
    phase.value = "opened";
    lastError.value = null;
  }

  function setCollections(
    cols: readonly CollectionSummary[],
    names: Readonly<Record<string, string>>,
  ): void {
    collections.value = cols;
    displayNames.value = names;
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
    displayNames.value = {};
    currentTable.value = null;
    lastError.value = null;
  }

  return {
    phase,
    collections,
    displayNames,
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
