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
import { useTableStore } from "@/stores/tableStore";
import { TABULATOR_INJECTION_KEY } from "./tabulatorInjection";
import LoadingOverlay from "@/components/feedback/LoadingOverlay.vue";
import ErrorOverlay from "@/components/feedback/ErrorOverlay.vue";

const props = defineProps<{
  onCellEdited?: CellEditedHandler;
  onValidationError?: CellValidationErrorHandler;
}>();
const emit = defineEmits<{
  rowContext: [payload: { rowKey: string | number; x: number; y: number }];
}>();

const gridEl = ref<HTMLElement | null>(null);
const store = useTableStore();
const tabulator = inject<Ref<TabulatorFull | null>>(TABULATOR_INJECTION_KEY);
useTabulator(gridEl, {
  onCellEdited: props.onCellEdited,
  onValidationError: props.onValidationError,
  tabulator: tabulator ?? undefined,
});

function onContextMenu(event: MouseEvent): void {
  const target = event.target;
  if (!(target instanceof Node) || !tabulator?.value) return;
  const row = tabulator.value.getRows("active").find((candidate) =>
    candidate.getElement?.().contains(target),
  );
  const rowKey = row?.getData()?.rowKey;
  if (typeof rowKey !== "string" && typeof rowKey !== "number") return;
  event.preventDefault();
  emit("rowContext", { rowKey, x: event.clientX, y: event.clientY });
}
</script>

<template>
  <div class="grid-wrapper" @contextmenu="onContextMenu">
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
</style>
