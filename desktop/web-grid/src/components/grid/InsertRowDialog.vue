<script setup lang="ts">
/**
 * InsertRowDialog — 必填新行的应用内取值表单。仅在存在必填列
 * （nullable=false 且 editable）时由宿主打开；emit("submit") 后由宿主
 * await mutationService.insertRow(values)，成功后关闭（失败保持打开并
 * 通过 error 呈现权威拒绝原因，输入不丢失）。值编辑/校验复用
 * grid/editorFactory 的 parseValue + validateLocally，不造占位值。
 */
import { computed, reactive, watch } from "vue";
import { NButton, NInput, NInputNumber, NModal, NSelect, NSwitch } from "naive-ui";
import type { ColumnEditSchema, ColumnSchema } from "@/contracts";
import { parseValue, validateLocally } from "@/grid/editorFactory";
import { t } from "@/i18n";

const props = defineProps<{
  readonly show: boolean;
  readonly columns: readonly ColumnEditSchema[];
  readonly displayColumns: readonly ColumnSchema[];
  readonly busy: boolean;
  readonly disabled: boolean;
  readonly error: string | null;
}>();

const emit = defineEmits<{
  submit: [values: Readonly<Record<string, unknown>>];
  close: [];
}>();

/** 每列草稿值；undefined=未填。boolean 恒有值（false 是合法已提供值）。 */
const draft = reactive<Record<string, unknown>>({});

watch(
  () => props.columns,
  (columns) => {
    const names = new Set(columns.map(column => column.name));
    for (const key of Object.keys(draft)) {
      if (!names.has(key)) delete draft[key];
    }
    for (const column of columns) {
      if (!(column.name in draft)) {
        draft[column.name] = column.editor.kind === "boolean" ? false : undefined;
      }
    }
  },
  { immediate: true },
);

function title(column: ColumnEditSchema): string {
  return props.displayColumns.find(item => item.name === column.name)?.title ?? column.name;
}

function blank(column: ColumnEditSchema): boolean {
  if (column.editor.kind === "boolean") return false;
  const value = draft[column.name];
  return value === undefined || value === null || value === ""
    || Array.isArray(value) && value.length === 0;
}

function issueFor(column: ColumnEditSchema): string | null {
  if (blank(column)) return t("schema.required");
  const result = validateLocally(
    column.editor,
    column.validation,
    draft[column.name],
    false,
  );
  return result.ok ? null : result.error ?? t("insertRow.invalid");
}

const issues = computed(() => new Map(
  props.columns.map(column => [column.name, issueFor(column)]),
));
const canSubmit = computed(() =>
  !props.busy && !props.disabled && [...issues.value.values()].every(issue => issue === null));

/** 原生 datetime-local 值 → 权威 UTC RFC3339 存储；无效输入按必填缺失拦截。 */
function dateStorageValue(column: ColumnEditSchema, raw: unknown): string | null {
  if (typeof raw !== "string" || !raw) return null;
  if (column.editor.kind !== "date" || column.editor.dateType !== "datetime") return raw;
  const instant = new Date(raw);
  return Number.isNaN(instant.getTime()) ? null : instant.toISOString();
}

function submit(): void {
  if (!canSubmit.value) return;
  const values: Record<string, unknown> = {};
  for (const column of props.columns) {
    const raw = draft[column.name];
    if (column.editor.kind === "date") {
      values[column.name] = dateStorageValue(column, raw);
      continue;
    }
    values[column.name] = typeof raw === "string" ? parseValue(column.editor, raw) : raw;
  }
  emit("submit", values);
}

function selectOptions(column: ColumnEditSchema): { label: string; value: string }[] {
  const options = column.editor.kind === "single_select" || column.editor.kind === "multi_select"
    ? column.editor.options
    : [];
  return options.map(option => ({ label: String(option), value: String(option) }));
}

function nativeDateInput(column: ColumnEditSchema): string {
  if (column.editor.kind !== "date") return "date";
  if (column.editor.dateType === "time") return "time";
  return column.editor.dateType === "datetime" ? "datetime-local" : "date";
}

/** 原生控件值：datetime 保留完整本地日期时间串，date 仅日期。 */
function nativeDateValue(raw: unknown): string {
  return typeof raw === "string" ? raw : "";
}
</script>

<template>
  <NModal
    :show="props.show && props.columns.length > 0"
    :mask-closable="false"
    :close-on-esc="false"
  >
    <div
      class="insert-row-dialog"
      data-testid="insert-row-dialog"
      role="dialog"
      :aria-label="t('insertRow.title')"
    >
      <header>
        <strong>{{ t('insertRow.title') }}</strong>
        <small>{{ t('insertRow.hint') }}</small>
      </header>
      <div class="fields">
        <div
          v-for="column in props.columns"
          :key="column.name"
          class="field-row"
          :data-testid="`insert-row-field-${column.name}`"
        >
          <label>
            <span>{{ title(column) }}<b v-if="issues.get(column.name)" class="issue">*</b></span>
            <NInput
              v-if="column.editor.kind === 'text'"
              v-model:value="draft[column.name] as string | undefined"
              :data-testid="`insert-row-input-${column.name}`"
              :type="column.editor.multiline ? 'textarea' : 'text'"
              :autosize="column.editor.multiline ? { minRows: 2, maxRows: 5 } : undefined"
              :maxlength="column.editor.maxLength ?? undefined"
              :disabled="props.busy || props.disabled"
            />
            <NInputNumber
              v-else-if="column.editor.kind === 'number'"
              :value="typeof draft[column.name] === 'number' ? (draft[column.name] as number) : null"
              :data-testid="`insert-row-input-${column.name}`"
              :disabled="props.busy || props.disabled"
              clearable
              @update:value="value => { draft[column.name] = value; }"
            />
            <NSwitch
              v-else-if="column.editor.kind === 'boolean'"
              :value="Boolean(draft[column.name])"
              :data-testid="`insert-row-input-${column.name}`"
              :disabled="props.busy || props.disabled"
              @update:value="value => { draft[column.name] = value; }"
            />
            <!-- 日期/日期时间/时间用原生控件选择，不做 RFC3339 手工输入；
                 datetime 在提交时转换为权威 UTC RFC3339。 -->
            <input
              v-else-if="column.editor.kind === 'date' && column.editor.dateType !== 'time'"
              :type="nativeDateInput(column)"
              :data-testid="`insert-row-input-${column.name}`"
              class="native-date"
              :disabled="props.busy || props.disabled"
              :value="nativeDateValue(draft[column.name])"
              @input="event => { draft[column.name] = (event.target as HTMLInputElement).value; }"
            />
            <input
              v-else-if="column.editor.kind === 'date'"
              type="time"
              step="1"
              :data-testid="`insert-row-input-${column.name}`"
              class="native-date"
              :disabled="props.busy || props.disabled"
              :value="nativeDateValue(draft[column.name])"
              @input="event => { draft[column.name] = (event.target as HTMLInputElement).value; }"
            />
            <NSelect
              v-else-if="column.editor.kind === 'single_select'"
              :value="typeof draft[column.name] === 'string' ? (draft[column.name] as string) : null"
              :data-testid="`insert-row-input-${column.name}`"
              :options="selectOptions(column)"
              :disabled="props.busy || props.disabled"
              clearable
              @update:value="value => { draft[column.name] = value; }"
            />
            <NSelect
              v-else-if="column.editor.kind === 'multi_select'"
              :value="Array.isArray(draft[column.name]) ? (draft[column.name] as string[]) : []"
              :data-testid="`insert-row-input-${column.name}`"
              :options="selectOptions(column)"
              :disabled="props.busy || props.disabled"
              multiple
              clearable
              @update:value="value => { draft[column.name] = value; }"
            />
            <NInput
              v-else
              v-model:value="draft[column.name] as string | undefined"
              type="textarea"
              :data-testid="`insert-row-input-${column.name}`"
              :autosize="{ minRows: 2, maxRows: 6 }"
              :disabled="props.busy || props.disabled"
            />
          </label>
          <small v-if="issues.get(column.name)" class="issue" :data-testid="`insert-row-issue-${column.name}`">
            {{ issues.get(column.name) }}
          </small>
        </div>
      </div>
      <p v-if="props.error" class="error" data-testid="insert-row-error">{{ props.error }}</p>
      <footer>
        <NButton size="small" data-testid="insert-row-cancel" :disabled="props.busy" @click="emit('close')">{{ t('common.cancel') }}</NButton>
        <NButton
          size="small"
          type="primary"
          data-testid="insert-row-submit"
          :disabled="!canSubmit"
          :loading="props.busy"
          @click="submit"
        >
          {{ t('insertRow.submit') }}
        </NButton>
      </footer>
    </div>
  </NModal>
</template>

<style scoped>
.insert-row-dialog {
  display: grid;
  gap: 12px;
  max-height: calc(100vh - 32px);
  overflow: auto;
  width: min(460px, calc(100vw - 32px));
  padding: 18px;
  border-radius: 14px;
  background: var(--vt-bg-elevated);
}
header { display: grid; gap: 4px; }
header strong { font-size: 16px; }
.fields { display: grid; gap: 12px; }
.field-row { display: grid; gap: 5px; }
.field-row label { display: grid; gap: 6px; font-size: 12px; font-weight: 650; }
.native-date {
  height: 34px;
  padding: 0 10px;
  color: var(--vt-fg);
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-md);
  background: var(--vt-bg);
}
.issue { color: var(--vt-color-danger, #dc2626); }
.error { margin: 0; padding: 8px 10px; border-radius: 8px; color: var(--vt-color-danger, #dc2626); background: color-mix(in srgb, #dc2626 8%, transparent); font-size: 12px; }
footer { display: flex; justify-content: flex-end; gap: 8px; }
</style>
