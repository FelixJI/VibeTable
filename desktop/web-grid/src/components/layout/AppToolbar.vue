<script setup lang="ts">
import { computed, h } from "vue";
import { NButton, NButtonGroup, NDropdown, NIcon, NTooltip } from "naive-ui";
import { BookOpenText, ChevronDown, Download, History, Keyboard, MoreHorizontal, Network, Plus, RefreshCw, Table2, Trash2, Upload } from "lucide-vue-next";
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
  insertRowDisabled?: boolean;
  dataIoBusy?: boolean;
  dataIoLocked?: boolean;
}>(), { pluginActions: () => [] });

const emit = defineEmits<{
  selectTable: [name: string];
  refresh: [];
  insertRow: [];
  openHelp: [];
  openHistory: [];
  openArchivedHistory: [];
  openFieldManager: [];
  openContent: [];
  importData: [];
  exportData: [];
  cancelDataTask: [];
  pluginAction: [key: string];
}>();

const displayNames = computed(() => workspace.displayNames);
const currentLabel = computed(() => {
  const item = workspace.collections.find((col) => col.collection === workspace.currentTable);
  return item ? collectionLabel(item, displayNames.value) : t("toolbar.noTable");
});
const tableOptions = computed(() => workspace.collections.map((collection) => ({
  key: collection.collection,
  label: collectionLabel(collection, displayNames.value),
  icon: () => h(Table2),
})));
const moreOptions = computed(() => [
  {
    label: "取消数据任务",
    key: "cancel-data-task",
    disabled: !props.dataIoBusy,
  },
  {
    label: "导入数据",
    key: "import",
    icon: () => h(Upload),
    disabled: !workspace.currentTable || props.dataIoBusy || props.dataIoLocked,
  },
  {
    label: "导出数据",
    key: "export",
    icon: () => h(Download),
    disabled: !workspace.currentTable || props.dataIoBusy || props.dataIoLocked,
  },
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
  if (key === "import") emit("importData");
  if (key === "export") emit("exportData");
  if (key === "cancel-data-task") emit("cancelDataTask");
}

function onHistoryMenu(key: string) {
  if (key === "archived") emit("openArchivedHistory");
}

function onSelectTable(key: string) {
  if (key !== workspace.currentTable) emit("selectTable", key);
}
</script>

<template>
  <div class="toolbar">
    <div class="table-heading desktop-table-heading">
      <NIcon :size="16"><Table2 /></NIcon>
      <strong data-testid="toolbar-table-title">{{ currentLabel }}</strong>
      <span v-if="workspace.currentTable && currentLabel !== workspace.currentTable">{{ workspace.currentTable }}</span>
    </div>
    <div class="compact-table-switcher">
      <NDropdown
        :options="tableOptions"
        placement="bottom-start"
        @select="onSelectTable"
      >
        <NButton
          size="small"
          secondary
          class="compact-table-trigger"
          :disabled="workspace.collections.length === 0"
          :aria-label="t('toolbar.switchTable', { name: currentLabel })"
          aria-haspopup="menu"
          data-testid="compact-table-switcher"
        >
          <template #icon><NIcon :size="15"><Table2 /></NIcon></template>
          <span class="compact-table-label">{{ currentLabel }}</span>
          <NIcon class="compact-table-chevron" :size="13"><ChevronDown /></NIcon>
        </NButton>
      </NDropdown>
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
            aria-label="打开内容记录"
            data-testid="toolbar-content-record"
            @click="emit('openContent')"
          >
            <template #icon><NIcon><BookOpenText /></NIcon></template>
          </NButton>
        </template>
        内容记录
      </NTooltip>
      <NTooltip placement="bottom" :delay="450">
        <template #trigger>
          <NButton
            size="small"
            quaternary
            :disabled="!workspace.currentTable"
            aria-label="关系与 Lookup 字段"
            data-testid="toolbar-field-manager"
            @click="emit('openFieldManager')"
          >
            <template #icon><NIcon><Network /></NIcon></template>
          </NButton>
        </template>
        关系与 Lookup 字段
      </NTooltip>
      <NTooltip placement="bottom" :delay="450">
        <template #trigger>
          <NButton
            size="small"
            quaternary
            :disabled="!workspace.currentTable || props.insertRowDisabled"
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
.compact-table-switcher {
  display: none;
  flex: 1 1 180px;
  min-width: 0;
}
.compact-table-trigger {
  min-width: 0;
  max-width: min(52vw, 260px);
  border-color: var(--vt-border);
  background: var(--vt-bg-subtle);
}
.compact-table-trigger :deep(.n-button__content) {
  min-width: 0;
}
.compact-table-label {
  min-width: 0;
  overflow: hidden;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.compact-table-chevron {
  flex: 0 0 auto;
  margin-left: 4px;
  color: var(--vt-fg-muted);
}
.toolbar-actions { display: flex; align-items: center; }
.history-control { margin-left: 2px; }
.history-menu-trigger { width: 22px; padding: 0 3px; }
@media (max-width: 899px) {
  .toolbar {
    flex-wrap: wrap;
    gap: 2px 8px;
    padding-block: 4px;
  }
  .table-heading {
    flex: 1 1 180px;
  }
  .desktop-table-heading {
    display: none;
  }
  .compact-table-switcher {
    display: flex;
  }
  .toolbar-actions {
    flex: 0 1 auto;
    flex-wrap: wrap;
    justify-content: flex-end;
    margin-left: auto;
  }
}
</style>
