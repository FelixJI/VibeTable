<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NButton, NCheckbox, NIcon, NInputNumber, NPopover, NSelect } from "naive-ui";
import { EyeOff, Filter, Layers3, Sigma } from "lucide-vue-next";
import type {
  ColumnSchema,
  FilterExpression,
  GroupCondition,
  SummaryCondition,
  LookupDefinition,
  NormalizedRelationDescriptor,
} from "@/contracts";
import FilterTreeEditor from "./FilterTreeEditor.vue";
import { cloneFilterExpressions } from "@/stores/viewQueryStore";

const props = defineProps<{
  columns: readonly ColumnSchema[];
  filters: readonly FilterExpression[];
  groups: readonly GroupCondition[];
  summaries: readonly SummaryCondition[];
  visibleFields: readonly string[];
  relations?: readonly NormalizedRelationDescriptor[];
  lookups?: readonly LookupDefinition[];
}>();
const emit = defineEmits<{ change: [value: {
  filters: FilterExpression[];
  groups: GroupCondition[];
  summaries: SummaryCondition[];
  visibleFields: string[];
}] }>();

const draftFilters = ref<FilterExpression[]>([]);
const draftGroups = ref<GroupCondition[]>([]);
const draftSummaries = ref<SummaryCondition[]>([]);
const draftVisible = ref<string[]>([]);
const relationsById = computed(() => new Map(
  (props.relations ?? []).map(relation => [relation.relationId, relation]),
));
const lookupsById = computed(() => new Map(
  (props.lookups ?? []).map(lookup => [lookup.lookupId, lookup]),
));
const groupColumns = computed(() => props.columns.filter((column) => {
  if (column.kind === "attachment" || column.dataType === "json") return false;
  if (column.kind === "relation") {
    return !!column.relationId && relationsById.value.get(column.relationId)?.kind === "m2o";
  }
  if (column.kind === "lookup") {
    return !!column.lookupId && lookupsById.value.get(column.lookupId)?.aggregation === "single";
  }
  return true;
}));
const groupOptions = computed(() => groupColumns.value.map((column) => ({ label: column.title, value: column.name })));
const numericOptions = computed(() => props.columns
  .filter((column) => column.dataType === "integer" || column.dataType === "decimal")
  .map((column) => ({ label: column.title, value: column.name })));
const hiddenCount = computed(() => Math.max(props.columns.length - props.visibleFields.length, 0));

watch(() => [props.filters, props.groups, props.summaries, props.visibleFields] as const, () => {
  draftFilters.value = cloneFilterExpressions(props.filters);
  draftGroups.value = [...props.groups];
  draftSummaries.value = [...props.summaries];
  draftVisible.value = [...props.visibleFields];
}, { immediate: true, deep: true });

function commit(): void {
  emit("change", {
    filters: cloneFilterExpressions(draftFilters.value),
    groups: draftGroups.value.slice(0, 2),
    summaries: draftSummaries.value.slice(0, 3),
    visibleFields: [...draftVisible.value],
  });
}
function addGroup(): void {
  const field = groupOptions.value[0]?.value;
  if (!field || draftGroups.value.length >= 2) return;
  draftGroups.value = [...draftGroups.value, { field, direction: "asc", bucket: "value" }];
}
function updateGroup(index: number, patch: Partial<GroupCondition>): void {
  draftGroups.value = draftGroups.value.map((group, groupIndex) => groupIndex === index ? { ...group, ...patch } : group);
}
function addSummary(): void {
  const field = numericOptions.value[0]?.value;
  if (!field || draftSummaries.value.length >= 3) return;
  draftSummaries.value = [...draftSummaries.value, { field, function: "sum" }];
}
function updateSummary(index: number, patch: Partial<SummaryCondition>): void {
  draftSummaries.value = draftSummaries.value.map((summary, summaryIndex) => summaryIndex === index ? { ...summary, ...patch } : summary);
}
function toggleField(field: string, checked: boolean): void {
  draftVisible.value = checked
    ? [...new Set([...draftVisible.value, field])]
    : draftVisible.value.filter((item) => item !== field);
}
function isDateField(field: string): boolean {
  const dataType = props.columns.find((column) => column.name === field)?.dataType;
  return dataType === "date" || dataType === "datetime";
}
function isNumericField(field: string): boolean {
  const dataType = props.columns.find((column) => column.name === field)?.dataType;
  return dataType === "integer" || dataType === "decimal";
}
</script>

<template>
  <div class="view-controls" aria-label="视图查询工具" data-testid="view-query-controls">
    <NPopover trigger="click" placement="bottom-start" :show-arrow="false">
      <template #trigger>
        <NButton size="tiny" quaternary data-testid="view-filter-trigger">
          <template #icon><NIcon><Filter /></NIcon></template>
          筛选 <b v-if="filters.length">{{ filters.length }}</b>
        </NButton>
      </template>
      <div class="control-card control-card--wide">
        <header><strong>筛选记录</strong><span>最多 3 级、50 个条件</span></header>
        <FilterTreeEditor :nodes="draftFilters" :columns="columns" @update="nodes => draftFilters = nodes" />
        <footer><NButton size="small" type="primary" data-testid="view-filter-apply" @click="commit">应用</NButton></footer>
      </div>
    </NPopover>

    <NPopover trigger="click" placement="bottom-start" :show-arrow="false">
      <template #trigger>
        <NButton size="tiny" quaternary data-testid="view-group-trigger">
          <template #icon><NIcon><Layers3 /></NIcon></template>
          分组 <b v-if="groups.length">{{ groups.length }}</b>
        </NButton>
      </template>
      <div class="control-card">
        <header><strong>只读分组</strong><span>基于完整筛选结果，最多两级</span></header>
        <div v-for="(group, index) in draftGroups" :key="index" class="config-row">
          <NSelect :data-testid="`view-group-field-${index}`" size="small" :value="group.field" :options="groupOptions" @update:value="field => updateGroup(index, { field, bucket: 'value', numberInterval: null })" />
          <NSelect size="small" :value="group.direction ?? 'asc'" :options="[{ label: '升序', value: 'asc' }, { label: '降序', value: 'desc' }]" @update:value="direction => updateGroup(index, { direction })" />
          <NSelect v-if="isDateField(group.field)" size="small" :value="group.bucket ?? 'value'" :options="[{ label: '精确值', value: 'value' }, { label: '年', value: 'year' }, { label: '季度', value: 'quarter' }, { label: '月', value: 'month' }, { label: '周', value: 'week' }, { label: '日', value: 'day' }, { label: '小时', value: 'hour' }]" @update:value="bucket => updateGroup(index, { bucket })" />
          <NSelect v-else-if="isNumericField(group.field)" size="small" :value="group.bucket ?? 'value'" :options="[{ label: '精确值', value: 'value' }, { label: '数值区间', value: 'number' }]" @update:value="bucket => updateGroup(index, { bucket, numberInterval: bucket === 'number' ? (group.numberInterval ?? 10) : null })" />
          <NInputNumber v-if="group.bucket === 'number'" size="small" :value="group.numberInterval ?? 10" :min="0.000001" :show-button="false" aria-label="数值分组间隔" @update:value="numberInterval => updateGroup(index, { numberInterval })" />
          <NButton size="tiny" quaternary aria-label="删除分组" @click="draftGroups = draftGroups.filter((_, i) => i !== index)">×</NButton>
        </div>
        <NButton size="tiny" secondary :disabled="draftGroups.length >= 2 || !groupOptions.length" @click="addGroup">＋ 分组字段</NButton>
        <footer><NButton size="small" type="primary" data-testid="view-group-apply" @click="commit">应用</NButton></footer>
      </div>
    </NPopover>

    <NPopover trigger="click" placement="bottom-start" :show-arrow="false">
      <template #trigger>
        <NButton size="tiny" quaternary data-testid="view-summary-trigger">
          <template #icon><NIcon><Sigma /></NIcon></template>
          汇总 <b v-if="summaries.length">{{ summaries.length }}</b>
        </NButton>
      </template>
      <div class="control-card">
        <header><strong>组内汇总</strong><span>数值字段，最多三个</span></header>
        <div v-for="(summary, index) in draftSummaries" :key="index" class="config-row config-row--summary">
          <NSelect size="small" :value="summary.field" :options="numericOptions" @update:value="field => updateSummary(index, { field })" />
          <NSelect size="small" :value="summary.function" :options="[{ label: '求和', value: 'sum' }, { label: '平均', value: 'avg' }, { label: '最小', value: 'min' }, { label: '最大', value: 'max' }]" @update:value="fn => updateSummary(index, { function: fn })" />
          <NButton size="tiny" quaternary aria-label="删除汇总" @click="draftSummaries = draftSummaries.filter((_, i) => i !== index)">×</NButton>
        </div>
        <NButton size="tiny" secondary :disabled="draftSummaries.length >= 3 || !numericOptions.length" @click="addSummary">＋ 汇总字段</NButton>
        <footer><NButton size="small" type="primary" data-testid="view-summary-apply" @click="commit">应用</NButton></footer>
      </div>
    </NPopover>

    <NPopover trigger="click" placement="bottom-start" :show-arrow="false">
      <template #trigger>
        <NButton size="tiny" quaternary data-testid="view-hidden-trigger">
          <template #icon><NIcon><EyeOff /></NIcon></template>
          隐藏 <b v-if="hiddenCount">{{ hiddenCount }}</b>
        </NButton>
      </template>
      <div class="control-card field-card">
        <header><strong>显示字段</strong><span>仅影响当前视图</span></header>
        <label v-for="column in columns" :key="column.name">
          <NCheckbox :checked="draftVisible.includes(column.name)" @update:checked="checked => toggleField(column.name, checked)" />
          <span>{{ column.title }}</span>
        </label>
        <footer><NButton size="small" type="primary" data-testid="view-hidden-apply" @click="commit">应用</NButton></footer>
      </div>
    </NPopover>
  </div>
</template>

<style scoped>
.view-controls { display: flex; min-height: 34px; align-items: center; gap: 2px; padding: 3px 10px; border-bottom: 1px solid var(--vt-border); background: var(--vt-bg); }
.view-controls b { min-width: 17px; padding: 0 5px; border-radius: 999px; color: var(--vt-fg-accent-strong); background: var(--vt-color-primary-50); font-size: 10px; line-height: 17px; }
.control-card { display: grid; width: 390px; gap: 10px; padding: 3px; }
.control-card--wide { width: min(560px, calc(100vw - 36px)); }
.control-card header { display: flex; align-items: baseline; justify-content: space-between; gap: 14px; padding-bottom: 8px; border-bottom: 1px solid var(--vt-border); }
.control-card header span { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.control-card footer { display: flex; justify-content: flex-end; padding-top: 8px; border-top: 1px solid var(--vt-border); }
.config-row { display: grid; grid-template-columns: minmax(150px, 1fr) 92px minmax(100px, auto) 26px; gap: 6px; }
.config-row--summary { grid-template-columns: minmax(170px, 1fr) 110px 26px; }
.field-card { width: 280px; max-height: 420px; overflow: auto; }
.field-card label { display: flex; min-height: 30px; align-items: center; gap: 9px; padding: 0 6px; border-radius: var(--vt-radius-sm); }
.field-card label:hover { background: var(--vt-bg-subtle); }
@media (max-width: 720px) {
  .view-controls { overflow-x: auto; }
  .control-card { width: min(390px, calc(100vw - 36px)); }
}
</style>
