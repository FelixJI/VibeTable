import { computed, ref } from "vue";
import { defineStore } from "pinia";
import type { DocumentDiffCompletedPayload, DocumentDiffChange, DocumentDiffChangePageResult } from "@/contracts";
import { registerWorkspaceEpochReset } from "@/stores/workspaceSessionStore";

export type DocumentAuthority = "workspace";
export type DocumentAvailability =
  | "available"
  | "missing"
  | "unmounted"
  | "unmanaged"
  | "unsafe"
  | "remote";
export type DocumentCapability =
  | "open"
  | "preview"
  | "reveal"
  | "history"
  | "relink"
  | "dragOut"
  | "unlink"
  | "diff";

export interface DocumentEntry {
  /** Stable canonical UUID used only by the workspace FileHistory authority. */
  readonly documentId: string;
  /** Opaque, session-bound capability. It is never a local path. */
  readonly entryHandle: string;
  readonly displayName: string;
  readonly relativePath: string;
  readonly extension: string;
  readonly authority: DocumentAuthority;
  readonly availability: DocumentAvailability;
  readonly mimeType: string;
  readonly sizeBytes: number;
  readonly effectiveRevisionCreatedAt: string;
  readonly formalVersion: number | null;
  readonly status: "active" | "deleted";
  readonly versionLabel?: string;
  readonly effectiveRevisionId?: string;
  readonly capabilities: readonly DocumentCapability[];
}

export type DocumentWorkspacePhase = "idle" | "loading" | "ready" | "failed";
export type DocumentDiffPhase = "idle" | "busy" | "ready" | "failed";
export type InspectorTab = "preview" | "history";

export const useDocumentWorkspaceStore = defineStore("documentWorkspace", () => {
  const phase = ref<DocumentWorkspacePhase>("idle");
  const entries = ref<readonly DocumentEntry[]>([]);
  const documentLabels = ref<Readonly<Record<string, string>>>({});
  const nextCursor = ref<string | null>(null);
  const topologyRevision = ref<number | null>(null);
  const selectedHandles = ref<readonly string[]>([]);
  const primaryHandle = ref<string | null>(null);
  const selectionAnchor = ref<number | null>(null);
  const inspectorTab = ref<InspectorTab>("preview");
  const lastError = ref<string | null>(null);
  const lastErrorCode = ref<string | null>(null);
  const query = ref("");
  const authorityFilter = ref<DocumentAuthority>("workspace");
  const diffPhase = ref<DocumentDiffPhase>("idle");
  const diffResult = ref<DocumentDiffCompletedPayload | null>(null);
  const diffChanges = ref<readonly DocumentDiffChange[]>([]);
  const diffNextCursor = ref<string | null>(null);
  const diffPageBusy = ref(false);
  const diffTarget = ref<{
    readonly entryHandle: string;
    readonly operationId: string;
    readonly historicalRevisionId: string;
    readonly effectiveRevisionId: string;
  } | null>(null);
  const diffError = ref<string | null>(null);
  let diffGeneration = 0;

  function resetDiff(): void {
    diffGeneration += 1;
    diffPhase.value = "idle";
    diffResult.value = null;
    diffChanges.value = [];
    diffNextCursor.value = null;
    diffPageBusy.value = false;
    diffTarget.value = null;
    diffError.value = null;
  }

  registerWorkspaceEpochReset("document-workspace", () => {
    phase.value = "idle";
    entries.value = [];
    documentLabels.value = {};
    nextCursor.value = null;
    topologyRevision.value = null;
    query.value = "";
    lastError.value = null;
    lastErrorCode.value = null;
    selectedHandles.value = [];
    primaryHandle.value = null;
    selectionAnchor.value = null;
    inspectorTab.value = "preview";
    resetDiff();
  });

  const visibleEntries = computed(() => {
    return entries.value.filter(
      (entry) => entry.authority === authorityFilter.value,
    );
  });

  const primaryEntry = computed(
    () => entries.value.find((entry) => entry.entryHandle === primaryHandle.value) ?? null,
  );

  function reconcileInspectorTab(): void {
    if (
      inspectorTab.value === "history"
      && !primaryEntry.value?.capabilities.includes("history")
    ) {
      inspectorTab.value = "preview";
    }
  }

  function beginLoad(): void {
    resetDiff();
    phase.value = "loading";
    lastError.value = null;
    lastErrorCode.value = null;
  }

  function setEntries(next: readonly DocumentEntry[]): void {
    setPage(next, null, 0, false);
  }

  function setPage(
    next: readonly DocumentEntry[],
    cursor: string | null,
    revision: number,
    append: boolean,
    preserveSelection = false,
  ): void {
    const labels = { ...documentLabels.value };
    for (const entry of next) {
      labels[entry.documentId] = entry.displayName;
    }
    documentLabels.value = labels;
    const merged = append
      ? [...entries.value.filter((current) =>
          !next.some((candidate) => candidate.documentId === current.documentId)), ...next]
      : next;
    const rebindHandle = (handle: string | null): string | null => {
      const previous = entries.value.find((entry) => entry.entryHandle === handle);
      if (!previous) return null;
      return merged.find((entry) => entry.documentId === previous.documentId
        && entry.authority === previous.authority)?.entryHandle ?? null;
    };
    const selected = preserveSelection
      ? selectedHandles.value.map(rebindHandle).filter((handle): handle is string => handle !== null)
      : selectedHandles.value;
    const primary = preserveSelection ? rebindHandle(primaryHandle.value) : primaryHandle.value;
    const anchor = preserveSelection && selectionAnchor.value !== null
      ? rebindHandle(visibleEntries.value[selectionAnchor.value]?.entryHandle ?? null)
      : null;
    const target = diffTarget.value;
    if (target) {
      const current = merged.find((entry) => entry.entryHandle === target.entryHandle);
      if (!current || current.effectiveRevisionId !== target.effectiveRevisionId) {
        resetDiff();
      }
    }
    entries.value = merged;
    nextCursor.value = cursor;
    topologyRevision.value = revision;
    phase.value = "ready";
    lastError.value = null;
    lastErrorCode.value = null;
    const handles = new Set(merged.map((entry) => entry.entryHandle));
    selectedHandles.value = selected.filter((handle) => handles.has(handle));
    primaryHandle.value = primary;
    if (preserveSelection) {
      const index = visibleEntries.value.findIndex((entry) => entry.entryHandle === anchor);
      selectionAnchor.value = index < 0 ? null : index;
    }
    if (!primaryHandle.value || !handles.has(primaryHandle.value)) {
      primaryHandle.value = null;
      selectionAnchor.value = null;
    }
    reconcileInspectorTab();
  }

  function setFailed(message: string, code: string | null = null): void {
    phase.value = "failed";
    lastError.value = message;
    lastErrorCode.value = code;
  }

  function removeActiveDocument(documentId: string): void {
    const removed = entries.value.find((entry) => entry.documentId === documentId);
    if (!removed) return;
    entries.value = entries.value.filter((entry) => entry.documentId !== documentId);
    selectedHandles.value = selectedHandles.value.filter(
      (handle) => handle !== removed.entryHandle,
    );
    if (primaryHandle.value === removed.entryHandle) {
      primaryHandle.value = null;
      selectionAnchor.value = null;
    }
    reconcileInspectorTab();
    if (diffTarget.value?.entryHandle === removed.entryHandle) resetDiff();
  }

  function setQuery(next: string): void {
    query.value = next;
  }

  function setAuthorityFilter(next: DocumentAuthority): void {
    authorityFilter.value = next;
    clearSelection();
  }

  function selectAt(index: number, options: { toggle?: boolean; range?: boolean } = {}): void {
    const list = visibleEntries.value;
    const entry = list[index];
    if (!entry) return;

    if (options.range && selectionAnchor.value !== null) {
      const from = Math.min(selectionAnchor.value, index);
      const to = Math.max(selectionAnchor.value, index);
      const range = list.slice(from, to + 1).map((item) => item.entryHandle);
      selectedHandles.value = options.toggle
        ? [...new Set([...selectedHandles.value, ...range])]
        : range;
    } else if (options.toggle) {
      selectedHandles.value = selectedHandles.value.includes(entry.entryHandle)
        ? selectedHandles.value.filter((handle) => handle !== entry.entryHandle)
        : [...selectedHandles.value, entry.entryHandle];
      selectionAnchor.value = index;
    } else {
      selectedHandles.value = [entry.entryHandle];
      selectionAnchor.value = index;
    }
    primaryHandle.value = entry.entryHandle;
    inspectorTab.value = "preview";
  }

  function selectAllVisible(): void {
    selectedHandles.value = visibleEntries.value.map((entry) => entry.entryHandle);
    primaryHandle.value = visibleEntries.value.at(-1)?.entryHandle ?? null;
    selectionAnchor.value = visibleEntries.value.length ? 0 : null;
    inspectorTab.value = "preview";
  }

  function clearSelection(): void {
    selectedHandles.value = [];
    primaryHandle.value = null;
    selectionAnchor.value = null;
    inspectorTab.value = "preview";
  }

  function showInspector(tab: InspectorTab): void {
    inspectorTab.value = tab === "history"
      && !primaryEntry.value?.capabilities.includes("history")
      ? "preview"
      : tab;
  }

  function beginDiff(
    entryHandle: string,
    historicalRevisionId: string,
    effectiveRevisionId: string,
    operationId: string = crypto.randomUUID(),
  ): number {
    diffGeneration += 1;
    diffPhase.value = "busy";
    diffResult.value = null;
    diffChanges.value = [];
    diffNextCursor.value = null;
    diffPageBusy.value = false;
    diffError.value = null;
    diffTarget.value = {
      entryHandle,
      operationId,
      historicalRevisionId,
      effectiveRevisionId,
    };
    return diffGeneration;
  }

  function completeDiff(
    generation: number,
    result: DocumentDiffCompletedPayload,
  ): boolean {
    const target = diffTarget.value;
    if (generation !== diffGeneration || !target || (result.outcome === "ready" && (
      target.entryHandle !== result.session.entryHandle ||
      target.historicalRevisionId !== result.session.historicalRevisionId ||
      target.effectiveRevisionId !== result.session.effectiveRevisionId))) {
      return false;
    }
    diffResult.value = result;
    diffPhase.value = result.outcome === "failure" ? "failed" : "ready";
    diffError.value = result.failure;
    return true;
  }

  function failDiff(generation: number, message: string): boolean {
    if (generation !== diffGeneration) return false;
    diffResult.value = null;
    diffChanges.value = [];
    diffNextCursor.value = null;
    diffPhase.value = "failed";
    diffError.value = message;
    return true;
  }

  function cancelDiff(): void {
    resetDiff();
  }

  function completeDiffPage(generation: number, result: DocumentDiffChangePageResult): boolean {
    if (generation !== diffGeneration || diffResult.value?.outcome !== "ready") return false;
    diffPageBusy.value = false;
    if (result.outcome === "failure") {
      diffResult.value = null;
      diffChanges.value = [];
      diffNextCursor.value = null;
      diffPhase.value = "failed";
      diffError.value = result.failure;
      return true;
    }
    if (result.page.sessionId !== diffResult.value.session.sessionId) return false;
    const ids = new Set(diffChanges.value.map(change => change.changeId));
    if (result.page.changes.some(change => ids.has(change.changeId))) throw new Error("Duplicate diff change");
    const merged = [...diffChanges.value, ...result.page.changes];
    const expected = diffResult.value.session.summary.totalChangeGroups;
    if (merged.length > expected || (result.page.nextCursor === null && merged.length !== expected)) {
      throw new Error("Diff page count does not match summary");
    }
    diffChanges.value = merged;
    diffNextCursor.value = result.page.nextCursor;
    return true;
  }

  function currentDiffGeneration(): number { return diffGeneration; }

  function clear(): void {
    phase.value = "idle";
    entries.value = [];
    documentLabels.value = {};
    nextCursor.value = null;
    topologyRevision.value = null;
    query.value = "";
    lastError.value = null;
    lastErrorCode.value = null;
    clearSelection();
    resetDiff();
  }

  return {
    phase,
    entries,
    documentLabels,
    nextCursor,
    topologyRevision,
    selectedHandles,
    primaryHandle,
    primaryEntry,
    inspectorTab,
    lastError,
    lastErrorCode,
    query,
    authorityFilter,
    diffPhase,
    diffResult,
    diffChanges,
    diffNextCursor,
    diffPageBusy,
    diffTarget,
    diffError,
    visibleEntries,
    beginLoad,
    setEntries,
    setPage,
    setFailed,
    removeActiveDocument,
    setQuery,
    setAuthorityFilter,
    selectAt,
    selectAllVisible,
    clearSelection,
    showInspector,
    beginDiff,
    completeDiff,
    completeDiffPage,
    currentDiffGeneration,
    failDiff,
    cancelDiff,
    clear,
  };
});
