<script setup lang="ts">
import { computed, ref } from "vue";
import { NButton, NIcon, NSpin, NTooltip } from "naive-ui";
import { AlertTriangle, Download, Grip, ImageDown, Pencil, RefreshCw, Table2, Trash2 } from "lucide-vue-next";
import type { DashboardPanel } from "@/dashboard";
import { formatDashboardNumber, type NumberFormatSpec } from "@/dashboard";
import { numericValue } from "@/dashboard/charts/chartOptionAdapter";
import type { DashboardPanelData } from "@/stores/dashboardStore";
import DashboardChart from "./DashboardChart.vue";
import { t } from "@/i18n";
import { useUiStore } from "@/stores/uiStore";

const props = defineProps<{
  panel: DashboardPanel;
  data: DashboardPanelData;
  editing: boolean;
  visible: boolean;
}>();
const emit = defineEmits<{
  refresh: [panelId: string];
  remove: [panelId: string];
  edit: [panel: DashboardPanel];
  exportCsv: [panel: DashboardPanel];
  exportPng: [panel: DashboardPanel];
  select: [panel: DashboardPanel, value: unknown];
}>();
const showData = ref(false);
const ui = useUiStore();
const columns = computed(() => [...new Set(props.data.rows.flatMap((row) => Object.keys(row)))]);
const metric = computed(() => numericValue(props.data.rows[0] ?? {}));
const labelText = computed(() => String(props.panel.options.text ?? props.panel.name));
const isChart = computed(() => ["time-series", "bar", "line", "donut", "pie"].includes(props.panel.productType));
const metricRows = computed(() => props.panel.productType === "metric-list" && props.data.rows.length > 0
  ? Object.entries(props.data.rows[0]!).map(([label, value]) => ({ label, value: numericValue({ value }) }))
  : props.data.rows.slice(0, 12).map((row) => ({ label: String(Object.values(row)[0] ?? ""), value: numericValue(row) })));
const summary = computed(() => {
  if (props.data.state === "failed") return `${props.panel.name}: ${props.data.error ?? t("dashboard.error.panel")}`;
  if (props.panel.productType === "metric") return `${props.panel.name}: ${formatDashboardNumber(metric.value, ui.locale)}`;
  return `${props.panel.name}: ${props.data.rows.length} ${t("dashboard.data.rows")}`;
});

function numberFormat(options: Readonly<Record<string, unknown>>): NumberFormatSpec {
  const style = options.style === "percent" || options.style === "currency" || options.style === "compact"
    ? options.style
    : "number";
  return {
    style,
    currency: typeof options.currency === "string" ? options.currency : undefined,
    minimumFractionDigits: integer(options.minimumFractionDigits),
    maximumFractionDigits: integer(options.maximumFractionDigits),
    prefix: typeof options.prefix === "string" ? options.prefix : undefined,
    suffix: typeof options.suffix === "string" ? options.suffix : undefined,
    percentIsWhole: options.percentIsWhole === true,
  };
}
function integer(value: unknown): number | undefined {
  return typeof value === "number" && Number.isInteger(value) && value >= 0 && value <= 20 ? value : undefined;
}
</script>

<template>
  <article class="dashboard-panel" :data-panel-id="panel.id" :aria-label="summary">
    <header v-if="panel.showHeader !== false || editing" class="panel-header">
      <span v-if="editing" class="panel-drag" :aria-label="t('dashboard.layout.drag')"><Grip :size="14" /></span>
      <div class="panel-title">
        <strong>{{ panel.name || t("dashboard.panel.untitled") }}</strong>
        <small v-if="data.updatedAt">{{ new Date(data.updatedAt).toLocaleTimeString() }}</small>
      </div>
      <div class="panel-actions">
        <NTooltip><template #trigger><NButton quaternary size="tiny" :aria-label="t('dashboard.action.refreshPanel')" @click="emit('refresh', panel.id)"><NIcon><RefreshCw /></NIcon></NButton></template>{{ t("dashboard.action.refreshPanel") }}</NTooltip>
        <NTooltip><template #trigger><NButton quaternary size="tiny" :aria-label="t('dashboard.action.dataView')" @click="showData = !showData"><NIcon><Table2 /></NIcon></NButton></template>{{ t("dashboard.action.dataView") }}</NTooltip>
        <NTooltip><template #trigger><NButton quaternary size="tiny" :aria-label="t('dashboard.action.exportCsv')" @click="emit('exportCsv', panel)"><NIcon><Download /></NIcon></NButton></template>{{ t("dashboard.action.exportCsv") }}</NTooltip>
        <NTooltip><template #trigger><NButton quaternary size="tiny" :aria-label="t('dashboard.action.exportPanelPng')" @click="emit('exportPng', panel)"><NIcon><ImageDown /></NIcon></NButton></template>{{ t("dashboard.action.exportPanelPng") }}</NTooltip>
        <NButton v-if="editing && panel.editable" quaternary size="tiny" :aria-label="t('dashboard.panel.edit')" @click="emit('edit', panel)"><NIcon><Pencil /></NIcon></NButton>
        <NButton v-if="editing && panel.editable" quaternary size="tiny" type="error" :aria-label="t('dashboard.action.removePanel')" @click="emit('remove', panel.id)"><NIcon><Trash2 /></NIcon></NButton>
      </div>
    </header>

    <div class="panel-body">
      <div v-if="data.state === 'loading' || data.state === 'queued'" class="panel-state"><NSpin size="small" />{{ t("dashboard.state.loading") }}</div>
      <div v-else-if="data.state === 'failed'" class="panel-state panel-state--error"><AlertTriangle :size="18" /><span>{{ data.error }}</span></div>
      <div v-else-if="!panel.editable" class="panel-state panel-state--unknown">
        <AlertTriangle :size="18" />
        <div><strong>{{ t("dashboard.unknown.title") }}</strong><p>{{ t("dashboard.unknown.description", { type: panel.rawType }) }}</p></div>
      </div>
      <div v-else-if="panel.productType === 'label'" class="label-panel">{{ labelText }}</div>
      <div v-else-if="panel.productType === 'metric'" class="metric-panel">
        <strong>{{ formatDashboardNumber(metric, ui.locale, numberFormat(panel.options)) }}</strong>
      </div>
      <div v-else-if="(panel.productType === 'list' || panel.productType === 'metric-list') && !showData" class="list-panel">
        <div v-for="(row, index) in metricRows" :key="index" class="list-row">
          <span>{{ row.label }}</span><strong>{{ formatDashboardNumber(row.value, ui.locale) }}</strong>
        </div>
        <p v-if="data.rows.length === 0" class="empty-copy">{{ t("dashboard.data.empty") }}</p>
      </div>
      <DashboardChart v-else-if="isChart && !showData && visible" :panel="panel" :rows="data.rows" @select="emit('select', panel, $event)" />
      <div v-else-if="isChart && !visible" class="chart-placeholder" aria-hidden="true"></div>

      <div v-if="showData" class="data-table-wrap">
        <table>
          <caption class="sr-only">{{ summary }}</caption>
          <thead><tr><th v-for="column in columns" :key="column" scope="col">{{ column }}</th></tr></thead>
          <tbody><tr v-for="(row, index) in data.rows.slice(0, 500)" :key="index"><td v-for="column in columns" :key="column">{{ row[column] }}</td></tr></tbody>
        </table>
        <p v-if="data.rows.length > 500" class="table-limit-note">{{ t("dashboard.data.tableLimit", { count: 500, total: data.rows.length }) }}</p>
      </div>
    </div>
    <footer v-if="data.truncated" class="panel-warning">{{ t("dashboard.data.truncated", { count: data.maxPoints }) }}</footer>
  </article>
</template>

<style scoped>
.dashboard-panel { display:flex; flex-direction:column; height:100%; overflow:hidden; border:1px solid var(--vt-border); border-radius:var(--vt-radius-lg); background:var(--vt-bg); box-shadow:var(--vt-shadow-sm); }
.panel-header { display:flex; align-items:center; min-height:38px; padding:0 8px 0 12px; border-bottom:1px solid var(--vt-border-subtle); }
.panel-drag { display:flex; margin-right:6px; color:var(--vt-fg-subtle); cursor:grab; }
.panel-title { min-width:0; display:flex; align-items:baseline; gap:8px; flex:1; }
.panel-title strong { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font-size:13px; }
.panel-title small { color:var(--vt-fg-subtle); font-size:10px; }
.panel-actions { display:flex; opacity:.35; transition:opacity .15s; }
.dashboard-panel:hover .panel-actions, .dashboard-panel:focus-within .panel-actions { opacity:1; }
.panel-body { position:relative; flex:1; min-height:0; overflow:hidden; padding:10px; }
.panel-state { display:flex; height:100%; align-items:center; justify-content:center; gap:8px; color:var(--vt-fg-muted); font-size:12px; }
.panel-state--error { color:var(--vt-color-danger-500); }
.panel-state--unknown { align-items:flex-start; justify-content:flex-start; padding:16px; background:var(--vt-bg-subtle); }
.panel-state p { margin:5px 0 0; }
.label-panel { display:flex; height:100%; align-items:center; justify-content:center; padding:10px; text-align:center; font-size:clamp(18px,3vw,36px); font-weight:750; }
.metric-panel { display:flex; height:100%; align-items:center; justify-content:center; gap:7px; font-variant-numeric:tabular-nums; }
.metric-panel strong { font-size:clamp(28px,4vw,52px); line-height:1; letter-spacing:-.035em; }
.metric-panel span { color:var(--vt-fg-muted); }
.list-panel { height:100%; overflow:auto; }
.list-row { display:flex; justify-content:space-between; gap:16px; padding:7px 5px; border-bottom:1px solid var(--vt-border-subtle); }
.list-row span { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.data-table-wrap { height:100%; overflow:auto; }
.table-limit-note { position:sticky; bottom:0; margin:0; padding:6px 8px; color:var(--vt-fg-muted); background:var(--vt-bg-subtle); font-size:10px; }
table { width:100%; border-collapse:collapse; font-size:11px; }
th,td { padding:6px 8px; text-align:left; border-bottom:1px solid var(--vt-border-subtle); white-space:nowrap; }
th { position:sticky; top:0; background:var(--vt-bg); }
.chart-placeholder { height:100%; background:linear-gradient(90deg,var(--vt-bg-subtle),var(--vt-bg),var(--vt-bg-subtle)); }
.panel-warning { padding:3px 10px; background:var(--vt-color-warning-50); color:var(--vt-color-warning-700); font-size:10px; }
.empty-copy { text-align:center; color:var(--vt-fg-subtle); }
.sr-only { position:absolute; width:1px; height:1px; overflow:hidden; clip:rect(0,0,0,0); }
</style>
