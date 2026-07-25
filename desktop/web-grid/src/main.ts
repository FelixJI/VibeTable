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
 *   5. `bridge.start()` + `bridge.notify("app.ready", {})` — tell the .NET
 *      host the renderer can receive startup state. WorkspaceView sends a
 *      second app.ready after its business subscriptions mount so cached
 *      database state is replayed without a race.
 *
 * Calling `useHostBridge()` is fine anywhere: it's just a module function that
 * returns the singleton. We invoke it after mount so subscriptions from step 4
 * are in place before the host can react to `app.ready`.
 */
import { createApp } from "vue";
import { createPinia } from "pinia";
import "./design-tokens/tokens.css";
import "./components/calendar/work-calendar.css";
import { initLocale } from "@/i18n";
import { useHostBridge } from "@/services/bridgeContext";
import App from "./App.vue";

initLocale();

const app = createApp(App);
app.use(createPinia());
app.mount("#app");

// Start the host bridge and notify the .NET host that the renderer is ready.
// This happens AFTER mount so the startup-state subscription is registered.
const bridge = useHostBridge();
bridge.start();
bridge.notify("app.ready", {});
