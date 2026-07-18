<script setup lang="ts">
/**
 * AppSidebar — pure-presentation table list.
 *
 * Reads collections and currentTable from `workspaceStore` and EMITS user
 * intent (select / newTable / openAdmin / requestDelete). It does NOT import or
 * call any service. `WorkspaceView` is the container that translates these
 * emits into service calls — this separation is the layered-architecture rule
 * (spec §2.2): components never call services directly.
 */
import { computed } from "vue";
import { NButton, NIcon, NList, NListItem } from "naive-ui";
import { Plus, Settings, Trash2 } from "lucide-vue-next";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { t } from "@/i18n";

const workspace = useWorkspaceStore();

const emit = defineEmits<{
  select: [name: string];
  newTable: [];
  openAdmin: [];
  requestDelete: [name: string];
}>();

const collections = computed(() => workspace.collections);
</script>

<template>
  <aside class="sidebar">
    <div class="sidebar-head">
      <span class="sidebar-title">{{ t("sidebar.tables") }}</span>
      <NButton
        size="small"
        quaternary
        :aria-label="t('sidebar.newTable')"
        data-testid="sidebar-new-table"
        @click="emit('newTable')"
      >
        <template #icon><NIcon :component="Plus" /></template>
      </NButton>
      <NButton
        size="small"
        quaternary
        :aria-label="t('sidebar.admin')"
        data-testid="sidebar-open-admin"
        @click="emit('openAdmin')"
      >
        <template #icon><NIcon :component="Settings" /></template>
      </NButton>
    </div>
    <NList hoverable clickable class="table-list" data-testid="sidebar-table-list">
      <NListItem
        v-for="col in collections"
        :key="col.collection"
        :class="{ 'table-item--active': col.collection === workspace.currentTable }"
        @click="emit('select', col.collection)"
      >
        <div class="table-item">
          <span class="table-name" data-testid="sidebar-table-name">{{ col.collection }}</span>
          <NButton
            size="tiny"
            quaternary
            :aria-label="t('sidebar.delete')"
            data-testid="sidebar-request-delete"
            @click.stop="emit('requestDelete', col.collection)"
          >
            <template #icon><NIcon :component="Trash2" :size="14" /></template>
          </NButton>
        </div>
      </NListItem>
    </NList>
  </aside>
</template>

<style scoped>
.sidebar {
  flex: 0 0 220px;
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
  padding: var(--vt-space-2) var(--vt-space-2);
  border-bottom: 1px solid var(--vt-border);
}
.sidebar-title {
  font-weight: 600;
  flex: 1;
  color: var(--vt-fg);
}
.table-list {
  flex: 1 1 auto;
  overflow-y: auto;
}
.table-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  width: 100%;
}
.table-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
:deep(.table-item--active) {
  background: var(--vt-color-primary-50);
}
:root.dark :deep(.table-item--active) {
  background: rgba(91, 139, 255, 0.15);
}
</style>
