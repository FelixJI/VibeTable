<script setup lang="ts">
import { computed, ref } from "vue";
import { NAlert, NButton, NEmpty, NIcon, NSpin, useMessage } from "naive-ui";
import { LayoutDashboard, Plus } from "lucide-vue-next";
import DashboardSidebar from "@/components/dashboard/DashboardSidebar.vue";
import DashboardToolbar from "@/components/dashboard/DashboardToolbar.vue";
import DashboardGrid from "@/components/dashboard/DashboardGrid.vue";
import DashboardCreateModal from "@/components/dashboard/DashboardCreateModal.vue";
import DashboardPanelEditor from "@/components/dashboard/DashboardPanelEditor.vue";
import DashboardSettingsDrawer from "@/components/dashboard/DashboardSettingsDrawer.vue";
import DashboardFilterBar from "@/components/dashboard/DashboardFilterBar.vue";
import DashboardDrilldownDrawer from "@/components/dashboard/DashboardDrilldownDrawer.vue";
import type { DashboardPanel, DashboardTemplateId, PanelPosition } from "@/dashboard";
import { useDashboardDraftStore, useDashboardStore, type DashboardManagedConfig } from "@/stores/dashboardStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useProvidedDashboardService } from "@/services/dashboardService";
import { exportDashboardCsv, exportDashboardElementPng, printDashboard } from "@/services/dashboardExportService";
import { t } from "@/i18n";

const store = useDashboardStore();
const draft = useDashboardDraftStore();
const workspace = useWorkspaceStore();
const service = useProvidedDashboardService();
const message = useMessage();
const surface = ref<HTMLElement | null>(null);
const createOpen = ref(false);
const panelEditorOpen = ref(false);
const settingsOpen = ref(false);
const editingPanel = ref<DashboardPanel | null>(null);
const drilldown = ref({
  show: false,
  title: "",
  selection: undefined as unknown,
  rows: [] as readonly Record<string, unknown>[],
  truncated: false,
  loading: false,
  error: null as string | null,
});
let drilldownGeneration = 0;
const activeDashboard = computed(() => draft.editing ? draft.draft : store.current);
const activeConfig = computed(() => draft.editing ? draft.config : store.config);
const filters = computed(() => activeConfig.value.globalFilters);
const collections = computed(() => workspace.collections.map((item) => item.collection));

async function selectDashboard(id: string): Promise<void> {
  if (draft.dirty && !window.confirm(t("dashboard.confirm.discard"))) return;
  draft.stop();
  await service.select(id);
}
function createDashboard(templateId: DashboardTemplateId, name: string): void {
  service.createFromTemplate(templateId, name);
  createOpen.value = false;
}
function copyDashboard(): void {
  if (!store.current) return;
  const skipped = service.copyCurrent(t("dashboard.copy.name", { name: store.current.name }));
  if (skipped > 0) message.warning(t("dashboard.copy.skipped", { count: skipped }));
}
async function deleteDashboard(): Promise<void> {
  if (!store.current || !window.confirm(t("dashboard.confirm.delete", { name: store.current.name }))) return;
  await service.deleteCurrent();
}
async function reloadConflict(): Promise<void> {
  const dashboardId = store.current?.id;
  if (!dashboardId || !window.confirm(t("dashboard.confirm.reloadConflict"))) return;
  draft.stop();
  await service.select(dashboardId);
}
function openNewPanel(): void {
  const count = activeDashboard.value?.panels.length ?? 0;
  if (count >= 100) { message.error(t("dashboard.panelLimit.hard")); return; }
  if (count >= 30) message.warning(t("dashboard.panelLimit.soft", { count }));
  editingPanel.value = null;
  panelEditorOpen.value = true;
}
function openPanel(panel: DashboardPanel): void { editingPanel.value = panel; panelEditorOpen.value = true; }
function submitPanel(panel: DashboardPanel): void {
  if (editingPanel.value) draft.updatePanel(panel.id, panel);
  else draft.addPanel({ ...panel, position: nextPosition(panel.position) });
  panelEditorOpen.value = false;
  void service.previewPanel(panel);
}
function nextPosition(base: PanelPosition): PanelPosition {
  const bottom = Math.max(0, ...(draft.draft?.panels.map((item) => item.position.y + item.position.height) ?? [0]));
  return { ...base, x: 0, y: bottom };
}
function updateLayout(panelId: string, position: PanelPosition): void { draft.updatePanel(panelId, { position }); }
function updateSettings(name: string, note: string, config: DashboardManagedConfig): void {
  draft.rename(name, note);
  draft.updateConfig(config);
  settingsOpen.value = false;
}
function updateRefresh(value: 0 | 30 | 60 | 300 | 900): void {
  if (!draft.editing) return;
  draft.updateConfig({ ...draft.config, refreshInterval: value });
}
function refreshActive(): void {
  if (draft.editing) void service.refreshDraft();
  else void service.refresh();
}
function updateFilter(key: string, value: unknown): void { store.setFilterValue(key, value); refreshActive(); }
function clearFilters(): void { store.clearFilterValues(); refreshActive(); }
function exportCsv(panel: DashboardPanel): void { exportDashboardCsv(panel.name, store.panelData[panel.id] ?? emptyData()); }
async function exportPng(panel?: DashboardPanel): Promise<void> {
  const element = panel
    ? surface.value?.querySelector<HTMLElement>(`[data-panel-id="${CSS.escape(panel.id)}"]`)
    : surface.value;
  if (!element) return;
  try { await exportDashboardElementPng(element, panel?.name ?? activeDashboard.value?.name ?? "dashboard"); }
  catch (error) { message.error(error instanceof Error ? error.message : String(error)); }
}
function discard(): void {
  if (!draft.dirty || window.confirm(t("dashboard.confirm.discard"))) draft.stop();
}
function emptyData() { return { state: "idle" as const, rows: [], truncated: false, maxPoints: 1, updatedAt: null, error: null }; }
function refreshPanel(panelId: string): void {
  const panel = activeDashboard.value?.panels.find((item) => item.id === panelId);
  if (panel) void service.previewPanel(panel);
}
async function selectPanel(panel: DashboardPanel, value: unknown): Promise<void> {
  service.selectPanelValue(panel, value);
  const currentGeneration = ++drilldownGeneration;
  drilldown.value = {
    show: true,
    title: t("dashboard.drilldown.title", { name: panel.name }),
    selection: value,
    rows: [],
    truncated: false,
    loading: true,
    error: null,
  };
  try {
    const result = await service.drilldown(panel, value);
    if (currentGeneration !== drilldownGeneration) return;
    drilldown.value = { ...drilldown.value, rows: result.rows, truncated: result.truncated, loading: false };
  } catch (error) {
    if (currentGeneration !== drilldownGeneration) return;
    drilldown.value = {
      ...drilldown.value,
      loading: false,
      error: error instanceof Error ? error.message : String(error),
    };
  }
}
function closeDrilldown(): void {
  drilldownGeneration += 1;
  drilldown.value = { ...drilldown.value, show: false, loading: false };
}
</script>

<template>
  <div class="dashboard-workspace" data-testid="dashboard-workspace">
    <DashboardSidebar :dashboards="store.list" :selected-id="store.current?.id" @select="selectDashboard" @create="createOpen = true" />
    <main class="dashboard-main">
      <DashboardToolbar v-if="activeDashboard" :name="activeDashboard.name" :editing="draft.editing" :dirty="draft.dirty" :saving="store.phase === 'saving'" :refresh-interval="activeConfig.refreshInterval" @refresh="refreshActive" @edit="service.beginEdit" @copy="copyDashboard" @delete="deleteDashboard" @save="service.save" @discard="discard" @add-panel="openNewPanel" @configure="settingsOpen = true" @refresh-interval="updateRefresh" @export-png="exportPng()" @print="printDashboard" />
      <DashboardFilterBar v-if="activeDashboard" :filters="filters" :values="store.sessionFilterValues" @change="updateFilter" @clear="clearFilters" />
      <NAlert v-if="draft.conflict" type="warning" class="dashboard-alert" data-testid="dashboard-conflict-error">
        {{ t("dashboard.error.conflict") }} {{ draft.conflict.message }}
        <template #action><NButton size="small" data-testid="dashboard-reload-conflict" @click="reloadConflict">{{ t("dashboard.action.reloadServer") }}</NButton></template>
      </NAlert>
      <NAlert v-if="store.offline" type="info" class="dashboard-alert">{{ t("dashboard.state.offline") }}</NAlert>
      <NAlert v-if="store.error" type="error" closable class="dashboard-alert" data-testid="dashboard-operation-error">{{ store.error }}</NAlert>
      <div v-if="store.phase === 'loading'" class="dashboard-state"><NSpin />{{ t("dashboard.state.loading") }}</div>
      <div v-else-if="activeDashboard" ref="surface" class="dashboard-surface">
        <DashboardGrid :panels="activeDashboard.panels" :data="store.panelData" :editing="draft.editing" @layout="updateLayout" @remove="draft.removePanel" @edit="openPanel" @refresh="refreshPanel" @export-csv="exportCsv" @export-png="exportPng" @select="selectPanel" />
        <NEmpty v-if="activeDashboard.panels.length === 0" :description="t('dashboard.empty.panels')" class="dashboard-empty"><template #icon><NIcon><LayoutDashboard /></NIcon></template><NButton v-if="draft.editing" type="primary" @click="openNewPanel"><template #icon><NIcon><Plus /></NIcon></template>{{ t("dashboard.action.addPanel") }}</NButton></NEmpty>
      </div>
      <NEmpty v-else :description="t('dashboard.empty.select')" class="dashboard-state"><template #icon><NIcon><LayoutDashboard /></NIcon></template><NButton type="primary" data-testid="dashboard-create-empty" @click="createOpen = true">{{ t("dashboard.action.create") }}</NButton></NEmpty>
    </main>
    <DashboardCreateModal :show="createOpen" @close="createOpen = false" @create="createDashboard" />
    <DashboardPanelEditor :show="panelEditorOpen" :panel="editingPanel" :dashboard-id="activeDashboard?.id ?? ''" :collections="collections" :allowed-types="store.allowedPanelTypes" @close="panelEditorOpen = false" @submit="submitPanel" />
    <DashboardSettingsDrawer v-if="activeDashboard" :show="settingsOpen" :name="activeDashboard.name" :note="activeDashboard.note" :config="activeConfig" :panels="activeDashboard.panels" @close="settingsOpen = false" @submit="updateSettings" />
    <DashboardDrilldownDrawer v-bind="drilldown" @close="closeDrilldown" />
  </div>
</template>

<style scoped>
.dashboard-workspace { display:flex; height:100%; min-height:0; background:var(--vt-bg-sunken); }
.dashboard-main { display:flex; flex:1; min-width:0; flex-direction:column; }
.dashboard-surface { flex:1; min-height:0; overflow:auto; }
.dashboard-state { display:flex; flex:1; align-items:center; justify-content:center; gap:10px; color:var(--vt-fg-muted); }
.dashboard-empty { margin-top:14vh; }
.dashboard-alert { margin:8px 12px 0; }
@media print { :deep(.dashboard-sidebar), :deep(.dashboard-toolbar), :deep(.dashboard-filter-bar) { display:none!important; } .dashboard-surface { overflow:visible; } }
@media (max-width:760px) { :deep(.dashboard-sidebar) { flex-basis:180px; } }
</style>
