<script setup lang="ts">
import { computed, onScopeDispose, ref, watch } from "vue";
import { NAlert, NButton, NInput, NPopconfirm, NSelect } from "naive-ui";
import { useHostBridge } from "@/services/bridgeContext";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useViewQueryStore } from "@/stores/viewQueryStore";
import type { ExportFormat, ExportResult, HostShortcut, HostExportParameters } from "@/contracts";
import { t } from "@/i18n";

const bridge = useHostBridge();
const session = useWorkspaceSessionStore();
const workspace = useWorkspaceStore();
const query = useViewQueryStore();
const ready = computed(() => session.hasOpenWorkspace && !session.isTransitioning);
const shortcuts = ref<readonly HostShortcut[]>([]);
const selectedId = ref("");
const label = ref("");
const target = ref<HostShortcut["target"]>("built-in-command");
const url = ref("");
const format = ref<ExportFormat>("csv");
const busy = ref(false);
const error = ref("");
const notice = ref("");
const hasExport = ref(false);
let generation = 0;
onScopeDispose(() => { generation++; });
const selected = computed(() => shortcuts.value.find(item => item.shortcutId === selectedId.value));
const canExport = computed(() => ready.value && !!workspace.currentTable && hasExport.value);
const targetOptions = computed(() => [
  { label: t("commands.exportTarget"), value: "built-in-command" },
  { label: t("commands.urlTarget"), value: "url" },
]);
function clearDraft() { selectedId.value = ""; label.value = ""; target.value = "built-in-command"; url.value = ""; }
function select(entry: HostShortcut) {
  selectedId.value = entry.shortcutId; label.value = entry.label; target.value = entry.target; url.value = entry.url ?? "";
  error.value = ""; notice.value = "";
}
function exportParameters(): HostExportParameters {
  if (!canExport.value || !workspace.currentTable) throw new Error(t("commands.selectTable"));
  // Export streams matching rows; grid grouping and group-page windows are presentation only.
  const { keyword, filters, sorts } = query.toQuery();
  return { collection: workspace.currentTable, query: {
    ...(keyword ? { keyword } : {}), filters, sorts, offset: 0, limit: 500,
  }, format: format.value };
}
function exported(result: ExportResult) {
  notice.value = t("commands.exported", { count: result.rowsWritten, name: result.outputDisplayName });
}
async function act(action: "reload" | "save" | "delete" | "launch" | "run") {
  if (!ready.value || busy.value) return;
  const ticket = ++generation;
  busy.value = true; error.value = ""; notice.value = "";
  try {
    if (action === "run") {
      const result = await bridge.request("command.run", { commandId: "export.query", params: exportParameters() });
      if (ticket === generation) {
        if (!result.success) throw new Error(result.error ?? t("commands.failed"));
        exported(result.output);
      }
      return;
    }
    if (action === "launch") {
      const entry = selected.value;
      if (!entry) return;
      const result = await bridge.request("shortcut.launch", {
        shortcutId: entry.shortcutId,
        ...(entry.target === "built-in-command" ? { params: exportParameters() } : {}),
      });
      if (ticket === generation) {
        if (result.output) exported(result.output);
        else notice.value = t(result.launched ? "commands.opened" : "commands.cancelled");
      }
      return;
    }
    if (action === "save") {
      const saved = await bridge.request("shortcut.save", { shortcut: {
        shortcutId: selectedId.value || crypto.randomUUID(), label: label.value.trim(), target: target.value,
        commandId: target.value === "built-in-command" ? "export.query" : null,
        url: target.value === "url" ? url.value.trim() : null,
      } });
      if (ticket !== generation) return;
      select(saved); notice.value = t("commands.saved");
    }
    if (action === "delete" && selected.value) {
      await bridge.request("shortcut.delete", { shortcutId: selected.value.shortcutId });
      if (ticket !== generation) return;
      clearDraft(); notice.value = t("commands.deleted");
    }
    if (ticket !== generation) return;
    const list = await bridge.request("shortcut.list", {});
    if (ticket !== generation) return;
    shortcuts.value = list.shortcuts;
    if (action === "reload") {
      const catalog = await bridge.request("command.list", {});
      if (ticket === generation) hasExport.value = catalog.commands.some(item => item.commandId === "export.query");
    }
  } catch (failure) {
    if (ticket === generation) error.value = failure instanceof Error ? failure.message : String(failure);
  } finally { if (ticket === generation) busy.value = false; }
}
watch([ready, () => session.activeWorkspaceId, () => session.sessionEpoch, () => workspace.currentTable], () => {
  generation++; busy.value = false; shortcuts.value = []; error.value = ""; notice.value = ""; hasExport.value = false; clearDraft();
  const ticket = generation;
  queueMicrotask(() => { if (ticket === generation && ready.value) void act("reload"); });
}, { immediate: true, flush: "sync" });
</script>

<template>
  <section class="commands-panel" data-testid="commands-panel">
    <header><h3>{{ t('commands.title') }}</h3><NButton size="small" data-testid="shortcut-new" :disabled="busy || !ready" @click="clearDraft">{{ t('commands.new') }}</NButton></header>
    <p>{{ t('commands.description') }}</p>
    <NAlert v-if="!ready" type="info">{{ t('commands.openWorkspace') }}</NAlert>
    <div class="command-run">
      <NSelect v-model:value="format" :options="[{label:'CSV',value:'csv'},{label:'Excel',value:'xlsx'}]" :disabled="busy" :aria-label="t('commands.format')" />
      <NButton :disabled="busy || !canExport" data-testid="command-run" @click="act('run')">{{ t('commands.export') }}</NButton>
    </div>
    <NAlert v-if="error" type="error" data-testid="commands-error">{{ error }}</NAlert>
    <p v-if="notice" role="status" data-testid="commands-notice">{{ notice }}</p>
    <div class="shortcut-editor">
      <nav :aria-label="t('commands.savedList')">
        <p v-if="!shortcuts.length">{{ t('commands.empty') }}</p>
        <button v-for="entry in shortcuts" :key="entry.shortcutId" type="button" :class="{ selected: selectedId === entry.shortcutId }" :disabled="busy" :aria-pressed="selectedId === entry.shortcutId" data-testid="shortcut-row" :data-shortcut-id="entry.shortcutId" @click="select(entry)">
          <strong>{{ entry.label }}</strong><small>{{ entry.target === 'url' ? entry.url : t('commands.exportTarget') }}</small>
        </button>
      </nav>
      <div class="shortcut-form">
        <NInput v-model:value="label" :maxlength="128" :disabled="busy || !ready" :placeholder="t('commands.name')" :input-props="{ 'aria-label': t('commands.name') }" data-testid="shortcut-label" />
        <NSelect v-model:value="target" :options="targetOptions" :disabled="busy || !ready" :aria-label="t('commands.target')" data-testid="shortcut-target" />
        <NInput v-if="target === 'url'" v-model:value="url" :maxlength="2048" :disabled="busy" placeholder="https://" :input-props="{ 'aria-label': t('commands.url') }" data-testid="shortcut-url" />
        <p v-else>{{ t('commands.currentQuery') }}</p>
        <div class="shortcut-actions">
          <NButton type="primary" :disabled="busy || !ready || !label.trim() || (target === 'url' && !url.trim())" data-testid="shortcut-save" @click="act('save')">{{ t('commands.save') }}</NButton>
          <NButton :disabled="busy || !selected || (selected.target === 'built-in-command' && !canExport)" data-testid="shortcut-launch" @click="act('launch')">{{ t('commands.launch') }}</NButton>
          <NPopconfirm @positive-click="act('delete')"><template #trigger><NButton :disabled="busy || !selected" data-testid="shortcut-delete">{{ t('commands.delete') }}</NButton></template>{{ t('commands.deleteConfirm') }}</NPopconfirm>
        </div>
      </div>
    </div>
  </section>
</template>
<style scoped>
.commands-panel { padding-bottom: var(--vt-space-2); }
header, .command-run, .shortcut-actions { display: flex; align-items: center; gap: 8px; }
header { justify-content: space-between; } h3 { margin: 0; }
p, small { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); overflow-wrap: anywhere; }
.command-run { margin: 12px 0; } .command-run :first-child { max-width: 130px; }
.shortcut-editor { display: grid; grid-template-columns: minmax(140px, 1fr) minmax(220px, 1.6fr); gap: 16px; margin-top: 16px; }
nav { border-right: 1px solid var(--vt-border); padding-right: 12px; max-height: 240px; overflow: auto; }
nav button { display: grid; gap: 4px; width: 100%; text-align: left; background: transparent; border: 1px solid transparent; border-radius: 4px; padding: 8px; color: var(--vt-fg); cursor: pointer; }
nav button.selected { border-color: var(--vt-border); background: var(--vt-bg-subtle); }
.shortcut-form { display: grid; gap: 10px; align-content: start; }
.shortcut-actions { flex-wrap: wrap; }
@media (max-width: 500px) { .shortcut-editor { grid-template-columns: 1fr; } nav { border-right: 0; max-height: 120px; } }
</style>
