<script setup lang="ts">
import { computed, ref, watch } from "vue";
import PluginSurfaceHost from "@/components/plugins/PluginSurfaceHost.vue";
import type { PluginSurfaceThemeSnapshot, PluginTaskViewSnapshot, WebPluginActionDescription } from "@/contracts";

type HeadlessOutcomeStatus = "running" | "succeeded" | "failed" | "cancelled" | "aborted" | "unknown";

const props = defineProps<{
  description: WebPluginActionDescription;
  theme: PluginSurfaceThemeSnapshot;
  task?: PluginTaskViewSnapshot | null;
}>();
const emit = defineEmits<{
  close: [];
  cancel: [taskId: string];
  resolve: [decision: "approved" | "rejected"];
}>();
const surface = computed(() => props.description.surface);
const moduleOutcome = ref<{ status: HeadlessOutcomeStatus; summary: string }>({ status: "running", summary: "" });
const stopped = ref(false);
const cancelRequested = ref(false);
const currentTask = computed(() => Boolean(surface.value?.surfaceToken) && props.task?.surfaceToken === surface.value?.surfaceToken
  && props.task?.pluginId === props.description.pluginId && props.task?.actionId === props.description.actionId ? props.task : null);
const hostTerminal = ref<{ taskId: string; status: HeadlessOutcomeStatus; summary: string } | null>(null);
const outcome = computed(() => {
  if (hostTerminal.value && (!currentTask.value || hostTerminal.value.taskId === currentTask.value.taskId)) return hostTerminal.value;
  if (currentTask.value) return { status: "running" as const, summary: cancelRequested.value
    ? "停止请求已发送，等待宿主核对结果；请勿重复执行。" : "宿主任务正在执行。" };
  return moduleOutcome.value;
});
const frameMounted = computed(() => !stopped.value && surface.value !== null);
let cancellationTaskId: string | null = null;
watch(currentTask, task => {
  if (!task) return;
  if (["succeeded", "failed", "cancelled", "aborted"].includes(task.state)) {
    if (hostTerminal.value?.taskId !== task.taskId) hostTerminal.value = { taskId: task.taskId,
      status: task.state === "aborted" && task.error?.code === "plugin_commit_unknown" ? "unknown" : task.state as HeadlessOutcomeStatus,
      summary: task.error?.code === "plugin_commit_unknown" ? "写入结果未知，请核对实际数据；请勿重复执行。"
        : task.result?.summary || task.error?.message || "" };
    retireSurface();
  } else if (cancelRequested.value && cancellationTaskId !== task.taskId) {
    cancellationTaskId = task.taskId;
    emit("cancel", task.taskId);
  }
}, { immediate: true, deep: true });

const statusLabel = computed(() => {
  switch (outcome.value.status) {
    case "succeeded": return "已完成";
    case "failed": return "失败";
    case "cancelled": return "已取消";
    case "aborted": return "已中止";
    case "unknown": return "已结束（结果未知）";
    default: return cancelRequested.value ? "停止请求已发送，等待结果" : "运行中";
  }
});

function retireSurface(): void {
  if (stopped.value) return;
  stopped.value = true;
  // PluginSurfaceHost owns the single close notification during unmount.
}

function settleTerminal(status: HeadlessOutcomeStatus, summary: string): void {
  if (cancelRequested.value || moduleOutcome.value.status !== "running") return;
  moduleOutcome.value = { status, summary };
  retireSurface();
}

function onFinish(status: string, summary: string): void {
  // Module completion is local; a matching Host task owns any write outcome.
  if (status === "succeeded" || status === "failed" || status === "cancelled") {
    settleTerminal(status, summary);
  } else {
    settleTerminal("unknown", summary || "插件报告了未知状态，结果需人工核对。");
  }
}

function onSurfaceClosed(): void {
  // A bare close carries no status payload: the run ended, but nothing here
  // may claim success for unobserved requests or receipts.
  settleTerminal("unknown", "插件已结束执行，未报告结果状态。");
}

function cancelRun(): void {
  if (cancelRequested.value || hostTerminal.value) return;
  cancelRequested.value = true;
  moduleOutcome.value = { status: "unknown", summary: "本地执行已停止，写入结果仍待宿主核对；请勿重复执行。" };
  if (currentTask.value) {
    cancellationTaskId = currentTask.value.taskId;
    emit("cancel", currentTask.value.taskId);
  }
  retireSurface();
}
</script>

<template>
  <!--
    Visible Host status for a headless (no-UI) action. The restricted frame is
    released on module exit, Host settlement or a local stop request. Host
    task settlement remains visible after unmount; confirmations resolve
    through the native host window (application/headless are native-only).
  -->
  <section class="headless-runner" data-testid="plugin-headless-runner" role="status" aria-live="polite">
    <header>
      <div>
        <span>HEADLESS ACTION</span>
        <strong>{{ description.title }}</strong>
      </div>
      <b :data-state="outcome.status">{{ statusLabel }}</b>
    </header>
    <p v-if="outcome.summary" class="headless-summary">{{ outcome.summary }}</p>
    <p v-else>插件正在后台受限环境中执行，不展示界面。</p>
    <div class="headless-actions">
      <button
        v-if="outcome.status === 'running'"
        type="button"
        data-testid="plugin-headless-cancel"
        :disabled="cancelRequested"
        @click="cancelRun"
      >取消执行</button>
      <button
        v-if="outcome.status !== 'running'"
        type="button"
        data-testid="plugin-headless-close"
        @click="emit('close')"
      >关闭</button>
    </div>
    <PluginSurfaceHost
      v-if="frameMounted && surface"
      :src="surface.src"
      :surface-token="surface.surfaceToken"
      :title="description.title"
      :theme="theme"
      :task="currentTask"
      variant="headless"
      @finish="(status, summary) => onFinish(status, summary)"
      @close="onSurfaceClosed"
      @resolve="decision => emit('resolve', decision)"
      @cancel="cancelRun"
    />
    <p v-if="!surface" class="headless-error" role="alert">无界面动作没有可用的受限执行环境，请重新触发。</p>
  </section>
</template>

<style scoped>
.headless-runner { display: flex; width: min(520px, calc(100vw - 48px)); flex-direction: column; gap: 10px; padding: 16px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: var(--vt-bg); box-shadow: 0 22px 60px rgb(15 23 42 / 24%); }
.headless-runner header { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.headless-runner header div { display: grid; gap: 3px; }
.headless-runner span { color: var(--vt-color-primary-500); font: 650 9px ui-monospace, monospace; letter-spacing: .12em; }
.headless-runner strong { font-size: var(--vt-font-body); }
.headless-runner b { flex: 0 0 auto; padding: 3px 9px; border-radius: 999px; color: var(--vt-fg-muted); background: var(--vt-bg-sunken); font-size: var(--vt-font-caption); }
.headless-runner b[data-state="succeeded"] { color: var(--vt-color-success); }
.headless-runner b[data-state="failed"], .headless-runner b[data-state="cancelled"] { color: var(--vt-color-danger); }
.headless-runner b[data-state="unknown"] { color: var(--vt-color-warning); }
.headless-runner p { margin: 0; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.headless-summary { color: var(--vt-fg); }
.headless-error { color: var(--vt-color-danger); }
.headless-actions { display: flex; justify-content: flex-end; gap: 7px; }
.headless-actions button { min-height: 28px; padding: 0 12px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-sm); background: var(--vt-bg); cursor: pointer; }
.headless-actions button:hover { background: var(--vt-bg-sunken); }
.headless-runner :deep(.surface-shell) { border: 0; min-height: 0; background: transparent; }
</style>
