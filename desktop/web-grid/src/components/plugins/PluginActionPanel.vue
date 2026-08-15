<script setup lang="ts">
import { computed, reactive, watch } from "vue";
import type { PluginTaskViewSnapshot, WebPluginActionDescription } from "@/contracts";

interface SchemaField {
  readonly type?: "string" | "number" | "integer" | "boolean";
  readonly title?: string;
  readonly description?: string;
  readonly default?: unknown;
  readonly enum?: readonly (string | number)[];
}

const props = withDefaults(defineProps<{
  description: WebPluginActionDescription;
  task?: PluginTaskViewSnapshot | null;
  showForm?: boolean;
  closable?: boolean;
}>(), {
  task: null,
  showForm: true,
  closable: true,
});
const emit = defineEmits<{
  start: [input: Readonly<Record<string, unknown>>];
  resolve: [decision: "approved" | "rejected"];
  cancel: [];
  close: [];
}>();
const form = reactive<Record<string, unknown>>({});

const fields = computed(() => {
  const properties = props.description.inputSchema.properties;
  if (typeof properties !== "object" || properties === null || Array.isArray(properties)) return [];
  const required = Array.isArray(props.description.inputSchema.required)
    ? props.description.inputSchema.required.filter((value): value is string => typeof value === "string")
    : [];
  return Object.entries(properties).flatMap(([name, value]) => {
    if (typeof value !== "object" || value === null || Array.isArray(value)) return [];
    return [{ name, schema: value as SchemaField, required: required.includes(name) }];
  });
});

const canStart = computed(() => fields.value.every((field) => {
  if (!field.required) return true;
  const value = form[field.name];
  return value !== undefined && value !== null && value !== "";
}));

function resetForm(): void {
  for (const key of Object.keys(form)) delete form[key];
  for (const field of fields.value) {
    form[field.name] = field.schema.default ?? (field.schema.type === "boolean" ? false : "");
  }
}

function updateValue(name: string, event: Event, type: SchemaField["type"]): void {
  const target = event.target as HTMLInputElement | HTMLSelectElement;
  if (type === "boolean") form[name] = (target as HTMLInputElement).checked;
  else if (type === "number" || type === "integer") form[name] = target.value === "" ? null : Number(target.value);
  else form[name] = target.value;
}

function start(): void {
  if (canStart.value) emit("start", { ...form });
}

watch(() => props.description, resetForm, { immediate: true });
</script>

<template>
  <aside class="action-panel" aria-label="插件动作">
    <header class="panel-header">
      <div>
        <span class="eyebrow">ACTION / {{ description.risk.toUpperCase() }}</span>
        <h2>{{ description.title }}</h2>
        <p v-if="description.description">{{ description.description }}</p>
      </div>
      <button v-if="closable !== false" data-testid="plugin-action-close" class="icon-button" type="button" aria-label="关闭动作面板" @click="emit('close')">×</button>
    </header>

    <form v-if="showForm !== false" class="action-form" @submit.prevent="start">
      <label v-for="field in fields" :key="field.name" class="field">
        <span>{{ field.schema.title ?? field.name }} <b v-if="field.required">*</b></span>
        <small v-if="field.schema.description">{{ field.schema.description }}</small>
        <input
          v-if="field.schema.type === 'boolean'"
          :data-testid="`plugin-field-${field.name}`"
          type="checkbox"
          :checked="Boolean(form[field.name])"
          @change="updateValue(field.name, $event, field.schema.type)"
        />
        <select
          v-else-if="field.schema.enum"
          :data-testid="`plugin-field-${field.name}`"
          :value="form[field.name]"
          @change="updateValue(field.name, $event, field.schema.type)"
        >
          <option value="">请选择</option>
          <option v-for="option in field.schema.enum" :key="String(option)" :value="option">{{ option }}</option>
        </select>
        <input
          v-else
          :data-testid="`plugin-field-${field.name}`"
          :type="field.schema.type === 'number' || field.schema.type === 'integer' ? 'number' : 'text'"
          :value="form[field.name] as string | number"
          @input="updateValue(field.name, $event, field.schema.type)"
        />
      </label>
      <button data-testid="plugin-action-start" class="primary-button" type="button" :disabled="!canStart" @click="start">开始执行</button>
    </form>

    <section v-if="task" class="task-card" aria-live="polite">
      <div class="task-heading">
        <div><span class="status-dot" :data-status="task.state"></span><strong>{{ task.state }}</strong></div>
        <code>{{ task.taskId }}</code>
      </div>
      <div class="progress-track"><i :style="{ width: `${task.progressPercent ?? 0}%` }"></i></div>
      <div class="task-meta"><span>{{ task.progressMessage ?? '等待运行时更新' }}</span><b>{{ task.progressPercent ?? 0 }}%</b></div>
      <button
        v-if="task.state === 'queued' || task.state === 'running'"
        data-testid="plugin-task-cancel"
        class="quiet-button"
        type="button"
        :disabled="task.cancelRequested"
        @click="emit('cancel')"
      >{{ task.cancelRequested ? '已请求取消，任务可能仍会完成' : '请求取消' }}</button>
    </section>

    <section v-if="task?.confirmation" data-testid="plugin-confirmation" class="confirmation" role="alertdialog" aria-modal="true">
      <span class="danger-kicker">FINAL {{ task.confirmation.risk.toUpperCase() }} CONFIRMATION</span>
      <h3>{{ task.confirmation.title }}</h3>
      <p>{{ task.confirmation.summary }}</p>
      <dl>
        <div><dt>目标数量</dt><dd>{{ task.confirmation.targetCount ?? '未知' }}</dd></div>
        <div><dt>确认失效</dt><dd>{{ task.confirmation.expiresAt }}</dd></div>
      </dl>
      <div class="confirm-actions">
        <button data-testid="plugin-confirm-reject" type="button" class="quiet-button" @click="emit('resolve', 'rejected')">拒绝</button>
        <button data-testid="plugin-confirm-approve" type="button" class="danger-button" @click="emit('resolve', 'approved')">确认并继续写入</button>
      </div>
    </section>

    <section v-if="task?.error" data-testid="plugin-task-error" class="task-error" role="alert">
      <span class="danger-kicker">SAFE ERROR / {{ task.error.code }}</span>
      <h3>{{ task.error.message }}</h3>
      <p>恢复方式：<code>{{ task.error.recoverability }}</code></p>
      <small v-if="task.error.causeId">诊断编号 {{ task.error.causeId }}</small>
    </section>

    <section v-if="task?.result" class="result-card">
      <span class="eyebrow">STRUCTURED RESULT / {{ task.result.status.toUpperCase() }}</span>
      <h3>{{ task.result.summary }}</h3>
      <div v-if="task.result.metrics.length" class="metrics">
        <div v-for="metric in task.result.metrics" :key="metric.label"><span>{{ metric.label }}</span><strong>{{ metric.value }}</strong></div>
      </div>
      <ul v-if="task.result.warnings.length" class="result-warnings"><li v-for="warning in task.result.warnings" :key="warning">{{ warning }}</li></ul>
      <pre v-if="task.result.table" class="result-table-wrap">{{ JSON.stringify(task.result.table, null, 2) }}</pre>
    </section>
  </aside>
</template>

<style scoped>
.action-panel { width: min(430px, 100%); height: 100%; overflow: auto; padding: 16px; color: var(--vt-fg); border-left: 1px solid var(--vt-border); background: var(--vt-bg); box-shadow: -12px 0 32px rgba(20, 28, 38, .06); }
.panel-header { display: flex; align-items: flex-start; justify-content: space-between; padding-bottom: 14px; border-bottom: 1px solid var(--vt-border); }
.eyebrow, .danger-kicker { color: var(--vt-fg-muted); font: 650 10px/1.2 ui-monospace, SFMono-Regular, Consolas, monospace; letter-spacing: .11em; }
.panel-header h2, .result-card h3 { margin: 7px 0 2px; font-size: 17px; }
.panel-header p { margin: 3px 0 0; color: var(--vt-fg-muted); }
.icon-button { width: 28px; height: 28px; color: var(--vt-fg-muted); border: 0; border-radius: 4px; background: transparent; cursor: pointer; }
.action-form { display: grid; gap: 12px; padding: 16px 0; }
.field { display: grid; gap: 5px; }
.field > span { font-weight: 600; }
.field b { color: var(--vt-color-danger); }
.field small { color: var(--vt-fg-muted); }
.field input:not([type="checkbox"]), .field select { width: 100%; height: 34px; padding: 0 9px; color: var(--vt-fg); border: 1px solid var(--vt-border); border-radius: 4px; background: var(--vt-bg); }
.field input[type="checkbox"] { width: 16px; height: 16px; accent-color: var(--vt-color-primary-500); }
.primary-button, .danger-button, .quiet-button { min-height: 32px; padding: 0 12px; border: 1px solid transparent; border-radius: 4px; font-weight: 600; cursor: pointer; }
.primary-button { color: #fff; background: var(--vt-color-primary-500); }
.primary-button:disabled, .quiet-button:disabled { opacity: .55; cursor: default; }
.task-card, .confirmation, .task-error, .result-card { margin-top: 12px; padding: 13px; border: 1px solid var(--vt-border); border-radius: 5px; background: var(--vt-bg-subtle); }
.task-heading, .task-heading div, .task-meta, .confirm-actions { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.task-heading code { color: var(--vt-fg-muted); font-size: 10px; }
.status-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--vt-color-warning); box-shadow: 0 0 0 3px color-mix(in srgb, var(--vt-color-warning) 15%, transparent); }
.status-dot[data-status="succeeded"] { background: var(--vt-color-success); }
.status-dot[data-status="failed"], .status-dot[data-status="aborted"] { background: var(--vt-color-danger); }
.progress-track { height: 4px; margin-top: 12px; overflow: hidden; background: var(--vt-border); }
.progress-track i { display: block; height: 100%; background: var(--vt-color-primary-500); transition: width 180ms var(--vt-ease); }
.task-meta { margin: 7px 0 10px; color: var(--vt-fg-muted); font-size: 11px; }
.quiet-button { color: var(--vt-fg); border-color: var(--vt-border); background: var(--vt-bg); }
.confirmation { border-left: 3px solid var(--vt-color-danger); background: color-mix(in srgb, var(--vt-color-danger) 6%, var(--vt-bg)); }
.danger-kicker { color: var(--vt-color-danger); }
.confirmation h3 { margin: 7px 0 4px; }
.confirmation p { margin: 0 0 10px; }
.confirmation dl { margin: 0 0 12px; font-size: 11px; }
.confirmation dl div { display: flex; justify-content: space-between; padding: 5px 0; border-top: 1px solid var(--vt-border); }
.confirmation dd { margin: 0; font-family: ui-monospace, SFMono-Regular, Consolas, monospace; }
.danger-button { color: #fff; background: var(--vt-color-danger); }
.task-error { color: var(--vt-color-danger); border-left: 3px solid var(--vt-color-danger); }
.task-error h3 { margin: 7px 0 5px; font-size: 14px; }
.task-error p { margin: 0 0 5px; color: var(--vt-fg); }
.task-error small { color: var(--vt-fg-muted); }
.metrics { display: grid; grid-template-columns: repeat(2, 1fr); gap: 1px; margin-top: 10px; background: var(--vt-border); }
.metrics div { display: grid; gap: 2px; padding: 8px; background: var(--vt-bg); }
.metrics span { color: var(--vt-fg-muted); font-size: 10px; }
.result-table-wrap { margin-top: 10px; overflow: auto; }
table { width: 100%; border-collapse: collapse; font-size: 11px; }
th, td { padding: 6px 7px; text-align: left; border-bottom: 1px solid var(--vt-border); }
th { color: var(--vt-fg-muted); font-family: ui-monospace, SFMono-Regular, Consolas, monospace; }
</style>
