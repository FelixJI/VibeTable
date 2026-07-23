<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import type { DashboardPanel } from "@/dashboard";
import { useTheme } from "@/composables/useTheme";
import { buildDashboardChartOption, dashboardChartKeyboardSelections, dashboardChartSelectionValue } from "@/dashboard/charts/chartOptionAdapter";
import { createDashboardChart } from "@/dashboard/charts/echartsCore";
import type { EChartsType } from "echarts/core";
import { t } from "@/i18n";

const props = defineProps<{
  panel: DashboardPanel;
  rows: readonly Record<string, unknown>[];
}>();
const emit = defineEmits<{ select: [value: unknown] }>();
const root = ref<HTMLElement | null>(null);
const { isDark } = useTheme();
let chart: EChartsType | null = null;
let observer: ResizeObserver | null = null;

const option = computed(() => buildDashboardChartOption(props.panel, props.rows, isDark.value, t("dashboard.chart.other")));
const keyboardSelections = computed(() => dashboardChartKeyboardSelections(props.panel, props.rows));
function selectByIndex(event: Event): void {
  const target = event.target as HTMLSelectElement;
  const index = Number(target.value);
  const selection = keyboardSelections.value[index];
  if (selection) emit("select", selection.value);
  target.value = "";
}

function mountChart(): void {
  if (!root.value) return;
  chart?.dispose();
  chart = createDashboardChart(root.value, isDark.value);
  chart.setOption(option.value, { notMerge: true, lazyUpdate: true });
  chart.on("click", (event) => emit("select", dashboardChartSelectionValue(event)));
}

onMounted(() => {
  mountChart();
  if (root.value) {
    observer = new ResizeObserver(() => chart?.resize());
    observer.observe(root.value);
  }
});

watch(option, (value) => chart?.setOption(value, { notMerge: true, lazyUpdate: true }));
watch(isDark, mountChart);
onBeforeUnmount(() => {
  observer?.disconnect();
  chart?.dispose();
  chart = null;
});
</script>

<template>
  <div class="dashboard-chart-wrap">
    <div ref="root" class="dashboard-chart" role="img" :aria-label="panel.name"></div>
    <label class="keyboard-select-label">
      <span class="sr-only">{{ t("dashboard.chart.keyboardSelect") }}</span>
      <select :aria-label="t('dashboard.chart.keyboardSelect')" @change="selectByIndex">
        <option value="" selected disabled>{{ t("dashboard.chart.keyboardSelect") }}</option>
        <option v-for="(item, index) in keyboardSelections" :key="index" :value="index">{{ item.label }}</option>
      </select>
    </label>
  </div>
</template>

<style scoped>
.dashboard-chart-wrap { position:relative; width:100%; height:100%; }
.dashboard-chart { width: 100%; height: 100%; min-height: 160px; }
.keyboard-select-label { position:absolute; top:2px; right:2px; opacity:.12; transition:opacity .15s; }
.keyboard-select-label:focus-within, .dashboard-chart-wrap:hover .keyboard-select-label { opacity:1; }
.keyboard-select-label select { max-width:160px; height:24px; border:1px solid var(--vt-border); border-radius:var(--vt-radius-sm); background:var(--vt-bg); color:var(--vt-fg); font-size:11px; }
.sr-only { position:absolute; width:1px; height:1px; overflow:hidden; clip:rect(0,0,0,0); }
</style>
