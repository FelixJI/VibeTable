<script setup lang="ts">
import { computed, ref, watch } from "vue";
import {
  NAlert,
  NButton,
  NDrawer,
  NDrawerContent,
  NInput,
  NInputNumber,
  NSelect,
  NSpin,
} from "naive-ui";
import {
  PRODUCT_PANEL_TYPES,
  enforceManifestMinimum,
  validateBinding,
  type BindingCollectionSchema,
  type DashboardPanel,
  type ProductPanelType,
} from "@/dashboard";
import type {
  DashboardManifestEntryPayload,
  DashboardMeasurePayload,
  DashboardPanelQueryPayload,
  FilterCondition,
  SortCondition,
} from "@/contracts";
import { t } from "@/i18n";

type EditableMeasure = { key: string; op: DashboardMeasurePayload["op"]; field: string | null };
type EditableFilter = { field: string; operator: FilterCondition["operator"]; valueText: string };
type EditableSort = { field: string; direction: SortCondition["direction"] };
type TimeUnit = "day" | "week" | "month";

const props = defineProps<{
  show: boolean;
  panel: DashboardPanel | null;
  dashboardId: string;
  collections: readonly string[];
  allowedTypes: readonly ProductPanelType[];
  manifest: Partial<Record<ProductPanelType, DashboardManifestEntryPayload>>;
  loadSchema: (collectionId: string, signal: AbortSignal) => Promise<BindingCollectionSchema>;
}>();
const emit = defineEmits<{ close: []; submit: [panel: DashboardPanel] }>();

const name = ref("");
const type = ref<ProductPanelType>("metric");
const collection = ref("");
const fields = ref<string[]>([]);
const dimensions = ref<string[]>([]);
const measures = ref<EditableMeasure[]>([]);
const filters = ref<EditableFilter[]>([]);
const sorts = ref<EditableSort[]>([]);
const timeField = ref<string | null>(null);
const timeUnit = ref<TimeUnit>("day");
const limit = ref(100);
const topN = ref<number | null>(null);
const labelText = ref("");
const schema = ref<BindingCollectionSchema | null>(null);
const schemaLoading = ref(false);
const schemaError = ref<string | null>(null);
let schemaController: AbortController | null = null;

const typeOptions = computed(() => PRODUCT_PANEL_TYPES
  .filter((value) => props.allowedTypes.includes(value))
  .map((value) => ({ value, label: t(`dashboard.panelType.${value}`) })));
const collectionOptions = computed(() => props.collections.map((value) => ({ value, label: value })));
const fieldOptions = computed(() => (schema.value?.fields ?? []).map((field) => ({
  value: field.ref,
  label: field.label,
})));
const groupOptions = computed(() => (schema.value?.fields ?? []).filter((field) => field.groupable).map((field) => ({
  value: field.ref,
  label: field.label,
})));
const timeFieldOptions = computed(() => (schema.value?.fields ?? []).filter((field) =>
  field.dataType === "date" || field.dataType === "datetime",
).map((field) => ({ value: field.ref, label: field.label })));
const aggregateOptions = ["count", "countDistinct", "sum", "avg", "min", "max"].map((value) => ({
  value, label: t(`dashboard.aggregate.${value}`),
}));
const timeUnitOptions: Array<{ value: TimeUnit; label: string }> = ["day", "week", "month"].map((value) => ({
  value: value as TimeUnit, label: t(`dashboard.timeUnit.${value}`),
}));
const isRecord = computed(() => type.value === "list");
const isLabel = computed(() => type.value === "label");
const supportsTopN = computed(() => ["bar", "pie", "donut"].includes(type.value));
const typeAllowed = computed(() => props.allowedTypes.includes(type.value));
const draftQuery = computed<DashboardPanelQueryPayload | null>(() => buildQuery());
const diagnostics = computed(() => draftQuery.value && schema.value
  ? validateBinding(draftQuery.value, schema.value)
  : []);
const canSubmit = computed(() => typeAllowed.value && name.value.trim() !== "" && (
  isLabel.value || (
    collection.value !== "" && !schemaLoading.value && !schemaError.value &&
    diagnostics.value.every((item) => item.severity !== "error") &&
    (isRecord.value ? fields.value.length > 0 : measures.value.length > 0)
  )
));

watch(() => [props.show, props.panel] as const, ([show, panel]) => {
  if (!show) {
    schemaController?.abort();
    return;
  }
  reset(panel);
  if (!isLabel.value && collection.value) void describeCollection();
}, { immediate: true });

watch(type, (next, previous) => {
  if (!props.show || next === previous) return;
  if (next === "label") return;
  if (measures.value.length === 0 && next !== "list") addMeasure();
  if (collection.value && !schema.value) void describeCollection();
});

function reset(panel: DashboardPanel | null): void {
  name.value = panel?.name ?? t("dashboard.panel.newName");
  const candidate = panel?.editable && panel.productType !== "custom" && panel.productType !== "unknown"
    ? panel.productType
    : "metric";
  type.value = props.allowedTypes.includes(candidate) ? candidate : props.allowedTypes[0] ?? "metric";
  collection.value = typeof panel?.query.collection === "string" ? panel.query.collection : props.collections[0] ?? "";
  fields.value = stringList(panel?.query.fields);
  dimensions.value = stringList(panel?.query.dimensions);
  measures.value = parseMeasures(panel?.query.measures);
  if (measures.value.length === 0 && type.value !== "label" && type.value !== "list") addMeasure();
  filters.value = parseFilters(panel?.query.filters);
  sorts.value = parseSorts(panel?.query.sorts);
  const bucket = record(panel?.query.timeBucket);
  timeField.value = typeof bucket.field === "string" ? bucket.field : null;
  timeUnit.value = bucket.unit === "week" || bucket.unit === "month" ? bucket.unit : "day";
  limit.value = integer(panel?.query.limit, 100);
  topN.value = typeof panel?.query.topN === "number" ? panel.query.topN : null;
  labelText.value = String(panel?.options.text ?? "");
  schema.value = null;
  schemaError.value = null;
}

async function describeCollection(): Promise<void> {
  const selected = collection.value;
  schemaController?.abort();
  const controller = new AbortController();
  schemaController = controller;
  schemaLoading.value = true;
  schemaError.value = null;
  try {
    const result = await props.loadSchema(selected, controller.signal);
    if (!controller.signal.aborted && collection.value === selected) schema.value = result;
  } catch (error) {
    if (!controller.signal.aborted) schemaError.value = error instanceof Error ? error.message : String(error);
  } finally {
    if (schemaController === controller) schemaLoading.value = false;
  }
}

function addMeasure(): void {
  measures.value.push({ key: `value${measures.value.length + 1}`, op: "count", field: null });
}
function addFilter(): void {
  filters.value.push({ field: schema.value?.fields[0]?.ref ?? "", operator: "eq", valueText: "" });
}
function addSort(): void {
  sorts.value.push({ field: schema.value?.fields[0]?.ref ?? "", direction: "asc" });
}
function removeAt<T>(items: T[], index: number): void { items.splice(index, 1); }
function measureFieldOptions(measure: EditableMeasure) {
  return (schema.value?.fields ?? []).filter((field) => field.summaryOperations.includes(measure.op))
    .map((field) => ({ value: field.ref, label: field.label }));
}
function filterOperatorOptions(filter: EditableFilter) {
  const field = schema.value?.fields.find((item) => item.ref === filter.field);
  return (field?.filterOperators ?? []).map((value) => ({ value, label: value }));
}
function updateFilterField(filter: EditableFilter): void {
  const field = schema.value?.fields.find((item) => item.ref === filter.field);
  if (field && !field.filterOperators.includes(filter.operator)) {
    filter.operator = field.filterOperators[0] as FilterCondition["operator"] ?? "eq";
  }
}
function repairDrift(): void {
  const valid = new Set((schema.value?.fields ?? []).map((field) => field.ref));
  fields.value = fields.value.filter((field) => valid.has(field));
  dimensions.value = dimensions.value.filter((field) => valid.has(field));
  measures.value = measures.value.filter((measure) => !measure.field || valid.has(measure.field));
  filters.value = filters.value.filter((filter) => valid.has(filter.field));
  sorts.value = sorts.value.filter((sort) => valid.has(sort.field));
  if (timeField.value && !valid.has(timeField.value)) timeField.value = null;
}

function buildQuery(): DashboardPanelQueryPayload | null {
  if (isLabel.value || !collection.value) return null;
  const parsedFilters: FilterCondition[] = filters.value.filter((filter) => filter.field).map((filter) => ({
    field: filter.field,
    operator: filter.operator,
    value: ["is_null", "is_not_null"].includes(filter.operator) ? null : parseFilterValue(filter.valueText),
  }));
  if (isRecord.value) {
    return {
      kind: "records", collection: collection.value, fields: [...fields.value], filters: parsedFilters,
      sorts: sorts.value.filter((sort) => sort.field).map((sort) => ({ ...sort })),
      limit: Math.min(100, Math.max(1, limit.value)),
    };
  }
  return {
    kind: "aggregate", collection: collection.value, dimensions: [...dimensions.value],
    measures: measures.value.map((measure) => ({ ...measure, field: measure.op === "count" ? measure.field : measure.field })),
    filters: parsedFilters,
    timeBucket: timeField.value ? { field: timeField.value, unit: timeUnit.value, timezone: "UTC" } : null,
    limit: Math.min(5_000, Math.max(1, limit.value)),
    topN: supportsTopN.value && topN.value ? Math.min(5_000, Math.max(1, topN.value)) : null,
  };
}

function submit(): void {
  const query = buildQuery();
  if (!canSubmit.value || (!isLabel.value && !query)) return;
  const panelId = props.panel?.id ?? `draft:${crypto.randomUUID()}`;
  const manifest = props.manifest[type.value];
  const minSize = manifest?.minSize ?? { x: 0, y: 0, width: 4, height: 3 };
  const options = {
    ...(props.panel?.options ?? {}),
    ...(isLabel.value ? { text: labelText.value } : {}),
  };
  const wireQuery = query
    ? structuredClone(query) as unknown as Readonly<Record<string, unknown>>
    : {};
  emit("submit", {
    ...(props.panel ?? {} as DashboardPanel),
    id: panelId,
    dashboardId: props.dashboardId,
    name: name.value.trim() || t("dashboard.panel.untitled"),
    note: props.panel?.note ?? null,
    icon: props.panel?.icon ?? null,
    color: props.panel?.color ?? null,
    showHeader: props.panel?.showHeader ?? true,
    type: type.value,
    rawType: type.value,
    productType: type.value,
    editable: true,
    position: enforceManifestMinimum(
      props.panel?.position ?? { ...minSize, x: 0, y: 0 },
      manifest,
    ),
    options,
    query: wireQuery,
    rawOptions: options,
    rawQuery: wireQuery,
  });
}

function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}
function record(value: unknown): Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : {};
}
function parseMeasures(value: unknown): EditableMeasure[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item, index) => {
    const source = record(item);
    const op = source.op;
    if (!isAggregate(op)) return [];
    return [{ key: typeof source.key === "string" ? source.key : `value${index + 1}`, op, field: typeof source.field === "string" ? source.field : null }];
  });
}
function parseFilters(value: unknown): EditableFilter[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item) => {
    const source = record(item);
    if (typeof source.field !== "string" || !isFilterOperator(source.operator)) return [];
    return [{ field: source.field, operator: source.operator, valueText: filterValueText(source.value) }];
  });
}
function parseSorts(value: unknown): EditableSort[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item) => {
    const source = record(item);
    return typeof source.field === "string"
      ? [{ field: source.field, direction: source.direction === "desc" ? "desc" as const : "asc" as const }]
      : [];
  });
}
function isAggregate(value: unknown): value is DashboardMeasurePayload["op"] {
  return ["count", "countDistinct", "sum", "avg", "min", "max"].includes(String(value));
}
function isFilterOperator(value: unknown): value is FilterCondition["operator"] {
  return ["eq", "ne", "in", "contains", "starts_with", "ends_with", "gt", "gte", "lt", "lte", "between", "is_null", "is_not_null"].includes(String(value));
}
function filterValueText(value: unknown): string {
  return typeof value === "string" ? value : value === undefined ? "" : JSON.stringify(value);
}
function parseFilterValue(value: string): unknown {
  const trimmed = value.trim();
  if (!trimmed) return null;
  try { return JSON.parse(trimmed); } catch { return trimmed; }
}
function integer(value: unknown, fallback: number): number {
  return typeof value === "number" && Number.isInteger(value) ? value : fallback;
}
</script>

<template>
  <NDrawer :show="show" :width="520" placement="right" @mask-click="emit('close')">
    <NDrawerContent :title="panel ? t('dashboard.panel.edit') : t('dashboard.action.addPanel')" closable @close="emit('close')">
      <div class="panel-form" data-testid="dashboard-panel-editor">
        <div class="identity-row">
          <label>{{ t("dashboard.field.name") }}<NInput v-model:value="name" maxlength="128" data-testid="dashboard-panel-name" /></label>
          <label>{{ t("dashboard.field.type") }}<NSelect v-model:value="type" :options="typeOptions" data-testid="dashboard-panel-type" /></label>
        </div>
        <label v-if="isLabel">{{ t("dashboard.field.text") }}<NInput v-model:value="labelText" type="textarea" /></label>
        <template v-else>
          <label>{{ t("dashboard.field.collection") }}<NSelect v-model:value="collection" filterable :options="collectionOptions" data-testid="dashboard-panel-collection" @update:value="describeCollection" /></label>
          <div v-if="schemaLoading" class="schema-state"><NSpin size="small" />{{ t("dashboard.schema.loading") }}</div>
          <NAlert v-else-if="schemaError" type="error">{{ schemaError }}</NAlert>
          <NAlert v-if="diagnostics.length" type="warning" data-testid="dashboard-binding-drift">
            <ul><li v-for="item in diagnostics" :key="`${item.path}:${item.code}`">{{ item.message }}</li></ul>
            <NButton
              size="small"
              data-testid="dashboard-repair-bindings"
              @click="repairDrift"
            >
              {{ t("dashboard.action.repairBindings") }}
            </NButton>
          </NAlert>
          <label v-if="isRecord">{{ t("dashboard.field.fields") }}<NSelect v-model:value="fields" multiple filterable :options="fieldOptions" data-testid="dashboard-panel-fields" /></label>
          <template v-else>
            <label>{{ t("dashboard.field.dimension") }}<NSelect v-model:value="dimensions" multiple filterable :options="groupOptions" data-testid="dashboard-panel-dimensions" /></label>
            <section class="binding-section">
              <header><strong>{{ t("dashboard.field.measures") }}</strong><NButton size="tiny" @click="addMeasure">{{ t("dashboard.action.addBinding") }}</NButton></header>
              <div v-for="(measure, index) in measures" :key="index" class="binding-row binding-row--measure">
                <NInput v-model:value="measure.key" :placeholder="t('dashboard.field.measureKey')" :data-testid="`dashboard-panel-measure-key-${index}`" />
                <NSelect v-model:value="measure.op" :options="aggregateOptions" :data-testid="`dashboard-panel-measure-op-${index}`" />
                <NSelect v-model:value="measure.field" clearable filterable :disabled="measure.op === 'count'" :options="measureFieldOptions(measure)" :data-testid="`dashboard-panel-measure-field-${index}`" />
                <NButton quaternary type="error" @click="removeAt(measures, index)">×</NButton>
              </div>
            </section>
            <div class="time-row">
              <label>{{ t("dashboard.field.timeField") }}<NSelect v-model:value="timeField" clearable filterable :options="timeFieldOptions" /></label>
              <label v-if="timeField">{{ t("dashboard.field.timeUnit") }}<NSelect v-model:value="timeUnit" :options="timeUnitOptions" /></label>
            </div>
          </template>
          <section class="binding-section">
            <header><strong>{{ t("dashboard.field.filters") }}</strong><NButton size="tiny" @click="addFilter">{{ t("dashboard.action.addBinding") }}</NButton></header>
            <div v-for="(filter, index) in filters" :key="index" class="binding-row">
              <NSelect v-model:value="filter.field" filterable :options="fieldOptions" @update:value="updateFilterField(filter)" />
              <NSelect v-model:value="filter.operator" :options="filterOperatorOptions(filter)" />
              <NInput v-if="!['is_null','is_not_null'].includes(filter.operator)" v-model:value="filter.valueText" />
              <span v-else />
              <NButton quaternary type="error" @click="removeAt(filters, index)">×</NButton>
            </div>
          </section>
          <section v-if="isRecord" class="binding-section">
            <header><strong>{{ t("dashboard.field.sorts") }}</strong><NButton size="tiny" @click="addSort">{{ t("dashboard.action.addBinding") }}</NButton></header>
            <div v-for="(sort, index) in sorts" :key="index" class="binding-row binding-row--sort">
              <NSelect v-model:value="sort.field" filterable :options="fieldOptions" />
              <NSelect v-model:value="sort.direction" :options="[{ value: 'asc', label: 'ASC' }, { value: 'desc', label: 'DESC' }]" />
              <NButton quaternary type="error" @click="removeAt(sorts, index)">×</NButton>
            </div>
          </section>
          <div class="limit-row">
            <label>{{ t("dashboard.field.limit") }}<NInputNumber v-model:value="limit" :min="1" :max="isRecord ? 100 : 5000" /></label>
            <label v-if="supportsTopN">Top N<NInputNumber v-model:value="topN" clearable :min="1" :max="5000" /></label>
          </div>
        </template>
      </div>
      <template #footer>
        <div class="drawer-actions">
          <NButton @click="emit('close')">{{ t("common.cancel") }}</NButton>
          <NButton type="primary" :disabled="!canSubmit" data-testid="dashboard-panel-submit" @click="submit">{{ t("common.confirm") }}</NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.panel-form { display:grid; gap:16px; }
.panel-form label { display:grid; gap:6px; min-width:0; color:var(--vt-fg-muted); font-size:12px; }
.identity-row,.time-row,.limit-row { display:grid; grid-template-columns:minmax(0,1.4fr) minmax(0,1fr); gap:12px; }
.binding-section { display:grid; gap:8px; padding:12px; border:1px solid var(--vt-border); border-radius:var(--vt-radius-md); background:color-mix(in srgb,var(--vt-bg-sunken) 68%,transparent); }
.binding-section header { display:flex; align-items:center; justify-content:space-between; }
.binding-row { display:grid; grid-template-columns:minmax(0,1.1fr) minmax(0,.9fr) minmax(0,1.3fr) 32px; gap:7px; align-items:center; }
.binding-row--measure { grid-template-columns:minmax(0,.9fr) minmax(0,.9fr) minmax(0,1.3fr) 32px; }
.binding-row--sort { grid-template-columns:minmax(0,1fr) 110px 32px; }
.schema-state { display:flex; align-items:center; gap:8px; color:var(--vt-fg-muted); font-size:12px; }
.drawer-actions { display:flex; justify-content:flex-end; gap:8px; width:100%; }
ul { margin:0; padding-left:18px; }
@media (max-width:620px) {
  .identity-row,.time-row,.limit-row { grid-template-columns:1fr; }
  .binding-row,.binding-row--measure { grid-template-columns:1fr 1fr; }
}
</style>
