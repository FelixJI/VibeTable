<script setup lang="ts">
import { NButton, NDatePicker, NIcon, NInput, NInputNumber, NSelect } from "naive-ui";
import { X } from "lucide-vue-next";
import { t } from "@/i18n";

interface FilterItem { key: string; label: string; type: string; defaultValue?: unknown }
defineProps<{ filters: readonly FilterItem[]; values: Readonly<Record<string, unknown>> }>();
const emit = defineEmits<{ change: [key: string, value: unknown]; clear: [] }>();

function range(value: unknown, index: number): number | null {
  return Array.isArray(value) && typeof value[index] === "number" ? value[index] : null;
}
function updateRange(key: string, value: unknown, index: number, next: number | null): void {
  const pair: Array<number | null> = [range(value, 0), range(value, 1)];
  pair[index] = next;
  emit("change", key, pair.every((item) => item === null) ? null : pair);
}
function selectValue(value: unknown): Array<string | number> {
  return Array.isArray(value)
    ? value.filter((item): item is string | number => typeof item === "string" || typeof item === "number")
    : [];
}
</script>

<template>
  <div v-if="filters.length" class="dashboard-filter-bar" :aria-label="t('dashboard.filters.title')">
    <div v-for="filter in filters" :key="filter.key" class="filter-control">
      <span>{{ filter.label }}</span>
      <NDatePicker v-if="filter.type === 'date-range'" size="small" type="datetimerange" clearable :value="(values[filter.key] ?? filter.defaultValue) as [number, number] | null" :data-testid="`dashboard-filter-value-${filter.key}`" @update:value="emit('change', filter.key, $event)" />
      <div v-else-if="filter.type === 'number-range'" class="number-range">
        <NInputNumber size="small" :placeholder="t('dashboard.filters.min')" :value="range(values[filter.key] ?? filter.defaultValue, 0)" :data-testid="`dashboard-filter-value-${filter.key}-min`" @update:value="updateRange(filter.key, values[filter.key] ?? filter.defaultValue, 0, $event)" />
        <i>–</i>
        <NInputNumber size="small" :placeholder="t('dashboard.filters.max')" :value="range(values[filter.key] ?? filter.defaultValue, 1)" :data-testid="`dashboard-filter-value-${filter.key}-max`" @update:value="updateRange(filter.key, values[filter.key] ?? filter.defaultValue, 1, $event)" />
      </div>
      <NSelect v-else-if="filter.type === 'enum'" size="small" multiple tag filterable clearable :options="[]" :value="selectValue(values[filter.key] ?? filter.defaultValue)" :data-testid="`dashboard-filter-value-${filter.key}`" @update:value="emit('change', filter.key, $event)" />
      <NInput v-else size="small" clearable :value="String(values[filter.key] ?? filter.defaultValue ?? '')" :data-testid="`dashboard-filter-value-${filter.key}`" @update:value="emit('change', filter.key, $event)" />
    </div>
    <NButton quaternary size="small" :aria-label="t('dashboard.filters.clear')" data-testid="dashboard-filters-clear" @click="emit('clear')"><NIcon><X /></NIcon></NButton>
  </div>
</template>

<style scoped>
.dashboard-filter-bar { display:flex; align-items:flex-end; gap:10px; padding:9px 14px; overflow-x:auto; border-bottom:1px solid var(--vt-border); background:var(--vt-bg-subtle); }
.filter-control { display:grid; flex:0 0 auto; gap:4px; min-width:180px; }
.filter-control>span { color:var(--vt-fg-muted); font-size:11px; }
.number-range { display:flex; align-items:center; gap:5px; width:230px; }
.number-range i { color:var(--vt-fg-subtle); }
</style>
