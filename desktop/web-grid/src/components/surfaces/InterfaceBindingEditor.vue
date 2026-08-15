<script setup lang="ts">
import { computed } from "vue";
import { NButton, NInput, NInputNumber, NSelect } from "naive-ui";

import type {
  BindingVariable,
  DataBinding,
  ViewFilter,
  ViewSort,
} from "@/contracts/generated/workbench";

const props = defineProps<{
  binding: DataBinding;
  bindings: readonly DataBinding[];
}>();
const emit = defineEmits<{
  update: [binding: DataBinding];
  remove: [bindingId: string];
}>();

const fieldOptions = computed(() => props.binding.query.fields.map((field) => ({
  value: field,
  label: field,
})));
const bindingOptions = computed(() => props.bindings
  .filter((binding) => binding.bindingId !== props.binding.bindingId)
  .map((binding) => ({ value: binding.bindingId, label: binding.bindingId })));
const operatorOptions = [
  "eq", "ne", "contains", "startsWith", "gt", "gte", "lt", "lte", "isNull", "isNotNull",
].map((value) => ({ value, label: value }));
const directionOptions = [{ value: "asc", label: "升序" }, { value: "desc", label: "降序" }];
const sourceOptions = [
  { value: "literal", label: "固定值" },
  { value: "selectedRecordField", label: "所选记录字段" },
];

function replace(patch: Partial<DataBinding>): void {
  emit("update", { ...props.binding, ...patch });
}

function replaceQuery(patch: Partial<DataBinding["query"]>): void {
  replace({ query: { ...props.binding.query, ...patch } });
}

function updateFilter(index: number, patch: Partial<ViewFilter>): void {
  replaceQuery({
    filters: props.binding.query.filters.map((item, itemIndex) => (
      itemIndex === index ? { ...item, ...patch } : item
    )),
  });
}

function addFilter(): void {
  const fieldId = props.binding.query.fields[0];
  if (!fieldId) return;
  replaceQuery({
    filters: [...props.binding.query.filters, { fieldId, operator: "eq", value: null }],
  });
}

function removeFilter(index: number): void {
  replaceQuery({ filters: props.binding.query.filters.filter((_, itemIndex) => itemIndex !== index) });
}

function updateSort(index: number, patch: Partial<ViewSort>): void {
  replaceQuery({
    sorts: props.binding.query.sorts.map((item, itemIndex) => (
      itemIndex === index ? { ...item, ...patch } : item
    )),
  });
}

function addSort(): void {
  const fieldId = props.binding.query.fields[0];
  if (!fieldId) return;
  replaceQuery({ sorts: [...props.binding.query.sorts, { fieldId, direction: "asc" }] });
}

function removeSort(index: number): void {
  replaceQuery({ sorts: props.binding.query.sorts.filter((_, itemIndex) => itemIndex !== index) });
}

function updateVariable(index: number, patch: Partial<BindingVariable>): void {
  replace({
    variables: props.binding.variables.map((item, itemIndex) => (
      itemIndex === index ? { ...item, ...patch } : item
    )),
  });
}

function updateVariableSource(index: number, source: BindingVariable["source"]): void {
  updateVariable(index, source === "literal" ? {
    source,
    sourceBindingId: null,
    sourceFieldId: null,
  } : {
    source,
    sourceBindingId: bindingOptions.value[0]?.value ?? null,
    sourceFieldId: sourceFields(bindingOptions.value[0]?.value ?? null)[0]?.value ?? null,
  });
}

function updateVariableBinding(index: number, sourceBindingId: string | null): void {
  updateVariable(index, {
    sourceBindingId,
    sourceFieldId: sourceFields(sourceBindingId)[0]?.value ?? null,
  });
}

function addVariable(): void {
  const targetFieldId = props.binding.query.fields[0];
  if (!targetFieldId || props.binding.variables.length >= 32) return;
  replace({
    variables: [...props.binding.variables, {
      variableId: `variable-${crypto.randomUUID()}`,
      targetFieldId,
      operator: "eq",
      source: "literal",
      sourceBindingId: null,
      sourceFieldId: null,
      value: null,
    }],
  });
}

function removeVariable(index: number): void {
  replace({ variables: props.binding.variables.filter((_, itemIndex) => itemIndex !== index) });
}

function sourceFields(bindingId: string | null): { value: string; label: string }[] {
  return props.bindings.find((item) => item.bindingId === bindingId)?.query.fields.map((field) => ({
    value: field,
    label: field,
  })) ?? [];
}

function scalar(value: string): string | null {
  return value === "" ? null : value;
}
</script>

<template>
  <section class="binding-editor" :data-testid="`interface-binding-${binding.bindingId}`">
    <header>
      <div><strong>{{ binding.query.tableId }}</strong><small>{{ binding.bindingId }}</small></div>
      <NButton quaternary size="tiny" type="error" aria-label="删除绑定" @click="emit('remove', binding.bindingId)">删除</NButton>
    </header>
    <small class="fields">{{ binding.query.fields.join(" · ") }}</small>
    <label>每页记录数<NInputNumber size="small" :min="1" :max="500" :value="binding.query.pageSize" @update:value="replaceQuery({ pageSize: $event ?? 100 })" /></label>

    <div class="editor-group">
      <div class="group-title"><strong>筛选</strong><NButton text size="tiny" @click="addFilter">添加</NButton></div>
      <div v-for="(filter, index) in binding.query.filters" :key="index" class="query-row">
        <NSelect size="tiny" :value="filter.fieldId" :options="fieldOptions" @update:value="updateFilter(index, { fieldId: $event })" />
        <NSelect size="tiny" :value="filter.operator" :options="operatorOptions" @update:value="updateFilter(index, { operator: $event })" />
        <NInput v-if="!['isNull','isNotNull'].includes(filter.operator)" size="tiny" :value="filter.value === null ? '' : String(filter.value)" placeholder="值" @update:value="updateFilter(index, { value: scalar($event) })" />
        <span v-else class="no-value">无需值</span>
        <NButton text size="tiny" type="error" aria-label="删除筛选" @click="removeFilter(index)">×</NButton>
      </div>
    </div>

    <div class="editor-group">
      <div class="group-title"><strong>排序</strong><NButton text size="tiny" @click="addSort">添加</NButton></div>
      <div v-for="(sort, index) in binding.query.sorts" :key="index" class="query-row compact">
        <NSelect size="tiny" :value="sort.fieldId" :options="fieldOptions" @update:value="updateSort(index, { fieldId: $event })" />
        <NSelect size="tiny" :value="sort.direction" :options="directionOptions" @update:value="updateSort(index, { direction: $event })" />
        <NButton text size="tiny" type="error" aria-label="删除排序" @click="removeSort(index)">×</NButton>
      </div>
    </div>

    <div class="editor-group">
      <div class="group-title"><strong>运行时变量</strong><NButton text size="tiny" :disabled="binding.variables.length >= 32" @click="addVariable">添加</NButton></div>
      <div v-for="(variable, index) in binding.variables" :key="variable.variableId" class="variable-row">
        <NSelect size="tiny" :value="variable.targetFieldId" :options="fieldOptions" @update:value="updateVariable(index, { targetFieldId: $event })" />
        <NSelect size="tiny" :value="variable.operator" :options="operatorOptions" @update:value="updateVariable(index, { operator: $event })" />
        <NSelect :data-testid="`variable-source-${index}`" size="tiny" :value="variable.source" :options="sourceOptions" @update:value="updateVariableSource(index, $event)" />
        <NInput v-if="variable.source === 'literal'" size="tiny" :value="variable.value === null ? '' : String(variable.value)" placeholder="固定值" @update:value="updateVariable(index, { value: scalar($event) })" />
        <template v-else>
          <NSelect size="tiny" :value="variable.sourceBindingId" :options="bindingOptions" placeholder="源绑定" @update:value="updateVariableBinding(index, $event)" />
          <NSelect size="tiny" :value="variable.sourceFieldId" :options="sourceFields(variable.sourceBindingId)" placeholder="源字段" @update:value="updateVariable(index, { sourceFieldId: $event })" />
        </template>
        <NButton text size="tiny" type="error" aria-label="删除变量" @click="removeVariable(index)">×</NButton>
      </div>
    </div>
  </section>
</template>

<style scoped>
.binding-editor { display:grid; gap:8px; margin-top:9px; padding:9px; border:1px solid var(--vt-border); border-radius:8px; background:var(--vt-bg-sunken); }
.binding-editor>header,.group-title { display:flex; align-items:center; justify-content:space-between; gap:6px; }.binding-editor>header div { display:grid; min-width:0; }.binding-editor>header strong,.binding-editor>header small,.fields { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }.fields { color:var(--vt-fg-muted); font-size:10px; }.binding-editor>label { display:grid; gap:4px; color:var(--vt-fg-muted); font-size:10px; }.editor-group { display:grid; gap:5px; padding-top:7px; border-top:1px solid var(--vt-border); }.group-title strong { font-size:10px; letter-spacing:.04em; }.query-row { display:grid; grid-template-columns:1fr 1fr 1fr auto; gap:4px; }.query-row.compact { grid-template-columns:1fr 1fr auto; }.variable-row { display:grid; grid-template-columns:1fr 1fr; gap:4px; padding:5px; border-radius:5px; background:var(--vt-bg); }.variable-row>button { justify-self:end; }.no-value { align-self:center; color:var(--vt-fg-muted); font-size:9px; }
</style>
