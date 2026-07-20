<script setup lang="ts">
import { NButton, NIcon, NSpin } from "naive-ui";
import { AlertTriangle, Eye, FileClock, FileQuestion, History } from "lucide-vue-next";
import type { DocumentEntry, DocumentRevision, InspectorTab } from "@/stores/documentWorkspaceStore";
import { t } from "@/i18n";

defineProps<{
  entry: DocumentEntry | null;
  activeTab: InspectorTab;
  revisions: readonly DocumentRevision[];
  historyLoading: boolean;
}>();

const emit = defineEmits<{
  tab: [tab: InspectorTab];
  preview: [entry: DocumentEntry];
  history: [entry: DocumentEntry];
  relink: [entry: DocumentEntry];
}>();

function unavailableTitle(entry: DocumentEntry): string {
  return t(`files.${entry.availability}.title`);
}

function unavailableHint(entry: DocumentEntry): string {
  return t(`files.${entry.availability}.hint`);
}
</script>

<template>
  <aside class="document-inspector" :aria-label="t('files.inspector')">
    <div class="inspector-tabs" role="tablist">
      <button :class="{ active: activeTab === 'preview' }" role="tab" :aria-selected="activeTab === 'preview'" @click="emit('tab', 'preview')">
        <NIcon :size="14"><Eye /></NIcon>{{ t("files.action.preview") }}
      </button>
      <button :class="{ active: activeTab === 'history' }" role="tab" :aria-selected="activeTab === 'history'" @click="entry && emit('history', entry)">
        <NIcon :size="14"><History /></NIcon>{{ t("files.action.history") }}
      </button>
    </div>

    <div v-if="!entry" class="inspector-empty">
      <span><NIcon :size="23"><FileQuestion /></NIcon></span>
      <strong>{{ t("files.inspector.empty") }}</strong>
      <p>{{ t("files.inspector.emptyHint") }}</p>
    </div>

    <template v-else>
      <header class="inspector-heading">
        <strong :title="entry.displayName">{{ entry.displayName }}</strong>
        <small>{{ entry.authority === "workspace" ? t("files.authority.workspace") : t("files.authority.cloud") }}</small>
      </header>

      <div v-if="!['available', 'remote'].includes(entry.availability)" class="missing-card">
        <NIcon :size="17"><AlertTriangle /></NIcon>
        <div><strong>{{ unavailableTitle(entry) }}</strong><p>{{ unavailableHint(entry) }}</p></div>
        <NButton v-if="entry.capabilities.includes('relink')" size="tiny" @click="emit('relink', entry)">{{ t("files.action.relink") }}</NButton>
      </div>

      <section v-else-if="activeTab === 'preview'" class="preview-stage">
        <span><NIcon :size="25"><Eye /></NIcon></span>
        <strong>{{ entry.capabilities.includes('preview') ? t("files.preview.waiting") : t("files.preview.unavailable") }}</strong>
        <p>{{ entry.capabilities.includes('preview') ? t("files.preview.hostHint") : t("files.preview.unavailableHint") }}</p>
        <NButton v-if="entry.capabilities.includes('preview')" size="small" @click="emit('preview', entry)">{{ t("files.preview.retry") }}</NButton>
      </section>

      <section v-else class="history-panel">
        <div v-if="historyLoading" class="history-loading"><NSpin size="small" />{{ t("files.history.loading") }}</div>
        <div v-else-if="revisions.length === 0" class="history-empty">
          <NIcon :size="23"><FileClock /></NIcon>
          <strong>{{ t("files.history.empty") }}</strong>
          <p>{{ t("files.history.emptyHint") }}</p>
        </div>
        <ol v-else>
          <li v-for="revision in revisions" :key="revision.revisionHandle">
            <i></i>
            <div><strong>{{ revision.label }}</strong><small>{{ revision.createdAt }}</small></div>
            <small>{{ revision.author }}</small>
          </li>
        </ol>
      </section>
    </template>
  </aside>
</template>

<style scoped>
.document-inspector { display: flex; flex: 0 0 392px; flex-direction: column; min-width: 360px; max-width: 420px; border-left: 1px solid var(--vt-border); background: var(--vt-bg); }
.inspector-tabs { display: flex; flex: 0 0 38px; gap: 2px; padding: 5px 8px 0; border-bottom: 1px solid var(--vt-border); }
.inspector-tabs button { display: flex; align-items: center; gap: 6px; padding: 0 10px; color: var(--vt-fg-muted); border: 0; border-bottom: 2px solid transparent; background: transparent; cursor: pointer; }
.inspector-tabs button.active { color: var(--vt-color-primary-500); border-bottom-color: var(--vt-color-primary-500); }
.inspector-empty, .preview-stage, .history-empty { display: flex; flex: 1; flex-direction: column; align-items: center; justify-content: center; padding: 28px; color: var(--vt-fg-muted); text-align: center; }
.inspector-empty > span, .preview-stage > span { display: grid; place-items: center; width: 46px; height: 46px; margin-bottom: 12px; color: var(--vt-color-primary-500); border-radius: 50%; background: var(--vt-color-primary-50); }
:root.dark .inspector-empty > span, :root.dark .preview-stage > span { background: rgba(91, 139, 255, 0.14); }
.inspector-empty strong, .preview-stage strong, .history-empty strong { color: var(--vt-fg-secondary); font-weight: 500; }
.inspector-empty p, .preview-stage p, .history-empty p { max-width: 260px; margin: 4px 0 14px; font-size: var(--vt-font-caption); }
.inspector-heading { display: flex; flex-direction: column; padding: 13px 16px; border-bottom: 1px solid var(--vt-border); }
.inspector-heading strong { overflow: hidden; font-weight: 550; text-overflow: ellipsis; white-space: nowrap; }
.inspector-heading small { color: var(--vt-fg-muted); }
.missing-card { display: grid; grid-template-columns: 22px 1fr auto; align-items: start; gap: 8px; margin: 14px; padding: 12px; color: #9a6700; border: 1px solid color-mix(in srgb, var(--vt-color-warning) 34%, var(--vt-border)); border-radius: var(--vt-radius-lg); background: color-mix(in srgb, var(--vt-color-warning) 8%, var(--vt-bg)); }
.missing-card strong { font-weight: 550; }
.missing-card p { margin: 2px 0 0; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.history-panel { flex: 1; min-height: 0; overflow: auto; }
.history-loading { display: flex; align-items: center; justify-content: center; gap: 9px; min-height: 120px; color: var(--vt-fg-muted); }
.history-empty { min-height: 240px; }
.history-panel ol { margin: 0; padding: 16px; list-style: none; }
.history-panel li { display: grid; grid-template-columns: 14px 1fr auto; gap: 8px; min-height: 52px; }
.history-panel li i { position: relative; width: 7px; height: 7px; margin-top: 6px; border: 2px solid var(--vt-color-primary-500); border-radius: 50%; }
.history-panel li i::after { position: absolute; top: 7px; left: 1px; width: 1px; height: 39px; background: var(--vt-border); content: ""; }
.history-panel li:last-child i::after { display: none; }
.history-panel li div { display: flex; flex-direction: column; }
.history-panel li strong { font-size: var(--vt-font-body); font-weight: 500; }
.history-panel li small { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
</style>
