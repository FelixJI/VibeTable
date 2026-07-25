<script setup lang="ts">
import { NSpin } from "naive-ui";
import { t } from "@/i18n";

defineProps<{ show: boolean }>();
</script>

<template>
  <div
    v-if="show"
    class="overlay overlay--loading"
    role="status"
    aria-live="polite"
    :aria-label="t('app.loading')"
  >
    <NSpin size="medium" />
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
.overlay--loading {
  background: rgba(255, 255, 255, 0.7);
}
:root.dark .overlay--loading {
  background: rgba(23, 25, 31, 0.7);
}
</style>
