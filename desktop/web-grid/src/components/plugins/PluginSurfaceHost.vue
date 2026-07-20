<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import type { PluginSurfaceThemeSnapshot, PluginTaskViewSnapshot } from "@/contracts";
import { useHostBridge } from "@/services/bridgeContext";
import { PLUGIN_SURFACE_CSP, isTrustedSurfaceMessage, validatePluginSurfaceUrl } from "./pluginSurfacePolicy";

const props = defineProps<{
  src: string;
  surfaceToken: string;
  title: string;
  theme: PluginSurfaceThemeSnapshot;
  task?: PluginTaskViewSnapshot | null;
}>();
const emit = defineEmits<{
  close: [];
  action: [payload: Readonly<Record<string, unknown>>];
  resolve: [decision: "approved" | "rejected"];
  cancel: [];
}>();
const bridge = useHostBridge();
const frame = ref<HTMLIFrameElement | null>(null);
const activeToken = ref(props.surfaceToken);
const valid = computed(() => validatePluginSurfaceUrl(props.src));
const origin = computed(() => valid.value ? new URL(props.src).origin : "");

function sendTheme(): void {
  if (!valid.value || !activeToken.value) return;
  frame.value?.contentWindow?.postMessage({
    contract: "vibetable.plugin-surface.v1",
    surfaceToken: activeToken.value,
    event: "themeChanged",
    payload: props.theme,
  }, origin.value);
}

function sendTask(): void {
  if (!valid.value || !activeToken.value || !props.task) return;
  frame.value?.contentWindow?.postMessage({
    contract: "vibetable.plugin-surface.v1",
    surfaceToken: activeToken.value,
    event: "taskChanged",
    payload: props.task,
  }, origin.value);
}

function onMessage(event: MessageEvent): void {
  if (!activeToken.value || !isTrustedSurfaceMessage(event, {
    expectedOrigin: origin.value,
    expectedSource: frame.value?.contentWindow ?? null,
    surfaceToken: activeToken.value,
  })) return;
  bridge.notify("plugin.surface.event", event.data);
  if (event.data.event === "action") {
    const payload = typeof event.data.payload === "object" && event.data.payload !== null
      && !Array.isArray(event.data.payload)
      ? event.data.payload as Readonly<Record<string, unknown>>
      : {};
    emit("action", payload);
  }
  if (event.data.event === "close") close();
}

function close(): void {
  if (!activeToken.value) return;
  const token = activeToken.value;
  activeToken.value = "";
  bridge.notify("plugin.surface.event", {
    contract: "vibetable.plugin-surface.v1",
    surfaceToken: token,
    event: "close",
    payload: {},
  });
  emit("close");
}

watch(() => props.theme, sendTheme, { deep: true });
watch(() => props.task, sendTask, { deep: true });
watch(() => props.surfaceToken, (token) => { activeToken.value = token; });
onMounted(() => window.addEventListener("message", onMessage));
onBeforeUnmount(() => {
  window.removeEventListener("message", onMessage);
  close();
});
</script>

<template>
  <section class="surface-shell" role="region" :aria-label="title">
    <header>
      <div><span>ISOLATED SURFACE</span><strong>{{ title }}</strong></div>
      <button type="button" aria-label="关闭插件页面" @click="close">×</button>
    </header>
    <iframe
      v-if="valid"
      ref="frame"
      :src="src"
      :title="title"
      sandbox="allow-scripts allow-same-origin"
      referrerpolicy="no-referrer"
      :data-required-csp="PLUGIN_SURFACE_CSP"
      @load="() => { sendTheme(); sendTask(); }"
    />
    <div v-else class="surface-error" role="alert">插件页面地址未通过不可变 origin 校验。</div>
    <footer v-if="task" class="host-runtime" aria-live="polite">
      <div class="task-status">
        <span><b :data-state="task.state"></b>{{ task.state }}</span>
        <code>{{ task.taskId }}</code>
        <button
          v-if="task.state === 'queued' || task.state === 'running'"
          type="button"
          :disabled="task.cancelRequested || task.progress?.cancellable === false"
          @click="emit('cancel')"
        >{{ task.cancelRequested ? '已请求取消' : '请求取消' }}</button>
      </div>
      <section v-if="task.confirmation" class="host-confirmation" role="alertdialog" aria-modal="true">
        <div><span>HOST FINAL {{ task.confirmation.risk.toUpperCase() }} CONFIRMATION</span><strong>{{ task.confirmation.title }}</strong><p>{{ task.confirmation.summary }}</p></div>
        <div class="confirmation-actions">
          <button type="button" @click="emit('resolve', 'rejected')">拒绝</button>
          <button data-testid="plugin-surface-confirm-approve" class="approve" type="button" @click="emit('resolve', 'approved')">由宿主确认并继续</button>
        </div>
      </section>
      <section v-if="task.error" data-testid="plugin-surface-task-error" class="host-error" role="alert">
        <div><span>SAFE ERROR / {{ task.error.code }}</span><strong>{{ task.error.message }}</strong></div>
        <p>恢复方式：<code>{{ task.error.recoverability }}</code></p>
      </section>
      <section v-if="task.result" class="host-result"><strong>{{ task.result.summary }}</strong></section>
    </footer>
  </section>
</template>

<style scoped>
.surface-shell { display: flex; min-height: 360px; flex-direction: column; overflow: hidden; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); background: var(--vt-bg-sunken); }
header { display: flex; flex: 0 0 42px; align-items: center; justify-content: space-between; padding: 0 10px 0 13px; border-bottom: 1px solid var(--vt-border); background: var(--vt-bg); }
header div { display: flex; align-items: baseline; gap: 10px; }
header span { color: var(--vt-color-success); font: 600 10px/1 ui-monospace, SFMono-Regular, Consolas, monospace; letter-spacing: .12em; }
header strong { font-size: var(--vt-font-body); }
header button { width: 28px; height: 28px; color: var(--vt-fg-muted); border: 0; border-radius: var(--vt-radius-sm); background: transparent; cursor: pointer; }
header button:hover { color: var(--vt-fg); background: var(--vt-bg-sunken); }
iframe { flex: 1 1 auto; width: 100%; min-height: 320px; border: 0; background: var(--vt-bg); }
.surface-error { display: grid; flex: 1 1 auto; place-items: center; color: var(--vt-color-danger); }
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
.host-confirmation span { color: var(--vt-color-danger); font: 650 9px ui-monospace, monospace; letter-spacing: .1em; }
.host-confirmation p { margin: 0; color: var(--vt-fg-muted); }
.confirmation-actions { display: flex; gap: 7px; }
.confirmation-actions .approve { color: #fff; border-color: var(--vt-color-danger); background: var(--vt-color-danger); }
.host-result { padding: 10px 12px; color: var(--vt-color-success); border-top: 1px solid var(--vt-border); }
.host-error { display: flex; align-items: center; justify-content: space-between; gap: 14px; padding: 10px 12px; color: var(--vt-color-danger); border-top: 1px solid var(--vt-border); border-left: 3px solid var(--vt-color-danger); }
.host-error div { display: grid; gap: 3px; }
.host-error span { font: 650 9px ui-monospace, monospace; letter-spacing: .1em; }
.host-error p { margin: 0; color: var(--vt-fg); }
</style>
