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
import { computed } from "vue";
import { NConfigProvider, darkTheme, NMessageProvider } from "naive-ui";
import type { GlobalTheme } from "naive-ui";
import { lightThemeOverrides, darkThemeOverrides } from "@/design-tokens/theme";
import { useTheme } from "@/composables/useTheme";
import WorkspaceView from "@/views/WorkspaceView.vue";

const { isDark } = useTheme();

const naiveTheme = computed<GlobalTheme | null>(() =>
  isDark.value ? darkTheme : null,
);
const overrides = computed(() =>
  isDark.value ? darkThemeOverrides : lightThemeOverrides,
);
</script>

<template>
  <NConfigProvider :theme="naiveTheme" :theme-overrides="overrides">
    <NMessageProvider>
      <WorkspaceView />
    </NMessageProvider>
  </NConfigProvider>
</template>
