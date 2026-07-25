<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import {
  NAlert, NButton, NCheckbox, NDrawer, NDrawerContent, NEmpty, NInput,
  NInputNumber, NSelect, NSwitch, NTabPane, NTabs, NTag,
} from "naive-ui";
import type {
  ApplyRelationChangeParams, JunctionContextFieldConfig, LookupAggregation,
  LookupDefinition, LookupOutputType, LookupQueryResult, LookupSource,
  LookupValidationResult, NormalizedRelationDescriptor, PreviewRelationChangeParams,
  RelationChangeConfig, RelationChangePlan, RelationDeletePolicy, RelationKind,
  RelationPreset, SchemaSnapshot,
} from "@/contracts";
import { lookupSourceOptions, resolveLookupPathCollection } from "./fieldManagerModel";

type ContextType = JunctionContextFieldConfig["type"];
type ContextDraft = Omit<JunctionContextFieldConfig, "type" | "defaultValue"> & {
  type: ContextType | "";
  defaultText: string;
};
type MappingDraft = { collection: string; fieldRef: string };
type PathDraft = { relationId: string; m2aCollection: string | null };

const props = defineProps<{
  show: boolean;
  collection: string;
  collections: readonly string[];
  schema: SchemaSnapshot | null;
  schemas: readonly SchemaSnapshot[];
  lookups: readonly LookupDefinition[];
  lookupCatalog: readonly LookupDefinition[];
  busy: boolean;
  error: string | null;
  relationPlan: RelationChangePlan | null;
  lookupValidation: LookupValidationResult | null;
  lookupPreview: LookupQueryResult | null;
}>();

const emit = defineEmits<{
  close: [];
  resetRelationPreview: [];
  previewRelation: [params: PreviewRelationChangeParams];
  applyRelation: [params: ApplyRelationChangeParams];
  validateLookup: [definition: LookupDefinition];
  previewLookup: [definition: LookupDefinition];
  createLookup: [definition: LookupDefinition];
  updateLookup: [definition: LookupDefinition];
  deleteLookup: [definition: LookupDefinition];
  loadSchema: [collection: string];
}>();

const activeTab = ref("relations");
const relationAction = ref<"create" | "update" | "delete">("create");
const selectedRelationId = ref<string | null>(null);
const selectedCascadeLookupIds = ref<string[]>([]);
const lookupMode = ref<"create" | "update">("create");
const selectedLookupId = ref<string | null>(null);
const deleteArmed = ref(false);

const relationForm = reactive({
  fieldKey: "", fieldDisplayName: "", kind: "m2o" as RelationKind,
  relatedCollection: "", allowedCollections: [] as string[], unique: false,
  nullable: true, onDelete: "nullify" as RelationDeletePolicy, displayTemplate: "",
  preset: "standard" as RelationPreset,
  relatedManyField: "", junctionCollection: "", sourceField: "", targetField: "",
  collectionField: "", sortField: "", context: [] as ContextDraft[],
});

const lookupForm = reactive({
  lookupId: "", fieldKey: "", displayName: "", path: [] as PathDraft[],
  sourceKind: "target_field" as LookupSource["kind"], sourceFieldRef: "",
  sourceLookupId: "", aggregation: "single" as LookupAggregation,
  outputType: "text" as LookupOutputType, outputScale: null as number | null,
  mappings: [] as MappingDraft[], revision: 1,
});

const relationOptions = computed(() => (props.schema?.normalizedRelations ?? []).map((item) => ({
  label: `${item.fieldRef} · ${item.kind.toUpperCase()}`,
  value: item.relationId,
})));
const relationCatalog = computed(() => props.schemas.flatMap((snapshot) => snapshot.normalizedRelations));
const collectionOptions = computed(() => props.collections.map((value) => ({ label: value, value })));
const finalPathCollection = computed(() => resolveLookupPathCollection(
  props.collection,
  lookupForm.path,
  relationCatalog.value,
));
const lookupOptions = computed(() => lookupSourceOptions(props.lookupCatalog, finalPathCollection.value));
const kindOptions = ["m2o", "o2m", "m2m", "m2a"].map((value) => ({ label: value.toUpperCase(), value }));
const onDeleteOptions = ["nullify", "restrict", "cascade"].map((value) => ({ label: value, value }));
const presetOptions = computed(() => {
  const values = relationForm.kind === "m2o"
    ? ["standard", "file"]
    : relationForm.kind === "o2m"
      ? ["standard", "translations"]
      : relationForm.kind === "m2m"
        ? ["standard", "files"]
        : ["standard"];
  return values.map((value) => ({ label: value, value }));
});
const contextTypeOptions = [
  "string", "text", "integer", "bigInteger", "decimal", "float", "boolean",
  "date", "dateTime", "time", "json", "uuid",
].map((value) => ({ label: value, value }));
const sourceOptions = [
  { label: "目标字段", value: "target_field" },
  { label: "中间表字段", value: "junction_field" },
  { label: "已有 Lookup", value: "lookup" },
];
const aggregationOptions = [
  "single", "values", "distinct_values", "related_count", "non_null_count",
  "sum", "average", "min", "max",
].map((value) => ({ label: value, value }));
const outputOptions = ["text", "integer", "boolean", "date", "datetime", "time", "json"]
  .map((value) => ({ label: value, value }));

const selectedRelation = computed(() => props.schema?.normalizedRelations.find(
  (item) => item.relationId === selectedRelationId.value,
) ?? null);
const relationValidation = computed(() => {
  if (relationAction.value !== "delete") {
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(relationForm.fieldKey)) return "字段键仅允许字母、数字和下划线，且不能以数字开头";
    if (!relationForm.fieldDisplayName.trim()) return "必须填写显示名称";
    if (relationForm.kind === "m2a" && relationForm.allowedCollections.length === 0) return "M2A 至少选择一个目标集合";
    if (relationForm.kind !== "m2a" && !relationForm.relatedCollection) return "必须显式选择目标集合";
    if (relationForm.preset === "file" && (relationForm.kind !== "m2o" || relationForm.relatedCollection !== "_managed_attachments")) return "file 预设必须是指向 _managed_attachments 的 M2O";
    if (relationForm.preset === "files" && (relationForm.kind !== "m2m" || relationForm.relatedCollection !== "_managed_attachments")) return "files 预设必须是指向 _managed_attachments 的 M2M";
    if (relationForm.preset === "translations" && relationForm.kind !== "o2m") return "translations 预设仅支持 O2M";
    if (relationForm.kind === "o2m" && !relationForm.relatedManyField.trim()) return "O2M 必须填写目标集合的 many field";
    if (["m2m", "m2a"].includes(relationForm.kind)
      && (!relationForm.junctionCollection || !relationForm.sourceField || !relationForm.targetField)) {
      return "多值关系必须完整填写中间表、source field 与 target field";
    }
    if (relationForm.context.some((item) => !item.field.trim() || !item.type)) return "每个 context field 都必须显式填写字段名和类型";
  } else if (!selectedRelationId.value) return "请选择要删除的关系";
  return null;
});
const lookupValidationLocal = computed(() => {
  if (!lookupForm.lookupId.trim() || !lookupForm.fieldKey.trim() || !lookupForm.displayName.trim()) return "Lookup ID、字段键和显示名称均为必填";
  if (lookupForm.path.length === 0 || lookupForm.path.some((step) => !step.relationId)) return "Lookup 路径至少包含一步完整关系";
  if (lookupForm.sourceKind === "lookup" ? !lookupForm.sourceLookupId : !lookupForm.sourceFieldRef.trim()) return "必须显式指定来源";
  if (lookupForm.outputType === "decimal" && lookupForm.outputScale === null) return "decimal 输出需显式指定精度";
  return null;
});

watch(() => props.show, (show) => { if (show && !lookupForm.lookupId) resetLookup(); });
watch([relationAction, selectedRelationId], () => {
  selectedCascadeLookupIds.value = [];
  emit("resetRelationPreview");
});
watch(selectedRelation, (relation) => {
  if (relation && relationAction.value === "update") fillRelation(relation);
});

function fillRelation(relation: NormalizedRelationDescriptor): void {
  relationForm.fieldKey = relation.fieldRef.includes(".") ? relation.fieldRef.split(".").at(-1) ?? relation.fieldRef : relation.fieldRef;
  // The normalized descriptor does not carry a provider-specific display label.
  // Leave it blank so an update requires an explicit value instead of guessing.
  relationForm.fieldDisplayName = "";
  relationForm.kind = relation.kind;
  relationForm.relatedCollection = relation.relatedCollection ?? "";
  relationForm.allowedCollections = [...relation.allowedCollections];
  relationForm.unique = relation.unique;
  relationForm.nullable = relation.nullable;
  relationForm.onDelete = relation.onDelete;
  relationForm.preset = relation.preset ?? "standard";
  relationForm.displayTemplate = relation.displayTemplate ?? "";
  relationForm.relatedManyField = relation.manyField ?? "";
  relationForm.junctionCollection = relation.junction?.collection ?? "";
  relationForm.sourceField = relation.junction?.sourceField ?? "";
  relationForm.targetField = relation.junction?.targetField ?? "";
  relationForm.collectionField = relation.junction?.collectionField ?? "";
  relationForm.sortField = relation.junction?.sortField ?? "";
  relationForm.context = (relation.junction?.contextFields ?? []).map((field) => ({
    field, type: "", nullable: true, defaultText: "",
  }));
}

function resetRelation(): void {
  selectedRelationId.value = null;
  Object.assign(relationForm, {
    fieldKey: "", fieldDisplayName: "", kind: "m2o", relatedCollection: "",
    allowedCollections: [], unique: false, nullable: true, onDelete: "nullify",
    preset: "standard",
    displayTemplate: "", relatedManyField: "", junctionCollection: "",
    sourceField: "", targetField: "", collectionField: "", sortField: "", context: [],
  });
  emit("resetRelationPreview");
}

function buildRelationConfig(): RelationChangeConfig {
  const common = {
    fieldKey: relationForm.fieldKey.trim(), fieldDisplayName: relationForm.fieldDisplayName.trim(),
    nullable: relationForm.nullable, onDelete: relationForm.onDelete,
    preset: relationForm.preset,
    displayTemplate: relationForm.displayTemplate.trim() || null,
  };
  if (relationForm.kind === "m2o") return {
    ...common, kind: "m2o", relatedCollection: relationForm.relatedCollection, unique: relationForm.unique,
  };
  if (relationForm.kind === "o2m") return {
    ...common, kind: "o2m", relatedCollection: relationForm.relatedCollection,
    relatedManyField: relationForm.relatedManyField.trim(),
  };
  const junction = {
    collection: relationForm.junctionCollection.trim(), sourceField: relationForm.sourceField.trim(),
    targetField: relationForm.targetField.trim(), collectionField: relationForm.collectionField.trim() || null,
    sortField: relationForm.sortField.trim() || null,
    contextFields: relationForm.context.map((item) => item.field.trim()).filter(Boolean),
  };
  const junctionContextFields = relationForm.context.map((item) => ({
    field: item.field.trim(), type: item.type as ContextType, nullable: item.nullable,
    ...parseDefault(item.defaultText),
  }));
  if (relationForm.kind === "m2m") return {
    ...common, kind: "m2m", relatedCollection: relationForm.relatedCollection, junction, junctionContextFields,
  };
  return { ...common, kind: "m2a", allowedCollections: relationForm.allowedCollections, junction, junctionContextFields };
}

function requestRelationPreview(): void {
  if (!props.schema || relationValidation.value) return;
  emit("previewRelation", {
    collection: props.collection,
    action: relationAction.value,
    relationId: relationAction.value === "create" ? null : selectedRelationId.value,
    config: relationAction.value === "delete" ? null : buildRelationConfig(),
    expectedSchemaRevision: props.schema.schemaRevision,
  });
}

function applyRelation(): void {
  if (!props.relationPlan) return;
  emit("applyRelation", {
    planId: props.relationPlan.planId,
    operationId: operationId("relation"),
    expectedSchemaRevision: props.relationPlan.expectedSchemaRevision,
    cascadeLookupIds: selectedCascadeLookupIds.value,
  });
}

function toggleCascade(lookupId: string, checked: boolean): void {
  const next = new Set(selectedCascadeLookupIds.value);
  if (checked) next.add(lookupId);
  else next.delete(lookupId);
  selectedCascadeLookupIds.value = [...next];
}

function addContext(): void {
  relationForm.context.push({ field: "", type: "", nullable: true, defaultText: "" });
}

function resetLookup(): void {
  selectedLookupId.value = null;
  lookupMode.value = "create";
  deleteArmed.value = false;
  Object.assign(lookupForm, {
    lookupId: `${props.collection}.`, fieldKey: "", displayName: "", path: [{ relationId: "", m2aCollection: null }],
    sourceKind: "target_field", sourceFieldRef: "", sourceLookupId: "", aggregation: "single",
    outputType: "text", outputScale: null, mappings: [], revision: 1,
  });
}

function editLookup(lookupId: string): void {
  const definition = props.lookups.find((item) => item.lookupId === lookupId);
  if (!definition) return;
  selectedLookupId.value = lookupId;
  lookupMode.value = "update";
  deleteArmed.value = false;
  Object.assign(lookupForm, {
    lookupId: definition.lookupId, fieldKey: definition.fieldKey, displayName: definition.displayName,
    path: definition.path.map((step) => ({ relationId: step.relationId, m2aCollection: step.m2aCollection ?? null })),
    sourceKind: definition.source.kind,
    sourceFieldRef: definition.source.kind === "lookup" ? "" : definition.source.fieldRef,
    sourceLookupId: definition.source.kind === "lookup" ? definition.source.lookupId : "",
    aggregation: definition.aggregation, outputType: definition.outputType,
    outputScale: definition.outputScale ?? null,
    mappings: definition.m2aFieldMapping.map((item) => ({ ...item })), revision: definition.revision,
  });
}

function lookupDefinition(): LookupDefinition {
  const source: LookupSource = lookupForm.sourceKind === "lookup"
    ? { kind: "lookup", lookupId: lookupForm.sourceLookupId }
    : { kind: lookupForm.sourceKind, fieldRef: lookupForm.sourceFieldRef.trim() };
  return {
    lookupId: lookupForm.lookupId.trim(), collection: props.collection,
    fieldKey: lookupForm.fieldKey.trim(), displayName: lookupForm.displayName.trim(),
    path: lookupForm.path.map((step) => ({ relationId: step.relationId, m2aCollection: step.m2aCollection })),
    source, m2aFieldMapping: lookupForm.mappings.map((item) => ({ ...item })),
    aggregation: lookupForm.aggregation, outputType: lookupForm.outputType,
    outputScale: lookupForm.outputType === "decimal" ? lookupForm.outputScale : null,
    revision: lookupForm.revision, state: "valid", diagnostics: [],
    dependencies: source.kind === "lookup" ? [source.lookupId] : [],
  };
}

function relationOptionsForPath(index: number) {
  let source = props.collection;
  for (let position = 0; position < index; position += 1) {
    const step = lookupForm.path[position];
    const relation = relationCatalog.value.find((item) => item.relationId === step?.relationId);
    if (!relation) return [];
    source = relation.kind === "m2a" ? step?.m2aCollection ?? "" : relation.relatedCollection ?? "";
  }
  return relationCatalog.value
    .filter((item) => item.sourceCollection === source)
    .map((item) => ({ label: `${item.fieldRef} · ${item.kind.toUpperCase()}`, value: item.relationId }));
}

function relationAtPath(index: number): NormalizedRelationDescriptor | null {
  return relationCatalog.value.find((item) => item.relationId === lookupForm.path[index]?.relationId) ?? null;
}

function pathChanged(index: number): void {
  lookupForm.path[index]!.m2aCollection = null;
  lookupForm.path.splice(index + 1);
  const relation = relationAtPath(index);
  if (relation?.kind !== "m2a" && relation?.relatedCollection) emit("loadSchema", relation.relatedCollection);
}

function m2aPathCollectionChanged(index: number, collection: string): void {
  lookupForm.path[index]!.m2aCollection = collection;
  lookupForm.path.splice(index + 1);
  if (collection) emit("loadSchema", collection);
}

function removeLookup(): void {
  const definition = props.lookups.find((item) => item.lookupId === selectedLookupId.value);
  if (!definition) return;
  if (!deleteArmed.value) { deleteArmed.value = true; return; }
  emit("deleteLookup", definition);
  deleteArmed.value = false;
}

function parseDefault(text: string): { defaultValue?: unknown } {
  if (!text.trim()) return {};
  try { return { defaultValue: JSON.parse(text) }; } catch { return { defaultValue: text }; }
}
function operationId(prefix: string): string {
  return globalThis.crypto?.randomUUID?.() ?? `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}
</script>

<template>
  <NDrawer :show="show" :width="760" placement="right" @mask-click="emit('close')">
    <NDrawerContent closable class="field-manager" @close="emit('close')">
      <template #header>
        <div class="drawer-title">
          <strong>关系与 Lookup 字段</strong>
          <span>{{ collection }} · schema {{ schema?.schemaRevision ?? "—" }}</span>
        </div>
      </template>
      <NAlert v-if="error" type="error" :show-icon="false" class="top-alert">{{ error }}</NAlert>
      <NTabs v-model:value="activeTab" type="line" animated>
        <NTabPane name="relations" tab="关系字段">
          <div class="manager-grid">
            <aside class="record-list">
              <div class="list-head"><strong>关系</strong><NButton size="tiny" @click="relationAction = 'create'; resetRelation()">新建</NButton></div>
              <button
                v-for="relation in schema?.normalizedRelations ?? []" :key="relation.relationId"
                type="button" :class="{ active: selectedRelationId === relation.relationId }"
                @click="selectedRelationId = relation.relationId; relationAction = 'update'"
              >
                <span>{{ relation.fieldRef }}</span><small>{{ relation.kind.toUpperCase() }} · {{ relation.state }}</small>
              </button>
              <NEmpty v-if="!schema?.normalizedRelations.length" size="small" description="暂无关系" />
            </aside>
            <section class="editor-form">
              <div class="mode-row">
                <NSelect v-model:value="relationAction" size="small" :options="[
                  { label: '创建', value: 'create' }, { label: '更新', value: 'update' }, { label: '删除', value: 'delete' },
                ]" />
                <NSelect v-if="relationAction !== 'create'" v-model:value="selectedRelationId" size="small" :options="relationOptions" placeholder="选择关系" />
              </div>
              <template v-if="relationAction !== 'delete'">
                <div class="form-grid two"><label>字段键<NInput v-model:value="relationForm.fieldKey" size="small" /></label><label>显示名称<NInput v-model:value="relationForm.fieldDisplayName" size="small" /></label></div>
                <div class="form-grid two"><label>类型<NSelect v-model:value="relationForm.kind" size="small" :options="kindOptions" /></label><label>预设<NSelect v-model:value="relationForm.preset" size="small" :options="presetOptions" /></label></div>
                <label>删除策略<NSelect v-model:value="relationForm.onDelete" size="small" :options="onDeleteOptions" /></label>
                <label v-if="relationForm.kind !== 'm2a'">目标集合<NSelect v-model:value="relationForm.relatedCollection" size="small" filterable :options="collectionOptions" placeholder="显式选择" /></label>
                <label v-else>允许的目标集合<NSelect v-model:value="relationForm.allowedCollections" multiple size="small" filterable :options="collectionOptions" placeholder="逐项选择" /></label>
                <div class="switch-row"><span>允许空值 <small>nullable</small></span><NSwitch v-model:value="relationForm.nullable" size="small" /></div>
                <div v-if="relationForm.kind === 'm2o'" class="switch-row"><span>一对一唯一约束 <small>unique O2O</small></span><NSwitch v-model:value="relationForm.unique" size="small" /></div>
                <label>显示模板 <small>可留空；系统不会猜测显示字段</small><NInput v-model:value="relationForm.displayTemplate" size="small" placeholder="例如 {{number}} · {{title}}" /></label>
                <label v-if="relationForm.kind === 'o2m'">目标集合 many field<NInput v-model:value="relationForm.relatedManyField" size="small" /></label>
                <fieldset v-if="relationForm.kind === 'm2m' || relationForm.kind === 'm2a'">
                  <legend>中间表结构</legend>
                  <div class="form-grid two"><label>collection<NInput v-model:value="relationForm.junctionCollection" size="small" /></label><label>source field<NInput v-model:value="relationForm.sourceField" size="small" /></label></div>
                  <div class="form-grid two"><label>target field<NInput v-model:value="relationForm.targetField" size="small" /></label><label>collection field<NInput v-model:value="relationForm.collectionField" size="small" :disabled="relationForm.kind !== 'm2a'" /></label></div>
                  <label>sort field<NInput v-model:value="relationForm.sortField" size="small" /></label>
                  <div class="subhead"><strong>Typed context fields</strong><NButton size="tiny" @click="addContext">添加</NButton></div>
                  <div v-for="(item, index) in relationForm.context" :key="index" class="context-row">
                    <NInput v-model:value="item.field" size="small" placeholder="field" />
                    <NSelect v-model:value="item.type" size="small" :options="contextTypeOptions" placeholder="显式类型" />
                    <NInput v-model:value="item.defaultText" size="small" placeholder="default (JSON)" />
                    <NCheckbox v-model:checked="item.nullable">空</NCheckbox>
                    <NButton size="tiny" quaternary type="error" @click="relationForm.context.splice(index, 1)">×</NButton>
                  </div>
                </fieldset>
              </template>
              <NAlert v-else type="warning" :show-icon="false">删除不会自动级联。必须先预览依赖与破坏性步骤，再显式选择需要一并删除的 Lookup。</NAlert>
              <p v-if="relationValidation" class="validation">{{ relationValidation }}</p>
              <div class="action-row"><NButton size="small" :disabled="!!relationValidation || busy" @click="requestRelationPreview">生成冻结预览</NButton></div>
              <section v-if="relationPlan" class="plan-card">
                <header><strong>变更计划</strong><NTag size="small" :type="relationPlan.canApply ? 'success' : 'error'">{{ relationPlan.canApply ? '可应用' : '已阻止' }}</NTag></header>
                <ol><li v-for="step in relationPlan.steps" :key="`${step.resource}:${step.key}`" :class="{ destructive: step.destructive }"><code>{{ step.action }}</code><span>{{ step.resource }} · {{ step.key }}</span><b v-if="step.destructive">破坏性</b></li></ol>
                <NAlert v-for="diagnostic in relationPlan.diagnostics" :key="diagnostic.code" :type="diagnostic.severity === 'error' ? 'error' : 'warning'" :show-icon="false">{{ diagnostic.code }} · {{ diagnostic.message }}</NAlert>
                <div v-if="relationPlan.affectedLookupIds.length" class="dependency-box">
                  <strong>受影响 Lookup（默认不级联）</strong>
                  <NCheckbox
                    v-for="lookupId in relationPlan.affectedLookupIds"
                    :key="lookupId"
                    :checked="selectedCascadeLookupIds.includes(lookupId)"
                    :data-testid="`cascade-lookup-${lookupId}`"
                    @update:checked="toggleCascade(lookupId, $event)"
                  >{{ lookupId }}</NCheckbox>
                </div>
                <NButton data-testid="apply-relation-plan" type="primary" size="small" :loading="busy" :disabled="!relationPlan.canApply || (relationPlan.affectedLookupIds.length > 0 && selectedCascadeLookupIds.length !== relationPlan.affectedLookupIds.length)" @click="applyRelation">应用此冻结计划</NButton>
              </section>
            </section>
          </div>
        </NTabPane>

        <NTabPane name="lookups" tab="Lookup">
          <div class="manager-grid">
            <aside class="record-list">
              <div class="list-head"><strong>Lookup</strong><NButton size="tiny" @click="resetLookup">新建</NButton></div>
              <button v-for="item in lookups" :key="item.lookupId" type="button" :class="{ active: selectedLookupId === item.lookupId }" @click="editLookup(item.lookupId)">
                <span>{{ item.displayName }}</span><small>{{ item.fieldKey }} · r{{ item.revision }}</small>
              </button>
              <NEmpty v-if="!lookups.length" size="small" description="暂无 Lookup" />
            </aside>
            <section class="editor-form">
              <div class="form-grid two"><label>Lookup ID<NInput v-model:value="lookupForm.lookupId" size="small" :disabled="lookupMode === 'update'" /></label><label>字段键<NInput v-model:value="lookupForm.fieldKey" size="small" /></label></div>
              <label>显示名称<NInput v-model:value="lookupForm.displayName" size="small" /></label>
              <fieldset>
                <legend>关系路径 · 不限制跳数</legend>
                <div v-for="(step, index) in lookupForm.path" :key="index" class="path-row">
                  <span>{{ index + 1 }}</span>
                  <NSelect v-model:value="step.relationId" size="small" filterable :options="relationOptionsForPath(index)" placeholder="显式选择 relationId" @update:value="pathChanged(index)" />
                  <NSelect v-if="relationAtPath(index)?.kind === 'm2a'" :value="step.m2aCollection" size="small" :options="(relationAtPath(index)?.allowedCollections ?? []).map(value => ({ label: value, value }))" placeholder="M2A collection" @update:value="m2aPathCollectionChanged(index, $event)" />
                  <NButton size="tiny" quaternary type="error" :disabled="lookupForm.path.length === 1" @click="lookupForm.path.splice(index, 1)">×</NButton>
                </div>
                <NButton size="tiny" @click="lookupForm.path.push({ relationId: '', m2aCollection: null })">增加一步</NButton>
              </fieldset>
              <div class="form-grid two"><label>来源类型<NSelect v-model:value="lookupForm.sourceKind" size="small" :options="sourceOptions" /></label><label v-if="lookupForm.sourceKind !== 'lookup'">显式 fieldRef<NInput v-model:value="lookupForm.sourceFieldRef" size="small" placeholder="目标字段或中间表字段" /></label><label v-else>终点集合 Lookup<NSelect v-model:value="lookupForm.sourceLookupId" data-testid="lookup-source-select" size="small" filterable :options="lookupOptions" :placeholder="finalPathCollection ? `${finalPathCollection} 的 Lookup` : '先完成路径'" /></label></div>
              <div class="form-grid two"><label>聚合<NSelect v-model:value="lookupForm.aggregation" size="small" :options="aggregationOptions" /></label><label>输出类型<NSelect v-model:value="lookupForm.outputType" size="small" :options="outputOptions" /></label></div>
              <label v-if="lookupForm.outputType === 'decimal'">小数精度<NInputNumber v-model:value="lookupForm.outputScale" size="small" :min="0" :max="30" /></label>
              <fieldset>
                <legend>M2A target → field 映射</legend>
                <div v-for="(mapping, index) in lookupForm.mappings" :key="index" class="mapping-row">
                  <NSelect v-model:value="mapping.collection" size="small" :options="collectionOptions" placeholder="collection" />
                  <NInput v-model:value="mapping.fieldRef" size="small" placeholder="显式 fieldRef" />
                  <NButton size="tiny" quaternary type="error" @click="lookupForm.mappings.splice(index, 1)">×</NButton>
                </div>
                <NButton size="tiny" @click="lookupForm.mappings.push({ collection: '', fieldRef: '' })">添加映射</NButton>
              </fieldset>
              <p v-if="lookupValidationLocal" class="validation">{{ lookupValidationLocal }}</p>
              <div class="action-row lookup-actions">
                <NButton size="small" :disabled="!!lookupValidationLocal || busy" @click="emit('validateLookup', lookupDefinition())">校验</NButton>
                <NButton size="small" :disabled="!!lookupValidationLocal || busy" @click="emit('previewLookup', lookupDefinition())">预览 12 行</NButton>
                <NButton type="primary" size="small" :disabled="!!lookupValidationLocal || busy" @click="lookupMode === 'create' ? emit('createLookup', lookupDefinition()) : emit('updateLookup', lookupDefinition())">{{ lookupMode === 'create' ? '创建' : '保存' }}</NButton>
                <NButton v-if="lookupMode === 'update'" type="error" size="small" ghost :disabled="busy" @click="removeLookup">{{ deleteArmed ? '再次点击确认删除' : '删除' }}</NButton>
              </div>
              <section v-if="lookupValidation" class="plan-card">
                <header><strong>服务端校验</strong><NTag size="small" :type="lookupValidation.valid ? 'success' : 'error'">{{ lookupValidation.valid ? '有效' : '无效' }}</NTag></header>
                <NAlert v-for="diagnostic in lookupValidation.diagnostics" :key="`${diagnostic.code}:${diagnostic.pathIndex}`" type="error" :show-icon="false">{{ diagnostic.code }}<span v-if="diagnostic.pathIndex != null"> · 第 {{ diagnostic.pathIndex + 1 }} 步</span> · {{ diagnostic.message }}</NAlert>
              </section>
              <section v-if="lookupPreview" class="preview-card"><strong>权威预览</strong><pre>{{ JSON.stringify(lookupPreview.rows.slice(0, 12), null, 2) }}</pre></section>
            </section>
          </div>
        </NTabPane>
      </NTabs>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.drawer-title { display: flex; min-width: 0; flex-direction: column; gap: 2px; }
.drawer-title strong { font-size: 15px; font-weight: 650; letter-spacing: .01em; }
.drawer-title span { overflow: hidden; color: var(--vt-fg-muted); font: 11px/1.3 ui-monospace, SFMono-Regular, Consolas, monospace; text-overflow: ellipsis; white-space: nowrap; }
.top-alert { margin-bottom: 8px; }
.manager-grid { display: grid; grid-template-columns: 188px minmax(0, 1fr); min-height: calc(100vh - 150px); margin: 0 -12px; border-top: 1px solid var(--vt-border); }
.record-list { min-width: 0; padding: 8px; border-right: 1px solid var(--vt-border); background: var(--vt-bg-subtle); }
.list-head, .subhead, .plan-card header { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.list-head { height: 30px; padding: 0 4px 6px; }
.record-list > button { display: flex; width: 100%; flex-direction: column; gap: 2px; margin: 1px 0; padding: 7px 8px; color: var(--vt-fg); border: 1px solid transparent; border-radius: var(--vt-radius-sm); background: transparent; text-align: left; cursor: pointer; }
.record-list > button:hover { border-color: var(--vt-border); background: var(--vt-bg); }
.record-list > button.active { border-color: var(--vt-color-primary-500); background: var(--vt-bg); box-shadow: inset 2px 0 0 var(--vt-color-primary-500); }
.record-list span { overflow: hidden; font-size: 12px; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.record-list small { overflow: hidden; color: var(--vt-fg-muted); font: 10px/1.3 ui-monospace, SFMono-Regular, Consolas, monospace; text-overflow: ellipsis; white-space: nowrap; }
.editor-form { display: flex; min-width: 0; flex-direction: column; gap: 9px; padding: 12px 14px 28px; }
.mode-row { display: grid; grid-template-columns: 120px minmax(0, 1fr); gap: 8px; padding-bottom: 9px; border-bottom: 1px solid var(--vt-border); }
.form-grid { display: grid; gap: 8px; }.form-grid.two { grid-template-columns: repeat(2, minmax(0, 1fr)); }
label { display: flex; min-width: 0; flex-direction: column; gap: 4px; color: var(--vt-fg-muted); font-size: 11px; font-weight: 600; }
label small { font-weight: 400; }
.switch-row { display: flex; align-items: center; justify-content: space-between; min-height: 30px; padding: 4px 8px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-sm); }
.switch-row span { font-size: 11px; font-weight: 600; }.switch-row small { color: var(--vt-fg-muted); font-weight: 400; }
fieldset { display: flex; min-width: 0; flex-direction: column; gap: 8px; margin: 2px 0; padding: 10px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); }
legend { padding: 0 5px; color: var(--vt-fg-muted); font: 600 10px/1 ui-monospace, SFMono-Regular, Consolas, monospace; letter-spacing: .04em; text-transform: uppercase; }
.context-row { display: grid; grid-template-columns: 1fr 120px 1fr auto 24px; gap: 5px; align-items: center; }
.path-row { display: grid; grid-template-columns: 20px minmax(0, 1fr) minmax(130px, .65fr) 24px; gap: 5px; align-items: center; }
.path-row > span { display: grid; width: 18px; height: 18px; place-items: center; color: var(--vt-bg); border-radius: 50%; background: var(--vt-fg-muted); font: 10px/1 ui-monospace, monospace; }
.mapping-row { display: grid; grid-template-columns: 180px minmax(0, 1fr) 24px; gap: 5px; }
.action-row { display: flex; justify-content: flex-end; gap: 6px; padding-top: 4px; }.lookup-actions { flex-wrap: wrap; }
.validation { margin: 0; color: var(--vt-color-error-500, #c2413a); font-size: 11px; }
.plan-card, .preview-card { display: flex; flex-direction: column; gap: 8px; padding: 10px; border: 1px solid var(--vt-border); border-left: 3px solid var(--vt-color-primary-500); border-radius: var(--vt-radius-sm); background: var(--vt-bg-subtle); }
.plan-card ol { display: flex; flex-direction: column; gap: 3px; margin: 0; padding: 0; list-style: none; }
.plan-card li { display: grid; grid-template-columns: 52px minmax(0, 1fr) auto; gap: 7px; align-items: center; min-height: 26px; padding: 3px 6px; border-bottom: 1px solid var(--vt-border); font-size: 11px; }
.plan-card li code { color: var(--vt-color-primary-500); }.plan-card li b { color: #b45309; font-size: 10px; }.plan-card li.destructive { background: color-mix(in srgb, #b45309 8%, transparent); }
.dependency-box { display: flex; flex-direction: column; gap: 5px; padding: 8px; border: 1px solid #b45309; }
.preview-card pre { max-height: 240px; margin: 0; overflow: auto; font: 10px/1.45 ui-monospace, SFMono-Regular, Consolas, monospace; white-space: pre-wrap; }
:deep(.n-drawer-body-content-wrapper) { padding: 8px 12px 0; }
@media (max-width: 760px) { .manager-grid { grid-template-columns: 138px minmax(0, 1fr); }.context-row { grid-template-columns: 1fr 100px; }.path-row { grid-template-columns: 20px 1fr 24px; }.form-grid.two { grid-template-columns: 1fr; } }
</style>
