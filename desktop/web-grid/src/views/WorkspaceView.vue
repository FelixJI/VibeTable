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
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, provide, ref, watch } from "vue";
import { useMessage } from "naive-ui";
import { NButton, NDropdown, NIcon, NModal } from "naive-ui";
import { FilePlus2 } from "lucide-vue-next";
import type { TabulatorFull } from "tabulator-tables";
import AppNavigation from "@/components/layout/AppNavigation.vue";
import AppSidebar from "@/components/layout/AppSidebar.vue";
import AppToolbar from "@/components/layout/AppToolbar.vue";
import ConnectionPill from "@/components/feedback/ConnectionPill.vue";
import GridHost from "@/components/grid/GridHost.vue";
import DataSourceViewBar from "@/components/grid/DataSourceViewBar.vue";
import RelationEditorPanel from "@/components/grid/RelationEditorPanel.vue";
import FieldManagerDrawer from "@/components/relations/FieldManagerDrawer.vue";
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
import WorkspaceCenter from "@/components/workspace/WorkspaceCenter.vue";
import WorkspaceSwitcher from "@/components/workspace/WorkspaceSwitcher.vue";
import ConflictCenterView from "@/views/ConflictCenterView.vue";
import RevisionHistoryDrawer from "@/components/history/RevisionHistoryDrawer.vue";
import ManagedAttachmentCell from "@/components/attachments/ManagedAttachmentCell.vue";
import JsonValueEditor from "@/components/editors/JsonValueEditor.vue";
import { projectPluginTheme } from "@/components/plugins/pluginTheme";
import { useDocumentWorkspaceService } from "@/services/documentWorkspaceService";
import { usePresetVersionService } from "@/services/presetVersionService";
import { useHostBridge } from "@/services/bridgeContext";
import {
  BridgeOperationError,
  BridgeTimeoutError,
} from "@/bridge/hostBridge";
import { useWorkspaceService } from "@/services/workspaceService";
import { useTableService } from "@/services/tableService";
import { usePasteService } from "@/services/pasteService";
import { useDataIoService } from "@/services/dataIoService";
import type { ApplyPasteInput } from "@/services/pasteService";
import { useMutationService } from "@/services/mutationService";
import { useTableAdminService } from "@/services/tableAdminService";
import { buildProductTableDefinition } from "@/services/tableAdminService";
import { buildProductFieldDefinition } from "@/services/schemaFieldDraft";
import { buildProductIndexDefinitions } from "@/services/schemaIndexDraft";
import {
  FormulaPreviewCoordinator,
  createBridgeFormulaPreviewPort,
} from "@/services/formulaPreviewCoordinator";
import { useErrorRouter } from "@/services/errorRouter";
import { useIdentifierMappingService } from "@/services/identifierMappingService";
import { createPluginCommandContext, usePluginService } from "@/services/pluginService";
import { useRevisionHistoryService } from "@/services/revisionHistoryService";
import { provideDashboardService, useDashboardService } from "@/services/dashboardService";
import { useRelationLookupService } from "@/services/relationLookupService";
import {
  restoreStructuredDialogFocus,
  type StructuredDialogFocusTarget,
  type StructuredGridLike,
} from "@/services/dialogFocus";
import {
  buildAuthoritativeLookupViewQuery,
  buildLookupProjectionFieldRefs,
} from "@/services/relationLookupQuery";
import {
  createNotificationDeduper,
  mutationRejectionMessage,
  relationLookupErrorMessage,
  relationLookupNoticeKey,
} from "@/services/notificationPolicy";
import { useKeyboard } from "@/composables/useKeyboard";
import { useUiStore, type AppView } from "@/stores/uiStore";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { usePasteStore } from "@/stores/pasteStore";
import { useTableStore } from "@/stores/tableStore";
import { useHistoryStore } from "@/stores/historyStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { usePluginStore } from "@/stores/pluginStore";
import { useRevisionHistoryStore } from "@/stores/revisionHistoryStore";
import { useDashboardDraftStore, useDashboardStore } from "@/stores/dashboardStore";
import { useRelationLookupStore } from "@/stores/relationLookupStore";
import { useRealtimeStore } from "@/stores/realtimeStore";
import { usePresetVersionStore } from "@/stores/presetVersionStore";
import { useDocumentWorkspaceStore } from "@/stores/documentWorkspaceStore";
import {
  registerWorkspaceEpochReset,
  useWorkspaceSessionStore,
} from "@/stores/workspaceSessionStore";
import { useWorkspaceProtectionStore } from "@/stores/workspaceProtectionStore";
import {
  requestWorkspaceV2UiAction,
  type WorkspaceV2UiAction,
} from "@/services/workspaceV2UiPort";
import { ROW_NUMBER_FIELD } from "@/grid/createGrid";
import {
  applyDataSourceView,
  captureDataSourceView,
  type DataSourceViewGrid,
} from "@/grid/dataSourceViewState";
import type { PresetEntry, PresetView } from "@/contracts";
import {
  classifyClipboard,
  mapCellsToColumns,
  parseClipboard,
} from "@/grid/clipboardParser";
import { resolvePasteContext } from "@/grid/pasteContext";
import { canLeaveDashboardDraft } from "@/dashboard/navigationGuard";
import type {
  NormalizedRelationDescriptor,
  ApplyRelationChangeParams,
  LookupDefinition,
  LookupQueryResult,
  LookupValidationResult,
  FilterCondition,
  LookupGroup,
  LookupValueProvenance,
  SortCondition,
  PasteCellPayload,
  PreviewPasteRequestedPayload,
  RelationTargetRef,
  PreviewRelationChangeParams,
  RelationChangePlan,
  AttachmentPolicy,
  ColumnSchema,
  ManagedAttachmentRef,
  AttachmentListResult,
  MutationErrorPayload,
} from "@/contracts";
import { normalizeTargets } from "@/grid/relationLookupRenderer";
import { t } from "@/i18n";

const workspaceService = useWorkspaceService();
const hostBridge = useHostBridge();
const documentWorkspaceService = useDocumentWorkspaceService();
const presetVersionService = usePresetVersionService();
const tableService = useTableService();
const pasteService = usePasteService();
const dataIoService = useDataIoService();
const mutationService = useMutationService();
const tableAdminService = useTableAdminService();
const errorRouter = useErrorRouter();
const identifierMappingService = useIdentifierMappingService();
const pluginService = usePluginService();
const revisionHistoryService = useRevisionHistoryService();
const dashboardService = useDashboardService();
provideDashboardService(dashboardService);
const relationLookupService = useRelationLookupService();
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
const realtime = useRealtimeStore();
const presetViews = usePresetVersionStore();
const documentWorkspace = useDocumentWorkspaceStore();
const workspaceSession = useWorkspaceSessionStore();
const workspaceProtection = useWorkspaceProtectionStore();
const showWorkspaceCenter = ref(false);
const editRejection = ref<MutationErrorPayload | null>(null);
const shouldShowNotification = createNotificationDeduper();
const editRejectionText = computed(() =>
  editRejection.value ? mutationRejectionMessage(editRejection.value) : "");
const DashboardWorkspaceView = defineAsyncComponent(() => import("@/views/DashboardWorkspaceView.vue"));
const formulaPreviewCoordinator = new FormulaPreviewCoordinator(
  createBridgeFormulaPreviewPort(hostBridge),
);
const formulaPreviews = ref<Record<string, {
  phase: "idle" | "loading" | "ready" | "error";
  value: unknown;
  error: string | null;
}>>({});

function previewDraftFormula(payload: {
  clientId: string;
  index: number;
  row: Readonly<Record<string, unknown>>;
}): void {
  const currentIndex = admin.form.fields.findIndex(
    (field) => field.clientId === payload.clientId,
  );
  if (currentIndex < 0) return;
  const fields = admin.form.fields.map((field, index) =>
    buildProductFieldDefinition(field, index));
  const indexes = admin.localIndexErrors.length === 0
    ? buildProductIndexDefinitions(admin.form.indexes, admin.form.fields, fields)
    : [];
  const formula = fields[currentIndex];
  if (!formula || formula.kind !== "formula") return;
  formulaPreviews.value = {
    ...formulaPreviews.value,
    [payload.clientId]: { phase: "loading", value: null, error: null },
  };
  formulaPreviewCoordinator.schedule(
    {
      definition: buildProductTableDefinition(
        admin.form.name || "formula_preview",
        fields,
        indexes,
      ),
      row: payload.row,
      changedFieldIds: fields
        .filter((field) => field.kind !== "formula")
        .map((field) => field.fieldId),
    },
    {
      onResult: (result) => {
        if (!admin.form.fields.some((field) => field.clientId === payload.clientId)) return;
        formulaPreviews.value = {
          ...formulaPreviews.value,
          [payload.clientId]: {
            phase: "ready",
            value: result.values[formula.physicalName],
            error: null,
          },
        };
      },
      onError: (error) => {
        if (!admin.form.fields.some((field) => field.clientId === payload.clientId)) return;
        formulaPreviews.value = {
          ...formulaPreviews.value,
          [payload.clientId]: {
            phase: "error",
            value: null,
            error: error instanceof Error ? error.message : String(error),
          },
        };
      },
    },
  );
}

watch(() => dashboards.featureEnabled, (enabled) => {
  if (!enabled && ui.activeView === "dashboard") {
    dashboardDraft.stop();
    ui.navigate("home");
  }
});

function confirmDashboardNavigation(): boolean {
  return canLeaveDashboardDraft(
    dashboardDraft.dirty,
    () => window.confirm(t("dashboard.confirm.discard")),
  );
}

function onNavigate(view: AppView): void {
  if (view === ui.activeView) {
    showWorkspaceCenter.value = false;
    return;
  }
  if (!confirmDashboardNavigation()) return;
  showWorkspaceCenter.value = false;
  dashboardDraft.stop();
  ui.navigate(view);
}

function openDatabaseWithGuard(): void {
  if (!confirmDashboardNavigation()) return;
  dashboardDraft.stop();
  workspaceService.openDatabase();
}

function onBeforeUnload(event: BeforeUnloadEvent): void {
  if (!dashboardDraft.dirty) return;
  event.preventDefault();
  event.returnValue = "";
}

const relationLookup = useRelationLookupStore();

const attachmentPanel = ref<{
  show: boolean;
  rowKey: string | number;
  column: ColumnSchema | null;
  policy: AttachmentPolicy | null;
  files: ManagedAttachmentRef[];
  loading: boolean;
  error: string | null;
}>({
  show: false,
  rowKey: "",
  column: null,
  policy: null,
  files: [],
  loading: false,
  error: null,
});
const attachmentDialogTrigger = ref<HTMLElement | null>(null);

const jsonEditor = ref<{
  show: boolean;
  rowKey: string | number;
  column: ColumnSchema | null;
  originalValue: unknown;
  expectedDigest: string | null;
  value: unknown;
  valid: boolean;
}>({
  show: false,
  rowKey: "",
  column: null,
  originalValue: null,
  expectedDigest: null,
  value: null,
  valid: true,
});
const jsonDialogTrigger = ref<StructuredDialogFocusTarget | null>(null);

function activeElement(): HTMLElement | null {
  return document.activeElement instanceof HTMLElement
    ? document.activeElement
    : null;
}

function restoreDialogTrigger(target: HTMLElement | null): void {
  if (target?.isConnected) target.focus({ preventScroll: true });
}

function closeAttachmentPanel(): void {
  attachmentPanel.value.show = false;
}

function finishAttachmentPanelClose(): void {
  restoreDialogTrigger(attachmentDialogTrigger.value);
  attachmentDialogTrigger.value = null;
}

function closeJsonEditor(): void {
  jsonEditor.value.show = false;
}

function finishJsonEditorClose(): void {
  restoreStructuredDialogFocus(
    tabulator.value as unknown as StructuredGridLike | null,
    jsonDialogTrigger.value,
  );
  jsonDialogTrigger.value = null;
}

function openJsonEditor(payload: {
  rowKey: string | number;
  column: ColumnSchema;
  value: unknown;
  expectedDigest: string | null;
  trigger?: HTMLElement | null;
}): void {
  jsonDialogTrigger.value = {
    element: payload.trigger ?? activeElement(),
    rowKey: payload.rowKey,
    field: payload.column.name,
  };
  jsonEditor.value = {
    show: true,
    rowKey: payload.rowKey,
    column: payload.column,
    originalValue: payload.value,
    expectedDigest: payload.expectedDigest,
    value: payload.value,
    valid: true,
  };
}

function commitJsonEdit(): void {
  const state = jsonEditor.value;
  if (!state.valid || !state.column) return;
  onCellEdited(
    state.rowKey,
    state.column.name,
    state.originalValue,
    state.value,
    state.expectedDigest,
  );
  closeJsonEditor();
}

async function openAttachmentPanel(payload: {
  rowKey: string | number;
  column: ColumnSchema;
}): Promise<void> {
  const policy = payload.column.attachmentPolicy;
  const tableId = workspace.currentTable;
  const fieldId = payload.column.fieldId;
  if (!policy || !tableId || !fieldId) {
    message.error(t("workspace.attachment.invalidField"));
    return;
  }
  attachmentDialogTrigger.value = activeElement();
  attachmentPanel.value = {
    show: true,
    rowKey: payload.rowKey,
    column: payload.column,
    policy,
    files: [],
    loading: true,
    error: null,
  };
  try {
    const result = await hostBridge.request("file.list", {
      tableId,
      recordId: String(payload.rowKey),
      fieldId,
    }) as AttachmentListResult;
    if (!Array.isArray(result.attachments)) {
      throw new Error(t("workspace.attachment.invalidResponse"));
    }
    attachmentPanel.value.files = [...result.attachments];
  } catch (error) {
    attachmentPanel.value.error = attachmentErrorMessage(error);
  } finally {
    attachmentPanel.value.loading = false;
  }
}

function attachmentErrorMessage(error: unknown): string {
  if (error instanceof BridgeTimeoutError) {
    return t("workspace.attachment.error.timeout");
  }
  if (error instanceof BridgeOperationError) {
    switch (error.code) {
      case "CANCELLED":
        return t("workspace.attachment.error.cancelled");
      case "ATTACHMENT_CONTEXT_INVALID":
      case "edit_conflict":
        return t("workspace.attachment.staleRow");
      case "ATTACHMENT_UPLOAD_OBJECTS_MISSING":
      case "ATTACHMENT_REPLACE_INVALID":
      case "NATIVE_OBJECTS_UNAVAILABLE":
        return t("workspace.attachment.error.picker");
      default:
        return t("workspace.attachment.error.operation");
    }
  }
  if (
    error instanceof Error
    && error.message === t("workspace.attachment.invalidResponse")
  ) {
    return error.message;
  }
  return t("workspace.attachment.error.generic");
}

function attachmentActionContext(): {
  tableId: string;
  recordId: string;
  fieldId: string;
  schemaRevision: string;
  expectedDigest: string;
} | null {
  const tableId = workspace.currentTable;
  const fieldId = attachmentPanel.value.column?.fieldId;
  const schemaRevision = tableStore.revision?.schemaRevision;
  const row = tableStore.allRows.find(
    (item) => item.rowKey === attachmentPanel.value.rowKey,
  );
  const digest = row?.__vibetableDigest;
  if (
    !tableId
    || !fieldId
    || !schemaRevision
    || typeof digest !== "string"
    || !/^sha256:[0-9a-f]{64}$/u.test(digest)
  ) {
    attachmentPanel.value.error = t("workspace.attachment.staleRow");
    return null;
  }
  return {
    tableId,
    recordId: String(attachmentPanel.value.rowKey),
    fieldId,
    schemaRevision,
    expectedDigest: digest,
  };
}

async function uploadAttachments(): Promise<void> {
  const context = attachmentActionContext();
  if (!context) return;
  attachmentPanel.value.loading = true;
  attachmentPanel.value.error = null;
  try {
    // The desktop host owns file selection. Renderer JSON never contains a
    // path, and one native picker behaves consistently across WebView2
    // runtimes, accessibility tooling, and automation.
    await hostBridge.request("file.uploadRequested", context);
    attachmentPanel.value.show = false;
  } catch (error) {
    attachmentPanel.value.error = attachmentErrorMessage(error);
  } finally {
    attachmentPanel.value.loading = false;
  }
}

async function replaceAttachment(storedName: string): Promise<void> {
  const context = attachmentActionContext();
  if (!context) return;
  const payload = { ...context, storedName };
  attachmentPanel.value.loading = true;
  attachmentPanel.value.error = null;
  try {
    await hostBridge.request("file.replaceRequested", payload);
    attachmentPanel.value.show = false;
  } catch (error) {
    attachmentPanel.value.error = attachmentErrorMessage(error);
  } finally {
    attachmentPanel.value.loading = false;
  }
}

async function removeAttachment(storedName: string): Promise<void> {
  const context = attachmentActionContext();
  if (!context) return;
  attachmentPanel.value.loading = true;
  attachmentPanel.value.error = null;
  try {
    await hostBridge.request("file.removeRequested", { ...context, storedName });
    attachmentPanel.value.files = attachmentPanel.value.files.filter(
      (item) => item.storedName !== storedName,
    );
  } catch (error) {
    attachmentPanel.value.error = attachmentErrorMessage(error);
  } finally {
    attachmentPanel.value.loading = false;
  }
}

function downloadAttachment(storedName: string): void {
  const file = attachmentPanel.value.files.find(
    (item) => item.storedName === storedName,
  );
  const tableId = workspace.currentTable;
  const fieldId = attachmentPanel.value.column?.fieldId;
  if (!file || !tableId || !fieldId) return;
  hostBridge.notify("file.downloadRequested", {
    tableId,
    recordId: String(attachmentPanel.value.rowKey),
    fieldId,
    storedName,
    originalName: file.originalName,
  });
}

function previewAttachment(storedName: string): void {
  const file = attachmentPanel.value.files.find(
    (item) => item.storedName === storedName,
  );
  const tableId = workspace.currentTable;
  const fieldId = attachmentPanel.value.column?.fieldId;
  if (!file || !tableId || !fieldId) return;
  hostBridge.notify("file.previewRequested", {
    tableId,
    recordId: String(attachmentPanel.value.rowKey),
    fieldId,
    storedName,
    originalName: file.originalName,
  });
}

const relationEditor = ref<{
  show: boolean;
  rowKey: string | number | null;
  field: string;
  descriptor: NormalizedRelationDescriptor | null;
  candidates: readonly RelationTargetRef[];
  query: string;
  m2aCollection: string | null;
  loading: boolean;
  applying: boolean;
  error: string | null;
}>({
  show: false,
  rowKey: null,
  field: "",
  descriptor: null,
  candidates: [],
  query: "",
  m2aCollection: null,
  loading: false,
  applying: false,
  error: null,
});
const fieldManager = ref<{
  show: boolean;
  busy: boolean;
  error: string | null;
  relationPlan: RelationChangePlan | null;
  lookupValidation: LookupValidationResult | null;
  lookupPreview: LookupQueryResult | null;
  schemas: Record<string, import("@/contracts").SchemaSnapshot>;
  lookupCatalog: Record<string, readonly LookupDefinition[]>;
}>({
  show: false,
  busy: false,
  error: null,
  relationPlan: null,
  lookupValidation: null,
  lookupPreview: null,
  schemas: {},
  lookupCatalog: {},
});
let relationSearchGeneration = 0;
let lookupDatasetGeneration = 0;
const lookupSourceNavigation = ref<{
  source: LookupValueProvenance;
  queryRequested: boolean;
} | null>(null);
const interactiveGridQuery = ref<{
  filters: readonly FilterCondition[];
  sorts: readonly SortCondition[];
  groups: readonly LookupGroup[];
} | null>(null);

watch(
  () => workspace.currentTable,
  (collection) => {
    editRejection.value = null;
    relationEditor.value.show = false;
    fieldManager.value.relationPlan = null;
    fieldManager.value.lookupValidation = null;
    fieldManager.value.lookupPreview = null;
    interactiveGridQuery.value = null;
    fieldManager.value.schemas = {};
    fieldManager.value.lookupCatalog = {};
    if (!collection) {
      fieldManager.value.show = false;
      relationLookup.reset();
      return;
    }
    void relationLookupService.loadContext(collection);
  },
  { immediate: true },
);

watch(
  [
    () => relationLookup.schema?.lookupRevision,
    () => relationLookup.capabilities?.lookupQueryV1,
    () => tableStore.datasetReady,
  ],
  () => { void refreshAuthoritativeLookupRows(); },
);

async function refreshAuthoritativeLookupRows(): Promise<void> {
  // Invalidate any older request before evaluating readiness. Context reloads
  // briefly clear capabilities/lookups; that transition must still retire an
  // in-flight response instead of surfacing an expected stale-revision error.
  const requestGeneration = ++lookupDatasetGeneration;
  const collection = workspace.currentTable;
  const page = tableStore.pages[0];
  const columns = tableStore.schema;
  if (
    !collection
    || !page
    || !columns
    || !tableStore.datasetReady
    || !relationLookup.capabilities?.lookupQueryV1
    || relationLookup.lookups.length === 0
  ) return;
  const fieldRefs = buildLookupProjectionFieldRefs(relationLookup.lookups);
  const fieldRefByName = new Map(columns.map((column) => [
    column.name,
    column.fieldId ?? `${collection}.${column.name}`,
  ]));
  const normalized = interactiveGridQuery.value ?? page.querySnapshot?.normalizedQuery ?? {};
  const { filters, sorts, groups } = buildAuthoritativeLookupViewQuery(normalized, fieldRefByName);
  try {
    const result = await relationLookupService.queryDataset({
      collection,
      fieldRefs,
      query: { filters, sorts, groups },
    });
    if (requestGeneration !== lookupDatasetGeneration) return;
    tableStore.applyLookupQueryResult(result);
  } catch (error) {
    if (requestGeneration !== lookupDatasetGeneration) return;
    const content = relationLookupErrorMessage(error);
    if (content && shouldShowNotification(relationLookupNoticeKey(error))) {
      message.error(content);
    }
  }
}

function onGridViewQueryChanged(query: {
  readonly filters: readonly FilterCondition[];
  readonly sorts: readonly SortCondition[];
  readonly groups: readonly LookupGroup[];
}): void {
  const table = workspace.currentTable;
  if (!table) return;
  interactiveGridQuery.value = query;
  if (!applyingPresetView) presetViews.markDirty();
  hostBridge.notify("table.queryRequested", {
    table,
    // Standard table.query is page-bounded (backend max 500) but compiles the
    // sort/filter on the data service, so the page is selected from the full dataset.
    query: { filters: query.filters, sorts: query.sorts, offset: 0, limit: 500 },
  });
  void refreshAuthoritativeLookupRows();
}

function navigateLookupSource(source: LookupValueProvenance): void {
  lookupSourceNavigation.value = { source, queryRequested: false };
  tableService.selectTable(source.collection);
  ui.navigate("tables");
}

function openFieldManager(): void {
  if (!workspace.currentTable) return;
  fieldManager.value.show = true;
  fieldManager.value.error = null;
  if (relationLookup.schema) fieldManager.value.schemas[relationLookup.schema.collection] = relationLookup.schema;
  fieldManager.value.lookupCatalog[workspace.currentTable] = relationLookup.lookups;
}

function closeFieldManager(): void {
  if (fieldManager.value.busy) return;
  fieldManager.value.show = false;
  fieldManager.value.relationPlan = null;
  fieldManager.value.lookupValidation = null;
  fieldManager.value.lookupPreview = null;
}

async function previewRelationChange(params: PreviewRelationChangeParams): Promise<void> {
  fieldManager.value.busy = true;
  fieldManager.value.error = null;
  fieldManager.value.relationPlan = null;
  try {
    fieldManager.value.relationPlan = await relationLookupService.previewRelationChange(params);
  } catch (error) {
    fieldManager.value.error = error instanceof Error ? error.message : String(error);
  } finally {
    fieldManager.value.busy = false;
  }
}

async function applyRelationChange(params: ApplyRelationChangeParams): Promise<void> {
  fieldManager.value.busy = true;
  fieldManager.value.error = null;
  try {
    await relationLookupService.applyRelationChange(params);
    fieldManager.value.relationPlan = null;
    await reloadFieldContext();
    message.success(t("workspace.relation.structureUpdated"));
  } catch (error) {
    fieldManager.value.error = error instanceof Error ? error.message : String(error);
  } finally {
    fieldManager.value.busy = false;
  }
}

async function validateLookupDefinition(definition: LookupDefinition): Promise<void> {
  fieldManager.value.busy = true;
  fieldManager.value.error = null;
  fieldManager.value.lookupValidation = null;
  try {
    fieldManager.value.lookupValidation = await relationLookupService.validateLookup(definition);
  } catch (error) {
    fieldManager.value.error = error instanceof Error ? error.message : String(error);
  } finally {
    fieldManager.value.busy = false;
  }
}

async function previewLookupDefinition(definition: LookupDefinition): Promise<void> {
  fieldManager.value.busy = true;
  fieldManager.value.error = null;
  fieldManager.value.lookupPreview = null;
  try {
    fieldManager.value.lookupPreview = await relationLookupService.previewLookup(definition);
  } catch (error) {
    fieldManager.value.error = error instanceof Error ? error.message : String(error);
  } finally {
    fieldManager.value.busy = false;
  }
}

async function mutateLookup(
  operation: "create" | "update" | "delete",
  definition: LookupDefinition,
): Promise<void> {
  fieldManager.value.busy = true;
  fieldManager.value.error = null;
  try {
    if (operation === "create") await relationLookupService.createLookup(definition);
    else if (operation === "update") await relationLookupService.updateLookup(definition);
    else await relationLookupService.deleteLookup(definition);
    fieldManager.value.lookupValidation = null;
    fieldManager.value.lookupPreview = null;
    await reloadFieldContext();
    message.success(
      operation === "delete"
        ? t("workspace.lookup.deleted")
        : t("workspace.lookup.saved"),
    );
  } catch (error) {
    fieldManager.value.error = error instanceof Error ? error.message : String(error);
  } finally {
    fieldManager.value.busy = false;
  }
}

async function reloadFieldContext(): Promise<void> {
  const collection = workspace.currentTable;
  if (!collection) return;
  const accepted = await relationLookupService.loadContext(collection);
  if (accepted) {
    if (relationLookup.schema) fieldManager.value.schemas = { [collection]: relationLookup.schema };
    fieldManager.value.lookupCatalog = { [collection]: relationLookup.lookups };
    await tableService.refresh();
  }
}

async function loadFieldManagerSchema(collection: string): Promise<void> {
  if (!collection) return;
  try {
    const [snapshot, lookups] = await Promise.all([
      fieldManager.value.schemas[collection]
        ? Promise.resolve(fieldManager.value.schemas[collection])
        : relationLookupService.describeCollection(collection),
      fieldManager.value.lookupCatalog[collection]
        ? Promise.resolve({ definitions: fieldManager.value.lookupCatalog[collection] })
        : relationLookupService.listCollectionLookups(collection),
    ]);
    fieldManager.value.schemas = { ...fieldManager.value.schemas, [collection]: snapshot };
    fieldManager.value.lookupCatalog = { ...fieldManager.value.lookupCatalog, [collection]: lookups.definitions };
  } catch (error) {
    fieldManager.value.error = error instanceof Error ? error.message : String(error);
  }
}

/**
 * Naive UI message API (requires NMessageProvider, which App.vue wraps around
 * WorkspaceView). Used to surface history.lastError so undo/redo failures are
 * not silent (e.g. when the host rejects an undo because of a stale digest).
 */
const message = useMessage();
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
const tabulator = ref<TabulatorFull | null>(null);
provide(TABULATOR_INJECTION_KEY, tabulator);
const pendingPresetView = ref<PresetView | null>(null);
const memoryDefaultViews = new Map<string, PresetView>();
let presetLoadGeneration = 0;
let applyingPresetView = false;

function captureCurrentView(isDefault = false): PresetView {
  return captureDataSourceView(
    tabulator.value as unknown as DataSourceViewGrid | null,
    { isDefault, density: ui.density },
  );
}

async function applyView(view: PresetView): Promise<void> {
  const grid = tabulator.value as unknown as DataSourceViewGrid | null;
  if (!grid) {
    pendingPresetView.value = view;
    return;
  }
  pendingPresetView.value = null;
  applyingPresetView = true;
  try {
    await applyDataSourceView(grid, view);
    await nextTick();
  } finally {
    applyingPresetView = false;
  }
}

watch(tabulator, (grid) => {
  const collection = workspace.currentTable;
  if (!grid || !collection) return;
  if (pendingPresetView.value) {
    void applyView(pendingPresetView.value);
  } else if (presetViews.presets.length === 0 && !memoryDefaultViews.has(collection)) {
    memoryDefaultViews.set(collection, captureCurrentView());
  }
});

async function loadCollectionViews(collection: string): Promise<void> {
  const generation = ++presetLoadGeneration;
  presetViews.begin();
  try {
    const result = await presetVersionService.listPresets(collection);
    if (generation !== presetLoadGeneration || workspace.currentTable !== collection) return;
    presetViews.receivePresets(result);
    const selected = result.presets.find((view) => view.view.isDefault) ?? result.presets[0];
    presetViews.activatePreset(selected?.id ?? null);
    if (selected) {
      await applyView(selected.view);
    } else if (tabulator.value && !memoryDefaultViews.has(collection)) {
      memoryDefaultViews.set(collection, captureCurrentView());
    }
  } catch (error) {
    if (generation === presetLoadGeneration) {
      message.error(error instanceof Error ? error.message : String(error));
    }
  }
}

watch(
  () => workspace.currentTable,
  (collection) => {
    pendingPresetView.value = null;
    presetViews.clearPresets(collection ?? "");
    if (collection) void loadCollectionViews(collection);
  },
  { immediate: true },
);

async function persistView(
  collection: string,
  name: string,
  view: PresetView,
  presetId?: string | null,
): Promise<PresetEntry | null> {
  if (workspace.currentTable !== collection) return null;
  const generation = presetLoadGeneration;
  presetViews.begin();
  try {
    const saved = await presetVersionService.savePreset(
      collection,
      name,
      view,
      presetId,
    );
    if (generation !== presetLoadGeneration || workspace.currentTable !== collection) {
      return null;
    }
    presetViews.upsertPreset(saved);
    return saved;
  } catch (error) {
    if (generation === presetLoadGeneration && workspace.currentTable === collection) {
      presetViews.fail(error);
    }
    return null;
  }
}

async function saveView(view: PresetEntry): Promise<PresetEntry | null> {
  const collection = workspace.currentTable;
  if (!collection || view.collection !== collection) return null;
  const saved = await persistView(
    collection,
    view.name,
    { ...captureCurrentView(view.view.isDefault), isDefault: view.view.isDefault },
    view.id,
  );
  if (saved) presetViews.markSaved();
  return saved;
}

async function switchView(view: PresetEntry): Promise<void> {
  if (view.collection !== workspace.currentTable || presetViews.loading) return;
  if (view.id === presetViews.activePresetId) return;
  const current = presetViews.presets.find((item) => item.id === presetViews.activePresetId);
  if (current && !(await saveView(current))) return;
  presetViews.activatePreset(view.id);
  await applyView(view.view);
}

async function createView(name: string): Promise<void> {
  const collection = workspace.currentTable;
  if (!collection) return;
  const saved = await persistView(
    collection,
    name,
    captureCurrentView(presetViews.presets.length === 0),
  );
  if (!saved) return;
  presetViews.activatePreset(saved.id);
}

async function duplicateView(view: PresetEntry, name: string): Promise<void> {
  const collection = workspace.currentTable;
  if (!collection) return;
  const saved = await persistView(collection, name, {
    ...view.view,
    isDefault: false,
  });
  if (!saved) return;
  presetViews.activatePreset(saved.id);
  await applyView(saved.view);
}

async function renameView(view: PresetEntry, name: string): Promise<void> {
  const source = view.id === presetViews.activePresetId
    ? { ...captureCurrentView(view.view.isDefault), isDefault: view.view.isDefault }
    : view.view;
  const saved = await persistView(
    view.collection,
    name,
    source,
    view.id,
  );
  if (!saved) return;
  presetViews.activatePreset(saved.id);
}

async function deleteView(view: PresetEntry): Promise<void> {
  if (view.collection !== workspace.currentTable || presetViews.loading) return;
  const generation = presetLoadGeneration;
  presetViews.begin();
  try {
    await presetVersionService.deletePreset(view.id, view.revision);
  } catch (error) {
    if (generation === presetLoadGeneration && workspace.currentTable === view.collection) {
      presetViews.fail(error);
    }
    return;
  }
  if (generation !== presetLoadGeneration || workspace.currentTable !== view.collection) return;
  presetViews.removePreset(view.id);
  const next = presetViews.presets.find((item) => item.view.isDefault) ?? presetViews.presets[0];
  presetViews.activatePreset(next?.id ?? null);
  if (next) {
    await applyView(next.view);
  } else {
    const fallback = memoryDefaultViews.get(view.collection);
    if (fallback) await applyView(fallback);
  }
}

async function setDefaultView(view: PresetEntry): Promise<void> {
  const previous = presetViews.presets.find((item) => item.view.isDefault && item.id !== view.id);
  const source = view.id === presetViews.activePresetId
    ? captureCurrentView(true)
    : { ...view.view, isDefault: true };
  const saved = await persistView(
    view.collection,
    view.name,
    source,
    view.id,
  );
  if (!saved) return;
  if (previous) {
    const demoted = await persistView(previous.collection, previous.name, {
      ...previous.view,
      isDefault: false,
    }, previous.id);
    if (!demoted && workspace.currentTable === view.collection) {
      const compensated = await persistView(view.collection, view.name, {
        ...source,
        isDefault: false,
      }, view.id);
      if (!compensated) {
        presetViews.fail(new Error(t("views.defaultCompensationFailed")));
        return;
      }
      await loadCollectionViews(view.collection);
      return;
    }
  }
  presetViews.activatePreset(saved.id);
}

watch(
  [
    () => workspace.currentTable,
    () => relationLookup.schema?.collection,
    () => relationLookup.schema?.primaryKey,
  ],
  ([collection, schemaCollection, primaryKey]) => {
    const navigation = lookupSourceNavigation.value;
    if (
      !navigation
      || navigation.queryRequested
      || collection !== navigation.source.collection
      || schemaCollection !== navigation.source.collection
      || !primaryKey
    ) return;
    navigation.queryRequested = true;
    hostBridge.notify("table.queryRequested", {
      table: navigation.source.collection,
      query: {
        filters: [{ field: primaryKey, operator: "eq", value: navigation.source.itemId }],
        sorts: [],
        offset: 0,
        limit: 1,
      },
    });
  },
);

watch(
  [() => tableStore.allRows, () => tabulator.value],
  async ([rows, grid]) => {
    const navigation = lookupSourceNavigation.value;
    if (!navigation?.queryRequested || !grid) return;
    const matchingRow = rows.find(
      (row) => String(row.rowKey) === navigation.source.itemId,
    );
    if (!matchingRow) return;
    const rowKey = matchingRow.rowKey as string | number;
    await nextTick();
    try {
      const navigationGrid = grid as unknown as {
        scrollToRow: (key: string | number, position: "center", ifVisible: boolean) => Promise<void>;
        getRow: (key: string | number) => {
          select: () => void;
          getElement: () => HTMLElement;
        } | false;
      };
      await navigationGrid.scrollToRow(rowKey, "center", true);
      const row = navigationGrid.getRow(rowKey);
      if (row) {
        row.select();
        row.getElement().classList.add("vt-row-selected");
      }
      message.success(t("workspace.lookup.sourceLocated", {
        collection: navigation.source.collection,
        itemId: navigation.source.itemId,
      }));
      lookupSourceNavigation.value = null;
    } catch {
      message.warning(t("workspace.lookup.sourceFiltered", {
        collection: navigation.source.collection,
        itemId: navigation.source.itemId,
      }));
      lookupSourceNavigation.value = null;
    }
  },
);

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
const pluginContextMenu = ref({
  show: false,
  x: 0,
  y: 0,
  rowKey: null as string | number | null,
  field: null as string | null,
});
const pluginContextOptions = computed(() => registeredPluginActions.value
  .filter(({ action }) => action.placements.includes("table.context-menu"))
  .map(({ key, label, plugin, action }) => ({
    key,
    label: `${label} · ${t(`plugin.risk.${action.risk}`)}`,
    disabled: plugin.status !== "enabled" || action.invocation !== "manual",
  })));
const gridContextOptions = computed(() => [
  {
    label: t("history.context.cell"),
    key: "history:cell",
    disabled: !pluginContextMenu.value.field,
  },
  { label: t("history.context.row"), key: "history:row" },
  ...(pluginContextOptions.value.length
    ? [{ type: "divider", key: "history-divider" }, ...pluginContextOptions.value]
    : []),
]);
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

function selectedRowKeys(): readonly (string | number)[] {
  return (tabulator.value?.getSelectedData() ?? []).flatMap((row) =>
    typeof row.rowKey === "string" || typeof row.rowKey === "number" ? [row.rowKey] : [],
  );
}

const insertRowDisabled = computed(() =>
  !tableStore.revision?.schemaRevision
  || !tableStore.editSchema?.some((column) => column.editable));

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

function openPluginContextMenu(payload: { rowKey: string | number; field?: string; x: number; y: number }): void {
  pluginContextMenu.value = { show: true, ...payload, field: payload.field ?? null };
}

function selectPluginContextAction(key: string): void {
  const rowKey = pluginContextMenu.value.rowKey;
  const field = pluginContextMenu.value.field;
  pluginContextMenu.value.show = false;
  if (rowKey !== null && key === "history:cell" && field) {
    revisionHistoryService.open({ scope: "cell", itemId: String(rowKey), field });
    return;
  }
  if (rowKey !== null && key === "history:row") {
    revisionHistoryService.open({ scope: "row", itemId: String(rowKey) });
    return;
  }
  if (rowKey !== null) void openRegisteredPluginAction(key, [rowKey]);
}

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
  revisionHistory.setSelection(payload.scope === "row"
    ? { scope: "row", itemId: String(payload.rowKey) }
    : { scope: "cell", itemId: String(payload.rowKey), field: payload.field });
}

function openCurrentHistory(): void {
  const selected = revisionHistory.selection;
  if (selected.scope === "multiple") return;
  revisionHistoryService.open({ ...selected, scope: selected.scope });
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

let viewMounted = false;
let businessConsumersInitialized = false;

function initializeBusinessConsumers(): void {
  if (
    businessConsumersInitialized
    || !viewMounted
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
  relationLookupService.init(() => tableService.refresh());
  pasteService.init();
  mutationService.init((error) => {
    if (error.kind !== "cancelled") editRejection.value = error;
  });
  tableAdminService.init();
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

onMounted(() => {
  viewMounted = true;
  window.addEventListener("beforeunload", onBeforeUnload);
  initializeBusinessConsumers();
});

onBeforeUnmount(() => {
  viewMounted = false;
  unregisterWorkspaceEpochReset();
  formulaPreviewCoordinator.dispose();
  tableService.dispose();
  relationLookupService.dispose();
  revisionHistoryService.invalidate();
  pluginService.dispose();
  dashboardService.dispose();
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

function openRelationEditor(payload: {
  rowKey: string | number;
  field: string;
  descriptor: NormalizedRelationDescriptor;
  value: unknown;
}): void {
  if (!relationLookup.capabilities?.relationEditV1) {
    message.error(t("workspace.relation.unsupported"));
    return;
  }
  const current = normalizeTargets(payload.value).map((target) => ({
    ...target,
    collection: target.collection || payload.descriptor.relatedCollection || "",
  }));
  relationEditor.value = {
    show: true,
    rowKey: payload.rowKey,
    field: payload.field,
    descriptor: payload.descriptor,
    candidates: [],
    query: "",
    m2aCollection: payload.descriptor.kind === "m2a"
      ? payload.descriptor.allowedCollections[0] ?? null
      : payload.descriptor.relatedCollection ?? null,
    loading: false,
    applying: false,
    error: null,
  };
  if (payload.descriptor.kind === "m2o") {
    relationLookup.openDraft(payload.descriptor.relationId, String(payload.rowKey), current);
    void searchRelationTargets("");
    return;
  }
  relationEditor.value.loading = true;
  void relationLookupService
    .loadDraft(payload.descriptor.relationId, String(payload.rowKey))
    .then(() => searchRelationTargets(""))
    .catch((error: unknown) => {
      relationEditor.value.loading = false;
      relationEditor.value.error = error instanceof Error ? error.message : String(error);
    });
}

async function searchRelationTargets(query: string, collection?: string | null): Promise<void> {
  const descriptor = relationEditor.value.descriptor;
  if (!descriptor) return;
  const targetCollection = collection ?? relationEditor.value.m2aCollection;
  if (descriptor.kind === "m2a" && !targetCollection) return;
  const generation = ++relationSearchGeneration;
  relationEditor.value.query = query;
  relationEditor.value.loading = true;
  relationEditor.value.error = null;
  try {
    const result = await relationLookupService.searchTargets({
      relationId: descriptor.relationId,
      query,
      collection: descriptor.kind === "m2a" ? targetCollection : null,
      offset: 0,
      limit: 50,
    });
    if (generation !== relationSearchGeneration || !relationEditor.value.show) return;
    relationEditor.value.candidates = result.items;
  } catch (error) {
    if (generation !== relationSearchGeneration) return;
    relationEditor.value.error = error instanceof Error ? error.message : String(error);
  } finally {
    if (generation === relationSearchGeneration) relationEditor.value.loading = false;
  }
}

async function selectRelationTarget(target: RelationTargetRef): Promise<void> {
  const descriptor = relationEditor.value.descriptor;
  const rowKey = relationEditor.value.rowKey;
  if (!descriptor || rowKey === null) return;
  if (descriptor.kind !== "m2o") {
    relationLookup.toggleDraftTarget(target);
    return;
  }
  relationEditor.value.applying = true;
  relationEditor.value.error = null;
  try {
    const result = await relationLookupService.updateSingle(descriptor.relationId, String(rowKey), target);
    if (result.outcome !== "committed") {
      throw new Error(t("workspace.relation.changed"));
    }
    tableStore.applyRelationValue(rowKey, relationEditor.value.field, result.current);
    closeRelationEditor();
  } catch (error) {
    relationEditor.value.error = error instanceof Error ? error.message : String(error);
  } finally {
    relationEditor.value.applying = false;
  }
}

async function clearSingleRelation(): Promise<void> {
  const descriptor = relationEditor.value.descriptor;
  const rowKey = relationEditor.value.rowKey;
  if (!descriptor || rowKey === null) return;
  relationEditor.value.applying = true;
  try {
    const result = await relationLookupService.updateSingle(descriptor.relationId, String(rowKey), null);
    if (result.outcome !== "committed") {
      throw new Error(t("workspace.relation.changed"));
    }
    tableStore.applyRelationValue(rowKey, relationEditor.value.field, null);
    closeRelationEditor();
  } catch (error) {
    relationEditor.value.error = error instanceof Error ? error.message : String(error);
  } finally {
    relationEditor.value.applying = false;
  }
}

function patchRelationJunction(target: RelationTargetRef, field: string, value: string): void {
  relationLookup.patchDraftJunction(target, { ...target.junctionValues, [field]: value });
}

async function applyRelationDraft(): Promise<void> {
  const rowKey = relationEditor.value.rowKey;
  if (rowKey === null) return;
  relationEditor.value.applying = true;
  relationEditor.value.error = null;
  try {
    const result = await relationLookupService.applyDraft();
    if (result.outcome !== "committed") {
      throw new Error(t("workspace.relation.changed"));
    }
    tableStore.applyRelationValue(rowKey, relationEditor.value.field, result.current);
    closeRelationEditor();
  } catch (error) {
    relationEditor.value.error = error instanceof Error ? error.message : String(error);
  } finally {
    relationEditor.value.applying = false;
  }
}

function changeM2ACollection(collection: string): void {
  relationEditor.value.m2aCollection = collection;
  void searchRelationTargets(relationEditor.value.query, collection);
}

function closeRelationEditor(): void {
  relationSearchGeneration += 1;
  relationEditor.value.show = false;
  relationLookup.closeDraft();
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

function refreshTable(): void {
  editRejection.value = null;
  tableService.refresh();
  if (workspace.currentTable) void relationLookupService.loadContext(workspace.currentTable);
}

async function importTableData(): Promise<void> {
  const collection = workspace.currentTable;
  const schemaRevision = tableStore.revision?.schemaRevision;
  if (!collection || !schemaRevision) return;
  try {
    const result = await dataIoService.importData(collection, schemaRevision);
    if (result) {
      message.success(t("dataIo.import.success", {
        count: result.createdCount + result.updatedCount,
      }));
      refreshTable();
    }
  } catch (error) {
    message.error(error instanceof Error ? error.message : String(error));
  }
}

async function exportTableData(): Promise<void> {
  const collection = workspace.currentTable;
  if (!collection) return;
  try {
    const result = await dataIoService.exportData(collection, {});
    message.success(t("dataIo.export.success", {
      count: result.rowsWritten,
      name: result.outputDisplayName,
    }));
  } catch (error) {
    message.error(error instanceof Error ? error.message : String(error));
  }
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
  if (ui.activeView === "dashboard") return t("nav.dashboard");
  if (ui.activeView === "conflicts") return t("workspaceV2.nav.conflicts");
  return t("nav.tables");
});

const showWorkspaceCenterScreen = computed(() =>
  workspaceSession.enabled
  && (showWorkspaceCenter.value || !workspaceSession.hasOpenWorkspace));

const unregisterWorkspaceEpochReset = registerWorkspaceEpochReset(
  "workspace-view-v1-consumers",
  ({ nextWorkspaceId }) => {
    workspace.clear();
    tableStore.reset();
    history.clear();
    documentWorkspace.clear();
    revisionHistory.reset();
    relationLookup.reset();
    dashboards.reset();
    dashboardDraft.stop();
    presetViews.clearPresets();
    realtime.reset();
    ui.setWorkspaceNamespace(nextWorkspaceId);
  },
);

async function handleWorkspaceV2Action(action: WorkspaceV2UiAction): Promise<void> {
  if (!workspaceSession.enabled || !workspaceProtection.beginOperation(action.method)) return;
  try {
    await requestWorkspaceV2UiAction(action);
    workspaceProtection.finishOperation();
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    workspaceProtection.finishOperation(message);
    if (action.method === "workspace.open" || action.method === "workspace.switch") {
      workspaceSession.failSwitch(message);
    }
  }
}

function openWorkspace(workspaceId: string): void {
  if (workspaceId === workspaceSession.activeWorkspaceId) {
    showWorkspaceCenter.value = false;
    return;
  }
  if (!workspaceSession.isTransitioning) workspaceSession.beginSwitch(workspaceId);
  showWorkspaceCenter.value = false;
  if (workspaceSession.activeWorkspaceId) {
    void handleWorkspaceV2Action({
      method: "workspace.switch",
      params: { targetWorkspaceId: workspaceId, openMode: "writable" },
    });
  } else {
    void handleWorkspaceV2Action({
      method: "workspace.open",
      params: { workspaceId, openMode: "writable" },
    });
  }
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
 * undo re-inserts the rows. The digest is issued by QueryPort and checked
 * atomically by MutationKernel; a row without one must never be deleted.
 */
function onDelete() {
  const range = activeRange();
  if (!range) return;
  const rows = range
    .getRows()
    .map((row) => {
      const data = row.getData() as {
        rowKey: number | string;
        __vibetableDigest?: unknown;
      };
      return {
        rowKey: data.rowKey,
        expectedDigest: typeof data.__vibetableDigest === "string"
          ? data.__vibetableDigest
          : "",
      };
    });
  if (rows.length === 0 || rows.some((row) => !isProductDigest(row.expectedDigest))) return;
  mutationService.deleteRows(rows);
}

function isProductDigest(value: string): boolean {
  return /^sha256:[0-9a-f]{64}$/u.test(value);
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
  const columns = (grid?.getColumns?.() ?? []).filter((column) =>
    column.getField() !== "rowKey" && column.getField() !== ROW_NUMBER_FIELD,
  );
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
      @navigate="onNavigate"
      @open-admin="onOpenAdmin"
      @open-help="ui.openShortcuts"
    />
    <section class="app-surface" :class="`density-${ui.density}`">
      <header class="app-header">
        <div class="app-title">
          <WorkspaceSwitcher
            v-if="workspaceSession.enabled"
            @switch="openWorkspace"
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
          @action="handleWorkspaceV2Action"
          @open="openWorkspace($event.workspaceId)"
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
            @request-delete="onRequestDelete"
          />
          <main class="main">
            <AppToolbar
              :plugin-actions="toolbarPluginActions"
              :history-scope-label="historyScopeLabel"
              :history-disabled="revisionHistory.selection.scope === 'multiple'"
              :insert-row-disabled="insertRowDisabled"
              :data-io-busy="dataIoService.busy.value"
              @select-table="onSelect"
              @refresh="refreshTable"
              @insert-row="mutationService.insertRow({})"
              @open-help="ui.openShortcuts"
              @open-history="openCurrentHistory"
              @open-archived-history="revisionHistoryService.open({ scope: 'archived' })"
              @open-field-manager="openFieldManager"
              @import-data="importTableData"
              @export-data="exportTableData"
              @cancel-data-task="dataIoService.cancelActive"
              @plugin-action="openRegisteredPluginAction"
            />
            <DataSourceViewBar
              v-if="workspace.currentTable"
              :collection="workspace.currentTable"
              :views="presetViews.presets"
              :active-id="presetViews.activePresetId"
              :loading="presetViews.loading"
              :dirty="presetViews.dirty"
              @create="createView"
              @switch="switchView"
              @save="saveView"
              @duplicate="duplicateView"
              @rename="renameView"
              @delete="deleteView"
              @set-default="setDefaultView"
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
            <GridHost
              v-else
              :on-cell-edited="onCellEdited"
              :insert-row-disabled="insertRowDisabled"
              @selection-change="onHistorySelection"
              :on-validation-error="onValidationError"
              @row-context="openPluginContextMenu"
              @relation-edit="openRelationEditor"
              @attachment-open="openAttachmentPanel"
              @json-edit="openJsonEditor"
              @lookup-source="navigateLookupSource"
              @view-query-change="onGridViewQueryChanged"
              @insert-first-row="mutationService.insertRow({})"
            />
            <div v-if="workspace.currentTable && tableStore.datasetReady" class="table-summary" data-testid="table-summary">
              {{ t("toolbar.rowCount", { count: tableStore.rowCount }) }}
            </div>
          </main>
        </div>
        <DashboardWorkspaceView v-if="!showWorkspaceCenterScreen && ui.activeView === 'dashboard' && dashboards.featureEnabled" />
        <SettingsView
          v-show="!showWorkspaceCenterScreen && ui.activeView === 'settings'"
          @reconnect="openDatabaseWithGuard"
          @open-help="ui.openShortcuts"
          @open-admin="onOpenAdmin"
          @load-mappings="identifierMappingService.load()"
          @save-mapping-aliases="identifierMappingService.updateAliases"
          @reconcile-mappings="identifierMappingService.reconcile"
          @workspace-v2-action="handleWorkspaceV2Action"
        />
        <FileWorkspaceView
          v-if="!showWorkspaceCenterScreen && ui.activeView === 'files'"
          @intent="documentWorkspaceService.dispatch"
          @workspace-v2-action="handleWorkspaceV2Action"
        />
        <ConflictCenterView
          v-if="!showWorkspaceCenterScreen && ui.activeView === 'conflicts' && workspaceSession.conflictEnabled"
          @action="handleWorkspaceV2Action"
        />
        <PluginCenterView v-if="!showWorkspaceCenterScreen && ui.activeView === 'plugins'" />
      </div>
    </section>
    <RelationEditorPanel
      v-if="relationEditor.show"
      :show="relationEditor.show"
      :descriptor="relationEditor.descriptor"
      :selected="relationLookup.draft?.selected ?? []"
      :candidates="relationEditor.candidates"
      :query="relationEditor.query"
      :m2a-collection="relationEditor.m2aCollection"
      :loading="relationEditor.loading"
      :applying="relationEditor.applying"
      :error="relationEditor.error"
      @close="closeRelationEditor"
      @search="searchRelationTargets"
      @select="selectRelationTarget"
      @clear="clearSingleRelation"
      @patch-junction="patchRelationJunction"
      @apply="applyRelationDraft"
      @collection-change="changeM2ACollection"
    />
    <NModal
      :show="attachmentPanel.show"
      :auto-focus="true"
      :trap-focus="true"
      :close-on-esc="true"
      :mask-closable="true"
      @update:show="show => { if (!show) closeAttachmentPanel() }"
      @after-leave="finishAttachmentPanelClose"
    >
      <aside
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
          <NButton size="tiny" quaternary @click="closeAttachmentPanel">
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
          @upload="uploadAttachments"
          @replace="replaceAttachment"
          @remove="removeAttachment"
          @preview="previewAttachment"
          @download="downloadAttachment"
        />
      </aside>
    </NModal>
    <NModal
      :show="jsonEditor.show"
      :auto-focus="true"
      :trap-focus="true"
      :close-on-esc="true"
      :mask-closable="true"
      @update:show="show => { if (!show) closeJsonEditor() }"
      @after-leave="finishJsonEditorClose"
    >
      <aside
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
            @click="closeJsonEditor"
          >{{ t("common.close") }}</NButton>
        </header>
        <JsonValueEditor
          :model-value="jsonEditor.value"
          @update:model-value="jsonEditor.value = $event"
          @validity-changed="jsonEditor.valid = $event"
        />
        <footer>
          <NButton size="small" @click="closeJsonEditor">{{ t("common.cancel") }}</NButton>
          <NButton
            type="primary"
            size="small"
            :disabled="!jsonEditor.valid"
            data-testid="json-editor-save"
            @click="commitJsonEdit"
          >{{ t("workspace.json.save") }}</NButton>
        </footer>
      </aside>
    </NModal>
    <FieldManagerDrawer
      v-if="workspace.currentTable"
      :show="fieldManager.show"
      :collection="workspace.currentTable"
      :collections="workspace.collections.map(item => item.collection)"
      :schema="relationLookup.schema"
      :schemas="Object.values(fieldManager.schemas)"
      :lookups="relationLookup.lookups"
      :lookup-catalog="Object.values(fieldManager.lookupCatalog).flat()"
      :busy="fieldManager.busy"
      :error="fieldManager.error"
      :relation-plan="fieldManager.relationPlan"
      :lookup-validation="fieldManager.lookupValidation"
      :lookup-preview="fieldManager.lookupPreview"
      @close="closeFieldManager"
      @reset-relation-preview="fieldManager.relationPlan = null"
      @preview-relation="previewRelationChange"
      @apply-relation="applyRelationChange"
      @validate-lookup="validateLookupDefinition"
      @preview-lookup="previewLookupDefinition"
      @create-lookup="mutateLookup('create', $event)"
      @update-lookup="mutateLookup('update', $event)"
      @delete-lookup="mutateLookup('delete', $event)"
      @load-schema="loadFieldManagerSchema"
    />
    <PastePanel @confirm="onConfirmPaste" @cancel="onCancelPaste" />
    <CreateTableModal
      :formula-previews="formulaPreviews"
      @formula-preview="previewDraftFormula"
      @submit="onSubmitCreate"
      @cancel="onCancelCreate"
    />
    <DeleteConfirmModal @confirm="onConfirmDelete" @cancel="onCancelDelete" />
    <ShortcutsView />
    <RevisionHistoryDrawer
      :field-options="historyFieldOptions"
      @close="revisionHistoryService.close"
      @reload="revisionHistoryService.refresh"
      @load-more="revisionHistoryService.loadMore"
      @preview="revisionHistoryService.previewRestore"
      @apply="revisionHistoryService.applyRestore"
    />
    <NDropdown
      trigger="manual"
      placement="bottom-start"
      :show="pluginContextMenu.show"
      :x="pluginContextMenu.x"
      :y="pluginContextMenu.y"
      :options="gridContextOptions"
      @update:show="show => { pluginContextMenu.show = show }"
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
