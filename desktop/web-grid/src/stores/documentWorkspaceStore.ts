import { computed, ref } from "vue";
import { defineStore } from "pinia";

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
  | "commitRevision"
  | "promoteVersion"
  | "schemes";

export interface DocumentEntry {
  /** Opaque, session-bound capability. It is never a local path. */
  readonly entryHandle: string;
  readonly displayName: string;
  readonly authority: DocumentAuthority;
  readonly availability: DocumentAvailability;
  readonly mimeType?: string;
  readonly size?: number;
  readonly modifiedAt?: string;
  readonly versionLabel?: string;
  readonly capabilities: readonly DocumentCapability[];
}

export interface DocumentRevision {
  readonly revisionHandle: string;
  readonly label: string;
  readonly createdAt: string;
  readonly size?: number;
  readonly author?: string;
}

export interface DocumentScheme {
  readonly schemeHandle: string;
  readonly name: string;
  readonly currentRevisionHandle: string | null;
  readonly currentRevisionLabel: string | null;
  readonly archived: boolean;
  readonly active: boolean;
}

export type DocumentWorkspacePhase = "idle" | "loading" | "ready" | "failed";
export type InspectorTab = "preview" | "history" | "schemes";

export const useDocumentWorkspaceStore = defineStore("documentWorkspace", () => {
  const phase = ref<DocumentWorkspacePhase>("idle");
  const entries = ref<readonly DocumentEntry[]>([]);
  const selectedHandles = ref<readonly string[]>([]);
  const primaryHandle = ref<string | null>(null);
  const selectionAnchor = ref<number | null>(null);
  const inspectorTab = ref<InspectorTab>("preview");
  const lastError = ref<string | null>(null);
  const lastErrorCode = ref<string | null>(null);
  const query = ref("");
  const authorityFilter = ref<DocumentAuthority>("workspace");
  const revisions = ref<Readonly<Record<string, readonly DocumentRevision[]>>>({});
  const historyLoadingFor = ref<string | null>(null);
  const schemes = ref<Readonly<Record<string, readonly DocumentScheme[]>>>({});
  const schemesLoadingFor = ref<string | null>(null);
  const activeOperation = ref<string | null>(null);

  const visibleEntries = computed(() => {
    const needle = query.value.trim().toLocaleLowerCase();
    return entries.value.filter(
      (entry) =>
        entry.authority === authorityFilter.value &&
        (!needle || entry.displayName.toLocaleLowerCase().includes(needle)),
    );
  });

  const primaryEntry = computed(
    () => entries.value.find((entry) => entry.entryHandle === primaryHandle.value) ?? null,
  );

  function beginLoad(): void {
    phase.value = "loading";
    lastError.value = null;
    lastErrorCode.value = null;
  }

  function setEntries(next: readonly DocumentEntry[]): void {
    entries.value = next;
    phase.value = "ready";
    lastError.value = null;
    lastErrorCode.value = null;
    const handles = new Set(next.map((entry) => entry.entryHandle));
    selectedHandles.value = selectedHandles.value.filter((handle) => handles.has(handle));
    if (!primaryHandle.value || !handles.has(primaryHandle.value)) {
      primaryHandle.value = null;
      selectionAnchor.value = null;
    }
  }

  function setFailed(message: string, code: string | null = null): void {
    phase.value = "failed";
    lastError.value = message;
    lastErrorCode.value = code;
    historyLoadingFor.value = null;
    schemesLoadingFor.value = null;
    activeOperation.value = null;
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
  }

  function selectAllVisible(): void {
    selectedHandles.value = visibleEntries.value.map((entry) => entry.entryHandle);
    primaryHandle.value = visibleEntries.value.at(-1)?.entryHandle ?? null;
    selectionAnchor.value = visibleEntries.value.length ? 0 : null;
  }

  function clearSelection(): void {
    selectedHandles.value = [];
    primaryHandle.value = null;
    selectionAnchor.value = null;
  }

  function showInspector(tab: InspectorTab): void {
    inspectorTab.value = tab;
  }

  function beginHistory(entryHandle: string): void {
    historyLoadingFor.value = entryHandle;
    inspectorTab.value = "history";
  }

  function setHistory(entryHandle: string, next: readonly DocumentRevision[]): void {
    revisions.value = { ...revisions.value, [entryHandle]: next };
    if (historyLoadingFor.value === entryHandle) historyLoadingFor.value = null;
  }

  function beginSchemes(entryHandle: string): void {
    schemesLoadingFor.value = entryHandle;
    inspectorTab.value = "schemes";
  }

  function setSchemes(entryHandle: string, next: readonly DocumentScheme[]): void {
    schemes.value = { ...schemes.value, [entryHandle]: next };
    if (schemesLoadingFor.value === entryHandle) schemesLoadingFor.value = null;
  }

  function beginOperation(operation: string): void {
    activeOperation.value = operation;
    lastError.value = null;
    lastErrorCode.value = null;
  }

  function finishOperation(): void {
    activeOperation.value = null;
  }

  function updateCurrentRevision(entryHandle: string, label: string): void {
    entries.value = entries.value.map((entry) =>
      entry.entryHandle === entryHandle ? { ...entry, versionLabel: label } : entry,
    );
  }

  function clear(): void {
    phase.value = "idle";
    entries.value = [];
    query.value = "";
    revisions.value = {};
    schemes.value = {};
    historyLoadingFor.value = null;
    schemesLoadingFor.value = null;
    activeOperation.value = null;
    lastError.value = null;
    lastErrorCode.value = null;
    clearSelection();
  }

  return {
    phase,
    entries,
    selectedHandles,
    primaryHandle,
    primaryEntry,
    inspectorTab,
    lastError,
    lastErrorCode,
    query,
    authorityFilter,
    revisions,
    historyLoadingFor,
    schemes,
    schemesLoadingFor,
    activeOperation,
    visibleEntries,
    beginLoad,
    setEntries,
    setFailed,
    setQuery,
    setAuthorityFilter,
    selectAt,
    selectAllVisible,
    clearSelection,
    showInspector,
    beginHistory,
    setHistory,
    beginSchemes,
    setSchemes,
    beginOperation,
    finishOperation,
    updateCurrentRevision,
    clear,
  };
});
