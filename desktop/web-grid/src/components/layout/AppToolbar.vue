<script setup lang="ts">
/**
 * AppToolbar — pure-presentation top toolbar.
 *
 * Reads `workspaceStore` (phase, currentTable), `tableStore` (rowCount,
 * datasetReady), and `uiStore` (themeMode) and EMITS user intent
 * (connect / refresh / openHelp). It does NOT call services — the theme
 * dropdown calls `ui.setThemeMode` directly because theme switching is a pure
 * UI concern (it just writes to the store; no host bridge involved).
 */
import { computed, h } from "vue";
import { NButton, NIcon, NText, NSpace, NDropdown } from "naive-ui";
import { Link, RefreshCw, Keyboard, Sun, Moon, Monitor } from "lucide-vue-next";
import type { Component } from "vue";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useTableStore } from "@/stores/tableStore";
import { useUiStore } from "@/stores/uiStore";
import type { ThemeMode } from "@/stores/uiStore";
import { t } from "@/i18n";

const workspace = useWorkspaceStore();
const table = useTableStore();
const ui = useUiStore();

const emit = defineEmits<{
  connect: [];
  refresh: [];
  openHelp: [];
}>();

const rowCountText = computed(() =>
  table.datasetReady ? t("toolbar.rowCount", { count: table.rowCount }) : "",
);

/**
 * Theme dropdown options. The icon is rendered as a VNodeChild via `h` to
 * satisfy Naive UI's `DropdownOption.icon` signature (`() => VNodeChild`).
 */
const themeOptions = [
  {
    label: t("toolbar.theme.system"),
    key: "system" as ThemeMode,
    icon: () => h(Monitor),
  },
  {
    label: t("toolbar.theme.light"),
    key: "light" as ThemeMode,
    icon: () => h(Sun),
  },
  {
    label: t("toolbar.theme.dark"),
    key: "dark" as ThemeMode,
    icon: () => h(Moon),
  },
];

const currentThemeIcon = computed<Component>(() => {
  if (ui.themeMode === "dark") return Moon;
  if (ui.themeMode === "light") return Sun;
  return Monitor;
});

function onTheme(key: string) {
  ui.setThemeMode(key as ThemeMode);
}
</script>

<template>
  <div class="toolbar">
    <NSpace align="center" :size="8">
      <NButton
        size="small"
        :disabled="workspace.phase === 'opened' || workspace.phase === 'opening'"
        data-testid="toolbar-connect"
        @click="emit('connect')"
      >
        <template #icon><NIcon :component="Link" /></template>
        {{ t("toolbar.connectDirectus") }}
      </NButton>
      <NButton
        size="small"
        :disabled="!workspace.currentTable"
        data-testid="toolbar-refresh"
        @click="emit('refresh')"
      >
        <template #icon><NIcon :component="RefreshCw" /></template>
        {{ t("toolbar.refresh") }}
      </NButton>
      <NText v-if="rowCountText" depth="3" data-testid="toolbar-row-count">{{ rowCountText }}</NText>
    </NSpace>
    <NSpace align="center" :size="8">
      <NButton
        size="small"
        quaternary
        :aria-label="t('toolbar.help')"
        data-testid="toolbar-open-help"
        @click="emit('openHelp')"
      >
        <template #icon><NIcon :component="Keyboard" /></template>
      </NButton>
      <NDropdown :options="themeOptions" placement="bottom-end" @select="onTheme">
        <NButton size="small" quaternary circle :aria-label="t('toolbar.theme')">
          <template #icon>
            <NIcon :component="currentThemeIcon" />
          </template>
        </NButton>
      </NDropdown>
    </NSpace>
  </div>
</template>

<style scoped>
.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: var(--vt-space-2) var(--vt-space-3);
  border-bottom: 1px solid var(--vt-border);
  background: var(--vt-bg);
}
</style>
