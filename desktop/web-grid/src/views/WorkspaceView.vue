<script setup lang="ts">
/**
 * WorkspaceView — the integration layer.
 *
 * This is the ONLY component that imports and calls services. Per spec §2.2,
 * the five child components (AppSidebar, AppToolbar, PastePanel,
 * CreateTableModal, DeleteConfirmModal) are PURE PRESENTATION: they read from
 * stores and emit user intent. WorkspaceView is the container that:
 *
 *   1. Calls `service.init()` on mount for every service so the inbound host
 *      events get wired to their stores (each service's `init()` subscribes to
 *      exactly the events it owns).
 *   2. Translates each component emit into the corresponding outbound service
 *      call (select -> tableService.selectTable, newTable -> open create modal
 *      + admin.openCreate, etc.).
 *
 * The paste apply call needs the current collection + token + a fresh
 * idempotency key; we read those from the paste + workspace stores here so the
 * PastePanel component stays free of service knowledge.
 */
import { onMounted } from "vue";
import AppSidebar from "@/components/layout/AppSidebar.vue";
import AppToolbar from "@/components/layout/AppToolbar.vue";
import GridHost from "@/components/grid/GridHost.vue";
import StatusBar from "@/components/feedback/StatusBar.vue";
import PastePanel from "@/components/panels/PastePanel.vue";
import CreateTableModal from "@/components/panels/CreateTableModal.vue";
import DeleteConfirmModal from "@/components/panels/DeleteConfirmModal.vue";
import ShortcutsView from "@/views/ShortcutsView.vue";
import { useWorkspaceService } from "@/services/workspaceService";
import { useTableService } from "@/services/tableService";
import { usePasteService } from "@/services/pasteService";
import type { ApplyPasteInput } from "@/services/pasteService";
import { useMutationService } from "@/services/mutationService";
import { useTableAdminService } from "@/services/tableAdminService";
import { useErrorRouter } from "@/services/errorRouter";
import { useUiStore } from "@/stores/uiStore";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { usePasteStore } from "@/stores/pasteStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";

const workspaceService = useWorkspaceService();
const tableService = useTableService();
const pasteService = usePasteService();
const mutationService = useMutationService();
const tableAdminService = useTableAdminService();
const errorRouter = useErrorRouter();
const ui = useUiStore();
const admin = useTableAdminStore();
const paste = usePasteStore();
const workspace = useWorkspaceStore();

onMounted(() => {
  // Subscribe each service to its inbound host events. Idempotent across
  // strict-mode double-mount in dev because each bridge.on replaces prior
  // handlers for the same key (see hostBridge).
  workspaceService.init();
  tableService.init();
  pasteService.init();
  mutationService.init();
  tableAdminService.init();
  errorRouter.init();
});

/**
 * Inline cell-edit handler for GridHost. Tabulator fires cellEdited AFTER the
 * cell value is already changed, so oldValue was captured up-front in the
 * cellEditing callback (see createGrid.buildOptions). We forward the full
 * (rowKey, column, oldValue, newValue) tuple to mutationService.updateCell,
 * which notifies the host; the host's `table.editCommitted` inbound event
 * then applies the canonical result to tableStore.
 */
function onCellEdited(
  rowKey: number | string,
  column: string,
  oldValue: unknown,
  newValue: unknown,
) {
  mutationService.updateCell(rowKey, column, oldValue, newValue);
}

/** Sidebar: select a table from the list. */
function onSelect(name: string) {
  tableService.selectTable(name);
}

/** Sidebar: open the create-table modal (reset form + flip UI flag). */
function onNewTable() {
  admin.openCreate();
  ui.openCreate();
}

/** Sidebar: open the host admin window. */
function onOpenAdmin() {
  tableAdminService.openAdmin();
}

/** Sidebar: ask the user to confirm deleting a table. */
function onRequestDelete(name: string) {
  ui.openDelete(name);
}

/** Create modal: submit the form. The service validates canSubmit again. */
function onSubmitCreate() {
  tableAdminService.createTable();
  // Keep the modal open: the service flips phase to idle on success (which
  // closes the modal via the host's database.collectionsChanged round-trip),
  // or to "failed" on error (modal stays open so the user sees the error).
}

/** Delete modal: confirm deletion of the targeted table. */
function onConfirmDelete() {
  const target = ui.deleteTarget;
  if (target) tableAdminService.deleteTable(target);
  ui.closeDelete();
}

/**
 * Paste panel: user confirmed the preview. Build the apply payload from the
 * current plan + workspace and forward to the paste service.
 */
function onConfirmPaste() {
  const plan = paste.plan;
  const collection = workspace.currentTable;
  if (!plan || !collection) return;
  const token = plan.token?.token ?? "";
  const input: ApplyPasteInput = {
    collection,
    token,
    idempotencyKey: `${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
  };
  pasteService.apply(input);
}

/** Paste panel: user cancelled. Reset the flow and hide the panel. */
function onCancelPaste() {
  paste.reset();
  ui.closePastePanel();
}
</script>

<template>
  <div class="workspace">
    <AppSidebar
      @select="onSelect"
      @new-table="onNewTable"
      @open-admin="onOpenAdmin"
      @request-delete="onRequestDelete"
    />
    <main class="main">
      <AppToolbar
        @connect="workspaceService.openDatabase"
        @refresh="tableService.refresh"
        @open-help="ui.openShortcuts"
      />
      <GridHost :on-cell-edited="onCellEdited" />
      <StatusBar />
    </main>
    <PastePanel @confirm="onConfirmPaste" @cancel="onCancelPaste" />
    <CreateTableModal @submit="onSubmitCreate" @cancel="ui.closeCreate()" />
    <DeleteConfirmModal @confirm="onConfirmDelete" @cancel="ui.closeDelete()" />
    <ShortcutsView />
  </div>
</template>

<style scoped>
.workspace {
  display: flex;
  flex-direction: row;
  height: 100%;
}
.main {
  display: flex;
  flex-direction: column;
  flex: 1 1 auto;
  min-width: 0;
}
</style>
