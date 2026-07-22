<script setup lang="ts">
/**
 * GridHost — thin presentational wrapper around `useTabulator`.
 *
 * Inline-edit callback: the actual routing to `mutationService.updateCell`
 * lives in WorkspaceView (the integration layer). Optional so GridHost stays
 * usable in read-only contexts.
 *
 * Tabulator instance sharing: WorkspaceView owns the Tabulator ref (created in
 * its setup) and provides it via {@link TABULATOR_INJECTION_KEY}. GridHost
 * injects it and forwards it to `useTabulator` so the composable populates
 * THAT ref (not a fresh internal one). This lets WorkspaceView read the active
 * range for the copy/paste/delete keyboard shortcuts (Task M5) without lifting
 * the entire useTabulator call out of GridHost.
 */
import { inject, ref } from "vue";
import type { Ref } from "vue";
import type { TabulatorFull } from "tabulator-tables";
import { useTabulator } from "@/composables/useTabulator";
import type { CellEditedHandler, CellValidationErrorHandler } from "@/grid/createGrid";
import { ROW_NUMBER_FIELD } from "@/grid/createGrid";
import { useTableStore } from "@/stores/tableStore";
import { TABULATOR_INJECTION_KEY } from "./tabulatorInjection";
import LoadingOverlay from "@/components/feedback/LoadingOverlay.vue";
import ErrorOverlay from "@/components/feedback/ErrorOverlay.vue";

const props = defineProps<{
  onCellEdited?: CellEditedHandler;
  onValidationError?: CellValidationErrorHandler;
}>();
const emit = defineEmits<{
  selectionChange: [payload:
    | { scope: "row" | "cell"; rowKey: string | number; field?: string }
    | { scope: "multiple" }];
  rowContext: [payload: { rowKey: string | number; field?: string; x: number; y: number }];
}>();

const gridEl = ref<HTMLElement | null>(null);
const store = useTableStore();
const tabulator = inject<Ref<TabulatorFull | null>>(TABULATOR_INJECTION_KEY);
useTabulator(gridEl, {
  onCellEdited: props.onCellEdited,
  onRangeSelectionChanged: ({ rowKeys, fields }) => {
    const dataFields = fields.filter((field) => field !== ROW_NUMBER_FIELD);
    if (rowKeys.length * dataFields.length > 1) {
      emit("selectionChange", { scope: "multiple" });
    } else if (rowKeys.length === 1 && dataFields.length === 1) {
      emit("selectionChange", { scope: "cell", rowKey: rowKeys[0]!, field: dataFields[0] });
    }
  },
  onValidationError: props.onValidationError,
  tabulator: tabulator ?? undefined,
});

interface LocatedCell {
  rowKey: string | number;
  field: string | null;
  rowElement: HTMLElement | null;
  cellElement: HTMLElement | null;
}

function locateCell(target: Node): LocatedCell | null {
  if (!tabulator?.value) return null;
  const rows = tabulator.value.getRows("active") as unknown as Array<{
    getData: () => Record<string, unknown>;
    getElement?: () => HTMLElement;
    getCells?: () => Array<{ getField: () => string; getElement?: () => HTMLElement }>;
  }>;
  const row = rows.find((candidate) => candidate.getElement?.().contains(target));
  const rowKey = row?.getData()?.rowKey;
  if (!row || (typeof rowKey !== "string" && typeof rowKey !== "number")) return null;
  const cell = row.getCells?.().find((candidate) => candidate.getElement?.().contains(target));
  return {
    rowKey,
    field: cell?.getField() ?? null,
    rowElement: row.getElement?.() ?? null,
    cellElement: cell?.getElement?.() ?? null,
  };
}

function markSelection(located: LocatedCell): void {
  gridEl.value?.querySelectorAll(".vt-row-selected, .vt-cell-selected").forEach((element) => {
    element.classList.remove("vt-row-selected", "vt-cell-selected");
  });
  if (located.field === ROW_NUMBER_FIELD) {
    located.rowElement?.classList.add("vt-row-selected");
    emit("selectionChange", { scope: "row", rowKey: located.rowKey });
    return;
  }
  if (located.field) {
    located.cellElement?.classList.add("vt-cell-selected");
    emit("selectionChange", { scope: "cell", rowKey: located.rowKey, field: located.field });
  }
}

function onGridClick(event: MouseEvent): void {
  const target = event.target;
  if (!(target instanceof Node)) return;
  const located = locateCell(target);
  if (located) markSelection(located);
}

function onContextMenu(event: MouseEvent): void {
  const target = event.target;
  if (!(target instanceof Node)) return;
  const located = locateCell(target);
  if (!located) return;
  event.preventDefault();
  markSelection(located);
  emit("rowContext", {
    rowKey: located.rowKey,
    field: located.field && located.field !== ROW_NUMBER_FIELD ? located.field : undefined,
    x: event.clientX,
    y: event.clientY,
  });
}
</script>

<template>
  <div class="grid-wrapper" @click="onGridClick" @contextmenu="onContextMenu">
    <div ref="gridEl" class="grid-host"></div>
    <LoadingOverlay :show="store.loading" />
    <ErrorOverlay :show="!!store.error" :message="store.error ?? ''" />
  </div>
</template>

<style scoped>
.grid-wrapper {
  position: relative;
  flex: 1 1 auto;
  min-height: 0;
}
.grid-host {
  height: 100%;
}
.grid-host :deep(.tabulator) {
  font-size: var(--vt-font-body);
  background: var(--vt-bg);
}
.grid-host :deep(.tabulator-cell.vt-row-number) {
  color: var(--vt-fg-muted);
  font-variant-numeric: tabular-nums;
  border-right-color: var(--vt-border);
  background: var(--vt-bg-subtle);
  cursor: pointer;
  user-select: none;
}
.grid-host :deep(.tabulator-row.vt-row-selected .tabulator-cell) {
  background: color-mix(in srgb, var(--vt-color-primary-500) 11%, var(--vt-bg));
}
.grid-host :deep(.tabulator-cell.vt-cell-selected) {
  outline: 2px solid var(--vt-color-primary-500);
  outline-offset: -2px;
}
</style>
