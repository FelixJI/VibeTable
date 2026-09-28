<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { NAlert, NButton, NInput, NSelect, NSpin, NTag } from "naive-ui";
import type { SelectOption } from "naive-ui";
import { Braces, ChevronLeft, FunctionSquare, PencilLine, Plus, Search } from "@lucide/vue";
import type { FormulaDraftValidationResult, FormulaFunctionInfo } from "@/contracts";
import type {
  FormulaAuthorDocument,
  FormulaAuthorToken,
  FormulaTextRange,
} from "@/contracts/generated/workbench";
import type { FormulaDraftDiagnostic, FormulaDraftValidateRequest } from "./formulaDraftRequest";
import {
  applySourceEdit,
  emptyFormulaAuthorDocument,
  formatTextRange,
  insertPlainText,
  insertReference,
  textRangeToSelection,
} from "./tokenDocument";

type FormulaDefinition = NonNullable<
  import("@/contracts/schemaV2").FieldDraftV2["formula"]
>;

interface FormulaTableOption {
  readonly tableId: string;
  readonly label: string;
}

interface FormulaSourceTable {
  readonly tableId: string;
  readonly fields: readonly FormulaFieldOption[];
}

interface FormulaFieldOption extends SelectOption {
  readonly label: string;
  /** Stable Schema V2 field identity; the only name the editor persists. */
  readonly fieldId: string;
  readonly dataType: string;
}

interface FormulaRelationOption extends SelectOption {
  readonly label: string;
  readonly fieldId: string;
  readonly many: boolean;
  readonly targetFields: readonly FormulaFieldOption[];
}

const props = defineProps<{
  value: FormulaDefinition;
  localFields: readonly FormulaFieldOption[];
  relations: readonly FormulaRelationOption[];
  /** Selectable source tables for TABLE(...) references; main owns loading. */
  tables?: readonly FormulaTableOption[];
  /** Field list for the currently loaded source table, matched by tableId. */
  sourceTable?: FormulaSourceTable | null;
  sourceTableLoading?: boolean;
  resultType?: string | null;
  authorDocument?: FormulaAuthorDocument | null;
  functions?: readonly FormulaFunctionInfo[];
  validation?: FormulaDraftValidationResult | null;
  validatedSource?: string;
  validatedDocumentRevision?: number | null;
  validating?: boolean;
  error?: string | null;
  diagnostic?: FormulaDraftDiagnostic | null;
  previewValue?: unknown;
  previewReady?: boolean;
  previewing?: boolean;
  previewError?: string | null;
  previewNote?: string | null;
}>();
const emit = defineEmits<{
  commit: [value: FormulaDefinition];
  validate: [request: FormulaDraftValidateRequest];
  loadTable: [tableId: string];
}>();

const rootRef = ref<HTMLElement | null>(null);
const editing = ref(false);
const waitingRestore = ref(false);
const workingSource = ref("");
const workingDocument = ref<FormulaAuthorDocument>(emptyFormulaAuthorDocument());
const selectedField = ref<string | null>(null);
const selectedRelation = ref<string | null>(null);
const selectedTarget = ref<string | null>(null);
const selectedDirectRelation = ref<string | null>(null);
const selectedDirectTarget = ref<string | null>(null);
const selectedTable = ref<string | null>(null);
const selectedSourceField = ref<string | null>(null);
const selectedAggregate = ref("SUM");
const functionSearch = ref("");
const functionCategory = ref<string | null>(null);
const selectedFunction = ref<FormulaFunctionInfo | null>(null);
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
  value: field.fieldId,
})));
const relationOptions = computed(() => props.relations.map(relation => ({
  label: `${relation.label}${relation.many ? " · 多条" : " · 单条"}`,
  value: relation.fieldId,
})));
const directRelationOptions = computed(() => props.relations
  .filter(relation => !relation.many)
  .map(relation => ({ label: relation.label, value: relation.fieldId })));
const activeDirectRelation = computed(() => props.relations.find(
  relation => !relation.many && relation.fieldId === selectedDirectRelation.value,
));
const directTargetOptions = computed(() => (activeDirectRelation.value?.targetFields ?? [])
  .map(field => ({ label: field.label, value: field.fieldId })));
const tableOptions = computed(() => (props.tables ?? []).map(table => ({
  label: table.label,
  value: table.tableId,
})));
const selectedTableOption = computed(() => (props.tables ?? []).find(
  table => table.tableId === selectedTable.value,
) ?? null);
const sourceTableReady = computed(() => !!selectedTable.value
  && props.sourceTable?.tableId === selectedTable.value);
const sourceFieldOptions = computed(() => (sourceTableReady.value
  ? props.sourceTable?.fields ?? []
  : []
).map(field => ({ label: `${field.label} · ${field.dataType}`, value: field.fieldId })));
const activeRelation = computed(() => props.relations.find(
  relation => relation.fieldId === selectedRelation.value,
));
const targetOptions = computed(() => (activeRelation.value?.targetFields ?? [])
  .filter(field => selectedAggregate.value === "COUNTA" || isNumericType(field.dataType))
  .map(field => ({ label: field.label, value: field.fieldId })));
const targetRequired = computed(() => selectedAggregate.value !== "COUNT");
const canInsertAggregate = computed(() => !!activeRelation.value
  && (!targetRequired.value || !!selectedTarget.value));
const functionCategories = computed(() => {
  const categories = new Set((props.functions ?? [])
    .map(item => item.category)
    .filter(category => category.length > 0));
  return [...categories].sort((a, b) => a.localeCompare(b, "zh-Hans"));
});
const functionCategoryOptions = computed(() => [
  { label: "全部分类", value: "" },
  ...functionCategories.value.map(category => ({ label: category, value: category })),
]);
const filteredFunctions = computed(() => {
  const keyword = functionSearch.value.trim().toLowerCase();
  const category = functionCategory.value;
  return (props.functions ?? []).filter(item =>
    (!category || item.category === category)
    && (!keyword
      || item.name.toLowerCase().includes(keyword)
      || item.category.toLowerCase().includes(keyword)
      || item.description.toLowerCase().includes(keyword)
      || item.signature.toLowerCase().includes(keyword)));
});
const summarySource = computed(() => props.authorDocument?.displaySource ?? "");
const inferredType = computed(() => {
  const resultType = props.validation?.resultType ?? props.resultType ?? "待推断";
  const elementType = props.validation?.resultElementType;
  // List results surface their element type, e.g. number[].
  return elementType && resultType === "json" ? `${elementType}[]` : resultType;
});
const sourceIsCurrent = computed(() => props.validatedSource === workingSource.value);
const canCommit = computed(() => editing.value
  && workingSource.value.trim().length > 0
  && !waitingRestore.value
  && !props.validating
  && !props.error
  && !!props.validation
  && sourceIsCurrent.value
  && (props.validatedDocumentRevision == null
    || props.validatedDocumentRevision === workingDocument.value.documentRevision));

watch(
  () => props.value,
  () => {
    if (editing.value) return;
    if (props.authorDocument) adoptDocument(props.authorDocument);
  },
  { deep: true },
);
watch(() => props.authorDocument, document => {
  if (!editing.value || !document) return;
  if (waitingRestore.value) {
    waitingRestore.value = false;
    adoptDocument(document);
    return;
  }
  // Adopt backend token refreshes (renamed labels) only for the exact text.
  if (props.validatedSource === document.displaySource
    && document.displaySource === workingSource.value) {
    adoptDocument(document);
  }
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
  clearValidationTimer();
  emit("validate", { kind: "invalidate" });
});

function adoptDocument(document: FormulaAuthorDocument): void {
  workingSource.value = document.displaySource;
  workingDocument.value = document;
}

function beginEditing(): void {
  editing.value = true;
  selectedFunction.value = null;
  functionSearch.value = "";
  functionCategory.value = null;
  if (props.authorDocument) {
    adoptDocument(props.authorDocument);
    return;
  }
  if (props.value.source) {
    waitingRestore.value = true;
    workingSource.value = "";
    workingDocument.value = emptyFormulaAuthorDocument();
    emit("validate", {
      kind: "restore",
      displaySource: props.value.source,
      adoptDocument: true,
    });
    return;
  }
  workingSource.value = "";
  workingDocument.value = emptyFormulaAuthorDocument();
}

function cancel(): void {
  clearValidationTimer();
  emit("validate", { kind: "invalidate", discardDocument: true });
  editing.value = false;
  waitingRestore.value = false;
  if (props.authorDocument) adoptDocument(props.authorDocument);
  else {
    workingSource.value = "";
    workingDocument.value = emptyFormulaAuthorDocument();
  }
  if (props.value.source) {
    emit("validate", { kind: "restore", displaySource: props.value.source, adoptDocument: true });
  }
}

function commit(): void {
  if (!canCommit.value || !props.validation) return;
  // Persist the sidecar-validated canonical source; display names never
  // reach storage, so renames cannot re-resolve a saved formula. Hosts that
  // predate the language field keep committing as cel-v1.
  emit("commit", {
    language: props.validation.language ?? "cel-v1",
    source: props.validation.canonicalSource,
  });
  editing.value = false;
}

function onSourceInput(value: string): void {
  const previous = workingSource.value;
  workingSource.value = value;
  workingDocument.value = applySourceEdit(workingDocument.value, previous, value);
  // Stale validation and previews die with the first keystroke, not later.
  emit("validate", { kind: "invalidate" });
  scheduleValidate();
}

function scheduleValidate(): void {
  clearValidationTimer();
  if (!workingSource.value.trim()) return;
  validationTimer = setTimeout(() => {
    validationTimer = null;
    emit("validate", {
      kind: "document",
      displaySource: workingSource.value,
      authorDocument: workingDocument.value,
    });
  }, 250);
}

function clearValidationTimer(): void {
  if (validationTimer !== null) clearTimeout(validationTimer);
  validationTimer = null;
}

function activeTextarea(): HTMLTextAreaElement | null {
  return rootRef.value?.querySelector<HTMLTextAreaElement>(
    '[data-testid="formula-source"] textarea',
  ) ?? null;
}

function currentSelection(): { start: number; end: number } {
  const textarea = activeTextarea();
  const fallback = workingSource.value.length;
  if (!textarea) return { start: fallback, end: fallback };
  return {
    start: textarea.selectionStart ?? fallback,
    end: textarea.selectionEnd ?? fallback,
  };
}

function applyInsertion(
  insertion: ReturnType<typeof insertReference>,
): void {
  workingSource.value = insertion.source;
  workingDocument.value = insertion.document;
  const caret = insertion.caret;
  void nextTick(() => {
    const textarea = activeTextarea();
    if (!textarea) return;
    textarea.focus();
    textarea.setSelectionRange(caret, caret);
  });
  emit("validate", { kind: "invalidate" });
  scheduleValidate();
}

function fieldToken(fieldId: string): FormulaAuthorToken {
  return { range: zeroRange(), kind: "field", fieldId, relationFieldId: null, targetFieldId: null };
}

function relationToken(fieldId: string): FormulaAuthorToken {
  return {
    range: zeroRange(),
    kind: "relation",
    fieldId,
    relationFieldId: fieldId,
    targetFieldId: null,
  };
}

function relationTargetToken(relationFieldId: string, targetFieldId: string): FormulaAuthorToken {
  return {
    range: zeroRange(),
    kind: "relationTarget",
    fieldId: targetFieldId,
    relationFieldId,
    targetFieldId,
  };
}

function tableToken(tableId: string): FormulaAuthorToken {
  return {
    range: zeroRange(),
    kind: "table",
    fieldId: null,
    tableId,
    relationFieldId: null,
    targetFieldId: null,
  };
}

function sourceFieldToken(tableId: string, fieldId: string): FormulaAuthorToken {
  return {
    range: zeroRange(),
    kind: "sourceField",
    fieldId,
    tableId,
    relationFieldId: null,
    targetFieldId: null,
  };
}

function zeroRange() {
  return { start: { line: 0, character: 0 }, end: { line: 0, character: 0 } };
}

function onLocalFieldSelect(value: string | null): void {
  selectedField.value = value;
  if (value) insertLocalField(value);
}

function insertLocalField(fieldId: string): void {
  const field = props.localFields.find(item => item.fieldId === fieldId);
  if (!field) return;
  const label = `{${field.label}}`;
  const selection = currentSelection();
  applyInsertion(insertReference(
    workingDocument.value,
    workingSource.value,
    selection.start,
    selection.end,
    {
      text: label,
      token: fieldToken(field.fieldId),
      labelStart: 0,
      labelLength: label.length,
    },
  ));
  selectedField.value = null;
}

function insertAggregate(): void {
  const relation = activeRelation.value;
  if (!relation || !canInsertAggregate.value) return;
  const selection = currentSelection();
  if (selectedAggregate.value === "COUNT") {
    const label = `{${relation.label}}`;
    const text = `COUNT(${label})`;
    applyInsertion(insertReference(
      workingDocument.value,
      workingSource.value,
      selection.start,
      selection.end,
      {
        text,
        token: relationToken(relation.fieldId),
        labelStart: "COUNT(".length,
        labelLength: label.length,
      },
    ));
    return;
  }
  const target = relation.targetFields.find(field => field.fieldId === selectedTarget.value);
  if (!target) return;
  const path = `{${relation.label}}.{${target.label}}`;
  const prefix = `${selectedAggregate.value}(`;
  applyInsertion(insertReference(
    workingDocument.value,
    workingSource.value,
    selection.start,
    selection.end,
    {
      text: `${prefix}${path})`,
      token: relationTargetToken(relation.fieldId, target.fieldId),
      labelStart: prefix.length,
      labelLength: path.length,
    },
  ));
}

function insertDirectRelationField(): void {
  const relation = activeDirectRelation.value;
  const target = relation?.targetFields.find(
    field => field.fieldId === selectedDirectTarget.value,
  );
  if (!relation || !target) return;
  const path = `{${relation.label}}.{${target.label}}`;
  const selection = currentSelection();
  applyInsertion(insertReference(
    workingDocument.value,
    workingSource.value,
    selection.start,
    selection.end,
    {
      text: path,
      token: relationTargetToken(relation.fieldId, target.fieldId),
      labelStart: 0,
      labelLength: path.length,
    },
  ));
}

function onTableSelect(value: string | null): void {
  selectedTable.value = value;
  selectedSourceField.value = null;
  if (value) emit("loadTable", value);
}

function insertTableReference(): void {
  const table = selectedTableOption.value;
  if (!table) return;
  const label = `{${table.label}}`;
  const selection = currentSelection();
  applyInsertion(insertReference(
    workingDocument.value,
    workingSource.value,
    selection.start,
    selection.end,
    {
      text: `TABLE(${label})`,
      token: tableToken(table.tableId),
      labelStart: "TABLE(".length,
      labelLength: label.length,
    },
  ));
}

function insertSourceFieldReference(): void {
  const tableId = selectedTable.value;
  if (!sourceTableReady.value || !tableId) return;
  const field = props.sourceTable?.fields.find(
    item => item.fieldId === selectedSourceField.value,
  );
  if (!field) return;
  const label = `{${field.label}}`;
  const selection = currentSelection();
  applyInsertion(insertReference(
    workingDocument.value,
    workingSource.value,
    selection.start,
    selection.end,
    {
      text: `CurrentValue.${label}`,
      token: sourceFieldToken(tableId, field.fieldId),
      labelStart: "CurrentValue.".length,
      labelLength: label.length,
    },
  ));
  selectedSourceField.value = null;
}

function insertFunctionCall(info: FormulaFunctionInfo): void {
  selectedFunction.value = info;
  const selection = currentSelection();
  const text = `${info.name}(`;
  applyInsertion(insertPlainText(
    workingDocument.value,
    workingSource.value,
    selection.start,
    selection.end,
    text,
  ));
}

function insertFunctionExample(info: FormulaFunctionInfo): void {
  selectedFunction.value = info;
  const selection = currentSelection();
  applyInsertion(insertPlainText(
    workingDocument.value,
    workingSource.value,
    selection.start,
    selection.end,
    info.example,
  ));
}

function locateDiagnostic(range: FormulaTextRange): void {
  const selection = textRangeToSelection(workingSource.value, range);
  if (!selection) return;
  void nextTick(() => {
    const textarea = activeTextarea();
    if (!textarea) return;
    textarea.focus();
    textarea.setSelectionRange(selection.start, selection.end);
  });
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
  <article ref="rootRef" class="specialized-editor" data-testid="formula-field-editor">
    <template v-if="!editing">
      <div class="editor-summary">
        <span class="editor-mark"><Braces :size="18" /></span>
        <div>
          <span class="eyebrow">FORMULA WORKBENCH</span>
          <strong>可视化公式</strong>
          <small data-testid="formula-summary-source">
            {{ summarySource || (value.source ? "正在恢复公式文本…" : "尚未配置公式") }}
          </small>
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
          :value="workingSource"
          type="textarea"
          :autosize="{ minRows: 5, maxRows: 12 }"
          placeholder="例如：SUM({明细}.{金额}) + {运费}"
          data-testid="formula-source"
          :disabled="waitingRestore"
          @update:value="onSourceInput(String($event ?? ''))"
        />
        <small>字段引用使用展示名并由系统按稳定 ID 绑定；保存后持久化为永久字段名称。</small>
      </label>

      <div v-if="waitingRestore" class="validating" data-testid="formula-restore-loading">
        <NSpin size="small" />正在恢复公式文本…
      </div>

      <div class="insert-grid">
        <section class="insert-card">
          <div><Plus :size="15" /><strong>插入当前表字段</strong></div>
          <NSelect
            :value="selectedField"
            :options="localFieldOptions"
            filterable
            placeholder="选择字段"
            data-testid="formula-local-field"
            @update:value="onLocalFieldSelect"
          />
          <small>当前行字段：取本表当前记录的字段值。</small>
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
        <section v-if="tables?.length" class="insert-card source-card">
          <div><Braces :size="15" /><strong>跨表来源（TABLE + CurrentValue）</strong></div>
          <NSelect
            :value="selectedTable"
            :options="tableOptions"
            filterable
            placeholder="选择来源表"
            data-testid="formula-source-table"
            @update:value="onTableSelect"
          />
          <NButton
            size="small"
            secondary
            :disabled="!selectedTableOption"
            data-testid="formula-insert-table"
            @click="insertTableReference"
          >插入 TABLE(来源表)</NButton>
          <div
            v-if="selectedTable && sourceTableLoading"
            class="validating"
            data-testid="formula-source-table-loading"
          ><NSpin size="small" />正在加载来源表字段…</div>
          <template v-else-if="selectedTable && sourceTableReady">
            <NSelect
              v-model:value="selectedSourceField"
              :options="sourceFieldOptions"
              filterable
              placeholder="选择来源记录字段"
              data-testid="formula-source-field"
            />
            <NButton
              size="small"
              secondary
              :disabled="!selectedSourceField"
              data-testid="formula-insert-source-field"
              @click="insertSourceFieldReference"
            >插入 CurrentValue.字段</NButton>
          </template>
          <small v-else-if="selectedTable">来源表字段尚未加载。</small>
          <small>
            来源记录字段来自所选表：先插入 TABLE(来源表)，再用 CurrentValue.字段 引用该表记录，
            放置是否合法由公式引擎校验；“插入当前表字段”引用当前行字段。
          </small>
        </section>
        <section class="insert-card function-card">
          <div><Search :size="15" /><strong>函数目录</strong></div>
          <NInput
            v-model:value="functionSearch"
            clearable
            placeholder="离线检索函数名、分类或说明"
            data-testid="formula-function-search"
          />
          <NSelect
            v-model:value="functionCategory"
            :options="functionCategoryOptions"
            placeholder="全部分类"
            data-testid="formula-function-category"
          />
          <div class="function-list" data-testid="formula-function-list">
            <button
              v-for="item in filteredFunctions"
              :key="item.name"
              type="button"
              class="function-item"
              :class="{ active: selectedFunction?.name === item.name }"
              data-testid="formula-function-option"
              @click="insertFunctionCall(item)"
            >
              <strong>{{ item.name }}</strong>
              <small>{{ item.signature }}</small>
            </button>
            <p v-if="!filteredFunctions.length" class="function-empty">没有匹配的函数</p>
          </div>
          <div v-if="selectedFunction" class="function-detail" data-testid="formula-function-detail">
            <strong>{{ selectedFunction.signature }}</strong>
            <p>{{ selectedFunction.description }}</p>
            <code data-testid="formula-function-example">{{ selectedFunction.example }}</code>
            <NButton
              size="tiny"
              secondary
              data-testid="formula-function-insert-example"
              @click="insertFunctionExample(selectedFunction)"
            >在光标处插入示例</NButton>
          </div>
        </section>
      </div>

      <NAlert
        v-if="error"
        type="error"
        :show-icon="false"
        data-testid="formula-validation-error"
      >
        <div class="error-body">
          <span>{{ error }}</span>
          <button
            v-if="diagnostic?.range"
            type="button"
            class="range-link"
            data-testid="formula-error-range"
            @click="locateDiagnostic(diagnostic.range)"
          >定位 {{ formatTextRange(diagnostic.range) }}</button>
        </div>
      </NAlert>
      <NAlert v-else-if="validation && sourceIsCurrent" type="success" :show-icon="false">
        公式有效 · {{ validation.resultType }} · {{ validation.dependencies.length }} 个直接依赖
      </NAlert>
      <div v-else-if="validating" class="validating"><NSpin size="small" />正在校验公式…</div>

      <NAlert
        v-if="previewing && sourceIsCurrent"
        type="info"
        :show-icon="false"
        data-testid="formula-preview-loading"
      ><NSpin size="small" /> 正在计算当前表第一条记录的样例结果…</NAlert>
      <NAlert
        v-else-if="previewError && sourceIsCurrent"
        type="warning"
        :show-icon="false"
        data-testid="formula-preview-error"
      >样例计算失败：{{ previewError }}</NAlert>
      <NAlert
        v-else-if="previewReady && sourceIsCurrent"
        type="info"
        :show-icon="false"
        data-testid="formula-preview-value"
      >样例结果：<code>{{ formatPreviewValue(previewValue) }}</code></NAlert>
      <NAlert
        v-else-if="previewNote && !previewing"
        type="default"
        :show-icon="false"
        data-testid="formula-preview-note"
      >{{ previewNote }}</NAlert>

      <div class="editor-actions">
        <small>跨表引用使用 TABLE(来源表) 与 CurrentValue.字段；多值结果需沿 Relation 聚合或用 FILTER/PROJECT 等函数处理，合法性由公式引擎校验。</small>
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
.editor-summary,.editor-heading,.editor-actions,.insert-card>div:first-child,.validating {
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
.source-card { grid-column: 1 / -1; grid-template-columns: repeat(2, minmax(0, 1fr)); }
.source-card>div, .source-card>.validating, .source-card small { grid-column: 1 / -1; }
.function-card { grid-column: 1 / -1; }
.function-card>div:first-child { grid-column: 1 / -1; }
.function-list {
  display: grid; max-height: 168px; gap: 6px; overflow-y: auto;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
}
.function-item {
  display: grid; gap: 2px; padding: 7px 9px; text-align: left; cursor: pointer;
  border: 1px solid var(--vt-border); border-radius: 8px;
  background: var(--vt-bg-elevated); color: inherit;
}
.function-item.active { border-color: #8b5cf6; }
.function-item small { color: var(--vt-fg-muted); overflow: hidden; text-overflow: ellipsis; }
.function-empty, .function-detail p { color: var(--vt-fg-muted); margin: 0; }
.function-detail {
  display: grid; gap: 6px; padding: 9px; border-radius: 8px;
  border: 1px dashed var(--vt-border); justify-items: start;
}
.function-detail code {
  padding: 4px 7px; border-radius: 6px; font-size: 11px;
  background: color-mix(in srgb, #8b5cf6 10%, var(--vt-bg-subtle));
}
.error-body { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.range-link {
  border: none; border-radius: 6px; padding: 2px 8px; cursor: pointer;
  color: inherit; text-decoration: underline;
  background: color-mix(in srgb, #8b5cf6 12%, transparent);
}
label { display: flex; flex-direction: column; gap: 7px; font-size: 12px; font-weight: 650; }
.eyebrow { color: #7c3aed; font-size: 9px; font-weight: 800; letter-spacing: .14em; }
small,.validating { color: var(--vt-fg-muted); }
@media(max-width:720px) {
  .insert-grid,.aggregate-card,.direct-card,.source-card { grid-template-columns: 1fr; }
  .aggregate-card>div,.direct-card>div { grid-column: auto; }
}
</style>
