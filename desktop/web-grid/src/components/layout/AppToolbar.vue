<script setup lang="ts">
import { computed, h } from "vue";
import { NButton, NDropdown, NIcon, NTooltip } from "naive-ui";
import { Keyboard, MoreHorizontal, RefreshCw, Table2 } from "lucide-vue-next";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { collectionLabel } from "./collectionLabel";
import { t } from "@/i18n";

const workspace = useWorkspaceStore();

const emit = defineEmits<{
  refresh: [];
  openHelp: [];
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

function onMore(key: string) {
  if (key === "refresh") emit("refresh");
  if (key === "help") emit("openHelp");
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
</style>
