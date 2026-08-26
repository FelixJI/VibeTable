<script setup lang="ts">
import { computed } from "vue";
import { NButton, NIcon, NSpin, NTooltip } from "naive-ui";
import { AlertCircle, Cloud, CloudOff } from "@lucide/vue";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { t } from "@/i18n";

const workspace = useWorkspaceStore();
const emit = defineEmits<{ reconnect: [] }>();

const state = computed(() => {
  if (workspace.phase === "opened") return "healthy";
  if (workspace.phase === "opening") return "loading";
  if (workspace.phase === "failed") return "failed";
  return "idle";
});
</script>

<template>
  <div v-if="state === 'healthy'" class="connection-pill connection-pill--healthy" data-testid="connection-pill">
    <NIcon :size="14"><Cloud /></NIcon>
    <span>{{ t("connection.connected") }}</span>
    <span class="connection-pill__count">
      {{ t("connection.connectedCount", { count: workspace.collections.length }) }}
    </span>
  </div>
  <div v-else-if="state === 'loading'" class="connection-pill" data-testid="connection-pill">
    <NSpin :size="13" />
    <span>{{ t("connection.connecting") }}</span>
  </div>
  <NTooltip v-else placement="bottom-end" :delay="300">
    <template #trigger>
      <NButton
        size="tiny"
        quaternary
        class="connection-pill connection-pill--action"
        :class="{ 'connection-pill--failed': state === 'failed' }"
        :aria-label="t('connection.retry')"
        data-testid="connection-retry"
        @click="emit('reconnect')"
      >
        <template #icon>
          <NIcon :size="14"><component :is="state === 'failed' ? CloudOff : AlertCircle" /></NIcon>
        </template>
        {{ state === "failed" ? t("connection.failed") : t("connection.waiting") }}
      </NButton>
    </template>
    {{ workspace.lastError || t("connection.retryHint") }}
  </NTooltip>
</template>

<style scoped>
.connection-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-height: 26px;
  padding: 0 9px;
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
  white-space: nowrap;
  border: 1px solid var(--vt-border);
  border-radius: 999px;
  background: var(--vt-bg);
}
.connection-pill--healthy {
  color: #008a68;
  border-color: rgba(0, 184, 138, 0.25);
  background: rgba(0, 184, 138, 0.07);
}
.connection-pill__count { color: var(--vt-fg-muted); }
.connection-pill__count::before { margin-right: 6px; content: "·"; }
:root.dark .connection-pill--healthy .connection-pill__count { color: var(--vt-fg-muted); }
.connection-pill--action {
  height: 26px;
}
.connection-pill--failed {
  color: var(--vt-color-danger);
  border-color: rgba(245, 74, 69, 0.28);
  background: rgba(245, 74, 69, 0.06);
}
:root.dark .connection-pill--healthy {
  color: #55d9b4;
}
</style>
