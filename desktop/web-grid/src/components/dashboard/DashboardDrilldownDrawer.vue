<script setup lang="ts">
import { computed } from "vue";
import { NAlert, NDataTable, NDrawer, NDrawerContent, NSpin } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { t } from "@/i18n";

const props = defineProps<{
  show: boolean;
  title: string;
  selection: unknown;
  rows: readonly Record<string, unknown>[];
  truncated: boolean;
  loading: boolean;
  error: string | null;
}>();
const emit = defineEmits<{ close: [] }>();
const columns = computed<DataTableColumns<Record<string, unknown>>>(() =>
  [...new Set(props.rows.flatMap((row) => Object.keys(row)))].map((key) => ({
    title: key,
    key,
    ellipsis: { tooltip: true },
    render: (row) => formatCell(row[key]),
  })),
);
const tableRows = computed(() => [...props.rows]);
function formatCell(value: unknown): string {
  if (value === null || value === undefined) return "—";
  return typeof value === "object" ? JSON.stringify(value) : String(value);
}
function rowKey(row: Record<string, unknown>): string {
  return typeof row.id === "string" || typeof row.id === "number"
    ? String(row.id)
    : JSON.stringify(row);
}
</script>

<template>
  <NDrawer :show="show" :width="680" placement="right" @update:show="!$event && emit('close')">
    <NDrawerContent :title="title" closable>
      <div data-testid="dashboard-drilldown">
      <p class="drilldown-context">{{ t("dashboard.drilldown.selection", { value: formatCell(selection) }) }}</p>
      <NAlert v-if="truncated" type="warning" class="drilldown-alert">{{ t("dashboard.drilldown.truncated") }}</NAlert>
      <NAlert v-if="error" type="error" class="drilldown-alert">{{ error }}</NAlert>
      <div v-if="loading" class="drilldown-loading"><NSpin />{{ t("dashboard.state.loading") }}</div>
      <NDataTable
        v-else
        :columns="columns"
        :data="tableRows"
        :pagination="{ pageSize: 20, showSizePicker: false }"
        :max-height="'calc(100vh - 190px)'"
        striped
        size="small"
        :row-key="rowKey"
        :aria-label="t('dashboard.drilldown.table')"
      />
      </div>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.drilldown-context { margin:0 0 12px; color:var(--vt-fg-muted); font-size:12px; }
.drilldown-alert { margin-bottom:12px; }
.drilldown-loading { display:flex; min-height:180px; align-items:center; justify-content:center; gap:10px; color:var(--vt-fg-muted); }
</style>
