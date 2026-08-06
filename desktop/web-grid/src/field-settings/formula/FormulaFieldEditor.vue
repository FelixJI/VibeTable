<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { NAlert, NButton, NInput, NSelect, NSpin, NTag } from "naive-ui";
import type { SelectOption } from "naive-ui";
import { Braces, ChevronLeft, FunctionSquare, PencilLine, Plus } from "lucide-vue-next";
import type {
  FieldDraftV2,
  FormulaDraftValidationResult,
} from "@/contracts";

type FormulaDefinition = NonNullable<FieldDraftV2["formula"]>;

interface FormulaFieldOption extends SelectOption {
  readonly label: string;
  readonly canonicalName: string;
  readonly dataType: string;
}

interface FormulaRelationOption extends SelectOption {
  readonly label: string;
  readonly canonicalName: string;
  readonly many: boolean;
  readonly targetFields: readonly FormulaFieldOption[];
}

const props = defineProps<{
  value: FormulaDefinition;
  localFields: readonly FormulaFieldOption[];
  relations: readonly FormulaRelationOption[];
  resultType?: string | null;
  validation?: FormulaDraftValidationResult | null;
  validatedSource?: string;
  validating?: boolean;
  error?: string | null;
  previewValue?: unknown;
  previewReady?: boolean;
  previewing?: boolean;
  previewError?: string | null;
  previewNote?: string | null;
}>();
const emit = defineEmits<{
  commit: [value: FormulaDefinition];
  validate: [displaySource: string];
}>();

const editing = ref(false);
const workingSource = ref("");
const selectedField = ref<string | null>(null);
const selectedRelation = ref<string | null>(null);
const selectedTarget = ref<string | null>(null);
const selectedDirectRelation = ref<string | null>(null);
const selectedDirectTarget = ref<string | null>(null);
const selectedAggregate = ref("SUM");
let validationTimer: ReturnType<typeof setTimeout> | null = null;

const aggregateOptions = [
  { label: "求和 SUM", value: "SUM" },
  { label: "平均值 AVERAGE", value: "AVERAGE" },
  { label: "最小值 MIN", value: "MIN" },
  { label: "最大值 MAX", value: "MAX" },
  { label: "记录数 COUNT", value: "COUNT" },
  { label: "非空数 COUNTA", value: "COUNTA" },
];
const localFieldOptions = computed(() => props.localFields.map(field => ({
  label: field.label,
  value: field.canonicalName,
})));
const relationOptions = computed(() => props.relations.map(relation => ({
  label: `${relation.label}${relation.many ? " · 多条" : " · 单条"}`,
  value: relation.canonicalName,
})));
const directRelationOptions = computed(() => props.relations
  .filter(relation => !relation.many)
  .map(relation => ({ label: relation.label, value: relation.canonicalName })));
const activeDirectRelation = computed(() => props.relations.find(
  relation => !relation.many && relation.canonicalName === selectedDirectRelation.value,
));
const directTargetOptions = computed(() => (activeDirectRelation.value?.targetFields ?? [])
  .map(field => ({ label: field.label, value: field.canonicalName })));
const activeRelation = computed(() => props.relations.find(
  relation => relation.canonicalName === selectedRelation.value,
));
const targetOptions = computed(() => (activeRelation.value?.targetFields ?? [])
  .filter(field => selectedAggregate.value === "COUNTA" || isNumericType(field.dataType))
  .map(field => ({ label: field.label, value: field.canonicalName })));
const targetRequired = computed(() => selectedAggregate.value !== "COUNT");
const canInsertAggregate = computed(() => !!activeRelation.value
  && (!targetRequired.value || !!selectedTarget.value));
const canCommit = computed(() => workingSource.value.trim().length > 0
  && !props.validating
  && !props.error
  && props.validatedSource === workingSource.value.trim()
  && !!props.validation);
const summarySource = computed(() => projectCanonicalSource(
  props.value.source,
  props.localFields,
  props.relations,
));
const inferredType = computed(() => props.validation?.resultType ?? props.resultType ?? "待推断");

watch(
  () => props.value,
  value => {
    if (!editing.value) {
      workingSource.value = projectCanonicalSource(
        value.source, props.localFields, props.relations,
      );
    }
  },
  { deep: true },
);
watch(workingSource, source => {
  if (!editing.value) return;
  if (validationTimer !== null) clearTimeout(validationTimer);
  validationTimer = null;
  if (!source.trim()) return;
  validationTimer = setTimeout(() => emit("validate", source.trim()), 250);
});
watch(selectedAggregate, () => {
  selectedTarget.value = null;
});
watch(selectedRelation, () => {
  selectedTarget.value = null;
});
watch(selectedDirectRelation, () => {
  selectedDirectTarget.value = null;
});
onBeforeUnmount(() => {
  if (validationTimer !== null) clearTimeout(validationTimer);
});

function beginEditing(): void {
  workingSource.value = summarySource.value;
  editing.value = true;
  if (workingSource.value.trim()) emit("validate", workingSource.value.trim());
}

function cancel(): void {
  workingSource.value = summarySource.value;
  editing.value = false;
}

function commit(): void {
  if (!canCommit.value || !props.validation) return;
  emit("commit", {
    language: "cel-v1",
    source: workingSource.value.trim(),
  });
  editing.value = false;
}

function insertLocalField(canonicalName: string): void {
  const field = props.localFields.find(item => item.canonicalName === canonicalName);
  if (!field) return;
  appendExpression(`{${field.label}}`);
  selectedField.value = null;
}

function insertAggregate(): void {
  const relation = activeRelation.value;
  if (!relation || !canInsertAggregate.value) return;
  if (selectedAggregate.value === "COUNT") {
    appendExpression(`COUNT({${relation.label}})`);
  } else {
    const target = relation.targetFields.find(
      field => field.canonicalName === selectedTarget.value,
    );
    if (!target) return;
    appendExpression(`${selectedAggregate.value}({${relation.label}}.{${target.label}})`);
  }
}

function insertDirectRelationField(): void {
  const relation = activeDirectRelation.value;
  const target = relation?.targetFields.find(
    field => field.canonicalName === selectedDirectTarget.value,
  );
  if (!relation || !target) return;
  appendExpression(`{${relation.label}}.{${target.label}}`);
}

function appendExpression(expression: string): void {
  const current = workingSource.value;
  workingSource.value = current && !/\s$/u.test(current)
    ? `${current} ${expression}`
    : `${current}${expression}`;
}

function projectCanonicalSource(
  source: string,
  fields: readonly FormulaFieldOption[],
  relations: readonly FormulaRelationOption[],
): string {
  let projected = source;
  const aggregateNames: Record<string, string> = {
    relationSum: "SUM",
    relationAverage: "AVERAGE",
    relationMin: "MIN",
    relationMax: "MAX",
    relationCountValues: "COUNTA",
  };
  projected = projected.replace(
    /\b(relationSum|relationAverage|relationMin|relationMax|relationCountValues)\(\s*([a-z][a-z0-9_]*)\s*,\s*"([a-z][a-z0-9_]*)"\s*\)/gu,
    (match, fn: string, relationName: string, targetName: string) => {
      const relation = relations.find(item => item.canonicalName === relationName);
      const target = relation?.targetFields.find(item => item.canonicalName === targetName);
      return relation && target ? `${aggregateNames[fn]}({${relation.label}}.{${target.label}})` : match;
    },
  );
  projected = projected.replace(
    /\brelationCount\(\s*([a-z][a-z0-9_]*)\s*\)/gu,
    (match, relationName: string) => {
      const relation = relations.find(item => item.canonicalName === relationName);
      return relation ? `COUNT({${relation.label}})` : match;
    },
  );
  for (const relation of relations) {
    for (const target of relation.targetFields) {
      projected = projected.replace(
        new RegExp(`\\b${escapeRegExp(relation.canonicalName)}\\.${escapeRegExp(target.canonicalName)}\\b`, "gu"),
        `{${relation.label}}.{${target.label}}`,
      );
    }
  }
  const names = new Map<string, string>();
  for (const field of fields) names.set(field.canonicalName, `{${field.label}}`);
  for (const relation of relations) names.set(relation.canonicalName, `{${relation.label}}`);
  return replaceIdentifiersOutsideStrings(projected, names);
}

function replaceIdentifiersOutsideStrings(source: string, names: ReadonlyMap<string, string>): string {
  let result = "";
  let inString = false;
  let escaped = false;
  for (let index = 0; index < source.length;) {
    const character = source[index]!;
    if (character === "\\" && inString) {
      result += character;
      escaped = !escaped;
      index += 1;
      continue;
    }
    if (character === '"') {
      if (!escaped) inString = !inString;
      escaped = false;
      result += character;
      index += 1;
      continue;
    }
    escaped = false;
    if (!inString && /[a-z_]/u.test(character)) {
      let end = index + 1;
      while (end < source.length && /[a-z0-9_]/u.test(source[end]!)) end += 1;
      const identifier = source.slice(index, end);
      result += names.get(identifier)
        ?? (/^f_[a-z0-9_]{8,}$/u.test(identifier) ? "#REF!" : identifier);
      index = end;
      continue;
    }
    result += character;
    index += 1;
  }
  return result;
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/gu, "\\$&");
}

function isNumericType(value: string): boolean {
  return value === "number" || value === "integer" || value === "decimal" || value === "float";
}

function formatPreviewValue(value: unknown): string {
  if (value === null) return "null";
  if (value === undefined) return "—";
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}
</script>

<template>
  <article class="specialized-editor" data-testid="formula-field-editor">
    <template v-if="!editing">
      <div class="editor-summary">
        <span class="editor-mark"><Braces :size="18" /></span>
        <div>
          <span class="eyebrow">FORMULA WORKBENCH</span>
          <strong>可视化公式</strong>
          <small>{{ summarySource || "尚未配置公式" }}</small>
        </div>
        <NTag size="small" :bordered="false">{{ inferredType }} · 自动</NTag>
      </div>
      <NButton secondary data-testid="formula-editor-entry" @click="beginEditing">
        <PencilLine :size="15" />进入公式工作台
      </NButton>
    </template>

    <template v-else>
      <div class="editor-heading">
        <NButton quaternary size="small" data-testid="formula-editor-cancel" @click="cancel">
          <ChevronLeft :size="15" />返回字段设置
        </NButton>
        <NTag size="small" :bordered="false">结果 {{ inferredType }} · 自动推断</NTag>
      </div>

      <label>
        <span>公式</span>
        <NInput
          v-model:value="workingSource"
          type="textarea"
          :autosize="{ minRows: 5, maxRows: 12 }"
          placeholder="例如：SUM({明细}.{金额}) + {运费}"
          data-testid="formula-source"
        />
        <small>字段引用使用展示名；保存后由系统转换为永久字段名称。</small>
      </label>

      <div class="insert-grid">
        <section class="insert-card">
          <div><Plus :size="15" /><strong>插入当前表字段</strong></div>
          <NSelect
            v-model:value="selectedField"
            :options="localFieldOptions"
            placeholder="选择字段"
            data-testid="formula-local-field"
            @update:value="$event && insertLocalField(String($event))"
          />
        </section>
        <section class="insert-card aggregate-card">
          <div><FunctionSquare :size="15" /><strong>沿 Relation 聚合</strong></div>
          <NSelect
            v-model:value="selectedAggregate"
            :options="aggregateOptions"
            data-testid="formula-aggregate-function"
          />
          <NSelect
            v-model:value="selectedRelation"
            :options="relationOptions"
            placeholder="选择关联字段"
            data-testid="formula-relation-field"
          />
          <NSelect
            v-if="targetRequired"
            v-model:value="selectedTarget"
            :options="targetOptions"
            placeholder="选择目标字段"
            data-testid="formula-target-field"
          />
          <NButton size="small" secondary :disabled="!canInsertAggregate" @click="insertAggregate">
            插入聚合
          </NButton>
        </section>
        <section v-if="directRelationOptions.length" class="insert-card direct-card">
          <div><Braces :size="15" /><strong>引用单条关联字段</strong></div>
          <NSelect
            v-model:value="selectedDirectRelation"
            :options="directRelationOptions"
            placeholder="选择单条关联"
            data-testid="formula-direct-relation"
          />
          <NSelect
            v-model:value="selectedDirectTarget"
            :options="directTargetOptions"
            placeholder="选择目标字段"
            data-testid="formula-direct-target"
          />
          <NButton
            size="small"
            secondary
            :disabled="!selectedDirectRelation || !selectedDirectTarget"
            @click="insertDirectRelationField"
          >插入引用</NButton>
        </section>
      </div>

      <NAlert v-if="error" type="error" :show-icon="false">{{ error }}</NAlert>
      <NAlert v-else-if="validation && validatedSource === workingSource.trim()" type="success" :show-icon="false">
        公式有效 · {{ validation.resultType }} · {{ validation.dependencies.length }} 个直接依赖
      </NAlert>
      <div v-else-if="validating" class="validating"><NSpin size="small" />正在由 sidecar 校验…</div>

      <NAlert
        v-if="previewing"
        type="info"
        :show-icon="false"
        data-testid="formula-preview-loading"
      ><NSpin size="small" /> 正在计算当前表第一条记录的样例结果…</NAlert>
      <NAlert
        v-else-if="previewError"
        type="warning"
        :show-icon="false"
        data-testid="formula-preview-error"
      >样例计算失败：{{ previewError }}</NAlert>
      <NAlert
        v-else-if="previewReady"
        type="info"
        :show-icon="false"
        data-testid="formula-preview-value"
      >样例结果：<code>{{ formatPreviewValue(previewValue) }}</code></NAlert>
      <NAlert
        v-else-if="previewNote"
        type="default"
        :show-icon="false"
        data-testid="formula-preview-note"
      >{{ previewNote }}</NAlert>

      <div class="editor-actions">
        <small>多值跨表计算必须沿 Relation 使用聚合函数；不允许任意扫描其他表。</small>
        <NButton
          type="primary"
          :disabled="!canCommit"
          data-testid="formula-editor-commit"
          @click="commit"
        >确认公式</NButton>
      </div>
    </template>
  </article>
</template>

<style scoped>
.specialized-editor {
  display: grid;
  gap: 14px;
  padding: 15px;
  border: 1px solid color-mix(in srgb, #8b5cf6 38%, var(--vt-border));
  border-radius: 13px;
  background:
    radial-gradient(circle at 90% 0, color-mix(in srgb, #8b5cf6 12%, transparent), transparent 38%),
    linear-gradient(145deg, color-mix(in srgb, #8b5cf6 7%, transparent), transparent 58%),
    var(--vt-bg-elevated);
}
.editor-summary,.editor-heading,.editor-actions,.insert-card>div,.validating {
  display: flex;
  align-items: center;
  gap: 10px;
}
.editor-summary>div { display: flex; flex: 1; min-width: 0; flex-direction: column; gap: 3px; }
.editor-summary small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.editor-mark {
  display: grid; width: 38px; height: 38px; place-items: center;
  border-radius: 11px; color: #7c3aed;
  background: color-mix(in srgb, #8b5cf6 14%, var(--vt-bg-subtle));
}
.editor-heading,.editor-actions { justify-content: space-between; }
.insert-grid { display: grid; grid-template-columns: minmax(0, .8fr) minmax(0, 1.2fr); gap: 12px; }
.insert-card {
  display: grid; align-content: start; gap: 9px; padding: 12px;
  border: 1px solid var(--vt-border); border-radius: 10px;
  background: color-mix(in srgb, var(--vt-bg-subtle) 75%, transparent);
}
.aggregate-card { grid-template-columns: repeat(2, minmax(0, 1fr)); }
.aggregate-card>div { grid-column: 1 / -1; }
.direct-card { grid-column: 1 / -1; grid-template-columns: repeat(3, minmax(0, 1fr)); }
.direct-card>div { grid-column: 1 / -1; }
label { display: flex; flex-direction: column; gap: 7px; font-size: 12px; font-weight: 650; }
.eyebrow { color: #7c3aed; font-size: 9px; font-weight: 800; letter-spacing: .14em; }
small,.validating { color: var(--vt-fg-muted); }
@media(max-width:720px) {
  .insert-grid,.aggregate-card,.direct-card { grid-template-columns: 1fr; }
  .aggregate-card>div,.direct-card>div { grid-column: auto; }
}
</style>
