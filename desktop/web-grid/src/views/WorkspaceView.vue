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
 *   3. Registers the global keyboard handler (Task M5). useKeyboard lives here
 *      (not App.vue) because copy/paste/delete need the Tabulator instance
 *      (provided by GridHost via inject) and direct access to the services.
 *
 * The paste apply call needs the current collection + token + a fresh
 * idempotency key; we read those from the paste + workspace stores here so the
 * PastePanel component stays free of service knowledge.
 */
import { onMounted, provide, ref, watch } from "vue";
import { useMessage } from "naive-ui";
import type { TabulatorFull } from "tabulator-tables";
import AppSidebar from "@/components/layout/AppSidebar.vue";
import AppToolbar from "@/components/layout/AppToolbar.vue";
import GridHost from "@/components/grid/GridHost.vue";
import { TABULATOR_INJECTION_KEY } from "@/components/grid/tabulatorInjection";
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
import { useKeyboard } from "@/composables/useKeyboard";
import { useUiStore } from "@/stores/uiStore";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { usePasteStore } from "@/stores/pasteStore";
import { useTableStore } from "@/stores/tableStore";
import { useHistoryStore } from "@/stores/historyStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import {
  classifyClipboard,
  mapCellsToColumns,
  parseClipboard,
} from "@/grid/clipboardParser";
import { resolvePasteContext } from "@/grid/pasteContext";
import type {
  PasteCellPayload,
  PreviewPasteRequestedPayload,
} from "@/contracts";

const workspaceService = useWorkspaceService();
const tableService = useTableService();
const pasteService = usePasteService();
const mutationService = useMutationService();
const tableAdminService = useTableAdminService();
const errorRouter = useErrorRouter();
const ui = useUiStore();
const admin = useTableAdminStore();
const paste = usePasteStore();
const tableStore = useTableStore();
const history = useHistoryStore();
const workspace = useWorkspaceStore();

/**
 * Naive UI message API (requires NMessageProvider, which App.vue wraps around
 * WorkspaceView). Used to surface history.lastError so undo/redo failures are
 * not silent (e.g. when the host rejects an undo because of a stale digest).
 */
const message = useMessage();

/**
 * Surface history failures as toasts. historyStore captures the message in
 * lastError when undo()/redo() throws (e.g. host rejects the inverse op); we
 * watch and toast, then rely on the next successful op or clear() to reset.
 */
watch(
  () => history.lastError,
  (err) => {
    if (err) message.error(err);
  },
);

/**
 * Tabulator instance ref owned by WorkspaceView and shared with GridHost via
 * provide/inject. GridHost injects this ref and forwards it to useTabulator,
 * which populates it when the grid initializes. Null until the first page
 * arrives; read on each shortcut invocation so we always see the current
 * instance (useTabulator rebuilds it on table switch).
 */
const tabulator = ref<TabulatorFull | null>(null);
provide(TABULATOR_INJECTION_KEY, tabulator);

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
  // history.clear() now happens inside tableService.selectTable so EVERY table
  // context reset clears the stack (select + refresh + any future caller).
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
 *
 * `pasteService.apply` stamps `beginApply()` on the store first (so the panel
 * flips to "applying" synchronously) before posting
 * `table.applyPasteRequested` with the single-use `token` from `plan.token`
 * and a fresh `idempotencyKey` so retries do not double-write.
 */
function onConfirmPaste() {
  const plan = paste.plan;
  const collection = workspace.currentTable;
  if (!plan || !collection) return;
  const token = plan.token?.token ?? "";
  const input: ApplyPasteInput = {
    collection,
    token,
    idempotencyKey: crypto.randomUUID(),
  };
  pasteService.apply(input);
}

/** Paste panel: user cancelled. Reset the flow and hide the panel. */
function onCancelPaste() {
  paste.reset();
  ui.closePastePanel();
}

// ---------------------------------------------------------------------------
// Keyboard shortcuts (Task M5). useKeyboard registers a document-level
// keydown listener; the callbacks below translate each shortcut into a
// service call. Copy/paste/delete read the active Tabulator range — guarded
// so they no-op cleanly when there is no selection or no table loaded.
// ---------------------------------------------------------------------------

/**
 * Read the active (latest) Tabulator range, or null if there is no grid or no
 * active range. Factored out so each callback can short-circuit identically.
 */
function activeRange() {
  const ranges = tabulator?.value?.getRanges?.() ?? [];
  return ranges.at(-1) ?? null;
}

/** Ctrl+C: serialize the active range to TSV and write it to the clipboard. */
function onCopy() {
  const range = activeRange();
  if (!range) return;
  const rows = range.getRows();
  const cols = range.getColumns();
  if (rows.length === 0 || cols.length === 0) return;
  const tsv = rows
    .map((row) => {
      const data = row.getData() as Record<string, unknown>;
      return cols
        .map((col) => String(data[col.getField()] ?? ""))
        .join("\t");
    })
    .join("\n");
  // navigator.clipboard is undefined in non-secure contexts; guard so the
  // shortcut no-ops instead of throwing.
  void navigator.clipboard?.writeText?.(tsv);
}

/**
 * Ctrl+V: read the clipboard, resolve the paste context, parse + map cells to
 * editable columns, then forward a {@link PreviewPasteRequestedPayload} to
 * pasteService. Mirrors the legacy main.ts wiring (commit 0713126).
 *
 * Gracefully no-ops when: no clipboard text, no current table, the clipboard
 * is empty/oversize, or resolvePasteContext throws (e.g. the query snapshot
 * has not arrived yet). The paste preview UI opens only on a successful
 * round-trip with the host.
 */
async function onPaste() {
  const collection = workspace.currentTable;
  if (!collection) return;
  // navigator.clipboard may be undefined or reject in non-secure contexts;
  // swallow the rejection so the shortcut fails silently rather than throwing
  // an unhandled promise rejection up to the document.
  let text: string | undefined;
  try {
    text = await navigator.clipboard?.readText?.();
  } catch {
    return;
  }
  if (!text) return;

  let parsed;
  try {
    parsed = parseClipboard(text);
  } catch {
    return;
  }
  const classified = classifyClipboard(parsed);
  // Oversize clipboard: surface nothing here. The legacy flow redirected to
  // file-import via a synthetic overflow plan; that path is out of scope for
  // M5 (deferred to a later task) — silently no-op so the user is not stuck.
  if ("overflow" in classified) return;

  let ctx;
  try {
    ctx = resolvePasteContext({
      grid: tabulator?.value ?? null,
      columns: tableStore.schema ?? [],
      querySnapshot: (tableStore.pages[0] as { querySnapshot?: unknown })
        ?.querySnapshot as never,
      revision: tableStore.revision,
    });
  } catch {
    // resolvePasteContext throws when the schema/snapshot is not ready, no
    // range is selected, or the anchor is not editable. All are user-facing
    // states — no-op so the user can correct and retry.
    return;
  }

  const mapped = mapCellsToColumns(
    parsed,
    ctx.editableColumns,
    ctx.anchorColumnIndex,
  );
  const cells: PasteCellPayload[][] = mapped.map((row) =>
    row.map((cell) => ({
      rowIndex: cell.rowIndex,
      columnIndex: cell.columnIndex,
      column: cell.column,
      rawValue: cell.rawValue,
      // The web layer does not parse typed values; the host's preview step
      // owns type coercion. Pass the raw string as the parsed value (mirrors
      // the legacy main.ts behavior).
      parsedValue: cell.rawValue,
    })),
  );
  const payload: PreviewPasteRequestedPayload = {
    collection,
    schemaRevision: ctx.schemaRevision,
    selection: ctx.selection,
    startCell: ctx.startCell,
    cells,
  };
  pasteService.preview(payload);
}

/**
 * Delete / Backspace: send the active range's rows to mutationService.
 *
 * Per the M5 design decision, NO confirmation dialog is shown — delete is
 * undo-backed: mutationService caches a row snapshot before sending and the
 * resulting `table.rowsDeleted` inbound event pushes a history entry whose
 * undo re-inserts the rows. `expectedDigest` is required by the wire contract
 * but ignored by the backend, so we stringify the rowKey as a stable filler.
 */
function onDelete() {
  const range = activeRange();
  if (!range) return;
  const rows = range
    .getRows()
    .map((row) => {
      const data = row.getData() as { rowKey: number | string };
      return { rowKey: data.rowKey, expectedDigest: String(data.rowKey) };
    });
  if (rows.length === 0) return;
  mutationService.deleteRows(rows);
}

useKeyboard({
  onCopy,
  onPaste,
  onDelete,
  onRefresh: () => tableService.refresh(),
  onNewTable: () => {
    admin.openCreate();
    ui.openCreate();
  },
  onHelp: () => ui.openShortcuts(),
  // Ctrl+Z / Ctrl+Shift+Z route through mutationService.performUndo /
  // performRedo (NOT history.undo directly): the service sets a suppress
  // guard so the host's confirmation round-trip does not push a duplicate
  // entry that would clear the redo stack.
  onUndo: () => void mutationService.performUndo(),
  onRedo: () => void mutationService.performRedo(),
});
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
