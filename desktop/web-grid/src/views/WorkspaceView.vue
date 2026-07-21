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
import { computed, onBeforeUnmount, onMounted, provide, ref, watch } from "vue";
import { useMessage } from "naive-ui";
import { NButton, NDropdown, NIcon } from "naive-ui";
import { FilePlus2 } from "lucide-vue-next";
import type { TabulatorFull } from "tabulator-tables";
import AppNavigation from "@/components/layout/AppNavigation.vue";
import AppSidebar from "@/components/layout/AppSidebar.vue";
import AppToolbar from "@/components/layout/AppToolbar.vue";
import ConnectionPill from "@/components/feedback/ConnectionPill.vue";
import GridHost from "@/components/grid/GridHost.vue";
import { TABULATOR_INJECTION_KEY } from "@/components/grid/tabulatorInjection";
import PastePanel from "@/components/panels/PastePanel.vue";
import CreateTableModal from "@/components/panels/CreateTableModal.vue";
import DeleteConfirmModal from "@/components/panels/DeleteConfirmModal.vue";
import ShortcutsView from "@/views/ShortcutsView.vue";
import HomeView from "@/views/HomeView.vue";
import SettingsView from "@/views/SettingsView.vue";
import FileWorkspaceView from "@/views/FileWorkspaceView.vue";
import PluginCenterView from "@/views/PluginCenterView.vue";
import PluginActionPanel from "@/components/plugins/PluginActionPanel.vue";
import PluginSurfaceHost from "@/components/plugins/PluginSurfaceHost.vue";
import { projectPluginTheme } from "@/components/plugins/pluginTheme";
import { useDocumentWorkspaceService } from "@/services/documentWorkspaceService";
import { useHostBridge } from "@/services/bridgeContext";
import { useWorkspaceService } from "@/services/workspaceService";
import { useTableService } from "@/services/tableService";
import { usePasteService } from "@/services/pasteService";
import type { ApplyPasteInput } from "@/services/pasteService";
import { useMutationService } from "@/services/mutationService";
import { useTableAdminService } from "@/services/tableAdminService";
import { useErrorRouter } from "@/services/errorRouter";
import { useIdentifierMappingService } from "@/services/identifierMappingService";
import { createPluginCommandContext, usePluginService } from "@/services/pluginService";
import { useKeyboard } from "@/composables/useKeyboard";
import { useUiStore } from "@/stores/uiStore";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { usePasteStore } from "@/stores/pasteStore";
import { useTableStore } from "@/stores/tableStore";
import { useHistoryStore } from "@/stores/historyStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { usePluginStore } from "@/stores/pluginStore";
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
import { t } from "@/i18n";

const workspaceService = useWorkspaceService();
const hostBridge = useHostBridge();
const documentWorkspaceService = useDocumentWorkspaceService();
const tableService = useTableService();
const pasteService = usePasteService();
const mutationService = useMutationService();
const tableAdminService = useTableAdminService();
const errorRouter = useErrorRouter();
const identifierMappingService = useIdentifierMappingService();
const pluginService = usePluginService();
const ui = useUiStore();
const admin = useTableAdminStore();
const paste = usePasteStore();
const tableStore = useTableStore();
const history = useHistoryStore();
const workspace = useWorkspaceStore();
const plugins = usePluginStore();

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

// Paste outcomes arrive asynchronously from the host. Opening the panel here
// keeps service/store code UI-agnostic while ensuring every non-idle state is
// visible to the user (preview, overflow, result, or error).
watch(
  () => paste.phase,
  (phase) => {
    if (phase !== "idle") ui.openPastePanel();
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

const pluginTheme = computed(() => projectPluginTheme({
  themeMode: ui.themeMode,
  locale: ui.locale,
  density: ui.density,
}));
const registeredPluginActions = computed(() => plugins.plugins.flatMap((plugin) =>
  plugin.manifest.actions.map((action) => ({
    key: `${plugin.pluginId}/${action.actionId}`,
    plugin,
    action,
    label: action.displayName[ui.locale]
      ?? action.displayName["zh-CN"]
      ?? action.displayName["en-US"]
      ?? action.actionId,
  })),
));
const toolbarPluginActions = computed(() => registeredPluginActions.value
  .filter(({ action }) => action.placements.includes("table.toolbar"))
  .map(({ key, label, plugin, action }) => ({
    key,
    label,
    risk: action.risk,
    disabled: plugin.status !== "enabled"
      || action.invocation !== "manual"
      || !workspace.currentTable,
  })));
const pluginContextMenu = ref({ show: false, x: 0, y: 0, rowKey: null as string | number | null });
const pluginContextOptions = computed(() => registeredPluginActions.value
  .filter(({ action }) => action.placements.includes("table.context-menu"))
  .map(({ key, label, plugin, action }) => ({
    key,
    label: `${label} · ${action.risk === "read" ? "只读" : action.risk === "write" ? "写入" : "危险"}`,
    disabled: plugin.status !== "enabled" || action.invocation !== "manual",
  })));

function selectedRowKeys(): readonly (string | number)[] {
  return (tabulator.value?.getSelectedData() ?? []).flatMap((row) =>
    typeof row.rowKey === "string" || typeof row.rowKey === "number" ? [row.rowKey] : [],
  );
}

function pluginContext(keys: readonly (string | number)[]) {
  return createPluginCommandContext({
    projectKey: plugins.projectKey,
    collection: workspace.currentTable,
    selectedKeys: keys,
    querySnapshot: tableStore.pages[0]?.querySnapshot ?? null,
    locale: ui.locale,
    theme: pluginTheme.value.mode,
    density: ui.density,
    user: plugins.currentUser,
    hostVersion: plugins.hostVersion,
  });
}

async function openRegisteredPluginAction(
  key: string,
  forcedKeys?: readonly (string | number)[],
): Promise<void> {
  const registered = registeredPluginActions.value.find((item) => item.key === key);
  if (!registered) return;
  try {
    await pluginService.describeAction(
      registered.plugin.pluginId,
      registered.action.actionId,
      pluginContext(forcedKeys ?? selectedRowKeys()),
    );
  } catch (error) {
    message.error(error instanceof Error ? error.message : String(error));
  }
}

function openPluginContextMenu(payload: { rowKey: string | number; x: number; y: number }): void {
  if (!pluginContextOptions.value.length) return;
  pluginContextMenu.value = { show: true, ...payload };
}

function selectPluginContextAction(key: string): void {
  const rowKey = pluginContextMenu.value.rowKey;
  pluginContextMenu.value.show = false;
  if (rowKey !== null) void openRegisteredPluginAction(key, [rowKey]);
}

async function startDescribedPluginAction(
  payload: Readonly<Record<string, unknown>>,
): Promise<void> {
  const description = plugins.describedAction;
  const context = plugins.activeContext;
  if (!description || !context) return;
  const input: Record<string, unknown> = { ...payload };
  const properties = description.inputSchema.properties;
  if (
    typeof properties === "object" && properties !== null && !Array.isArray(properties)
    && "collection" in properties && input.collection === undefined && context.collection
  ) input.collection = context.collection;
  try {
    await pluginService.startAction(description.pluginId, description.actionId, input, context);
  } catch (error) {
    message.error(error instanceof Error ? error.message : String(error));
  }
}

async function resolvePluginInteraction(decision: "approved" | "rejected"): Promise<void> {
  if (!plugins.activeTask) return;
  try {
    await pluginService.resolveInteraction(plugins.activeTask, decision);
  } catch (error) {
    message.error(error instanceof Error ? error.message : String(error));
  }
}

async function cancelPluginTask(): Promise<void> {
  if (!plugins.activeTask) return;
  try {
    await pluginService.cancelTask(plugins.activeTask.taskId);
  } catch (error) {
    message.error(error instanceof Error ? error.message : String(error));
  }
}

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
  pluginService.init();
  void pluginService.list().catch(() => undefined);
  // App.vue gates this workspace until host startup/auth is ready. Re-announce
  // app.ready only after all business subscriptions are installed so the host
  // replays database.opened that may have completed while StartupGate was shown.
  hostBridge.notify("app.ready", {});
});

onBeforeUnmount(() => pluginService.dispose());

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
  ui.rememberTable(name);
  ui.navigate("tables");
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

function onOpenTableFromHome(name: string) {
  onSelect(name);
}

const pageTitle = computed(() => {
  if (ui.activeView === "home") return t("nav.home");
  if (ui.activeView === "settings") return t("nav.settings");
  if (ui.activeView === "files") return t("nav.files");
  if (ui.activeView === "plugins") return t("nav.plugins");
  return t("nav.tables");
});

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
}

function onCancelCreate() {
  ui.closeCreate();
  admin.close();
}

function onCancelDelete() {
  ui.closeDelete();
  admin.close();
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
  if ("overflow" in classified) {
    paste.setOverflow();
    return;
  }

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

/** Ctrl+A: select the full visible data range using Tabulator's range API. */
function onSelectAll() {
  const grid = tabulator?.value as unknown as {
    getRows?: () => Array<{ getCell: (field: string) => unknown }>;
    getColumns?: () => Array<{ getField: () => string }>;
    getRanges?: () => Array<{ remove?: () => void }>;
    addRange?: (start: unknown, end: unknown) => unknown;
  } | null;
  const rows = grid?.getRows?.() ?? [];
  const columns = (grid?.getColumns?.() ?? []).filter((column) => column.getField() !== "rowKey");
  if (!grid?.addRange || rows.length === 0 || columns.length === 0) return;
  for (const range of grid.getRanges?.() ?? []) range.remove?.();
  grid.addRange(
    rows[0].getCell(columns[0].getField()),
    rows.at(-1)!.getCell(columns.at(-1)!.getField()),
  );
}

/** F2: begin editing the top-left cell in the active range. */
function onEditCell() {
  const range = activeRange() as unknown as {
    getCells?: () => Array<Array<{ edit?: () => void }>>;
  } | null;
  range?.getCells?.()[0]?.[0]?.edit?.();
}

useKeyboard({
  isTableContext: () => ui.activeView === "tables",
  onCopy,
  onPaste,
  onDelete,
  onSelectAll,
  onEditCell,
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
    <AppNavigation
      @open-admin="onOpenAdmin"
      @open-help="ui.openShortcuts"
    />
    <section class="app-surface" :class="`density-${ui.density}`">
      <header class="app-header">
        <div class="app-title">
          <span>VibeTable</span>
          <i></i>
          <strong>{{ pageTitle }}</strong>
        </div>
        <ConnectionPill @reconnect="workspaceService.openDatabase" />
      </header>
      <div class="view-stack">
        <HomeView
          v-show="ui.activeView === 'home'"
          @open-table="onOpenTableFromHome"
          @new-table="onNewTable"
          @open-admin="onOpenAdmin"
        />
        <div v-show="ui.activeView === 'tables'" class="tables-view">
          <AppSidebar
            @select="onSelect"
            @new-table="onNewTable"
            @request-delete="onRequestDelete"
          />
          <main class="main">
            <AppToolbar
              :plugin-actions="toolbarPluginActions"
              @refresh="tableService.refresh"
              @insert-row="mutationService.insertRow({})"
              @open-help="ui.openShortcuts"
              @plugin-action="openRegisteredPluginAction"
            />
            <div v-if="!workspace.currentTable" class="table-empty" data-testid="table-empty">
              <span><NIcon :size="21"><FilePlus2 /></NIcon></span>
              <h2>{{ t("table.empty.title") }}</h2>
              <p>{{ t("table.empty.description") }}</p>
              <NButton type="primary" size="small" @click="onNewTable">{{ t("sidebar.newTable") }}</NButton>
            </div>
            <GridHost
              v-else
              :on-cell-edited="onCellEdited"
              @row-context="openPluginContextMenu"
            />
            <div v-if="workspace.currentTable && tableStore.datasetReady" class="table-summary" data-testid="table-summary">
              {{ t("toolbar.rowCount", { count: tableStore.rowCount }) }}
            </div>
          </main>
        </div>
        <SettingsView
          v-show="ui.activeView === 'settings'"
          @reconnect="workspaceService.openDatabase"
          @open-help="ui.openShortcuts"
          @open-admin="onOpenAdmin"
          @load-mappings="identifierMappingService.load()"
          @save-mapping-aliases="identifierMappingService.updateAliases"
          @import-mappings="identifierMappingService.importMappings"
          @reconcile-mappings="identifierMappingService.reconcile"
          @delete-mapping="identifierMappingService.deleteMapping"
          @purge-mappings="identifierMappingService.purgeMappings"
        />
        <FileWorkspaceView
          v-if="ui.activeView === 'files'"
          @intent="documentWorkspaceService.dispatch"
        />
        <PluginCenterView v-if="ui.activeView === 'plugins'" />
      </div>
    </section>
    <PastePanel @confirm="onConfirmPaste" @cancel="onCancelPaste" />
    <CreateTableModal @submit="onSubmitCreate" @cancel="onCancelCreate" />
    <DeleteConfirmModal @confirm="onConfirmDelete" @cancel="onCancelDelete" />
    <ShortcutsView />
    <NDropdown
      trigger="manual"
      placement="bottom-start"
      :show="pluginContextMenu.show"
      :x="pluginContextMenu.x"
      :y="pluginContextMenu.y"
      :options="pluginContextOptions"
      @select="selectPluginContextAction"
      @clickoutside="pluginContextMenu.show = false"
    />
    <div v-if="plugins.actionOpen && plugins.describedAction" class="plugin-action-overlay">
      <PluginSurfaceHost
        v-if="plugins.describedAction.presentation === 'custom' && plugins.describedAction.surface"
        :src="plugins.describedAction.surface.src"
        :surface-token="plugins.describedAction.surface.surfaceToken"
        :title="plugins.describedAction.surface.title"
        :theme="pluginTheme"
        :task="plugins.activeTask"
        @action="startDescribedPluginAction"
        @resolve="resolvePluginInteraction"
        @cancel="cancelPluginTask"
        @close="plugins.closeAction"
      />
      <PluginActionPanel
        v-else
        :description="plugins.describedAction"
        :task="plugins.activeTask"
        @start="startDescribedPluginAction"
        @resolve="resolvePluginInteraction"
        @cancel="cancelPluginTask"
        @close="plugins.closeAction"
      />
    </div>
  </div>
</template>

<style scoped>
.workspace {
  display: flex;
  flex-direction: row;
  width: 100%;
  min-width: 0;
  height: 100%;
  min-height: 0;
  overflow: hidden;
  background: var(--vt-bg);
}
.plugin-action-overlay { position: fixed; z-index: 80; inset: 0; display: grid; place-items: stretch end; background: rgba(10, 15, 22, .38); backdrop-filter: blur(2px); }
.plugin-action-overlay > :deep(*) { height: 100%; }
.plugin-action-overlay :deep(.surface-shell) { width: min(920px, calc(100% - 60px)); margin: 30px; }
.app-surface {
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
  background: var(--vt-bg);
}
.app-header {
  display: flex;
  flex: 0 0 44px;
  align-items: center;
  justify-content: space-between;
  padding: 0 14px 0 16px;
  border-bottom: 1px solid var(--vt-border);
  background: var(--vt-bg);
}
.app-title { display: flex; align-items: center; gap: 9px; min-width: 0; }
.app-title span { font-weight: 650; letter-spacing: -0.01em; }
.app-title i { width: 1px; height: 13px; background: var(--vt-border); }
.app-title strong { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); font-weight: 500; }
.view-stack { position: relative; flex: 1 1 auto; min-width: 0; min-height: 0; overflow: hidden; background: var(--vt-bg); }
.view-stack > * { width: 100%; height: 100%; }
.tables-view { display: flex; min-width: 0; }
.main {
  display: flex;
  flex-direction: column;
  flex: 1 1 auto;
  min-width: 0;
}
.table-empty {
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  color: var(--vt-fg-muted);
  background: var(--vt-bg);
}
.table-empty > span { display: grid; place-items: center; width: 42px; height: 42px; margin-bottom: 12px; color: var(--vt-color-primary-500); border-radius: var(--vt-radius-lg); background: var(--vt-color-primary-50); }
.table-empty h2 { margin: 0 0 4px; color: var(--vt-fg); font-size: var(--vt-font-title); font-weight: 600; }
.table-empty p { margin: 0 0 16px; }
.table-summary {
  flex: 0 0 26px;
  padding: 4px 12px;
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
  text-align: right;
  border-top: 1px solid var(--vt-border);
  background: var(--vt-bg);
}
.density-compact :deep(.sidebar .table-row) { min-height: 30px; }
.density-compact :deep(.toolbar) { min-height: 36px; }
</style>
