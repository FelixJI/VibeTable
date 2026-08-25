<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NAlert, NButton, NDrawer, NDrawerContent, NInput, NSelect, NSpin } from "naive-ui";
import type { DashboardFilterVariablePayload, DashboardInteractionPayload } from "@/contracts";
import { cloneDashboardData, type DashboardManagedConfig } from "@/stores/dashboardStore";
import {
  interactiveDashboardFilterOperator,
  resolveDashboardFilterField,
  type BindingCollectionSchema,
  type DashboardPanel,
} from "@/dashboard";
import { t } from "@/i18n";

const props = defineProps<{
  show: boolean;
  name: string;
  note: string;
  config: DashboardManagedConfig;
  panels: readonly DashboardPanel[];
  loadSchema: (collectionId: string, signal: AbortSignal) => Promise<BindingCollectionSchema>;
}>();
const emit = defineEmits<{ close: []; submit: [name: string, note: string, config: DashboardManagedConfig] }>();
const name = ref("");
const note = ref("");
const filters = ref<DashboardFilterVariablePayload[]>([]);
const interactions = ref<DashboardInteractionPayload[]>([]);
const schemas = ref<Record<string, BindingCollectionSchema>>({});
const schemaLoading = ref(false);
const schemaError = ref<string | null>(null);
let schemaController: AbortController | null = null;
const filterTypes = ["date-range", "enum", "user", "relation", "number-range"].map((value) => ({
  value, label: t(`dashboard.filterType.${value}`),
}));
const panelOptions = computed(() => props.panels.filter((panel) => panel.editable && panel.query.collection)
  .map((panel) => ({ label: panel.name, value: panel.id })));
const hasIncompatibleFilterBindings = computed(() => !schemaLoading.value && filters.value.some((filter) => {
  const targetPanelIds = filter.targetPanels.length > 0
    ? filter.targetPanels
    : panelOptions.value.map((panel) => panel.value);
  return targetPanelIds.some((panelId) => {
    const field = resolveDashboardFilterField(filter, panelId);
    return !field || !fieldOptionsForFilter(filter.type, panelId).some((option) => option.value === field);
  });
}));

watch(() => props.show, (show) => {
  if (!show) {
    schemaController?.abort();
    return;
  }
  name.value = props.name;
  note.value = props.note;
  filters.value = [...cloneDashboardData(props.config.globalFilters)];
  interactions.value = [...cloneDashboardData(props.config.interactions)];
  void loadSchemas();
});

async function loadSchemas(): Promise<void> {
  schemaController?.abort();
  const controller = new AbortController();
  schemaController = controller;
  schemaLoading.value = true;
  schemaError.value = null;
  try {
    const collections = [...new Set(props.panels.flatMap((panel) =>
      typeof panel.query.collection === "string" ? [panel.query.collection] : [],
    ))];
    const loaded = await Promise.all(collections.map(async (collection) => [
      collection,
      await props.loadSchema(collection, controller.signal),
    ] as const));
    if (!controller.signal.aborted) schemas.value = Object.fromEntries(loaded);
  } catch (error) {
    if (!controller.signal.aborted) schemaError.value = error instanceof Error ? error.message : String(error);
  } finally {
    if (schemaController === controller) schemaLoading.value = false;
  }
}

function addFilter(): void {
  const type = "enum" as const;
  const panelId = panelOptions.value.find((panel) =>
    fieldOptionsForFilter(type, panel.value).length > 0,
  )?.value;
  const field = panelId ? fieldOptionsForFilter(type, panelId)[0]?.value : undefined;
  filters.value.push({
    key: `filter_${filters.value.length + 1}`,
    label: t("dashboard.filters.new"),
    type,
    allowedFields: field ? [field] : [],
    targetPanels: panelId ? [panelId] : [],
    fieldBindings: panelId && field ? { [panelId]: field } : {},
  });
}
function updateFilter(index: number, key: "label" | "key" | "type", value: unknown): void {
  const current = filters.value[index];
  if (key !== "type" || !filterType(value)) {
    filters.value[index] = { ...current, [key]: value } as DashboardFilterVariablePayload;
    return;
  }
  if (current.targetPanels.length === 0) {
    filters.value[index] = { ...current, type: value };
    return;
  }
  const bindings = Object.fromEntries(current.targetPanels.flatMap((panelId) => {
    const options = fieldOptionsForFilter(value, panelId);
    const currentField = resolveDashboardFilterField(current, panelId);
    const field = options.some((option) => option.value === currentField)
      ? currentField
      : options[0]?.value;
    return field ? [[panelId, field]] : [];
  }));
  filters.value[index] = {
    ...current,
    type: value,
    targetPanels: current.targetPanels.filter((panelId) =>
      Object.prototype.hasOwnProperty.call(bindings, panelId),
    ),
    fieldBindings: bindings,
    allowedFields: [...new Set(Object.values(bindings))],
  };
}
function updateFilterTargets(index: number, value: unknown): void {
  const current = filters.value[index];
  const targets = stringList(value).filter((panelId) =>
    fieldOptionsForFilter(current.type, panelId).length > 0,
  );
  const bindings = Object.fromEntries(targets.flatMap((panelId) => {
    const options = fieldOptionsForFilter(current.type, panelId);
    const currentField = resolveDashboardFilterField(current, panelId);
    const field = options.some((option) => option.value === currentField)
      ? currentField
      : options[0]?.value;
    return field ? [[panelId, field]] : [];
  }));
  filters.value[index] = {
    ...current,
    targetPanels: targets,
    fieldBindings: bindings,
    allowedFields: [...new Set(Object.values(bindings))],
  };
}
function updateFilterBinding(index: number, panelId: string, field: unknown): void {
  if (typeof field !== "string") return;
  const current = filters.value[index];
  if (!fieldOptionsForFilter(current.type, panelId).some((option) => option.value === field)) return;
  const bindings = { ...(current.fieldBindings ?? {}), [panelId]: field };
  filters.value[index] = {
    ...current,
    fieldBindings: bindings,
    allowedFields: [...new Set(Object.values(bindings))],
  };
}
function addInteraction(): void {
  const source = panelOptions.value[0]?.value ?? "";
  const target = panelOptions.value.find((item) => item.value !== source)?.value;
  const targetField = target ? fieldOptionsForPanel(target)[0]?.value ?? "" : "";
  interactions.value.push({
    sourcePanelId: source,
    sourceField: outputFieldOptions(source)[0]?.value ?? null,
    targetPanelIds: target ? [target] : [],
    targetField,
  });
}
function updateInteraction(index: number, patch: Partial<DashboardInteractionPayload>): void {
  const next = { ...interactions.value[index], ...patch };
  const options = targetFieldOptions(next);
  if (next.targetField && !options.some((item) => item.value === next.targetField)) {
    next.targetField = options[0]?.value ?? "";
  }
  interactions.value[index] = next;
}
function panelById(panelId: string): DashboardPanel | undefined {
  return props.panels.find((panel) => panel.id === panelId);
}
function fieldOptionsForPanel(panelId: string) {
  const collection = panelById(panelId)?.query.collection;
  if (typeof collection !== "string") return [];
  return (schemas.value[collection]?.fields ?? []).map((field) => ({ value: field.ref, label: field.label }));
}
function fieldOptionsForFilter(type: DashboardFilterVariablePayload["type"], panelId: string) {
  const collection = panelById(panelId)?.query.collection;
  if (typeof collection !== "string") return [];
  const operator = interactiveDashboardFilterOperator(type);
  return (schemas.value[collection]?.fields ?? [])
    .filter((field) => field.filterOperators.includes(operator))
    .map((field) => ({ value: field.ref, label: field.label }));
}
function outputFieldOptions(panelId: string) {
  const panel = panelById(panelId);
  if (!panel) return [];
  const refs = [
    ...stringList(panel.query.fields),
    ...stringList(panel.query.dimensions),
    ...(typeof record(panel.query.timeBucket).field === "string" ? [String(record(panel.query.timeBucket).field)] : []),
    ...array(panel.query.measures).flatMap((measure) => typeof record(measure).key === "string" ? [String(record(measure).key)] : []),
  ];
  const labels = new Map(fieldOptionsForPanel(panelId).map((item) => [item.value, item.label]));
  return [...new Set(refs)].map((value) => ({ value, label: labels.get(value) ?? value }));
}
function targetFieldOptions(interaction: DashboardInteractionPayload) {
  const sets = interaction.targetPanelIds.map((panelId) => new Map(
    fieldOptionsForPanel(panelId).map((item) => [item.value, item.label]),
  ));
  if (sets.length === 0) return [];
  return [...sets[0]!.entries()].filter(([value]) => sets.every((items) => items.has(value)))
    .map(([value, label]) => ({ value, label }));
}
function submit(): void {
  const normalizedFilters = filters.value.map((filter) => {
    if (filter.targetPanels.length === 0) return filter;
    const fieldBindings = Object.fromEntries(filter.targetPanels.flatMap((panelId) => {
      const field = resolveDashboardFilterField(filter, panelId);
      return field ? [[panelId, field]] : [];
    }));
    return {
      ...filter,
      fieldBindings,
      allowedFields: [...new Set(Object.values(fieldBindings))],
    };
  });
  emit("submit", name.value.trim(), note.value.trim(), {
    ...props.config,
    globalFilters: normalizedFilters,
    interactions: interactions.value,
  });
}
function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}
function array(value: unknown): unknown[] { return Array.isArray(value) ? value : []; }
function record(value: unknown): Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : {};
}
function filterType(value: unknown): value is DashboardFilterVariablePayload["type"] {
  return value === "date-range" || value === "enum" || value === "user"
    || value === "relation" || value === "number-range";
}
</script>

<template>
  <NDrawer :show="show" :width="600" @mask-click="emit('close')">
    <NDrawerContent :title="t('dashboard.settings.title')" closable @close="emit('close')">
      <div class="settings-form" data-testid="dashboard-settings">
        <label>{{ t("dashboard.field.name") }}<NInput v-model:value="name" maxlength="128" /></label>
        <label>{{ t("dashboard.field.note") }}<NInput v-model:value="note" type="textarea" maxlength="512" data-testid="dashboard-settings-note" /></label>
        <div v-if="schemaLoading" class="schema-state"><NSpin size="small" />{{ t("dashboard.schema.loading") }}</div>
        <NAlert v-else-if="schemaError" type="error">{{ schemaError }}</NAlert>
        <section>
          <header><div><strong>{{ t("dashboard.filters.title") }}</strong><small>{{ t("dashboard.filters.hint") }}</small></div><NButton size="tiny" :disabled="schemaLoading" data-testid="dashboard-add-filter" @click="addFilter">{{ t("common.add") }}</NButton></header>
          <NAlert v-if="hasIncompatibleFilterBindings" type="error">{{ t("dashboard.filters.incompatible") }}</NAlert>
          <div v-for="(filter, index) in filters" :key="`${filter.key}:${index}`" class="config-card">
            <NInput size="small" :value="filter.label" :placeholder="t('dashboard.filters.label')" :data-testid="`dashboard-filter-label-${index}`" @update:value="updateFilter(index, 'label', $event)" />
            <NInput size="small" :value="filter.key" :placeholder="t('dashboard.filters.key')" :data-testid="`dashboard-filter-key-${index}`" @update:value="updateFilter(index, 'key', $event)" />
            <NSelect size="small" :value="filter.type" :options="filterTypes" :data-testid="`dashboard-filter-type-${index}`" @update:value="updateFilter(index, 'type', $event)" />
            <NSelect size="small" multiple filterable :value="[...filter.targetPanels]" :options="panelOptions.filter((panel) => fieldOptionsForFilter(filter.type, panel.value).length > 0)" :placeholder="t('dashboard.filters.targets')" :data-testid="`dashboard-filter-targets-${index}`" @update:value="updateFilterTargets(index, $event)" />
            <div v-for="panelId in filter.targetPanels" :key="panelId" class="binding-line">
              <span>{{ panelById(panelId)?.name }}</span>
              <NSelect size="small" filterable :value="filter.fieldBindings?.[panelId]" :options="fieldOptionsForFilter(filter.type, panelId)" :data-testid="`dashboard-filter-binding-${index}-${panelId}`" @update:value="updateFilterBinding(index, panelId, $event)" />
            </div>
            <NButton size="tiny" quaternary type="error" @click="filters.splice(index, 1)">{{ t("common.delete") }}</NButton>
          </div>
        </section>
        <section>
          <header><div><strong>{{ t("dashboard.interactions.title") }}</strong><small>{{ t("dashboard.interactions.hint") }}</small></div><NButton size="tiny" data-testid="dashboard-add-interaction" @click="addInteraction">{{ t("common.add") }}</NButton></header>
          <div v-for="(item, index) in interactions" :key="index" class="config-card interaction-card">
            <label>{{ t("dashboard.interactions.sourcePanel") }}<NSelect size="small" :value="item.sourcePanelId" :options="panelOptions" :data-testid="`dashboard-interaction-source-${index}`" @update:value="updateInteraction(index, { sourcePanelId: String($event), sourceField: null })" /></label>
            <label>{{ t("dashboard.interactions.sourceField") }}<NSelect size="small" clearable :value="item.sourceField" :options="outputFieldOptions(item.sourcePanelId)" :data-testid="`dashboard-interaction-source-field-${index}`" @update:value="updateInteraction(index, { sourceField: $event ? String($event) : null })" /></label>
            <label>{{ t("dashboard.interactions.targetPanels") }}<NSelect size="small" multiple filterable :value="[...item.targetPanelIds]" :options="panelOptions.filter((panel) => panel.value !== item.sourcePanelId)" :data-testid="`dashboard-interaction-targets-${index}`" @update:value="updateInteraction(index, { targetPanelIds: stringList($event) })" /></label>
            <label>{{ t("dashboard.interactions.targetField") }}<NSelect size="small" filterable :value="item.targetField" :options="targetFieldOptions(item)" :data-testid="`dashboard-interaction-target-field-${index}`" @update:value="updateInteraction(index, { targetField: String($event) })" /></label>
            <NButton size="tiny" quaternary type="error" @click="interactions.splice(index, 1)">{{ t("common.delete") }}</NButton>
          </div>
        </section>
      </div>
      <template #footer><div class="drawer-actions"><NButton @click="emit('close')">{{ t("common.cancel") }}</NButton><NButton type="primary" :disabled="!name.trim() || schemaLoading || !!schemaError || hasIncompatibleFilterBindings" data-testid="dashboard-settings-submit" @click="submit">{{ t("common.confirm") }}</NButton></div></template>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.settings-form { display:grid; gap:18px; }
.settings-form>label,.interaction-card label { display:grid; gap:6px; color:var(--vt-fg-muted); font-size:12px; }
section { display:grid; gap:9px; padding-top:4px; border-top:1px solid var(--vt-border-subtle); }
section header { display:flex; align-items:center; justify-content:space-between; }
section header div { display:grid; gap:2px; } section small { color:var(--vt-fg-subtle); }
.config-card { display:grid; grid-template-columns:1fr 1fr; gap:8px; padding:11px; border:1px solid var(--vt-border); border-radius:var(--vt-radius-md); background:var(--vt-bg-subtle); }
.config-card>:last-child { justify-self:end; }
.binding-line { display:grid; grid-column:1/-1; grid-template-columns:minmax(100px,.7fr) minmax(0,1.3fr); gap:8px; align-items:center; }
.binding-line span { overflow:hidden; color:var(--vt-fg-muted); font-size:12px; text-overflow:ellipsis; white-space:nowrap; }
.interaction-card { grid-template-columns:1fr 1fr; }
.schema-state { display:flex; align-items:center; gap:8px; color:var(--vt-fg-muted); font-size:12px; }
.drawer-actions { display:flex; justify-content:flex-end; gap:8px; width:100%; }
@media (max-width:680px) { .config-card,.interaction-card { grid-template-columns:1fr; } }
</style>
