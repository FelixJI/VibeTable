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
import { reconcileState } from "@/grid/gridState";
import { createGridPresentationPersistence, type GridPresentationService } from "@/services/gridPresentationService";
import type { usePresetVersionService } from "@/services/presetVersionService";
import type { usePresetVersionStore } from "@/stores/presetVersionStore";
import type { useTableStore } from "@/stores/tableStore";
import type { useUiStore } from "@/stores/uiStore";
import { cloneFilterExpressions, type useViewQueryStore } from "@/stores/viewQueryStore";
import type { useWorkspaceStore } from "@/stores/workspaceStore";

type PresetStore = ReturnType<typeof usePresetVersionStore>;
type ViewQueryStore = ReturnType<typeof useViewQueryStore>;
type TableStore = ReturnType<typeof useTableStore>;
type UiStore = ReturnType<typeof useUiStore>;
type WorkspaceStore = ReturnType<typeof useWorkspaceStore>;
type PresetService = ReturnType<typeof usePresetVersionService>;

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
  readonly defaultCompensationError: () => Error;
}

export function createPresetViewController(
  dependencies: PresetViewDependencies,
): PresetViewController {
  const pendingView = ref<PresetView | null>(null);
  const presentedView = ref<PresetView | null>(null);
  const presentationError = ref("");
  let pendingRestore: { view: PresetView; local: GridState | null } | null = null;
  let initializing = true;
  let forcedRemote = false;
  let legacyCozy = false;
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
    && (dependencies.presets.loading || !presentedView.value || !!pendingView.value));
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
    return {
      ...base,
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
    if (initializing || applying || pendingView.value || !dependencies.workspace.currentTable
      || !dependencies.grid.current.value || dependencies.grid.current.value.initialized === false) return;
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
    const { view: baseline, local } = pendingRestore;
    pendingRestore = null;
    const state = local ? reconcileState(local, schema) : null;
    const view: PresetView = state ? {
      ...baseline, columns: state.columns,
      filters: cloneFilterExpressions(state.filters), sorts: [...state.sorts],
      search: state.keyword ?? "", density: state.density,
      visibleFields: [...state.columns.filter(column => column.visible !== false).map(column => column.name), ...state.newlyAdded],
    } : baseline;
    initializing = false;
    await applyView(view);
  }

  function requestAuthoritative(groupOffset = 0): void {
    const table = dependencies.workspace.currentTable;
    if (!table) return;
    dependencies.executeQuery(table, dependencies.query.toQuery(groupOffset));
    dependencies.refreshLookups();
  }

  async function applyView(view: PresetView): Promise<void> {
    const collection = dependencies.workspace.currentTable;
    if (!collection) return;
    presentedView.value = view;
    const generation = loadGeneration;
    legacyCozy = view.density === "cozy";
    if (view.density) dependencies.ui.setDensity(view.density === "cozy" ? "comfortable" : view.density);
    dependencies.query.replace(
      collection,
      view,
      (dependencies.table.schema ?? []).map(column => column.name),
    );
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
        ?? result.presets.find(view => view.view.isDefault)
        ?? result.presets[0];
      dependencies.presets.activatePreset(selected?.id ?? null);
      const baseline = selected?.view ?? memoryDefaults.get(collection) ?? {
        kind: "table", layout: "table", columns: [], filters: [], sorts: [], search: "",
        visibleFields: (dependencies.table.schema ?? []).map(column => column.name),
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
        savePresentation();
      }
    } catch (error) {
      if (generation === loadGeneration) {
        dependencies.presets.fail(error);
        dependencies.reportError(error);
      }
    }
  }

  async function persist(
    collection: string,
    name: string,
    view: PresetView,
    target: Pick<PresetEntry, "id" | "revision"> | null = null,
  ): Promise<PresetEntry | null> {
    if (dependencies.workspace.currentTable !== collection) return null;
    const generation = loadGeneration;
    dependencies.presets.begin();
    try {
      const saved = await dependencies.service.savePreset(collection, name, view, target);
      if (generation !== loadGeneration || dependencies.workspace.currentTable !== collection) return null;
      dependencies.presets.upsertPreset(saved);
      return saved;
    } catch (error) {
      if (generation === loadGeneration && dependencies.workspace.currentTable === collection) {
        dependencies.presets.fail(error);
      }
      return null;
    }
  }

  function saveTarget(view: PresetEntry): Pick<PresetEntry, "id" | "revision"> {
    return { id: view.id, revision: view.revision };
  }

  async function save(view: PresetEntry): Promise<PresetEntry | null> {
    const collection = dependencies.workspace.currentTable;
    if (!collection || view.collection !== collection) return null;
    const saved = await persist(collection, view.name, {
      ...captureCurrent(view.view.isDefault),
      isDefault: view.view.isDefault,
    }, saveTarget(view));
    if (saved) {
      dependencies.presets.markSaved();
      savePresentation();
    }
    return saved;
  }

  async function setDefault(view: PresetEntry): Promise<void> {
    const previous = dependencies.presets.presets.find(
      item => item.view.isDefault && item.id !== view.id,
    );
    const source = view.id === dependencies.presets.activePresetId
      ? captureCurrent(true)
      : { ...view.view, isDefault: true };
    const saved = await persist(view.collection, view.name, source, saveTarget(view));
    if (!saved) return;
    if (previous) {
      const demoted = await persist(previous.collection, previous.name, {
        ...previous.view,
        isDefault: false,
      }, saveTarget(previous));
      if (!demoted && dependencies.workspace.currentTable === view.collection) {
        const compensated = await persist(view.collection, view.name, {
          ...source,
          isDefault: false,
        }, saveTarget(saved));
        if (!compensated) {
          dependencies.presets.fail(dependencies.defaultCompensationError());
          return;
        }
        await loadCollection(view.collection);
        return;
      }
    }
    dependencies.presets.activatePreset(saved.id);
  }

  watch(
    () => dependencies.table.schema,
    (columns) => {
      if (pendingRestore) { void restoreLoaded(); return; }
      const collection = dependencies.workspace.currentTable;
      if (!collection || !columns || dependencies.query.visibleFields.length > 0) return;
      const fields = columns.map(column => column.name);
      if (presentedView.value) dependencies.query.replace(collection, presentedView.value, fields);
      else dependencies.query.reset(collection, fields);
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
      applying = false;
      presentationError.value = "";
      presentedView.value = null;
      pendingRestore = null;
      pendingView.value = null;
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
            await nextTick();
          } finally {
            applying = false;
          }
        }
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
        if (current && dependencies.presets.dirty && !(await save(current))) return;
        dependencies.presets.activatePreset(intent.view.id);
        await applyView(intent.view.view);
        savePresentation();
        return;
      }
      case "view.create": {
        const collection = dependencies.workspace.currentTable;
        if (!collection) return;
        const view: PresetView = {
          ...captureTable(dependencies.presets.presets.length === 0),
          kind: intent.request.kind,
          layout: intent.request.kind,
          dateField: intent.request.dateField,
          endDateField: intent.request.endDateField,
          titleField: intent.request.titleField,
          groupField: intent.request.groupField,
          coverField: intent.request.coverField,
        };
        const saved = await persist(collection, intent.request.name, view);
        if (saved) {
          dependencies.presets.activatePreset(saved.id);
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
        if (!saved) return;
        dependencies.presets.activatePreset(saved.id);
        await applyView(saved.view);
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
        if (saved) dependencies.presets.activatePreset(saved.id);
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
