<script setup lang="ts">
import { computed } from "vue";
import { NButton } from "naive-ui";
import type { ColumnSchema, GroupCondition, SummaryCondition, ViewGroupRow } from "@/contracts";

const props = defineProps<{
  rows: readonly ViewGroupRow[];
  groups: readonly GroupCondition[];
  summaries: readonly SummaryCondition[];
  columns: readonly ColumnSchema[];
  hasMore: boolean;
}>();
const emit = defineEmits<{ more: [] }>();
const names = computed(() => new Map(props.columns.map((column) => [column.name, column.title])));

function groupLabel(row: ViewGroupRow): string {
  return row.key.map((value, index) => {
    const field = props.groups[index]?.field ?? `group_${index + 1}`;
    return `${names.value.get(field) ?? field}: ${String(value ?? "空值")}`;
  }).join(" / ");
}
function summaryLabel(row: ViewGroupRow): string {
  return row.summaries.map((value, index) => {
    const summary = props.summaries[index];
    if (!summary) return String(value ?? "—");
    const field = names.value.get(summary.field) ?? summary.field;
    const fn = { sum: "合计", avg: "平均", min: "最小", max: "最大" }[summary.function];
    return `${field} ${fn}: ${String(value ?? "—")}`;
  }).join(" · ");
}
</script>

<template>
  <section v-if="rows.length" class="view-groups" aria-label="权威分组结果" data-testid="view-group-results">
    <div class="view-groups__heading">
      <strong>分组概览</strong>
      <span>完整筛选结果 · {{ rows.length }} 个组合</span>
    </div>
    <ol>
      <li v-for="(row, index) in rows" :key="`${index}:${JSON.stringify(row.key)}`">
        <span class="group-key">{{ groupLabel(row) }}</span>
        <b>{{ row.count }}</b>
        <span v-if="row.summaries.length" class="group-summary">{{ summaryLabel(row) }}</span>
      </li>
    </ol>
    <NButton v-if="hasMore" size="tiny" quaternary data-testid="view-group-more" @click="emit('more')">
      加载更多分组
    </NButton>
  </section>
</template>

<style scoped>
.view-groups { flex: 0 0 auto; max-height: 176px; overflow: auto; border-bottom: 1px solid var(--vt-border); background: color-mix(in srgb, var(--vt-bg-subtle) 82%, var(--vt-bg)); }
.view-groups__heading { position: sticky; top: 0; z-index: 1; display: flex; align-items: baseline; gap: 9px; padding: 6px 10px; background: inherit; }
.view-groups__heading strong { font-size: var(--vt-font-caption); }
.view-groups__heading span { color: var(--vt-fg-muted); font-size: 11px; }
.view-groups ol { display: grid; margin: 0; padding: 0; list-style: none; }
.view-groups li { display: grid; grid-template-columns: minmax(180px, 1fr) 64px minmax(180px, auto); align-items: center; gap: 10px; min-height: 29px; padding: 3px 10px; border-top: 1px solid color-mix(in srgb, var(--vt-border) 70%, transparent); font-size: var(--vt-font-caption); }
.group-key { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.view-groups li b { justify-self: end; padding: 1px 7px; border-radius: 999px; background: var(--vt-bg-sunken); font-variant-numeric: tabular-nums; }
.group-summary { color: var(--vt-fg-muted); white-space: nowrap; }
@media (max-width: 720px) { .view-groups li { grid-template-columns: minmax(150px, 1fr) 52px; } .group-summary { grid-column: 1 / -1; } }
</style>
