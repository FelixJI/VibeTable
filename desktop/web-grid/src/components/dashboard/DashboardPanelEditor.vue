<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NButton, NDrawer, NDrawerContent, NInput, NInputNumber, NSelect } from "naive-ui";
import { PRODUCT_PANEL_TYPES, type DashboardPanel, type ProductPanelType } from "@/dashboard";
import { t } from "@/i18n";

const props = defineProps<{
  show: boolean;
  panel: DashboardPanel | null;
  dashboardId: string;
  collections: readonly string[];
  allowedTypes: readonly ProductPanelType[];
}>();
const emit = defineEmits<{ close: []; submit: [panel: DashboardPanel] }>();
const name = ref("");
const type = ref<ProductPanelType>("metric");
const collection = ref("");
const fields = ref("");
const dimension = ref("");
const aggregate = ref<"count" | "countDistinct" | "sum" | "avg" | "min" | "max">("count");
const measureField = ref("");
const limit = ref(100);
const labelText = ref("");
const filterField = ref("");
const filterOperator = ref("eq");
const filterValue = ref("");
const sortField = ref("");
const sortDirection = ref<"asc" | "desc">("asc");
const timeField = ref("");
const timeUnit = ref<"minute" | "hour" | "day" | "week" | "month" | "quarter" | "year">("day");
const typeOptions = computed(() => PRODUCT_PANEL_TYPES
  .filter((value) => props.allowedTypes.includes(value))
  .map((value) => ({ value, label: t(`dashboard.panelType.${value}`) })));
const collectionOptions = computed(() => props.collections.map((value) => ({ value, label: value })));
const aggregateOptions = ["count", "countDistinct", "sum", "avg", "min", "max"].map((value) => ({ value, label: t(`dashboard.aggregate.${value}`) }));
const operatorOptions = ["eq", "ne", "in", "contains", "starts_with", "ends_with", "gt", "gte", "lt", "lte", "between", "is_null", "is_not_null"].map((value) => ({ value, label: value }));
const timeUnitOptions = ["minute", "hour", "day", "week", "month", "quarter", "year"].map((value) => ({ value, label: t(`dashboard.timeUnit.${value}`) }));
const isRecord = computed(() => type.value === "list");
const isLabel = computed(() => type.value === "label");
const typeAllowed = computed(() => props.allowedTypes.includes(type.value));

watch(() => [props.show, props.panel] as const, ([show, panel]) => {
  if (!show) return;
  name.value = panel?.name ?? t("dashboard.panel.newName");
  const candidate = panel?.editable && panel.productType !== "custom" && panel.productType !== "unknown"
    ? panel.productType
    : "metric";
  type.value = props.allowedTypes.includes(candidate) ? candidate : props.allowedTypes[0] ?? "metric";
  collection.value = String(panel?.query.collection ?? panel?.options.collection ?? props.collections[0] ?? "");
  fields.value = stringList(panel?.query.fields ?? panel?.options.fields).join(", ");
  dimension.value = stringList(panel?.query.dimensions ?? panel?.query.groupBy)[0] ?? "";
  const measure = Array.isArray(panel?.query.measures) ? panel?.query.measures[0] : undefined;
  aggregate.value = aggregateValue(recordValue(measure, "op")) ?? "count";
  measureField.value = String(recordValue(measure, "field") ?? "");
  limit.value = typeof panel?.query.limit === "number" ? panel.query.limit : 100;
  labelText.value = String(panel?.options.text ?? "");
  const firstFilter = Array.isArray(panel?.query.filters) ? panel.query.filters[0] : undefined;
  filterField.value = String(recordValue(firstFilter, "field") ?? "");
  filterOperator.value = String(recordValue(firstFilter, "operator") ?? "eq");
  const existingFilterValue = recordValue(firstFilter, "value");
  filterValue.value = Array.isArray(existingFilterValue) ? existingFilterValue.join(", ") : String(existingFilterValue ?? "");
  const firstSort = Array.isArray(panel?.query.sorts) ? panel.query.sorts[0] : undefined;
  sortField.value = String(recordValue(firstSort, "field") ?? "");
  sortDirection.value = recordValue(firstSort, "direction") === "desc" ? "desc" : "asc";
  const bucket = panel?.query.timeBucket;
  timeField.value = String(recordValue(bucket, "field") ?? "");
  const unit = recordValue(bucket, "unit");
  timeUnit.value = timeUnitValue(unit) ?? "day";
}, { immediate: true });

function submit(): void {
  const panelId = props.panel?.id ?? `draft:${crypto.randomUUID()}`;
  const cleanFields = fields.value.split(",").map((item) => item.trim()).filter(Boolean);
  const filters = filterField.value ? [{ field: filterField.value, operator: filterOperator.value, value: parseFilterValue(filterValue.value) }] : [];
  const query: Record<string, unknown> = isLabel.value ? {} : isRecord.value ? {
    kind: "records", collection: collection.value, fields: cleanFields, filters,
    sorts: sortField.value ? [{ field: sortField.value, direction: sortDirection.value }] : [], limit: Math.min(100, limit.value),
  } : {
    kind: "aggregate", collection: collection.value,
    dimensions: dimension.value ? [dimension.value] : [],
    measures: [{ key: "value", op: aggregate.value, field: measureField.value || null }],
    filters, timeBucket: timeField.value ? { field: timeField.value, unit: timeUnit.value, timezone: "UTC" } : null,
    limit: Math.min(100_000, limit.value), topN: ["bar", "pie", "donut"].includes(type.value) ? Math.min(100, limit.value) : null,
  };
  const options = { ...(props.panel?.options ?? {}), ...(isLabel.value ? { text: labelText.value } : {}), collection: collection.value };
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
    position: props.panel?.position ?? { x: 0, y: 0, width: type.value === "label" ? 12 : 6, height: type.value === "label" ? 1 : 4 },
    options,
    query,
    rawOptions: options,
    rawQuery: query,
  });
}

function stringList(value: unknown): string[] { return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : []; }
function recordValue(value: unknown, key: string): unknown { return typeof value === "object" && value !== null ? (value as Record<string, unknown>)[key] : undefined; }
function aggregateValue(value: unknown): typeof aggregate.value | null { return ["count", "countDistinct", "sum", "avg", "min", "max"].includes(String(value)) ? value as typeof aggregate.value : null; }
function timeUnitValue(value: unknown): typeof timeUnit.value | null { return ["minute", "hour", "day", "week", "month", "quarter", "year"].includes(String(value)) ? value as typeof timeUnit.value : null; }
function parseFilterValue(value: string): unknown {
  const parts = value.split(",").map((item) => item.trim()).filter(Boolean);
  const parsed = parts.map((item) => item !== "" && Number.isFinite(Number(item)) ? Number(item) : item);
  return parsed.length > 1 ? parsed : parsed[0] ?? null;
}
</script>

<template>
  <NDrawer :show="show" :width="390" placement="right" @mask-click="emit('close')">
    <NDrawerContent :title="panel ? t('dashboard.panel.edit') : t('dashboard.action.addPanel')" closable @close="emit('close')">
      <div class="panel-form">
        <label>{{ t("dashboard.field.name") }}<NInput v-model:value="name" maxlength="128" /></label>
        <label>{{ t("dashboard.field.type") }}<NSelect v-model:value="type" :options="typeOptions" /></label>
        <label v-if="isLabel">{{ t("dashboard.field.text") }}<NInput v-model:value="labelText" type="textarea" /></label>
        <template v-else>
          <label>{{ t("dashboard.field.collection") }}<NSelect v-model:value="collection" filterable tag :options="collectionOptions" /></label>
          <label v-if="isRecord">{{ t("dashboard.field.fields") }}<NInput v-model:value="fields" :placeholder="t('dashboard.field.commaSeparated')" /></label>
          <template v-if="isRecord">
            <label>{{ t("dashboard.field.sort") }}<NInput v-model:value="sortField" /></label>
            <label>{{ t("dashboard.field.direction") }}<NSelect v-model:value="sortDirection" :options="[{ value: 'asc', label: 'ASC' }, { value: 'desc', label: 'DESC' }]" /></label>
          </template>
          <template v-else>
            <label>{{ t("dashboard.field.dimension") }}<NInput v-model:value="dimension" /></label>
            <label>{{ t("dashboard.field.aggregate") }}<NSelect v-model:value="aggregate" :options="aggregateOptions" /></label>
            <label>{{ t("dashboard.field.measure") }}<NInput v-model:value="measureField" :disabled="aggregate === 'count'" /></label>
            <label>{{ t("dashboard.field.timeField") }}<NInput v-model:value="timeField" /></label>
            <label v-if="timeField">{{ t("dashboard.field.timeUnit") }}<NSelect v-model:value="timeUnit" :options="timeUnitOptions" /></label>
          </template>
          <div class="filter-fields">
            <label>{{ t("dashboard.field.filterField") }}<NInput v-model:value="filterField" /></label>
            <label>{{ t("dashboard.field.operator") }}<NSelect v-model:value="filterOperator" :options="operatorOptions" /></label>
            <label v-if="!['is_null','is_not_null'].includes(filterOperator)">{{ t("dashboard.field.filterValue") }}<NInput v-model:value="filterValue" :placeholder="t('dashboard.field.commaSeparated')" /></label>
          </div>
          <label>{{ t("dashboard.field.limit") }}<NInputNumber v-model:value="limit" :min="1" :max="100000" /></label>
        </template>
      </div>
      <template #footer><div class="drawer-actions"><NButton @click="emit('close')">{{ t("common.cancel") }}</NButton><NButton type="primary" :disabled="!typeAllowed || !name.trim() || (!isLabel && !collection) || (isRecord && !fields.trim())" @click="submit">{{ t("common.confirm") }}</NButton></div></template>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.panel-form { display:grid; gap:15px; }
.panel-form label { display:grid; gap:6px; color:var(--vt-fg-muted); font-size:12px; }
.filter-fields { display:grid; grid-template-columns:1fr 1fr; gap:10px; padding:10px; border:1px solid var(--vt-border); border-radius:var(--vt-radius-md); }
.filter-fields label:last-child { grid-column:1/-1; }
.drawer-actions { display:flex; justify-content:flex-end; gap:8px; width:100%; }
</style>
