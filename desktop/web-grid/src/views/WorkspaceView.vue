<script setup lang="ts">
/**
 * WorkspaceView — the integration layer.
 *
 * This is the ONLY component that imports and calls services. Per spec §2.2,
 * the five child components (AppSidebar, AppToolbar, PastePanel,
 * CreateTableModal, DeleteConfirmModal) are PURE PRESENTATION: they read from
 * stores and emit user intent. WorkspaceView is the container that:
 *
 *   1. Initializes event-driven services before announcing `app.ready`.
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
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, provide, ref, watch } from "vue";
import { useMessage } from "naive-ui";
import { NButton, NDropdown, NIcon, NModal } from "naive-ui";
import { FilePlus2 } from "@lucide/vue";
import type { TabulatorFull } from "tabulator-tables";
import AppNavigation from "@/components/layout/AppNavigation.vue";
import AppSidebar from "@/components/layout/AppSidebar.vue";
import AppToolbar from "@/components/layout/AppToolbar.vue";
import ContentRecordPanel from "@/content/ContentRecordPanel.vue";
import WorkspaceSearchView from "@/search/WorkspaceSearchView.vue";
import { useWorkspaceSearchStore } from "@/search/workspaceSearchStore";
import { createWorkspaceSearchNavigation } from "@/search/workspaceSearchNavigation";
import ConnectionPill from "@/components/feedback/ConnectionPill.vue";
import GridHost from "@/components/grid/GridHost.vue";
import DataSourceViewBar from "@/components/grid/DataSourceViewBar.vue";
import ViewQueryControls from "@/components/grid/ViewQueryControls.vue";
import ViewGroupPanel from "@/components/grid/ViewGroupPanel.vue";
import { createTabulatorDataSourceViewSource } from "@/grid/dataSourceViewState";
import RecordCalendarView from "@/components/grid/RecordCalendarView.vue";
import RecordGalleryView from "@/components/grid/RecordGalleryView.vue";
import RecordKanbanView from "@/components/grid/RecordKanbanView.vue";
import RecordTimelineView from "@/components/grid/RecordTimelineView.vue";
import RelationEditorPanel from "@/components/grid/RelationEditorPanel.vue";
import FieldSettingsDrawer from "@/field-settings/FieldSettingsDrawer.vue";
import { useFieldSettingsService } from "@/field-settings/service";
import { TABULATOR_INJECTION_KEY } from "@/components/grid/tabulatorInjection";
import PastePanel from "@/components/panels/PastePanel.vue";
import ImportPreviewPanel from "@/components/panels/ImportPreviewPanel.vue";
import CreateTableModal from "@/components/panels/CreateTableModal.vue";
import DeleteConfirmModal from "@/components/panels/DeleteConfirmModal.vue";
import ShortcutsView from "@/views/ShortcutsView.vue";
import HomeView from "@/views/HomeView.vue";
import SettingsView from "@/views/SettingsView.vue";
import FileWorkspaceView from "@/views/FileWorkspaceView.vue";
import PluginCenterView from "@/views/PluginCenterView.vue";
import PluginActionPanel from "@/components/plugins/PluginActionPanel.vue";
import PluginSurfaceHost from "@/components/plugins/PluginSurfaceHost.vue";
import WorkspaceCenter from "@/components/workspace/WorkspaceCenter.vue";
import WorkspaceSwitcher from "@/components/workspace/WorkspaceSwitcher.vue";
import ConflictCenterView from "@/views/ConflictCenterView.vue";
import RevisionHistoryDrawer from "@/components/history/RevisionHistoryDrawer.vue";
import ManagedAttachmentCell from "@/components/attachments/ManagedAttachmentCell.vue";
import JsonValueEditor from "@/components/editors/JsonValueEditor.vue";
import { useDocumentWorkspaceService } from "@/services/documentWorkspaceService";
import { usePresetVersionService } from "@/services/presetVersionService";
import { useHostBridge } from "@/services/bridgeContext";
import { useWorkspaceService } from "@/services/workspaceService";
import { useTableService } from "@/services/tableService";
import { usePasteService } from "@/services/pasteService";
import { useDataIoService } from "@/services/dataIoService";
import { useMutationService } from "@/services/mutationService";
import {
  createStructuredDialogFocus,
  type StructuredGridLike,
} from "@/services/dialogFocus";
import { reportStructuredDialogFocusE2EOutcome } from "@/services/dialogFocusDiagnostics";
import { createNaiveModalContentUnmountAdapter } from "@/services/naiveModalContentUnmount";
import { useTableAdminService } from "@/services/tableAdminService";
import { useErrorRouter } from "@/services/errorRouter";
import { usePluginService } from "@/services/pluginService";
import { useRevisionHistoryService } from "@/services/revisionHistoryService";
import { provideDashboardService, useDashboardService } from "@/services/dashboardService";
import { provideSurfaceService, useSurfaceService } from "@/services/surfaceService";
import { SurfaceHostRepository } from "@/surfaces/surfaceHostRepository";
import { useRelationLookupService } from "@/services/relationLookupService";
import {
  mutationRejectionMessage,
  relationLookupErrorMessage,
  workspaceV2ErrorMessage,
} from "@/services/notificationPolicy";
import { useKeyboard } from "@/composables/useKeyboard";
import { useDataIoTask } from "@/composables/useDataIoTask";
import { useUiStore, type AppView } from "@/stores/uiStore";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { usePasteStore } from "@/stores/pasteStore";
import { useTableStore } from "@/stores/tableStore";
import { useHistoryStore } from "@/stores/historyStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { usePluginStore } from "@/stores/pluginStore";
import { useRevisionHistoryStore } from "@/stores/revisionHistoryStore";
import { useDashboardDraftStore, useDashboardStore } from "@/stores/dashboardStore";
import { useSurfaceStore } from "@/stores/surfaceStore";
import { useRelationLookupStore } from "@/stores/relationLookupStore";
import { useRealtimeStore } from "@/stores/realtimeStore";
import { usePresetVersionStore } from "@/stores/presetVersionStore";
import { useViewQueryStore } from "@/stores/viewQueryStore";
import { useDocumentWorkspaceStore } from "@/stores/documentWorkspaceStore";
import {
  registerWorkspaceEpochReset,
  useWorkspaceSessionStore,
} from "@/stores/workspaceSessionStore";
import { useWorkspaceProtectionStore } from "@/stores/workspaceProtectionStore";
import {
  requestWorkspaceV2UiAction,
} from "@/services/workspaceV2UiPort";
import { decideWorkspaceStartup } from "@/services/workspaceStartupPolicy";
import { createWorkspaceNavigationController } from "@/workspace/workspaceNavigationController";
import { createStructuredCellDialogController } from "@/workspace/structuredCellDialogController";
import { createLookupProvenanceController } from "@/workspace/lookupProvenanceController";
import { createPresetViewController } from "@/workspace/presetViewController";
import { createAlternativeViewInteractionController } from "@/workspace/alternativeViewInteractionController";
import { createRelationEditorController } from "@/workspace/relationEditorController";
import { createWorkspacePluginController } from "@/workspace/workspacePluginController";
import {
  createTabulatorInteractionAdapter,
  createWorkspaceTableInteractionController,
} from "@/workspace/workspaceTableInteractionController";
import { createWorkspaceSessionUiController } from "@/workspace/workspaceSessionUiController";
import { createAuthoritativeLookupController } from "@/workspace/authoritativeLookupController";
import type {
  MutationErrorPayload,
} from "@/contracts";
import { t } from "@/i18n";

const workspaceService = useWorkspaceService();
const message = useMessage();
const hostBridge = useHostBridge();
const documentWorkspaceService = useDocumentWorkspaceService();
const presetVersionService = usePresetVersionService();
const tableService = useTableService();
const pasteService = usePasteService();
const dataIoService = useDataIoService();
const mutationService = useMutationService();
const tableAdminService = useTableAdminService();
const errorRouter = useErrorRouter();
const pluginService = usePluginService();
const revisionHistoryService = useRevisionHistoryService();
const dashboardService = useDashboardService();
provideDashboardService(dashboardService);
const surfaceService = useSurfaceService(new SurfaceHostRepository(hostBridge));
provideSurfaceService(surfaceService);
const relationLookupService = useRelationLookupService();
const fieldSettingsService = useFieldSettingsService({ onCommitted: refreshTable });
const ui = useUiStore();
const admin = useTableAdminStore();
const paste = usePasteStore();
const tableStore = useTableStore();
const history = useHistoryStore();
const workspace = useWorkspaceStore();
const plugins = usePluginStore();
const revisionHistory = useRevisionHistoryStore();
const dashboards = useDashboardStore();
const dashboardDraft = useDashboardDraftStore();
const surfaces = useSurfaceStore();
const realtime = useRealtimeStore();
const presetViews = usePresetVersionStore();
const viewQuery = useViewQueryStore();
const documentWorkspace = useDocumentWorkspaceStore();
const workspaceSearch = useWorkspaceSearchStore();
const contentPanelOpen = ref(false);
const contentRowKey = ref<string | number | null>(null);
const contentRow = computed(() => tableStore.allRows.find(
  (row) => row.rowKey === contentRowKey.value,
) ?? null);
const workspaceSession = useWorkspaceSessionStore();
const workspaceProtection = useWorkspaceProtectionStore();
const showWorkspaceCenter = ref(false);
const workspaceSessionUi = createWorkspaceSessionUiController({
  session: workspaceSession,
  protection: workspaceProtection,
  documents: documentWorkspace,
  showCenter: showWorkspaceCenter,
  request: requestWorkspaceV2UiAction,
  errorMessage: workspaceV2ErrorMessage,
  initializeConsumers: () => initializeBusinessConsumers(),
});
const {
  busy: dataIoBusy,
  previewSession: importPreviewSession,
  applying: importApplying,
  cancelling: importCancelling,
  applyError: importApplyError,
  canPreviewImport: canImportTableData,
  canExport: canExportTableData,
  previewImport: importTableData,
  applyImport: confirmTableImport,
  cancelImport: cancelActiveImport,
  cancelActiveTask: cancelDataTask,
  dismissPreview: cancelImportPreview,
  exportData: exportTableData,
} = useDataIoTask({
  service: dataIoService,
  resolveContext: () => ({
    collection: workspace.currentTable,
    schemaRevision: tableStore.schemaRevision,
  }),
  importSucceeded: (count) => message.success(t("dataIo.import.success", { count })),
  exportSucceeded: (result) => message.success(t("dataIo.export.success", {
    count: result.rowsWritten,
    name: result.outputDisplayName,
  })),
  reportError: (error) => message.error(error),
  refresh: refreshTable,
});
const editRejection = ref<MutationErrorPayload | null>(null);
const editRejectionText = computed(() =>
  editRejection.value ? mutationRejectionMessage(editRejection.value) : "");
const DashboardWorkspaceView = defineAsyncComponent(() => import("@/views/DashboardWorkspaceView.vue"));
const InterfaceWorkspaceView = defineAsyncComponent(() => import("@/views/InterfaceWorkspaceView.vue"));
const workspaceNavigation = createWorkspaceNavigationController(
  {
    dashboardDirty: () => dashboardDraft.dirty,
    surfaceDirty: () => surfaces.dirty,
  },
  {
    confirmDashboardDiscard: () => window.confirm(t("dashboard.confirm.discard")),
    confirmSurfaceDiscard: () => window.confirm(t("surface.confirm.discard")),
    stopDashboardDraft: () => dashboardDraft.stop(),
    resetSurfaceDraft: () => surfaceService.reset(),
  },
);

function onNavigate(view: AppView): void {
  if (view === ui.activeView) {
    showWorkspaceCenter.value = false;
    return;
  }
  workspaceNavigation.attempt(() => {
    showWorkspaceCenter.value = false;
    ui.navigate(view);
  });
}

function openDatabaseWithGuard(): void {
  const departure = workspaceNavigation.authorizeDeparture();
  if (!departure) return;
  void workspaceService.openDatabase().then((outcome) => {
    if (outcome === "opened") departure.commit();
  });
}

function onBeforeUnload(event: BeforeUnloadEvent): void {
  if (!workspaceNavigation.hasUnsavedChanges()) return;
  event.preventDefault();
  event.returnValue = "";
}

const relationLookup = useRelationLookupStore();
const tabulator = ref<TabulatorFull | null>(null);
provide(TABULATOR_INJECTION_KEY, tabulator);
const structuredDialogFocus = createStructuredDialogFocus({
  getGrid: () => tabulator.value as unknown as StructuredGridLike | null,
  getScope: () => ({
    workspaceId: workspaceSession.activeWorkspaceId,
    sessionEpoch: workspaceSession.sessionEpoch,
    tableId: workspace.currentTable,
  }),
  subscribeScope: listener => watch(
    () => [
      workspaceSession.activeWorkspaceId,
      workspaceSession.sessionEpoch,
      workspace.currentTable,
    ] as const,
    listener,
    { flush: "sync" },
  ),
  reportOutcome: reportStructuredDialogFocusE2EOutcome,
});
const tableInteractions = createWorkspaceTableInteractionController({
  workspace,
  table: tableStore,
  ui,
  admin,
  paste,
  pasteService,
  mutationService,
  tableAdminService,
  grid: createTabulatorInteractionAdapter(tabulator),
  readClipboard: async () => await navigator.clipboard?.readText?.() ?? "",
  writeClipboard: async text => { await navigator.clipboard?.writeText?.(text); },
  createId: () => crypto.randomUUID(),
});
const structuredCellDialogs = createStructuredCellDialogController({
  bridge: hostBridge,
  resolveAttachmentAuthority: (rowKey) => {
    const digest = tableStore.allRows.find(item => item.rowKey === rowKey)?.__vibetableDigest;
    return {
      tableId: workspace.currentTable,
      schemaRevision: tableStore.revision?.schemaRevision ?? null,
      expectedDigest: typeof digest === "string" ? digest : null,
    };
  },
  commitJson: edit => onCellEdited(
    edit.rowKey,
    edit.field,
    edit.originalValue,
    edit.value,
    edit.expectedDigest,
  ),
  dialogFocus: structuredDialogFocus,
  translate: t,
  reportError: reportStructuredDialogError,
});
const attachmentModalLifecycle = createNaiveModalContentUnmountAdapter({
  claimRelease: () => structuredCellDialogs.claimCloseLease("attachment"),
  reportError: reportStructuredDialogError,
});
const jsonModalLifecycle = createNaiveModalContentUnmountAdapter({
  claimRelease: () => structuredCellDialogs.claimCloseLease("json"),
  reportError: reportStructuredDialogError,
});
const vAttachmentModalContentUnmount = attachmentModalLifecycle.contentUnmountDirective;
const vJsonModalContentUnmount = jsonModalLifecycle.contentUnmountDirective;
const attachmentPanel = structuredCellDialogs.state.attachment;
const jsonEditor = structuredCellDialogs.state.json;
watch(
  () => attachmentPanel.show,
  show => attachmentModalLifecycle.showChanged(show),
  { flush: "sync", immediate: true },
);
watch(
  () => jsonEditor.show,
  show => jsonModalLifecycle.showChanged(show),
  { flush: "sync", immediate: true },
);
const lookupSourcesDialog = ref<HTMLElement | null>(null);
const attachmentDialog = ref<HTMLElement | null>(null);
const jsonEditorDialog = ref<HTMLElement | null>(null);

function focusModalDialog(dialog: HTMLElement | null): void {
  dialog?.focus({ preventScroll: true });
}

function reportStructuredDialogError(error: unknown): void {
  message.error(error instanceof Error ? error.message : String(error));
}

const lookupProvenance = createLookupProvenanceController({
  readPage: request => relationLookupService.readLookupValuePage(request),
  selectTable: collection => tableService.selectTable(collection),
  navigateTables: () => ui.navigate("tables"),
  queryRecord: (collection, primaryKey, itemId) => hostBridge.notify("table.queryRequested", {
    table: collection,
    query: {
      filters: [{ field: primaryKey, operator: "eq", value: itemId }],
      sorts: [],
      offset: 0,
      limit: 1,
    },
  }),
  getCurrentTable: () => workspace.currentTable,
  getSchemaContext: () => ({
    collection: relationLookup.schema?.collection ?? null,
    primaryKey: relationLookup.schema?.primaryKey ?? null,
  }),
  getRows: () => tableStore.allRows,
  getGrid: () => tabulator.value,
  getColumns: () => tableStore.schema ?? [],
  openContent: (rowKey) => {
    contentRowKey.value = rowKey;
    contentPanelOpen.value = true;
  },
  openAttachment: (rowKey, column) => {
    void structuredCellDialogs.dispatch({ type: "attachment.open", rowKey, column });
  },
  reportLocated: source => message.success(t("workspace.lookup.sourceLocated", {
    collection: source.collection,
    itemId: source.itemId,
  })),
  reportFiltered: source => message.warning(t("workspace.lookup.sourceFiltered", {
    collection: source.collection,
    itemId: source.itemId,
  })),
  errorMessage: relationLookupErrorMessage,
});
const lookupSources = lookupProvenance.state;
const relationEditorController = createRelationEditorController({
  workspace,
  table: tableStore,
  relations: relationLookup,
  service: relationLookupService,
  getTableDefinition: collection => hostBridge.request("schema.getTable", { tableId: collection }),
  selectTable: collection => tableService.selectTable(collection),
  navigateTables: () => ui.navigate("tables"),
  openTarget: target => { void lookupProvenance.dispatch({ type: "source.openTarget", target }); },
  reportInfo: content => message.info(content),
  reportSuccess: content => message.success(content),
  reportError: content => message.error(content),
  unsupportedError: () => t("workspace.relation.unsupported"),
  changedError: () => t("workspace.relation.changed"),
});
const relationEditor = computed(() => relationEditorController.state);
const pendingRelationCreation = relationEditorController.pendingCreation;
const authoritativeLookups = createAuthoritativeLookupController({
  currentTable: () => workspace.currentTable,
  tablePage: () => tableStore.pages[0] ?? null,
  columns: () => tableStore.schema,
  datasetReady: () => tableStore.datasetReady,
  schemaRevision: () => tableStore.revision?.schemaRevision ?? null,
  dataRevision: () => tableStore.revision?.dataRevision ?? null,
  relationSchema: () => relationLookup.schema,
  capabilities: () => relationLookup.capabilities,
  lookups: () => relationLookup.lookups,
  resetContext: () => relationLookup.reset(),
  loadContext: collection => relationLookupService.loadContext(collection),
  queryLookups: request => relationLookupService.queryLookups(request),
  acceptResult: (result, currentDataRevision) => {
    if (!relationLookup.acceptLookup(result, currentDataRevision)) return false;
    tableStore.applyLookupQueryResult(result);
    return true;
  },
  clearEditRejection: () => { editRejection.value = null; },
  reportError: content => message.error(content),
});

function openFieldManager(): void {
  if (!workspace.currentTable) return;
  void fieldSettingsService.openCreate(workspace.currentTable, "relation");
}

/**
 * Naive UI message API (requires NMessageProvider, which App.vue wraps around
 * WorkspaceView). Used to surface history.lastError so undo/redo failures are
 * not silent (e.g. when the host rejects an undo because of a stale digest).
 */
const realtimeTaskProgress = computed(() => {
  const progress = realtime.activeFormulaBackfill?.progress ?? 0;
  return Math.max(0, Math.min(100, Math.round(progress * 100)));
});
const realtimeTaskLabel = computed(() => {
  const task = realtime.activeFormulaBackfill;
  return task ? t(`realtime.task.${task.taskType}`) : "";
});

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

watch(
  () => realtime.reconcileError,
  (error) => {
    if (error) message.warning(t("realtime.reconcileFailed", { error }));
  },
);

watch(
  () => realtime.latestTask?.eventId,
  () => {
    const task = realtime.latestTask;
    if (!task || task.taskType !== "formulaBackfill") return;
    if (task.state === "succeeded") {
      message.success(t("realtime.backfillSucceeded"));
    } else if (task.state === "failed") {
      message.error(t("realtime.backfillFailed", {
        error: task.error?.message ?? t("realtime.unknownError"),
      }));
    } else if (task.state === "cancelled") {
      message.warning(t("realtime.backfillCancelled"));
    }
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

watch(
  () => revisionHistory.appliedSequence,
  (sequence, previous) => {
    if (sequence <= previous) return;
    message.success(t("history.restoreSuccess"));
    // A server restore is a new revision, never a local Ctrl+Z history entry.
    tableService.refresh();
    revisionHistoryService.refresh();
  },
);

/**
 * Tabulator instance ref owned by WorkspaceView and shared with GridHost via
 * provide/inject. GridHost injects this ref and forwards it to useTabulator,
 * which populates it when the grid initializes. Null until the first page
 * arrives; read on each shortcut invocation so we always see the current
 * instance (useTabulator rebuilds it on table switch).
 */
const presetViewController = createPresetViewController({
  workspace,
  table: tableStore,
  ui,
  presets: presetViews,
  query: viewQuery,
  service: presetVersionService,
  grid: createTabulatorDataSourceViewSource(tabulator),
  executeQuery: (table, query) => {
    authoritativeLookups.recordQuery(query);
    hostBridge.notify("table.queryRequested", { table, query });
  },
  refreshLookups: () => { void authoritativeLookups.refresh(); },
  reportError: error => message.error(error instanceof Error ? error.message : String(error)),
  defaultCompensationError: () => new Error(t("views.defaultCompensationFailed")),
});
const activePresetView = presetViewController.activeView;
const activeViewKind = presetViewController.activeKind;
const projectedPresetRows = presetViewController.projectedRows;
const dateFieldOptions = presetViewController.dateFields;
const titleFieldOptions = presetViewController.titleFields;
const groupFieldOptions = presetViewController.groupFields;
const coverFieldOptions = presetViewController.coverFields;
const alternativeViewInteractionController = createAlternativeViewInteractionController({
  getActiveView: () => activePresetView.value,
  getSchema: () => tableStore.schema ?? [],
  getRows: () => tableStore.allRows,
  updateCell: (rowKey, column, oldValue, newValue, expectedDigest) => {
    mutationService.updateCell(rowKey, column, oldValue, newValue, expectedDigest);
  },
});
const kanbanInteraction = computed(() => alternativeViewInteractionController.kanbanState());
const calendarInteraction = computed(() => alternativeViewInteractionController.calendarState());

const pluginController = createWorkspacePluginController({
  workspace,
  table: tableStore,
  ui,
  plugins,
  service: pluginService,
  selectedRows: () => tabulator.value?.getSelectedData() ?? [],
  openHistory: selection => revisionHistoryService.open(selection),
  openFieldCreate: tableId => { void fieldSettingsService.openCreate(tableId); },
  openFieldEdit: (tableId, fieldId) => { void fieldSettingsService.openEdit(tableId, fieldId); },
  reportError: content => message.error(content),
  historyCellLabel: () => t("history.context.cell"),
  historyRowLabel: () => t("history.context.row"),
  riskLabel: risk => t(`plugin.risk.${risk}`),
});
const pluginTheme = pluginController.theme;
const toolbarPluginActions = pluginController.toolbarActions;
const pluginContextMenu = pluginController.rowMenu;
const columnContextMenu = pluginController.columnMenu;
const columnContextOptions = pluginController.columnMenuOptions;
const gridContextOptions = pluginController.rowMenuOptions;
const historyScopeLabel = computed(() => {
  const selected = revisionHistory.selection;
  if (selected.scope === "multiple") return t("history.scope.multiple");
  if (selected.scope === "row") return t("history.scope.row", { item: selected.itemId ?? "—" });
  if (selected.scope === "cell") {
    return t("history.scope.cell", { item: selected.itemId ?? "—", field: selected.field ?? "—" });
  }
  return t("history.scope.table");
});
const historyFieldOptions = computed(() => (tableStore.schema ?? []).map((column) => ({
  label: column.title || column.name,
  value: column.name,
})));

const insertRowDisabled = computed(() =>
  !tableStore.revision?.schemaRevision
  || !tableStore.editSchema?.some((column) => column.editable));

function onHistorySelection(payload: {
  scope: "row" | "cell" | "multiple";
  rowKey?: string | number;
  field?: string;
}): void {
  if (payload.scope === "multiple") {
    revisionHistory.setSelection({ scope: "multiple" });
    return;
  }
  if (payload.rowKey === undefined) return;
  contentRowKey.value = payload.rowKey;
  revisionHistory.setSelection(payload.scope === "row"
    ? { scope: "row", itemId: String(payload.rowKey) }
    : { scope: "cell", itemId: String(payload.rowKey), field: payload.field });
}

function openCurrentHistory(): void {
  let selected = revisionHistory.selection;
  if (
    selected.scope === "cell"
    && tableStore.schema
    && !tableStore.schema.some((column) => column.name === selected.field)
  ) {
    // A workspace restore can replace the schema while Tabulator is retiring
    // its previous range. Do not send that stale physical field name to the
    // new history authority; the only honest surviving scope is the table.
    revisionHistory.setSelection({ scope: "table" });
    selected = revisionHistory.selection;
  }
  if (selected.scope === "multiple") return;
  revisionHistoryService.open({ ...selected, scope: selected.scope });
}

let viewMounted = false;
let businessConsumersInitialized = false;
let startupWorkspaceDecisionMade = false;

function applyWorkspaceStartupPolicy(): void {
  if (!viewMounted || startupWorkspaceDecisionMade) return;
  const decision = decideWorkspaceStartup(
    workspaceSession.enabled,
    workspaceSession.hasOpenWorkspace,
    ui.workspaceStartupPolicy,
    workspaceSession.workspaces,
  );
  if (decision.kind === "wait") return;
  startupWorkspaceDecisionMade = true;
  if (decision.kind === "open") {
    void workspaceSessionUi.open(decision.workspaceId);
    return;
  }
  showWorkspaceCenter.value = decision.kind === "workspaceCenter";
}

function initializeBusinessConsumers(): void {
  if (
    businessConsumersInitialized
    || !viewMounted
    || workspaceSessionUi.activationPending.value
    || (workspaceSession.enabled && !workspaceSession.hasOpenWorkspace)
  ) {
    return;
  }
  businessConsumersInitialized = true;
  // Subscribe each service to its inbound host events. Idempotent across
  // strict-mode double-mount in dev because each bridge.on replaces prior
  // handlers for the same key (see hostBridge).
  workspaceService.init();
  tableService.init();
  // tableService/mutationService already own scalar-row reconciliation.
  // Relation/Lookup invalidation reloads its capability context and the
  // dataRevision watcher below re-queries authoritative Lookup rows. Starting
  // a second full-table refresh here briefly removes the grid, can satisfy UI
  // waits before an undo is committed, and races the mutation confirmation.
  relationLookupService.init();
  pasteService.init();
  mutationService.init(
    (error) => {
      if (error.kind !== "cancelled") editRejection.value = error;
    },
    (result) => { void relationEditorController.dispatch({ type: "pending.complete", result }); },
  );
  tableAdminService.init(async (tableId) => {
    workspace.selectTable(tableId);
    await fieldSettingsService.openCreate(tableId);
  });
  errorRouter.init();
  pluginService.init();
  dashboardService.init();
  void pluginService.list().catch(() => undefined);
  // App.vue gates this workspace until the host runtime is ready. Re-announce
  // app.ready only after all business subscriptions are installed so the host
  // replays database.opened that may have completed while StartupGate was shown.
  hostBridge.notify("app.ready", {});
}

watch(
  () => [workspaceSession.enabled, workspaceSession.hasOpenWorkspace] as const,
  initializeBusinessConsumers,
);
watch(
  () => [
    workspaceSession.enabled,
    workspaceSession.hasOpenWorkspace,
    workspaceSession.workspaces,
    ui.workspaceStartupPolicy,
  ] as const,
  applyWorkspaceStartupPolicy,
);

onMounted(() => {
  viewMounted = true;
  window.addEventListener("beforeunload", onBeforeUnload);
  applyWorkspaceStartupPolicy();
  initializeBusinessConsumers();
});

onBeforeUnmount(() => {
  viewMounted = false;
  unregisterWorkspaceEpochReset();
  tableService.dispose();
  relationLookupService.dispose();
  revisionHistoryService.dispose();
  pluginService.dispose();
  dashboardService.dispose();
  surfaceService.dispose();
  fieldSettingsService.dispose();
  attachmentModalLifecycle.dispose();
  jsonModalLifecycle.dispose();
  structuredDialogFocus.dispose();
  window.removeEventListener("beforeunload", onBeforeUnload);
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
  expectedDigest: string | null = null,
) {
  mutationService.updateCell(
    rowKey,
    column,
    oldValue,
    newValue,
    expectedDigest,
  );
}

/**
 * Inline-edit validation-failure handler for GridHost. When local validation
 * rejects an edit (e.g. too many fractional digits for the column's scale), the
 * grid has already rolled the cell back; we surface a toast so the rejection is
 * not silent. The value was never forwarded to the mutation service.
 */
function onValidationError(
  _rowKey: number | string,
  column: string,
  error: string,
) {
  message.error(`${column}: ${error}`);
}

/** Sidebar: select a table from the list. */
function onSelect(name: string) {
  // history.clear() now happens inside tableService.selectTable so EVERY table
  // context reset clears the stack (select + refresh + any future caller).
  tableService.selectTable(name);
  revisionHistoryService.invalidate();
  revisionHistory.reset();
  ui.rememberTable(name);
  ui.navigate("tables");
}

const workspaceSearchNavigation = createWorkspaceSearchNavigation({
  resolveHit: (hit) => workspaceSearch.resolveHit(hit),
  getDocuments: () => documentWorkspace.entries,
  getDocumentPhase: () => documentWorkspace.phase,
  dispatchDocument: (intent) => documentWorkspaceService.dispatch(intent),
  selectDocument: (index) => documentWorkspace.selectAt(index),
  showDocumentHistory: () => documentWorkspace.showInspector("history"),
  readDocumentHistory: (documentId) => {
    void workspaceSessionUi.execute({
      method: "fileHistory.readTree",
      params: { documentId },
    });
  },
  setLookupNavigation: (target) => {
    void lookupProvenance.dispatch({ type: "source.locate", target });
  },
  selectTable: (tableId) => tableService.selectTable(tableId),
  navigate: (view) => ui.navigate(view),
  warnStale: () => message.warning(t("workspaceSearch.open.stale")),
  reportInvalid: () => message.error(t("workspaceSearch.open.invalid")),
});

function refreshTable(): void {
  editRejection.value = null;
  tableService.refresh();
  if (workspace.currentTable) void relationLookupService.loadContext(workspace.currentTable);
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
  if (ui.activeView === "search") return t("nav.search");
  if (ui.activeView === "plugins") return t("nav.plugins");
  if (ui.activeView === "dashboard") return t("nav.dashboard");
  if (ui.activeView === "interfaces") return t("nav.interfaces");
  if (ui.activeView === "conflicts") return t("workspaceV2.nav.conflicts");
  return t("nav.tables");
});

const showWorkspaceCenterScreen = computed(() =>
  workspaceSession.enabled
  && (showWorkspaceCenter.value
    || (!workspaceSession.hasOpenWorkspace && ui.activeView === "home")));

const unregisterWorkspaceEpochReset = registerWorkspaceEpochReset(
  "workspace-view-v1-consumers",
  ({ nextWorkspaceId }) => {
    void structuredCellDialogs.dispatch({ type: "attachment.close" });
    void structuredCellDialogs.dispatch({ type: "json.close" });
    void lookupProvenance.dispatch({ type: "scope.retire" });
    void relationEditorController.dispatch({ type: "scope.retire" });
    contentPanelOpen.value = false;
    contentRowKey.value = null;
    workspace.clear();
    tableStore.reset();
    history.clear();
    documentWorkspace.clear();
    workspaceSearch.reset();
    relationLookup.reset();
    dashboards.reset();
    dashboardDraft.stop();
    surfaceService.reset();
    presetViews.clearPresets();
    realtime.reset();
    ui.setWorkspaceNamespace(nextWorkspaceId);
  },
);

// ---------------------------------------------------------------------------
// Keyboard shortcuts (Task M5). useKeyboard registers a document-level
// keydown listener; the callbacks below translate each shortcut into a
// service call. Copy/paste/delete read the active Tabulator range — guarded
// so they no-op cleanly when there is no selection or no table loaded.
// ---------------------------------------------------------------------------

useKeyboard({
  isTableContext: () => ui.activeView === "tables",
  onCopy: () => void tableInteractions.dispatch({ type: "keyboard.copy" }),
  onPaste: () => void tableInteractions.dispatch({ type: "keyboard.paste" }),
  onDelete: () => void tableInteractions.dispatch({ type: "keyboard.delete" }),
  onSelectAll: () => void tableInteractions.dispatch({ type: "keyboard.selectAll" }),
  onEditCell: () => void tableInteractions.dispatch({ type: "keyboard.editCell" }),
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
      @navigate="onNavigate"
      @open-admin="onOpenAdmin"
      @open-help="ui.openShortcuts"
    />
    <section class="app-surface" :class="`density-${ui.density}`">
      <header class="app-header">
        <div class="app-title">
          <WorkspaceSwitcher
            v-if="workspaceSession.enabled"
            @switch="workspaceSessionUi.open"
            @center="showWorkspaceCenter = true"
          />
          <span v-else>VibeTable</span>
          <i></i>
          <strong>{{ pageTitle }}</strong>
        </div>
        <ConnectionPill v-if="!workspaceSession.enabled" @reconnect="openDatabaseWithGuard" />
      </header>
      <div class="view-stack">
        <WorkspaceCenter
          v-if="showWorkspaceCenterScreen"
          @action="workspaceSessionUi.execute"
          @open="workspaceSessionUi.open($event.workspaceId)"
        />
        <HomeView
          v-show="!showWorkspaceCenterScreen && ui.activeView === 'home'"
          @open-table="onOpenTableFromHome"
          @new-table="onNewTable"
          @open-admin="onOpenAdmin"
        />
        <div v-show="!showWorkspaceCenterScreen && ui.activeView === 'tables'" class="tables-view">
          <AppSidebar
            @select="onSelect"
            @new-table="onNewTable"
            @request-delete="tableInteractions.dispatch({ type: 'table.requestDelete', table: $event })"
          />
          <main class="main">
            <AppToolbar
              :plugin-actions="toolbarPluginActions"
              :history-scope-label="historyScopeLabel"
              :history-disabled="revisionHistory.selection.scope === 'multiple'"
              :insert-row-disabled="insertRowDisabled"
              :data-io-busy="dataIoBusy"
              :data-io-import-disabled="!canImportTableData"
              :data-io-export-disabled="!canExportTableData"
              @select-table="onSelect"
              @refresh="refreshTable"
              @insert-row="mutationService.insertRow({})"
              @open-help="ui.openShortcuts"
              @open-history="openCurrentHistory"
              @open-archived-history="revisionHistoryService.open({ scope: 'archived' })"
              @open-field-manager="openFieldManager"
              @open-content="contentPanelOpen = true"
              @import-data="importTableData"
              @export-data="exportTableData"
              @cancel-data-task="cancelDataTask"
              @plugin-action="pluginController.dispatch({ type: 'action.open', key: $event })"
            />
            <DataSourceViewBar
              v-if="workspace.currentTable"
              :collection="workspace.currentTable"
              :views="presetViews.presets"
              :active-id="presetViews.activePresetId"
              :loading="presetViews.loading"
              :dirty="presetViews.dirty"
              :error="presetViews.error"
              :date-fields="dateFieldOptions"
              :title-fields="titleFieldOptions"
              :group-fields="groupFieldOptions"
              :cover-fields="coverFieldOptions"
              @create="presetViewController.dispatch({ type: 'view.create', request: $event })"
              @switch="presetViewController.dispatch({ type: 'view.switch', view: $event })"
              @save="presetViewController.dispatch({ type: 'view.save', view: $event })"
              @duplicate="(view, name) => presetViewController.dispatch({ type: 'view.duplicate', view, name })"
              @rename="(view, name) => presetViewController.dispatch({ type: 'view.rename', view, name })"
              @delete="presetViewController.dispatch({ type: 'view.delete', view: $event })"
              @set-default="presetViewController.dispatch({ type: 'view.setDefault', view: $event })"
              @reload="presetViewController.dispatch({ type: 'view.reload' })"
            />
            <section
              v-if="pendingRelationCreation && workspace.currentTable === pendingRelationCreation.targetCollection"
              class="relation-create-notice"
              role="status"
              aria-live="polite"
              data-testid="relation-create-return-notice"
            >
              <span>
                正在为“{{ pendingRelationCreation.relationLabel }}”完整创建目标记录；
                下一条成功创建的记录将自动关联并返回原表。
              </span>
              <NButton size="tiny" quaternary @click="relationEditorController.dispatch({ type: 'pending.cancel' })">
                取消并返回
              </NButton>
            </section>
            <ViewQueryControls
              v-if="workspace.currentTable"
              :columns="tableStore.schema ?? []"
              :filters="viewQuery.filters"
              :groups="viewQuery.groups"
              :summaries="viewQuery.summaries"
              :visible-fields="viewQuery.visibleFields"
              :relations="relationLookup.schema?.normalizedRelations ?? []"
              :lookups="relationLookup.lookups"
              :search-relation-targets="relationEditorController.searchFilterTargets"
              @change="presetViewController.dispatch({ type: 'definition.changed', definition: $event })"
            />
            <ViewGroupPanel
              v-if="workspace.currentTable"
              :rows="tableStore.viewGroups"
              :groups="viewQuery.groups"
              :summaries="viewQuery.summaries"
              :columns="tableStore.schema ?? []"
              :has-more="tableStore.hasMoreViewGroups"
              :collapsed-keys="viewQuery.collapsedGroupKeys"
              @more="presetViewController.dispatch({ type: 'groups.loadMore' })"
              @toggle="presetViewController.dispatch({ type: 'group.toggle', key: $event })"
            />
            <section
              v-if="editRejection"
              class="edit-rejection-notice"
              role="status"
              aria-live="polite"
              data-testid="edit-rejection-notice"
            >
              <span>{{ editRejectionText }}</span>
              <NButton
                size="tiny"
                type="warning"
                secondary
                data-testid="edit-rejection-reload"
                @click="refreshTable"
              >
                {{ t("workspace.editRejected.reload") }}
              </NButton>
              <NButton
                size="tiny"
                quaternary
                :aria-label="t('common.close')"
                @click="editRejection = null"
              >
                {{ t("common.close") }}
              </NButton>
            </section>
            <section
              v-if="realtime.activeFormulaBackfill"
              class="realtime-task"
              role="status"
              aria-live="polite"
              data-testid="realtime-task-progress"
            >
              <div class="realtime-task-copy">
                <strong>{{ realtimeTaskLabel }}</strong>
                <span>{{ t("realtime.backfillHint") }}</span>
              </div>
              <div
                class="realtime-progress-track"
                role="progressbar"
                :aria-label="realtimeTaskLabel"
                aria-valuemin="0"
                aria-valuemax="100"
                :aria-valuenow="realtimeTaskProgress"
              >
                <i :style="{ width: `${realtimeTaskProgress}%` }"></i>
              </div>
              <b>{{ realtimeTaskProgress }}%</b>
            </section>
            <div v-if="!workspace.currentTable" class="table-empty" data-testid="table-empty">
              <span><NIcon :size="21"><FilePlus2 /></NIcon></span>
              <h2>{{ t("table.empty.title") }}</h2>
              <p>{{ t("table.empty.description") }}</p>
              <NButton type="primary" size="small" @click="onNewTable">{{ t("sidebar.newTable") }}</NButton>
            </div>
            <template v-else>
              <GridHost
                v-show="activeViewKind === 'table'"
                :on-cell-edited="onCellEdited"
                :insert-row-disabled="insertRowDisabled"
                @selection-change="onHistorySelection"
                :on-validation-error="onValidationError"
                @row-context="pluginController.dispatch({ type: 'rowMenu.open', ...$event })"
                @column-context="pluginController.dispatch({ type: 'columnMenu.open', ...$event })"
                @relation-edit="relationEditorController.dispatch({ type: 'editor.open', ...$event })"
                @attachment-open="structuredCellDialogs.dispatch({ type: 'attachment.open', ...$event })"
                @json-edit="structuredCellDialogs.dispatch({ type: 'json.open', ...$event })"
                @lookup-source="lookupProvenance.dispatch({ type: 'source.navigate', source: $event })"
				@lookup-source-page="lookupProvenance.dispatch({ type: 'sources.open', page: $event })"
                @view-query-change="presetViewController.dispatch({ type: 'runtime.changed', query: $event })"
                @window-boundary="tableService.loadNextWindow"
                @insert-first-row="mutationService.insertRow({})"
              />
              <RecordCalendarView
                v-if="activeViewKind === 'calendar' && activePresetView"
                :rows="projectedPresetRows"
                :schema="tableStore.schema ?? []"
                :view="activePresetView"
                :interaction-enabled="calendarInteraction.enabled"
                :movable-records="calendarInteraction.movableRecords"
                @intent="alternativeViewInteractionController.dispatch($event)"
              />
              <RecordTimelineView
                v-else-if="activeViewKind === 'timeline' && activePresetView"
                :rows="projectedPresetRows"
                :schema="tableStore.schema ?? []"
                :view="activePresetView"
              />
              <RecordKanbanView
                v-else-if="activeViewKind === 'kanban' && activePresetView"
                :rows="projectedPresetRows"
                :schema="tableStore.schema ?? []"
                :view="activePresetView"
                :interaction-enabled="kanbanInteraction.enabled"
                :lane-options="kanbanInteraction.lanes"
                @card-move="alternativeViewInteractionController.dispatch({ type: 'kanban.card.move', ...$event })"
              />
              <RecordGalleryView
                v-else-if="activeViewKind === 'gallery' && activePresetView"
                :rows="projectedPresetRows"
                :schema="tableStore.schema ?? []"
                :view="activePresetView"
              />
            </template>
            <div v-if="workspace.currentTable && tableStore.datasetReady" class="table-summary" data-testid="table-summary">
              {{ t("toolbar.rowCount", { count: tableStore.rowCount }) }}
            </div>
          </main>
        </div>
        <DashboardWorkspaceView v-if="!showWorkspaceCenterScreen && ui.activeView === 'dashboard'" />
        <InterfaceWorkspaceView v-if="!showWorkspaceCenterScreen && ui.activeView === 'interfaces'" />
        <SettingsView
          v-show="!showWorkspaceCenterScreen && ui.activeView === 'settings'"
          @reconnect="openDatabaseWithGuard"
          @open-help="ui.openShortcuts"
          @workspace-v2-action="workspaceSessionUi.execute"
        />
        <FileWorkspaceView
          v-if="!showWorkspaceCenterScreen && ui.activeView === 'files'"
          :requested-revision-id="workspaceSearchNavigation.requestedRevisionId.value"
          @intent="documentWorkspaceService.dispatch"
          @workspace-v2-action="workspaceSessionUi.execute"
        />
        <WorkspaceSearchView
          v-if="!showWorkspaceCenterScreen && ui.activeView === 'search'"
          @open="workspaceSearchNavigation.open"
        />
        <ConflictCenterView
          v-if="!showWorkspaceCenterScreen && ui.activeView === 'conflicts' && workspaceSession.conflictEnabled"
          @action="workspaceSessionUi.execute"
        />
        <PluginCenterView v-if="!showWorkspaceCenterScreen && ui.activeView === 'plugins'" />
      </div>
    </section>
    <ContentRecordPanel
      :show="contentPanelOpen"
      :table-id="workspace.currentTable ?? ''"
      :row="contentRow"
      :columns="tableStore.schema ?? []"
      :documents="documentWorkspace.entries"
      :document-labels="documentWorkspace.documentLabels"
      @close="contentPanelOpen = false"
      @saved="refreshTable"
    />
    <RelationEditorPanel
      v-if="relationEditor.show"
      :show="relationEditor.show"
      :descriptor="relationEditor.descriptor"
      :field-label="relationEditor.fieldLabel"
      :selected="relationEditorController.selected"
      :candidates="relationEditor.candidates"
      :total="relationEditor.total"
      :query="relationEditor.query"
      :loading="relationEditor.loading"
      :applying="relationEditor.applying"
      :error="relationEditor.error"
      :target-fields="relationEditor.targetFields"
      :target-relations="relationEditor.targetRelations"
      :target-relation-options="relationEditor.targetRelationOptions"
      :target-relation-loading="relationEditor.targetRelationLoading"
      :target-display-field="relationEditor.targetDisplayField"
      :create-schema-loading="relationEditor.createSchemaLoading"
      @close="relationEditorController.dispatch({ type: 'editor.close' })"
      @search="relationEditorController.dispatch({ type: 'targets.search', query: $event })"
      @select="relationEditorController.dispatch({ type: 'target.select', target: $event })"
      @clear="relationEditorController.dispatch({ type: 'target.clear' })"
      @apply="relationEditorController.dispatch({ type: 'draft.apply' })"
      @load-more="relationEditorController.dispatch({ type: 'targets.loadMore' })"
      @create="relationEditorController.dispatch({ type: 'target.create', label: $event })"
      @create-full="relationEditorController.dispatch({ type: 'target.createFull', values: $event })"
      @search-create-relation="(field, query) => relationEditorController.dispatch({ type: 'target.searchNested', field, query })"
      @full-create-fallback="relationEditorController.dispatch({ type: 'target.openFullEditor' })"
      @open="relationEditorController.dispatch({ type: 'target.open', target: $event })"
    />
	<NModal
		:show="lookupSources.show"
		:auto-focus="false"
		:trap-focus="true"
		:close-on-esc="true"
		:mask-closable="true"
		@update:show="show => { if (!show) lookupProvenance.dispatch({ type: 'sources.close' }) }"
		@after-enter="focusModalDialog(lookupSourcesDialog)"
	>
		<div
			ref="lookupSourcesDialog"
			class="lookup-sources-panel"
			role="dialog"
			aria-modal="true"
			aria-labelledby="lookup-sources-title"
			tabindex="-1"
			data-testid="lookup-sources-panel"
		>
			<header>
				<div>
					<strong id="lookup-sources-title">{{ t("workspace.lookup.sourcesTitle") }}</strong>
					<small>{{ lookupSources.items.length }} / {{ lookupSources.total }}{{ lookupSources.totalKnown ? "" : "+" }}</small>
				</div>
				<NButton size="tiny" quaternary @click="lookupProvenance.dispatch({ type: 'sources.close' })">{{ t("common.close") }}</NButton>
			</header>
			<p v-if="lookupSources.error" class="lookup-sources-error" role="alert">{{ lookupSources.error }}</p>
			<ol>
				<li v-for="source in lookupSources.items" :key="`${source.collection}:${source.itemId}:${source.fieldId}`">
					<button
						type="button"
						@click="lookupProvenance.dispatch({ type: 'source.navigate', source }); lookupProvenance.dispatch({ type: 'sources.close' })"
					>
						<span>{{ source.collectionLabel }} · {{ source.recordLabel }}</span>
						<small>{{ source.fieldLabel }} · {{ String(source.value ?? "—") }}</small>
					</button>
				</li>
			</ol>
			<footer v-if="lookupSources.hasMore">
				<NButton size="small" :loading="lookupSources.loading" @click="lookupProvenance.dispatch({ type: 'sources.loadMore' })">
					{{ t("workspace.lookup.loadMoreSources") }}
				</NButton>
			</footer>
		</div>
	</NModal>
    <NModal
      :show="attachmentPanel.show"
      :auto-focus="false"
      :trap-focus="true"
      :close-on-esc="true"
      :mask-closable="true"
      @update:show="show => { if (!show) structuredCellDialogs.dispatch({ type: 'attachment.close' }) }"
      @after-enter="focusModalDialog(attachmentDialog)"
      @before-leave="attachmentModalLifecycle.beforeLeave()"
    >
      <div
        v-attachment-modal-content-unmount
        ref="attachmentDialog"
        class="attachment-panel"
        role="dialog"
        aria-modal="true"
        aria-labelledby="attachment-panel-title"
        tabindex="-1"
        data-testid="attachment-panel"
      >
        <header>
          <div>
            <strong id="attachment-panel-title">
              {{ attachmentPanel.column?.title ?? t("workspace.attachment.title") }}
            </strong>
            <small>{{ workspace.currentTable }} · {{ attachmentPanel.rowKey }}</small>
          </div>
          <NButton
            size="tiny"
            quaternary
            @click="structuredCellDialogs.dispatch({ type: 'attachment.close' })"
          >
            {{ t("common.close") }}
          </NButton>
        </header>
        <p v-if="attachmentPanel.loading" role="status" aria-live="polite">
          {{ t("workspace.attachment.loading") }}
        </p>
        <ManagedAttachmentCell
          v-else-if="attachmentPanel.policy"
          :files="attachmentPanel.files"
          :policy="attachmentPanel.policy"
          :error="attachmentPanel.error"
          @upload="structuredCellDialogs.dispatch({ type: 'attachment.upload' })"
          @replace="structuredCellDialogs.dispatch({ type: 'attachment.replace', storedName: $event })"
          @remove="structuredCellDialogs.dispatch({ type: 'attachment.remove', storedName: $event })"
          @preview="structuredCellDialogs.dispatch({ type: 'attachment.preview', storedName: $event })"
          @download="structuredCellDialogs.dispatch({ type: 'attachment.download', storedName: $event })"
        />
      </div>
    </NModal>
    <NModal
      :show="jsonEditor.show"
      :auto-focus="false"
      :trap-focus="true"
      :close-on-esc="true"
      :mask-closable="true"
      @update:show="show => { if (!show) structuredCellDialogs.dispatch({ type: 'json.close' }) }"
      @after-enter="focusModalDialog(jsonEditorDialog)"
      @before-leave="jsonModalLifecycle.beforeLeave()"
    >
      <div
        v-json-modal-content-unmount
        ref="jsonEditorDialog"
        class="json-editor-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="json-editor-title"
        tabindex="-1"
        data-testid="json-editor-modal"
      >
        <header>
          <div>
            <strong id="json-editor-title">{{ jsonEditor.column?.title ?? "JSON" }}</strong>
            <small>{{ workspace.currentTable }} · {{ jsonEditor.rowKey }}</small>
          </div>
          <NButton
            size="tiny"
            quaternary
            data-testid="json-editor-close"
            @click="structuredCellDialogs.dispatch({ type: 'json.close' })"
          >{{ t("common.close") }}</NButton>
        </header>
        <JsonValueEditor
          :model-value="jsonEditor.value"
          @update:model-value="structuredCellDialogs.dispatch({ type: 'json.change', value: $event })"
          @validity-changed="structuredCellDialogs.dispatch({ type: 'json.validity', valid: $event })"
        />
        <footer>
          <NButton
            size="small"
            @click="structuredCellDialogs.dispatch({ type: 'json.close' })"
          >{{ t("common.cancel") }}</NButton>
          <NButton
            type="primary"
            size="small"
            :disabled="!jsonEditor.valid"
            data-testid="json-editor-save"
            @click="structuredCellDialogs.dispatch({ type: 'json.save' })"
          >{{ t("workspace.json.save") }}</NButton>
        </footer>
      </div>
    </NModal>
    <PastePanel
      @confirm="tableInteractions.dispatch({ type: 'paste.confirm' })"
      @cancel="tableInteractions.dispatch({ type: 'paste.cancel' })"
    />
    <ImportPreviewPanel
      v-if="importPreviewSession"
      :session="importPreviewSession"
      :applying="importApplying"
      :cancellable="dataIoBusy"
      :cancelling="importCancelling"
      :error="importApplyError"
      @confirm="confirmTableImport"
      @cancel="cancelImportPreview"
      @cancel-task="cancelActiveImport"
    />
    <CreateTableModal
      @submit="tableInteractions.dispatch({ type: 'table.create' })"
      @cancel="tableInteractions.dispatch({ type: 'table.cancelCreate' })"
    />
    <DeleteConfirmModal
      @confirm="tableInteractions.dispatch({ type: 'table.delete' })"
      @cancel="tableInteractions.dispatch({ type: 'table.cancelDelete' })"
    />
    <ShortcutsView />
    <RevisionHistoryDrawer
      :field-options="historyFieldOptions"
      @close="revisionHistoryService.close"
      @reload="revisionHistoryService.refresh"
      @load-more="revisionHistoryService.loadMore"
      @preview="revisionHistoryService.previewRestore"
      @apply="revisionHistoryService.applyRestore"
    />
    <FieldSettingsDrawer
      @close="fieldSettingsService.requestClose"
      @plan="fieldSettingsService.plan()"
      @apply="fieldSettingsService.apply"
      @cancel-migration="fieldSettingsService.cancelMigration"
      @load-recycle-bin="fieldSettingsService.loadRecycleBin"
      @restore="fieldSettingsService.restore"
      @load-relation-catalog="fieldSettingsService.loadRelationCatalog"
      @select-relation-target="fieldSettingsService.selectRelationTarget"
      @load-lookup-catalog="fieldSettingsService.loadLookupCatalog"
      @resolve-lookup-path="fieldSettingsService.resolveLookupPath"
      @load-formula-catalog="fieldSettingsService.loadFormulaCatalog"
      @validate-formula="fieldSettingsService.validateFormulaDraft"
    />
    <NDropdown
      trigger="manual"
      placement="bottom-start"
      :show="columnContextMenu.show"
      :x="columnContextMenu.x"
      :y="columnContextMenu.y"
      :options="columnContextOptions"
      @update:show="pluginController.dispatch({ type: 'columnMenu.visible', show: $event })"
      @select="pluginController.dispatch({ type: 'columnMenu.select', key: $event })"
      @clickoutside="pluginController.dispatch({ type: 'columnMenu.visible', show: false })"
    />
    <NDropdown
      trigger="manual"
      placement="bottom-start"
      :show="pluginContextMenu.show"
      :x="pluginContextMenu.x"
      :y="pluginContextMenu.y"
      :options="gridContextOptions"
      @update:show="pluginController.dispatch({ type: 'rowMenu.visible', show: $event })"
      @select="pluginController.dispatch({ type: 'rowMenu.select', key: $event })"
      @clickoutside="pluginController.dispatch({ type: 'rowMenu.visible', show: false })"
    />
    <div v-if="plugins.actionOpen && plugins.describedAction" class="plugin-action-overlay">
      <PluginSurfaceHost
        v-if="plugins.describedAction.presentation === 'custom' && plugins.describedAction.surface"
        :src="plugins.describedAction.surface.src"
        :surface-token="plugins.describedAction.surface.surfaceToken"
        :title="plugins.describedAction.surface.title"
        :theme="pluginTheme"
        :task="plugins.activeTask"
        @action="pluginController.dispatch({ type: 'action.start', payload: $event })"
        @resolve="pluginController.dispatch({ type: 'interaction.resolve', decision: $event })"
        @cancel="pluginController.dispatch({ type: 'task.cancel' })"
        @close="plugins.closeAction"
      />
      <PluginActionPanel
        v-else
        :description="plugins.describedAction"
        :task="plugins.activeTask"
        @start="pluginController.dispatch({ type: 'action.start', payload: $event })"
        @resolve="pluginController.dispatch({ type: 'interaction.resolve', decision: $event })"
        @cancel="pluginController.dispatch({ type: 'task.cancel' })"
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
.lookup-sources-panel {
	position: fixed;
	z-index: 78;
	top: 0;
	right: 0;
	bottom: 0;
	display: flex;
	width: min(480px, calc(100vw - 32px));
	flex-direction: column;
	padding: 18px;
	border-left: 1px solid var(--vt-border);
	background: var(--vt-bg);
	box-shadow: -12px 0 32px rgb(15 23 42 / 12%);
}
.lookup-sources-panel > header,
.lookup-sources-panel > footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.lookup-sources-panel > header { padding-bottom: 12px; border-bottom: 1px solid var(--vt-border); }
.lookup-sources-panel > header div { display: grid; gap: 2px; }
.lookup-sources-panel > header small,
.lookup-sources-panel li small { color: var(--vt-fg-muted); }
.lookup-sources-panel ol { display: grid; flex: 1 1 auto; align-content: start; margin: 0; padding: 8px 0; overflow: auto; list-style: none; }
.lookup-sources-panel li button { display: grid; width: 100%; gap: 3px; padding: 9px 8px; border: 0; border-bottom: 1px solid var(--vt-border); color: inherit; background: transparent; text-align: left; cursor: pointer; }
.lookup-sources-panel li button:hover { background: var(--vt-bg-sunken); }
.lookup-sources-panel > footer { justify-content: center; padding-top: 12px; border-top: 1px solid var(--vt-border); }
.lookup-sources-error { color: var(--vt-color-danger-600); }
.attachment-panel {
  position: fixed;
  z-index: 78;
  top: 0;
  right: 0;
  bottom: 0;
  width: min(440px, calc(100vw - 32px));
  padding: 18px;
  overflow: auto;
  border-left: 1px solid var(--vt-border);
  background: var(--vt-bg);
  box-shadow: -12px 0 32px rgb(15 23 42 / 12%);
}
.attachment-panel > header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 18px;
}
.attachment-panel > header div {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 3px;
}
.attachment-panel > header strong {
  color: var(--vt-fg);
  font-size: var(--vt-font-title);
}
.attachment-panel > header small {
  overflow: hidden;
  color: var(--vt-fg-muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.json-editor-dialog {
  position: fixed;
  z-index: 79;
  top: 50%;
  left: 50%;
  display: grid;
  width: min(680px, calc(100vw - 48px));
  max-height: calc(100vh - 48px);
  gap: 14px;
  padding: 16px;
  overflow: auto;
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-lg);
  background: var(--vt-bg);
  box-shadow: 0 22px 60px rgb(15 23 42 / 24%);
  transform: translate(-50%, -50%);
}
.json-editor-dialog > header,
.json-editor-dialog > footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.json-editor-dialog > header div {
  display: grid;
  gap: 2px;
}
.json-editor-dialog > header small { color: var(--vt-fg-muted); }
.json-editor-dialog > footer { justify-content: flex-end; }
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
.realtime-task {
  display: grid;
  grid-template-columns: minmax(180px, auto) minmax(120px, 1fr) 38px;
  flex: 0 0 40px;
  align-items: center;
  gap: 12px;
  padding: 0 12px;
  border-bottom: 1px solid var(--vt-color-primary-100);
  background: var(--vt-color-primary-50);
}
.edit-rejection-notice {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 8px;
  min-height: 40px;
  padding: 6px 12px;
  border-bottom: 1px solid var(--vt-color-warning);
  background: var(--vt-color-warning-50);
  color: var(--vt-fg);
  font-size: var(--vt-font-caption);
}
.relation-create-notice {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 8px;
  min-height: 40px;
  padding: 6px 12px;
  border-bottom: 1px solid var(--vt-color-primary-200);
  background: var(--vt-color-primary-50);
  color: var(--vt-fg);
  font-size: var(--vt-font-caption);
}
.relation-create-notice > span {
  min-width: 0;
  flex: 1 1 auto;
}
.edit-rejection-notice > span {
  min-width: 0;
  flex: 1 1 auto;
}
.realtime-task-copy {
  display: flex;
  min-width: 0;
  align-items: baseline;
  gap: 8px;
}
.realtime-task-copy strong {
  color: var(--vt-fg);
  font-size: var(--vt-font-caption);
  font-weight: 600;
}
.realtime-task-copy span {
  overflow: hidden;
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.realtime-progress-track {
  height: 4px;
  overflow: hidden;
  border-radius: 999px;
  background: var(--vt-color-primary-100);
}
.realtime-progress-track i {
  display: block;
  height: 100%;
  border-radius: inherit;
  background: var(--vt-color-primary-500);
  transition: width 180ms var(--vt-ease);
}
.realtime-task > b {
  color: var(--vt-fg);
  font-size: var(--vt-font-caption);
  font-variant-numeric: tabular-nums;
  text-align: right;
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
@media (max-width: 899px) {
  .tables-view :deep(.sidebar) { display: none; }
}
</style>
