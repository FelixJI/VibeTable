<script setup lang="ts">
import { computed } from "vue";
import { NButton, NInput, NSpin } from "naive-ui";
import type { InterfaceAction, InterfaceElement } from "@/contracts/generated/workbench";
import type { SurfaceBindingData } from "@/surfaces/surfaceRuntime";

defineOptions({ name: "InterfaceRuntimeElement" });
const props = defineProps<{
  element: InterfaceElement;
  data: Readonly<Record<string, SurfaceBindingData>>;
  selected: Readonly<Record<string, Readonly<Record<string, unknown>> | null>>;
  forms: Readonly<Record<string, Readonly<Record<string, unknown>>>>;
  bindingFields: Readonly<Record<string, readonly string[]>>;
  actionKinds: Readonly<Record<string, InterfaceAction["kind"]>>;
}>();
const emit = defineEmits<{
  select: [bindingId: string, row: Readonly<Record<string, unknown>>];
  action: [actionId: string, bindingId: string | null, elementId: string];
  form: [elementId: string, field: string, value: unknown];
}>();

const binding = computed(() => props.element.bindingId
  ? props.data[props.element.bindingId] ?? null
  : null);
const rows = computed(() => binding.value?.rows ?? []);
const selectedRow = computed(() => props.element.bindingId
  ? props.selected[props.element.bindingId] ?? rows.value[0] ?? null
  : null);
const fields = computed(() => {
  if (!props.element.bindingId) return [];
  return props.bindingFields[props.element.bindingId] ?? [];
});
const actionKind = computed(() => props.element.actionId
  ? props.actionKinds[props.element.actionId] ?? null
  : null);
const form = computed(() => props.forms[props.element.elementId]
  ?? (actionKind.value === "record.create" ? {} : selectedRow.value)
  ?? {});
const metric = computed(() => {
  const row = rows.value[0];
  if (!row) return "—";
  const field = fields.value[0];
  return field ? format(row[field]) : rows.value.length;
});
const chartBars = computed(() => rows.value.slice(0, 8).map((row, index) => {
  const field = fields.value.find((name) => typeof row[name] === "number");
  const value = field ? Number(row[field]) : index + 1;
  return { label: format(row[fields.value[0] ?? ""]), value, width: Math.max(8, Math.min(100, Math.abs(value))) };
}));

function format(value: unknown): string {
  if (value === null || value === undefined || value === "") return "—";
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}
function forwardSelect(bindingId: string, row: Readonly<Record<string, unknown>>): void {
  emit("select", bindingId, row);
}
function forwardAction(actionId: string, bindingId: string | null, elementId: string): void {
  emit("action", actionId, bindingId, elementId);
}
function forwardForm(elementId: string, field: string, value: unknown): void {
  emit("form", elementId, field, value);
}
</script>

<template>
  <section
    class="runtime-element"
    :class="[`runtime-element--${element.kind}`, `runtime-width--${element.width}`]"
    :data-testid="`interface-runtime-${element.elementId}`"
  >
    <template v-if="['section', 'columns', 'tabs'].includes(element.kind)">
      <header v-if="element.text" class="element-heading">{{ element.text }}</header>
      <div class="element-children" :class="`children--${element.kind}`">
        <InterfaceRuntimeElement
          v-for="child in element.children"
          :key="child.elementId"
          :element="child"
          :data="data"
          :selected="selected"
          :forms="forms"
          :binding-fields="bindingFields"
          :action-kinds="actionKinds"
          @select="forwardSelect"
          @action="forwardAction"
          @form="forwardForm"
        />
      </div>
    </template>

    <p v-else-if="element.kind === 'text'" class="display-text">{{ element.text }}</p>

    <div v-else-if="binding?.state === 'loading'" class="element-state"><NSpin size="small" />正在加载数据</div>
    <div v-else-if="binding?.state === 'failed'" class="element-state element-state--error" role="alert">
      {{ binding.error }}
    </div>

    <div v-else-if="element.kind === 'metric'" class="metric-card">
      <small>{{ element.text || fields[0] || "指标" }}</small><strong>{{ metric }}</strong>
    </div>

    <div v-else-if="element.kind === 'chart'" class="chart-card" role="img" :aria-label="element.text || '数据图表'">
      <strong>{{ element.text || "概览" }}</strong>
      <div v-for="bar in chartBars" :key="bar.label" class="chart-row">
        <span>{{ bar.label }}</span><i :style="{ width: `${bar.width}%` }"></i><b>{{ bar.value }}</b>
      </div>
      <p v-if="chartBars.length === 0" class="empty-copy">暂无可视化数据</p>
    </div>

    <div v-else-if="element.kind === 'record-list'" class="record-list" role="region" :aria-label="element.text || '记录列表'">
      <header>{{ element.text || "记录" }}<span>{{ rows.length }}</span></header>
      <div class="record-table" role="table">
        <div class="record-row record-row--head" role="row">
          <b v-for="field in fields" :key="field" role="columnheader">{{ field }}</b>
        </div>
        <button
          v-for="(row, index) in rows"
          :key="String(row.rowKey ?? row.id ?? index)"
          type="button"
          class="record-row"
          :class="{ selected: selectedRow === row }"
          role="row"
          @click="element.bindingId && emit('select', element.bindingId, row)"
        >
          <span v-for="field in fields" :key="field" role="cell">{{ format(row[field]) }}</span>
        </button>
      </div>
      <p v-if="rows.length === 0" class="empty-copy">暂无记录</p>
    </div>

    <dl v-else-if="element.kind === 'record-detail'" class="record-detail">
      <div v-for="field in fields" :key="field"><dt>{{ field }}</dt><dd>{{ format(selectedRow?.[field]) }}</dd></div>
      <p v-if="!selectedRow" class="empty-copy">选择一条记录查看详情</p>
    </dl>

    <form
      v-else-if="element.kind === 'form'"
      class="record-form"
      @submit.prevent="element.actionId && emit('action', element.actionId, element.bindingId, element.elementId)"
    >
      <strong>{{ element.text || "记录表单" }}</strong>
      <label v-for="field in fields" :key="field">
        <span>{{ field }}</span>
        <NInput
          :value="String(form[field] ?? '')"
          @update:value="emit('form', element.elementId, field, $event)"
        />
      </label>
      <NButton type="primary" attr-type="submit" :disabled="!element.actionId">提交</NButton>
    </form>

    <NButton
      v-else-if="element.kind === 'button' || element.kind === 'navigation'"
      :type="element.kind === 'navigation' ? 'default' : 'primary'"
      :disabled="!element.actionId"
      @click="element.actionId && emit('action', element.actionId, element.bindingId, element.elementId)"
    >
      {{ element.text || (element.kind === "navigation" ? "前往页面" : "执行操作") }}
    </NButton>
  </section>
</template>

<style scoped>
.runtime-element { min-width:0; }
.runtime-width--full { grid-column:span 12; }
.runtime-width--half { grid-column:span 6; }
.runtime-width--third { grid-column:span 4; }
.element-heading { margin-bottom:10px; font-size:13px; font-weight:700; letter-spacing:.01em; }
.element-children { display:grid; grid-template-columns:repeat(12,minmax(0,1fr)); gap:12px; }
.runtime-element--section { padding:16px; border:1px solid var(--vt-border); border-radius:12px; background:var(--vt-bg); box-shadow:0 1px 2px rgb(15 23 42/.04); }
.runtime-element--columns > .element-children { align-items:start; }
.runtime-element--tabs { padding:12px; border-left:3px solid var(--vt-color-primary-500); background:var(--vt-bg-subtle); }
.display-text { margin:0; padding:6px 2px; color:var(--vt-fg); line-height:1.6; white-space:pre-wrap; }
.element-state { display:flex; min-height:86px; align-items:center; justify-content:center; gap:8px; color:var(--vt-fg-muted); }
.element-state--error { color:var(--vt-color-danger); }
.metric-card,.chart-card,.record-list,.record-detail,.record-form { border:1px solid var(--vt-border); border-radius:10px; background:var(--vt-bg); }
.metric-card { display:flex; min-height:104px; flex-direction:column; justify-content:center; padding:18px; }
.metric-card small { color:var(--vt-fg-muted); }.metric-card strong { font-size:30px; font-variant-numeric:tabular-nums; }
.chart-card { padding:14px; }.chart-row { display:grid; grid-template-columns:90px 1fr 48px; align-items:center; gap:8px; margin-top:8px; font-size:12px; }
.chart-row span { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }.chart-row i { height:7px; border-radius:5px; background:linear-gradient(90deg,var(--vt-color-primary-500),var(--vt-color-primary-300)); }.chart-row b { text-align:right; }
.record-list { overflow:hidden; }.record-list > header { display:flex; justify-content:space-between; padding:11px 13px; border-bottom:1px solid var(--vt-border); font-weight:700; }.record-list > header span { color:var(--vt-fg-muted); font-weight:500; }
.record-table { overflow:auto; }.record-row { display:grid; width:100%; grid-template-columns:repeat(auto-fit,minmax(110px,1fr)); border:0; border-bottom:1px solid var(--vt-border-subtle,var(--vt-border)); background:transparent; color:inherit; text-align:left; }
.record-row:not(.record-row--head) { cursor:pointer; }.record-row:not(.record-row--head):hover,.record-row.selected { background:var(--vt-bg-subtle); }.record-row>* { overflow:hidden; padding:8px 11px; text-overflow:ellipsis; white-space:nowrap; }.record-row--head { background:var(--vt-bg-subtle); font-size:11px; text-transform:uppercase; }
.record-detail { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:0; overflow:hidden; }.record-detail div { padding:11px 13px; border-bottom:1px solid var(--vt-border); }.record-detail dt { color:var(--vt-fg-muted); font-size:11px; }.record-detail dd { margin:3px 0 0; }
.record-form { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:12px; padding:16px; }.record-form>strong,.record-form>button { grid-column:1/-1; }.record-form label { display:grid; gap:5px; font-size:12px; }
.empty-copy { margin:18px; color:var(--vt-fg-muted); text-align:center; }
@media (max-width:720px) { .runtime-width--half,.runtime-width--third { grid-column:span 12; }.record-form,.record-detail { grid-template-columns:1fr; } }
</style>
