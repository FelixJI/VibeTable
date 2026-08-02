<script setup lang="ts">
import { computed } from "vue";
import { CalendarClock } from "lucide-vue-next";
import type { ColumnSchema, PresetView } from "@/contracts";
import { t } from "@/i18n";
import { useUiStore } from "@/stores/uiStore";
import { rowTitle } from "./recordViewUtils";

const DAY = 24 * 60 * 60 * 1000;
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

const items = computed(() => {
  if (!props.view.dateField) return [];
  return props.rows.flatMap((row) => {
    const start = parseDate(row[props.view.dateField!]);
    if (!start) return [];
    const parsedEnd = props.view.endDateField ? parseDate(row[props.view.endDateField]) : null;
    const end = parsedEnd && parsedEnd.valueOf() >= start.valueOf() ? parsedEnd : start;
    return [{ row, start, end }];
  }).sort((left, right) => left.start.valueOf() - right.start.valueOf());
});
const range = computed(() => {
  if (!items.value.length) return null;
  const start = new Date(Math.min(...items.value.map((item) => item.start.valueOf())));
  const end = new Date(Math.max(...items.value.map((item) => item.end.valueOf())));
  const days = Math.max(1, Math.ceil((end.valueOf() - start.valueOf()) / DAY) + 1);
  return { start, end, days };
});
const formatter = computed(() => new Intl.DateTimeFormat(ui.locale, { month: "short", day: "numeric" }));
const ticks = computed(() => {
  if (!range.value) return [];
  const count = Math.min(6, range.value.days + 1);
  return Array.from({ length: count }, (_, index) => {
    const ratio = count === 1 ? 0 : index / (count - 1);
    const value = new Date(range.value!.start.valueOf() + ratio * (range.value!.days - 1) * DAY);
    return { label: formatter.value.format(value), left: ratio * 100 };
  });
});

function barStyle(item: (typeof items.value)[number]): Record<string, string> {
  if (!range.value) return {};
  const leftDays = Math.max(0, (item.start.valueOf() - range.value.start.valueOf()) / DAY);
  const durationDays = Math.max(1, (item.end.valueOf() - item.start.valueOf()) / DAY + 1);
  return {
    left: `${(leftDays / range.value.days) * 100}%`,
    width: `${Math.max(3, (durationDays / range.value.days) * 100)}%`,
  };
}
</script>

<template>
  <section class="record-timeline" data-testid="record-timeline-view">
    <header><div><strong>{{ t("views.kind.timeline") }}</strong><small>{{ t("views.timeline.summary", { count: items.length }) }}</small></div></header>
    <div v-if="items.length" class="timeline-board">
      <div class="timeline-scale" data-testid="timeline-scale">
        <span v-for="tick in ticks" :key="`${tick.left}-${tick.label}`" :style="{ left: `${tick.left}%` }">{{ tick.label }}</span>
      </div>
      <article v-for="item in items" :key="String(item.row.rowKey ?? `${item.start.valueOf()}-${rowTitle(item.row, view)}`)">
        <div class="timeline-title"><strong>{{ rowTitle(item.row, view) }}</strong><small>{{ formatter.format(item.start) }}<template v-if="item.end.valueOf() !== item.start.valueOf()"> → {{ formatter.format(item.end) }}</template></small></div>
        <div class="timeline-track">
          <i v-for="tick in ticks" :key="tick.left" :style="{ left: `${tick.left}%` }"></i>
          <span :style="barStyle(item)" data-testid="timeline-bar"><CalendarClock :size="13" /><b>{{ rowTitle(item.row, view) }}</b></span>
        </div>
      </article>
    </div>
    <div v-else class="timeline-empty"><CalendarClock :size="28" /><strong>{{ t("views.timeline.empty") }}</strong><small>{{ t("views.timeline.emptyHint") }}</small></div>
  </section>
</template>

<style scoped>
.record-timeline { min-height: 0; flex: 1 1 auto; overflow: auto; padding: 18px 22px; background: var(--vt-bg-subtle); }
.record-timeline > header { margin-bottom: 14px; }
.record-timeline > header > div { display: grid; gap: 2px; }
.record-timeline header strong { font-size: 17px; }
.record-timeline header small { color: var(--vt-fg-muted); }
.timeline-board { min-width: 760px; overflow: hidden; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: var(--vt-bg); box-shadow: var(--vt-shadow-1); }
.timeline-scale { position: relative; height: 38px; margin-left: 210px; border-bottom: 1px solid var(--vt-border); background: var(--vt-bg-sunken); }
.timeline-scale span { position: absolute; top: 11px; padding: 0 4px; color: var(--vt-fg-muted); font-size: 10px; transform: translateX(-50%); white-space: nowrap; }
.timeline-scale span:first-child { transform: none; }
.timeline-scale span:last-child { transform: translateX(-100%); }
.timeline-board article { display: grid; min-height: 64px; grid-template-columns: 210px minmax(520px, 1fr); border-bottom: 1px solid var(--vt-border); }
.timeline-board article:last-child { border-bottom: 0; }
.timeline-title { display: grid; min-width: 0; align-content: center; gap: 3px; padding: 10px 14px; border-right: 1px solid var(--vt-border); }
.timeline-title strong { overflow: hidden; font-size: var(--vt-font-caption); text-overflow: ellipsis; white-space: nowrap; }
.timeline-title small { color: var(--vt-fg-muted); font-size: 10px; }
.timeline-track { position: relative; min-width: 0; overflow: hidden; background: linear-gradient(to bottom, transparent, color-mix(in srgb, var(--vt-bg-subtle) 60%, transparent)); }
.timeline-track > i { position: absolute; top: 0; bottom: 0; width: 1px; background: color-mix(in srgb, var(--vt-border) 68%, transparent); }
.timeline-track > span { position: absolute; top: 17px; display: flex; min-width: 28px; height: 30px; align-items: center; gap: 5px; padding: 0 8px; overflow: hidden; color: var(--vt-fg-accent-strong); border: 1px solid color-mix(in srgb, var(--vt-color-primary-500) 38%, var(--vt-border)); border-radius: var(--vt-radius-md); background: var(--vt-color-primary-50); box-shadow: 0 2px 8px color-mix(in srgb, var(--vt-color-primary-500) 10%, transparent); }
.timeline-track b { overflow: hidden; font-size: 11px; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.timeline-empty { display: grid; min-height: 260px; place-items: center; align-content: center; gap: 7px; color: var(--vt-fg-muted); text-align: center; }
</style>
