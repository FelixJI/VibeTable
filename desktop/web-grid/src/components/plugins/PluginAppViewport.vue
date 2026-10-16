<script setup lang="ts">
import PluginSurfaceHost from "@/components/plugins/PluginSurfaceHost.vue";
import type { PluginSurfaceThemeSnapshot, PluginTaskViewSnapshot } from "@/contracts";

defineProps<{
  src: string;
  surfaceToken: string;
  title: string;
  theme: PluginSurfaceThemeSnapshot;
  navigateBack?: (surfaceToken: string) => boolean;
  task?: PluginTaskViewSnapshot | null;
  tasks?: readonly PluginTaskViewSnapshot[];
}>();
const emit = defineEmits<{
  resolve: [decision: "approved" | "rejected", taskId: string];
  cancel: [taskId: string];
  close: [];
  manage: [];
}>();
</script>

<template>
  <!--
    Full business viewport for a plugin application. The plugin iframe fills
    the available viewport; this chrome and the shell navigation stay outside
    the frame, so workspace/plugin management, 返回平台 and the trusted task
    footer are always reachable and can never be shadowed by plugin DOM.
  -->
  <section class="plugin-app-viewport" data-testid="plugin-app-viewport" role="region" :aria-label="title">
    <header class="plugin-app-bar">
      <div class="plugin-app-title">
        <span>PLUGIN APP</span>
        <strong>{{ title }}</strong>
      </div>
      <div class="plugin-app-actions">
        <button type="button" data-testid="plugin-app-manage" @click="emit('manage')">管理插件</button>
        <button type="button" data-testid="plugin-app-return" class="primary" @click="emit('close')">返回平台</button>
      </div>
    </header>
    <div class="plugin-app-body">
      <PluginSurfaceHost
        :src="src"
        :surface-token="surfaceToken"
        :title="title"
        :theme="theme"
        :navigate-back="navigateBack"
        :task="task"
        :tasks="tasks"
        variant="app"
        @resolve="(decision, taskId) => emit('resolve', decision, taskId)"
        @cancel="taskId => emit('cancel', taskId)"
        @close="() => emit('close')"
      />
    </div>
  </section>
</template>

<style scoped>
.plugin-app-viewport { display: flex; width: 100%; height: 100%; min-width: 0; min-height: 0; flex-direction: column; overflow: hidden; background: var(--vt-bg); }
.plugin-app-bar { display: flex; flex: 0 0 40px; align-items: center; justify-content: space-between; gap: 12px; padding: 0 12px; border-bottom: 1px solid var(--vt-border); background: var(--vt-bg); }
.plugin-app-title { display: flex; min-width: 0; align-items: baseline; gap: 10px; }
.plugin-app-title span { color: var(--vt-color-primary-500); font: 600 10px/1 ui-monospace, SFMono-Regular, Consolas, monospace; letter-spacing: .12em; }
.plugin-app-title strong { overflow: hidden; font-size: var(--vt-font-body); text-overflow: ellipsis; white-space: nowrap; }
.plugin-app-actions { display: flex; flex: 0 0 auto; gap: 7px; }
.plugin-app-actions button { min-height: 26px; padding: 0 10px; color: var(--vt-fg); border: 1px solid var(--vt-border); border-radius: var(--vt-radius-sm); background: var(--vt-bg); cursor: pointer; }
.plugin-app-actions button:hover { background: var(--vt-bg-sunken); }
.plugin-app-actions button.primary { color: #fff; border-color: var(--vt-color-primary-500); background: var(--vt-color-primary-500); }
.plugin-app-body { display: flex; flex: 1 1 auto; min-width: 0; min-height: 0; flex-direction: column; }
.plugin-app-body > :deep(.surface-shell) { flex: 1 1 auto; width: 100%; min-height: 0; }
</style>
