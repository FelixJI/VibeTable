<script setup lang="ts">
import { computed, ref } from "vue";
import { NButton, NDatePicker, NDynamicTags, NInput, NInputNumber, NSelect } from "naive-ui";
import type {
  ColumnSchema,
  FilterCondition,
  FilterExpression,
  FilterOperator,
  NormalizedRelationDescriptor,
  RelationTargetRef,
} from "@/contracts";

defineOptions({ name: "FilterTreeEditor" });
const props = withDefaults(defineProps<{
  nodes: readonly FilterExpression[];
  columns: readonly ColumnSchema[];
  depth?: number;
  totalConditions?: number;
  relations?: readonly NormalizedRelationDescriptor[];
  searchRelationTargets?: (relationId: string, query: string) => Promise<readonly RelationTargetRef[]>;
}>(), { depth: 1 });
const emit = defineEmits<{ update: [nodes: FilterExpression[]] }>();

const capabilityColumns = computed(() => props.columns.filter(
  column => (column.filterOperators?.length ?? 0) > 0,
));
const fieldOptions = computed(() => capabilityColumns.value.map((column) => ({
  label: column.title,
  value: column.name,
})));
const operatorLabels: Record<FilterOperator, string> = {
  eq: "等于", ne: "不等于", contains: "包含", starts_with: "开头是",
  ends_with: "结尾是", gt: "大于", gte: "大于等于", lt: "小于",
  lte: "小于等于", between: "介于", in: "属于", is_null: "为空",
  is_not_null: "不为空", regex: "正则匹配",
};
const operatorOptionsFor = (field: string) => {
  const operators = props.columns.find(item => item.name === field)?.filterOperators ?? [];
  return operators.map(value => ({ label: operatorLabels[value], value }));
};
const localConditionCount = computed(() => countConditions(props.nodes));
const conditionCount = computed(() => props.totalConditions ?? localConditionCount.value);
const atConditionLimit = computed(() => conditionCount.value >= 50);
const relationOptions = ref<Record<string, Array<{ label: string; value: string }>>>({});
const relationLoading = ref<Record<string, boolean>>({});

function countConditions(nodes: readonly FilterExpression[]): number {
  return nodes.reduce((count, node) => count + (isCondition(node)
    ? 1
    : countConditions(node.filters)), 0);
}

function isCondition(node: FilterExpression): node is FilterCondition {
  return "field" in node;
}
function replace(index: number, value: FilterExpression): void {
  emit("update", props.nodes.map((node, nodeIndex) => nodeIndex === index ? value : node));
}
function updateLogic(index: number, logic: "AND" | "OR"): void {
  const current = props.nodes[index];
  if (!current) return;
  replace(index, { ...current, logic });
}
function remove(index: number): void {
  emit("update", props.nodes.filter((_, nodeIndex) => nodeIndex !== index));
}
function addCondition(): void {
  const field = capabilityColumns.value[0]?.name;
  if (!field || atConditionLimit.value) return;
  const operator = operatorOptionsFor(field)[0]?.value;
  if (!operator) return;
  emit("update", [...props.nodes, { field, operator, value: initialValue(field, operator) }]);
}
function addGroup(): void {
  const field = capabilityColumns.value[0]?.name;
  if (!field || props.depth >= 3 || atConditionLimit.value) return;
  const operator = operatorOptionsFor(field)[0]?.value;
  if (!operator) return;
  emit("update", [...props.nodes, {
    groupLogic: "AND",
    filters: [{ field, operator, value: initialValue(field, operator) }],
  }]);
}
function updateCondition(index: number, patch: Partial<FilterCondition>): void {
  const current = props.nodes[index];
  if (!current || !isCondition(current)) return;
  replace(index, { ...current, ...patch });
}
function updateField(index: number, field: string): void {
  const current = props.nodes[index];
  if (!current || !isCondition(current)) return;
  const allowed = operatorOptionsFor(field).map(option => option.value);
  if (!allowed.length) return;
  updateCondition(index, {
    field,
    operator: allowed.includes(current.operator) ? current.operator : allowed[0]!,
    value: initialValue(field, allowed.includes(current.operator) ? current.operator : allowed[0]!),
  });
}
function updateOperator(index: number, operator: FilterOperator): void {
  const current = props.nodes[index];
  if (!current || !isCondition(current)) return;
  updateCondition(index, { operator, value: initialValue(current.field, operator) });
}
function columnFor(field: string): ColumnSchema | undefined {
  return props.columns.find(item => item.name === field);
}
function filterInputFor(field: string): NonNullable<ColumnSchema["filterInput"]> {
  return columnFor(field)?.filterInput ?? "text";
}
function filterOptionsFor(field: string) {
  return [...(columnFor(field)?.filterOptions ?? [])];
}
function relationIdFor(field: string): string | null {
  const column = columnFor(field);
  if (column?.filterInput !== "relation") return null;
  if (column.relationId) return column.relationId;
  const fieldIdentity = column.fieldId ?? column.name;
  return props.relations?.find(relation => relation.fieldRef === fieldIdentity)?.relationId ?? null;
}
async function searchRelation(field: string, query: string): Promise<void> {
  const relationId = relationIdFor(field);
  if (!relationId || !props.searchRelationTargets) return;
  relationLoading.value = { ...relationLoading.value, [field]: true };
  try {
    const targets = await props.searchRelationTargets(relationId, query);
    relationOptions.value = {
      ...relationOptions.value,
      [field]: targets.map(target => ({ label: target.label, value: target.itemId })),
    };
  } finally {
    relationLoading.value = { ...relationLoading.value, [field]: false };
  }
}
function optionValue(node: FilterCondition): string | string[] | null {
  if (Array.isArray(node.value)) return node.value.map(value => String(value));
  if (typeof node.value === "string" || typeof node.value === "number") return String(node.value);
  return null;
}
function initialValue(field: string, operator: FilterOperator): unknown {
  if (operator === "is_null" || operator === "is_not_null") return undefined;
  if (operator === "between") return ["", ""];
  if (operator === "in") return [];
  if (columnFor(field)?.dataType === "boolean") return false;
  return "";
}
function parseScalar(field: string, value: string): unknown {
  const column = columnFor(field);
  if (column?.dataType === "boolean") return value === "true";
  const numeric = column?.dataType === "integer" || column?.dataType === "decimal";
  return numeric && value.trim() !== "" && Number.isFinite(Number(value))
    ? Number(value)
    : value;
}
function updateValue(index: number, value: string): void {
  const current = props.nodes[index];
  if (!current || !isCondition(current)) return;
  updateCondition(index, { value: parseScalar(current.field, value) });
}
function updateBetweenValue(index: number, part: number, value: string): void {
  const current = props.nodes[index];
  if (!current || !isCondition(current)) return;
  const previous = Array.isArray(current.value) ? [...current.value] : ["", ""];
  previous[part] = parseScalar(current.field, value);
  updateCondition(index, { value: previous.slice(0, 2) });
}
function updateDiscreteValues(index: number, values: Array<string | number>): void {
  const current = props.nodes[index];
  if (!current || !isCondition(current)) return;
  updateCondition(index, {
    value: values.map(value => typeof value === "string"
      ? parseScalar(current.field, value)
      : value),
  });
}
</script>

<template>
  <div class="filter-tree" :data-depth="depth">
    <div v-for="(node, index) in nodes" :key="index" class="filter-node">
      <template v-if="isCondition(node)">
        <span v-if="index === 0" class="joiner">当</span>
        <NSelect
          v-else
          size="tiny"
          class="joiner-select"
          :value="node.logic ?? 'AND'"
          :options="[{ label: '且', value: 'AND' }, { label: '或', value: 'OR' }]"
          aria-label="条件连接方式"
          @update:value="logic => updateLogic(index, logic)"
        />
        <NSelect
          size="small"
          class="field-select"
          :value="node.field"
          :options="fieldOptions"
          aria-label="筛选字段"
          @update:value="field => updateField(index, field)"
        />
        <NSelect
          size="small"
          class="operator-select"
          :value="node.operator"
          :options="operatorOptionsFor(node.field)"
          aria-label="筛选操作符"
          @update:value="operator => updateOperator(index, operator)"
        />
        <div v-if="node.operator === 'between'" class="between-inputs">
          <NDatePicker
            v-if="filterInputFor(node.field) === 'date' || filterInputFor(node.field) === 'dateTime'"
            size="small"
            :type="filterInputFor(node.field) === 'dateTime' ? 'datetime' : 'date'"
            :formatted-value="String(Array.isArray(node.value) ? (node.value[0] ?? '') : '') || null"
            :value-format="filterInputFor(node.field) === 'dateTime' ? 'yyyy-MM-dd HH:mm:ss' : 'yyyy-MM-dd'"
            aria-label="筛选起始值"
            @update:formatted-value="value => updateBetweenValue(index, 0, value ?? '')"
          />
          <NInputNumber
            v-else-if="filterInputFor(node.field) === 'number'"
            size="small"
            :value="Number(Array.isArray(node.value) ? node.value[0] : null)"
            :show-button="false"
            aria-label="筛选起始值"
            @update:value="value => updateBetweenValue(index, 0, String(value ?? ''))"
          />
          <NInput
            v-else
            size="small"
            :value="String(Array.isArray(node.value) ? (node.value[0] ?? '') : '')"
            aria-label="筛选起始值"
            @update:value="value => updateBetweenValue(index, 0, value)"
          />
          <span>至</span>
          <NDatePicker
            v-if="filterInputFor(node.field) === 'date' || filterInputFor(node.field) === 'dateTime'"
            size="small"
            :type="filterInputFor(node.field) === 'dateTime' ? 'datetime' : 'date'"
            :formatted-value="String(Array.isArray(node.value) ? (node.value[1] ?? '') : '') || null"
            :value-format="filterInputFor(node.field) === 'dateTime' ? 'yyyy-MM-dd HH:mm:ss' : 'yyyy-MM-dd'"
            aria-label="筛选结束值"
            @update:formatted-value="value => updateBetweenValue(index, 1, value ?? '')"
          />
          <NInputNumber
            v-else-if="filterInputFor(node.field) === 'number'"
            size="small"
            :value="Number(Array.isArray(node.value) ? node.value[1] : null)"
            :show-button="false"
            aria-label="筛选结束值"
            @update:value="value => updateBetweenValue(index, 1, String(value ?? ''))"
          />
          <NInput
            v-else
            size="small"
            :value="String(Array.isArray(node.value) ? (node.value[1] ?? '') : '')"
            aria-label="筛选结束值"
            @update:value="value => updateBetweenValue(index, 1, value)"
          />
        </div>
        <NSelect
          v-else-if="node.operator === 'in' && filterOptionsFor(node.field).length > 0"
          size="small"
          class="value-input"
          multiple
          :value="Array.isArray(node.value) ? node.value : []"
          :options="filterOptionsFor(node.field)"
          aria-label="筛选值列表"
          @update:value="(value: string[]) => updateDiscreteValues(index, value)"
        />
        <NDynamicTags
          v-else-if="node.operator === 'in' && filterInputFor(node.field) !== 'relation'"
          class="value-input"
          :value="Array.isArray(node.value) ? node.value.map(value => String(value)) : []"
          aria-label="筛选值列表"
          @update:value="(value: string[]) => updateDiscreteValues(index, value)"
        />
        <NSelect
          v-else-if="filterInputFor(node.field) === 'relation'"
          size="small"
          class="value-input"
          filterable
          remote
          clearable
          :multiple="node.operator === 'in'"
          :loading="relationLoading[node.field] === true"
          :value="optionValue(node)"
          :options="relationOptions[node.field] ?? []"
          aria-label="关联记录筛选值"
          @search="query => searchRelation(node.field, query)"
          @focus="searchRelation(node.field, '')"
          @update:value="value => updateCondition(index, { value })"
        />
        <NSelect
          v-else-if="columnFor(node.field)?.dataType === 'boolean' && node.operator !== 'is_null' && node.operator !== 'is_not_null'"
          size="small"
          class="value-input"
          :value="String(Boolean(node.value))"
          :options="[{ label: '是', value: 'true' }, { label: '否', value: 'false' }]"
          aria-label="布尔筛选值"
          @update:value="value => updateCondition(index, { value: value === 'true' })"
        />
        <NSelect
          v-else-if="filterInputFor(node.field) === 'select' || filterInputFor(node.field) === 'multiSelect'"
          size="small"
          class="value-input"
          :multiple="filterInputFor(node.field) === 'multiSelect'"
          :value="optionValue(node)"
          :options="filterOptionsFor(node.field)"
          aria-label="选项筛选值"
          @update:value="value => updateCondition(index, { value })"
        />
        <NDatePicker
          v-else-if="filterInputFor(node.field) === 'date' || filterInputFor(node.field) === 'dateTime'"
          size="small"
          class="value-input"
          :type="filterInputFor(node.field) === 'dateTime' ? 'datetime' : 'date'"
          :formatted-value="typeof node.value === 'string' ? node.value : null"
          :value-format="filterInputFor(node.field) === 'dateTime' ? 'yyyy-MM-dd HH:mm:ss' : 'yyyy-MM-dd'"
          aria-label="日期筛选值"
          @update:formatted-value="value => updateCondition(index, { value })"
        />
        <NInputNumber
          v-else-if="filterInputFor(node.field) === 'number'"
          size="small"
          class="value-input"
          :value="typeof node.value === 'number' ? node.value : null"
          :show-button="false"
          aria-label="数值筛选值"
          @update:value="value => updateCondition(index, { value })"
        />
        <NInput
          v-else-if="node.operator !== 'is_null' && node.operator !== 'is_not_null'"
          size="small"
          class="value-input"
          :value="String(node.value ?? '')"
          aria-label="筛选值"
          @update:value="value => updateValue(index, value)"
        />
        <NButton size="tiny" quaternary aria-label="删除筛选条件" @click="remove(index)">×</NButton>
      </template>
      <div v-else class="filter-group">
        <div class="filter-group__heading">
          <span>条件组</span>
          <NSelect
            size="tiny"
            :value="node.groupLogic ?? 'AND'"
            :options="[{ label: '全部满足', value: 'AND' }, { label: '任一满足', value: 'OR' }]"
            @update:value="groupLogic => replace(index, { ...node, groupLogic })"
          />
          <NButton size="tiny" quaternary aria-label="删除筛选条件组" @click="remove(index)">×</NButton>
        </div>
        <FilterTreeEditor
          :nodes="node.filters"
          :columns="columns"
          :depth="depth + 1"
          :total-conditions="conditionCount"
          :relations="relations"
          :search-relation-targets="searchRelationTargets"
          @update="filters => replace(index, { ...node, filters })"
        />
      </div>
    </div>
    <div class="filter-actions">
      <NButton size="tiny" secondary :disabled="!columns.length || atConditionLimit" @click="addCondition">＋ 条件</NButton>
      <NButton size="tiny" quaternary :disabled="depth >= 3 || !columns.length || atConditionLimit" @click="addGroup">＋ 条件组</NButton>
    </div>
  </div>
</template>

<style scoped>
.filter-tree { display: grid; gap: 7px; min-width: 430px; }
.filter-node { display: flex; align-items: center; gap: 6px; }
.joiner { width: 28px; flex: 0 0 auto; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); text-align: right; }
.joiner-select { width: 54px; flex: 0 0 auto; }
.field-select { width: 132px; }
.operator-select { width: 108px; }
.value-input { width: 132px; }
.between-inputs { display: grid; grid-template-columns: 92px auto 92px; align-items: center; gap: 5px; }
.filter-group { width: 100%; padding: 8px; border-left: 2px solid var(--vt-color-primary-300); border-radius: 0 var(--vt-radius-sm) var(--vt-radius-sm) 0; background: var(--vt-bg-subtle); }
.filter-group__heading { display: grid; grid-template-columns: 1fr 120px 28px; align-items: center; gap: 6px; margin-bottom: 8px; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.filter-actions { display: flex; gap: 6px; padding-left: 34px; }
@media (max-width: 720px) {
  .filter-tree { min-width: min(420px, calc(100vw - 48px)); }
  .filter-node { flex-wrap: wrap; }
  .value-input { margin-left: 34px; }
}
</style>
