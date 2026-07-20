<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { NButton, NIcon, NInput } from "naive-ui";
import { Cloud, FilePlus2, Files, RefreshCw, Search } from "lucide-vue-next";
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
const selectedCount = computed(() => store.selectedHandles.length);
const currentRevisions = computed(() => store.primaryHandle ? store.revisions[store.primaryHandle] ?? [] : []);

function requestList(): void {
  store.beginLoad();
  service.list(props.scope, store.authorityFilter);
}

function setAuthority(authority: "workspace" | "cloud"): void {
  store.setAuthorityFilter(authority);
  requestList();
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

onMounted(requestList);
</script>

<template>
  <section class="file-workspace" data-testid="file-workspace">
    <header class="file-toolbar">
      <div class="authority-switch" role="tablist" :aria-label="t('files.source')">
        <button :class="{ active: store.authorityFilter === 'workspace' }" role="tab" :aria-selected="store.authorityFilter === 'workspace'" @click="setAuthority('workspace')">
          <NIcon :size="15"><Files /></NIcon>{{ t("files.authority.workspace") }}
        </button>
        <button disabled role="tab" aria-disabled="true" aria-selected="false" :title="t('files.cloud.notReady')">
          <NIcon :size="15"><Cloud /></NIcon>{{ t("files.authority.cloud") }}
        </button>
      </div>
      <NInput :value="store.query" clearable size="small" class="file-search" :placeholder="t('files.search')" @update:value="store.setQuery">
        <template #prefix><NIcon :size="15"><Search /></NIcon></template>
      </NInput>
      <span v-if="selectedCount" class="selection-count">{{ t("files.selected", { count: selectedCount }) }}</span>
      <NButton quaternary size="small" :aria-label="t('files.refresh')" @click="requestList"><template #icon><NIcon :size="16"><RefreshCw /></NIcon></template></NButton>
      <NButton type="primary" size="small" disabled :title="t('files.add.notReady')"><template #icon><NIcon :size="16"><FilePlus2 /></NIcon></template>{{ t("files.add") }}</NButton>
    </header>

    <div v-if="store.phase === 'failed' && store.entries.length" class="file-error" role="alert">
      <span>{{ store.lastError }}</span><NButton text size="tiny" @click="requestList">{{ t("files.retry") }}</NButton>
    </div>

    <div class="file-body">
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
          <NButton v-if="!store.query && store.authorityFilter === 'workspace'" size="small" type="primary" disabled :title="t('files.add.notReady')">{{ t("files.add") }}</NButton>
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
.authority-switch { display: flex; gap: 2px; padding: 3px; border-radius: var(--vt-radius-md); background: var(--vt-bg-sunken); }
.authority-switch button { display: flex; align-items: center; gap: 6px; min-height: 28px; padding: 0 10px; color: var(--vt-fg-muted); border: 0; border-radius: var(--vt-radius-sm); background: transparent; cursor: pointer; }
.authority-switch button.active { color: var(--vt-fg); background: var(--vt-bg); box-shadow: var(--vt-shadow-1); }
.file-search { width: min(280px, 30vw); margin-left: 4px; }
.selection-count { margin-left: auto; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.file-body { display: flex; flex: 1; min-height: 0; }
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
