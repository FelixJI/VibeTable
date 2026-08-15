<script setup lang="ts">
import { NButton, NIcon } from "naive-ui";
import { AlertTriangle, Eye, FileQuestion, History } from "lucide-vue-next";
import type { DocumentEntry, InspectorTab } from "@/stores/documentWorkspaceStore";
import type { FileRevisionV2 } from "@/contracts/workspaceV2";
import type { FileRevisionTreeProjection } from "@/stores/workspaceProtectionStore";
import FileRevisionTree from "@/components/files/FileRevisionTree.vue";
import { t } from "@/i18n";
import type { DocumentDiffCompletedPayload } from "@/contracts";
import type { DocumentDiffPhase } from "@/stores/documentWorkspaceStore";

defineProps<{
  entry: DocumentEntry | null;
  activeTab: InspectorTab;
  busy: boolean;
  requestedRevisionId?: string | null;
  revisionTree?: FileRevisionTreeProjection | null;
  diffPhase?: DocumentDiffPhase;
  diffResult?: DocumentDiffCompletedPayload | null;
}>();

const emit = defineEmits<{
  tab: [tab: InspectorTab];
  preview: [entry: DocumentEntry];
  history: [entry: DocumentEntry];
  relink: [entry: DocumentEntry];
  restoreFileRevision: [entry: DocumentEntry, revision: FileRevisionV2];
  upgradeFileRevision: [entry: DocumentEntry, revision: FileRevisionV2];
  activateFileRevision: [entry: DocumentEntry, revision: FileRevisionV2];
  compareFileRevision: [entry: DocumentEntry, revision: FileRevisionV2];
  cancelFileDiff: [];
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
      <button
        :class="{ active: activeTab === 'preview' }"
        role="tab"
        :aria-selected="activeTab === 'preview'"
        @click="emit('tab', 'preview')"
      >
        <NIcon :size="14"><Eye /></NIcon>{{ t("files.action.preview") }}
      </button>
      <button
        :class="{ active: activeTab === 'history' }"
        role="tab"
        :aria-selected="activeTab === 'history'"
        @click="entry && emit('history', entry)"
      >
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
        <small>{{ entry.versionLabel ?? t("files.authority.workspace") }}</small>
      </header>

      <div v-if="!['available', 'remote'].includes(entry.availability)" class="missing-card">
        <NIcon :size="17"><AlertTriangle /></NIcon>
        <div><strong>{{ unavailableTitle(entry) }}</strong><p>{{ unavailableHint(entry) }}</p></div>
        <NButton
          v-if="entry.capabilities.includes('relink')"
          size="tiny"
          @click="emit('relink', entry)"
        >
          {{ t("files.action.relink") }}
        </NButton>
      </div>

      <section v-else-if="activeTab === 'preview'" class="preview-stage">
        <span><NIcon :size="25"><Eye /></NIcon></span>
        <strong>
          {{ entry.capabilities.includes('preview')
            ? t("files.preview.waiting")
            : t("files.preview.unavailable") }}
        </strong>
        <p>
          {{ entry.capabilities.includes('preview')
            ? t("files.preview.hostHint")
            : t("files.preview.unavailableHint") }}
        </p>
        <NButton
          v-if="entry.capabilities.includes('preview')"
          size="small"
          @click="emit('preview', entry)"
        >
          {{ t("files.preview.retry") }}
        </NButton>
      </section>

      <section v-else class="history-panel">
        <FileRevisionTree
          :tree="revisionTree ?? null"
          :busy="busy"
          :requested-revision-id="requestedRevisionId"
          :can-compare="entry.capabilities.includes('diff')"
          :diff-phase="diffPhase"
          :diff-result="diffResult"
          @restore="emit('restoreFileRevision', entry, $event)"
          @upgrade="emit('upgradeFileRevision', entry, $event)"
          @activate="emit('activateFileRevision', entry, $event)"
          @compare="emit('compareFileRevision', entry, $event)"
          @cancel-compare="emit('cancelFileDiff')"
        />
      </section>
    </template>
  </aside>
</template>

<style scoped>
.document-inspector { display: flex; flex: 0 0 420px; flex-direction: column; min-width: 360px; max-width: 460px; border-left: 1px solid var(--vt-border); background: var(--vt-bg); }
.inspector-tabs { display: flex; flex: 0 0 38px; gap: 2px; padding: 5px 8px 0; overflow-x: auto; border-bottom: 1px solid var(--vt-border); }
.inspector-tabs button { display: flex; align-items: center; gap: 6px; padding: 0 10px; color: var(--vt-fg-muted); border: 0; border-bottom: 2px solid transparent; background: transparent; cursor: pointer; white-space: nowrap; }
.inspector-tabs button.active { color: var(--vt-color-primary-500); border-bottom-color: var(--vt-color-primary-500); }
.inspector-empty, .preview-stage { display: flex; flex: 1; flex-direction: column; align-items: center; justify-content: center; padding: 28px; color: var(--vt-fg-muted); text-align: center; }
.inspector-empty > span, .preview-stage > span { display: grid; place-items: center; width: 46px; height: 46px; margin-bottom: 12px; color: var(--vt-color-primary-500); border-radius: 50%; background: var(--vt-color-primary-50); }
.inspector-empty strong, .preview-stage strong { color: var(--vt-fg-secondary); font-weight: 500; }
.inspector-empty p, .preview-stage p { max-width: 280px; margin: 4px 0 14px; font-size: var(--vt-font-caption); }
.inspector-heading { display: flex; flex-direction: column; padding: 13px 16px; border-bottom: 1px solid var(--vt-border); }
.inspector-heading strong { overflow: hidden; font-weight: 550; text-overflow: ellipsis; white-space: nowrap; }
.inspector-heading small { color: var(--vt-fg-muted); }
.missing-card { display: grid; grid-template-columns: 22px 1fr auto; align-items: start; gap: 8px; margin: 14px; padding: 12px; color: #9a6700; border: 1px solid color-mix(in srgb, var(--vt-color-warning) 34%, var(--vt-border)); border-radius: var(--vt-radius-lg); background: color-mix(in srgb, var(--vt-color-warning) 8%, var(--vt-bg)); }
.missing-card p { margin: 2px 0 0; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.history-panel { flex: 1; min-height: 0; overflow: auto; }
@media (max-width: 680px) {
  .document-inspector { min-width: 0; flex-basis: 100%; max-width: none; border-top: 1px solid var(--vt-border); border-left: 0; }
}
</style>
