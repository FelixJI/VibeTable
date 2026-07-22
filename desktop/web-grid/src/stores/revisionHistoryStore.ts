import { computed, ref } from "vue";
import { defineStore } from "pinia";
import type {
  HistoryChangeSet,
  HistoryPage,
  RestorePreview,
  RestoreResult,
  RevisionHistoryScope,
} from "@/contracts";

export interface RevisionHistorySelection {
  readonly scope: "table" | "row" | "cell" | "multiple";
  readonly itemId?: string;
  readonly field?: string;
}

export type OpenRevisionHistorySelection = RevisionHistorySelection & {
  readonly scope: "table" | "row" | "cell";
};

export interface RevisionHistoryQuery {
  search: string;
  actorId: string;
  actions: string[];
  field: string;
  recordId: string;
  dateFrom: string;
  dateTo: string;
}

export type RevisionHistoryPhase =
  | "idle"
  | "loading"
  | "loadingMore"
  | "ready"
  | "empty"
  | "failed"
  | "unavailable";
export type RestoreFlowPhase = "idle" | "previewing" | "ready" | "applying" | "failed";

const emptyQuery = (): RevisionHistoryQuery => ({
  search: "",
  actorId: "",
  actions: [],
  field: "",
  recordId: "",
  dateFrom: "",
  dateTo: "",
});

/**
 * Server revision history state. This deliberately stays separate from
 * historyStore, which is the short-lived Ctrl+Z/Ctrl+Shift+Z session stack.
 */
export const useRevisionHistoryStore = defineStore("revisionHistory", () => {
  const panelOpen = ref(false);
  const selection = ref<RevisionHistorySelection>({ scope: "table" });
  const scope = ref<RevisionHistoryScope>("table");
  const itemId = ref<string | null>(null);
  const field = ref<string | null>(null);
  const query = ref<RevisionHistoryQuery>(emptyQuery());
  const phase = ref<RevisionHistoryPhase>("idle");
  const changeSets = ref<readonly HistoryChangeSet[]>([]);
  const total = ref(0);
  const hasMore = ref(false);
  const offset = ref(0);
  const limit = 50;
  const capabilityHash = ref("");
  const schemaRevision = ref("");
  const lastError = ref<string | null>(null);
  const lastErrorCode = ref<string | null>(null);

  const restorePhase = ref<RestoreFlowPhase>("idle");
  const preview = ref<RestorePreview | null>(null);
  const previewItemId = ref<string | null>(null);
  const previewRevision = ref<string | null>(null);
  const previewField = ref<string | null>(null);
  const restoreError = ref<string | null>(null);
  const restoreErrorCode = ref<string | null>(null);
  const lastApplied = ref<RestoreResult | null>(null);
  const appliedSequence = ref(0);

  const canApply = computed(() => {
    if (restorePhase.value !== "ready" || !preview.value?.token) return false;
    if (preview.value.canApply !== undefined) return preview.value.canApply;
    return preview.value.scalarChanges.length + preview.value.relationChanges.length > 0;
  });
  const isFiltered = computed(() => {
    const value = query.value;
    return !!(
      value.search || value.actorId || value.actions.length || value.field ||
      value.recordId || value.dateFrom || value.dateTo
    );
  });

  function setSelection(next: RevisionHistorySelection): void {
    selection.value = next;
  }

  function open(next: OpenRevisionHistorySelection | { scope: "archived" }): void {
    panelOpen.value = true;
    scope.value = next.scope;
    itemId.value = "itemId" in next ? next.itemId ?? null : null;
    field.value = "field" in next ? next.field ?? null : null;
    if (next.scope !== "archived") selection.value = next;
    clearPreview();
  }

  function close(): void {
    panelOpen.value = false;
    clearPreview();
  }

  function updateQuery(next: Partial<RevisionHistoryQuery>): void {
    query.value = { ...query.value, ...next };
  }

  function clearFilters(): void {
    query.value = emptyQuery();
  }

  function beginLoad(append = false): void {
    phase.value = append ? "loadingMore" : "loading";
    lastError.value = null;
    lastErrorCode.value = null;
    if (!append) {
      offset.value = 0;
      changeSets.value = [];
      total.value = 0;
      hasMore.value = false;
    }
  }

  function receivePage(page: HistoryPage, append = false): void {
    const incoming = page.changeSets ?? [];
    changeSets.value = append ? [...changeSets.value, ...incoming] : incoming;
    total.value = page.total;
    offset.value = changeSets.value.length;
    hasMore.value = page.hasMore ?? changeSets.value.length < page.total;
    capabilityHash.value = page.capabilityHash;
    schemaRevision.value = page.schemaRevision;
    phase.value = changeSets.value.length ? "ready" : "empty";
    lastError.value = null;
    lastErrorCode.value = null;
  }

  function failLoad(message: string, code: string | null = null): void {
    lastError.value = message;
    lastErrorCode.value = code;
    phase.value = code === "history_not_allowed" || code === "archive_not_supported"
      ? "unavailable"
      : "failed";
  }

  function beginPreview(target: {
    itemId: string;
    revisionId: string;
    field?: string | null;
  }): void {
    previewItemId.value = target.itemId;
    previewRevision.value = target.revisionId;
    previewField.value = target.field ?? null;
    preview.value = null;
    restorePhase.value = "previewing";
    restoreError.value = null;
    restoreErrorCode.value = null;
  }

  function receivePreview(next: RestorePreview): void {
    preview.value = next;
    restorePhase.value = "ready";
    restoreError.value = null;
    restoreErrorCode.value = null;
  }

  function beginApply(): void {
    if (!canApply.value) return;
    restorePhase.value = "applying";
    restoreError.value = null;
    restoreErrorCode.value = null;
  }

  function failRestore(message: string, code: string | null = null): void {
    restorePhase.value = "failed";
    restoreError.value = message;
    restoreErrorCode.value = code;
  }

  function completeRestore(result: RestoreResult): void {
    lastApplied.value = result;
    appliedSequence.value += 1;
    clearPreview();
  }

  function clearPreview(): void {
    restorePhase.value = "idle";
    preview.value = null;
    previewItemId.value = null;
    previewRevision.value = null;
    previewField.value = null;
    restoreError.value = null;
    restoreErrorCode.value = null;
  }

  function reset(): void {
    panelOpen.value = false;
    selection.value = { scope: "table" };
    scope.value = "table";
    itemId.value = null;
    field.value = null;
    query.value = emptyQuery();
    phase.value = "idle";
    changeSets.value = [];
    total.value = 0;
    hasMore.value = false;
    offset.value = 0;
    capabilityHash.value = "";
    schemaRevision.value = "";
    lastError.value = null;
    lastErrorCode.value = null;
    lastApplied.value = null;
    appliedSequence.value = 0;
    clearPreview();
  }

  return {
    panelOpen,
    selection,
    scope,
    itemId,
    field,
    query,
    phase,
    changeSets,
    total,
    hasMore,
    offset,
    limit,
    capabilityHash,
    schemaRevision,
    lastError,
    lastErrorCode,
    restorePhase,
    preview,
    previewItemId,
    previewRevision,
    previewField,
    restoreError,
    restoreErrorCode,
    lastApplied,
    appliedSequence,
    canApply,
    isFiltered,
    setSelection,
    open,
    close,
    updateQuery,
    clearFilters,
    beginLoad,
    receivePage,
    failLoad,
    beginPreview,
    receivePreview,
    beginApply,
    failRestore,
    completeRestore,
    clearPreview,
    reset,
  };
});
