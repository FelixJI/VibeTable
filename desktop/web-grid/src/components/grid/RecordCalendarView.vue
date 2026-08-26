<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { ChevronLeft, ChevronRight } from "@lucide/vue";
import type { ColumnSchema, PresetView } from "@/contracts";
import { t } from "@/i18n";
import { useUiStore } from "@/stores/uiStore";
import { calendarDateKey } from "@/grid/calendarDateValue";
import type {
  CalendarMovableRecord,
  CalendarRecordMoveIntent,
} from "@/workspace/alternativeViewInteractionController";

const props = defineProps<{
  rows: readonly Record<string, unknown>[];
  schema: readonly ColumnSchema[];
  view: PresetView;
  interactionEnabled?: boolean;
  movableRecords?: readonly CalendarMovableRecord[];
}>();
const emit = defineEmits<{
  intent: [intent: CalendarRecordMoveIntent];
}>();
const ui = useUiStore();
const month = ref(startOfMonth(new Date()));
const draggedRecord = ref<CalendarMovableRecord | null>(null);
const movableByRowKey = computed(() => new Map(
  (props.movableRecords ?? []).map(record => [record.rowKey, record.expectedDigest] as const),
));
const dateFieldType = computed<"date" | "datetime">(() => (
  props.schema.find(column => column.name === props.view.dateField)?.dataType === "date"
    ? "date"
    : "datetime"
));

function startOfMonth(value: Date): Date {
  return new Date(value.getFullYear(), value.getMonth(), 1);
}

function dateKey(value: unknown): string | null {
  if (typeof value !== "string" && !(value instanceof Date)) return null;
  const parsed = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(parsed.valueOf())) return null;
  return `${parsed.getFullYear()}-${String(parsed.getMonth() + 1).padStart(2, "0")}-${String(parsed.getDate()).padStart(2, "0")}`;
}

function rowTitle(row: Record<string, unknown>): string {
  const value = props.view.titleField ? row[props.view.titleField] : null;
  if (value !== null && value !== undefined && String(value).trim()) return String(value);
  return t("views.recordFallback", { id: String(row.rowKey ?? "—") });
}

const itemsByDate = computed(() => {
  const grouped = new Map<string, Record<string, unknown>[]>();
  if (!props.view.dateField) return grouped;
  for (const row of props.rows) {
    const key = calendarDateKey(row[props.view.dateField], dateFieldType.value);
    if (!key) continue;
    const items = grouped.get(key) ?? [];
    items.push(row);
    grouped.set(key, items);
  }
  return grouped;
});

watch(
  () => [props.view.dateField, props.rows] as const,
  () => {
    const first = [...itemsByDate.value.keys()].sort()[0];
    if (first) month.value = startOfMonth(new Date(`${first}T00:00:00`));
  },
  { immediate: true },
);

const monthLabel = computed(() => new Intl.DateTimeFormat(ui.locale, {
  year: "numeric",
  month: "long",
}).format(month.value));
const weekdays = computed(() => {
  const formatter = new Intl.DateTimeFormat(ui.locale, { weekday: "short" });
  return Array.from({ length: 7 }, (_, index) => formatter.format(new Date(2026, 7, 3 + index)));
});
const cells = computed(() => {
  const first = startOfMonth(month.value);
  const mondayOffset = (first.getDay() + 6) % 7;
  return Array.from({ length: 42 }, (_, index) => {
    const date = new Date(first.getFullYear(), first.getMonth(), index - mondayOffset + 1);
    const key = dateKey(date)!;
    return {
      key,
      day: date.getDate(),
      outside: date.getMonth() !== first.getMonth(),
      today: key === dateKey(new Date()),
      items: itemsByDate.value.get(key) ?? [],
    };
  });
});

function moveMonth(offset: number): void {
  month.value = new Date(month.value.getFullYear(), month.value.getMonth() + offset, 1);
}

function movableRecord(row: Record<string, unknown>): CalendarMovableRecord | null {
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
  if (!draggedRecord.value) return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
}

function dropRecord(targetDate: string, event: DragEvent): void {
  const record = draggedRecord.value;
  draggedRecord.value = null;
  if (!record) return;
  event.preventDefault();
  emit("intent", { type: "calendar.record.move", ...record, targetDate });
}
</script>

<template>
  <section class="record-calendar" data-testid="record-calendar-view">
    <header>
      <div><strong>{{ monthLabel }}</strong><small>{{ t("views.calendar.summary", { count: rows.length }) }}</small></div>
      <div class="calendar-actions">
        <button type="button" :aria-label="t('views.calendar.previous')" @click="moveMonth(-1)"><ChevronLeft :size="17" /></button>
        <button type="button" @click="month = startOfMonth(new Date())">{{ t("views.calendar.today") }}</button>
        <button type="button" :aria-label="t('views.calendar.next')" @click="moveMonth(1)"><ChevronRight :size="17" /></button>
      </div>
    </header>
    <div class="calendar-grid calendar-weekdays"><span v-for="weekday in weekdays" :key="weekday">{{ weekday }}</span></div>
    <div class="calendar-grid calendar-days">
      <article
        v-for="cell in cells"
        :key="cell.key"
        data-testid="calendar-day"
        :data-date="cell.key"
        :class="{ outside: cell.outside, today: cell.today }"
        @dragover="allowRecordDrop"
        @drop="dropRecord(cell.key, $event)"
      >
        <b>{{ cell.day }}</b>
        <div>
          <span
            v-for="row in cell.items.slice(0, 3)"
            :key="String(row.rowKey ?? rowTitle(row))"
            data-testid="calendar-record"
            :title="rowTitle(row)"
            :draggable="Boolean(movableRecord(row))"
            @dragstart="startRecordDrag(row, $event)"
            @dragend="draggedRecord = null"
          >{{ rowTitle(row) }}</span>
          <small v-if="cell.items.length > 3">{{ t("views.calendar.more", { count: cell.items.length - 3 }) }}</small>
        </div>
      </article>
    </div>
  </section>
</template>

<style scoped>
.record-calendar { min-height: 0; flex: 1 1 auto; overflow: auto; padding: 16px; background: var(--vt-bg-subtle); }
.record-calendar > header { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 12px; }
.record-calendar > header > div:first-child { display: grid; gap: 2px; }
.record-calendar header strong { font-size: 17px; }
.record-calendar header small { color: var(--vt-fg-muted); }
.calendar-actions { display: flex; align-items: center; gap: 5px; }
.calendar-actions button { display: grid; min-width: 30px; height: 30px; place-items: center; padding: 0 9px; color: var(--vt-fg-muted); border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); background: var(--vt-bg); cursor: pointer; }
.calendar-grid { display: grid; grid-template-columns: repeat(7, minmax(110px, 1fr)); }
.calendar-weekdays { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); text-align: center; }
.calendar-weekdays span { padding: 7px; }
.calendar-days { overflow: hidden; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: var(--vt-bg); }
.calendar-days article { min-height: 112px; padding: 8px; overflow: hidden; border-right: 1px solid var(--vt-border); border-bottom: 1px solid var(--vt-border); }
.calendar-days article:nth-child(7n) { border-right: 0; }
.calendar-days article:nth-last-child(-n+7) { border-bottom: 0; }
.calendar-days article.outside { color: var(--vt-fg-muted); background: var(--vt-bg-subtle); opacity: .72; }
.calendar-days article.today { box-shadow: inset 0 0 0 2px var(--vt-color-primary-500); }
.calendar-days article > b { display: inline-grid; width: 24px; height: 24px; place-items: center; border-radius: 50%; font-size: var(--vt-font-caption); }
.calendar-days article.today > b { color: white; background: var(--vt-color-primary-500); }
.calendar-days article > div { display: grid; gap: 4px; margin-top: 5px; }
.calendar-days article span { padding: 3px 6px; overflow: hidden; color: var(--vt-fg-accent-strong); border-left: 2px solid var(--vt-color-primary-500); border-radius: 3px; background: var(--vt-color-primary-50); font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
.calendar-days article small { color: var(--vt-fg-muted); font-size: 10px; }
</style>
