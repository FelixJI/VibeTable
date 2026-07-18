<script setup lang="ts">
import { computed } from "vue";
import { t } from "@/i18n";
import { useTableStore } from "@/stores/tableStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";

const table = useTableStore();
const workspace = useWorkspaceStore();

const text = computed(() => {
  if (table.error) return t("status.tableLoadFailed", { message: table.error });
  if (table.loading && workspace.currentTable) {
    return t("status.tableLoading", { name: workspace.currentTable });
  }
  if (table.datasetReady) {
    return t("status.tableLoaded", { count: table.rowCount });
  }
  if (workspace.phase === "opening") return t("status.databaseOpening");
  if (workspace.phase === "opened" && !workspace.currentTable) {
    return t("status.databaseOpened", { count: workspace.collections.length });
  }
  return t("app.ready");
});
</script>

<template>
  <div class="status-bar">{{ text }}</div>
</template>

<style scoped>
.status-bar {
  font-size: var(--vt-font-caption);
  color: var(--vt-fg-muted);
  padding: var(--vt-space-1) var(--vt-space-3);
  border-top: 1px solid var(--vt-border);
}
</style>
