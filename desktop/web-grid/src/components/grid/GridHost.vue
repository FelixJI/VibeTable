<script setup lang="ts">
import { ref } from "vue";
import { useTabulator } from "@/composables/useTabulator";
import { useTableStore } from "@/stores/tableStore";
import LoadingOverlay from "@/components/feedback/LoadingOverlay.vue";
import ErrorOverlay from "@/components/feedback/ErrorOverlay.vue";

const gridEl = ref<HTMLElement | null>(null);
const store = useTableStore();
useTabulator(gridEl);
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
