<script setup lang="ts">
import { ref, watch } from "vue";
import { NButton, NIcon, NInput, NModal, NPopconfirm, NSpin, NTag } from "naive-ui";
import { AlertTriangle, Boxes, Eye, FileClock, FileQuestion, GitBranch, History, Save, Upload } from "lucide-vue-next";
import type { DocumentEntry, DocumentRevision, DocumentScheme, InspectorTab } from "@/stores/documentWorkspaceStore";
import { t } from "@/i18n";

const props = defineProps<{
  entry: DocumentEntry | null;
  activeTab: InspectorTab;
  revisions: readonly DocumentRevision[];
  schemes: readonly DocumentScheme[];
  historyLoading: boolean;
  schemesLoading: boolean;
  busy: boolean;
}>();

const emit = defineEmits<{
  tab: [tab: InspectorTab];
  preview: [entry: DocumentEntry];
  history: [entry: DocumentEntry];
  schemes: [entry: DocumentEntry];
  relink: [entry: DocumentEntry];
  commit: [entry: DocumentEntry, note?: string, schemeHandle?: string | null];
  promote: [entry: DocumentEntry, versionLabel: string, note?: string, schemeHandle?: string | null];
  previewRevision: [entry: DocumentEntry, revision: DocumentRevision];
  restoreRevision: [entry: DocumentEntry, revision: DocumentRevision];
  createScheme: [entry: DocumentEntry, name: string, baseRevisionHandle?: string | null];
  renameScheme: [entry: DocumentEntry, scheme: DocumentScheme, name: string];
  archiveScheme: [entry: DocumentEntry, scheme: DocumentScheme];
}>();

const dialog = ref<"commit" | "promote" | "scheme" | "rename" | null>(null);
const input = ref("");
const note = ref("");
const renameTarget = ref<DocumentScheme | null>(null);

watch(() => props.entry?.entryHandle, () => {
  dialog.value = null;
  input.value = "";
  note.value = "";
  renameTarget.value = null;
});

function activeScheme(): DocumentScheme | null {
  return props.schemes.find((scheme) => scheme.active) ?? null;
}

function openDialog(kind: typeof dialog.value, scheme?: DocumentScheme): void {
  dialog.value = kind;
  renameTarget.value = scheme ?? null;
  input.value = scheme?.name ?? "";
  note.value = "";
}

function confirmDialog(): void {
  const entry = props.entry;
  if (!entry) return;
  const value = input.value.trim();
  if (dialog.value === "commit") {
    emit("commit", entry, note.value.trim() || undefined, activeScheme()?.schemeHandle);
  } else if (dialog.value === "promote" && value) {
    emit("promote", entry, value, note.value.trim() || undefined, activeScheme()?.schemeHandle);
  } else if (dialog.value === "scheme" && value) {
    emit("createScheme", entry, value, props.revisions[0]?.revisionHandle);
  } else if (dialog.value === "rename" && value && renameTarget.value) {
    emit("renameScheme", entry, renameTarget.value, value);
  } else {
    return;
  }
  dialog.value = null;
}

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
      <button :class="{ active: activeTab === 'schemes' }" role="tab" :aria-selected="activeTab === 'schemes'" @click="entry && emit('schemes', entry)">
        <NIcon :size="14"><GitBranch /></NIcon>{{ t("files.scheme.title") }}
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
        <NButton v-if="entry.capabilities.includes('relink')" size="tiny" @click="emit('relink', entry)">{{ t("files.action.relink") }}</NButton>
      </div>

      <section v-else-if="activeTab === 'preview'" class="preview-stage">
        <span><NIcon :size="25"><Eye /></NIcon></span>
        <strong>{{ entry.capabilities.includes('preview') ? t("files.preview.waiting") : t("files.preview.unavailable") }}</strong>
        <p>{{ entry.capabilities.includes('preview') ? t("files.preview.hostHint") : t("files.preview.unavailableHint") }}</p>
        <NButton v-if="entry.capabilities.includes('preview')" size="small" @click="emit('preview', entry)">{{ t("files.preview.retry") }}</NButton>
      </section>

      <section v-else-if="activeTab === 'history'" class="history-panel">
        <div class="version-actions">
          <NButton size="small" :disabled="busy || !entry.capabilities.includes('commitRevision')" data-testid="document-commit" @click="openDialog('commit')">
            <template #icon><NIcon><Save /></NIcon></template>{{ t("files.revision.commit") }}
          </NButton>
          <NButton size="small" type="primary" secondary :disabled="busy || !entry.capabilities.includes('promoteVersion')" data-testid="document-promote" @click="openDialog('promote')">
            <template #icon><NIcon><Upload /></NIcon></template>{{ t("files.revision.promote") }}
          </NButton>
        </div>
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
            <div class="revision-actions">
              <small>{{ revision.author }}</small>
              <NButton size="tiny" quaternary @click="emit('previewRevision', entry, revision)">{{ t("files.revision.preview") }}</NButton>
              <NPopconfirm
                :positive-text="t('files.revision.restoreConfirm')"
                :negative-text="t('common.cancel')"
                @positive-click="emit('restoreRevision', entry, revision)"
              >
                <template #trigger><NButton size="tiny" quaternary type="warning" :disabled="busy">{{ t("files.revision.restore") }}</NButton></template>
                {{ t("files.revision.restoreWarning", { version: revision.label }) }}
              </NPopconfirm>
            </div>
          </li>
        </ol>
      </section>

      <section v-else class="scheme-panel">
        <div class="scheme-intro">
          <div><strong>{{ t("files.scheme.title") }}</strong><small>{{ t("files.scheme.hint") }}</small></div>
          <NButton size="small" type="primary" secondary :disabled="busy || !entry.capabilities.includes('schemes')" data-testid="document-scheme-create" @click="openDialog('scheme')">
            <template #icon><NIcon><GitBranch /></NIcon></template>{{ t("files.scheme.create") }}
          </NButton>
        </div>
        <div v-if="schemesLoading" class="history-loading"><NSpin size="small" />{{ t("files.scheme.loading") }}</div>
        <div v-else-if="schemes.length === 0" class="history-empty">
          <NIcon :size="23"><Boxes /></NIcon><strong>{{ t("files.scheme.empty") }}</strong><p>{{ t("files.scheme.emptyHint") }}</p>
        </div>
        <article v-for="scheme in schemes" v-else :key="scheme.schemeHandle" class="scheme-card" :class="{ active: scheme.active }">
          <div>
            <span><strong>{{ scheme.name }}</strong><NTag v-if="scheme.active" size="small" type="success">{{ t("files.scheme.active") }}</NTag></span>
            <small>{{ scheme.currentRevisionLabel ?? t("files.scheme.noRevision") }}</small>
          </div>
          <div class="scheme-actions">
            <NButton v-if="!scheme.archived" size="tiny" quaternary :disabled="busy || scheme.active" @click="openDialog('rename', scheme)">{{ t("common.rename") }}</NButton>
            <NPopconfirm :positive-text="t('files.scheme.archive')" :negative-text="t('common.cancel')" @positive-click="emit('archiveScheme', entry, scheme)">
              <template #trigger><NButton v-if="!scheme.archived" size="tiny" quaternary type="warning" :disabled="busy || scheme.active">{{ t("files.scheme.archive") }}</NButton></template>
              {{ t("files.scheme.archiveWarning", { name: scheme.name }) }}
            </NPopconfirm>
          </div>
        </article>
      </section>
    </template>

    <NModal :show="dialog !== null" preset="card" class="document-dialog" :title="dialog ? t(`files.dialog.${dialog}`) : ''" @update:show="show => { if (!show) dialog = null }">
      <NInput v-if="dialog !== 'commit'" v-model:value="input" autofocus :placeholder="dialog === 'promote' ? t('files.revision.versionPlaceholder') : t('files.scheme.namePlaceholder')" @keyup.enter="confirmDialog" />
      <NInput v-if="dialog === 'commit' || dialog === 'promote'" v-model:value="note" type="textarea" :placeholder="t('files.revision.notePlaceholder')" />
      <template #footer><div class="dialog-footer"><NButton @click="dialog = null">{{ t("common.cancel") }}</NButton><NButton type="primary" :disabled="dialog !== 'commit' && !input.trim()" @click="confirmDialog">{{ t("common.confirm") }}</NButton></div></template>
    </NModal>
  </aside>
</template>

<style scoped>
.document-inspector { display: flex; flex: 0 0 420px; flex-direction: column; min-width: 360px; max-width: 460px; border-left: 1px solid var(--vt-border); background: var(--vt-bg); }
.inspector-tabs { display: flex; flex: 0 0 38px; gap: 2px; padding: 5px 8px 0; overflow-x: auto; border-bottom: 1px solid var(--vt-border); }
.inspector-tabs button { display: flex; align-items: center; gap: 6px; padding: 0 10px; color: var(--vt-fg-muted); border: 0; border-bottom: 2px solid transparent; background: transparent; cursor: pointer; white-space: nowrap; }
.inspector-tabs button.active { color: var(--vt-color-primary-500); border-bottom-color: var(--vt-color-primary-500); }
.inspector-empty, .preview-stage, .history-empty { display: flex; flex: 1; flex-direction: column; align-items: center; justify-content: center; padding: 28px; color: var(--vt-fg-muted); text-align: center; }
.inspector-empty > span, .preview-stage > span { display: grid; place-items: center; width: 46px; height: 46px; margin-bottom: 12px; color: var(--vt-color-primary-500); border-radius: 50%; background: var(--vt-color-primary-50); }
.inspector-empty strong, .preview-stage strong, .history-empty strong { color: var(--vt-fg-secondary); font-weight: 500; }
.inspector-empty p, .preview-stage p, .history-empty p { max-width: 280px; margin: 4px 0 14px; font-size: var(--vt-font-caption); }
.inspector-heading { display: flex; flex-direction: column; padding: 13px 16px; border-bottom: 1px solid var(--vt-border); }
.inspector-heading strong { overflow: hidden; font-weight: 550; text-overflow: ellipsis; white-space: nowrap; }
.inspector-heading small { color: var(--vt-fg-muted); }
.missing-card { display: grid; grid-template-columns: 22px 1fr auto; align-items: start; gap: 8px; margin: 14px; padding: 12px; color: #9a6700; border: 1px solid color-mix(in srgb, var(--vt-color-warning) 34%, var(--vt-border)); border-radius: var(--vt-radius-lg); background: color-mix(in srgb, var(--vt-color-warning) 8%, var(--vt-bg)); }
.missing-card p { margin: 2px 0 0; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.history-panel { flex: 1; min-height: 0; overflow: auto; }
.version-actions { display: flex; flex-wrap: wrap; gap: 8px; padding: 12px 16px; border-bottom: 1px solid var(--vt-border); background: var(--vt-bg-subtle); }
.history-loading { display: flex; align-items: center; justify-content: center; gap: 9px; min-height: 120px; color: var(--vt-fg-muted); }
.history-empty { min-height: 240px; }
.history-panel ol { margin: 0; padding: 16px; list-style: none; }
.history-panel li { display: grid; grid-template-columns: 14px minmax(90px, 1fr) auto; gap: 8px; min-height: 62px; }
.history-panel li i { position: relative; width: 7px; height: 7px; margin-top: 6px; border: 2px solid var(--vt-color-primary-500); border-radius: 50%; }
.history-panel li i::after { position: absolute; top: 7px; left: 1px; width: 1px; height: 48px; background: var(--vt-border); content: ""; }
.history-panel li:last-child i::after { display: none; }
.history-panel li div { display: flex; flex-direction: column; }
.history-panel li strong { font-size: var(--vt-font-body); font-weight: 500; }
.history-panel li small { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.revision-actions { align-items: flex-end; }
.scheme-panel { flex: 1; min-height: 0; padding: 12px; overflow: auto; background: var(--vt-bg-subtle); }
.scheme-intro { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
.scheme-intro > div, .scheme-card > div:first-child { display: flex; min-width: 0; flex-direction: column; }
.scheme-intro small, .scheme-card small { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.scheme-card { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 8px; padding: 11px 12px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); background: var(--vt-bg); }
.scheme-card.active { border-color: color-mix(in srgb, var(--vt-color-primary-500) 42%, var(--vt-border)); box-shadow: inset 3px 0 var(--vt-color-primary-500); }
.scheme-card span { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; }
.scheme-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 2px; }
.dialog-footer { display: flex; justify-content: flex-end; gap: 8px; }
:global(.document-dialog) { width: min(440px, calc(100vw - 28px)); }
@media (max-width: 680px) {
  .document-inspector { min-width: 0; flex-basis: 100%; max-width: none; border-top: 1px solid var(--vt-border); border-left: 0; }
  .scheme-card { align-items: flex-start; flex-direction: column; }
}
</style>
