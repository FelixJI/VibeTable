<script setup lang="ts">
/**
 * App.vue — root component (Task 19 final assembly).
 *
 * Wires the Naive UI theme provider (light/dark from useTheme + token-based
 * overrides from design-tokens/theme) and the NMessageProvider (for useMessage
 * inside services/components), and mounts WorkspaceView as the single child.
 *
 * The global keyboard handler is registered here: it fires shortcuts into the
 * keyboardStore and routes `?` to the shortcuts page via uiStore.openShortcuts
 * — the primary discovery surface for shortcuts. We deliberately pass NO
 * `tabulator` ref: the Tabulator instance lives inside GridHost and arrow/Tab/
 * Enter are already handled by Tabulator's range API, so useKeyboard does not
 * need it (see useKeyboard's UseKeyboardOptions.tabulator comment).
 */
import { computed } from "vue";
import { NConfigProvider, darkTheme, NMessageProvider } from "naive-ui";
import type { GlobalTheme } from "naive-ui";
import { lightThemeOverrides, darkThemeOverrides } from "@/design-tokens/theme";
import { useTheme } from "@/composables/useTheme";
import { useKeyboard } from "@/composables/useKeyboard";
import { useUiStore } from "@/stores/uiStore";
import WorkspaceView from "@/views/WorkspaceView.vue";

const { isDark } = useTheme();
const ui = useUiStore();

const naiveTheme = computed<GlobalTheme | null>(() =>
  isDark.value ? darkTheme : null,
);
const overrides = computed(() =>
  isDark.value ? darkThemeOverrides : lightThemeOverrides,
);

// Global keyboard shortcuts. `?` opens the shortcuts page so they're
// discoverable. Other callbacks (onRefresh/onNewTable/onCopy/onPaste) are left
// for later: those intents are also reachable via the toolbar / context UI.
useKeyboard({
  onHelp: () => ui.openShortcuts(),
});
</script>

<template>
  <NConfigProvider :theme="naiveTheme" :theme-overrides="overrides">
    <NMessageProvider>
      <WorkspaceView />
    </NMessageProvider>
  </NConfigProvider>
</template>
