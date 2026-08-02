<script setup lang="ts">
import { computed } from "vue";
import { Kanban } from "lucide-vue-next";
import type { ColumnSchema, PresetView } from "@/contracts";
import { t } from "@/i18n";
import { displayValue, metadataFields, rowTitle } from "./recordViewUtils";

const props = defineProps<{
  rows: readonly Record<string, unknown>[];
  schema: readonly ColumnSchema[];
  view: PresetView;
}>();

const details = computed(() => metadataFields(
  props.schema,
  [props.view.titleField, props.view.groupField],
  props.view.visibleFields,
));
const lanes = computed(() => {
  if (!props.view.groupField) return [];
  const grouped = new Map<string, Record<string, unknown>[]>();
  for (const row of props.rows) {
    const raw = row[props.view.groupField];
    const label = raw === null || raw === undefined || String(raw).trim() === ""
      ? t("views.kanban.ungrouped")
      : displayValue(raw);
    const records = grouped.get(label) ?? [];
    records.push(row);
    grouped.set(label, records);
  }
  return [...grouped].map(([label, records]) => ({ label, records }));
});
</script>

<template>
  <section class="record-kanban" data-testid="record-kanban-view">
    <header>
      <div><strong>{{ t("views.kind.kanban") }}</strong><small>{{ t("views.kanban.summary", { laneCount: lanes.length, count: rows.length }) }}</small></div>
    </header>
    <div v-if="lanes.length" class="kanban-lanes">
      <section v-for="lane in lanes" :key="lane.label" class="kanban-lane">
        <header><strong>{{ lane.label }}</strong><span>{{ lane.records.length }}</span></header>
        <div class="kanban-cards">
          <article v-for="row in lane.records" :key="String(row.rowKey ?? rowTitle(row, view))" data-testid="kanban-card">
            <strong>{{ rowTitle(row, view) }}</strong>
            <dl v-if="details.length">
              <template v-for="field in details" :key="field.name">
                <dt>{{ field.title }}</dt><dd>{{ displayValue(row[field.name]) }}</dd>
              </template>
            </dl>
          </article>
        </div>
      </section>
    </div>
    <div v-else class="view-empty"><Kanban :size="30" /><strong>{{ t("views.kanban.empty") }}</strong><small>{{ t("views.kanban.emptyHint") }}</small></div>
  </section>
</template>

<style scoped>
.record-kanban { min-height: 0; flex: 1 1 auto; overflow: auto; padding: 16px 18px; background: var(--vt-bg-subtle); }
.record-kanban > header { margin-bottom: 12px; }
.record-kanban > header > div { display: grid; gap: 2px; }
.record-kanban > header strong { font-size: 17px; }
.record-kanban > header small { color: var(--vt-fg-muted); }
.kanban-lanes { display: flex; min-height: 320px; align-items: flex-start; gap: 12px; padding-bottom: 12px; overflow-x: auto; }
.kanban-lane { width: 286px; flex: 0 0 286px; overflow: hidden; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: color-mix(in srgb, var(--vt-bg-sunken) 72%, var(--vt-bg)); }
.kanban-lane > header { display: flex; align-items: center; justify-content: space-between; padding: 10px 12px; border-bottom: 1px solid var(--vt-border); }
.kanban-lane > header strong { overflow: hidden; font-size: var(--vt-font-caption); text-overflow: ellipsis; white-space: nowrap; }
.kanban-lane > header span { min-width: 22px; padding: 2px 6px; color: var(--vt-fg-muted); border-radius: 999px; background: var(--vt-bg); font-size: 10px; text-align: center; }
.kanban-cards { display: grid; gap: 8px; padding: 8px; }
.kanban-cards article { display: grid; gap: 9px; padding: 11px 12px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); background: var(--vt-bg); box-shadow: var(--vt-shadow-1); }
.kanban-cards article > strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
dl { display: grid; grid-template-columns: minmax(52px, auto) minmax(0, 1fr); gap: 4px 8px; margin: 0; font-size: 11px; }
dt { color: var(--vt-fg-muted); }
dd { margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.view-empty { display: grid; min-height: 260px; place-items: center; align-content: center; gap: 7px; color: var(--vt-fg-muted); text-align: center; }
</style>
