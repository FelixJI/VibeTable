<script setup lang="ts">
import { computed, ref } from "vue";
import { CalendarClock } from "@lucide/vue";
import type { ColumnSchema, PresetView } from "@/contracts";
import { calendarDateKey } from "@/grid/calendarDateValue";
import {
  createTimelineInteractionRange,
  timelineDateAtTrackOffset,
  timelineDatePosition,
} from "@/grid/timelineDateGeometry";
import { t } from "@/i18n";
import { useUiStore } from "@/stores/uiStore";
import type {
  TimelineMovableRecord,
  TimelineRecordMoveIntent,
} from "@/workspace/alternativeViewInteractionController";
import { rowTitle } from "./recordViewUtils";

const DAY = 24 * 60 * 60 * 1000;
const props = defineProps<{
  rows: readonly Record<string, unknown>[];
  schema: readonly ColumnSchema[];
  view: PresetView;
  interactionEnabled?: boolean;
  movableRecords?: readonly TimelineMovableRecord[];
}>();
const emit = defineEmits<{
  intent: [intent: TimelineRecordMoveIntent];
}>();
const ui = useUiStore();
const draggedRecord = ref<TimelineMovableRecord | null>(null);
const movableByRowKey = computed(() => new Map(
  (props.movableRecords ?? []).map(record => [record.rowKey, record.expectedDigest] as const),
));

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
    const startDateKey = calendarDateKey(row[props.view.dateField!], "date");
    return [{ row, start, end, startDateKey }];
  }).sort((left, right) => left.start.valueOf() - right.start.valueOf());
});
const interactionRange = computed(() => props.interactionEnabled
  ? createTimelineInteractionRange(
    items.value.flatMap(item => item.startDateKey ? [item.startDateKey] : []),
  )
  : null);
const range = computed(() => {
  if (!items.value.length) return null;
  const start = new Date(Math.min(...items.value.map((item) => item.start.valueOf())));
  const end = new Date(Math.max(...items.value.map((item) => item.end.valueOf())));
  const days = Math.max(1, Math.ceil((end.valueOf() - start.valueOf()) / DAY) + 1);
  return { start, end, days };
});
const localFormatter = computed(() => new Intl.DateTimeFormat(
  ui.locale,
  { month: "short", day: "numeric" },
));
const logicalDateFormatter = computed(() => new Intl.DateTimeFormat(
  ui.locale,
  { month: "short", day: "numeric", timeZone: "UTC" },
));

function logicalDateLabel(dateKey: string): string {
  return logicalDateFormatter.value.format(new Date(`${dateKey}T00:00:00.000Z`));
}

const ticks = computed(() => {
  if (interactionRange.value) {
    const count = Math.min(6, interactionRange.value.dayCount + 1);
    return Array.from({ length: count }, (_, index) => {
      const ratio = count === 1 ? 0 : index / (count - 1);
      const dateKey = timelineDateAtTrackOffset(interactionRange.value!, ratio, 1);
      return { label: dateKey ? logicalDateLabel(dateKey) : "", left: ratio * 100 };
    });
  }
  if (!range.value) return [];
  const count = Math.min(6, range.value.days + 1);
  return Array.from({ length: count }, (_, index) => {
    const ratio = count === 1 ? 0 : index / (count - 1);
    const value = new Date(range.value!.start.valueOf() + ratio * (range.value!.days - 1) * DAY);
    return { label: localFormatter.value.format(value), left: ratio * 100 };
  });
});

function startLabel(item: (typeof items.value)[number]): string {
  return interactionRange.value && item.startDateKey
    ? logicalDateLabel(item.startDateKey)
    : localFormatter.value.format(item.start);
}

function barStyle(item: (typeof items.value)[number]): Record<string, string> {
  if (!range.value) return {};
  if (interactionRange.value && item.startDateKey) {
    const position = timelineDatePosition(item.startDateKey, interactionRange.value);
    if (position !== null) {
      return {
        left: `${position * 100}%`,
        width: `${Math.max(3, 100 / interactionRange.value.dayCount)}%`,
      };
    }
  }
  const leftDays = Math.max(0, (item.start.valueOf() - range.value.start.valueOf()) / DAY);
  const durationDays = Math.max(1, (item.end.valueOf() - item.start.valueOf()) / DAY + 1);
  return {
    left: `${(leftDays / range.value.days) * 100}%`,
    width: `${Math.max(3, (durationDays / range.value.days) * 100)}%`,
  };
}

function movableRecord(row: Record<string, unknown>): TimelineMovableRecord | null {
  if (!props.interactionEnabled) return null;
  const rowKey = row.rowKey;
  if (typeof rowKey !== "string" && typeof rowKey !== "number") return null;
  const expectedDigest = movableByRowKey.value.get(rowKey);
  return expectedDigest && expectedDigest === row.__vibetableDigest
    ? { rowKey, expectedDigest }
    : null;
}

function startRecordDrag(row: Record<string, unknown>, event: DragEvent): void {
  const record = movableRecord(row);
  draggedRecord.value = record;
  if (record && event.dataTransfer) {
    event.dataTransfer.setData("text/plain", String(record.rowKey));
    event.dataTransfer.effectAllowed = "move";
  }
}

function allowRecordDrop(event: DragEvent): void {
  if (!draggedRecord.value || !interactionRange.value) return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
}

function dropRecord(event: DragEvent): void {
  const record = draggedRecord.value;
  draggedRecord.value = null;
  const track = event.currentTarget;
  if (!record || !interactionRange.value || !(track instanceof HTMLElement)) return;
  const bounds = track.getBoundingClientRect();
  const targetDate = timelineDateAtTrackOffset(
    interactionRange.value,
    event.clientX - bounds.left,
    bounds.width,
  );
  if (!targetDate) return;
  event.preventDefault();
  emit("intent", { type: "timeline.record.move", ...record, targetDate });
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
        <div class="timeline-title"><strong>{{ rowTitle(item.row, view) }}</strong><small>{{ startLabel(item) }}<template v-if="item.end.valueOf() !== item.start.valueOf()"> → {{ localFormatter.format(item.end) }}</template></small></div>
        <div
          class="timeline-track"
          data-testid="timeline-track"
          :data-start-date="interactionRange?.startDate"
          :data-end-date="interactionRange?.endDate"
          @dragover="allowRecordDrop"
          @drop="dropRecord"
        >
          <i v-for="tick in ticks" :key="tick.left" :style="{ left: `${tick.left}%` }"></i>
          <span
            :style="barStyle(item)"
            data-testid="timeline-record"
            :data-date="item.startDateKey ?? undefined"
            :draggable="Boolean(movableRecord(item.row))"
            @dragstart="startRecordDrag(item.row, $event)"
            @dragend="draggedRecord = null"
          ><CalendarClock :size="13" /><b>{{ rowTitle(item.row, view) }}</b></span>
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
