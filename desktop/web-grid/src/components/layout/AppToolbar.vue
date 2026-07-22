<script setup lang="ts">
import { computed, h } from "vue";
import { NButton, NButtonGroup, NDropdown, NIcon, NTooltip } from "naive-ui";
import { ChevronDown, History, Keyboard, MoreHorizontal, Plus, RefreshCw, Table2, Trash2 } from "lucide-vue-next";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { collectionLabel } from "./collectionLabel";
import { t } from "@/i18n";

const workspace = useWorkspaceStore();
const props = withDefaults(defineProps<{
  pluginActions?: readonly {
    key: string;
    label: string;
    risk: "read" | "write" | "destructive";
    disabled: boolean;
  }[];
  historyScopeLabel?: string;
  historyDisabled?: boolean;
}>(), { pluginActions: () => [] });

const emit = defineEmits<{
  refresh: [];
  insertRow: [];
  openHelp: [];
  openHistory: [];
  openArchivedHistory: [];
  pluginAction: [key: string];
}>();

const displayNames = computed(() => workspace.displayNames);
const currentLabel = computed(() => {
  const item = workspace.collections.find((col) => col.collection === workspace.currentTable);
  return item ? collectionLabel(item, displayNames.value) : t("toolbar.noTable");
});
const moreOptions = computed(() => [
  {
    label: t("toolbar.refreshShortcut"),
    key: "refresh",
    icon: () => h(RefreshCw),
    disabled: !workspace.currentTable,
  },
  {
    label: t("toolbar.helpShortcut"),
    key: "help",
    icon: () => h(Keyboard),
  },
]);
const historyOptions = computed(() => [{
  label: t("toolbar.archivedHistory"),
  key: "archived",
  icon: () => h(Trash2),
  disabled: !workspace.currentTable,
}]);

function onMore(key: string) {
  if (key === "refresh") emit("refresh");
  if (key === "help") emit("openHelp");
}

function onHistoryMenu(key: string) {
  if (key === "archived") emit("openArchivedHistory");
}
</script>

<template>
  <div class="toolbar">
    <div class="table-heading">
      <NIcon :size="16"><Table2 /></NIcon>
      <strong data-testid="toolbar-table-title">{{ currentLabel }}</strong>
      <span v-if="workspace.currentTable && currentLabel !== workspace.currentTable">{{ workspace.currentTable }}</span>
    </div>
    <div class="toolbar-actions">
      <NTooltip v-for="action in props.pluginActions" :key="action.key" placement="bottom" :delay="450">
        <template #trigger>
          <NButton
            size="small"
            tertiary
            :type="action.risk === 'destructive' ? 'error' : action.risk === 'write' ? 'warning' : 'default'"
            :disabled="action.disabled"
            :data-testid="`plugin-toolbar-${action.key}`"
            @click="emit('pluginAction', action.key)"
          >{{ action.label }}</NButton>
        </template>
        插件动作 · {{ action.risk === 'read' ? '只读' : action.risk === 'write' ? '写入' : '危险' }}
      </NTooltip>
      <NTooltip placement="bottom" :delay="450">
        <template #trigger>
          <NButton
            size="small"
            quaternary
            :disabled="!workspace.currentTable"
            :aria-label="t('toolbar.insertRow')"
            data-testid="toolbar-insert-row"
            @click="emit('insertRow')"
          >
            <template #icon><NIcon><Plus /></NIcon></template>
          </NButton>
        </template>
        {{ t("toolbar.insertRow") }}
      </NTooltip>
      <NButtonGroup class="history-control">
        <NTooltip placement="bottom" :delay="450">
          <template #trigger>
            <NButton
              size="small"
              quaternary
              :disabled="!workspace.currentTable || props.historyDisabled"
              :aria-label="t('toolbar.historyCurrent')"
              data-testid="toolbar-history"
              @click="emit('openHistory')"
            >
              <template #icon><NIcon><History /></NIcon></template>
            </NButton>
          </template>
          {{ props.historyScopeLabel || t("toolbar.history") }}
        </NTooltip>
        <NDropdown :options="historyOptions" placement="bottom-end" @select="onHistoryMenu">
          <NButton
            size="small"
            quaternary
            class="history-menu-trigger"
            :disabled="!workspace.currentTable"
            :aria-label="t('toolbar.archivedHistory')"
            data-testid="toolbar-history-menu"
          >
            <template #icon><NIcon :size="13"><ChevronDown /></NIcon></template>
          </NButton>
        </NDropdown>
      </NButtonGroup>
      <NDropdown :options="moreOptions" placement="bottom-end" @select="onMore">
        <NTooltip placement="bottom" :delay="450">
          <template #trigger>
            <NButton
              size="small"
              quaternary
              :aria-label="t('toolbar.more')"
              data-testid="toolbar-more"
            >
              <template #icon><NIcon><MoreHorizontal /></NIcon></template>
            </NButton>
          </template>
          {{ t("toolbar.more") }}
        </NTooltip>
      </NDropdown>
    </div>
  </div>
</template>

<style scoped>
.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  min-height: 42px;
  padding: 0 var(--vt-space-3);
  border-bottom: 1px solid var(--vt-border);
  background: var(--vt-bg);
}
.table-heading { display: flex; align-items: center; min-width: 0; gap: 8px; }
.table-heading > :deep(.n-icon) { color: var(--vt-color-primary-500); }
.table-heading strong { overflow: hidden; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.table-heading span { overflow: hidden; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); text-overflow: ellipsis; white-space: nowrap; }
.toolbar-actions { display: flex; align-items: center; }
.history-control { margin-left: 2px; }
.history-menu-trigger { width: 22px; padding: 0 3px; }
</style>
