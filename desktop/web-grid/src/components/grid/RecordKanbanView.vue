<script setup lang="ts">
import { computed, ref } from "vue";
import { Kanban } from "lucide-vue-next";
import type { ColumnSchema, PresetView } from "@/contracts";
import { t } from "@/i18n";
import { displayValue, metadataFields, rowTitle } from "./recordViewUtils";

const props = defineProps<{
  rows: readonly Record<string, unknown>[];
  schema: readonly ColumnSchema[];
  view: PresetView;
  interactionEnabled?: boolean;
  laneOptions?: readonly { readonly optionId: string; readonly label: string }[];
}>();
const emit = defineEmits<{
  cardMove: [intent: {
    readonly rowKey: string | number;
    readonly targetOptionId: string;
    readonly expectedDigest: string;
  }];
}>();

interface KanbanLane {
  readonly key: string;
  readonly label: string;
  readonly targetOptionId: string | null;
  readonly records: Record<string, unknown>[];
}

interface DraggedCard {
  readonly rowKey: string | number;
  readonly expectedDigest: string;
}

const draggedCard = ref<DraggedCard | null>(null);

const details = computed(() => metadataFields(
  props.schema,
  [props.view.titleField, props.view.groupField],
  props.view.visibleFields,
));
const lanes = computed(() => {
  if (!props.view.groupField) return [];
  const grouped = new Map<string, KanbanLane>();
  const optionIds = new Set<string>();
  for (const option of props.laneOptions ?? []) {
    optionIds.add(option.optionId);
    grouped.set(`option:${option.optionId}`, {
      key: `option:${option.optionId}`,
      label: option.label,
      targetOptionId: option.optionId,
      records: [],
    });
  }
  for (const row of props.rows) {
    const raw = row[props.view.groupField];
    const blank = raw === null || raw === undefined || String(raw).trim() === "";
    const optionId = typeof raw === "string" && optionIds.has(raw) ? raw : null;
    const key = optionId ? `option:${optionId}` : blank ? "ungrouped" : `unknown:${String(raw)}`;
    let lane = grouped.get(key);
    if (!lane) {
      lane = {
        key,
        label: blank ? t("views.kanban.ungrouped") : displayValue(raw),
        targetOptionId: null,
        records: [],
      };
      grouped.set(key, lane);
    }
    lane.records.push(row);
  }
  return [...grouped.values()];
});

function canDrag(row: Record<string, unknown>): boolean {
  return props.interactionEnabled === true
    && (typeof row.rowKey === "string" || typeof row.rowKey === "number")
    && typeof row.__vibetableDigest === "string";
}

function onDragStart(event: DragEvent, row: Record<string, unknown>): void {
  if (!canDrag(row)) {
    event.preventDefault();
    draggedCard.value = null;
    return;
  }
  const rowKey = row.rowKey as string | number;
  const expectedDigest = row.__vibetableDigest as string;
  draggedCard.value = { rowKey, expectedDigest };
  event.dataTransfer?.setData("text/plain", String(rowKey));
  if (event.dataTransfer) event.dataTransfer.effectAllowed = "move";
}

function onDragOver(event: DragEvent, targetOptionId: string | null): void {
  if (!draggedCard.value || !targetOptionId || props.interactionEnabled !== true) return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
}

function onDrop(event: DragEvent, targetOptionId: string | null): void {
  const dragged = draggedCard.value;
  draggedCard.value = null;
  if (!dragged || !targetOptionId || props.interactionEnabled !== true) return;
  event.preventDefault();
  emit("cardMove", { ...dragged, targetOptionId });
}
</script>

<template>
  <section class="record-kanban" data-testid="record-kanban-view">
    <header>
      <div><strong>{{ t("views.kind.kanban") }}</strong><small>{{ t("views.kanban.summary", { laneCount: lanes.length, count: rows.length }) }}</small></div>
    </header>
    <div v-if="lanes.length" class="kanban-lanes">
      <section
        v-for="lane in lanes"
        :key="lane.key"
        class="kanban-lane"
        data-testid="kanban-lane"
        :data-option-id="lane.targetOptionId ?? undefined"
        @dragover="onDragOver($event, lane.targetOptionId)"
        @drop="onDrop($event, lane.targetOptionId)"
      >
        <header><strong>{{ lane.label }}</strong><span>{{ lane.records.length }}</span></header>
        <div class="kanban-cards">
          <article
            v-for="row in lane.records"
            :key="String(row.rowKey ?? rowTitle(row, view))"
            data-testid="kanban-card"
            :data-row-key="String(row.rowKey ?? '')"
            :draggable="canDrag(row)"
            @dragstart="onDragStart($event, row)"
            @dragend="draggedCard = null"
          >
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
