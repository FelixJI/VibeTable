<script setup lang="ts">
import { computed, ref, watch } from "vue";
import PluginTaskList from "./PluginTaskList.vue";
import PluginSurfaceHost from "@/components/plugins/PluginSurfaceHost.vue";
import type { PluginSurfaceThemeSnapshot, PluginTaskViewSnapshot, WebPluginActionDescription } from "@/contracts";

type HeadlessOutcomeStatus = "running" | "succeeded" | "failed" | "cancelled" | "aborted" | "unknown" | "partial";

const props = defineProps<{
  description: WebPluginActionDescription;
  theme: PluginSurfaceThemeSnapshot;
  navigateBack?: (surfaceToken: string) => boolean;
  task?: PluginTaskViewSnapshot | null;
  tasks?: readonly PluginTaskViewSnapshot[];
}>();
const emit = defineEmits<{
  close: [];
  cancel: [taskId: string];
  resolve: [decision: "approved" | "rejected", taskId: string];
}>();
const surface = computed(() => props.description.surface);
const moduleOutcome = ref<{ status: HeadlessOutcomeStatus; summary: string }>({ status: "running", summary: "" });
const stopped = ref(false);
const cancelRequested = ref(false);
const receivedTasks = computed(() => (props.tasks ?? (props.task ? [props.task] : [])).filter(task =>
  Boolean(surface.value?.surfaceToken) && task.surfaceToken === surface.value?.surfaceToken
  && task.pluginId === props.description.pluginId && task.actionId === props.description.actionId));
const knownTasks = ref<Record<string, PluginTaskViewSnapshot>>({});
const currentTasks = computed(() => Object.values(knownTasks.value));
const terminal = (task: PluginTaskViewSnapshot) => ["succeeded", "failed", "cancelled", "aborted"].includes(task.state);
const cancellations = new Set<string>();
function cancelTask(taskId: string): void {
  const task = knownTasks.value[taskId];
  if (!task || terminal(task) || cancellations.has(taskId)) return;
  cancellations.add(taskId);
  emit("cancel", taskId);
}
watch(receivedTasks, tasks => {
  for (const task of tasks) {
    // Terminal receipts remain sticky even while other tasks on this run settle.
    if (!knownTasks.value[task.taskId] || !terminal(knownTasks.value[task.taskId]!))
      knownTasks.value[task.taskId] = task;
    if (cancelRequested.value) cancelTask(task.taskId);
  }
}, { immediate: true, deep: true });
const outcome = computed(() => {
  const tasks = currentTasks.value;
  if (!tasks.length) return moduleOutcome.value;
  if (!stopped.value || tasks.some(task => !terminal(task))) return { status: "running" as const, summary: cancelRequested.value
    ? "停止请求已发送，等待宿主核对结果；请勿重复执行。" : "宿主任务正在执行。" };
  if (tasks.some(task => task.error?.code === "plugin_commit_unknown"))
    return { status: "unknown" as const, summary: "写入结果未知，请核对实际数据；请勿重复执行。" };
  const saved = tasks.filter(task => task.state === "succeeded");
  if (!cancelRequested.value && moduleOutcome.value.status === "unknown")
    return { status: "unknown" as const, summary: `${moduleOutcome.value.summary} 已确认的成功写入请见下方宿主记录；其余结果未知，请勿重复执行。` };
  if (saved.length && (saved.length !== tasks.length
    || (!cancelRequested.value && moduleOutcome.value.status !== "succeeded")))
    return { status: "partial" as const, summary: `部分写入已完成：${saved.map(task => task.result?.summary || task.taskId).join("；")}。其余结果请核对下方宿主记录；请勿重复执行。` };
  const failed = tasks.find(task => task.state === "failed" || task.state === "aborted");
  if (failed) return { status: failed.state as HeadlessOutcomeStatus, summary: failed.error?.message || "" };
  if (tasks.some(task => task.state === "cancelled")) return { status: "cancelled" as const, summary: "" };
  return { status: "succeeded" as const, summary: tasks.map(task => task.result?.summary).filter(Boolean).join("；") };
});
// A Host task terminal is not module completion: one frame may have many requests.
const frameMounted = computed(() => !stopped.value && surface.value !== null);

const statusLabel = computed(() => {
  switch (outcome.value.status) {
    case "succeeded": return "已完成";
    case "failed": return "失败";
    case "cancelled": return "已取消";
    case "aborted": return "已中止";
    case "unknown": return "已结束（结果未知）";
    case "partial": return "部分完成（请核对）";
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
  if (cancelRequested.value) return;
  cancelRequested.value = true;
  moduleOutcome.value = { status: "unknown", summary: "本地执行已停止，写入结果仍待宿主核对；请勿重复执行。" };
  for (const task of currentTasks.value) cancelTask(task.taskId);
  retireSurface();
}
</script>

<template>
  <!--
    Visible Host status for a headless (no-UI) action. The restricted frame is
    released on module exit or a local stop request. Host
    task settlement remains visible after unmount; confirmations resolve
    through the native host window (application/headless are native-only).
  -->
  <section class="headless-runner" data-testid="plugin-headless-runner" role="status" aria-live="polite">
    <header>
      <div>
        <strong>{{ description.title }}</strong>
      </div>
      <b :data-state="outcome.status">{{ statusLabel }}</b>
    </header>
    <p v-if="outcome.summary" class="headless-summary">{{ outcome.summary }}</p>
    <p v-else>插件正在后台受限环境中执行，不展示界面。</p>
    <p v-if="currentTasks.length && stopped && moduleOutcome.status !== 'running'" data-testid="headless-module-result">
      {{ cancelRequested ? '本地执行已停止；各任务结果以下方宿主记录为准。' : `本地执行已结束：${moduleOutcome.summary || moduleOutcome.status}` }}
    </p>
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
        :navigate-back="navigateBack"
      :tasks="currentTasks"
      :show-tasks="false"
      variant="headless"
      @finish="(status, summary) => onFinish(status, summary)"
      @close="onSurfaceClosed"
      @resolve="(decision, taskId) => emit('resolve', decision, taskId)"
      @cancel="cancelTask"
    />
    <PluginTaskList :tasks="currentTasks" variant="headless"
      @cancel="cancelTask" @resolve="(decision, taskId) => emit('resolve', decision, taskId)" />
    <p v-if="!surface" class="headless-error" role="alert">无界面动作没有可用的受限执行环境，请重新触发。</p>
  </section>
</template>

<style scoped>
.headless-runner { display: flex; width: min(520px, calc(100vw - 48px)); flex-direction: column; gap: 10px; padding: 16px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: var(--vt-bg); box-shadow: 0 22px 60px rgb(15 23 42 / 24%); }
.headless-runner header { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.headless-runner header div { display: grid; gap: 3px; }
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
