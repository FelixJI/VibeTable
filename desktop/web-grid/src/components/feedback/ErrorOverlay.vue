<script setup lang="ts">
import { NResult } from "naive-ui";

defineProps<{ show: boolean; message: string }>();
</script>

<template>
  <div v-if="show" class="overlay overlay--error" data-testid="table-error-overlay" role="alert">
    <NResult status="error" :description="message" />
  </div>
</template>

<style scoped>
.overlay {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 10;
}
.overlay--error {
  color: var(--vt-color-danger-600);
  background: color-mix(in srgb, var(--vt-color-danger-500) 8%, var(--vt-bg) 92%);
  backdrop-filter: blur(2px);
}
</style>
