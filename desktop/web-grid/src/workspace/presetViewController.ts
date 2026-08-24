import { computed, nextTick, ref, watch, type ComputedRef } from "vue";

import type {
  FilterExpression,
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
  | "presets" | "activePresetId" | "loading"
  | "begin" | "receivePresets" | "activatePreset" | "upsertPreset"
  | "fail" | "markSaved" | "markDirty" | "clearPresets" | "removePreset">;
type ViewQueryPort = Pick<ViewQueryStore,
  | "filters" | "sorts" | "groups" | "summaries" | "collapsedGroupKeys"
  | "search" | "visibleFields" | "toQuery" | "replace" | "reset"
  | "updateRuntime" | "updateDefinition" | "toggleGroup">;
type TablePort = Pick<TableStore, "allRows" | "schema" | "viewGroups">;
type UiPort = Pick<UiStore, "density">;
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
  const memoryDefaults = new Map<string, PresetView>();
  let loadGeneration = 0;
  let applying = false;

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
      { isDefault, density: dependencies.ui.density },
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

  function requestAuthoritative(groupOffset = 0): void {
    const table = dependencies.workspace.currentTable;
    if (!table) return;
    dependencies.executeQuery(table, dependencies.query.toQuery(groupOffset));
    dependencies.refreshLookups();
  }

  async function applyView(view: PresetView): Promise<void> {
    const collection = dependencies.workspace.currentTable;
    if (!collection) return;
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
    const grid = dependencies.grid.current.value;
    if (!grid) {
      pendingView.value = view;
      return;
    }
    pendingView.value = null;
    applying = true;
    try {
      await applyDataSourceView(grid, view);
      await nextTick();
    } finally {
      applying = false;
    }
    requestAuthoritative();
  }

  async function loadCollection(collection: string, preserveActive = false): Promise<void> {
    const generation = ++loadGeneration;
    const requestedActiveId = preserveActive ? dependencies.presets.activePresetId : null;
    dependencies.presets.begin();
    try {
      const result = await dependencies.service.listPresets(collection);
      if (generation !== loadGeneration || dependencies.workspace.currentTable !== collection) return;
      dependencies.presets.receivePresets(result);
      const selected = result.presets.find(view => view.id === requestedActiveId)
        ?? result.presets.find(view => view.view.isDefault)
        ?? result.presets[0];
      dependencies.presets.activatePreset(selected?.id ?? null);
      if (selected) await applyView(selected.view);
      else if (dependencies.grid.current.value && !memoryDefaults.has(collection)) {
        memoryDefaults.set(collection, captureCurrent());
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
    if (saved) dependencies.presets.markSaved();
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
      const collection = dependencies.workspace.currentTable;
      if (!collection || !columns || dependencies.query.visibleFields.length > 0) return;
      const fields = columns.map(column => column.name);
      if (activeView.value) dependencies.query.replace(collection, activeView.value, fields);
      else dependencies.query.reset(collection, fields);
    },
  );

  watch(dependencies.grid.current, (grid) => {
    const collection = dependencies.workspace.currentTable;
    if (!grid || !collection) return;
    if (pendingView.value) void applyView(pendingView.value);
    else if (dependencies.presets.presets.length === 0 && !memoryDefaults.has(collection)) {
      memoryDefaults.set(collection, captureCurrent());
    }
  });

  watch(
    () => dependencies.workspace.currentTable,
    (collection) => {
      pendingView.value = null;
      dependencies.presets.clearPresets(collection ?? "");
      dependencies.query.reset(collection ?? "");
      if (collection) void loadCollection(collection);
    },
    { immediate: true },
  );

  async function dispatch(intent: PresetViewIntent): Promise<PresetEntry | null | void> {
    switch (intent.type) {
      case "runtime.changed": {
        const table = dependencies.workspace.currentTable;
        if (!table || applying) return;
        dependencies.query.updateRuntime(intent.query);
        dependencies.presets.markDirty();
        requestAuthoritative();
        return;
      }
      case "definition.changed": {
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
        if (current && !(await save(current))) return;
        dependencies.presets.activatePreset(intent.view.id);
        await applyView(intent.view.view);
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
        if (saved) dependencies.presets.activatePreset(saved.id);
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
