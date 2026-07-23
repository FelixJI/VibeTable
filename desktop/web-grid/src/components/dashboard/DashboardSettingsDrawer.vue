<script setup lang="ts">
import { ref, toRaw, watch } from "vue";
import { NButton, NDrawer, NDrawerContent, NInput, NSelect } from "naive-ui";
import type { DashboardFilterVariablePayload, DashboardInteractionPayload } from "@/contracts";
import type { DashboardManagedConfig } from "@/stores/dashboardStore";
import type { DashboardPanel } from "@/dashboard";
import { t } from "@/i18n";

const props = defineProps<{ show: boolean; name: string; note: string; config: DashboardManagedConfig; panels: readonly DashboardPanel[] }>();
const emit = defineEmits<{ close: []; submit: [name: string, note: string, config: DashboardManagedConfig] }>();
const name = ref("");
const note = ref("");
const filters = ref<DashboardFilterVariablePayload[]>([]);
const interactions = ref<DashboardInteractionPayload[]>([]);
const filterTypes = ["date-range", "enum", "user", "relation", "number-range"].map((value) => ({ value, label: t(`dashboard.filterType.${value}`) }));

watch(() => props.show, (show) => {
  if (!show) return;
  name.value = props.name;
  note.value = props.note;
  filters.value = structuredClone(toRaw(props.config.globalFilters)) as DashboardFilterVariablePayload[];
  interactions.value = structuredClone(toRaw(props.config.interactions)) as DashboardInteractionPayload[];
});

function addFilter(): void {
  filters.value.push({ key: `filter_${filters.value.length + 1}`, label: t("dashboard.filters.new"), type: "enum", allowedFields: [], targetPanels: [] });
}
function updateFilter(index: number, key: keyof DashboardFilterVariablePayload, value: unknown): void {
  const normalized = key === "allowedFields" || key === "targetPanels"
    ? csv(value)
    : key === "fieldBindings" ? parseBindings(value) : value;
  filters.value[index] = { ...filters.value[index], [key]: normalized } as DashboardFilterVariablePayload;
}
function addInteraction(): void {
  const source = props.panels[0]?.id ?? "";
  interactions.value.push({ sourcePanelId: source, sourceField: null, targetPanelIds: [], targetField: "" });
}
function updateInteraction(index: number, key: keyof DashboardInteractionPayload, value: unknown): void {
  interactions.value[index] = { ...interactions.value[index], [key]: key === "targetPanelIds" ? csv(value) : value } as DashboardInteractionPayload;
}
function csv(value: unknown): string[] { return String(value ?? "").split(",").map((item) => item.trim()).filter(Boolean); }
function parseBindings(value: unknown): Record<string, string> {
  return Object.fromEntries(csv(value).flatMap((item) => {
    const separator = item.indexOf(":");
    const panelId = item.slice(0, separator).trim();
    const field = item.slice(separator + 1).trim();
    return separator > 0 && panelId && field ? [[panelId, field]] : [];
  }));
}
function formatBindings(value: Readonly<Record<string, string>> | undefined): string {
  return Object.entries(value ?? {}).map(([panelId, field]) => `${panelId}:${field}`).join(", ");
}
function submit(): void {
  emit("submit", name.value.trim(), note.value.trim(), { ...props.config, globalFilters: filters.value, interactions: interactions.value });
}
</script>

<template>
  <NDrawer :show="show" :width="520" @mask-click="emit('close')">
    <NDrawerContent :title="t('dashboard.settings.title')" closable @close="emit('close')">
      <div class="settings-form">
        <label>{{ t("dashboard.field.name") }}<NInput v-model:value="name" maxlength="128" /></label>
        <label>{{ t("dashboard.field.note") }}<NInput v-model:value="note" type="textarea" maxlength="512" /></label>
        <section><header><div><strong>{{ t("dashboard.filters.title") }}</strong><small>{{ t("dashboard.filters.hint") }}</small></div><NButton size="tiny" @click="addFilter">{{ t("common.add") }}</NButton></header>
          <div v-for="(filter, index) in filters" :key="`${filter.key}:${index}`" class="config-card">
            <NInput size="small" :value="filter.label" :placeholder="t('dashboard.filters.label')" @update:value="updateFilter(index, 'label', $event)" />
            <NInput size="small" :value="filter.key" :placeholder="t('dashboard.filters.key')" @update:value="updateFilter(index, 'key', $event)" />
            <NSelect size="small" :value="filter.type" :options="filterTypes" @update:value="updateFilter(index, 'type', $event)" />
            <NInput size="small" :value="filter.allowedFields.join(', ')" :placeholder="t('dashboard.filters.field')" @update:value="updateFilter(index, 'allowedFields', $event)" />
            <NInput size="small" :value="filter.targetPanels.join(', ')" :placeholder="t('dashboard.filters.targets')" @update:value="updateFilter(index, 'targetPanels', $event)" />
            <NInput size="small" :value="formatBindings(filter.fieldBindings)" :placeholder="t('dashboard.filters.bindings')" @update:value="updateFilter(index, 'fieldBindings', $event)" />
            <NButton size="tiny" quaternary type="error" @click="filters.splice(index, 1)">{{ t("common.delete") }}</NButton>
          </div>
        </section>
        <section><header><div><strong>{{ t("dashboard.interactions.title") }}</strong><small>{{ t("dashboard.interactions.hint") }}</small></div><NButton size="tiny" @click="addInteraction">{{ t("common.add") }}</NButton></header>
          <div v-for="(item, index) in interactions" :key="index" class="config-card interaction-card">
            <NSelect size="small" :value="item.sourcePanelId" :options="panels.map((panel) => ({ label: panel.name, value: panel.id }))" @update:value="updateInteraction(index, 'sourcePanelId', $event)" />
            <NInput size="small" :value="item.sourceField ?? ''" :placeholder="t('dashboard.interactions.sourceField')" @update:value="updateInteraction(index, 'sourceField', $event || null)" />
            <NInput size="small" :value="item.targetPanelIds.join(', ')" :placeholder="t('dashboard.filters.targets')" @update:value="updateInteraction(index, 'targetPanelIds', $event)" />
            <NInput size="small" :value="item.targetField" :placeholder="t('dashboard.filters.field')" @update:value="updateInteraction(index, 'targetField', $event)" />
            <NButton size="tiny" quaternary type="error" @click="interactions.splice(index, 1)">{{ t("common.delete") }}</NButton>
          </div>
        </section>
      </div>
      <template #footer><div class="drawer-actions"><NButton @click="emit('close')">{{ t("common.cancel") }}</NButton><NButton type="primary" :disabled="!name.trim()" @click="submit">{{ t("common.confirm") }}</NButton></div></template>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.settings-form { display:grid; gap:18px; }
.settings-form>label { display:grid; gap:6px; color:var(--vt-fg-muted); font-size:12px; }
section { display:grid; gap:9px; padding-top:4px; border-top:1px solid var(--vt-border-subtle); }
section header { display:flex; align-items:center; justify-content:space-between; }
section header div { display:grid; gap:2px; } section small { color:var(--vt-fg-subtle); }
.config-card { display:grid; grid-template-columns:1fr 1fr; gap:7px; padding:9px; border:1px solid var(--vt-border); border-radius:var(--vt-radius-md); background:var(--vt-bg-subtle); }
.config-card>:last-child { justify-self:end; }
.interaction-card { grid-template-columns:1fr 1fr; }
.drawer-actions { display:flex; justify-content:flex-end; gap:8px; width:100%; }
</style>
