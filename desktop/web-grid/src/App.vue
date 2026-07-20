<script setup lang="ts">
/**
 * App.vue — root component (Task 19 final assembly).
 *
 * Wires the Naive UI theme provider (light/dark from useTheme + token-based
 * overrides from design-tokens/theme) and the NMessageProvider (for useMessage
 * inside services/components), and mounts WorkspaceView as the single child.
 *
 * The global keyboard handler is registered inside WorkspaceView (Task M5), not
 * here: the copy/paste/delete shortcuts need access to the Tabulator instance
 * (to read the active range) and to mutationService / pasteService / tableService
 * — both of which live in WorkspaceView's setup scope. App.vue keeps only the
 * theme provider so it stays a thin shell around WorkspaceView.
 */
import { computed, onBeforeUnmount, onMounted } from "vue";
import { NConfigProvider, darkTheme, NMessageProvider } from "naive-ui";
import type { GlobalTheme } from "naive-ui";
import { lightThemeOverrides, darkThemeOverrides } from "@/design-tokens/theme";
import { useTheme } from "@/composables/useTheme";
import WorkspaceView from "@/views/WorkspaceView.vue";
import StartupGate from "@/components/startup/StartupGate.vue";
import { useStartupStore } from "@/stores/startupStore";
import { useStartupService } from "@/services/startupService";
import type { StartupPhase } from "@/contracts";

const { isDark } = useTheme();
const startup = useStartupStore();
const startupService = useStartupService();
startupService.init();

const gatePhase = computed<Exclude<StartupPhase, "ready">>(() =>
  startup.phase === "ready" ? "starting" : startup.phase,
);

const naiveTheme = computed<GlobalTheme | null>(() =>
  isDark.value ? darkTheme : null,
);
const overrides = computed(() =>
  isDark.value ? darkThemeOverrides : lightThemeOverrides,
);

function preventUnhandledFileDrop(event: DragEvent): void {
  if (Array.from(event.dataTransfer?.types ?? []).includes("Files")) {
    // FileWorkspaceView handles valid imports before the event bubbles here.
    // Everywhere else, suppress Chromium's default file navigation.
    event.preventDefault();
  }
}

onMounted(() => {
  window.addEventListener("dragover", preventUnhandledFileDrop);
  window.addEventListener("drop", preventUnhandledFileDrop);
});
onBeforeUnmount(() => {
  window.removeEventListener("dragover", preventUnhandledFileDrop);
  window.removeEventListener("drop", preventUnhandledFileDrop);
});
</script>

<template>
  <NConfigProvider :theme="naiveTheme" :theme-overrides="overrides">
    <NMessageProvider>
      <WorkspaceView v-if="startup.phase === 'ready'" />
      <StartupGate
        v-else
        :phase="gatePhase"
        :stage="startup.stage"
        :detail="startup.detail"
        :email="startup.email"
        :remember-password="startup.rememberPassword"
        :auto-login="startup.autoLogin"
        :can-retry="startup.canRetry"
        :can-cancel="startup.canCancel"
        :logs="startup.logs"
        @first-run-submit="startupService.submitFirstRun"
        @login-submit="startupService.submitLogin"
        @retry="startupService.retry"
        @cancel="startupService.cancel"
      />
    </NMessageProvider>
  </NConfigProvider>
</template>
