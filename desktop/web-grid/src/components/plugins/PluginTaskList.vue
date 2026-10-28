<script setup lang="ts">
import type { PluginTaskViewSnapshot } from "@/contracts";
import { t } from "@/i18n";
defineProps<{ tasks: readonly PluginTaskViewSnapshot[]; variant?: "panel" | "app" | "headless" }>();
const emit = defineEmits<{ cancel: [taskId: string]; resolve: [decision: "approved" | "rejected", taskId: string] }>();
</script>
<template>
  <div v-if="tasks.length" class="task-list" aria-label="宿主任务列表">
    <footer v-for="task in tasks" :key="task.taskId" :data-task-id="task.taskId" class="host-runtime" aria-live="polite">
      <div class="task-status">
        <span><b :data-state="task.state"></b>{{ task.state }}</span>
        <code>{{ task.taskId }}</code>
        <button
          v-if="task.state === 'queued' || task.state === 'running'"
          type="button"
          :disabled="task.cancelRequested || task.progress?.cancellable === false"
          @click="emit('cancel', task.taskId)"
        >{{ task.cancelRequested ? '已请求取消' : '请求取消' }}</button>
      </div>
      <section v-if="task.confirmation" class="host-confirmation" role="alertdialog" aria-modal="true">
        <div><span>{{ t("pluginConfirm.hostFinal", { risk: t(`plugin.risk.${task.confirmation.risk}`) }) }}</span><strong>{{ task.confirmation.title }}</strong><p>{{ task.confirmation.summary }}</p></div>
        <p v-if="variant === 'app' || variant === 'headless'">请在宿主原生确认窗口中批准或拒绝。</p>
        <div v-else class="confirmation-actions">
          <button type="button" @click="emit('resolve', 'rejected', task.taskId)">拒绝</button>
          <button data-testid="plugin-surface-confirm-approve" class="approve" type="button" @click="emit('resolve', 'approved', task.taskId)">由宿主确认并继续</button>
        </div>
      </section>
      <section v-if="task.error" data-testid="plugin-surface-task-error" class="host-error" role="alert">
        <div><span>{{ t("pluginTask.error", { code: task.error.code }) }}</span><strong>{{ task.error.message }}</strong></div>
        <p v-if="task.error.code === 'plugin_commit_unknown'">写入结果未知，请核对实际数据；请勿重复执行。</p>
        <p>恢复方式：<code>{{ task.error.recoverability }}</code></p>
      </section>
      <section v-if="task.result" class="host-result"><strong>{{ task.result.summary }}</strong></section>
    </footer>
  </div>
</template>
<style scoped>
.task-list { flex: 0 1 auto; min-height: 0; max-height: min(320px, 40vh); overflow: auto; }
.host-runtime { flex: 0 0 auto; border-top: 1px solid var(--vt-border); background: var(--vt-bg); }
.task-status { display: flex; min-height: 36px; align-items: center; gap: 10px; padding: 0 12px; }
.task-status span { display: inline-flex; align-items: center; gap: 6px; font-weight: 600; }
.task-status b { width: 7px; height: 7px; border-radius: 50%; background: var(--vt-color-warning); }
.task-status b[data-state="succeeded"] { background: var(--vt-color-success); }
.task-status b[data-state="failed"], .task-status b[data-state="aborted"] { background: var(--vt-color-danger); }
.task-status code { margin-right: auto; color: var(--vt-fg-muted); font-size: 10px; }
.task-status button, .confirmation-actions button { min-height: 28px; padding: 0 9px; border: 1px solid var(--vt-border); border-radius: 4px; background: var(--vt-bg); }
.task-status button:disabled { opacity: .55; }
.host-confirmation { display: flex; align-items: center; justify-content: space-between; gap: 18px; padding: 12px; border-top: 1px solid color-mix(in srgb, var(--vt-color-danger) 35%, var(--vt-border)); border-left: 3px solid var(--vt-color-danger); background: color-mix(in srgb, var(--vt-color-danger) 6%, var(--vt-bg)); }
.host-confirmation > div:first-child { display: grid; gap: 3px; }
.host-confirmation span { color: var(--vt-color-danger); font-size: 10px; font-weight: 650; }
.host-confirmation p { margin: 0; color: var(--vt-fg-muted); }
.confirmation-actions { display: flex; gap: 7px; }
.confirmation-actions .approve { color: #fff; border-color: var(--vt-color-danger); background: var(--vt-color-danger); }
.host-result { padding: 10px 12px; color: var(--vt-color-success); border-top: 1px solid var(--vt-border); }
.host-error { display: flex; align-items: center; justify-content: space-between; gap: 14px; padding: 10px 12px; color: var(--vt-color-danger); border-top: 1px solid var(--vt-border); border-left: 3px solid var(--vt-color-danger); }
.host-error div { display: grid; gap: 3px; }
.host-error span { font-size: 10px; font-weight: 650; }
.host-error p { margin: 0; color: var(--vt-fg); }
</style>
