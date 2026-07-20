<script setup lang="ts">
import { NButton, NIcon, NTooltip } from "naive-ui";
import {
  HelpCircle,
  Database,
  Files,
  Home,
  Blocks,
  Settings,
  Table2,
} from "lucide-vue-next";
import type { AppView } from "@/stores/uiStore";
import { useUiStore } from "@/stores/uiStore";
import { t } from "@/i18n";
import brandIconUrl from "@/assets/brand/vibetable.png";

const ui = useUiStore();
const emit = defineEmits<{
  navigate: [view: AppView];
  openAdmin: [];
  openHelp: [];
}>();

const primary = [
  { view: "home" as const, icon: Home, label: "nav.home" },
  { view: "tables" as const, icon: Table2, label: "nav.tables" },
  { view: "files" as const, icon: Files, label: "nav.files" },
  { view: "plugins" as const, icon: Blocks, label: "nav.plugins" },
];

function navigate(view: AppView) {
  ui.navigate(view);
  emit("navigate", view);
}
</script>

<template>
  <nav class="app-navigation" :aria-label="t('nav.application')">
    <img class="brand-mark" :src="brandIconUrl" alt="" aria-hidden="true" />

    <div class="nav-group nav-group--primary">
      <NTooltip v-for="item in primary" :key="item.view" placement="right" :delay="450">
        <template #trigger>
          <NButton
            quaternary
            class="nav-button"
            :class="{ 'nav-button--active': ui.activeView === item.view }"
            :aria-label="t(item.label)"
            :data-testid="`nav-${item.view}`"
            @click="navigate(item.view)"
          >
            <template #icon><NIcon :size="19"><component :is="item.icon" /></NIcon></template>
          </NButton>
        </template>
        {{ t(item.label) }}
      </NTooltip>
    </div>

    <div class="nav-group nav-group--secondary">
      <NTooltip placement="right" :delay="450">
        <template #trigger>
          <NButton
            quaternary
            class="nav-button"
            :aria-label="t('nav.directus')"
            data-testid="nav-directus"
            @click="emit('openAdmin')"
          >
            <template #icon><NIcon :size="19"><Database /></NIcon></template>
          </NButton>
        </template>
        {{ t("nav.directus") }}
      </NTooltip>
      <NTooltip placement="right" :delay="450">
        <template #trigger>
          <NButton
            quaternary
            class="nav-button"
            :aria-label="t('nav.help')"
            data-testid="nav-help"
            @click="emit('openHelp')"
          >
            <template #icon><NIcon :size="19"><HelpCircle /></NIcon></template>
          </NButton>
        </template>
        {{ t("nav.help") }}
      </NTooltip>
      <NTooltip placement="right" :delay="450">
        <template #trigger>
          <NButton
            quaternary
            class="nav-button"
            :class="{ 'nav-button--active': ui.activeView === 'settings' }"
            :aria-label="t('nav.settings')"
            data-testid="nav-settings"
            @click="navigate('settings')"
          >
            <template #icon><NIcon :size="19"><Settings /></NIcon></template>
          </NButton>
        </template>
        {{ t("nav.settings") }}
      </NTooltip>
    </div>
  </nav>
</template>

<style scoped>
.app-navigation {
  position: relative;
  z-index: 30;
  display: flex;
  flex: 0 0 52px;
  flex-direction: column;
  align-items: center;
  height: 100%;
  padding: 10px 6px 8px;
  border-right: 1px solid var(--vt-border);
  background: var(--vt-bg);
}
.brand-mark {
  width: 24px;
  height: 24px;
  margin: 1px 0 14px;
  border-radius: 7px;
  object-fit: cover;
}
.nav-group {
  display: flex;
  flex-direction: column;
  gap: 4px;
  width: 100%;
}
.nav-group--secondary {
  margin-top: auto;
}
.nav-button {
  width: 40px;
  height: 36px;
  color: var(--vt-fg-muted);
  border-radius: var(--vt-radius-md);
}
.nav-button:hover {
  color: var(--vt-fg);
  background: var(--vt-bg-sunken);
}
.nav-button--active {
  color: var(--vt-color-primary-500);
  background: var(--vt-color-primary-50);
}
:root.dark .nav-button--active {
  background: rgba(91, 139, 255, 0.14);
}
</style>
