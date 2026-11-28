<script setup lang="ts">
import { createPluginSurfaceUi, projectSurfaceTasks } from "./pluginSurfaceUi";
import PluginTaskList from "./PluginTaskList.vue";
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import type { PluginSurfacePhase, PluginSurfaceThemeSnapshot, PluginTaskViewSnapshot } from "@/contracts";
import { useHostBridge } from "@/services/bridgeContext";
import { PLUGIN_SURFACE_CSP, isTrustedSurfaceMessage, validatePluginSurfaceUrl } from "./pluginSurfacePolicy";

const props = withDefaults(defineProps<{
  src: string;
  surfaceToken: string;
  title: string;
  theme: PluginSurfaceThemeSnapshot;
  task?: PluginTaskViewSnapshot | null;
  tasks?: readonly PluginTaskViewSnapshot[];
  showTasks?: boolean;
  navigateBack?: (surfaceToken: string) => boolean;
  openFieldProtection?: (surfaceToken: string) => boolean;
  variant?: "panel" | "app" | "headless";
  /** v1 surface lifecycle: `background` frames are hidden, unfocusable and read-only on the Host side. */
  phase?: PluginSurfacePhase;
}>(), { showTasks: true, phase: "foreground" });
const emit = defineEmits<{
  /** The surface this token names closed (its own close message or the panel's close button); `token` identifies the frame, so a
   * replaced frame's late close never passes for the visible application's. Unmounting releases the token without emitting. */
  close: [token: string];
  /** The application signalled `readyToActivate` (SDK `api.lifecycle.readyToActivate()`): it finished preparing and
   * a click may promote this very element. The transport `ready` only means the SDK subscribed. `token` identifies the frame. */
  ready: [token: string];
  action: [payload: Readonly<Record<string, unknown>>];
  resolve: [decision: "approved" | "rejected", taskId: string];
  cancel: [taskId: string];
  finish: [status: string, summary: string];
}>();
const bridge = useHostBridge();
const frame = ref<HTMLIFrameElement | null>(null);
const activeToken = ref(props.surfaceToken);
const valid = computed(() => validatePluginSurfaceUrl(props.src));
const currentTasks = computed(() => props.tasks ?? (props.task ? [props.task] : []));
const notices = ref<string[]>([]);
const presentation = createPluginSurfaceUi({
  token: () => activeToken.value, theme: () => props.theme, tasks: () => currentTasks.value,
  cancel: taskId => emit("cancel", taskId), back: () => props.navigateBack?.(activeToken.value) ?? false,
  openFieldProtection: () => props.openFieldProtection?.(activeToken.value) ?? false,
  show: message => { notices.value = [...notices.value, message].slice(-3); },
  background: () => props.phase === "background",
});
const origin = computed(() => valid.value ? new URL(props.src).origin : "");

function postToSurface(event: "themeChanged" | "taskChanged" | "tasksChanged" | "uiResponse" | "lifecycleChanged", payload: unknown): void {
  if (!valid.value || !activeToken.value) return;
  // postMessage structured-clones its argument, and Vue reactive proxies
  // cannot be cloned; theme/task are JSON contract snapshots, so post a plain copy.
  frame.value?.contentWindow?.postMessage({
    contract: "vibetable.plugin-surface.v1",
    surfaceToken: activeToken.value,
    event,
    payload: JSON.parse(JSON.stringify(payload)),
  }, origin.value);
}

function sendTheme(): void {
  postToSurface("themeChanged", props.theme);
}

/** vibetable.plugin-surface-lifecycle.v1: the plugin re-checks its own version of the read-only state on each change. */
function sendLifecycle(): void {
  postToSurface("lifecycleChanged", { phase: props.phase });
}

function sendTask(): void {
  const safe = projectSurfaceTasks(currentTasks.value, activeToken.value);
  // Preserve the legacy event name, never the privileged parent-only DTO.
  for (const task of safe) postToSurface("taskChanged", task);
  postToSurface("tasksChanged", safe);
}

function onMessage(event: MessageEvent): void {
  if (!valid.value || !activeToken.value || !isTrustedSurfaceMessage(event, {
    expectedOrigin: origin.value,
    expectedSource: frame.value?.contentWindow ?? null,
    surfaceToken: activeToken.value,
  })) return;
  if (event.data.event === "action" && event.data.payload.service === "ui") {
    const response = presentation.handle(event.data.payload);
    if (response) postToSurface("uiResponse", response);
    return;
  }
  if (event.data.event === "readyToActivate") {
    // Application readiness is consumed here and never forwarded: the Host authority does not act on it, and
    // an older Host would reject it as an unknown surface event.
    emit("ready", activeToken.value);
    return;
  }
  // Transport `ready`: the SDK subscribed late or lazily; replay the current snapshots without treating the frame as reusable.
  if (event.data.event === "ready") { sendLifecycle(); sendTheme(); sendTask(); }
  if (event.data.event !== "close") bridge.notify("plugin.surface.event", event.data);
  if (event.data.event === "action") {
    const payload = typeof event.data.payload === "object" && event.data.payload !== null
      && !Array.isArray(event.data.payload)
      ? event.data.payload as Readonly<Record<string, unknown>>
      : {};
    emit("action", payload);
  }
  if (event.data.event === "close") {
    // Headless modules report their terminal status through the close
    // payload; visible surfaces close without one.
    const payload = typeof event.data.payload === "object" && event.data.payload !== null
      && !Array.isArray(event.data.payload)
      ? event.data.payload as Readonly<Record<string, unknown>>
      : {};
    close(payload);
  }
}

function close(payload: Readonly<Record<string, unknown>> = {}, notifyParent = true): void {
  if (!activeToken.value) return;
  const token = activeToken.value;
  activeToken.value = "";
  bridge.notify("plugin.surface.event", {
    contract: "vibetable.plugin-surface.v1",
    surfaceToken: token,
    event: "close",
    payload,
  });
  if (typeof payload.status === "string" && payload.status) {
    emit("finish", payload.status, typeof payload.summary === "string" ? payload.summary : "");
  }
  if (notifyParent) emit("close", token);
}

watch(() => props.theme, sendTheme, { deep: true });
watch(currentTasks, sendTask, { deep: true });
watch(() => props.phase, sendLifecycle);
watch(() => props.surfaceToken, (token) => {
  // Retire the former authorization without closing its replacement's view.
  close({}, false);
  activeToken.value = token;
  presentation.reset(); notices.value = [];
});
onMounted(() => window.addEventListener("message", onMessage));
onBeforeUnmount(() => {
  window.removeEventListener("message", onMessage);
  // Unmounting releases this token's Host session; it is not the user closing the application. The successor
  // view mounted in its place (a promoted pre-warmed surface, another application) stays open.
  close({}, false);
});
</script>

<template>
  <section
    class="surface-shell"
    :class="[variant === 'app' ? 'surface-shell--app' : null, phase === 'background' ? 'surface-shell--background' : null]"
    role="region"
    :aria-label="title"
    :aria-hidden="phase === 'background' ? 'true' : undefined"
  >
    <header v-if="variant !== 'app' && variant !== 'headless'">
      <div><strong>{{ title }}</strong></div>
      <button type="button" aria-label="关闭插件页面" @click="close()">×</button>
    </header>
    <iframe
      v-if="valid"
      :key="surfaceToken"
      ref="frame"
      :src="src"
      :name="surfaceToken"
      :data-plugin-print-surface="variant === 'app' ? surfaceToken : undefined"
      :title="title"
      :class="variant === 'headless' ? 'surface-frame--hidden' : null"
      :tabindex="phase === 'background' ? -1 : undefined"
      sandbox="allow-scripts allow-same-origin"
      referrerpolicy="no-referrer"
      :data-required-csp="PLUGIN_SURFACE_CSP"
      :data-surface-phase="phase"
      @load="() => { sendLifecycle(); sendTheme(); sendTask(); }"
    />
    <div v-else class="surface-error" role="alert">插件页面地址未通过不可变 origin 校验。</div>
    <aside v-if="notices.length" class="plugin-notices" aria-label="插件通知" aria-live="polite">
      <strong>插件 · {{ title }}</strong>
      <p v-for="(notice, index) in notices" :key="index">{{ notice }}</p>
    </aside>
    <PluginTaskList v-if="showTasks !== false" :tasks="currentTasks" :variant="variant"
      @cancel="taskId => emit('cancel', taskId)" @resolve="(decision, taskId) => emit('resolve', decision, taskId)" />
  </section>
</template>

<style scoped>
.surface-shell { display: flex; min-height: 360px; flex-direction: column; overflow: hidden; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); background: var(--vt-bg-sunken); }
.surface-shell--app { min-height: 0; height: 100%; border: 0; border-radius: 0; }
.surface-shell--app iframe { min-height: 0; }
.surface-frame--hidden { position: absolute; width: 1px; height: 1px; min-height: 0; overflow: hidden; clip-path: inset(50%); pointer-events: none; }
/* Pre-warmed frame: never visible, never focusable, never a pointer target. Scripts still load. */
.surface-shell--background { visibility: hidden; pointer-events: none; }
header { display: flex; flex: 0 0 42px; align-items: center; justify-content: space-between; padding: 0 10px 0 13px; border-bottom: 1px solid var(--vt-border); background: var(--vt-bg); }
header div { display: flex; align-items: baseline; gap: 10px; }
header strong { font-size: var(--vt-font-body); }
header button { width: 28px; height: 28px; color: var(--vt-fg-muted); border: 0; border-radius: var(--vt-radius-sm); background: transparent; cursor: pointer; }
header button:hover { color: var(--vt-fg); background: var(--vt-bg-sunken); }
iframe { flex: 1 1 auto; width: 100%; min-height: 320px; border: 0; background: var(--vt-bg); }
.plugin-notices { flex: 0 0 auto; max-height: 160px; overflow: auto; padding: 8px 12px; border-top: 1px solid var(--vt-border); white-space: pre-wrap; overflow-wrap: anywhere; }
.plugin-notices p { margin: 4px 0; }
.surface-error { display: grid; flex: 1 1 auto; place-items: center; color: var(--vt-color-danger); }

</style>
