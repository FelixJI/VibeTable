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
  collapsedKeys?: readonly string[];
}>();
const emit = defineEmits<{ more: []; toggle: [key: string] }>();
const names = computed(() => new Map(props.columns.map((column) => [column.name, column.title])));
const collapsed = computed(() => new Set(props.collapsedKeys ?? []));
const tree = computed(() => {
  if (props.groups.length < 2) return [];
  const parents = new Map<string, {
    key: string;
    value: unknown;
    count: number;
    summaries: readonly unknown[];
    rows: ViewGroupRow[];
  }>();
  for (const row of props.rows) {
    const value = row.key[0];
    const key = JSON.stringify([value]);
    const parent = parents.get(key) ?? {
      key,
      value,
      count: row.parentCount ?? row.count,
      summaries: row.parentSummaries ?? row.summaries,
      rows: [],
    };
    parent.rows.push(row);
    parents.set(key, parent);
  }
  return [...parents.values()];
});

function firstGroupLabel(value: unknown): string {
  const field = props.groups[0]?.field ?? "group_1";
  return `${names.value.get(field) ?? field}: ${String(value ?? "空值")}`;
}

function groupLabel(row: ViewGroupRow, startIndex = 0): string {
  return row.key.map((value, index) => {
    const field = props.groups[index + startIndex]?.field ?? `group_${index + startIndex + 1}`;
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
      <span>完整筛选结果 · 已加载 {{ rows.length }} 个组合</span>
    </div>
    <ol v-if="groups.length < 2">
      <li v-for="(row, index) in rows" :key="`${index}:${JSON.stringify(row.key)}`">
        <span class="group-key">{{ groupLabel(row) }}</span>
        <b>{{ row.count }}</b>
        <span v-if="row.summaries.length" class="group-summary">{{ summaryLabel(row) }}</span>
      </li>
    </ol>
    <ol v-else class="group-tree">
      <li v-for="parent in tree" :key="parent.key" class="group-parent">
        <button type="button" class="group-toggle" :aria-expanded="!collapsed.has(parent.key)" @click="emit('toggle', parent.key)">
          <span aria-hidden="true">{{ collapsed.has(parent.key) ? "▸" : "▾" }}</span>
          <span class="group-key">{{ firstGroupLabel(parent.value) }}</span>
          <b>{{ parent.count }}</b>
          <span v-if="parent.summaries.length" class="group-summary">{{ summaryLabel({ key: [parent.value], count: parent.count, summaries: parent.summaries }) }}</span>
        </button>
        <ol v-if="!collapsed.has(parent.key)" class="group-children">
          <li v-for="(row, index) in parent.rows" :key="`${index}:${JSON.stringify(row.key)}`">
            <span class="group-key">{{ groupLabel({ ...row, key: row.key.slice(1) }, 1) }}</span>
            <b>{{ row.count }}</b>
            <span v-if="row.summaries.length" class="group-summary">{{ summaryLabel(row) }}</span>
          </li>
        </ol>
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
.view-groups .group-parent { display: block; padding: 0; }
.group-toggle { display: grid; width: 100%; grid-template-columns: 18px minmax(180px, 1fr) 64px minmax(180px, auto); align-items: center; gap: 8px; min-height: 31px; padding: 3px 10px; border: 0; border-top: 1px solid color-mix(in srgb, var(--vt-border) 70%, transparent); color: inherit; background: transparent; font: inherit; text-align: left; cursor: pointer; }
.group-toggle:hover { background: var(--vt-bg-sunken); }
.group-toggle b { justify-self: end; }
.group-children { padding-left: 18px !important; }
.group-key { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.view-groups li b { justify-self: end; padding: 1px 7px; border-radius: 999px; background: var(--vt-bg-sunken); font-variant-numeric: tabular-nums; }
.group-summary { color: var(--vt-fg-muted); white-space: nowrap; }
@media (max-width: 720px) { .view-groups li { grid-template-columns: minmax(150px, 1fr) 52px; } .group-summary { grid-column: 1 / -1; } }
</style>
