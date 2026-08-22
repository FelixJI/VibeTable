<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { NAlert, NButton, NIcon, NInput, NModal, NSelect } from "naive-ui";
import { FileQuestion, FilePlus2, Files, RefreshCw, Search } from "lucide-vue-next";
import DocumentList from "@/components/files/DocumentList.vue";
import DocumentContextMenu from "@/components/files/DocumentContextMenu.vue";
import DocumentInspector from "@/components/files/DocumentInspector.vue";
import { createDocumentWorkspaceService, defaultDocumentQuery, type DocumentWorkspaceIntent, type DocumentWorkspaceScope } from "@/services/documentWorkspaceService";
import { useDocumentWorkspaceStore, type DocumentCapability, type DocumentEntry } from "@/stores/documentWorkspaceStore";
import type { FileRevisionV2 } from "@/contracts/workspaceV2";
import { useWorkspaceProtectionStore } from "@/stores/workspaceProtectionStore";
import { t } from "@/i18n";
import type {
  FileDocumentFilter,
  FileDocumentFilterField,
  FileDocumentQuery,
  PendingFileChange,
  WorkspaceV2UiAction,
} from "@/contracts/workspaceV2Bridge";
import { HOST_FILE_UPGRADE_GRANT } from "@/services/workspaceV2HostAdapter";

const props = withDefaults(defineProps<{
  scope?: DocumentWorkspaceScope;
  requestedRevisionId?: string | null;
}>(), {
  scope: () => ({ kind: "global" as const }),
  requestedRevisionId: null,
});
type WorkspaceFileHistoryAction = WorkspaceV2UiAction<
  | "fileHistory.readTree"
  | "fileHistory.restore"
  | "fileHistory.upgrade"
  | "fileHistory.activateLeaf"
  | "fileHistory.unlink"
  | "fileHistory.listPendingChanges"
  | "fileHistory.applyPendingChange"
>;
const emit = defineEmits<{
  intent: [intent: DocumentWorkspaceIntent];
  workspaceV2Action: [action: WorkspaceFileHistoryAction];
}>();
const store = useDocumentWorkspaceStore();
const protection = useWorkspaceProtectionStore();
const service = createDocumentWorkspaceService((intent) => emit("intent", intent));
const menu = ref<{ entry: DocumentEntry; x: number; y: number } | null>(null);
const unlinkTarget = ref<DocumentEntry | null>(null);
const pendingChangesOpen = ref(false);
const pendingCandidates = ref<Record<string, string>>({});
const dropDepth = ref(0);
const dropActive = ref(false);
const dropFeedback = ref(false);
const filterLogic = ref<"and" | "or">("and");
const filterField = ref<FileDocumentFilterField>("extension");
const filterOperator = ref<FileDocumentFilter["operator"]>("eq");
const filterValue = ref("");
const metadataFilters = ref<readonly FileDocumentFilter[]>([]);
let dropFeedbackTimer: ReturnType<typeof setTimeout> | null = null;
let queryTimer: ReturnType<typeof setTimeout> | null = null;
const selectedCount = computed(() => store.selectedHandles.length);
const currentRevisionTree = computed(() =>
  store.primaryEntry ? protection.fileTrees[store.primaryEntry.documentId] ?? null : null);

function pendingCandidateEntries(change: PendingFileChange): readonly DocumentEntry[] {
  const ids = new Set(change.candidateDocumentIds);
  return store.entries.filter((entry) =>
    ids.has(entry.documentId) && Boolean(entry.effectiveRevisionId));
}

function selectedPendingCandidate(change: PendingFileChange): DocumentEntry | null {
  const candidates = pendingCandidateEntries(change);
  const selected = pendingCandidates.value[change.changeId];
  return candidates.find((entry) => entry.documentId === selected)
    ?? candidates[0]
    ?? null;
}

function openPendingChanges(): void {
  pendingCandidates.value = Object.fromEntries(
    protection.pendingFileChanges.flatMap((change) => {
      const candidate = pendingCandidateEntries(change)[0];
      return candidate ? [[change.changeId, candidate.documentId]] : [];
    }),
  );
  pendingChangesOpen.value = true;
}

function applyPendingChange(
  change: PendingFileChange,
  action: "new" | "move" | "copy" | "delete" | "dismiss",
): void {
  const needsIdentity = action === "move" || action === "copy" || action === "delete";
  const candidate = needsIdentity ? selectedPendingCandidate(change) : null;
  if (needsIdentity && (!candidate?.effectiveRevisionId)) return;
  emit("workspaceV2Action", {
    method: "fileHistory.applyPendingChange",
    params: {
      changeId: change.changeId,
      action,
      documentId: candidate?.documentId ?? null,
      expectedEffectiveRevisionId: candidate?.effectiveRevisionId ?? null,
    },
  });
}

function formatPendingSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function requestList(): void {
  store.setAuthorityFilter("workspace");
  store.beginLoad();
  service.list(props.scope, "workspace", currentQuery());
}

const fieldOptions: { value: FileDocumentFilterField; label: string }[] = [
  { value: "displayName", label: t("files.filter.displayName") },
  { value: "relativePath", label: t("files.filter.relativePath") },
  { value: "extension", label: t("files.filter.extension") },
  { value: "mimeType", label: t("files.filter.mimeType") },
  { value: "sizeBytes", label: t("files.filter.sizeBytes") },
  { value: "effectiveRevisionCreatedAt", label: t("files.filter.revisionTime") },
  { value: "status", label: t("files.filter.status") },
];
const operatorOptions = computed<readonly FileDocumentFilter["operator"][]>(() => {
  if (filterField.value === "sizeBytes") return ["eq", "gt", "gte", "lt", "lte"];
  if (filterField.value === "effectiveRevisionCreatedAt") return ["eq", "before", "after"];
  if (filterField.value === "status") return ["eq"];
  return ["eq", "contains"];
});

function currentQuery(cursor: string | null = null): FileDocumentQuery {
  const search = store.query.trim();
  const filters: FileDocumentFilter[] = [
    ...(search ? [{ field: "displayName" as const, operator: "contains" as const, value: search }] : []),
    ...metadataFilters.value,
  ];
  if (filters.length === 0) return defaultDocumentQuery("", cursor);
  return {
    logic: filterLogic.value,
    filters,
    sort: [{ field: "effectiveRevisionCreatedAt", direction: "desc" }],
    limit: 100,
    cursor,
  };
}

function requestNextPage(): void {
  if (!store.nextCursor || store.phase === "loading") return;
  store.beginLoad();
  service.list(props.scope, "workspace", currentQuery(store.nextCursor));
}

function addMetadataFilter(): void {
  const raw = filterValue.value.trim();
  if (!raw) return;
  const value: unknown = filterField.value === "sizeBytes" ? Number(raw) : raw;
  if (filterField.value === "sizeBytes" && !Number.isFinite(value)) return;
  metadataFilters.value = [...metadataFilters.value, {
    field: filterField.value,
    operator: filterOperator.value,
    value,
  }];
  filterValue.value = "";
  requestList();
}

function removeMetadataFilter(index: number): void {
  metadataFilters.value = metadataFilters.value.filter((_, current) => current !== index);
  requestList();
}

function select(index: number, options: { toggle: boolean; range: boolean }): void {
  store.selectAt(index, options);
  store.showInspector("preview");
}

function history(entry: DocumentEntry): void {
  const index = store.visibleEntries.findIndex(
    (candidate) => candidate.entryHandle === entry.entryHandle,
  );
  const currentEntry = store.visibleEntries[index];
  if (!currentEntry?.capabilities.includes("history")) return;
  if (store.primaryHandle !== currentEntry.entryHandle) store.selectAt(index);
  store.showInspector("history");
  emit("workspaceV2Action", {
    method: "fileHistory.readTree",
    params: { documentId: currentEntry.documentId },
  });
}

function onAction(action: DocumentCapability, entry: DocumentEntry): void {
  menu.value = null;
  if (action === "history") history(entry);
  else if (action === "unlink") unlinkTarget.value = entry;
  else if (
    action === "open" ||
    action === "preview" ||
    action === "reveal" ||
    action === "relink" ||
    action === "dragOut"
  ) {
    service[action](entry.entryHandle);
  }
}

function confirmUnlink(): void {
  const entry = unlinkTarget.value;
  if (!entry?.effectiveRevisionId) return;
  emit("workspaceV2Action", {
    method: "fileHistory.unlink",
    params: {
      documentId: entry.documentId,
      expectedEffectiveRevisionId: entry.effectiveRevisionId,
    },
  });
  unlinkTarget.value = null;
}

function restoreFileRevision(revision: FileRevisionV2): void {
  const effectiveRevisionId = currentRevisionTree.value?.effectiveRevisionId;
  if (!effectiveRevisionId) return;
  emit("workspaceV2Action", {
    method: "fileHistory.restore",
    params: {
      documentId: revision.documentId,
      expectedEffectiveRevisionId: effectiveRevisionId,
      historicalRevisionId: revision.revisionId,
    },
  });
}

function upgradeFileRevision(revision: FileRevisionV2): void {
  emit("workspaceV2Action", {
    method: "fileHistory.upgrade",
    params: {
      documentId: revision.documentId,
      revisionId: revision.revisionId,
      pathGrant: HOST_FILE_UPGRADE_GRANT,
    },
  });
}

function activateFileRevision(revision: FileRevisionV2): void {
  const effectiveRevisionId = currentRevisionTree.value?.effectiveRevisionId;
  if (!effectiveRevisionId) return;
  emit("workspaceV2Action", {
    method: "fileHistory.activateLeaf",
    params: {
      documentId: revision.documentId,
      expectedEffectiveRevisionId: effectiveRevisionId,
      targetLeafRevisionId: revision.revisionId,
    },
  });
}

function compareFileRevision(entry: DocumentEntry, revision: FileRevisionV2): void {
  const effectiveRevisionId = currentRevisionTree.value?.effectiveRevisionId;
  if (!entry.capabilities.includes("diff") ||
    !effectiveRevisionId ||
    entry.effectiveRevisionId !== effectiveRevisionId ||
    revision.revisionId === effectiveRevisionId) return;
  service.compare(
    entry.entryHandle,
    revision.revisionId,
    effectiveRevisionId,
  );
}

function cancelFileDiff(): void {
  const target = store.diffTarget;
  if (!target) return;
  service.cancelDiff(target.entryHandle, target.operationId);
  store.cancelDiff();
}

function isExternalFileDrag(event: DragEvent): boolean {
  // Reading the type list is enough to distinguish a native file drag. Do not
  // enumerate DataTransfer.files/items or read non-standard File.path values.
  return Array.from(event.dataTransfer?.types ?? []).includes("Files");
}

function onDragEnter(event: DragEvent): void {
  if (!isExternalFileDrag(event)) return;
  event.preventDefault();
  dropDepth.value += 1;
  dropActive.value = true;
}

function onDragOver(event: DragEvent): void {
  if (!isExternalFileDrag(event)) return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
}

function onDragLeave(event: DragEvent): void {
  if (!isExternalFileDrag(event)) return;
  dropDepth.value = Math.max(0, dropDepth.value - 1);
  if (dropDepth.value === 0) dropActive.value = false;
}

function onExternalDrop(event: DragEvent): void {
  if (!isExternalFileDrag(event)) return;
  event.preventDefault();
  dropDepth.value = 0;
  dropActive.value = false;
  // Enumerate only to obtain the DOM File objects required by WebView2's
  // additionalObjects channel. Never read File.path, name, bytes, or base64.
  const files = Array.from(event.dataTransfer?.files ?? []);
  service.externalDrop(props.scope, files);
  dropFeedback.value = true;
  if (dropFeedbackTimer) clearTimeout(dropFeedbackTimer);
  dropFeedbackTimer = setTimeout(() => { dropFeedback.value = false; }, 3_200);
}

onMounted(requestList);
onBeforeUnmount(() => {
  if (dropFeedbackTimer) clearTimeout(dropFeedbackTimer);
  if (queryTimer) clearTimeout(queryTimer);
});
watch(() => store.query, () => {
  if (queryTimer) clearTimeout(queryTimer);
  queryTimer = setTimeout(requestList, 220);
});
watch(filterField, () => {
  filterOperator.value = operatorOptions.value[0] ?? "eq";
});
watch(filterLogic, requestList);
watch(
  () => protection.pendingFileChanges.length,
  (count) => {
    if (count === 0) pendingChangesOpen.value = false;
  },
);
</script>

<template>
  <section
    class="file-workspace"
    data-testid="file-workspace"
    @dragenter="onDragEnter"
    @dragover="onDragOver"
    @dragleave="onDragLeave"
    @drop="onExternalDrop"
  >
    <header class="file-toolbar">
      <NInput :value="store.query" clearable size="small" class="file-search" :input-props="{ 'aria-label': t('files.search') }" :placeholder="t('files.search')" @update:value="store.setQuery">
        <template #prefix><NIcon :size="15"><Search /></NIcon></template>
      </NInput>
      <span v-if="selectedCount" class="selection-count">{{ t("files.selected", { count: selectedCount }) }}</span>
      <NButton quaternary size="small" :aria-label="t('files.refresh')" data-testid="document-refresh" @click="requestList"><template #icon><NIcon :size="16"><RefreshCw /></NIcon></template></NButton>
      <NButton type="primary" size="small" data-testid="document-import" @click="service.importFiles(scope)"><template #icon><NIcon :size="16"><FilePlus2 /></NIcon></template>{{ t("files.import") }}</NButton>
    </header>
    <div class="file-query-builder" data-testid="file-query-builder">
      <NSelect v-model:value="filterLogic" size="small" class="logic-select" data-testid="file-filter-logic" :options="[
        { value: 'and', label: t('files.filter.and') }, { value: 'or', label: t('files.filter.or') },
      ]" />
      <NSelect v-model:value="filterField" size="small" data-testid="file-filter-field" :options="fieldOptions" />
      <NSelect v-model:value="filterOperator" size="small" data-testid="file-filter-operator" :options="operatorOptions.map((value) => ({ value, label: value }))" />
      <NInput v-model:value="filterValue" size="small" data-testid="file-filter-value" :placeholder="t('files.filter.value')" @keyup.enter="addMetadataFilter" />
      <NButton size="small" data-testid="file-filter-add" @click="addMetadataFilter">{{ t("files.filter.add") }}</NButton>
      <button v-for="(filter, index) in metadataFilters" :key="`${filter.field}-${index}`" type="button" class="filter-chip" :data-testid="`file-filter-chip-${index}`" @click="removeMetadataFilter(index)">
        {{ filter.field }} {{ filter.operator }} {{ String(filter.value) }} ×
      </button>
    </div>

    <div v-if="store.phase === 'failed' && store.entries.length" class="file-error" role="alert">
      <span>{{ store.lastError }}</span><NButton text size="tiny" @click="requestList">{{ t("files.retry") }}</NButton>
    </div>
    <div v-if="dropFeedback" class="drop-feedback" role="status" data-testid="drop-feedback">
      {{ t("files.drop.forwarded") }}
    </div>
    <NAlert
      v-if="protection.pendingFileChanges.length"
      type="warning"
      class="pending-change-alert"
      :show-icon="true"
      data-testid="pending-file-change-alert"
    >
      <div class="pending-change-alert-content">
        <span>
          {{ t("files.pending.summary", {
            count: protection.pendingFileChanges.length,
          }) }}
        </span>
        <NButton size="tiny" @click="openPendingChanges">
          {{ t("files.pending.review") }}
        </NButton>
      </div>
    </NAlert>

    <div class="file-body">
      <div v-if="dropActive" class="external-drop-zone" data-testid="external-drop-zone">
        <span><NIcon :size="24"><FilePlus2 /></NIcon></span>
        <strong>{{ t("files.drop.title") }}</strong>
        <p>{{ t("files.drop.hint") }}</p>
      </div>
      <main class="file-list-pane">
        <div v-if="store.phase === 'loading' && store.entries.length === 0" class="file-skeleton" aria-busy="true">
          <i v-for="index in 8" :key="index"></i>
        </div>
        <div v-else-if="store.phase === 'failed' && store.entries.length === 0" class="file-empty">
          <span><NIcon :size="22"><Files /></NIcon></span><strong>{{ t("files.failed") }}</strong><p>{{ store.lastError }}</p><NButton size="small" @click="requestList">{{ t("files.retry") }}</NButton>
        </div>
        <div v-else-if="store.visibleEntries.length === 0" class="file-empty">
          <span><NIcon :size="22"><Files /></NIcon></span>
          <strong>{{ store.query ? t("files.search.empty") : t("files.empty") }}</strong>
          <p>{{ store.query ? t("files.search.emptyHint") : t("files.emptyHint") }}</p>
          <NButton v-if="!store.query" size="small" type="primary" @click="service.importFiles(scope)">{{ t("files.import") }}</NButton>
        </div>
        <DocumentList
          v-else
          :entries="store.visibleEntries"
          :selected-handles="store.selectedHandles"
          @select="select"
          @select-all="store.selectAllVisible"
          @open="service.open($event.entryHandle)"
          @preview="service.preview($event.entryHandle)"
          @context="(entry, point) => menu = { entry, ...point }"
          @drag-out="service.dragOut($event.entryHandle)"
        />
        <div v-if="store.nextCursor" class="file-pagination">
          <NButton
            size="small"
            :loading="store.phase === 'loading'"
            data-testid="document-load-more"
            @click="requestNextPage"
          >
            {{ t("files.loadMore") }}
          </NButton>
        </div>
      </main>
      <DocumentInspector
        :entry="store.primaryEntry"
        :active-tab="store.inspectorTab"
        :busy="protection.busyOperation !== null"
        :revision-tree="currentRevisionTree"
        :requested-revision-id="props.requestedRevisionId"
        :diff-phase="store.diffPhase"
        :diff-result="store.diffResult"
        @tab="store.showInspector"
        @preview="service.preview($event.entryHandle)"
        @history="history"
        @relink="service.relink($event.entryHandle)"
        @restore-file-revision="(_entry, revision) => restoreFileRevision(revision)"
        @upgrade-file-revision="(_entry, revision) => upgradeFileRevision(revision)"
        @activate-file-revision="(_entry, revision) => activateFileRevision(revision)"
        @compare-file-revision="compareFileRevision"
        @cancel-file-diff="cancelFileDiff"
      />
    </div>

    <DocumentContextMenu v-if="menu" :entry="menu.entry" :x="menu.x" :y="menu.y" @action="onAction" @close="menu = null" />
    <NModal
      :show="unlinkTarget !== null"
      preset="card"
      :title="t('files.unlink.title')"
      class="unlink-modal"
      @update:show="value => { if (!value) unlinkTarget = null; }"
    >
      <NAlert type="warning" :show-icon="true">
        {{ t("files.unlink.hint") }}
      </NAlert>
      <template #footer>
        <div class="modal-actions">
          <NButton @click="unlinkTarget = null">{{ t("common.cancel") }}</NButton>
          <NButton
            type="error"
            data-testid="document-unlink-confirm"
            :disabled="!unlinkTarget?.effectiveRevisionId"
            @click="confirmUnlink"
          >
            {{ t("files.unlink.confirm") }}
          </NButton>
        </div>
      </template>
    </NModal>
    <NModal
      :show="pendingChangesOpen"
      preset="card"
      :title="t('files.pending.title')"
      class="pending-changes-modal"
      :trap-focus="true"
      :auto-focus="true"
      @update:show="value => { pendingChangesOpen = value; }"
    >
      <NAlert type="info" :show-icon="true">
        {{ t("files.pending.hint") }}
      </NAlert>
      <div class="pending-change-list">
        <article
          v-for="change in protection.pendingFileChanges"
          :key="change.changeId"
          class="pending-change-card"
          :data-testid="`pending-change-${change.changeId}`"
        >
          <span class="pending-change-icon" aria-hidden="true">
            <NIcon :size="18"><FileQuestion /></NIcon>
          </span>
          <div class="pending-change-copy">
            <strong>{{ change.relativePath }}</strong>
            <small>
              {{ change.missing
                ? t("files.pending.missing")
                : t("files.pending.observed", { size: formatPendingSize(change.observedSize) }) }}
            </small>
            <NSelect
              v-if="pendingCandidateEntries(change).length"
              :value="selectedPendingCandidate(change)?.documentId ?? null"
              size="small"
              :aria-label="t('files.pending.candidate')"
              :options="pendingCandidateEntries(change).map(entry => ({
                label: entry.displayName,
                value: entry.documentId,
              }))"
              :data-testid="`pending-candidate-${change.changeId}`"
              @update:value="value => {
                if (typeof value === 'string') {
                  pendingCandidates = { ...pendingCandidates, [change.changeId]: value };
                }
              }"
            />
          </div>
          <div class="pending-change-actions">
            <NButton
              v-if="!change.missing"
              size="tiny"
              :disabled="protection.busyOperation !== null"
              :data-testid="`pending-new-${change.changeId}`"
              @click="applyPendingChange(change, 'new')"
            >
              {{ t("files.pending.new") }}
            </NButton>
            <NButton
              v-if="!change.missing && pendingCandidateEntries(change).length"
              size="tiny"
              :disabled="protection.busyOperation !== null"
              :data-testid="`pending-move-${change.changeId}`"
              @click="applyPendingChange(change, 'move')"
            >
              {{ t("files.pending.move") }}
            </NButton>
            <NButton
              v-if="!change.missing && pendingCandidateEntries(change).length"
              size="tiny"
              :disabled="protection.busyOperation !== null"
              :data-testid="`pending-copy-${change.changeId}`"
              @click="applyPendingChange(change, 'copy')"
            >
              {{ t("files.pending.copy") }}
            </NButton>
            <NButton
              v-if="change.missing && pendingCandidateEntries(change).length"
              size="tiny"
              type="error"
              :disabled="protection.busyOperation !== null"
              :data-testid="`pending-delete-${change.changeId}`"
              @click="applyPendingChange(change, 'delete')"
            >
              {{ t("files.pending.delete") }}
            </NButton>
            <NButton
              size="tiny"
              quaternary
              :disabled="protection.busyOperation !== null"
              :data-testid="`pending-dismiss-${change.changeId}`"
              @click="applyPendingChange(change, 'dismiss')"
            >
              {{ t("files.pending.dismiss") }}
            </NButton>
          </div>
        </article>
      </div>
      <template #footer>
        <div class="modal-actions">
          <NButton @click="pendingChangesOpen = false">{{ t("common.close") }}</NButton>
        </div>
      </template>
    </NModal>
  </section>
</template>

<style scoped>
.file-workspace { display: flex; flex-direction: column; height: 100%; min-width: 0; background: var(--vt-bg); }
.file-toolbar { display: flex; flex: 0 0 46px; align-items: center; gap: 8px; padding: 0 10px; border-bottom: 1px solid var(--vt-border); background: var(--vt-bg); }
.file-search { width: min(280px, 30vw); margin-left: 4px; }
.file-query-builder { display: grid; grid-template-columns: 125px 150px 105px minmax(150px, 1fr) auto; align-items: center; gap: 6px; min-height: 42px; padding: 5px 12px; border-bottom: 1px solid var(--vt-border); background: var(--vt-bg-subtle); }
.logic-select { min-width: 0; }
.filter-chip { grid-column: span 1; overflow: hidden; padding: 3px 8px; color: var(--vt-fg-secondary); font-size: var(--vt-font-caption); text-overflow: ellipsis; white-space: nowrap; border: 1px solid var(--vt-border); border-radius: 999px; background: var(--vt-bg); cursor: pointer; }
.filter-chip:hover { color: var(--vt-color-danger-600); border-color: color-mix(in srgb, var(--vt-color-danger-500) 45%, var(--vt-border)); }
.selection-count { margin-left: auto; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.file-body { position: relative; display: flex; flex: 1; min-height: 0; }
.drop-feedback { min-height: 32px; padding: 6px 12px; color: var(--vt-fg-accent); font-size: var(--vt-font-caption); border-bottom: 1px solid var(--vt-color-primary-100); background: var(--vt-color-primary-50); }
.pending-change-alert { margin: 8px 10px 0; }
.pending-change-alert-content { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.external-drop-zone { position: absolute; z-index: 80; inset: 12px; display: flex; flex-direction: column; align-items: center; justify-content: center; color: var(--vt-fg-accent); border: 2px dashed var(--vt-color-primary-200); border-radius: 12px; background: color-mix(in srgb, var(--vt-color-primary-50) 92%, var(--vt-bg)); box-shadow: inset 0 0 0 1px rgba(255,255,255,.7); pointer-events: none; }
.external-drop-zone span { display: grid; place-items: center; width: 48px; height: 48px; margin-bottom: 10px; border-radius: 50%; background: var(--vt-bg); box-shadow: var(--vt-shadow-1); }
.external-drop-zone strong { font-size: var(--vt-font-label); font-weight: 600; }
.external-drop-zone p { margin: 4px 0 0; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.file-error { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 34px; padding: 5px 12px; color: var(--vt-color-danger-600); border-bottom: 1px solid color-mix(in srgb, var(--vt-color-danger-500) 28%, var(--vt-border)); background: color-mix(in srgb, var(--vt-color-danger-500) 7%, var(--vt-bg)); font-size: var(--vt-font-caption); }
.file-list-pane { flex: 1; min-width: 0; overflow: auto; }
.file-pagination { display: flex; justify-content: center; padding: 10px 12px 16px; }
.file-empty { display: flex; height: 100%; min-height: 280px; flex-direction: column; align-items: center; justify-content: center; padding: 32px; color: var(--vt-fg-muted); text-align: center; }
.file-empty > span { display: grid; place-items: center; width: 44px; height: 44px; margin-bottom: 12px; color: var(--vt-color-primary-500); border-radius: var(--vt-radius-lg); background: var(--vt-color-primary-50); }
:root.dark .file-empty > span { background: rgba(91, 139, 255, 0.14); }
.file-empty strong { color: var(--vt-fg-secondary); font-weight: 550; }
.file-empty p { max-width: 380px; margin: 4px 0 14px; font-size: var(--vt-font-caption); }
.file-skeleton { padding: 33px 12px 0; }
.file-skeleton i { display: block; height: 40px; border-bottom: 1px solid var(--vt-border); background: linear-gradient(90deg, transparent 0%, var(--vt-bg-subtle) 30%, var(--vt-bg-sunken) 50%, var(--vt-bg-subtle) 70%, transparent 100%); background-size: 240% 100%; animation: shimmer 1.4s infinite; }
.modal-actions { display: flex; justify-content: flex-end; gap: 8px; }
:global(.unlink-modal) { width: min(500px, calc(100vw - 28px)); }
:global(.pending-changes-modal) { width: min(760px, calc(100vw - 28px)); }
.pending-change-list { display: grid; gap: 9px; max-height: min(58vh, 560px); margin-top: 12px; overflow: auto; }
.pending-change-card { display: grid; grid-template-columns: 34px minmax(0, 1fr) auto; align-items: center; gap: 10px; padding: 11px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: var(--vt-bg-subtle); }
.pending-change-icon { display: grid; place-items: center; width: 32px; height: 32px; color: var(--vt-color-warning); border-radius: 9px; background: color-mix(in srgb, var(--vt-color-warning) 10%, var(--vt-bg)); }
.pending-change-copy { display: grid; min-width: 0; gap: 4px; }
.pending-change-copy strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pending-change-copy small { color: var(--vt-fg-muted); }
.pending-change-copy :deep(.n-select) { max-width: 280px; margin-top: 3px; }
.pending-change-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 5px; }
@keyframes shimmer { to { background-position: -140% 0; } }
@media (max-width: 920px) { .file-search { width: 180px; } :deep(.document-inspector) { flex-basis: 360px; } }
@media (max-width: 680px) {
  .file-toolbar { min-height: 46px; height: auto; flex-wrap: wrap; padding-block: 7px; }
  .file-search { width: min(100%, 240px); flex: 1 1 180px; }
  .selection-count { margin-left: 0; }
  .file-query-builder { grid-template-columns: 1fr 1fr; }
  .file-body { flex-direction: column; overflow: auto; }
  .file-list-pane { min-height: 260px; }
  .pending-change-card { grid-template-columns: 32px minmax(0, 1fr); }
  .pending-change-actions { grid-column: 1 / -1; justify-content: flex-start; }
}
</style>
