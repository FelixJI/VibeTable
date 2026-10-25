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

/** Absent metadata and an empty metadata bag are the same catalog entry. */
function sameMetadata(
  a: Readonly<Record<string, unknown>> | undefined,
  b: Readonly<Record<string, unknown>> | undefined,
): boolean {
  if (a === b) return true;
  const left = a ?? {};
  const right = b ?? {};
  const leftKeys = Object.keys(left);
  if (leftKeys.length !== Object.keys(right).length) return false;
  return leftKeys.every((key) => Object.is(left[key], right[key]));
}

/** Semantic catalog equality — no hash or serialized identity is introduced. */
function sameCollections(
  a: readonly CollectionSummary[],
  b: readonly CollectionSummary[],
): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  return a.every((item, index) =>
    item.collection === b[index].collection && sameMetadata(item.metadata, b[index].metadata));
}

function sameDisplayNames(
  a: Readonly<Record<string, string>>,
  b: Readonly<Record<string, string>>,
): boolean {
  if (a === b) return true;
  const leftKeys = Object.keys(a);
  if (leftKeys.length !== Object.keys(b).length) return false;
  return leftKeys.every((key) => Object.is(a[key], b[key]));
}

export const useWorkspaceStore = defineStore("workspace", () => {
  const phase = ref<WorkspacePhase>("idle");
  let phaseBeforeOpen: WorkspacePhase = "idle";
  const collections = ref<readonly CollectionSummary[]>([]);
  const displayNames = ref<Readonly<Record<string, string>>>({});
  const currentTable = ref<string | null>(null);
  const lastError = ref<string | null>(null);

  function beginOpen(): void {
    if (phase.value !== "opening") phaseBeforeOpen = phase.value;
    phase.value = "opening";
    lastError.value = null;
  }

  function cancelOpen(): void {
    if (phase.value !== "opening") return;
    phase.value = phaseBeforeOpen;
    lastError.value = null;
  }

  function setOpened(
    cols: readonly CollectionSummary[],
    names: Readonly<Record<string, string>>,
  ): void {
    // An equal-content catalog keeps its array identity so reference-based
    // stale guards (e.g. field-settings async reads) are not retired by a
    // duplicate projection of the same host snapshot.
    if (!sameCollections(collections.value, cols)
      || !sameDisplayNames(displayNames.value, names)) {
      collections.value = cols;
      displayNames.value = names;
    }
    phase.value = "opened";
    lastError.value = null;
  }

  function setCollections(
    cols: readonly CollectionSummary[],
    names: Readonly<Record<string, string>>,
  ): void {
    if (currentTable.value && !cols.some(item => item.collection === currentTable.value)) {
      lastError.value = `数据表“${displayNames.value[currentTable.value] ?? currentTable.value}”已删除或不可用，请选择其他数据表。`;
      currentTable.value = null;
    }
    if (sameCollections(collections.value, cols)
      && sameDisplayNames(displayNames.value, names)) return;
    collections.value = cols;
    displayNames.value = names;
  }

  function selectTable(name: string): void {
    currentTable.value = name;
    lastError.value = null;
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
    cancelOpen,
    setOpened,
    setCollections,
    selectTable,
    setFailed,
    clear,
  };
});
