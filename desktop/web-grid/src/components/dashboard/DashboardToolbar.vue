<script setup lang="ts">
import { NButton, NButtonGroup, NIcon, NSelect, NTooltip } from "naive-ui";
import { Check, Copy, Download, Edit3, Plus, Printer, RefreshCw, RotateCcw, Settings2, Trash2 } from "@lucide/vue";
import { t } from "@/i18n";

defineProps<{
  name: string;
  editing: boolean;
  dirty: boolean;
  saving: boolean;
  refreshInterval: number;
}>();
const emit = defineEmits<{
  refresh: [];
  edit: [];
  copy: [];
  delete: [];
  save: [];
  discard: [];
  addPanel: [];
  configure: [];
  exportPng: [];
  print: [];
  refreshInterval: [value: 0 | 30 | 60 | 300 | 900];
}>();
const refreshOptions = [
  { label: t("dashboard.refresh.manual"), value: 0 },
  { label: "30s", value: 30 },
  { label: "1m", value: 60 },
  { label: "5m", value: 300 },
  { label: "15m", value: 900 },
];
</script>

<template>
  <header class="dashboard-toolbar">
    <div class="toolbar-title"><strong>{{ name }}</strong><span v-if="dirty">{{ t("dashboard.state.unsaved") }}</span></div>
    <div class="toolbar-actions">
      <NSelect :value="refreshInterval" :options="refreshOptions" size="tiny" class="refresh-select" :disabled="!editing" :aria-label="t('dashboard.refresh.label')" @update:value="emit('refreshInterval', $event)" />
      <NTooltip><template #trigger><NButton quaternary size="small" data-testid="dashboard-refresh" :aria-label="t('dashboard.action.refresh')" @click="emit('refresh')"><NIcon><RefreshCw /></NIcon></NButton></template>{{ t("dashboard.action.refresh") }}</NTooltip>
      <NButtonGroup v-if="editing">
        <NButton size="small" data-testid="dashboard-add-panel" @click="emit('addPanel')"><template #icon><NIcon><Plus /></NIcon></template>{{ t("dashboard.action.addPanel") }}</NButton>
        <NButton size="small" data-testid="dashboard-configure" @click="emit('configure')"><template #icon><NIcon><Settings2 /></NIcon></template>{{ t("dashboard.action.configure") }}</NButton>
        <NButton size="small" @click="emit('discard')"><template #icon><NIcon><RotateCcw /></NIcon></template>{{ t("dashboard.action.discard") }}</NButton>
        <NButton size="small" type="primary" data-testid="dashboard-save" :disabled="!dirty" :loading="saving" @click="emit('save')"><template #icon><NIcon><Check /></NIcon></template>{{ t("dashboard.action.save") }}</NButton>
      </NButtonGroup>
      <NButton v-else size="small" data-testid="dashboard-edit" @click="emit('edit')"><template #icon><NIcon><Edit3 /></NIcon></template>{{ t("dashboard.action.edit") }}</NButton>
      <NTooltip v-if="!editing"><template #trigger><NButton quaternary size="small" type="error" :aria-label="t('dashboard.action.delete')" @click="emit('delete')"><NIcon><Trash2 /></NIcon></NButton></template>{{ t("dashboard.action.delete") }}</NTooltip>
      <NTooltip><template #trigger><NButton quaternary size="small" :aria-label="t('dashboard.action.copy')" @click="emit('copy')"><NIcon><Copy /></NIcon></NButton></template>{{ t("dashboard.action.copy") }}</NTooltip>
      <NTooltip><template #trigger><NButton quaternary size="small" :aria-label="t('dashboard.action.exportPng')" @click="emit('exportPng')"><NIcon><Download /></NIcon></NButton></template>{{ t("dashboard.action.exportPng") }}</NTooltip>
      <NTooltip><template #trigger><NButton quaternary size="small" :aria-label="t('dashboard.action.print')" @click="emit('print')"><NIcon><Printer /></NIcon></NButton></template>{{ t("dashboard.action.print") }}</NTooltip>
    </div>
  </header>
</template>

<style scoped>
.dashboard-toolbar { display:flex; align-items:center; min-height:46px; padding:0 12px 0 16px; border-bottom:1px solid var(--vt-border); background:var(--vt-bg); }
.toolbar-title { display:flex; min-width:0; flex:1; align-items:center; gap:9px; }
.toolbar-title strong { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font-size:14px; }
.toolbar-title span { padding:2px 6px; border-radius:10px; color:var(--vt-color-warning-700); background:var(--vt-color-warning-50); font-size:10px; }
.toolbar-actions { display:flex; align-items:center; gap:4px; }
.refresh-select { width:94px; }
</style>
