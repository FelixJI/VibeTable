/**
 * main.ts — application boot (Task 19 final assembly).
 *
 * Boot order is load-bearing:
 *
 *   1. `initLocale()`  — reads localStorage; no Vue/Pinia deps.
 *   2. `createApp(App)` — construct the Vue instance.
 *   3. `app.use(createPinia())` — install Pinia BEFORE mount so every store
 *      used during component setup/onMounted has an active instance.
 *   4. `app.mount("#app")` — synchronously mounts the tree. App subscribes to
 *      host startup state before the bridge handshake; WorkspaceView remains
 *      gated until the local runtime is ready.
 *   5. `bridge.start()` + `app.ready {phase: "shell"}` — tell the .NET
 *      host the renderer can receive startup state. WorkspaceView sends a
 *      second app.ready {phase: "business"} after its business subscriptions
 *      mount so database state can be replayed and realtime can start safely.
 *
 * Calling `useHostBridge()` is fine anywhere: it's just a module function that
 * returns the singleton. We invoke it after mount so subscriptions from step 4
 * are in place before the host can react to `app.ready`.
 */
import { createApp, watch } from "vue";
import { createPinia } from "pinia";
import "./design-tokens/tokens.css";
import "./components/calendar/work-calendar.css";
import { initLocale } from "@/i18n";
import { useHostBridge } from "@/services/bridgeContext";
import { createWorkspaceV2HostAdapter } from "@/services/workspaceV2HostAdapter";
import { setWorkspaceV2UiPort } from "@/services/workspaceV2UiPort";
import { installWorkspaceWireE2EPort } from "@/services/workspaceWireE2EPort";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import { useStartupStore } from "@/stores/startupStore";
import { useThemeProjectionService } from "@/services/themeProjectionService";
import App from "./App.vue";

initLocale();

const app = createApp(App);
const pinia = createPinia();
app.use(pinia);
const bridge = useHostBridge();
const workspaceV2Adapter = createWorkspaceV2HostAdapter(bridge);
setWorkspaceV2UiPort(workspaceV2Adapter.port);
const disposeE2EWirePort = new URL(window.location.href).searchParams
  .get("vibetable-e2e") === "1"
  ? installWorkspaceWireE2EPort(window, () => {
      const session = useWorkspaceSessionStore(pinia);
      return session.activeWorkspaceId && session.sessionEpoch > 0
        ? {
            workspaceId: session.activeWorkspaceId,
            sessionEpoch: session.sessionEpoch,
          }
        : null;
    }, workspaceV2Adapter.port)
  : () => undefined;
app.mount("#app");

// Theme projection: subscribe before the app.ready handshake so the Host's
// initial appearance snapshot (posted right after app.ready) is never
// missed. Requests wait until the startup handshake is ready — the Host
// rejects business requests that arrive before app.ready.
const themeProjection = useThemeProjectionService();
themeProjection.attach();

// Start the host bridge and notify the .NET host that the renderer is ready.
// This happens AFTER mount so the startup-state subscription is registered.
bridge.start();
bridge.notify("app.ready", { phase: "shell" });

const startupForTheme = useStartupStore();
const stopThemeReadyWatch = watch(
  () => startupForTheme.phase === "ready",
  (ready) => {
    if (ready) void themeProjection.initialize();
  },
  { immediate: true },
);

if (import.meta.hot) {
  import.meta.hot.dispose(() => {
    // Recycle the theme subscription + watcher so an HMR remount cannot
    // stack duplicate handlers on the same bridge.
    stopThemeReadyWatch();
    themeProjection.dispose();
    disposeE2EWirePort();
    workspaceV2Adapter.dispose();
    setWorkspaceV2UiPort(null);
  });
}
