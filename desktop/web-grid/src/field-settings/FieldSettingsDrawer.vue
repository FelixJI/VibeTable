<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import {
  NAlert,
  NButton,
  NCheckbox,
  NDrawer,
  NDrawerContent,
  NDynamicTags,
  NEmpty,
  NInput,
  NInputNumber,
  NProgress,
  NSelect,
  NSpace,
  NSwitch,
  NTabPane,
  NTabs,
  NTag,
} from "naive-ui";
import { ArchiveRestore, Plus, RefreshCw, Trash2 } from "lucide-vue-next";
import type {
  FieldDraftV2,
  LogicalTypeV2,
  SelectOptionV2,
} from "@/contracts";
import FormulaFieldEditor from "./formula/FormulaFieldEditor.vue";
import LookupFieldEditor from "./lookup/LookupFieldEditor.vue";
import { useFieldSettingsStore } from "./store";

const emit = defineEmits<{
  close: [];
  plan: [];
  apply: [];
  cancelMigration: [];
  loadRecycleBin: [];
  restore: [fieldId: string];
  loadRelationCatalog: [];
  selectRelationTarget: [tableId: string];
  loadLookupCatalog: [];
  resolveLookupPath: [path: readonly { readonly relationFieldId: string }[]];
  loadFormulaCatalog: [];
  validateFormula: [displaySource: string];
}>();

const store = useFieldSettingsStore();
const rootTab = ref("settings");
const settingsTab = ref("general");
const jsonDefaultText = ref("null");
const jsonSchemaText = ref("{}");
const jsonEditorError = ref("");
const planCard = ref<HTMLElement | null>(null);
watch(() => store.open, (open) => {
  if (open) {
    rootTab.value = store.result?.definition?.lifecycle.state === "retired"
      ? "recycle"
      : "settings";
    settingsTab.value = "general";
  }
});
watch(
  () => [store.open, store.draft?.logicalType] as const,
  ([open, logicalType], previous) => {
    if (open && logicalType === "relation"
      && (previous?.[0] !== open || previous?.[1] !== logicalType)) {
      emit("loadRelationCatalog");
    }
    if (open && logicalType === "lookup"
      && (previous?.[0] !== open || previous?.[1] !== logicalType)) {
      emit("loadLookupCatalog");
    }
    if (open && logicalType === "formula"
      && (previous?.[0] !== open || previous?.[1] !== logicalType)) {
      emit("loadFormulaCatalog");
    }
  },
);
watch(rootTab, (value) => {
  if (value === "recycle") emit("loadRecycleBin");
});
watch(() => store.plan?.planId, async (planId, previousPlanId) => {
  if (!planId || planId === previousPlanId) return;
  await nextTick();
  planCard.value?.scrollIntoView({ behavior: "smooth", block: "nearest" });
});
watch(
  () => [
    store.draft?.logicalType,
    store.draft?.value.default.value,
    store.draft?.json?.schema,
  ],
  () => {
    if (store.draft?.logicalType !== "json") return;
    jsonDefaultText.value = JSON.stringify(
      store.draft.value.default.value,
      null,
      2,
    ) ?? "null";
    jsonSchemaText.value = JSON.stringify(store.draft.json?.schema ?? {}, null, 2);
    jsonEditorError.value = "";
  },
  { immediate: true },
);

const typeOptions = computed(() => {
  const currentType = store.result?.definition?.logicalType;
  const allowed = currentType
    ? new Set([currentType, ...(store.sourceCapability?.conversionTargets ?? [])])
    : null;
  return store.capabilities
    .filter((item) => item.userCreatable && (!allowed || allowed.has(item.logicalType)))
    .map((item) => ({ label: typeLabel(item.logicalType), value: item.logicalType }));
});
const conversionRuleOptions = computed(() => (store.capability?.conversionRules ?? [])
  .map((value) => ({ label: value, value })));
const selectOptions = computed(() => store.draft?.select?.options.map((option) => ({
  label: option.label,
  value: option.optionId,
})).filter((option) => option.value && store.draft?.select?.options
  .find((candidate) => candidate.optionId === option.value)?.state === "active") ?? []);
const isComputedField = computed(() =>
  store.draft?.logicalType === "formula" || store.draft?.logicalType === "lookup");
const progress = computed(() => {
  const status = store.migration;
  if (!status?.total) return 0;
  return Math.min(100, Math.round((status.processed / status.total) * 100));
});
const relationTableOptions = computed(() => store.relationTables.map(table => ({
  label: table.displayName,
  value: table.tableId,
})));
const relationSourceFieldOptions = computed(() => (store.relationSourceSchema?.columns ?? [])
  .filter(column => column.fieldId && column.kind !== "system" && column.kind !== "relation")
  .map(column => ({ label: column.title, value: column.fieldId! })));
const relationTargetFieldOptions = computed(() => (store.relationTargetSchema?.columns ?? [])
  .filter(column => column.fieldId && column.kind !== "system" && column.kind !== "relation")
  .map(column => ({ label: column.title, value: column.fieldId! })));
const lookupRelationOptions = computed(() => store.lookupSchemas.map(schema => schema.columns
  .filter(column => column.kind === "relation" && column.fieldId && column.relationId)
  .flatMap(column => {
    const relation = schema.normalizedRelations.find(item => item.relationId === column.relationId);
    if (!relation?.relatedCollection || relation.kind === "m2a" || relation.junction) return [];
    return [{
      label: column.title,
      value: column.fieldId!,
      many: relation.kind !== "m2o",
    }];
  })));
const lookupTargetFieldOptions = computed(() => {
  const schema = store.lookupSchemas.at(-1);
  return (schema?.columns ?? [])
    .filter(column => column.fieldId && column.kind !== "system" && column.kind !== "relation")
    .map(column => ({ label: column.title, value: column.fieldId! }));
});
const formulaLocalFields = computed(() => (store.formulaSourceSchema?.columns ?? [])
  .filter(column => column.fieldId
    && column.fieldId !== store.result?.fieldId
    && column.kind !== "system"
    && column.kind !== "relation")
  .map(column => ({
    label: column.title,
    canonicalName: column.name,
    dataType: column.dataType,
  })));
const formulaRelations = computed(() => (store.formulaSourceSchema?.columns ?? [])
  .filter(column => column.fieldId && column.kind === "relation" && column.relationId)
  .flatMap(column => {
    const descriptor = store.formulaSourceSchema?.normalizedRelations.find(
      item => item.relationId === column.relationId,
    );
    const target = store.formulaTargetSchemas[column.fieldId!];
    if (!descriptor?.relatedCollection || descriptor.kind === "m2a"
      || descriptor.junction || !target) return [];
    return [{
      label: column.title,
      canonicalName: column.name,
      many: descriptor.kind !== "m2o",
      targetFields: target.columns
        .filter(item => item.fieldId && item.kind !== "system" && item.kind !== "relation")
        .map(item => ({
          label: item.title,
          canonicalName: item.name,
          dataType: item.dataType,
        })),
    }];
  }));

function patch(patchValue: Partial<FieldDraftV2>): void {
  store.patchDraft(patchValue);
}

function patchValue(patchValue: Partial<FieldDraftV2["value"]>): void {
  if (!store.draft) return;
  patch({ value: { ...store.draft.value, ...patchValue } });
}

function patchDefault(defaultPatch: Partial<FieldDraftV2["value"]["default"]>): void {
  if (!store.draft) return;
  patchValue({ default: { ...store.draft.value.default, ...defaultPatch, source: "user" } });
}

function patchConstraint<K extends keyof FieldDraftV2["constraints"]>(
  key: K,
  value: FieldDraftV2["constraints"][K],
): void {
  if (!store.draft) return;
  patch({ constraints: { ...store.draft.constraints, [key]: value } });
}

function patchDisplay(patchValue: Partial<FieldDraftV2["display"]>): void {
  if (!store.draft) return;
  patch({ display: { ...store.draft.display, ...patchValue } });
}

function patchStorage(
  patchValue: Partial<FieldDraftV2["storage"]["options"]>,
): void {
  if (!store.draft) return;
  patch({
    storage: {
      ...store.draft.storage,
      options: { ...store.draft.storage.options, ...patchValue },
    },
  });
}

function patchRelation(
  patchValue: Partial<NonNullable<FieldDraftV2["relation"]>>,
): void {
  if (!store.draft?.relation) return;
  patch({ relation: { ...store.draft.relation, ...patchValue } });
}

function confirmationLabel(value: string): string {
  if (value === "relationPair") return "同时停用或删除另一张表中的反向关联字段";
  if (value === "cascade") return "目标记录删除时级联删除当前表记录";
  if (value === "backupReceipt") return "永久清除已有可验证备份";
  if (value === "fieldName") return "输入的字段名称与待清除字段一致";
  return value;
}

function patchFile(
  patchValue: Partial<NonNullable<FieldDraftV2["file"]>>,
): void {
  if (!store.draft?.file) return;
  patch({ file: { ...store.draft.file, ...patchValue } });
}

function patchJson(
  patchValue: Partial<NonNullable<FieldDraftV2["json"]>>,
): void {
  if (!store.draft?.json) return;
  patch({ json: { ...store.draft.json, ...patchValue } });
}

function temporalDefaultKind(): string {
  const value = store.draft?.value.default.value;
  if (value && typeof value === "object" && "kind" in value) {
    return String((value as { readonly kind?: unknown }).kind ?? "fixed");
  }
  return "fixed";
}

function setTemporalDefaultKind(kind: string): void {
  if (kind === "fixed") {
    patchDefault({ value: "" });
    return;
  }
  patchDefault({ value: { kind } });
}

function patchGeoDefault(key: "lat" | "lon", value: number | null): void {
  const current = store.draft?.value.default.value;
  const point = current && typeof current === "object"
    ? current as { readonly lat?: unknown; readonly lon?: unknown }
    : {};
  patchDefault({
    value: {
      lat: key === "lat" ? (value ?? 0) : Number(point.lat ?? 0),
      lon: key === "lon" ? (value ?? 0) : Number(point.lon ?? 0),
    },
  });
}

function geoDefault(key: "lat" | "lon"): number {
  const current = store.draft?.value.default.value;
  if (!current || typeof current !== "object") return 0;
  return Number((current as Readonly<Record<string, unknown>>)[key] ?? 0);
}

function numericRange(key: "min" | "max"): number | null {
  const value = store.draft?.constraints.range[key];
  return typeof value === "number" ? value : null;
}

function patchJSONText(kind: "default" | "schema", text: string): void {
  if (kind === "default") jsonDefaultText.value = text;
  else jsonSchemaText.value = text;
  try {
    const parsed = JSON.parse(text) as unknown;
    if (kind === "schema") {
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
        throw new Error("JSON Schema 必须是对象");
      }
      patchJson({ schema: parsed as Readonly<Record<string, unknown>> });
    } else {
      patchDefault({ value: parsed });
    }
    jsonEditorError.value = "";
  } catch (error) {
    jsonEditorError.value = error instanceof Error ? error.message : String(error);
  }
}

function changeType(value: LogicalTypeV2): void {
  store.changeType(value);
}

function addSelectOption(): void {
  if (!store.draft) return;
  const options = [...(store.draft.select?.options ?? [])];
  options.push({
    optionId: "",
    label: `选项 ${options.length + 1}`,
    color: "#64748b",
    order: options.length,
    state: "active",
  });
  patch({ select: { options } });
}

function updateSelectOption(index: number, value: Partial<SelectOptionV2>): void {
  if (!store.draft?.select) return;
  const options = store.draft.select.options.map((item, position) =>
    position === index ? { ...item, ...value } : item);
  patch({ select: { options } });
}

function removeSelectOption(index: number): void {
  if (!store.draft?.select) return;
  const option = store.draft.select.options[index];
  if (!option) return;
  if (!option.optionId) {
    patch({ select: { options: store.draft.select.options.filter((_, i) => i !== index) } });
    return;
  }
  updateSelectOption(index, { state: "retired" });
}

function optionDeletionRules(option: SelectOptionV2): { label: string; value: string }[] {
  const rules = [{
    label: "清空使用该选项的单元格",
    value: `selectOption:${option.optionId}:clear`,
  }];
  for (const replacement of store.draft?.select?.options ?? []) {
    if (
      replacement.optionId
      && replacement.optionId !== option.optionId
      && replacement.state === "active"
    ) {
      rules.push({
        label: `替换为“${replacement.label}”`,
        value: `selectOption:${option.optionId}:replace:${replacement.optionId}`,
      });
    }
  }
  return rules;
}

function deleteSelectOption(index: number): void {
  if (!store.draft?.select) return;
  const option = store.draft.select.options[index];
  if (
    !option?.optionId
    || !store.conversionRule.startsWith(`selectOption:${option.optionId}:`)
  ) return;
  patch({
    select: {
      options: store.draft.select.options.filter((_, position) => position !== index),
    },
  });
}

function typeLabel(type: LogicalTypeV2): string {
  const labels: Partial<Record<LogicalTypeV2, string>> = {
    text: "单行文本", editor: "富文本", number: "数字", bool: "勾选",
    date: "日期", dateTime: "日期时间", time: "时间", autoDate: "自动日期",
    email: "邮箱", url: "网址", select: "单选", multiSelect: "多选",
    relation: "关联", file: "附件", geoPoint: "地理坐标", json: "JSON",
    formula: "公式", lookup: "查找引用",
  };
  return labels[type] ?? type;
}

function isTextual(type: LogicalTypeV2): boolean {
  return ["text", "editor", "email", "url"].includes(type);
}
</script>

<template>
  <NDrawer
    :show="store.open"
    :width="680"
    placement="right"
    @mask-click="emit('close')"
    @esc="emit('close')"
  >
    <NDrawerContent
      class="field-settings-drawer"
      closable
      :native-scrollbar="false"
      @close="emit('close')"
    >
      <template #header>
        <div class="drawer-heading">
          <div>
            <span class="eyebrow">SCHEMA V2</span>
            <strong>{{ store.isExisting ? "字段设置" : "新增字段" }}</strong>
          </div>
          <NTag size="small" :bordered="false">
            {{ store.result?.schemaRevision ?? "正在读取…" }}
          </NTag>
        </div>
      </template>

      <NAlert
        v-if="store.error"
        type="error"
        :show-icon="false"
        class="top-alert"
        data-testid="field-settings-error"
      >
        <strong>{{ store.errorCode ?? "字段变更失败" }}</strong>
        <div>{{ store.error }}</div>
      </NAlert>

      <div v-if="store.phase === 'loading'" class="loading-card">
        <NProgress type="line" processing :percentage="38" :show-indicator="false" />
        <span>正在读取字段能力与推荐设置…</span>
      </div>

      <NTabs v-else v-model:value="rootTab" type="segment" animated>
        <NTabPane name="settings" tab="字段设置">
          <template v-if="store.draft">
            <section class="identity-card">
              <label>
                <span>显示名称</span>
                <NInput
                  :value="store.draft.displayName"
                  maxlength="128"
                  placeholder="例如：订单金额"
                  data-testid="field-display-name"
                  @update:value="patch({ displayName: $event })"
                />
              </label>
              <label>
                <span>字段类型</span>
                <NSelect
                  :value="store.draft.logicalType"
                  :options="typeOptions"
                  filterable
                  data-testid="field-logical-type"
                  @update:value="changeType($event as LogicalTypeV2)"
                />
              </label>
              <label class="wide">
                <span>说明</span>
                <NInput
                  :value="store.draft.help"
                  type="textarea"
                  maxlength="300"
                  :autosize="{ minRows: 2, maxRows: 4 }"
                  placeholder="记录字段用途、填写规则或示例"
                  @update:value="patch({ help: $event })"
                />
              </label>
            </section>
            <div v-if="!isComputedField" class="recommended-action">
              <NButton size="small" secondary @click="store.restoreRecommended">
                恢复当前类型推荐值
              </NButton>
              <small>只修改草稿；仍需预览并应用</small>
            </div>

            <NTabs v-model:value="settingsTab" type="line" animated>
              <NTabPane name="general" tab="常规">
                <section v-if="!isComputedField" class="settings-section">
                  <div class="section-title">
                    <div><strong>值与空白</strong><small>0、false、空文本与未填写会被明确区分</small></div>
                  </div>
                  <div class="switch-row" v-if="store.capability?.supportsRequired">
                    <div><strong>必填</strong><small>现有数据不兼容时，计划会阻止应用</small></div>
                    <NSwitch
                      :value="store.draft.value.required"
                      @update:value="patchValue({ required: $event })"
                    />
                  </div>
                  <div class="switch-row" v-if="store.capability?.supportsDefault">
                    <div><strong>默认值</strong><small>仅影响之后未提供该字段的新记录</small></div>
                    <NSwitch
                      :value="store.draft.value.default.enabled"
                      data-testid="field-default-enabled"
                      @update:value="patchDefault({ enabled: $event })"
                    />
                  </div>
                  <div v-if="store.capability?.supportsDefault && store.draft.value.default.enabled" class="default-editor">
                    <NSwitch
                      v-if="store.draft.logicalType === 'bool'"
                      :value="Boolean(store.draft.value.default.value)"
                      data-testid="field-default-bool"
                      @update:value="patchDefault({ value: $event })"
                    />
                    <NInputNumber
                      v-else-if="store.draft.logicalType === 'number'"
                      :value="typeof store.draft.value.default.value === 'number'
                        ? store.draft.value.default.value : null"
                      clearable
                      data-testid="field-default-number"
                      @update:value="patchDefault({ value: $event })"
                    />
                    <NSelect
                      v-else-if="store.draft.logicalType === 'select'"
                      :value="store.draft.value.default.value as string | null"
                      :options="selectOptions"
                      clearable
                      @update:value="patchDefault({ value: $event })"
                    />
                    <div
                      v-else-if="['date', 'dateTime', 'time'].includes(store.draft.logicalType)"
                      class="two-column"
                    >
                      <NSelect
                        :value="temporalDefaultKind()"
                        :options="[
                          { label: '固定值', value: 'fixed' },
                          ...(store.draft.logicalType === 'date'
                            ? [{ label: '今天', value: 'today' }] : []),
                          ...(store.draft.logicalType === 'dateTime'
                            ? [{ label: '当前时刻', value: 'now' }] : []),
                          ...(store.draft.logicalType === 'time'
                            ? [{ label: '当前时间', value: 'currentTime' }] : []),
                        ]"
                        @update:value="setTemporalDefaultKind"
                      />
                      <NInput
                        v-if="temporalDefaultKind() === 'fixed'"
                        :value="typeof store.draft.value.default.value === 'string'
                          ? store.draft.value.default.value : ''"
                        :placeholder="store.draft.logicalType === 'date'
                          ? 'YYYY-MM-DD'
                          : store.draft.logicalType === 'dateTime'
                            ? 'RFC3339（含时区）' : 'HH:mm:ss'"
                        @update:value="patchDefault({ value: $event })"
                      />
                    </div>
                    <div v-else-if="store.draft.logicalType === 'geoPoint'" class="two-column">
                      <NInputNumber
                        :value="geoDefault('lat')"
                        placeholder="纬度"
                        @update:value="patchGeoDefault('lat', $event)"
                      />
                      <NInputNumber
                        :value="geoDefault('lon')"
                        placeholder="经度"
                        @update:value="patchGeoDefault('lon', $event)"
                      />
                    </div>
                    <div v-else-if="store.draft.logicalType === 'json'">
                      <NInput
                        :value="jsonDefaultText"
                        type="textarea"
                        :autosize="{ minRows: 3, maxRows: 9 }"
                        data-testid="field-default-json"
                        @update:value="patchJSONText('default', $event)"
                      />
                      <NAlert v-if="jsonEditorError" type="error" :show-icon="false">
                        {{ jsonEditorError }}
                      </NAlert>
                    </div>
                    <NSelect
                      v-else-if="store.draft.logicalType === 'multiSelect'"
                      :value="Array.isArray(store.draft.value.default.value)
                        ? store.draft.value.default.value as string[] : []"
                      :options="selectOptions"
                      multiple
                      clearable
                      @update:value="patchDefault({ value: $event })"
                    />
                    <NInput
                      v-else
                      :value="typeof store.draft.value.default.value === 'string'
                        ? store.draft.value.default.value : ''"
                      clearable
                      data-testid="field-default-text"
                      @update:value="patchDefault({ value: $event })"
                    />
                  </div>
                </section>

                <section v-if="store.draft.logicalType === 'number'" class="settings-section">
                  <div class="section-title"><div><strong>数字显示</strong><small>只改变呈现，不改写原始值</small></div></div>
                  <div class="two-column">
                    <label><span>小数位</span><NInputNumber
                      :value="store.draft.display.displayScale"
                      :min="0" :max="12"
                      @update:value="patchDisplay({ displayScale: $event ?? 0 })"
                    /></label>
                    <label><span>预设</span><NSelect
                      :value="store.draft.display.preset"
                      :options="(store.capability?.displayPresets ?? []).map(value => ({ label: value, value }))"
                      @update:value="patchDisplay({ preset: $event })"
                    /></label>
                  </div>
                  <div class="switch-row">
                    <div><strong>千分位</strong></div>
                    <NSwitch :value="store.draft.display.useGrouping" @update:value="patchDisplay({ useGrouping: $event })" />
                  </div>
                </section>

                <section v-if="['select', 'multiSelect'].includes(store.draft.logicalType)" class="settings-section">
                  <div class="section-title">
                    <div><strong>选项</strong><small>选项身份由 Sidecar 分配，修改标签不会改写记录</small></div>
                    <NButton size="tiny" secondary @click="addSelectOption"><Plus :size="14" />添加</NButton>
                  </div>
                  <div v-for="(option, index) in store.draft.select?.options ?? []" :key="`${option.optionId}:${index}`" class="option-row">
                    <input
                      type="color"
                      :value="option.color || '#64748b'"
                      aria-label="选项颜色"
                      @input="updateSelectOption(index, { color: ($event.target as HTMLInputElement).value })"
                    />
                    <NInput
                      :value="option.label"
                      :disabled="option.state === 'retired'"
                      @update:value="updateSelectOption(index, { label: $event })"
                    />
                    <NTag v-if="option.state === 'retired'" size="small">已停用</NTag>
                    <NButton
                      quaternary
                      size="tiny"
                      aria-label="停用选项"
                      :disabled="option.state === 'retired'"
                      @click="removeSelectOption(index)"
                    >
                      <Trash2 :size="14" />
                    </NButton>
                    <template v-if="option.optionId && option.state === 'active'">
                      <NSelect
                        class="option-delete-rule"
                        size="small"
                        placeholder="删除时：替换或清空"
                        :value="store.conversionRule.startsWith(`selectOption:${option.optionId}:`) ? store.conversionRule : null"
                        :options="optionDeletionRules(option)"
                        @update:value="store.setConversionRule"
                      />
                      <NButton
                        quaternary
                        size="tiny"
                        type="error"
                        aria-label="永久删除选项"
                        :disabled="!store.conversionRule.startsWith(`selectOption:${option.optionId}:`)"
                        @click="deleteSelectOption(index)"
                      >
                        删除
                      </NButton>
                    </template>
                  </div>
                </section>

                <section v-if="store.draft.logicalType === 'relation' && store.draft.relation" class="settings-section">
                  <div class="section-title">
                    <div>
                      <strong>双向关联</strong>
                      <small>选择表和显示字段；系统自动创建并维护另一端字段</small>
                    </div>
                    <NTag v-if="store.draft.relation.pairId" size="small" type="success">已成对</NTag>
                  </div>
                  <NAlert v-if="store.relationCatalogError" type="error" :show-icon="false">
                    {{ store.relationCatalogError }}
                  </NAlert>
                  <div class="two-column">
                    <label><span>关联到</span><NSelect
                      data-testid="relation-target-table"
                      :value="store.draft.relation.targetTableId"
                      :options="relationTableOptions"
                      :loading="store.relationCatalogLoading"
                      :disabled="store.isExisting"
                      placeholder="选择数据表"
                      @update:value="emit('selectRelationTarget', String($event))"
                    /></label>
                    <label><span>本表可选择</span><NSelect
                      :value="store.draft.relation.cardinality"
                      :options="[{label:'单条',value:'one'},{label:'多条',value:'many'}]"
                      @update:value="patchRelation({ cardinality: $event as 'one' | 'many' })"
                    /></label>
                    <label><span>关联记录显示为</span><NSelect
                      data-testid="relation-target-display-field"
                      :value="store.draft.relation.displayFieldId"
                      :options="relationTargetFieldOptions"
                      placeholder="选择目标表显示字段"
                      @update:value="patchRelation({ displayFieldId: $event })"
                    /></label>
                    <label><span>目标删除时</span><NSelect
                      :value="store.draft.relation.deletePolicy"
                      :options="[
                        {label:'置空',value:'setNull'},{label:'阻止删除',value:'restrict'}]"
                      @update:value="patchRelation({ deletePolicy: $event as 'setNull' | 'restrict' | 'cascade' })"
                    /></label>
                    <template v-if="store.action === 'create' && store.relationPair">
                      <label><span>另一端字段名称</span><NInput
                        data-testid="relation-reciprocal-name"
                        :value="store.relationPair.reciprocalDisplayName"
                        placeholder="例如：订单"
                        @update:value="store.patchRelationPair({ reciprocalDisplayName: $event })"
                      /></label>
                      <label><span>另一端可选择</span><NSelect
                        :value="store.relationPair.reciprocalCardinality"
                        :options="[{label:'单条',value:'one'},{label:'多条',value:'many'}]"
                        @update:value="store.patchRelationPair({
                          reciprocalCardinality: $event as 'one' | 'many',
                        })"
                      /></label>
                      <label><span>本表记录显示为</span><NSelect
                        data-testid="relation-source-display-field"
                        :value="store.relationPair.sourceDisplayFieldId"
                        :options="relationSourceFieldOptions"
                        placeholder="选择本表显示字段"
                        @update:value="store.patchRelationPair({ sourceDisplayFieldId: $event })"
                      /></label>
                    </template>
                  </div>
                </section>

                <section v-if="store.draft.logicalType === 'bool'" class="settings-section">
                  <div class="section-title"><div><strong>布尔显示</strong><small>未填写、false 和 true 保持三种状态</small></div></div>
                  <div class="two-column">
                    <label><span>显示方式</span><NSelect
                      :value="store.draft.display.mode"
                      :options="[{label:'复选框',value:'checkbox'},{label:'开关',value:'switch'},{label:'文字',value:'text'}]"
                      @update:value="patchDisplay({ mode: $event })"
                    /></label>
                    <span />
                    <label><span>真值标签</span><NInput
                      :value="store.draft.display.trueLabel"
                      @update:value="patchDisplay({ trueLabel: $event })"
                    /></label>
                    <label><span>假值标签</span><NInput
                      :value="store.draft.display.falseLabel"
                      @update:value="patchDisplay({ falseLabel: $event })"
                    /></label>
                  </div>
                </section>

                <section v-if="['date', 'dateTime', 'time'].includes(store.draft.logicalType)" class="settings-section">
                  <div class="section-title"><div><strong>时间显示</strong><small>精度和时区仅影响呈现</small></div></div>
                  <div class="two-column">
                    <label><span>显示精度</span><NSelect
                      :value="store.draft.display.precision"
                      :options="[
                        {label:'日期',value:'day'},{label:'分钟',value:'minute'},
                        {label:'秒',value:'second'},{label:'毫秒',value:'millisecond'}]"
                      @update:value="patchDisplay({ precision: $event })"
                    /></label>
                    <label v-if="store.draft.logicalType === 'dateTime'"><span>显示时区</span><NSelect
                      :value="store.draft.display.timezone"
                      :options="[{label:'系统时区',value:'system'},{label:'UTC',value:'UTC'}]"
                      @update:value="patchDisplay({ timezone: $event })"
                    /></label>
                  </div>
                </section>

                <section v-if="store.draft.logicalType === 'autoDate' && store.draft.autoDate" class="settings-section">
                  <div class="section-title"><div><strong>系统时间角色</strong><small>只读字段，由记录生命周期维护</small></div></div>
                  <NInput :value="store.draft.autoDate.role" readonly />
                </section>

                <section v-if="store.draft.formula" class="settings-section">
                  <div class="section-title">
                    <div>
                      <strong>计算字段</strong>
                      <small>结构化公式由专用模块维护，保存仍通过同一冻结 FieldChangePlan</small>
                    </div>
                  </div>
                  <FormulaFieldEditor
                    :value="store.draft.formula"
                    :local-fields="formulaLocalFields"
                    :relations="formulaRelations"
                    :result-type="store.result?.definition?.formula?.resultType"
                    :validation="store.formulaValidation"
                    :validated-source="store.formulaValidatedSource"
                    :validating="store.formulaValidating || store.formulaCatalogLoading"
                    :error="store.formulaValidationError || store.formulaCatalogError"
                    :preview-value="store.formulaPreviewValue"
                    :preview-ready="store.formulaPreviewReady"
                    :previewing="store.formulaPreviewing"
                    :preview-error="store.formulaPreviewError"
                    :preview-note="store.formulaPreviewNote"
                    @commit="patch({ formula: $event })"
                    @validate="emit('validateFormula', $event)"
                  />
                </section>

                <section v-if="store.draft.lookup" class="settings-section">
                  <div class="section-title">
                    <div>
                      <strong>查找引用</strong>
                      <small>可视化选择最多 {{ store.lookupMaxDepth }} 跳关系；结果类型和单值/列表形状自动推导</small>
                    </div>
                  </div>
                  <LookupFieldEditor
                    :value="store.draft.lookup"
                    :relation-options="lookupRelationOptions"
                    :target-field-options="lookupTargetFieldOptions"
                    :max-depth="store.lookupMaxDepth"
                    :loading="store.lookupCatalogLoading"
                    :error="store.lookupCatalogError"
                    @commit="patch({ lookup: $event })"
                    @path-change="emit('resolveLookupPath', $event)"
                  />
                </section>
              </NTabPane>

              <NTabPane name="advanced" tab="高级">
                <section v-if="store.result?.definition" class="settings-section">
                  <div class="section-title"><div><strong>只读身份诊断</strong><small>重命名、迁移和恢复不会改变产品字段身份</small></div></div>
                  <div class="two-column">
                    <label><span>fieldId</span><NInput :value="store.result.definition.identity.fieldId" readonly /></label>
                    <label><span>physicalName</span><NInput :value="store.result.definition.identity.physicalName" readonly /></label>
                    <label class="wide">
                      <span>数据源字段标识（只读）</span>
                      <NInput :value="store.result.definition.identity.providerFieldId" readonly />
                      <small>由存储引擎维护，用于诊断与迁移，普通使用无需修改。</small>
                    </label>
                  </div>
                </section>
                <section v-if="!isComputedField" class="settings-section">
                  <div class="section-title"><div><strong>约束</strong><small>保存前会扫描现有数据</small></div></div>
                  <div v-if="store.capability?.supportsUnique" class="switch-row">
                    <div><strong>唯一</strong><small>产品空白值不参与唯一比较</small></div>
                    <NSwitch
                      :value="store.draft.constraints.unique.enabled"
                      @update:value="patchConstraint('unique', {
                        ...store.draft!.constraints.unique, enabled: $event,
                      })"
                    />
                  </div>
                  <div v-if="isTextual(store.draft.logicalType)" class="two-column">
                    <label><span>最短长度</span><NInputNumber
                      :value="store.draft.constraints.length.min"
                      :min="0" clearable
                      @update:value="patchConstraint('length', { ...store.draft!.constraints.length, min: $event })"
                    /></label>
                    <label><span>最长长度</span><NInputNumber
                      :value="store.draft.constraints.length.max"
                      :min="0" clearable
                      @update:value="patchConstraint('length', { ...store.draft!.constraints.length, max: $event })"
                    /></label>
                    <label class="wide"><span>正则表达式</span><NInput
                      :value="store.draft.constraints.pattern.value"
                      placeholder="留空表示不限制"
                      @update:value="patchConstraint('pattern', {
                        enabled: !!$event, value: $event,
                      })"
                    /></label>
                  </div>
                  <div v-if="store.draft.logicalType === 'number'" class="two-column">
                    <label><span>最小值</span><NInputNumber
                      :value="numericRange('min')" clearable
                      @update:value="patchConstraint('range', { ...store.draft!.constraints.range, min: $event })"
                    /></label>
                    <label><span>最大值</span><NInputNumber
                      :value="numericRange('max')" clearable
                      @update:value="patchConstraint('range', { ...store.draft!.constraints.range, max: $event })"
                    /></label>
                  </div>

                  <div v-if="store.draft.logicalType === 'number'" class="switch-row">
                    <div><strong>仅允许整数</strong><small>这是值约束，不会改变 binary64 存储</small></div>
                    <NSwitch
                      :value="store.draft.storage.options.onlyInt"
                      @update:value="patchStorage({ onlyInt: $event })"
                    />
                  </div>
                  <template v-if="store.draft.logicalType === 'number'">
                    <div class="two-column">
                      <label><span>小数位模式</span><NSelect
                        :value="store.draft.display.scaleMode"
                        :options="[{label:'最多',value:'max'},{label:'固定',value:'fixed'}]"
                        @update:value="patchDisplay({ scaleMode: $event })"
                      /></label>
                      <label><span>币种</span><NInput
                        :value="store.draft.display.currency"
                        @update:value="patchDisplay({ currency: $event })"
                      /></label>
                      <label><span>百分比存储</span><NSelect
                        :value="store.draft.display.percentStorage"
                        :options="[{label:'比率',value:'ratio'},{label:'百分数',value:'percent'}]"
                        @update:value="patchDisplay({ percentStorage: $event })"
                      /></label>
                      <label><span>单位</span><NInput
                        :value="store.draft.display.unit ?? ''"
                        @update:value="patchDisplay({ unit: $event || null })"
                      /></label>
                    </div>
                    <div class="switch-row">
                      <div><strong>去除末尾零</strong></div>
                      <NSwitch
                        :value="store.draft.display.trimTrailingZeros"
                        @update:value="patchDisplay({ trimTrailingZeros: $event })"
                      />
                    </div>
                  </template>

                  <div
                    v-if="['date', 'dateTime', 'time'].includes(store.draft.logicalType)"
                    class="two-column"
                  >
                    <label><span>最早值</span><NInput
                      :value="typeof store.draft.constraints.range.min === 'string'
                        ? store.draft.constraints.range.min : ''"
                      @update:value="patchConstraint('range', {
                        ...store.draft!.constraints.range, min: $event || null,
                      })"
                    /></label>
                    <label><span>最晚值</span><NInput
                      :value="typeof store.draft.constraints.range.max === 'string'
                        ? store.draft.constraints.range.max : ''"
                      @update:value="patchConstraint('range', {
                        ...store.draft!.constraints.range, max: $event || null,
                      })"
                    /></label>
                  </div>

                  <div v-if="store.draft.logicalType === 'editor'" class="two-column">
                    <label><span>最大字节数</span><NInputNumber
                      :value="store.draft.storage.options.maxSize"
                      :min="1"
                      @update:value="patchStorage({ maxSize: $event ?? 1 })"
                    /></label>
                    <div class="switch-row compact">
                      <div><strong>转换 URL</strong></div>
                      <NSwitch
                        :value="store.draft.storage.options.convertURLs"
                        @update:value="patchStorage({ convertURLs: $event })"
                      />
                    </div>
                  </div>

                  <div v-if="['email', 'url'].includes(store.draft.logicalType)" class="two-column">
                    <label><span>仅允许域名</span><NDynamicTags
                      :value="[...store.draft.constraints.domains.only]"
                      @update:value="patchConstraint('domains', {
                        ...store.draft!.constraints.domains, only: $event,
                      })"
                    /></label>
                    <label><span>排除域名</span><NDynamicTags
                      :value="[...store.draft.constraints.domains.except]"
                      @update:value="patchConstraint('domains', {
                        ...store.draft!.constraints.domains, except: $event,
                      })"
                    /></label>
                  </div>

                  <div v-if="['select', 'multiSelect', 'relation'].includes(store.draft.logicalType)" class="two-column">
                    <label><span>最少选择</span><NInputNumber
                      :value="store.draft.constraints.selection.min"
                      :min="0"
                      @update:value="patchConstraint('selection', {
                        ...store.draft!.constraints.selection, min: $event ?? 0,
                      })"
                    /></label>
                    <label><span>最多选择</span><NInputNumber
                      :value="store.draft.constraints.selection.max"
                      :min="1" clearable
                      @update:value="patchConstraint('selection', {
                        ...store.draft!.constraints.selection, max: $event,
                      })"
                    /></label>
                  </div>

                  <div v-if="store.draft.logicalType === 'geoPoint'" class="two-column">
                    <label><span>坐标显示小数位</span><NInputNumber
                      :value="store.draft.display.displayScale"
                      :min="0" :max="12"
                      @update:value="patchDisplay({ displayScale: $event ?? 6 })"
                    /></label>
                  </div>
                </section>

                <section v-if="store.draft.logicalType === 'file' && store.draft.file" class="settings-section">
                  <div class="section-title"><div><strong>附件限制</strong><small>永久清除会同时删除关联 blob</small></div></div>
                  <div class="two-column">
                    <label><span>最多文件数</span><NInputNumber
                      :value="store.draft.file.maxFiles" :min="1"
                      @update:value="patchFile({ maxFiles: $event ?? 1 })"
                    /></label>
                    <label><span>单文件最大字节</span><NInputNumber
                      :value="store.draft.file.maxBytesPerFile" :min="1"
                      @update:value="patchFile({ maxBytesPerFile: $event ?? 1 })"
                    /></label>
                    <label><span>允许 MIME</span><NDynamicTags
                      :value="[...store.draft.file.allowedMimeTypes]"
                      @update:value="patchFile({ allowedMimeTypes: $event })"
                    /></label>
                    <label><span>缩略图规格</span><NDynamicTags
                      :value="[...store.draft.file.thumbs]"
                      @update:value="patchFile({ thumbs: $event })"
                    /></label>
                  </div>
                  <div class="switch-row">
                    <div><strong>受保护附件</strong><small>读取需要受控 token</small></div>
                    <NSwitch :value="store.draft.file.protected" @update:value="patchFile({ protected: $event })" />
                  </div>
                </section>

                <section v-if="store.draft.logicalType === 'json' && store.draft.json" class="settings-section">
                  <div class="section-title"><div><strong>JSON 结构</strong><small>默认值 null 与未启用默认值严格区分</small></div></div>
                  <div class="two-column">
                    <label><span>根类型</span><NSelect
                      :value="store.draft.json.rootType"
                      :options="['any','object','array','string','number','boolean','null'].map(value => ({label:value,value}))"
                      @update:value="patchJson({ rootType: $event as NonNullable<FieldDraftV2['json']>['rootType'] })"
                    /></label>
                    <label><span>最大字节数</span><NInputNumber
                      :value="store.draft.json.maxSize" :min="1"
                      @update:value="patchJson({ maxSize: $event ?? 1 })"
                    /></label>
                    <label><span>编辑模式</span><NSelect
                      :value="store.draft.display.mode"
                      :options="[{label:'代码',value:'code'},{label:'树形',value:'tree'}]"
                      @update:value="patchDisplay({ mode: $event })"
                    /></label>
                    <label><span>缩进</span><NSelect
                      :value="store.draft.display.indent ?? 2"
                      :options="[{label:'2 空格',value:2},{label:'4 空格',value:4}]"
                      @update:value="patchDisplay({ indent: $event })"
                    /></label>
                    <label class="wide"><span>JSON Schema</span><NInput
                      :value="jsonSchemaText"
                      type="textarea"
                      :autosize="{ minRows: 4, maxRows: 12 }"
                      @update:value="patchJSONText('schema', $event)"
                    /></label>
                  </div>
                  <NAlert v-if="jsonEditorError" type="error" :show-icon="false">
                    {{ jsonEditorError }}
                  </NAlert>
                </section>

                <section v-if="!isComputedField" class="settings-section">
                  <div class="switch-row">
                    <div><strong>在数据源后台突出显示</strong><small>仅影响 PocketBase 管理后台的字段展示，不改变 VibeTable 表格界面或数据。</small></div>
                    <NSwitch
                      :value="store.draft.storage.options.presentable"
                      @update:value="patchStorage({ presentable: $event })"
                    />
                  </div>
                </section>

                <section v-if="store.action === 'convert'" class="settings-section emphasis">
                  <div class="section-title"><div><strong>类型转换</strong><small>非空表将通过 shadow migration 执行</small></div></div>
                  <label><span>转换规则</span><NSelect
                    :value="store.conversionRule"
                    @update:value="store.setConversionRule"
                    :options="conversionRuleOptions"
                    placeholder="请选择明确的转换规则"
                  /></label>
                </section>

                <section class="danger-zone" v-if="store.isExisting">
                  <div><strong>危险区</strong><small>这些操作都必须先生成冻结计划</small></div>
              <div v-if="store.draft.relation" class="switch-row">
                    <div>
                      <strong>目标删除时级联删除本表记录</strong>
                      <small>方向：目标表 → 当前表；计划会扫描影响记录和依赖</small>
                    </div>
                    <NSwitch
                      :value="store.draft.relation.deletePolicy === 'cascade'"
                      @update:value="patchRelation({
                        deletePolicy: $event ? 'cascade' : 'restrict',
                      })"
                    />
                  </div>
                  <NSpace>
                    <NButton secondary type="warning" @click="store.beginPlan('retire'); emit('plan')">
                      停用字段
                    </NButton>
                    <NButton secondary type="error" @click="store.beginPlan('purge'); emit('plan')">
                      永久清除
                    </NButton>
                  </NSpace>
                  <NInput
                    :value="store.backupReceipt"
                    placeholder="永久清除需要备份回执"
                    @update:value="store.setBackupReceipt"
                  />
                  <NInput
                    :value="store.confirmation"
                    placeholder="按计划要求输入文字确认"
                    @update:value="store.setConfirmation"
                  />
                </section>
              </NTabPane>
            </NTabs>

            <section v-if="store.plan" ref="planCard" class="plan-card" data-testid="field-change-plan">
              <div class="section-title">
                <div>
                  <span class="eyebrow">FROZEN PLAN</span>
                  <strong>{{ store.plan.intent.action }} · {{ store.plan.classes.join(" / ") }}</strong>
                  <small>有效期至 {{ store.plan.expiresAt }}</small>
                </div>
                <NTag :type="store.plan.canApply ? 'success' : 'error'" size="small">
                  {{ store.plan.canApply ? "可应用" : "已阻止" }}
                </NTag>
              </div>
              <NAlert
                v-if="store.draft.relation?.pairId"
                type="warning"
                :show-icon="false"
              >
                停用或永久清除此字段时，计划会同时处理另一端关联字段；任一端存在依赖都会阻止操作。
              </NAlert>
              <div class="impact-grid">
                <span><b>{{ store.plan.impact.records }}</b>记录</span>
                <span><b>{{ store.plan.impact.missing }}</b>空白</span>
                <span><b>{{ store.plan.impact.ambiguous }}</b>歧义</span>
                <span><b>{{ store.plan.impact.dependencies.length }}</b>依赖</span>
              </div>
              <NAlert v-for="warning in store.plan.warnings" :key="warning.code + warning.path" type="warning" :show-icon="false">
                <strong>{{ warning.code }}</strong> · {{ warning.message }}
              </NAlert>
              <NAlert v-for="item in store.plan.errors" :key="item.code + item.path" type="error" :show-icon="false">
                <strong>{{ item.code }}</strong> · {{ item.message }}
              </NAlert>
              <NCheckbox
                v-for="item in store.plan.confirmations"
                :key="item"
                :checked="store.confirmations.includes(item)"
                @update:checked="checked => store.confirmations = checked
                  ? [...store.confirmations, item]
                  : store.confirmations.filter(value => value !== item)"
              >
                我已确认：{{ confirmationLabel(item) }}
              </NCheckbox>
            </section>

            <section v-if="store.migration || store.phase === 'migrating'" class="migration-card">
              <div class="section-title">
                <div><span class="eyebrow">MIGRATION</span><strong>{{ store.migration?.phase ?? "planned" }}</strong></div>
                <NTag size="small">{{ store.migration?.processed ?? 0 }} / {{ store.migration?.total ?? 0 }}</NTag>
              </div>
              <NProgress type="line" :percentage="progress" :processing="store.phase === 'migrating'" />
              <NButton
                v-if="store.migration?.canCancel"
                secondary type="warning"
                @click="emit('cancelMigration')"
              >
                取消迁移
              </NButton>
            </section>
          </template>
        </NTabPane>

        <NTabPane name="recycle" tab="回收站">
          <div class="recycle-head">
            <div><strong>已停用字段</strong><small>恢复会保留原字段身份与数据</small></div>
            <NButton size="small" quaternary @click="emit('loadRecycleBin')"><RefreshCw :size="14" />刷新</NButton>
          </div>
          <NEmpty v-if="store.recycled.length === 0" description="回收站为空" />
          <article v-for="field in store.recycled" :key="field.identity.fieldId" class="recycle-item">
            <div><ArchiveRestore :size="16" /><span><strong>{{ field.displayName }}</strong><small>{{ typeLabel(field.logicalType) }}</small></span></div>
            <NButton size="small" secondary @click="emit('restore', field.identity.fieldId)">恢复</NButton>
          </article>
        </NTabPane>
      </NTabs>

      <template #footer>
        <div class="drawer-footer">
          <span v-if="store.dirty" class="dirty-dot">● 有未保存更改</span>
          <span v-else>字段身份由 Sidecar 管理</span>
          <NSpace>
            <NButton data-testid="field-close-button" @click="emit('close')">关闭</NButton>
            <NButton
              secondary
              :disabled="!store.canPlan"
              :loading="store.phase === 'planning'"
              data-testid="field-plan-button"
              @click="emit('plan')"
            >
              预览变更
            </NButton>
            <NButton
              type="primary"
              :disabled="!store.canApply"
              :loading="store.phase === 'applying'"
              data-testid="field-apply-button"
              @click="emit('apply')"
            >
              保存字段变更
            </NButton>
          </NSpace>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.drawer-heading,.section-title,.drawer-footer,.recycle-head,.switch-row {
  display:flex;align-items:center;justify-content:space-between;gap:16px;
}
.drawer-heading>div,.section-title>div,.recycle-head>div,.switch-row>div {
  display:flex;flex-direction:column;gap:3px;
}
.drawer-heading strong{font-size:18px}.eyebrow{font-size:10px;letter-spacing:.14em;color:var(--vt-fg-accent);font-weight:800}
small{color:var(--vt-fg-muted);line-height:1.4}.top-alert{margin-bottom:14px}
.loading-card,.identity-card,.settings-section,.plan-card,.migration-card,.recycle-item {
  border:1px solid var(--vt-border);border-radius:14px;background:var(--vt-bg-elevated);
}
.loading-card{display:grid;gap:10px;padding:18px}.identity-card{padding:16px;display:grid;grid-template-columns:1fr 1fr;gap:14px;margin-bottom:8px}
.recommended-action{display:flex;align-items:center;justify-content:space-between;gap:12px;margin:0 0 8px}.recommended-action small{color:var(--vt-fg-muted)}
label{display:flex;flex-direction:column;gap:7px;font-size:12px;font-weight:650}.wide{grid-column:1/-1}
.settings-section,.plan-card,.migration-card{padding:16px;margin:12px 0;display:grid;gap:14px}
.two-column{display:grid;grid-template-columns:1fr 1fr;gap:12px}.default-editor{padding:12px;border-radius:10px;background:var(--vt-bg-subtle)}
.option-row{display:grid;grid-template-columns:34px 1fr auto;gap:8px;align-items:center}.option-row input[type=color]{width:30px;height:30px;border:0;background:none}
.emphasis{border-color:color-mix(in srgb,var(--vt-fg-accent) 45%,var(--vt-border))}
.danger-zone{display:grid;gap:12px;margin-top:18px;padding:16px;border:1px solid color-mix(in srgb,#ef4444 40%,var(--vt-border));border-radius:14px;background:color-mix(in srgb,#ef4444 5%,var(--vt-bg-elevated))}
.danger-zone>div:first-child{display:flex;flex-direction:column;gap:3px}.impact-grid{display:grid;grid-template-columns:repeat(4,1fr);gap:8px}
.impact-grid span{display:flex;flex-direction:column;padding:10px;background:var(--vt-bg-subtle);border-radius:10px}.impact-grid b{font-size:18px}
.recycle-head{margin:14px 0}.recycle-item{display:flex;align-items:center;justify-content:space-between;padding:12px;margin-bottom:8px}
.recycle-item>div{display:flex;align-items:center;gap:10px}.recycle-item span{display:flex;flex-direction:column}
.drawer-footer{width:100%;font-size:12px;color:var(--vt-fg-muted)}.dirty-dot{color:#d97706}
@media(max-width:720px){.identity-card,.two-column{grid-template-columns:1fr}.wide{grid-column:auto}.impact-grid{grid-template-columns:1fr 1fr}.drawer-footer{align-items:flex-start;flex-direction:column}}
</style>
