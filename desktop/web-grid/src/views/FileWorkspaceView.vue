<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { NButton, NIcon, NInput } from "naive-ui";
import { FilePlus2, Files, RefreshCw, Search } from "lucide-vue-next";
import DocumentList from "@/components/files/DocumentList.vue";
import DocumentContextMenu from "@/components/files/DocumentContextMenu.vue";
import DocumentInspector from "@/components/files/DocumentInspector.vue";
import { createDocumentWorkspaceService, type DocumentWorkspaceIntent, type DocumentWorkspaceScope } from "@/services/documentWorkspaceService";
import { useDocumentWorkspaceStore, type DocumentCapability, type DocumentEntry } from "@/stores/documentWorkspaceStore";
import { t } from "@/i18n";

const props = withDefaults(defineProps<{ scope?: DocumentWorkspaceScope }>(), {
  scope: () => ({ kind: "global" as const }),
});
const emit = defineEmits<{ intent: [intent: DocumentWorkspaceIntent] }>();
const store = useDocumentWorkspaceStore();
const service = createDocumentWorkspaceService((intent) => emit("intent", intent));
const menu = ref<{ entry: DocumentEntry; x: number; y: number } | null>(null);
const dropDepth = ref(0);
const dropActive = ref(false);
const dropFeedback = ref(false);
let dropFeedbackTimer: ReturnType<typeof setTimeout> | null = null;
const selectedCount = computed(() => store.selectedHandles.length);
const currentRevisions = computed(() => store.primaryHandle ? store.revisions[store.primaryHandle] ?? [] : []);

function requestList(): void {
  store.setAuthorityFilter("workspace");
  store.beginLoad();
  service.list(props.scope, "workspace");
}

function select(index: number, options: { toggle: boolean; range: boolean }): void {
  store.selectAt(index, options);
  store.showInspector("preview");
}

function history(entry: DocumentEntry): void {
  store.beginHistory(entry.entryHandle);
  service.history(entry.entryHandle);
}

function onAction(action: DocumentCapability, entry: DocumentEntry): void {
  menu.value = null;
  if (action === "history") history(entry);
  else service[action](entry.entryHandle);
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
});
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
      <NButton quaternary size="small" :aria-label="t('files.refresh')" @click="requestList"><template #icon><NIcon :size="16"><RefreshCw /></NIcon></template></NButton>
      <NButton type="primary" size="small" data-testid="document-import" @click="service.importFiles(scope)"><template #icon><NIcon :size="16"><FilePlus2 /></NIcon></template>{{ t("files.import") }}</NButton>
    </header>

    <div v-if="store.phase === 'failed' && store.entries.length" class="file-error" role="alert">
      <span>{{ store.lastError }}</span><NButton text size="tiny" @click="requestList">{{ t("files.retry") }}</NButton>
    </div>
    <div v-if="dropFeedback" class="drop-feedback" role="status" data-testid="drop-feedback">
      {{ t("files.drop.forwarded") }}
    </div>

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
      </main>
      <DocumentInspector
        :entry="store.primaryEntry"
        :active-tab="store.inspectorTab"
        :revisions="currentRevisions"
        :history-loading="store.historyLoadingFor === store.primaryHandle"
        @tab="store.showInspector"
        @preview="service.preview($event.entryHandle)"
        @history="history"
        @relink="service.relink($event.entryHandle)"
      />
    </div>

    <DocumentContextMenu v-if="menu" :entry="menu.entry" :x="menu.x" :y="menu.y" @action="onAction" @close="menu = null" />
  </section>
</template>

<style scoped>
.file-workspace { display: flex; flex-direction: column; height: 100%; min-width: 0; background: var(--vt-bg); }
.file-toolbar { display: flex; flex: 0 0 46px; align-items: center; gap: 8px; padding: 0 10px; border-bottom: 1px solid var(--vt-border); background: var(--vt-bg); }
.file-search { width: min(280px, 30vw); margin-left: 4px; }
.selection-count { margin-left: auto; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.file-body { position: relative; display: flex; flex: 1; min-height: 0; }
.drop-feedback { min-height: 32px; padding: 6px 12px; color: var(--vt-fg-accent); font-size: var(--vt-font-caption); border-bottom: 1px solid var(--vt-color-primary-100); background: var(--vt-color-primary-50); }
.external-drop-zone { position: absolute; z-index: 80; inset: 12px; display: flex; flex-direction: column; align-items: center; justify-content: center; color: var(--vt-fg-accent); border: 2px dashed var(--vt-color-primary-200); border-radius: 12px; background: color-mix(in srgb, var(--vt-color-primary-50) 92%, var(--vt-bg)); box-shadow: inset 0 0 0 1px rgba(255,255,255,.7); pointer-events: none; }
.external-drop-zone span { display: grid; place-items: center; width: 48px; height: 48px; margin-bottom: 10px; border-radius: 50%; background: var(--vt-bg); box-shadow: var(--vt-shadow-1); }
.external-drop-zone strong { font-size: var(--vt-font-label); font-weight: 600; }
.external-drop-zone p { margin: 4px 0 0; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.file-error { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 34px; padding: 5px 12px; color: var(--vt-color-danger-600); border-bottom: 1px solid color-mix(in srgb, var(--vt-color-danger-500) 28%, var(--vt-border)); background: color-mix(in srgb, var(--vt-color-danger-500) 7%, var(--vt-bg)); font-size: var(--vt-font-caption); }
.file-list-pane { flex: 1; min-width: 0; overflow: auto; }
.file-empty { display: flex; height: 100%; min-height: 280px; flex-direction: column; align-items: center; justify-content: center; padding: 32px; color: var(--vt-fg-muted); text-align: center; }
.file-empty > span { display: grid; place-items: center; width: 44px; height: 44px; margin-bottom: 12px; color: var(--vt-color-primary-500); border-radius: var(--vt-radius-lg); background: var(--vt-color-primary-50); }
:root.dark .file-empty > span { background: rgba(91, 139, 255, 0.14); }
.file-empty strong { color: var(--vt-fg-secondary); font-weight: 550; }
.file-empty p { max-width: 380px; margin: 4px 0 14px; font-size: var(--vt-font-caption); }
.file-skeleton { padding: 33px 12px 0; }
.file-skeleton i { display: block; height: 40px; border-bottom: 1px solid var(--vt-border); background: linear-gradient(90deg, transparent 0%, var(--vt-bg-subtle) 30%, var(--vt-bg-sunken) 50%, var(--vt-bg-subtle) 70%, transparent 100%); background-size: 240% 100%; animation: shimmer 1.4s infinite; }
@keyframes shimmer { to { background-position: -140% 0; } }
@media (max-width: 920px) { .file-search { width: 180px; } :deep(.document-inspector) { flex-basis: 360px; } }
</style>
