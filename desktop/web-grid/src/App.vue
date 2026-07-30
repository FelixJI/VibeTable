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
import {
  NConfigProvider,
  darkTheme,
  dateEnUS,
  dateZhCN,
  enUS,
  NMessageProvider,
  zhCN,
} from "naive-ui";
import type { GlobalTheme } from "naive-ui";
import { lightThemeOverrides, darkThemeOverrides } from "@/design-tokens/theme";
import { useTheme } from "@/composables/useTheme";
import { useUiStore } from "@/stores/uiStore";
import WorkspaceView from "@/views/WorkspaceView.vue";
import StartupGate from "@/components/startup/StartupGate.vue";
import { useStartupStore } from "@/stores/startupStore";
import { useStartupService } from "@/services/startupService";
import type { StartupPhase } from "@/contracts";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";

const { isDark } = useTheme();
const ui = useUiStore();
const startup = useStartupStore();
const startupService = useStartupService();
const workspaceSession = useWorkspaceSessionStore();
startupService.init();
const shellReady = computed(() =>
  startup.phase === "ready" || workspaceSession.enabled);

const gatePhase = computed<Exclude<StartupPhase, "ready">>(() =>
  startup.phase === "ready" ? "starting" : startup.phase,
);

const naiveTheme = computed<GlobalTheme | null>(() =>
  isDark.value ? darkTheme : null,
);
const overrides = computed(() =>
  isDark.value ? darkThemeOverrides : lightThemeOverrides,
);
const naiveLocale = computed(() => ui.locale === "zh-CN" ? zhCN : enUS);
const naiveDateLocale = computed(() => ui.locale === "zh-CN" ? dateZhCN : dateEnUS);

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
  <NConfigProvider
    :theme="naiveTheme"
    :theme-overrides="overrides"
    :locale="naiveLocale"
    :date-locale="naiveDateLocale"
  >
    <NMessageProvider placement="bottom" :max="1">
      <WorkspaceView v-if="shellReady" />
      <StartupGate
        v-else
        :phase="gatePhase"
        :stage="startup.stage"
        :detail="startup.detail"
        :can-retry="startup.canRetry"
        :can-cancel="startup.canCancel"
        :logs="startup.logs"
        @retry="startupService.retry"
        @cancel="startupService.cancel"
      />
    </NMessageProvider>
  </NConfigProvider>
</template>
