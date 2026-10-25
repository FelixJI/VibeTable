import { computed, nextTick, onScopeDispose, ref, watch, type ComputedRef, type Ref } from "vue";

import type {
  FilterExpression,
  GridState,
  GroupCondition,
  PresetEntry,
  PresetView,
  SortCondition,
  SummaryCondition,
  TableQuery,
} from "@/contracts";
import {
  applyDataSourceView,
  captureDataSourceView,
  type DataSourceViewGridSource,
} from "@/grid/dataSourceViewState";
import { isRowIdFieldName, reconcileState } from "@/grid/gridState";
import { createGridPresentationPersistence, type GridPresentationService } from "@/services/gridPresentationService";
import type { usePresetVersionService } from "@/services/presetVersionService";
import type { usePresetVersionStore } from "@/stores/presetVersionStore";
import type { useTableStore } from "@/stores/tableStore";
import type { useUiStore } from "@/stores/uiStore";
import { cloneFilterExpressions, defaultVisibleFields, type useViewQueryStore } from "@/stores/viewQueryStore";
import type { useWorkspaceStore } from "@/stores/workspaceStore";

type PresetStore = ReturnType<typeof usePresetVersionStore>;
type ViewQueryStore = ReturnType<typeof useViewQueryStore>;
type TableStore = ReturnType<typeof useTableStore>;
type UiStore = ReturnType<typeof useUiStore>;
type WorkspaceStore = ReturnType<typeof useWorkspaceStore>;
type PresetService = ReturnType<typeof usePresetVersionService>;

interface PersistOutcome {
  readonly saved: PresetEntry | null;
  readonly failure?: unknown;
}

type PresetStorePort = Pick<PresetStore,
  | "presets" | "activePresetId" | "loading" | "dirty"
  | "begin" | "receivePresets" | "activatePreset" | "upsertPreset"
  | "fail" | "markSaved" | "markDirty" | "clearPresets" | "removePreset">;
type ViewQueryPort = Pick<ViewQueryStore,
  | "filters" | "sorts" | "groups" | "summaries" | "collapsedGroupKeys"
  | "search" | "visibleFields" | "toQuery" | "replace" | "reset"
  | "updateRuntime" | "updateDefinition" | "toggleGroup">;
type TablePort = Pick<TableStore, "allRows" | "schema" | "viewGroups">;
type UiPort = Pick<UiStore, "density" | "setDensity">;
type WorkspacePort = Pick<WorkspaceStore, "currentTable">;
export type PresetServicePort = Pick<PresetService,
  "listPresets" | "savePreset" | "deletePreset">;

export interface PresetViewDefinition {
  readonly filters: readonly FilterExpression[];
  readonly groups: readonly GroupCondition[];
  readonly summaries: readonly SummaryCondition[];
  readonly visibleFields: readonly string[];
}

export interface CreatePresetViewRequest {
  readonly name: string;
  readonly kind: "table" | "calendar" | "timeline" | "kanban" | "gallery";
  readonly dateField: string | null;
  readonly endDateField: string | null;
  readonly titleField: string | null;
  readonly groupField: string | null;
  readonly coverField: string | null;
}

export type PresetViewIntent =
  | { readonly type: "presentation.changed" }
  | { readonly type: "presentation.reload" }
  | { readonly type: "keyword.changed"; readonly keyword: string }
  | { readonly type: "density.changed"; readonly density: "compact" | "comfortable" }
  | { readonly type: "column.frozen"; readonly field: string; readonly frozen: boolean }
  | {
    readonly type: "runtime.changed";
    readonly query: {
      readonly headerFilters: readonly FilterExpression[];
      readonly sorts: readonly SortCondition[];
      readonly groups: readonly GroupCondition[];
    };
  }
  | { readonly type: "definition.changed"; readonly definition: PresetViewDefinition }
  | { readonly type: "groups.loadMore" }
  | { readonly type: "group.toggle"; readonly key: string }
  | { readonly type: "view.create"; readonly request: CreatePresetViewRequest }
  | { readonly type: "view.switch"; readonly view: PresetEntry }
  | { readonly type: "view.reset" }
  | { readonly type: "view.save"; readonly view: PresetEntry }
  | { readonly type: "view.duplicate"; readonly view: PresetEntry; readonly name: string }
  | { readonly type: "view.rename"; readonly view: PresetEntry; readonly name: string }
  | { readonly type: "view.delete"; readonly view: PresetEntry }
  | { readonly type: "view.setDefault"; readonly view: PresetEntry }
  | { readonly type: "view.reload" };

interface FieldOption {
  readonly label: string;
  readonly value: string;
}

export interface PresetViewController {
  readonly presentationError: Readonly<Ref<string>>;
  readonly presentationLoading: ComputedRef<boolean>;
  readonly frozenFields: ComputedRef<readonly string[]>;
  restoreGrid(): Promise<void>;
  flush(): Promise<void>;
  readonly activeView: ComputedRef<PresetView | null>;
  readonly activeKind: ComputedRef<NonNullable<PresetView["kind"]>>;
  readonly projectedRows: ComputedRef<readonly Readonly<Record<string, unknown>>[]>;
  readonly dateFields: ComputedRef<readonly FieldOption[]>;
  readonly titleFields: ComputedRef<readonly FieldOption[]>;
  readonly groupFields: ComputedRef<readonly FieldOption[]>;
  readonly coverFields: ComputedRef<readonly FieldOption[]>;
  dispatch(intent: PresetViewIntent): Promise<PresetEntry | null | void>;
}

export interface PresetViewDependencies {
  readonly presentation?: {
    readonly service: GridPresentationService;
    readonly identity: Readonly<Ref<string | null>>;
  };
  readonly workspace: WorkspacePort;
  readonly table: TablePort;
  readonly ui: UiPort;
  readonly presets: PresetStorePort;
  readonly query: ViewQueryPort;
  readonly service: PresetServicePort;
  readonly grid: DataSourceViewGridSource;
  readonly executeQuery: (table: string, query: TableQuery) => void;
  readonly refreshLookups: () => void;
  readonly reportError: (error: unknown) => void;
  /** Localized "default change incomplete" notice; `cause` keeps the original failure for diagnostics. */
  readonly defaultChangeIncompleteError: (cause: unknown) => Error;
}

export function createPresetViewController(
  dependencies: PresetViewDependencies,
): PresetViewController {
  const pendingView = ref<PresetView | null>(null);
  const presentedView = ref<PresetView | null>(null);
  const presentationError = ref("");
  const presentationQueryReady = ref(false);
  let pendingRestore: { view: PresetView; local: GridState | null } | null = null;
  let initializing = true;
  let forcedRemote = false;
  let legacyCozy = false;
  // 已知当前表字段集：schema 增量时区分新字段（默认可见，行 ID 除外）与
  // 既有字段（保持用户显式隐藏）。在 restoreLoaded/applyView/schema watcher
  // 同步；切表时随 currentTable watcher 清空。
  let trackedSchemaFields = new Set<string>();
  const memoryDefaults = new Map<string, PresetView>();
  let loadGeneration = 0;
  let applying = false;
  const persistence = dependencies.presentation ? createGridPresentationPersistence(
    dependencies.presentation.service,
    () => dependencies.presentation!.identity.value,
    error => { presentationError.value = error.message; },
  ) : null;
  const frozenFields = computed(() => (presentedView.value?.columns ?? [])
    .filter(column => column.frozen).map(column => column.name));
  const presentationLoading = computed(() => !!dependencies.workspace.currentTable
    && (dependencies.presets.loading || !presentedView.value || !!pendingView.value || !presentationQueryReady.value));
  onScopeDispose(() => { loadGeneration++; persistence?.retire(); });

  const activeView = computed(() => dependencies.presets.presets
    .find(item => item.id === dependencies.presets.activePresetId)?.view ?? null);
  const activeKind = computed(() => activeView.value?.kind ?? "table");
  const projectedRows = computed(() => dependencies.table.allRows);
  const dateFields = computed(() => (dependencies.table.schema ?? [])
    .filter(column => column.dataType === "date" || column.dataType === "datetime")
    .map(column => ({ label: column.title, value: column.name })));
  const titleFields = computed(() => (dependencies.table.schema ?? [])
    .filter(column => column.dataType === "text")
    .map(column => ({ label: column.title, value: column.name })));
  const groupFields = computed(() => (dependencies.table.schema ?? [])
    .filter(column => (
      column.kind !== "attachment"
      && column.kind !== "relation"
      && column.kind !== "lookup"
      && (column.dataType === "text" || column.dataType === "integer" || column.dataType === "boolean")
    ))
    .map(column => ({ label: column.title, value: column.name })));
  const coverFields = computed(() => (dependencies.table.schema ?? [])
    .filter(column => column.kind === "attachment" || column.dataType === "text")
    .map(column => ({ label: column.title, value: column.name })));

  function captureTable(isDefault = false): PresetView {
    const captured = captureDataSourceView(
      dependencies.grid.current.value,
      { isDefault, density: legacyCozy && dependencies.ui.density === "comfortable" ? "cozy" : dependencies.ui.density },
    );
    return {
      ...captured,
      columns: captured.columns?.map(column => ({
        ...column,
        visible: dependencies.query.visibleFields.includes(column.name),
      })),
      filters: cloneFilterExpressions(dependencies.query.filters),
      sorts: [...dependencies.query.sorts],
      groups: [...dependencies.query.groups],
      summaries: [...dependencies.query.summaries],
      collapsedGroupKeys: [...dependencies.query.collapsedGroupKeys],
      search: dependencies.query.search,
      visibleFields: [...dependencies.query.visibleFields],
    };
  }

  function captureCurrent(isDefault = false): PresetView {
    const base = activeView.value && activeKind.value !== "table"
      ? activeView.value
      : captureTable(isDefault);
    let columns = base.columns;
    const schemaFields = (dependencies.table.schema ?? []).map(column => column.name);
    if (columns?.length) {
      // 与 captureTable 同一做法按当前选择同步列可见性，并补齐当前 schema
      // 缺失的列：新字段被显式隐藏后不得因缺席而永远被当作新增默认显示。
      const known = new Set(columns.map(column => column.name));
      const nextOrder = columns.length;
      const missing = schemaFields
        .filter(name => !known.has(name))
        .map((name, index) => ({
          name, order: nextOrder + index,
          visible: dependencies.query.visibleFields.includes(name),
          frozen: false, width: null,
        }));
      columns = [...columns, ...missing].map(column => ({
        ...column,
        visible: dependencies.query.visibleFields.includes(column.name),
      }));
    } else if (schemaFields.length) {
      // 原布局为空的合法视图在 schema 已知时物化完整列（按当前选择写
      // visible），避免本地持久空 columns 后重载把全部字段当作新增全显。
      columns = schemaFields.map((name, order) => ({
        name, order,
        visible: dependencies.query.visibleFields.includes(name),
        frozen: false, width: null,
      }));
    }
    return {
      ...base,
      columns,
      filters: cloneFilterExpressions(dependencies.query.filters),
      sorts: [...dependencies.query.sorts],
      groups: [...dependencies.query.groups],
      summaries: [...dependencies.query.summaries],
      collapsedGroupKeys: [...dependencies.query.collapsedGroupKeys],
      search: dependencies.query.search,
      visibleFields: [...dependencies.query.visibleFields],
      isDefault,
    };
  }

  function savePresentation(): void {
    // pendingRestore 暂存期间不得把旧 grid/query 捕获成待应用视图的布局。
    if (initializing || applying || pendingView.value || pendingRestore
      || !dependencies.workspace.currentTable
      || !dependencies.grid.current.value
      || dependencies.grid.current.value.initialized === false) return;
    const view = captureCurrent();
    presentedView.value = view;
    const active = dependencies.presets.presets.find(item => item.id === dependencies.presets.activePresetId);
    persistence?.save({
      columns: view.columns ?? [], sorts: view.sorts,
      filters: cloneFilterExpressions(view.filters), keyword: view.search || null,
      density: legacyCozy && dependencies.ui.density === "comfortable" ? "cozy" : dependencies.ui.density,
      forcedRemote,
      presetId: active?.id ?? null, presetRevision: active?.revision ?? null,
    });
  }

  async function restoreGrid(): Promise<void> {
    const grid = dependencies.grid.current.value;
    const view = pendingView.value ?? presentedView.value;
    if (!grid || grid.initialized === false || !view || initializing) return;
    const generation = loadGeneration;
    applying = true;
    try {
      await applyDataSourceView(grid, view);
      await nextTick();
      if (generation === loadGeneration) pendingView.value = null;
    } catch (error) {
      if (generation === loadGeneration) presentationError.value = error instanceof Error ? error.message : String(error);
    } finally { if (generation === loadGeneration) applying = false; }
  }

  async function restoreLoaded(): Promise<void> {
    const schema = dependencies.table.schema;
    if (!schema || !pendingRestore) return;
    trackedSchemaFields = new Set(schema.map(column => column.name));
    const { view: baseline, local } = pendingRestore;
    pendingRestore = null;
    const state = local ? reconcileState(local, schema) : null;
    const view: PresetView = state ? {
      ...baseline, columns: state.columns,
      filters: cloneFilterExpressions(state.filters), sorts: [...state.sorts],
      search: state.keyword ?? "", density: state.density,
      visibleFields: [...state.columns.filter(column => column.visible !== false).map(column => column.name), ...defaultVisibleFields(state.newlyAdded)],
    } : baseline;
    initializing = false;
    await applyView(view);
  }

  function requestAuthoritative(groupOffset = 0): void {
    const table = dependencies.workspace.currentTable;
    if (!table) return;
    dependencies.executeQuery(table, dependencies.query.toQuery(groupOffset));
    dependencies.refreshLookups();
    presentationQueryReady.value = true;
  }

  async function applyView(view: PresetView): Promise<void> {
    const collection = dependencies.workspace.currentTable;
    if (!collection) return;
    // 带列布局的视图可能与当前 schema 脱节：用同一 reconcileState 语义
    // 对照，已知列保持原可见性、新增列默认可见、已删列剪退；对当前
    // schema 捕获的视图幂等。无列布局保持既有 visibleFields 语义。
    const schema = dependencies.table.schema;
    // beginLoad 期间完整视图也没有列布局；此时应用并保存会把保留网格
    // 按空 visibleFields 捕获为全隐藏。所有视图等待 schema 后再应用。
    if (!schema) {
      pendingRestore = { view, local: null };
      return;
    }
    if (view.columns?.length) {
      const state = reconcileState({
        columns: view.columns, sorts: [], filters: [], keyword: null,
        density: view.density ?? "comfortable", forcedRemote: false,
      }, schema);
      // 已知字段以顶层 visibleFields 为权威（兼容历史保存的 columns 与顶层
      // 不一致状态）；columns 集合用于识别新增字段补默认可见，并把
      // columns.visible 同步为最终选择，供 applyDataSourceView 与后续保存一致。
      const covered = new Set(state.columns.map(column => column.name));
      const selected = new Set(view.visibleFields);
      const visibleFields = schema
        .map(column => column.name)
        .filter(name => selected.has(name) || (!covered.has(name) && !isRowIdFieldName(name)));
      view = {
        ...view,
        columns: state.columns.map(column => ({ ...column, visible: visibleFields.includes(column.name) })),
        visibleFields,
      };
    }
    // 应用新视图即接管最新意图：清掉可能遗留的未知 schema 暂存。
    pendingRestore = null;
    presentedView.value = view;
    const generation = loadGeneration;
    legacyCozy = view.density === "cozy";
    if (view.density) dependencies.ui.setDensity(view.density === "cozy" ? "comfortable" : view.density);
    // 应用视图即确认当前 schema 为已知基线（覆盖 view.reload 等非恢复路径）。
    const schemaFields = (dependencies.table.schema ?? []).map(column => column.name);
    trackedSchemaFields = new Set(schemaFields);
    dependencies.query.replace(collection, view, schemaFields);
    if (view.kind && view.kind !== "table") {
      pendingView.value = null;
      requestAuthoritative();
      return;
    }
    pendingView.value = view;
    await restoreGrid();
    if (generation !== loadGeneration || collection !== dependencies.workspace.currentTable) return;
    requestAuthoritative();
  }

  async function loadCollection(collection: string, preserveActive = false, restoreLocal = false): Promise<void> {
    const generation = ++loadGeneration;
    presentationQueryReady.value = false;
    const requestedActiveId = preserveActive ? dependencies.presets.activePresetId : null;
    dependencies.presets.begin();
    try {
      const [result, local] = await Promise.all([
        dependencies.service.listPresets(collection),
        restoreLocal ? persistence?.open(collection) ?? Promise.resolve(null) : Promise.resolve(null),
      ]);
      if (generation !== loadGeneration || dependencies.workspace.currentTable !== collection) return;
      dependencies.presets.receivePresets(result);
      const selected = result.presets.find(view => view.id === (requestedActiveId ?? local?.presetId))
        ?? (restoreLocal && local?.presetId === null ? undefined : result.presets.find(view => view.view.isDefault));
      dependencies.presets.activatePreset(selected?.id ?? null);
      const baseline = selected?.view ?? memoryDefaults.get(collection) ?? {
        kind: "table", layout: "table", columns: [], filters: [], sorts: [], search: "",
        visibleFields: defaultVisibleFields((dependencies.table.schema ?? []).map(column => column.name)),
      };
      if (restoreLocal) forcedRemote = local?.forcedRemote ?? false;
      const matches = (local?.presetId ?? null) === (selected?.id ?? null)
        && (local?.presetRevision ?? null) === (selected?.revision ?? null);
      if (restoreLocal) {
        pendingRestore = { view: baseline, local: matches ? local : null };
        await restoreLoaded();
      } else {
        initializing = false;
        await applyView(baseline);
        if (generation !== loadGeneration || dependencies.workspace.currentTable !== collection) return;
        savePresentation();
      }
    } catch (error) {
      if (generation === loadGeneration) {
        dependencies.presets.fail(error);
        dependencies.reportError(error);
      }
    }
  }

  async function persistTracked(
    collection: string,
    name: string,
    view: PresetView,
    target: Pick<PresetEntry, "id" | "revision"> | null = null,
  ): Promise<PersistOutcome> {
    if (dependencies.workspace.currentTable !== collection) return { saved: null };
    const generation = loadGeneration;
    dependencies.presets.begin();
    try {
      const saved = await dependencies.service.savePreset(collection, name, view, target);
      if (generation !== loadGeneration || dependencies.workspace.currentTable !== collection) return { saved: null };
      dependencies.presets.upsertPreset(saved);
      return { saved };
    } catch (error) {
      if (generation === loadGeneration && dependencies.workspace.currentTable === collection) {
        dependencies.presets.fail(error);
      }
      return { saved: null, failure: error };
    }
  }

  async function persist(
    collection: string,
    name: string,
    view: PresetView,
    target: Pick<PresetEntry, "id" | "revision"> | null = null,
  ): Promise<PresetEntry | null> {
    return (await persistTracked(collection, name, view, target)).saved;
  }

  function saveTarget(view: PresetEntry): Pick<PresetEntry, "id" | "revision"> {
    return { id: view.id, revision: view.revision };
  }

  async function save(view: PresetEntry): Promise<PresetEntry | null> {
    const collection = dependencies.workspace.currentTable;
    if (!collection || view.collection !== collection) return null;
    const generation = loadGeneration;
    const saved = await persist(collection, view.name, {
      ...captureCurrent(view.view.isDefault),
      isDefault: view.view.isDefault,
    }, saveTarget(view));
    if (generation !== loadGeneration || collection !== dependencies.workspace.currentTable) return null;
    if (saved) {
      dependencies.presets.markSaved();
      savePresentation();
    }
    return saved;
  }

  async function setDefault(view: PresetEntry): Promise<void> {
    const generation = loadGeneration;
    const stillCurrent = () => generation === loadGeneration
      && view.collection === dependencies.workspace.currentTable;
    const previous = dependencies.presets.presets.find(
      item => item.view.isDefault && item.id !== view.id,
    );
    const source = view.id === dependencies.presets.activePresetId
      ? captureCurrent(true)
      : { ...view.view, isDefault: true };
    const saved = await persistTracked(view.collection, view.name, source, saveTarget(view));
    if (!saved.saved || !stillCurrent()) return;
    if (previous) {
      const demoted = await persistTracked(previous.collection, previous.name, {
        ...previous.view,
        isDefault: false,
      }, saveTarget(previous));
      if (!stillCurrent()) return;
      if (!demoted.saved) {
        // No automatic compensation: a failed demotion (even a definite CAS
        // rejection — another process may already have demoted the old
        // default) never proves the authoritative default state, and a third
        // write could leave both views non-default. The committed promotion
        // stays; surface the localized notice until an explicit reload.
        if (demoted.failure !== undefined) {
          dependencies.presets.fail(dependencies.defaultChangeIncompleteError(demoted.failure));
        }
        return;
      }
    }
    dependencies.presets.activatePreset(saved.saved.id);
  }

  watch(
    () => dependencies.table.schema,
    (columns) => {
      if (pendingRestore) {
        void restoreLoaded();
        return;
      }
      const collection = dependencies.workspace.currentTable;
      if (!collection || !columns) return;
      const fields = columns.map(column => column.name);
      const visible = new Set(dependencies.query.visibleFields);
      if (trackedSchemaFields.size === 0) {
        // 首次见到该表 schema：维持既有初始化语义。
        if (presentedView.value) dependencies.query.replace(collection, presentedView.value, fields);
        else dependencies.query.reset(collection, fields);
      } else {
        // 增量：新字段默认可见（行 ID 除外），既有字段保持原状（含显式
        // 隐藏与隐藏全部），已删字段清退。
        dependencies.query.visibleFields = fields.filter(name =>
          visible.has(name) || (!trackedSchemaFields.has(name) && !isRowIdFieldName(name)));
      }
      trackedSchemaFields = new Set(fields);
    },
  );

  watch(dependencies.grid.current, (grid) => {
    const collection = dependencies.workspace.currentTable;
    if (!grid || !collection) return;
    if (pendingView.value) void restoreGrid();
    else if (dependencies.presets.presets.length === 0 && !memoryDefaults.has(collection)) {
      memoryDefaults.set(collection, captureCurrent());
    }
  });

  watch(
    [() => dependencies.workspace.currentTable, () => dependencies.presentation?.identity.value],
    ([collection, identity], previous) => {
      const generation = ++loadGeneration;
      if (previous?.[1] !== identity) memoryDefaults.clear();
      persistence?.retire();
      initializing = true;
      presentationQueryReady.value = false;
      applying = false;
      presentationError.value = "";
      presentedView.value = null;
      pendingRestore = null;
      pendingView.value = null;
      trackedSchemaFields = new Set();
      dependencies.presets.clearPresets(collection ?? "");
      dependencies.query.reset(collection ?? "");
      void nextTick().then(() => {
        if (generation !== loadGeneration || !collection) return;
        if (dependencies.presentation && !dependencies.presentation.identity.value) return;
        void loadCollection(collection, false, true);
      });
    },
    { immediate: true, flush: "sync" },
  );
  watch(() => dependencies.ui.density, () => savePresentation());

  async function dispatch(intent: PresetViewIntent): Promise<PresetEntry | null | void> {
    const generation = loadGeneration;
    const collection = dependencies.workspace.currentTable;
    const stillCurrent = () => generation === loadGeneration
      && collection === dependencies.workspace.currentTable;
    switch (intent.type) {
      case "presentation.changed":
        if (!applying && !initializing && dependencies.grid.current.value) {
          dependencies.query.visibleFields = (captureDataSourceView(dependencies.grid.current.value).columns ?? [])
            .filter(column => column.visible !== false).map(column => column.name);
        }
        savePresentation();
        return;
      case "presentation.reload": {
        const collection = dependencies.workspace.currentTable;
        if (collection) {
          presentationError.value = "";
          initializing = true;
          await loadCollection(collection, true, true);
        }
        return;
      }
      case "keyword.changed":
        if (initializing || applying) return;
        dependencies.query.search = intent.keyword;
        dependencies.presets.markDirty();
        requestAuthoritative();
        savePresentation();
        return;
      case "density.changed":
        if (initializing || applying) return;
        legacyCozy = false;
        dependencies.ui.setDensity(intent.density);
        savePresentation();
        return;
      case "column.frozen": {
        if (initializing || applying) return;
        const view = captureCurrent();
        await applyView({ ...view, columns: view.columns?.map(column => column.name === intent.field
          ? { ...column, frozen: intent.frozen } : column) });
        if (!stillCurrent()) return;
        savePresentation();
        return;
      }
      case "runtime.changed": {
        const table = dependencies.workspace.currentTable;
        if (!table || applying || initializing) return;
        dependencies.query.updateRuntime(intent.query);
        dependencies.presets.markDirty();
        requestAuthoritative();
        savePresentation();
        return;
      }
      case "definition.changed": {
        if (initializing || applying) return;
        dependencies.query.updateDefinition(intent.definition);
        dependencies.presets.markDirty();
        const grid = dependencies.grid.current.value;
        if (grid && activeKind.value === "table") {
          applying = true;
          try {
            await applyDataSourceView(grid, captureTable());
            if (!stillCurrent()) return;
            await nextTick();
          } finally {
            if (stillCurrent()) applying = false;
          }
        }
        if (!stillCurrent()) return;
        requestAuthoritative();
        savePresentation();
        return;
      }
      case "groups.loadMore":
        requestAuthoritative(dependencies.table.viewGroups.length);
        return;
      case "group.toggle":
        dependencies.query.toggleGroup(intent.key);
        dependencies.presets.markDirty();
        return;
      case "view.save":
        return await save(intent.view);
      case "view.reset": {
        if (!collection || dependencies.presets.loading) return;
        const current = dependencies.presets.presets.find(item => item.id === dependencies.presets.activePresetId);
        if (current && dependencies.presets.dirty && !(await save(current))) return;
        if (!stillCurrent()) return;
        dependencies.presets.activatePreset(null);
        const fields = (dependencies.table.schema ?? []).map(column => column.name);
        const visible = defaultVisibleFields(fields);
        await applyView({
          kind: "table", layout: "table", filters: [], sorts: [], search: "",
          groups: [], summaries: [], visibleFields: visible,
          columns: fields.map((name, order) => ({ name, order, visible: visible.includes(name), frozen: false, width: null })),
        });
        if (stillCurrent()) savePresentation();
        return;
      }
      case "view.switch": {
        if (
          intent.view.collection !== dependencies.workspace.currentTable
          || dependencies.presets.loading
          || intent.view.id === dependencies.presets.activePresetId
        ) return;
        const current = dependencies.presets.presets.find(
          item => item.id === dependencies.presets.activePresetId,
        );
        await persistence?.flush();
        if (!stillCurrent()) return;
        if (current && dependencies.presets.dirty && !(await save(current))) return;
        if (!stillCurrent()) return;
        dependencies.presets.activatePreset(intent.view.id);
        await applyView(intent.view.view);
        if (!stillCurrent()) return;
        savePresentation();
        return;
      }
      case "view.create": {
        const collection = dependencies.workspace.currentTable;
        if (!collection) return;
        const view: PresetView = {
          ...captureTable(false),
          kind: intent.request.kind,
          layout: intent.request.kind,
          dateField: intent.request.dateField,
          endDateField: intent.request.endDateField,
          titleField: intent.request.titleField,
          groupField: intent.request.groupField,
          coverField: intent.request.coverField,
        };
        const saved = await persist(collection, intent.request.name, view);
        if (!stillCurrent()) return;
        if (saved) {
          dependencies.presets.activatePreset(saved.id);
          await applyView(saved.view);
          if (!stillCurrent()) return;
          savePresentation();
        }
        return;
      }
      case "view.duplicate": {
        const collection = dependencies.workspace.currentTable;
        if (!collection) return;
        const saved = await persist(collection, intent.name, {
          ...intent.view.view,
          isDefault: false,
        });
        if (!saved || !stillCurrent()) return;
        dependencies.presets.activatePreset(saved.id);
        await applyView(saved.view);
        if (!stillCurrent()) return;
        savePresentation();
        return;
      }
      case "view.rename": {
        const source = intent.view.id === dependencies.presets.activePresetId
          ? { ...captureCurrent(intent.view.view.isDefault), isDefault: intent.view.view.isDefault }
          : intent.view.view;
        const saved = await persist(
          intent.view.collection,
          intent.name,
          source,
          saveTarget(intent.view),
        );
        if (saved && stillCurrent()) dependencies.presets.activatePreset(saved.id);
        return;
      }
      case "view.delete": {
        const view = intent.view;
        if (view.collection !== dependencies.workspace.currentTable || dependencies.presets.loading) return;
        const generation = loadGeneration;
        dependencies.presets.begin();
        try {
          await dependencies.service.deletePreset(view.id, view.revision);
        } catch (error) {
          if (generation === loadGeneration && dependencies.workspace.currentTable === view.collection) {
            dependencies.presets.fail(error);
          }
          return;
        }
        if (generation !== loadGeneration || dependencies.workspace.currentTable !== view.collection) return;
        dependencies.presets.removePreset(view.id);
        const next = dependencies.presets.presets.find(item => item.view.isDefault)
          ?? dependencies.presets.presets[0];
        dependencies.presets.activatePreset(next?.id ?? null);
        if (next) await applyView(next.view);
        else {
          const fallback = memoryDefaults.get(view.collection);
          if (fallback) await applyView(fallback);
        }
        return;
      }
      case "view.setDefault":
        await setDefault(intent.view);
        return;
      case "view.reload": {
        const collection = dependencies.workspace.currentTable;
        if (collection) await loadCollection(collection, true);
        return;
      }
    }
  }

  return {
    presentationError,
    presentationLoading,
    frozenFields,
    restoreGrid,
    flush: () => persistence?.flush() ?? Promise.resolve(),
    activeView,
    activeKind,
    projectedRows,
    dateFields,
    titleFields,
    groupFields,
    coverFields,
    dispatch,
  };
}
