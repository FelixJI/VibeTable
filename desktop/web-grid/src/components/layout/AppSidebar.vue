<script setup lang="ts">
import { computed, ref } from "vue";
import { NButton, NCheckbox, NIcon, NInput, NModal, NTooltip } from "naive-ui";
import type { TableCopyScope } from "@/contracts";
import { Copy, Plus, Search, Table2, Trash2 } from "@lucide/vue";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useUiStore } from "@/stores/uiStore";
import { collectionLabel } from "./collectionLabel";
import { localModifiedLabel } from "./tableDescription";
import { t } from "@/i18n";

const workspace = useWorkspaceStore();
const ui = useUiStore();
const query = ref("");

const emit = defineEmits<{
  select: [name: string];
  newTable: [];
  requestDelete: [name: string];
  duplicate: [name: string, displayName: string, scope: TableCopyScope];
}>();

const displayNames = computed(() => workspace.displayNames);

/** Existing display names for the duplicate-name guard. */
const takenNames = computed(() =>
  new Set(Object.values(workspace.displayNames).map((name) => name.trim())));

function uniqueCopyName(source: string): string {
  const base = `${source} 副本`;
  if (!takenNames.value.has(base)) return base;
  let index = 2;
  while (takenNames.value.has(`${base} ${index}`)) index += 1;
  return `${base} ${index}`;
}

const duplicateTitle = "复制表";

const duplicateDialog = ref(false);
const duplicateTarget = ref<string | null>(null);
const duplicateInput = ref("");
const copyRecords = ref(true);
const copyViews = ref(true);
const duplicateNameConflict = computed(() => {
  const name = duplicateInput.value.trim();
  return !!name && takenNames.value.has(name);
});

function openDuplicate(name: string): void {
  copyRecords.value = true;
  copyViews.value = true;
  duplicateTarget.value = name;
  const known = workspace.collections.find((item) => item.collection === name);
  const sourceLabel = known
    ? collectionLabel(known, displayNames.value)
    : displayNames.value[name]?.trim() || name;
  duplicateInput.value = uniqueCopyName(sourceLabel);
  duplicateDialog.value = true;
}

function confirmDuplicate(): void {
  const name = duplicateInput.value.trim();
  const target = duplicateTarget.value;
  if (!target || !name || duplicateNameConflict.value) return;
  duplicateDialog.value = false;
  duplicateTarget.value = null;
  emit("duplicate", target, name, {copyRecords: copyRecords.value, copyViews: copyViews.value});
}

function describe(col: { collection: string }): string | null {
  return localModifiedLabel(ui.tableModified[col.collection]);
}

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
        <button
          type="button"
          class="table-select"
          :aria-current="col.collection === workspace.currentTable ? 'page' : undefined"
          @click="emit('select', col.collection)"
        >
          <span class="table-icon"><NIcon :size="15"><Table2 /></NIcon></span>
          <span class="table-copy">
            <span class="table-name" data-testid="sidebar-table-name">{{ collectionLabel(col, displayNames) }}</span>
            <!-- 表说明：备注或本地最后修改时间；物理 id 不再作为说明展示。 -->
            <small
              v-if="describe(col)"
              data-testid="sidebar-table-description"
            >{{ describe(col) }}</small>
          </span>
        </button>
        <NTooltip placement="right" :delay="450">
          <template #trigger>
          <NButton
            size="tiny"
            quaternary
            class="delete-button"
            :aria-label="duplicateTitle"
            data-testid="sidebar-request-duplicate"
            @click="openDuplicate(col.collection)"
          >
            <template #icon><NIcon :size="14"><Copy /></NIcon></template>
          </NButton>
          </template>
          {{ duplicateTitle }}
        </NTooltip>
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
    <NModal
      :show="duplicateDialog"
      preset="card"
      class="table-duplicate-dialog"
      :title="duplicateTitle"
      @update:show="show => { if (!show) duplicateDialog = false }"
    >
      <div class="duplicate-dialog-fields">
        <small>结构始终复制，可选择是否包含记录和视图。附件与自动编号仅支持不复制记录；查找引用、跨表双向关联、集合公式暂不支持，遇到不支持的内容会停止且不创建副本。</small>
        <NCheckbox :checked="true" disabled data-testid="table-duplicate-structure">结构（必选）</NCheckbox>
        <NCheckbox v-model:checked="copyRecords" data-testid="table-duplicate-records">复制记录</NCheckbox>
        <NCheckbox v-model:checked="copyViews" data-testid="table-duplicate-views">复制视图</NCheckbox>
        <NInput
          v-model:value="duplicateInput"
          autofocus
          placeholder="副本表名"
          :input-props="{ 'aria-label': '副本表名' }"
          data-testid="table-duplicate-name"
          @keyup.enter="confirmDuplicate"
        />
        <small v-if="duplicateNameConflict" class="duplicate-name-conflict" data-testid="table-duplicate-conflict">
          已存在同名数据表
        </small>
      </div>
      <template #footer>
        <div class="dialog-actions">
          <NButton @click="duplicateDialog = false">{{ t("common.cancel") }}</NButton>
          <NButton
            type="primary"
            :disabled="!duplicateInput.trim() || duplicateNameConflict"
            data-testid="table-duplicate-confirm"
            @click="confirmDuplicate"
          >{{ t("common.confirm") }}</NButton>
        </div>
      </template>
    </NModal>
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
  grid-template-columns: minmax(0, 1fr) 26px 26px;
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
.dialog-actions { display: flex; justify-content: flex-end; gap: 8px; }
.duplicate-dialog-fields { display: grid; gap: 10px; }
.duplicate-name-conflict { color: var(--vt-color-danger); }
:global(.table-duplicate-dialog) { width: min(420px, calc(100vw - 28px)); }
</style>
