<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { NAlert, NButton, NInput, NInputNumber, NSelect, NSpin, NSwitch, NTag } from "naive-ui";
import type { SelectOption } from "naive-ui";
import { ArrowRight, ChevronLeft, Funnel, GitBranch, Minus, PencilLine, Plus, X } from "@lucide/vue";
import type { FieldDraftV2 } from "@/contracts";
import {
  CONDITION_OPERATOR_LABELS,
  LOOKUP_CONDITION_MAX_RULES,
  conditionOperators,
  dateTimeLocalToRfc3339,
  isConditionLogicalType,
  isNullOperator,
  isValidDateValue,
  isValidDateTimeLocalValue,
  rfc3339ToDateTimeLocal,
  type LookupConditionFieldOption,
} from "./lookupCondition";

type LookupDefinition = NonNullable<FieldDraftV2["lookup"]>;
type LookupCondition = NonNullable<LookupDefinition["condition"]>;
type ConditionOperator = LookupCondition["rules"][number]["operator"];

interface LookupOption extends SelectOption {
  readonly label: string;
  readonly value: string;
  readonly many?: boolean;
}

interface WorkingRule {
  sourceFieldId: string;
  operator: ConditionOperator | "";
  operandKind: "field" | "constant";
  operandFieldId: string;
  constantText: string;
  constantNumber: number | null;
  constantBool: boolean | null;
}

interface WorkingCondition {
  sourceTableId: string;
  match: LookupCondition["match"];
  distinct: boolean;
  rules: WorkingRule[];
}

interface WorkingDraft {
  mode: "path" | "condition";
  path: { relationFieldId: string }[];
  targetFieldId: string;
  condition: WorkingCondition;
}

const props = defineProps<{
  value: LookupDefinition;
  relationOptions: LookupOption[][];
  targetFieldOptions: LookupOption[];
  maxDepth: number;
  loading?: boolean;
  error?: string | null;
  sourceTableOptions?: LookupOption[];
  currentFieldOptions?: LookupConditionFieldOption[];
  conditionFieldOptions?: LookupConditionFieldOption[];
  previewLoading?: boolean;
  previewValue?: unknown;
  previewReady?: boolean;
  previewError?: string | null;
}>();
const emit = defineEmits<{
  commit: [value: LookupDefinition];
  pathChange: [path: LookupDefinition["path"]];
  sourceTableChange: [sourceTableId: string];
  draftChange: [value: LookupDefinition | null];
}>();

const modeOptions: LookupOption[] = [
  { label: "关系路径（沿引用逐跳取值）", value: "path" },
  { label: "条件筛选（按条件查询来源表）", value: "condition" },
];
const matchOptions: LookupOption[] = [
  { label: "满足全部（ALL）", value: "all" },
  { label: "满足任一（ANY）", value: "any" },
];
const operandKindOptions: LookupOption[] = [
  { label: "当前行字段", value: "field" },
  { label: "类型化常量", value: "constant" },
];
const boolConstantOptions: LookupOption[] = [
  { label: "真（true）", value: "true" },
  { label: "假（false）", value: "false" },
];

const editing = ref(false);
const working = ref<WorkingDraft>(cloneValue(props.value));

const sourceTableChoices = computed(() => props.sourceTableOptions ?? []);
const conditionFieldChoices = computed(() => props.conditionFieldOptions ?? []);
const currentFieldChoices = computed(() => props.currentFieldOptions ?? []);
const sourceFieldOptions = computed<LookupOption[]>(() =>
  conditionFieldChoices.value
    .filter(field => isConditionLogicalType(field.logicalType))
    .map(field => ({ label: field.label, value: field.value })));

const pathComplete = computed(() =>
  working.value.path.length > 0
  && working.value.path.length <= props.maxDepth
  && working.value.path.every(step => step.relationFieldId.length > 0)
  && working.value.targetFieldId.trim().length > 0);
const conditionComplete = computed(() => {
  const condition = working.value.condition;
  return condition.sourceTableId.length > 0
    && working.value.targetFieldId.trim().length > 0
    && condition.rules.length >= 1
    && condition.rules.length <= LOOKUP_CONDITION_MAX_RULES
    && condition.rules.every(rule => isRuleComplete(rule));
});
const canCommit = computed(() =>
  working.value.mode === "path" ? pathComplete.value : conditionComplete.value);
const producesList = computed(() => working.value.path.some((step, index) =>
  props.relationOptions[index]?.find(option => option.value === step.relationFieldId)?.many));
const targetFieldLabel = computed(() => props.targetFieldOptions.find(
  option => option.value === props.value.targetFieldId,
)?.label ?? "目标字段");
const sourceTableLabel = computed(() => {
  const sourceTableId = props.value.condition?.sourceTableId ?? "";
  return sourceTableChoices.value.find(option => option.value === sourceTableId)?.label
    ?? (sourceTableId.length > 0 ? sourceTableId : "来源表");
});
const conditionSummary = computed(() => {
  const condition = props.value.condition;
  if (!condition) return "";
  return [
    condition.match === "any" ? "任一匹配" : "全部匹配",
    `${condition.rules.length} 条规则`,
    condition.distinct ? "按值保序去重" : "保留原值",
  ].join(" · ");
});

watch(
  () => props.value,
  (value) => {
    // 仅非编辑态同步外部值；不覆盖未提交输入（上下文切换由父层 :key 卸载重建）。
    if (!editing.value) working.value = cloneValue(value);
  },
  { deep: true },
);

onBeforeUnmount(() => {
  if (editing.value) emit("draftChange", null);
});

function beginEditing(): void {
  working.value = cloneValue(props.value);
  editing.value = true;
  // 进入编辑即用保存的定义发起预览；目录缺失时自然为 null。
  emitDraft();
}

function cancel(): void {
  const original = cloneValue(props.value);
  working.value = original;
  editing.value = false;
  // 无论是否改过模式/来源，恢复原模式目录：条件回原来源表，路径回原 path。
  if (original.mode === "condition") {
    emit("sourceTableChange", original.condition.sourceTableId);
  } else {
    emit("pathChange", original.path);
  }
  emit("draftChange", null);
}

function commit(): void {
  const draft = buildDraft();
  if (!draft) return;
  emit("commit", draft);
  editing.value = false;
  emit("draftChange", draft);
}

function emitDraft(): void {
  emit("draftChange", buildDraft());
}

function buildDraft(): LookupDefinition | null {
  if (working.value.mode === "path") {
    if (!pathComplete.value) return null;
    return {
      path: working.value.path.map(step => ({ relationFieldId: step.relationFieldId })),
      targetFieldId: working.value.targetFieldId.trim(),
    };
  }
  if (!conditionComplete.value) return null;
  const condition = working.value.condition;
  return {
    path: [],
    targetFieldId: working.value.targetFieldId.trim(),
    condition: {
      sourceTableId: condition.sourceTableId,
      match: condition.match,
      rules: condition.rules.map(rule => {
        // conditionComplete 已保证非空，仅做类型收窄。
        const operator = rule.operator as ConditionOperator;
        if (isNullOperator(operator)) {
          return { sourceFieldId: rule.sourceFieldId, operator };
        }
        if (rule.operandKind === "field") {
          return {
            sourceFieldId: rule.sourceFieldId,
            operator,
            operand: { kind: "field", fieldId: rule.operandFieldId },
          };
        }
        return {
          sourceFieldId: rule.sourceFieldId,
          operator,
          operand: { kind: "constant", value: constantValueOf(rule) },
        };
      }),
      distinct: condition.distinct,
    },
  };
}

function constantValueOf(rule: WorkingRule): string | number | boolean {
  switch (conditionField(rule)?.logicalType) {
    case "number": return rule.constantNumber ?? 0;
    case "bool": return rule.constantBool ?? false;
    // datetime-local 无 zone；提交统一转 RFC3339 UTC。
    case "dateTime": return dateTimeLocalToRfc3339(rule.constantText) ?? rule.constantText;
    default: return rule.constantText;
  }
}

function cloneValue(value: LookupDefinition): WorkingDraft {
  const condition = value.condition;
  return {
    mode: condition ? "condition" : "path",
    path: value.path.map(step => ({ relationFieldId: step.relationFieldId })),
    targetFieldId: value.targetFieldId,
    condition: {
      sourceTableId: condition?.sourceTableId ?? "",
      match: condition?.match ?? "all",
      distinct: condition?.distinct ?? false,
      rules: condition?.rules.length
        ? condition.rules.map(rule => {
          const value = rule.operand?.value;
          return {
            sourceFieldId: rule.sourceFieldId,
            operator: rule.operator,
            operandKind: rule.operand?.kind ?? "field",
            operandFieldId: rule.operand?.fieldId ?? "",
            // 已保存 dateTime 常量是 RFC3339；回显转为本地 datetime-local 串。
            constantText: typeof value === "string"
              ? toDisplayConstantText(rule.sourceFieldId, value)
              : "",
            constantNumber: typeof value === "number" && Number.isFinite(value) ? value : null,
            constantBool: typeof value === "boolean" ? value : null,
          };
        })
        : [createRule()],
    },
  };
}

function createRule(): WorkingRule {
  return {
    sourceFieldId: "",
    operator: "",
    operandKind: "field",
    operandFieldId: "",
    constantText: "",
    constantNumber: null,
    constantBool: null,
  };
}

function toDisplayConstantText(sourceFieldId: string, value: string): string {
  const logicalType = props.conditionFieldOptions?.find(
    field => field.value === sourceFieldId,
  )?.logicalType;
  return logicalType === "dateTime" ? rfc3339ToDateTimeLocal(value) : value;
}

function conditionField(rule: WorkingRule): LookupConditionFieldOption | undefined {
  return conditionFieldChoices.value.find(field => field.value === rule.sourceFieldId);
}

function ruleLogicalType(rule: WorkingRule): string {
  return conditionField(rule)?.logicalType ?? "";
}

function ruleOperatorOptions(rule: WorkingRule): LookupOption[] {
  const field = conditionField(rule);
  if (!field) return [];
  return conditionOperators(field.logicalType, field.filterOperators).map(operator => ({
    label: CONDITION_OPERATOR_LABELS[operator],
    value: operator,
  }));
}

function operandFieldOptions(rule: WorkingRule): LookupOption[] {
  const field = conditionField(rule);
  if (!field) return [];
  return currentFieldChoices.value
    .filter(item => item.logicalType === field.logicalType)
    .map(item => ({ label: item.label, value: item.value }));
}

function ruleSelectOptions(rule: WorkingRule): LookupOption[] {
  return (conditionField(rule)?.selectOptions ?? []).map(option => ({
    label: option.label,
    value: option.optionId,
  }));
}

function isRuleComplete(rule: WorkingRule): boolean {
  const field = conditionField(rule);
  if (!field || !isConditionLogicalType(field.logicalType)) return false;
  if (!rule.operator) return false;
  // 运算符必须属于公开交集；is_null 也不能绕过目录。
  if (!conditionOperators(field.logicalType, field.filterOperators).includes(rule.operator)) {
    return false;
  }
  if (isNullOperator(rule.operator)) return true;
  if (rule.operandKind === "field") {
    const operandField = currentFieldChoices.value.find(
      item => item.value === rule.operandFieldId,
    );
    return !!operandField && operandField.logicalType === field.logicalType;
  }
  switch (field.logicalType) {
    case "text":
      // 空字符串是类型化文本常量，不是 nil。
      return true;
    case "number":
      return rule.constantNumber !== null && Number.isFinite(rule.constantNumber);
    case "bool": return rule.constantBool !== null;
    case "date": return isValidDateValue(rule.constantText);
    case "dateTime": return isValidDateTimeLocalValue(rule.constantText);
    case "select": return (field.selectOptions ?? []).some(
      option => option.optionId === rule.constantText);
    default: return false;
  }
}

function selectMode(mode: string): void {
  if (mode !== "path" && mode !== "condition") return;
  if (working.value.mode === mode) return;
  // 路径与条件互斥；返回字段目录随模式切换，两侧各自保留数据。
  working.value = { ...working.value, mode, targetFieldId: "" };
  if (mode === "condition") {
    emit("sourceTableChange", working.value.condition.sourceTableId);
  } else {
    emit("pathChange", working.value.path);
  }
  emitDraft();
}

function selectTarget(targetFieldId: string): void {
  working.value = { ...working.value, targetFieldId };
  emitDraft();
}

function selectSourceTable(sourceTableId: string): void {
  // 改来源表即作废全部依赖选择；旧表的请求结果不得视为有效。
  working.value = {
    ...working.value,
    targetFieldId: "",
    condition: { ...working.value.condition, sourceTableId, rules: [createRule()] },
  };
  emit("sourceTableChange", sourceTableId);
  emitDraft();
}

function selectMatch(match: string): void {
  if (match !== "all" && match !== "any") return;
  working.value = {
    ...working.value,
    condition: { ...working.value.condition, match },
  };
  emitDraft();
}

function setDistinct(distinct: boolean): void {
  working.value = {
    ...working.value,
    condition: { ...working.value.condition, distinct },
  };
  emitDraft();
}

function updateRule(index: number, patch: Partial<WorkingRule>): void {
  const rules = working.value.condition.rules.map((rule, ruleIndex) =>
    ruleIndex === index ? { ...rule, ...patch } : rule);
  working.value = { ...working.value, condition: { ...working.value.condition, rules } };
  emitDraft();
}

function selectRuleSourceField(index: number, sourceFieldId: string): void {
  const field = conditionFieldChoices.value.find(item => item.value === sourceFieldId);
  const operators = field ? conditionOperators(field.logicalType, field.filterOperators) : [];
  const rule = working.value.condition.rules[index];
  const previous = rule?.operator ?? "";
  const operator = previous !== "" && operators.includes(previous)
    ? previous
    : operators[0] ?? "";
  updateRule(index, {
    sourceFieldId,
    operator,
    operandFieldId: "",
    constantText: "",
    constantNumber: null,
    constantBool: null,
  });
}

function selectRuleOperator(index: number, operator: string): void {
  updateRule(index, { operator: operator as ConditionOperator });
}

function selectRuleOperandKind(index: number, kind: string): void {
  if (kind !== "field" && kind !== "constant") return;
  updateRule(index, { operandKind: kind });
}

function addRule(): void {
  if (working.value.condition.rules.length >= LOOKUP_CONDITION_MAX_RULES) return;
  working.value = {
    ...working.value,
    condition: {
      ...working.value.condition,
      rules: [...working.value.condition.rules, createRule()],
    },
  };
  emitDraft();
}

function removeRule(index: number): void {
  if (working.value.condition.rules.length <= 1) return;
  const rules = working.value.condition.rules.filter((_, ruleIndex) => ruleIndex !== index);
  working.value = { ...working.value, condition: { ...working.value.condition, rules } };
  emitDraft();
}

function selectStep(index: number, relationFieldId: string): void {
  const path = working.value.path.slice(0, index + 1).map(step => ({ ...step }));
  path[index] = { relationFieldId };
  working.value = { ...working.value, path, targetFieldId: "" };
  emit("pathChange", path);
  emitDraft();
}

function addStep(): void {
  if (working.value.path.length >= props.maxDepth) return;
  const path = [...working.value.path, { relationFieldId: "" }];
  working.value = { ...working.value, path, targetFieldId: "" };
  emitDraft();
}

function removeStep(): void {
  if (working.value.path.length <= 1) return;
  const path = working.value.path.slice(0, -1);
  working.value = { ...working.value, path, targetFieldId: "" };
  emit("pathChange", path);
  emitDraft();
}

function onConstantDateInput(index: number, event: Event): void {
  updateRule(index, { constantText: (event.target as HTMLInputElement).value });
}

function selectConstantBool(index: number, value: string | null): void {
  updateRule(index, { constantBool: value === null ? null : value === "true" });
}

function formatPreviewValue(value: unknown): string {
  if (Array.isArray(value)) {
    return value.length === 0
      ? "[]（无匹配返回空列表）"
      : value.map(item => formatPreviewValue(item)).join("、");
  }
  if (value === null) return "null";
  if (value === undefined) return "undefined";
  if (typeof value === "object") return JSON.stringify(value) ?? String(value);
  return String(value);
}
</script>

<template>
  <article class="specialized-editor" data-testid="lookup-field-editor">
    <template v-if="!editing">
      <div v-if="working.mode === 'path'" class="editor-summary">
        <span class="editor-mark"><GitBranch :size="18" /></span>
        <div>
          <span class="eyebrow">LOOKUP MODULE</span>
          <strong>引用路径</strong>
          <small class="path-summary">
            <template v-for="(step, index) in value.path" :key="index">
              <code>{{ relationOptions[index]?.find(option => option.value === step.relationFieldId)?.label || "关系字段" }}</code>
              <ArrowRight :size="12" />
            </template>
            <code>{{ targetFieldLabel }}</code>
          </small>
        </div>
        <NTag size="small" :bordered="false">自动类型</NTag>
      </div>
      <div v-else class="editor-summary">
        <span class="editor-mark"><Funnel :size="18" /></span>
        <div>
          <span class="eyebrow">LOOKUP MODULE</span>
          <strong>条件筛选</strong>
          <small class="path-summary">
            <code>{{ sourceTableLabel }}</code>
            <ArrowRight :size="12" />
            <code>{{ targetFieldLabel }}</code>
          </small>
          <small>{{ conditionSummary }}</small>
        </div>
        <NTag size="small" :bordered="false">结果列表</NTag>
      </div>
      <NButton secondary :disabled="loading" data-testid="lookup-editor-entry" @click="beginEditing">
        <PencilLine :size="15" />
        进入查找引用编辑器
      </NButton>
    </template>

    <template v-else>
      <div class="editor-heading">
        <NButton quaternary size="small" data-testid="lookup-editor-cancel" @click="cancel">
          <ChevronLeft :size="15" />
          返回字段设置
        </NButton>
        <NTag size="small" :bordered="false">
          {{ working.mode === "path" ? "结构化路径" : "条件筛选" }}
        </NTag>
      </div>
      <NAlert v-if="error" type="error" :show-icon="false">{{ error }}</NAlert>
      <label>
        <span>取值模式（路径与条件互斥）</span>
        <NSelect
          :value="working.mode"
          :options="modeOptions"
          data-testid="lookup-mode"
          @update:value="selectMode(String($event))"
        />
      </label>

      <div v-if="working.mode === 'path'" class="lookup-path">
        <label v-for="(step, index) in working.path" :key="index">
          <span>第 {{ index + 1 }} 跳关系</span>
          <NSelect
            :value="step.relationFieldId || null"
            :options="relationOptions[index] ?? []"
            :loading="loading"
            :placeholder="`选择第 ${index + 1} 跳关系`"
            :data-testid="`lookup-relation-step-${index}`"
            @update:value="selectStep(index, String($event))"
          />
        </label>
        <label>
          <span>引用目标字段</span>
          <NSelect
            :value="working.targetFieldId || null"
            :options="targetFieldOptions"
            :loading="loading"
            placeholder="选择目标字段"
            data-testid="lookup-target-field"
            @update:value="selectTarget(String($event))"
          />
        </label>
      </div>
      <div v-if="working.mode === 'path'" class="path-actions">
        <NButton size="small" secondary :disabled="working.path.length >= maxDepth" @click="addStep">
          <Plus :size="14" />继续关联
        </NButton>
        <NButton size="small" quaternary :disabled="working.path.length <= 1" @click="removeStep">
          <Minus :size="14" />移除末跳
        </NButton>
        <NTag size="small" :bordered="false">
          {{ producesList ? "多值 · 类型化列表" : "单值 · 自动类型" }}
        </NTag>
      </div>

      <template v-else>
        <div class="lookup-path">
          <label>
            <span>来源表</span>
            <NSelect
              :value="working.condition.sourceTableId || null"
              :options="sourceTableChoices"
              :loading="loading"
              placeholder="选择来源数据表"
              data-testid="lookup-condition-source-table"
              @update:value="selectSourceTable(String($event))"
            />
          </label>
          <label>
            <span>返回字段</span>
            <NSelect
              :value="working.targetFieldId || null"
              :options="targetFieldOptions"
              :loading="loading"
              placeholder="选择返回字段"
              data-testid="lookup-target-field"
              @update:value="selectTarget(String($event))"
            />
          </label>
          <label>
            <span>条件组合</span>
            <NSelect
              :value="working.condition.match"
              :options="matchOptions"
              data-testid="lookup-condition-match"
              @update:value="selectMatch(String($event))"
            />
          </label>
          <div class="switch-cell">
            <span>结果去重</span>
            <div class="switch-line">
              <NSwitch
                :value="working.condition.distinct"
                size="small"
                data-testid="lookup-condition-distinct"
                @update:value="setDistinct"
              />
              <small>{{ working.condition.distinct ? "按值保序去重" : "保留原值" }}</small>
            </div>
          </div>
        </div>

        <div class="condition-rules" data-testid="lookup-condition-rules">
          <header class="rules-head">
            <strong>匹配条件（{{ working.condition.rules.length }} / {{ LOOKUP_CONDITION_MAX_RULES }}）</strong>
            <NButton
              size="tiny"
              secondary
              :disabled="working.condition.rules.length >= LOOKUP_CONDITION_MAX_RULES"
              data-testid="lookup-condition-add-rule"
              @click="addRule"
            >
              <Plus :size="12" />添加条件
            </NButton>
          </header>
          <div
            v-for="(rule, index) in working.condition.rules"
            :key="index"
            class="rule-row"
            :data-testid="`lookup-condition-rule-${index}`"
          >
            <label class="cell">
              <span>来源字段</span>
              <NSelect
                :value="rule.sourceFieldId || null"
                :options="sourceFieldOptions"
                :loading="loading"
                filterable
                placeholder="选择来源字段"
                :data-testid="`lookup-rule-source-field-${index}`"
                @update:value="selectRuleSourceField(index, String($event))"
              />
            </label>
            <label class="cell">
              <span>运算符</span>
              <NSelect
                :value="rule.operator || null"
                :options="ruleOperatorOptions(rule)"
                :disabled="!rule.sourceFieldId"
                placeholder="选择运算符"
                :data-testid="`lookup-rule-operator-${index}`"
                @update:value="selectRuleOperator(index, String($event))"
              />
            </label>
            <template v-if="rule.operator && !isNullOperator(rule.operator)">
              <label class="cell">
                <span>比较对象</span>
                <NSelect
                  :value="rule.operandKind"
                  :options="operandKindOptions"
                  placeholder="选择比较对象"
                  :data-testid="`lookup-rule-operand-kind-${index}`"
                  @update:value="selectRuleOperandKind(index, String($event))"
                />
              </label>
              <label v-if="rule.operandKind === 'field'" class="cell">
                <span>当前行字段</span>
                <NSelect
                  :value="rule.operandFieldId || null"
                  :options="operandFieldOptions(rule)"
                  :loading="loading"
                  placeholder="选择当前行字段"
                  :data-testid="`lookup-rule-operand-field-${index}`"
                  @update:value="updateRule(index, { operandFieldId: String($event) })"
                />
              </label>
              <label v-else-if="ruleLogicalType(rule) === 'text'" class="cell">
                <span>文本常量</span>
                <NInput
                  :value="rule.constantText"
                  placeholder="输入文本，可为空字符串"
                  :data-testid="`lookup-rule-constant-text-${index}`"
                  @update:value="updateRule(index, { constantText: String($event) })"
                />
              </label>
              <label v-else-if="ruleLogicalType(rule) === 'number'" class="cell">
                <span>数字常量</span>
                <NInputNumber
                  :value="rule.constantNumber"
                  :data-testid="`lookup-rule-constant-number-${index}`"
                  @update:value="updateRule(index, { constantNumber: $event })"
                />
              </label>
              <label v-else-if="ruleLogicalType(rule) === 'bool'" class="cell">
                <span>布尔常量</span>
                <NSelect
                  :value="rule.constantBool === null ? null : rule.constantBool ? 'true' : 'false'"
                  :options="boolConstantOptions"
                  placeholder="选择真或假"
                  :data-testid="`lookup-rule-constant-bool-${index}`"
                  @update:value="selectConstantBool(index, $event === null ? null : String($event))"
                />
              </label>
              <label v-else-if="ruleLogicalType(rule) === 'date'" class="cell">
                <span>日期常量</span>
                <input
                  type="date"
                  class="native-datetime"
                  :value="rule.constantText"
                  :data-testid="`lookup-rule-constant-date-${index}`"
                  @input="onConstantDateInput(index, $event)"
                />
              </label>
              <label v-else-if="ruleLogicalType(rule) === 'dateTime'" class="cell">
                <span>日期时间常量</span>
                <input
                  type="datetime-local"
                  class="native-datetime"
                  :value="rule.constantText"
                  :data-testid="`lookup-rule-constant-datetime-${index}`"
                  @input="onConstantDateInput(index, $event)"
                />
              </label>
              <label v-else class="cell">
                <span>选项常量</span>
                <NSelect
                  :value="rule.constantText || null"
                  :options="ruleSelectOptions(rule)"
                  placeholder="选择选项"
                  :data-testid="`lookup-rule-constant-select-${index}`"
                  @update:value="updateRule(index, { constantText: String($event) })"
                />
              </label>
            </template>
            <div v-else class="cell cell-muted">
              <span>比较值</span>
              <small>空值判断无需比较值</small>
            </div>
            <NButton
              class="rule-remove"
              quaternary
              size="tiny"
              :disabled="working.condition.rules.length <= 1"
              :data-testid="`lookup-condition-remove-rule-${index}`"
              @click="removeRule(index)"
            >
              <X :size="13" />
            </NButton>
          </div>
          <small class="rules-note">
            基础条件仅支持 text / number / bool / date / dateTime / select；运算符取字段公开 filterOperators 与类型闭集的交集。结果恒为列表（无匹配 []；去重按值保序、来源 ID 升序保留首值），由后端权威计算。
          </small>
        </div>

        <div class="condition-preview">
          <NAlert
            v-if="previewLoading"
            type="info"
            :show-icon="false"
            data-testid="lookup-preview-loading"
          ><NSpin size="small" /> 正在计算当前表第一条记录的样例结果…</NAlert>
          <NAlert
            v-else-if="previewError"
            type="warning"
            :show-icon="false"
            data-testid="lookup-preview-error"
          >样例计算失败：{{ previewError }}</NAlert>
          <NAlert
            v-else-if="previewReady"
            type="info"
            :show-icon="false"
            data-testid="lookup-preview-value"
          >样例结果：<code>{{ formatPreviewValue(previewValue) }}</code></NAlert>
        </div>
      </template>

      <div class="editor-actions">
        <small v-if="working.mode === 'path'">
          最多 {{ maxDepth }} 跳；类型和单值/列表形状由路径自动推导，不在 Lookup 内聚合。
        </small>
        <small v-else>未填完不可确认；来源表或字段变更会清空依赖选择。</small>
        <NButton
          type="primary"
          :disabled="!canCommit"
          data-testid="lookup-editor-commit"
          @click="commit"
        >
          {{ working.mode === "path" ? "确认引用路径" : "确认条件筛选" }}
        </NButton>
      </div>
    </template>
  </article>
</template>

<style scoped>
.specialized-editor {
  display: grid;
  gap: 14px;
  padding: 14px;
  border: 1px solid color-mix(in srgb, #0ea5e9 35%, var(--vt-border));
  border-radius: 12px;
  background:
    linear-gradient(135deg, color-mix(in srgb, #0ea5e9 7%, transparent), transparent 55%),
    var(--vt-bg-elevated);
}
.editor-summary,.editor-heading,.editor-actions,.path-summary,.path-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}
.editor-summary>div {
  display: flex;
  flex: 1;
  min-width: 0;
  flex-direction: column;
  gap: 3px;
}
.editor-mark {
  display: grid;
  width: 36px;
  height: 36px;
  place-items: center;
  border-radius: 10px;
  color: #0284c7;
  background: color-mix(in srgb, #0ea5e9 12%, var(--vt-bg-subtle));
}
.editor-heading,.editor-actions { justify-content: space-between; }
.lookup-path { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.path-actions { flex-wrap: wrap; }
label {
  display: flex;
  flex-direction: column;
  gap: 7px;
  font-size: 12px;
  font-weight: 650;
}
.path-summary { gap: 5px; overflow: hidden; }
.path-summary code {
  overflow: hidden;
  max-width: 150px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.eyebrow {
  color: #0284c7;
  font-size: 9px;
  font-weight: 800;
  letter-spacing: .14em;
}
small { color: var(--vt-fg-muted); }
.switch-cell { display: flex; flex-direction: column; gap: 7px; font-size: 12px; font-weight: 650; }
.switch-line { display: flex; align-items: center; gap: 8px; }
.condition-rules { display: flex; flex-direction: column; gap: 10px; }
.rules-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.rules-head strong { font-size: 12px; }
.rule-row {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr)) auto;
  gap: 8px;
  align-items: end;
  padding: 10px;
  border: 1px solid var(--vt-border);
  border-radius: 10px;
  background: var(--vt-bg-subtle);
}
.rule-row .cell { min-width: 0; }
.cell-muted { justify-content: center; }
.cell-muted small { font-weight: 500; }
.rule-remove { align-self: end; }
.native-datetime {
  height: 30px;
  padding: 0 8px;
  border: 1px solid var(--vt-border);
  border-radius: 4px;
  background: var(--vt-bg-elevated);
  color: var(--vt-fg);
  font: inherit;
  font-weight: 500;
}
.condition-preview:empty { display: none; }
@media(max-width:720px) {
  .lookup-path { grid-template-columns: 1fr; }
  .rule-row { grid-template-columns: 1fr; }
  .cell-muted { grid-column: auto; }
}
</style>
