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
import { computed, nextTick, onBeforeUnmount, onMounted, provide, ref, watch } from "vue";
import { useMessage } from "naive-ui";
import { NButton, NDropdown, NIcon } from "naive-ui";
import { FilePlus2 } from "lucide-vue-next";
import type { TabulatorFull } from "tabulator-tables";
import AppNavigation from "@/components/layout/AppNavigation.vue";
import AppSidebar from "@/components/layout/AppSidebar.vue";
import AppToolbar from "@/components/layout/AppToolbar.vue";
import ConnectionPill from "@/components/feedback/ConnectionPill.vue";
import GridHost from "@/components/grid/GridHost.vue";
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
import RevisionHistoryDrawer from "@/components/history/RevisionHistoryDrawer.vue";
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
import { useRevisionHistoryService } from "@/services/revisionHistoryService";
import { useRelationLookupService } from "@/services/relationLookupService";
import { buildAuthoritativeLookupViewQuery } from "@/services/relationLookupQuery";
import { useKeyboard } from "@/composables/useKeyboard";
import { useUiStore } from "@/stores/uiStore";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { usePasteStore } from "@/stores/pasteStore";
import { useTableStore } from "@/stores/tableStore";
import { useHistoryStore } from "@/stores/historyStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { usePluginStore } from "@/stores/pluginStore";
import { useRevisionHistoryStore } from "@/stores/revisionHistoryStore";
import { useRelationLookupStore } from "@/stores/relationLookupStore";
import { ROW_NUMBER_FIELD } from "@/grid/createGrid";
import {
  classifyClipboard,
  mapCellsToColumns,
  parseClipboard,
} from "@/grid/clipboardParser";
import { resolvePasteContext } from "@/grid/pasteContext";
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
} from "@/contracts";
import { normalizeTargets } from "@/grid/relationLookupRenderer";
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
const revisionHistoryService = useRevisionHistoryService();
const relationLookupService = useRelationLookupService();
const ui = useUiStore();
const admin = useTableAdminStore();
const paste = usePasteStore();
const tableStore = useTableStore();
const history = useHistoryStore();
const workspace = useWorkspaceStore();
const plugins = usePluginStore();
const revisionHistory = useRevisionHistoryStore();
const relationLookup = useRelationLookupStore();

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
  const requestGeneration = ++lookupDatasetGeneration;
  const fieldRefs = columns.map((column) => column.fieldId ?? `${collection}.${column.name}`);
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
    message.error(error instanceof Error ? error.message : String(error));
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
  hostBridge.notify("table.queryRequested", {
    table,
    // Standard table.query is page-bounded (backend max 500) but compiles the
    // sort/filter on Directus, so the page is selected from the full dataset.
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
    message.success("关系结构已更新");
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
    message.success(operation === "delete" ? "Lookup 已删除" : "Lookup 已保存");
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
      message.success(`已定位来源记录 ${navigation.source.collection} · ${navigation.source.itemId}`);
      lookupSourceNavigation.value = null;
    } catch {
      message.warning(`已筛选来源记录 ${navigation.source.collection} · ${navigation.source.itemId}`);
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
    label: `${label} · ${action.risk === "read" ? "只读" : action.risk === "write" ? "写入" : "危险"}`,
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

onMounted(() => {
  // Subscribe each service to its inbound host events. Idempotent across
  // strict-mode double-mount in dev because each bridge.on replaces prior
  // handlers for the same key (see hostBridge).
  workspaceService.init();
  tableService.init();
  relationLookupService.init(() => tableService.refresh());
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

onBeforeUnmount(() => {
  relationLookupService.dispose();
  revisionHistoryService.invalidate();
  pluginService.dispose();
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
    message.error("当前环境不支持关系编辑");
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
    if (result.outcome !== "committed") throw new Error("关系记录已变化，请刷新后重试");
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
    if (result.outcome !== "committed") throw new Error("关系记录已变化，请刷新后重试");
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
    if (result.outcome !== "committed") throw new Error("关系记录已变化，请刷新后重试");
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
              :history-scope-label="historyScopeLabel"
              :history-disabled="revisionHistory.selection.scope === 'multiple'"
              @refresh="refreshTable"
              @insert-row="mutationService.insertRow({})"
              @open-help="ui.openShortcuts"
              @open-history="openCurrentHistory"
              @open-archived-history="revisionHistoryService.open({ scope: 'archived' })"
              @open-field-manager="openFieldManager"
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
              @selection-change="onHistorySelection"
              :on-validation-error="onValidationError"
              @row-context="openPluginContextMenu"
              @relation-edit="openRelationEditor"
              @lookup-source="navigateLookupSource"
              @view-query-change="onGridViewQueryChanged"
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
    <CreateTableModal @submit="onSubmitCreate" @cancel="onCancelCreate" />
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
