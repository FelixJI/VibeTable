<script setup lang="ts">
import { computed, ref } from "vue";
import { NButton, NIcon, NInput, NTooltip } from "naive-ui";
import { Plus, Search, Table2, Trash2 } from "lucide-vue-next";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { collectionLabel } from "./collectionLabel";
import { t } from "@/i18n";

const workspace = useWorkspaceStore();
const query = ref("");

const emit = defineEmits<{
  select: [name: string];
  newTable: [];
  requestDelete: [name: string];
}>();

const displayNames = computed(() => workspace.displayNames);
const collections = computed(() => {
  const needle = query.value.trim().toLocaleLowerCase();
  if (!needle) return workspace.collections;
  return workspace.collections.filter((item) =>
    `${collectionLabel(item, displayNames.value)} ${item.collection}`
      .toLocaleLowerCase()
      .includes(needle),
  );
});
</script>

<template>
  <aside class="sidebar">
    <div class="sidebar-head">
      <div>
        <span class="sidebar-title">{{ t("sidebar.tables") }}</span>
        <small>{{ workspace.collections.length }}</small>
      </div>
      <NTooltip placement="bottom" :delay="450">
        <template #trigger>
          <NButton
            size="small"
            quaternary
            :aria-label="t('sidebar.newTable')"
            data-testid="sidebar-new-table"
            @click="emit('newTable')"
          >
            <template #icon><NIcon><Plus /></NIcon></template>
          </NButton>
        </template>
        {{ t("sidebar.newTable") }}（Ctrl+N）
      </NTooltip>
    </div>
    <div class="sidebar-search">
      <NInput v-model:value="query" size="small" clearable :input-props="{ 'aria-label': t('sidebar.search') }" :placeholder="t('sidebar.search')">
        <template #prefix><NIcon :size="14"><Search /></NIcon></template>
      </NInput>
    </div>
    <div class="table-list" data-testid="sidebar-table-list">
      <div
        v-for="col in collections"
        :key="col.collection"
        class="table-row"
        :class="{ 'table-item--active': col.collection === workspace.currentTable }"
      >
        <button type="button" class="table-select" @click="emit('select', col.collection)">
          <span class="table-icon"><NIcon :size="15"><Table2 /></NIcon></span>
          <span class="table-copy">
            <span class="table-name" data-testid="sidebar-table-name">{{ collectionLabel(col, displayNames) }}</span>
            <small v-if="collectionLabel(col, displayNames) !== col.collection">{{ col.collection }}</small>
          </span>
        </button>
        <NTooltip placement="right" :delay="450">
          <template #trigger>
          <NButton
            size="tiny"
            quaternary
            class="delete-button"
            :aria-label="t('sidebar.delete')"
            data-testid="sidebar-request-delete"
            @click="emit('requestDelete', col.collection)"
          >
            <template #icon><NIcon :size="14"><Trash2 /></NIcon></template>
          </NButton>
          </template>
          {{ t("sidebar.delete") }}
        </NTooltip>
      </div>
      <div v-if="collections.length === 0" class="sidebar-empty">
        {{ query ? t("sidebar.search.empty") : t("sidebar.empty") }}
      </div>
    </div>
  </aside>
</template>

<style scoped>
.sidebar {
  flex: 0 0 232px;
  display: flex;
  flex-direction: column;
  border-right: 1px solid var(--vt-border);
  background: var(--vt-bg-subtle);
  overflow: hidden;
}
.sidebar-head {
  display: flex;
  align-items: center;
  gap: var(--vt-space-1);
  height: 46px;
  padding: 6px 10px 4px 14px;
}
.sidebar-head > div { display: flex; align-items: baseline; gap: 7px; }
.sidebar-head small { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.sidebar-title {
  font-weight: 600;
  flex: 1;
  color: var(--vt-fg);
}
.sidebar-search { padding: 6px 10px 10px; }
.table-list {
  flex: 1 1 auto;
  overflow-y: auto;
  padding: 0 6px 10px;
}
.table-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 26px;
  align-items: center;
  width: 100%;
  min-height: 36px;
  padding: 3px 4px 3px 0;
  color: var(--vt-fg-secondary);
  border-radius: var(--vt-radius-md);
  background: transparent;
}
.table-row:hover { color: var(--vt-fg); background: var(--vt-bg-sunken); }
.table-select { display: grid; grid-template-columns: 24px minmax(0, 1fr); align-items: center; min-width: 0; padding: 0 0 0 7px; color: inherit; text-align: left; border: 0; background: transparent; cursor: pointer; }
.table-icon { color: var(--vt-fg-muted); }
.table-copy { display: flex; flex-direction: column; min-width: 0; }
.table-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.table-copy small { overflow: hidden; color: var(--vt-fg-muted); font-size: 10px; text-overflow: ellipsis; white-space: nowrap; }
.table-item--active {
  color: var(--vt-color-primary-500);
  background: var(--vt-color-primary-50);
}
:root.dark .table-item--active {
  background: rgba(91, 139, 255, 0.15);
}
.delete-button { opacity: 0; }
.table-row:hover .delete-button, .table-row:focus-within .delete-button { opacity: 1; }
.sidebar-empty { padding: 20px 10px; color: var(--vt-fg-muted); text-align: center; font-size: var(--vt-font-caption); }
</style>
