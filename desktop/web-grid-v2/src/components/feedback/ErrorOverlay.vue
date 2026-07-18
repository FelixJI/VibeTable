<script setup lang="ts">
import { NResult } from "naive-ui";

defineProps<{ show: boolean; message: string }>();
</script>

<template>
  <div v-if="show" class="overlay overlay--error">
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
  background: rgba(254, 226, 226, 0.9);
}
</style>
