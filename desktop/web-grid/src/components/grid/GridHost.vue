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
import { NButton, NIcon } from "naive-ui";
import { Plus } from "lucide-vue-next";
import type { TabulatorFull } from "tabulator-tables";
import { useTabulator } from "@/composables/useTabulator";
import type { CellEditedHandler, CellValidationErrorHandler } from "@/grid/createGrid";
import type { ColumnSchema, LookupSourcePageIntent, LookupValueProvenance, NormalizedRelationDescriptor } from "@/contracts";
import type { FilterExpression, GroupCondition, SortCondition } from "@/contracts";
import { ROW_NUMBER_FIELD } from "@/grid/createGrid";
import { useTableStore } from "@/stores/tableStore";
import { useUiStore } from "@/stores/uiStore";
import { useRelationLookupStore } from "@/stores/relationLookupStore";
import { TABULATOR_INJECTION_KEY } from "./tabulatorInjection";
import LoadingOverlay from "@/components/feedback/LoadingOverlay.vue";
import ErrorOverlay from "@/components/feedback/ErrorOverlay.vue";
import LookupGroupPanel from "@/components/grid/LookupGroupPanel.vue";
import { t } from "@/i18n";

const props = defineProps<{
  onCellEdited?: CellEditedHandler;
  onValidationError?: CellValidationErrorHandler;
  insertRowDisabled?: boolean;
}>();
const emit = defineEmits<{
  selectionChange: [payload:
    | { scope: "row" | "cell"; rowKey: string | number; field?: string }
    | { scope: "multiple" }];
  rowContext: [payload: { rowKey: string | number; field?: string; x: number; y: number }];
  relationEdit: [payload: {
    rowKey: string | number;
    field: string;
    descriptor: NormalizedRelationDescriptor;
    value: unknown;
  }];
  lookupSource: [source: LookupValueProvenance];
	lookupSourcePage: [intent: LookupSourcePageIntent];
  attachmentOpen: [payload: {
    rowKey: string | number;
    column: ColumnSchema;
  }];
  jsonEdit: [payload: {
    rowKey: string | number;
    column: ColumnSchema;
    value: unknown;
    expectedDigest: string | null;
    trigger: HTMLElement | null;
  }];
  viewQueryChange: [query: {
    readonly filters: readonly FilterExpression[];
    readonly sorts: readonly SortCondition[];
    readonly groups: readonly GroupCondition[];
  }];
  insertFirstRow: [];
  columnContext: [payload: { field: string; x: number; y: number }];
}>();

const gridEl = ref<HTMLElement | null>(null);
const store = useTableStore();
const ui = useUiStore();
const relationLookup = useRelationLookupStore();
const tabulator = inject<Ref<TabulatorFull | null>>(TABULATOR_INJECTION_KEY);
const { dataApplying } = useTabulator(gridEl, {
  onCellEdited: props.onCellEdited,
  onRangeSelectionChanged: ({ rowKeys, fields }) => {
    const dataFields = fields.filter((field) => field !== ROW_NUMBER_FIELD);
    if (dataFields.length > 0) markRangeAriaByKeys(rowKeys, dataFields);
    if (rowKeys.length * dataFields.length > 1) {
      emit("selectionChange", { scope: "multiple" });
    } else if (rowKeys.length === 1 && dataFields.length === 1) {
      emit("selectionChange", { scope: "cell", rowKey: rowKeys[0]!, field: dataFields[0] });
    }
  },
  onValidationError: props.onValidationError,
  onRelationEditRequested: (rowKey, field, descriptor, value) => {
    emit("relationEdit", { rowKey, field, descriptor, value });
  },
  onLookupSourceRequested: (source) => emit("lookupSource", source),
	onLookupSourcePageRequested: (intent) => emit("lookupSourcePage", intent),
  onAttachmentOpenRequested: (rowKey, column) =>
    emit("attachmentOpen", { rowKey, column }),
  onViewQueryChanged: (query) => emit("viewQueryChange", query),
  tabulator: tabulator ?? undefined,
});

interface LocatedCell {
  rowKey: string | number;
  field: string | null;
  rowData: Record<string, unknown>;
  rowElement: HTMLElement | null;
  cellElement: HTMLElement | null;
}

function validDigest(value: unknown): string | null {
  return typeof value === "string" && /^sha256:[0-9a-f]{64}$/u.test(value)
    ? value
    : null;
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
    rowData: row.getData(),
    rowElement: row.getElement?.() ?? null,
    cellElement: cell?.getElement?.() ?? null,
  };
}

function onGridDoubleClick(event: MouseEvent): void {
  const target = event.target;
  if (!(target instanceof Node)) return;
  const located = locateCell(target);
  if (!located?.field || located.field === ROW_NUMBER_FIELD) return;
  const column = store.schema?.find((item) => item.name === located.field);
  const edit = store.editSchema?.find((item) => item.name === located.field);
  if (!column || column.dataType !== "json" || !edit?.editable) return;
  event.preventDefault();
  located.cellElement?.focus({ preventScroll: true });
  emit("jsonEdit", {
    rowKey: located.rowKey,
    column,
    value: located.rowData[located.field],
    expectedDigest: validDigest(located.rowData.__vibetableDigest),
    trigger: located.cellElement,
  });
}

function resolveKeyboardCell(target: EventTarget | null): LocatedCell | null {
  if (target instanceof Node) {
    const direct = locateCell(target);
    if (direct) return direct;
  }
  const active = gridEl.value?.querySelector(
    ".tabulator-range-cell-active, .vt-cell-selected, .tabulator-cell:focus",
  );
  return active ? locateCell(active) : null;
}

function markSelection(located: LocatedCell): void {
  clearSelectionIndicators();
  if (located.field === ROW_NUMBER_FIELD) {
    located.rowElement?.classList.add("vt-row-selected");
    located.rowElement?.setAttribute("aria-selected", "true");
    emit("selectionChange", { scope: "row", rowKey: located.rowKey });
    return;
  }
  if (located.field) {
    located.cellElement?.classList.add("vt-cell-selected");
    located.cellElement?.setAttribute("aria-selected", "true");
    emit("selectionChange", { scope: "cell", rowKey: located.rowKey, field: located.field });
  }
}

function clearSelectionIndicators(): void {
  gridEl.value?.querySelectorAll(
    '.vt-row-selected, .vt-cell-selected, [aria-selected="true"]',
  ).forEach((element) => {
    element.classList.remove("vt-row-selected", "vt-cell-selected");
    element.removeAttribute("aria-selected");
  });
}

function onGridClick(event: MouseEvent): void {
  const target = event.target;
  if (!(target instanceof Node)) return;
  const located = locateCell(target);
  if (located && !hasMultiCellRange()) markSelection(located);
}

function hasMultiCellRange(): boolean {
  const ranges = (tabulator?.value as unknown as {
    getRanges?: () => Array<{
      getRows?: () => Array<{
        getCells?: () => Array<{
          getField?: () => string;
          getElement?: () => HTMLElement;
        }>;
      }>;
      getColumns?: () => Array<{ getField?: () => string }>;
    }>;
  } | null)?.getRanges?.() ?? [];
  const active = ranges.at(-1);
  const multiple =
    (active?.getRows?.().length ?? 0) * (active?.getColumns?.().length ?? 0) > 1;
  if (multiple && active) {
    markRangeAria(active.getRows?.() ?? [], active.getColumns?.() ?? []);
  }
  return multiple;
}

function markRangeAriaByKeys(
  rowKeys: readonly (string | number)[],
  fields: readonly string[],
): void {
  const selectedRows = new Set(rowKeys);
  const rows = (tabulator?.value as unknown as {
    getRows?: () => Array<{
      getData?: () => Record<string, unknown>;
      getCells?: () => Array<{
        getField?: () => string;
        getElement?: () => HTMLElement;
      }>;
    }>;
  } | null)?.getRows?.()
    .filter((row) => {
      const rowKey = row.getData?.().rowKey;
      return (typeof rowKey === "string" || typeof rowKey === "number")
        && selectedRows.has(rowKey);
    }) ?? [];
  markRangeAria(rows, fields.map((field) => ({ getField: () => field })));
}

function markRangeAria(
  rows: Array<{
    getCells?: () => Array<{
      getField?: () => string;
      getElement?: () => HTMLElement;
    }>;
  }>,
  columns: Array<{ getField?: () => string }>,
): void {
  const fields = new Set(
    columns
      .map((column) => column.getField?.())
      .filter((field): field is string => !!field && field !== ROW_NUMBER_FIELD),
  );
  clearSelectionIndicators();
  for (const row of rows) {
    for (const cell of row.getCells?.() ?? []) {
      if (fields.has(cell.getField?.() ?? "")) {
        cell.getElement?.().setAttribute("aria-selected", "true");
      }
    }
  }
}

function onContextMenu(event: MouseEvent): void {
  const target = event.target;
  if (!(target instanceof Node)) return;
  const element = target instanceof Element ? target : target.parentElement;
  const header = element?.closest<HTMLElement>(".tabulator-col[data-field]");
  const headerField = header?.dataset.field;
  if (headerField && headerField !== ROW_NUMBER_FIELD) {
    event.preventDefault();
    emit("columnContext", { field: headerField, x: event.clientX, y: event.clientY });
    return;
  }
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

function onGridKeyDown(event: KeyboardEvent): void {
  const located = resolveKeyboardCell(event.target);
  if (!located) return;

  if (event.shiftKey && event.key === "F10") {
    event.preventDefault();
    event.stopPropagation();
    markSelection(located);
    const bounds = located.cellElement?.getBoundingClientRect()
      ?? located.rowElement?.getBoundingClientRect();
    emit("rowContext", {
      rowKey: located.rowKey,
      field: located.field && located.field !== ROW_NUMBER_FIELD
        ? located.field
        : undefined,
      x: bounds?.left ?? 0,
      y: bounds?.bottom ?? 0,
    });
    return;
  }

  if (event.key !== "Enter" && event.key !== " ") return;
  if (!located.field || located.field === ROW_NUMBER_FIELD) return;
  const column = store.schema?.find((item) => item.name === located.field);
  if (!column) return;

  if (column.kind === "attachment" && column.attachmentPolicy) {
    event.preventDefault();
    event.stopPropagation();
    markSelection(located);
    emit("attachmentOpen", { rowKey: located.rowKey, column });
    return;
  }
  if (column.dataType === "json") {
    const edit = store.editSchema?.find((item) => item.name === located.field);
    if (!edit?.editable) return;
    event.preventDefault();
    event.stopPropagation();
    markSelection(located);
    located.cellElement?.focus({ preventScroll: true });
    emit("jsonEdit", {
      rowKey: located.rowKey,
      column,
      value: located.rowData[located.field],
      expectedDigest: validDigest(located.rowData.__vibetableDigest),
      trigger: located.cellElement,
    });
    return;
  }
  if (column.kind === "relation" && column.relationId) {
    const descriptor = relationLookup.relation(column.relationId);
    if (!descriptor || descriptor.state !== "valid") return;
    event.preventDefault();
    event.stopPropagation();
    markSelection(located);
    emit("relationEdit", {
      rowKey: located.rowKey,
      field: located.field,
      descriptor,
      value: located.rowData[located.field],
    });
  }
}
</script>

<template>
  <div
    class="grid-wrapper"
    :class="[
      `density-${ui.density}`,
      { 'grid-wrapper--updating': dataApplying },
    ]"
    role="region"
    :aria-label="t('grid.ariaLabel')"
    :aria-busy="store.loading || dataApplying"
    tabindex="0"
    @click="onGridClick"
    @dblclick="onGridDoubleClick"
    @contextmenu="onContextMenu"
    @keydown.capture="onGridKeyDown"
  >
    <LookupGroupPanel />
    <div class="grid-host">
      <div ref="gridEl" class="tabulator-mount"></div>
      <div
        v-if="store.datasetReady && store.allRows.length === 0"
        class="grid-empty-state"
        data-testid="grid-empty-state"
      >
        <span class="grid-empty-icon"><NIcon :size="20"><Plus /></NIcon></span>
        <strong>{{ t("grid.empty.title") }}</strong>
        <p>{{ t("grid.empty.description") }}</p>
        <NButton
          type="primary"
          size="small"
          :disabled="props.insertRowDisabled"
          data-testid="grid-add-first-row"
          @click.stop="emit('insertFirstRow')"
        >
          <template #icon><NIcon><Plus /></NIcon></template>
          {{ t("grid.empty.addFirstRow") }}
        </NButton>
      </div>
    </div>
    <LoadingOverlay :show="store.loading" />
    <ErrorOverlay :show="!!store.error" :message="store.error ?? ''" />
  </div>
</template>

<style scoped>
.grid-wrapper {
  position: relative;
  display: flex;
  flex-direction: column;
  flex: 1 1 auto;
  min-height: 0;
  overflow: hidden;
  background: var(--vt-bg-subtle);
}
.grid-host {
  position: relative;
  flex: 1 1 auto;
  min-height: 0;
  overflow: hidden;
}
.grid-wrapper--updating .grid-host {
  pointer-events: none;
}
.grid-empty-state {
  position: absolute;
  inset: 0;
  z-index: 2;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 32px;
  color: var(--vt-fg-muted);
  text-align: center;
  background: var(--vt-bg);
}
.grid-empty-state strong {
  margin-top: 10px;
  color: var(--vt-fg);
  font-size: var(--vt-font-title);
  font-weight: 600;
}
.grid-empty-state p {
  max-width: 320px;
  margin: 4px 0 16px;
}
.grid-empty-icon {
  display: grid;
  width: 42px;
  height: 42px;
  place-items: center;
  color: var(--vt-color-primary-600);
  border: 1px solid color-mix(in srgb, var(--vt-color-primary-500) 22%, var(--vt-border));
  border-radius: var(--vt-radius-lg);
  background: var(--vt-color-primary-50);
}
.tabulator-mount {
  width: 100%;
  height: 100%;
  min-height: 0;
}
.grid-host :deep(.tabulator) {
  height: 100%;
  overflow: hidden;
  color: var(--vt-fg);
  border: 0;
  border-top: 1px solid var(--vt-border-subtle);
  font-size: var(--vt-font-body);
  background: var(--vt-bg);
}
.grid-host :deep(.tabulator .tabulator-header) {
  color: var(--vt-fg-secondary);
  border: 0;
  border-bottom: 1px solid var(--vt-border);
  background: var(--vt-bg-subtle);
  font-size: var(--vt-font-caption);
  font-weight: 600;
}
.grid-host :deep(.tabulator .tabulator-header .tabulator-col) {
  min-height: 36px;
  border-right: 1px solid var(--vt-border-subtle);
  background: transparent;
  transition:
    color var(--vt-duration-fast) var(--vt-ease),
    background var(--vt-duration-fast) var(--vt-ease);
}
.grid-host :deep(.tabulator .tabulator-header .tabulator-col:hover) {
  color: var(--vt-fg);
  background: var(--vt-bg-sunken);
}
.grid-host :deep(.tabulator .tabulator-header .tabulator-col-content) {
  padding: 8px 10px 7px;
}
.grid-host :deep(.tabulator .tabulator-header .tabulator-col-title) {
  overflow: hidden;
  line-height: 20px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.grid-host :deep(.tabulator .tabulator-header .tabulator-col-sorter) {
  right: 8px;
  width: 12px;
}
.grid-host :deep(.tabulator .tabulator-header .tabulator-col-sorter .tabulator-arrow) {
  border-right-width: 4px;
  border-bottom-width: 4px;
  border-left-width: 4px;
  opacity: .42;
}
.grid-host :deep(.tabulator .tabulator-header .tabulator-col[aria-sort="ascending"] .tabulator-arrow),
.grid-host :deep(.tabulator .tabulator-header .tabulator-col[aria-sort="descending"] .tabulator-arrow) {
  border-bottom-color: var(--vt-color-primary-600);
  opacity: 1;
}
.grid-host :deep(.tabulator .tabulator-header .tabulator-header-filter) {
  margin-top: 5px;
}
.grid-host :deep(.tabulator .tabulator-header .tabulator-header-filter input) {
  height: 26px;
  padding: 0 7px;
  color: var(--vt-fg);
  border: 1px solid transparent;
  border-radius: var(--vt-radius-sm);
  outline: 0;
  background: color-mix(in srgb, var(--vt-bg-sunken) 62%, transparent);
  font-size: 11px;
  opacity: .76;
  transition:
    border-color var(--vt-duration-fast) var(--vt-ease),
    box-shadow var(--vt-duration-fast) var(--vt-ease),
    opacity var(--vt-duration-fast) var(--vt-ease);
}
.grid-host :deep(.tabulator .tabulator-header .tabulator-header-filter input::placeholder) {
  color: var(--vt-fg-placeholder);
}
.grid-host :deep(.tabulator .tabulator-header .tabulator-header-filter input:focus) {
  border-color: var(--vt-color-primary-500);
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--vt-color-primary-500) 14%, transparent);
  background: var(--vt-bg);
  opacity: 1;
}
.grid-host :deep(.tabulator .tabulator-header .tabulator-header-filter input:not(:placeholder-shown)) {
  color: var(--vt-fg-accent);
  border-color: color-mix(in srgb, var(--vt-color-primary-500) 30%, var(--vt-border));
  background: color-mix(in srgb, var(--vt-color-primary-50) 68%, var(--vt-bg));
  opacity: 1;
}
.grid-host :deep(.tabulator .tabulator-tableholder) {
  overflow-x: auto;
  color: var(--vt-fg);
  background: var(--vt-bg-subtle);
}
.grid-host :deep(.tabulator .tabulator-tableholder:focus-visible) {
  outline: 2px solid var(--vt-color-primary-500);
  outline-offset: -2px;
}
.grid-host :deep(.tabulator .tabulator-table) {
  color: inherit;
  background: var(--vt-bg);
}
.grid-host :deep(.tabulator-row),
.grid-host :deep(.tabulator-row.tabulator-row-even),
.grid-host :deep(.tabulator-row.tabulator-row-odd) {
  min-height: 36px;
  color: var(--vt-fg);
  border: 0;
  border-bottom: 1px solid var(--vt-border-subtle);
  background: var(--vt-bg);
}
.grid-host :deep(.tabulator-row:hover),
.grid-host :deep(.tabulator-row.tabulator-selectable:hover) {
  background: color-mix(in srgb, var(--vt-color-primary-500) 4%, var(--vt-bg));
}
.grid-host :deep(.tabulator-row.tabulator-selected),
.grid-host :deep(.tabulator-row.tabulator-selected:hover) {
  color: var(--vt-fg);
  background: color-mix(in srgb, var(--vt-color-primary-500) 9%, var(--vt-bg));
}
.grid-host :deep(.tabulator-row .tabulator-cell) {
  min-height: 36px;
  padding: 7px 10px;
  color: inherit;
  border-right: 1px solid var(--vt-border-subtle);
  line-height: 21px;
  background: transparent;
}
.grid-wrapper.density-compact .grid-host :deep(.tabulator .tabulator-header .tabulator-col) {
  min-height: 31px;
}
.grid-wrapper.density-compact .grid-host :deep(.tabulator .tabulator-header .tabulator-col-content) {
  padding: 5px 8px 4px;
}
.grid-wrapper.density-compact .grid-host :deep(.tabulator .tabulator-header .tabulator-header-filter) {
  margin-top: 3px;
}
.grid-wrapper.density-compact .grid-host :deep(.tabulator .tabulator-header .tabulator-header-filter input) {
  height: 22px;
  padding-inline: 6px;
}
.grid-wrapper.density-compact .grid-host :deep(.tabulator-row),
.grid-wrapper.density-compact .grid-host :deep(.tabulator-row.tabulator-row-even),
.grid-wrapper.density-compact .grid-host :deep(.tabulator-row.tabulator-row-odd),
.grid-wrapper.density-compact .grid-host :deep(.tabulator-row .tabulator-cell) {
  min-height: 30px;
}
.grid-wrapper.density-compact .grid-host :deep(.tabulator-row .tabulator-cell) {
  padding: 4px 8px;
  line-height: 21px;
}
.grid-wrapper.density-compact .grid-host :deep(.tabulator-row .tabulator-cell.tabulator-editing) {
  padding: 2px 5px;
}
.grid-host :deep(.tabulator-row .tabulator-cell:last-child) {
  border-right-color: transparent;
}
.grid-host :deep(.tabulator-row .tabulator-cell.tabulator-editable:hover) {
  background: color-mix(in srgb, var(--vt-color-primary-500) 6%, transparent);
}
.grid-host :deep(.tabulator-row .tabulator-cell.tabulator-editing) {
  padding: 4px 7px;
  border: 1px solid var(--vt-color-primary-500);
  border-radius: 2px;
  outline: 0;
  box-shadow: inset 0 0 0 1px var(--vt-color-primary-500);
  background: var(--vt-bg);
}
.grid-host :deep(.tabulator-row .tabulator-cell.tabulator-editing input),
.grid-host :deep(.tabulator-row .tabulator-cell.tabulator-editing textarea) {
  color: var(--vt-fg);
  outline: 0;
  background: transparent;
}
.grid-host :deep(.tabulator-frozen) {
  border-right: 1px solid var(--vt-border) !important;
  box-shadow: 5px 0 10px -10px rgba(20, 28, 38, .46);
}
.grid-host :deep(.tabulator-placeholder) {
  min-height: 180px;
  color: var(--vt-fg-muted);
  background: var(--vt-bg);
}
.grid-host :deep(.tabulator-placeholder .tabulator-placeholder-contents) {
  padding: 40px 24px;
  font-size: var(--vt-font-body);
  font-weight: 500;
}
.grid-host :deep(.tabulator-range-overlay),
.grid-host :deep(.tabulator-range) {
  border-color: var(--vt-color-primary-500);
  background: transparent;
}
.grid-host :deep(.tabulator-row .tabulator-cell[aria-selected="true"]) {
  background: color-mix(in srgb, var(--vt-color-primary-500) 5%, var(--vt-bg));
}
.grid-host :deep(.tabulator-range-cell-active) {
  border-color: var(--vt-color-primary-600);
  box-shadow:
    inset 0 0 0 2px var(--vt-color-primary-600),
    0 0 0 1px color-mix(in srgb, var(--vt-color-primary-500) 16%, transparent);
}
.grid-host :deep(.tabulator-popup-container),
.grid-host :deep(.tabulator-menu) {
  color: var(--vt-fg);
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-md);
  background: var(--vt-bg-elevated);
  box-shadow: var(--vt-shadow-2);
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
  background: color-mix(in srgb, var(--vt-color-primary-500) 9%, var(--vt-bg));
}
.grid-host :deep(.tabulator-cell.vt-cell-selected) {
  background: color-mix(in srgb, var(--vt-color-primary-500) 5%, var(--vt-bg));
  box-shadow: inset 0 0 0 2px var(--vt-color-primary-600);
}
.grid-host :deep(.tabulator-cell.vt-structured-cell:focus-visible) {
  position: relative;
  z-index: 1;
  outline: 2px solid var(--vt-color-primary-500);
  outline-offset: -2px;
  box-shadow: inset 0 0 0 1px var(--vt-bg);
}
.grid-host :deep(.vt-json-value) {
  display: block;
  overflow: hidden;
  color: var(--vt-fg-secondary);
  font-family: Consolas, "SFMono-Regular", monospace;
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.grid-host :deep(.vt-json-cell) {
  cursor: default;
}
.grid-host :deep(.vt-attachment-cell),
.grid-host :deep(.vt-relation-cell--editable),
.grid-host :deep(.vt-json-cell) {
  cursor: pointer;
}
.grid-host :deep(.vt-attachment-summary) {
  display: inline-flex;
  align-items: center;
  max-width: 100%;
  gap: 6px;
  padding: 1px 7px 1px 5px;
  color: var(--vt-fg-accent);
  border: 1px solid color-mix(in srgb, var(--vt-color-primary-500) 22%, var(--vt-border));
  border-radius: 999px;
  background: color-mix(in srgb, var(--vt-color-primary-50) 58%, var(--vt-bg));
}
.grid-host :deep(.vt-attachment-summary__icon) {
  display: inline-grid;
  place-items: center;
  width: 16px;
  height: 16px;
  flex: 0 0 16px;
  border-radius: 50%;
  color: var(--vt-fg-accent-strong);
  background: color-mix(in srgb, var(--vt-color-primary-500) 12%, transparent);
  font-size: 12px;
  font-weight: 700;
  line-height: 1;
}
.grid-host :deep(.vt-attachment-summary__icon--add::before) {
  content: "+";
}
.grid-host :deep(.vt-attachment-summary__icon--existing::before) {
  content: "↗";
  transform: rotate(-45deg);
}
.grid-host :deep(.vt-attachment-summary__label) {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.grid-host :deep(.vt-relation-value),
.grid-host :deep(.vt-lookup-value) {
  display: flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
  height: 100%;
  overflow: hidden;
}
.grid-host :deep(.vt-relation-token) {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
  max-width: 160px;
  padding: 1px 7px;
  border: 1px solid color-mix(in srgb, var(--vt-color-primary-500) 24%, var(--vt-border));
  border-radius: 999px;
  color: var(--vt-fg);
  background: color-mix(in srgb, var(--vt-color-primary-500) 7%, var(--vt-bg));
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.grid-host :deep(.vt-relation-collection) {
  padding-right: 4px;
  border-right: 1px solid var(--vt-border);
  color: var(--vt-fg-accent-strong);
  font-size: 10px;
  font-weight: 700;
  letter-spacing: .02em;
}
.grid-host :deep(.vt-relation-more),
.grid-host :deep(.vt-cell-empty),
.grid-host :deep(.vt-lookup-mark) { color: var(--vt-fg-muted); }
.grid-host :deep(.vt-lookup-state) {
  display: inline-flex;
  align-items: center;
  padding: 1px 6px;
  border-radius: var(--vt-radius-sm);
  color: var(--vt-fg-secondary);
  background: var(--vt-bg-sunken);
  font-size: var(--vt-font-caption);
  white-space: nowrap;
}
.grid-host :deep(.vt-lookup-state--restricted) { color: var(--vt-color-warning); }
.grid-host :deep(.vt-lookup-state--invalid),
.grid-host :deep(.vt-lookup-state--too_expensive) { color: var(--vt-color-danger); }
.grid-host :deep(.vt-lookup-source) {
  flex: 0 0 auto;
  padding: 1px 5px;
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-sm);
  color: var(--vt-fg-accent-strong);
  background: var(--vt-bg);
  font: inherit;
  cursor: pointer;
}
.grid-host :deep(.vt-relation-cell--editable) { cursor: pointer; }
.grid-host :deep(.vt-relation-cell--editable:hover) {
  background: color-mix(in srgb, var(--vt-color-primary-500) 7%, var(--vt-bg));
}
</style>
