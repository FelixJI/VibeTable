<script setup lang="ts">
import { ref } from "vue";
import { useTabulator } from "@/composables/useTabulator";
import type { CellEditedHandler } from "@/grid/createGrid";
import { useTableStore } from "@/stores/tableStore";
import LoadingOverlay from "@/components/feedback/LoadingOverlay.vue";
import ErrorOverlay from "@/components/feedback/ErrorOverlay.vue";

/**
 * Inline-edit callback. GridHost is a thin presentational wrapper around
 * `useTabulator`; the actual routing to `mutationService.updateCell` lives in
 * WorkspaceView (the integration layer). Optional so GridHost stays usable in
 * read-only contexts.
 */
const props = defineProps<{ onCellEdited?: CellEditedHandler }>();

const gridEl = ref<HTMLElement | null>(null);
const store = useTableStore();
useTabulator(gridEl, { onCellEdited: props.onCellEdited });
</script>

<template>
  <div class="grid-wrapper">
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
