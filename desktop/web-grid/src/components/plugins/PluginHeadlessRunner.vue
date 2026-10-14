<script setup lang="ts">
import { computed, ref } from "vue";
import PluginSurfaceHost from "@/components/plugins/PluginSurfaceHost.vue";
import { useHostBridge } from "@/services/bridgeContext";
import type { PluginSurfaceThemeSnapshot, PluginTaskViewSnapshot, WebPluginActionDescription } from "@/contracts";

type HeadlessOutcomeStatus = "running" | "succeeded" | "failed" | "cancelled" | "unknown";

const props = defineProps<{
  description: WebPluginActionDescription;
  theme: PluginSurfaceThemeSnapshot;
  task?: PluginTaskViewSnapshot | null;
}>();
const emit = defineEmits<{
  close: [];
  cancel: [];
  resolve: [decision: "approved" | "rejected"];
  cancelTask: [];
}>();
const bridge = useHostBridge();
const surface = computed(() => props.description.surface);
const outcome = ref<{ status: HeadlessOutcomeStatus; summary: string }>({
  status: "running",
  summary: "",
});
// The restricted frame is mounted ONLY while the run is active. A terminal
// outcome unmounts the frame (stopping its JS/CPU for real, not just revoking
// the token) while the Host-owned status above stays visible.
const frameMounted = computed(() => outcome.value.status === "running" && surface.value !== null);

const statusLabel = computed(() => {
  switch (outcome.value.status) {
    case "succeeded": return "已完成";
    case "failed": return "失败";
    case "cancelled": return "已取消";
    case "unknown": return "已结束（结果未知）";
    default: return "运行中";
  }
});

function retireSurface(): void {
  const token = surface.value?.surfaceToken;
  if (!token) return;
  // Best-effort token retire; unmounting below retires the WPF side through
  // frame Destroyed as well.
  bridge.notify("plugin.surface.event", {
    contract: "vibetable.plugin-surface.v1",
    surfaceToken: token,
    event: "close",
    payload: {},
  });
}

function settleTerminal(status: HeadlessOutcomeStatus, summary: string): void {
  if (outcome.value.status !== "running") return;
  outcome.value = { status, summary };
  retireSurface();
}

function onFinish(status: string, summary: string): void {
  // Only the module's explicit status settles a known terminal outcome; any
  // other status string stays UNKNOWN — never reported as a write success.
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
  settleTerminal("cancelled", "已由宿主取消");
  emit("cancel");
}
</script>

<template>
  <!--
    Visible Host status for a headless (no-UI) action. The restricted frame is
    hidden while running and UNMOUNTED at the first terminal outcome
    (finish/failure/cancel), releasing the token/frame; confirmations resolve
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
      :task="task"
      variant="headless"
      @finish="(status, summary) => onFinish(status, summary)"
      @close="onSurfaceClosed"
      @resolve="decision => emit('resolve', decision)"
      @cancel="() => emit('cancelTask')"
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
