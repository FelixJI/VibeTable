<script setup lang="ts">
import { computed } from "vue";
import { NButton, NIcon, NTooltip } from "naive-ui";
import {
  HelpCircle,
  Database,
  Files,
  Home,
  LayoutDashboard,
  LayoutTemplate,
  Blocks,
  Search,
  GitCompareArrows,
  Settings,
  Table2,
} from "@lucide/vue";
import type { AppView } from "@/stores/uiStore";
import { useUiStore } from "@/stores/uiStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import { useWorkspaceProtectionStore } from "@/stores/workspaceProtectionStore";
import { t } from "@/i18n";
import brandIconUrl from "@/assets/brand/vibetable.png";

const ui = useUiStore();
const workspaceSession = useWorkspaceSessionStore();
const protection = useWorkspaceProtectionStore();
const emit = defineEmits<{
  navigate: [view: AppView];
  openAdmin: [];
  openHelp: [];
}>();

const primary = computed(() => [
  { view: "home" as const, icon: Home, label: "nav.home" },
  { view: "tables" as const, icon: Table2, label: "nav.tables" },
  { view: "dashboard" as const, icon: LayoutDashboard, label: "nav.dashboard" },
  { view: "interfaces" as const, icon: LayoutTemplate, label: "nav.interfaces" },
  { view: "files" as const, icon: Files, label: "nav.files" },
  { view: "search" as const, icon: Search, label: "nav.search" },
  ...(workspaceSession.conflictEnabled ? [{
    view: "conflicts" as const,
    icon: GitCompareArrows,
    label: "workspaceV2.nav.conflicts",
  }] : []),
  { view: "plugins" as const, icon: Blocks, label: "nav.plugins" },
]);

function navigate(view: AppView) {
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
            :aria-current="ui.activeView === item.view ? 'page' : undefined"
            :data-testid="`nav-${item.view}`"
            @click="navigate(item.view)"
          >
            <template #icon><NIcon :size="19"><component :is="item.icon" /></NIcon></template>
            <span
              v-if="item.view === 'conflicts' && protection.pendingConflictCount"
              class="nav-badge"
              :aria-label="t('workspaceV2.conflict.count', { count: protection.pendingConflictCount })"
            >
              {{ Math.min(99, protection.pendingConflictCount) }}
            </span>
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
            :aria-label="t('nav.admin')"
            data-testid="nav-admin"
            @click="emit('openAdmin')"
          >
            <template #icon><NIcon :size="19"><Database /></NIcon></template>
          </NButton>
        </template>
        {{ t("nav.admin") }}
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
            :aria-current="ui.activeView === 'settings' ? 'page' : undefined"
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
  position: relative;
  width: 40px;
  height: 36px;
  color: var(--vt-fg-muted);
  border-radius: var(--vt-radius-md);
}
.nav-badge {
  position: absolute;
  top: 2px;
  right: 3px;
  display: grid;
  min-width: 15px;
  height: 15px;
  place-items: center;
  padding: 0 3px;
  color: white;
  border: 2px solid var(--vt-bg);
  border-radius: 999px;
  background: var(--vt-color-danger-500);
  font-size: 8px;
  font-weight: 700;
  line-height: 1;
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
