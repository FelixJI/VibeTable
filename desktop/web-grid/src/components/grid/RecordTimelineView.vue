<script setup lang="ts">
import { computed } from "vue";
import { CalendarClock } from "lucide-vue-next";
import type { ColumnSchema, PresetView } from "@/contracts";
import { t } from "@/i18n";
import { useUiStore } from "@/stores/uiStore";

const props = defineProps<{
  rows: readonly Record<string, unknown>[];
  schema: readonly ColumnSchema[];
  view: PresetView;
}>();
const ui = useUiStore();

function parseDate(value: unknown): Date | null {
  if (typeof value !== "string" && !(value instanceof Date)) return null;
  const parsed = value instanceof Date ? value : new Date(value);
  return Number.isNaN(parsed.valueOf()) ? null : parsed;
}

function rowTitle(row: Record<string, unknown>): string {
  const value = props.view.titleField ? row[props.view.titleField] : null;
  if (value !== null && value !== undefined && String(value).trim()) return String(value);
  return t("views.recordFallback", { id: String(row.rowKey ?? "—") });
}

const formatter = computed(() => new Intl.DateTimeFormat(ui.locale, { dateStyle: "medium", timeStyle: "short" }));
const items = computed(() => {
  if (!props.view.dateField) return [];
  return props.rows.flatMap((row) => {
    const start = parseDate(row[props.view.dateField!]);
    if (!start) return [];
    const end = props.view.endDateField ? parseDate(row[props.view.endDateField]) : null;
    return [{ row, start, end }];
  }).sort((left, right) => left.start.valueOf() - right.start.valueOf());
});
</script>

<template>
  <section class="record-timeline" data-testid="record-timeline-view">
    <header><div><strong>{{ t("views.kind.timeline") }}</strong><small>{{ t("views.timeline.summary", { count: items.length }) }}</small></div></header>
    <div v-if="items.length" class="timeline-list">
      <article v-for="item in items" :key="String(item.row.rowKey ?? `${item.start.valueOf()}-${rowTitle(item.row)}`)">
        <i></i>
        <div class="timeline-date">
          <strong>{{ formatter.format(item.start) }}</strong>
          <small v-if="item.end">→ {{ formatter.format(item.end) }}</small>
        </div>
        <div class="timeline-card">
          <CalendarClock :size="17" />
          <div><strong>{{ rowTitle(item.row) }}</strong><small>{{ view.dateField }}<template v-if="view.endDateField"> → {{ view.endDateField }}</template></small></div>
        </div>
      </article>
    </div>
    <div v-else class="timeline-empty"><CalendarClock :size="28" /><strong>{{ t("views.timeline.empty") }}</strong><small>{{ t("views.timeline.emptyHint") }}</small></div>
  </section>
</template>

<style scoped>
.record-timeline { min-height: 0; flex: 1 1 auto; overflow: auto; padding: 18px 22px; background: var(--vt-bg-subtle); }
.record-timeline > header { margin-bottom: 18px; }
.record-timeline > header > div { display: grid; gap: 2px; }
.record-timeline header strong { font-size: 17px; }
.record-timeline header small { color: var(--vt-fg-muted); }
.timeline-list { position: relative; display: grid; max-width: 920px; margin: 0 auto; }
.timeline-list::before { position: absolute; top: 12px; bottom: 12px; left: 199px; width: 2px; background: color-mix(in srgb, var(--vt-color-primary-500) 25%, var(--vt-border)); content: ""; }
.timeline-list article { position: relative; display: grid; grid-template-columns: 180px 20px minmax(260px, 1fr); align-items: start; gap: 10px; padding-bottom: 14px; }
.timeline-list article > i { z-index: 1; width: 10px; height: 10px; justify-self: center; margin-top: 13px; border: 2px solid var(--vt-bg); border-radius: 50%; background: var(--vt-color-primary-500); box-shadow: 0 0 0 2px color-mix(in srgb, var(--vt-color-primary-500) 30%, transparent); }
.timeline-date { display: grid; gap: 2px; padding-top: 8px; text-align: right; }
.timeline-date strong { font-size: var(--vt-font-caption); }
.timeline-date small { color: var(--vt-fg-muted); font-size: 10px; }
.timeline-card { display: flex; gap: 10px; padding: 12px 14px; color: var(--vt-fg); border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: var(--vt-bg); box-shadow: var(--vt-shadow-1); }
.timeline-card > svg { flex: none; margin-top: 2px; color: var(--vt-color-primary-500); }
.timeline-card > div { display: grid; gap: 3px; }
.timeline-card small { color: var(--vt-fg-muted); }
.timeline-empty { display: grid; min-height: 260px; place-items: center; align-content: center; gap: 7px; color: var(--vt-fg-muted); text-align: center; }
</style>
