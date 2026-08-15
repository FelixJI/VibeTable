import { BridgeOperationError } from "@/bridge/hostBridge";
import {
  BindingRuntime,
  enforceManifestMinimum,
  getDashboardTemplate,
  type Dashboard,
  type DashboardPanel,
  type DashboardTemplateId,
  validatePanelManifest,
} from "@/dashboard";
import {
  cloneDashboardData,
  useDashboardDraftStore,
  useDashboardStore,
  type DashboardManagedConfig,
} from "@/stores/dashboardStore";
import type {
  DashboardManagedConfigPayload,
  DashboardPanelQueryPayload,
  DashboardPanelType,
  FilterCondition,
  SortCondition,
} from "@/contracts";
import { useHostBridge } from "./bridgeContext";
import { getLocale } from "@/i18n";
import { inject, provide, toRaw, type InjectionKey } from "vue";
import { DashboardQueryExecutor, DashboardSchemaCatalog } from "./dashboardBindingPorts";

interface QueryResultWire {
  readonly rows?: readonly Record<string, unknown>[];
  readonly truncated?: boolean;
  readonly maxPoints?: number;
}

export interface DashboardDrilldownResult {
  readonly rows: readonly Record<string, unknown>[];
  readonly truncated: boolean;
}

interface SaveResultWire {
  readonly workspace?: unknown;
  readonly clientPanelIds?: Readonly<Record<string, string>>;
}
interface DeleteResultWire { readonly deleted?: string }

const DASHBOARD_MEMORY_POINT_LIMIT = 250_000;

export function useDashboardService() {
  const bridge = useHostBridge();
  const store = useDashboardStore();
  const draft = useDashboardDraftStore();
  const queue = new DashboardQueryQueue(6);
  const schemaCatalog = new DashboardSchemaCatalog(bridge);
  const bindingRuntime = new BindingRuntime(schemaCatalog, new DashboardQueryExecutor(bridge));
  let generation = 0;
  let refreshTimer: number | null = null;
  let realtimeTimer: number | null = null;
  let disposed = false;
  let visiblePanelIds: Set<string> | null = null;
  const unsubscribe: Array<() => void> = [];
  const activeQueryRequestIds = new Set<string>();
  const activePanelControllers = new Map<string, AbortController>();
  const panelGenerations = new Map<string, number>();

  function init(): void {
    unsubscribe.push(bridge.on("database.opened", () => {
      schemaCatalog.invalidate();
      store.reset();
      void loadManifest();
      void list();
    }));
    unsubscribe.push(bridge.on("data.changed", (payload) => {
      if (!store.current || document.hidden) return;
      const collection = payload.tableId;
      schemaCatalog.invalidate(collection);
      if (collection === "vibetable_dashboards" || collection === "vibetable_panels" ||
          collection === "vibetable_dashboard_configs" || dependsOnCollection(collection)) {
        store.markAllStale();
        if (realtimeTimer !== null) window.clearTimeout(realtimeTimer);
        realtimeTimer = window.setTimeout(() => void refresh(), 350);
      }
    }));
    window.addEventListener("online", onOnline);
    window.addEventListener("offline", onOffline);
    document.addEventListener("visibilitychange", onVisibilityChange);
  }

  function dispose(): void {
    disposed = true;
    generation += 1;
    cancelActiveQueries();
    queue.clear();
    stopRefreshTimer();
    if (realtimeTimer !== null) window.clearTimeout(realtimeTimer);
    window.removeEventListener("online", onOnline);
    window.removeEventListener("offline", onOffline);
    document.removeEventListener("visibilitychange", onVisibilityChange);
    for (const stop of unsubscribe.splice(0)) stop();
  }

  async function list(): Promise<void> {
    store.beginList();
    try {
      const result = await bridge.request("dashboard.listRequested", {});
      if (!disposed) store.receiveList(result);
    } catch (error) {
      store.fail(errorMessage(error));
    }
  }

  async function loadManifest(): Promise<void> {
    try {
      const result = await bridge.request("dashboard.manifestRequested", {});
      if (!disposed) store.receiveManifest(result);
    } catch (error) {
      store.fail(errorMessage(error));
    }
  }

  async function select(dashboardId: string): Promise<void> {
    generation += 1;
    cancelActiveQueries();
    queue.clear();
    visiblePanelIds = null;
    store.beginLoad();
    const selectedGeneration = generation;
    try {
      const result = await bridge.request("dashboard.readRequested", { dashboardId });
      if (disposed || selectedGeneration !== generation) return;
      store.receiveWorkspace(result);
      draft.stop();
      configureRefreshTimer();
      await queryAllPanels(selectedGeneration);
    } catch (error) {
      if (selectedGeneration === generation) store.fail(errorMessage(error));
    }
  }

  async function refresh(): Promise<void> {
    if (!store.current || (store.phase !== "ready" && store.phase !== "failed") ||
        store.offline || document.hidden) return;
    generation += 1;
    cancelActiveQueries();
    queue.clear();
    const refreshGeneration = generation;
    store.beginRefresh();
    store.markAllStale();
    await queryAllPanels(refreshGeneration);
  }

  function beginEdit(): void {
    if (store.current) draft.begin(store.current, store.config, store.revision);
  }

  function describeCollection(collectionId: string, signal: AbortSignal) {
    return schemaCatalog.describe(collectionId, signal);
  }

  function discardEdit(): void {
    draft.stop();
  }

  function createFromTemplate(templateId: DashboardTemplateId, name: string): void {
    const template = getDashboardTemplate(templateId);
    if (template.panels.some((panel) => !store.allowedPanelTypes.includes(panel.type))) {
      store.fail("The dashboard template contains a panel type disabled by the runtime manifest.");
      return;
    }
    const dashboardId = `draft:${crypto.randomUUID()}`;
    const panels = template.panels.map((item): DashboardPanel => {
      const id = `draft:${crypto.randomUUID()}`;
      return {
        id,
        dashboardId,
        name: item.title[getLocale()],
        type: item.type,
        rawType: item.type,
        productType: item.type,
        editable: true,
        position: enforceManifestMinimum(item.position, store.panelManifest[item.type]),
        options: {},
        query: {},
        rawOptions: {},
        rawQuery: {},
      };
    });
    const dashboard: Dashboard = { id: dashboardId, name, note: "", panels };
    draft.begin(dashboard, {
      configVersion: 1,
      globalFilters: [],
      interactions: [],
      refreshInterval: 0,
    }, null);
    draft.dirty = true;
  }

  function copyCurrent(name: string): number {
    if (!store.current) return 0;
    const dashboardId = `draft:${crypto.randomUUID()}`;
    const supported = store.current.panels.filter((panel) => panel.editable);
    const skipped = store.current.panels.length - supported.length;
    const panelIds: Record<string, string> = {};
    const panels = supported.map((panel) => {
      const id = `draft:${crypto.randomUUID()}`;
      panelIds[panel.id] = id;
      return {
        ...cloneDashboardData(toRaw(panel)),
        id,
        dashboardId,
      };
    });
    const config = remapManagedConfig(cloneDashboardData(toRaw(store.config)), panelIds);
    draft.begin({ ...cloneDashboardData(toRaw(store.current)), id: dashboardId, name, panels }, config, null);
    draft.dirty = true;
    return skipped;
  }

  async function save(): Promise<void> {
    const source = draft.draft;
    if (!source || !draft.dirty) return;
    const invalid = source.panels.filter((panel) => panel.editable).flatMap((panel) =>
      panel.productType === "custom" || panel.productType === "unknown"
        ? []
        : validatePanelManifest(panel, store.panelManifest[panel.productType]));
    if (invalid.length > 0) {
      store.fail(invalid.map((item) => item.message).join(" "));
      return;
    }
    store.beginSave();
    const isNew = source.id.startsWith("draft:");
    const payload = {
      dashboardId: isNew ? null : source.id,
      expectedRevision: isNew ? null : draft.baseRevision,
      idempotencyKey: crypto.randomUUID(),
      name: source.name,
      note: source.note,
      panels: source.panels.filter((panel) => panel.editable).map((panel) => ({
        clientId: panel.id,
        panelId: panel.id.startsWith("draft:") ? null : panel.id,
        name: panel.name,
        type: editableType(panel.productType),
        position: panel.position,
        note: panel.note,
        icon: panel.icon,
        color: panel.color,
        showHeader: panel.showHeader !== false,
        options: panel.options,
        query: toWireDashboardQuery(panel),
      })),
      deletedPanelIds: [...draft.deletedPanelIds],
      config: draft.config,
    };
    try {
      const result = await bridge.request("dashboard.saveRequested", payload) as SaveResultWire;
      if (!result.workspace) throw new Error("Dashboard save returned no workspace.");
      store.receiveWorkspace(rewriteWorkspaceReferences(result.workspace, result.clientPanelIds ?? {}));
      draft.stop();
      configureRefreshTimer();
      await list();
      await queryAllPanels(generation);
    } catch (error) {
      if (error instanceof BridgeOperationError && error.code === "dashboard_edit_conflict") {
        draft.setConflict(error.message);
      }
      store.fail(errorMessage(error));
    }
  }

  async function deleteCurrent(): Promise<void> {
    const dashboardId = store.current?.id;
    if (!dashboardId || dashboardId.startsWith("draft:")) return;
    store.beginSave();
    try {
      const result = await bridge.request("dashboard.deleteRequested", { dashboardId }) as DeleteResultWire;
      if (result.deleted !== dashboardId) throw new Error("Dashboard delete returned an invalid result.");
      generation += 1;
      cancelActiveQueries();
      queue.clear();
      stopRefreshTimer();
      draft.stop();
      store.reset();
      await Promise.all([loadManifest(), list()]);
    } catch (error) {
      store.fail(errorMessage(error));
    }
  }

  async function queryAllPanels(expectedGeneration = generation): Promise<void> {
    const dashboard = store.current;
    if (!dashboard || expectedGeneration !== generation) return;
    const queryable = visiblePanelIds
      ? dashboard.panels.filter((panel) => visiblePanelIds!.has(panel.id))
      : dashboard.panels;
    const tasks = queryable.map((panel, index) => queue.enqueue(
      () => queryPanel(panel, expectedGeneration, store.config),
      index,
    ));
    await Promise.allSettled(tasks);
    if (expectedGeneration === generation) store.lastRefreshAt = Date.now();
  }

  async function queryPanel(
    panel: DashboardPanel,
    expectedGeneration: number,
    runtimeConfig: DashboardManagedConfigPayload,
    expectedPanelGeneration?: number,
  ): Promise<void> {
    if (expectedPanelGeneration !== undefined && panelGenerations.get(panel.id) !== expectedPanelGeneration) return;
    if (!panel.editable || Object.keys(panel.query).length === 0) return;
    const baseQuery = toWireDashboardQuery(panel);
    if (!baseQuery) return;
    store.setPanelState(panel.id, { state: "loading", error: null });
    const controller = new AbortController();
    activePanelControllers.get(panel.id)?.abort(new DOMException("Superseded", "AbortError"));
    activePanelControllers.set(panel.id, controller);
    const filtered = withRuntimeFilters(baseQuery, panel.id, runtimeConfig, store.sessionFilterValues);
    const baseFilterCount = baseQuery.filters?.length ?? 0;
    const result = await bindingRuntime.evaluate({
      panelId: panel.id,
      panelType: editableType(panel.productType),
      query: baseQuery,
    }, {
      limits: store.limits,
      runtimeFilters: (filtered.filters ?? []).slice(baseFilterCount),
    }, controller.signal);
    const activePanels = draft.editing ? draft.draft?.panels : store.current?.panels;
    if (expectedGeneration !== generation ||
        (expectedPanelGeneration !== undefined && panelGenerations.get(panel.id) !== expectedPanelGeneration) ||
        activePanelControllers.get(panel.id) !== controller ||
        !activePanels?.some((item) => item.id === panel.id)) return;
    activePanelControllers.delete(panel.id);
    if (result.state === "cancelled") return;
    if (result.state === "drift") {
      store.setPanelState(panel.id, {
        state: "failed",
        error: result.diagnostics.map((item) => item.message).join(" "),
      });
      return;
    }
    if (result.state === "error") {
      store.setPanelState(panel.id, { state: "failed", error: result.error.message });
      return;
    }
    const occupied = Object.entries(store.panelData)
      .filter(([id]) => id !== panel.id)
      .reduce((count, [id, item]) => {
        const occupiedPanel = activePanels.find((candidate) => candidate.id === id);
        return count + item.rows.length * (occupiedPanel ? panelPointWeight(occupiedPanel) : 1);
      }, 0);
    const remainingPoints = Math.max(0, DASHBOARD_MEMORY_POINT_LIMIT - occupied);
    const rows = result.rows.slice(0, Math.floor(remainingPoints / panelPointWeight(panel)));
    store.setPanelState(panel.id, {
      state: "ready",
      rows,
      truncated: result.truncated || rows.length < result.rows.length,
      maxPoints: result.maxPoints,
      updatedAt: Date.now(),
      error: null,
    });
  }

  function cancelActiveQueries(): void {
    for (const controller of activePanelControllers.values()) {
      controller.abort(new DOMException("Cancelled", "AbortError"));
    }
    activePanelControllers.clear();
    for (const requestId of activeQueryRequestIds) {
      bridge.notify("dashboard.cancelRequested", { targetRequestId: requestId });
    }
    activeQueryRequestIds.clear();
  }

  function configureRefreshTimer(): void {
    stopRefreshTimer();
    const seconds = store.config.refreshInterval;
    if (seconds === 0 || store.offline || document.hidden) return;
    refreshTimer = window.setInterval(() => void refresh(), seconds * 1000);
  }

  function stopRefreshTimer(): void {
    if (refreshTimer !== null) window.clearInterval(refreshTimer);
    refreshTimer = null;
  }

  function onOnline(): void {
    store.offline = false;
    configureRefreshTimer();
    void refresh();
  }

  function onOffline(): void {
    store.offline = true;
    generation += 1;
    cancelActiveQueries();
    queue.clear();
    stopRefreshTimer();
    store.markAllStale();
  }

  function onVisibilityChange(): void {
    if (document.hidden) {
      generation += 1;
      cancelActiveQueries();
      queue.clear();
      stopRefreshTimer();
    }
    else {
      configureRefreshTimer();
      void refresh();
    }
  }

  function dependsOnCollection(collection: string): boolean {
    return (store.current?.panels ?? []).some((panel) => panel.query.collection === collection);
  }

  function selectPanelValue(panel: DashboardPanel, value: unknown): void {
    store.setFilterValue(`selection:${panel.id}`, value);
    if (draft.editing) {
      void refreshDraft();
    } else {
      void refresh();
    }
  }

  async function drilldown(panel: DashboardPanel, selection: unknown): Promise<DashboardDrilldownResult> {
    const collection = typeof panel.query.collection === "string" ? panel.query.collection : "";
    const dimensions = stringArray(panel.query.dimensions);
    const timeBucket = isRecord(panel.query.timeBucket) ? panel.query.timeBucket : null;
    const timeField = typeof timeBucket?.field === "string" ? timeBucket.field : null;
    const selectionField = timeField ?? dimensions[0];
    if (!collection || !selectionField) {
      throw new Error("This panel has no drilldown dimension.");
    }
    const configuredFields = stringArray(panel.options.drilldownFields);
    const measureFields = Array.isArray(panel.query.measures)
      ? panel.query.measures.flatMap((measure) => isRecord(measure) && typeof measure.field === "string"
        ? [measure.field]
        : [])
      : [];
    const fields = [...new Set(configuredFields.length > 0
      ? configuredFields
      : ["id", ...dimensions, ...(timeField ? [timeField] : []), ...measureFields])];
    const selectedFields = dashboardSelectionFieldValues(selection);
    const selectionFilters: FilterCondition[] = [];
    for (const field of [...new Set([...(timeField ? [timeField] : []), ...dimensions])]) {
      const value = selectedFields[field];
      if (value === null || value === undefined || value === "") continue;
      if (field === timeField && typeof value === "string") {
        selectionFilters.push(...timeBucketFilters(field, String(timeBucket?.unit ?? "day"), value));
      } else {
        selectionFilters.push({ field, operator: Array.isArray(value) ? "in" : "eq", value });
      }
    }
    if (selectionFilters.length === 0) {
      const value = dashboardSelectionValue(selection);
      selectionFilters.push({
        field: selectionField,
        operator: Array.isArray(value) ? "in" : "eq",
        value,
      });
    }
    const recordQuery: DashboardPanelQueryPayload = {
      kind: "records",
      collection,
      fields,
      filters: [...filterConditions(panel.query.filters), ...selectionFilters],
      limit: 100,
    };
    const query = withRuntimeFilters(
      recordQuery,
      panel.id,
      draft.editing ? draft.config : store.config,
      store.sessionFilterValues,
    );
    const handle = bridge.requestWithHandle("dashboard.queryRequested", {
      panelType: "list",
      query,
    });
    activeQueryRequestIds.add(handle.requestId);
    try {
      const result = await handle.promise as QueryResultWire;
      return {
        rows: Array.isArray(result.rows) ? result.rows : [],
        truncated: result.truncated === true,
      };
    } finally {
      activeQueryRequestIds.delete(handle.requestId);
    }
  }

  async function previewPanel(panel: DashboardPanel): Promise<void> {
    const panelGeneration = (panelGenerations.get(panel.id) ?? 0) + 1;
    panelGenerations.set(panel.id, panelGeneration);
    activePanelControllers.get(panel.id)?.abort(new DOMException("Superseded", "AbortError"));
    await queue.enqueue(
      () => queryPanel(panel, generation, draft.editing ? draft.config : store.config, panelGeneration),
      0,
    );
  }

  async function refreshDraft(): Promise<void> {
    if (!draft.editing || !draft.draft || store.offline || document.hidden) return;
    generation += 1;
    cancelActiveQueries();
    queue.clear();
    const expectedGeneration = generation;
    const queryable = visiblePanelIds
      ? draft.draft.panels.filter((panel) => visiblePanelIds!.has(panel.id))
      : draft.draft.panels;
    await Promise.allSettled(queryable.map((panel, index) => queue.enqueue(
      () => queryPanel(panel, expectedGeneration, draft.config),
      index,
    )));
  }

  function setVisiblePanels(panelIds: readonly string[]): void {
    const previous = visiblePanelIds;
    visiblePanelIds = new Set(panelIds);
    for (const panelId of visiblePanelIds) {
      if (previous?.has(panelId)) continue;
      const panel = (draft.editing ? draft.draft : store.current)?.panels.find((item) => item.id === panelId);
      const data = store.panelData[panelId];
      if (panel && (!data || data.state === "idle" || data.state === "stale")) void previewPanel(panel);
    }
  }

  return {
    init, dispose, list, select, refresh, beginEdit, discardEdit,
    createFromTemplate, copyCurrent, save, deleteCurrent, queryAllPanels,
    previewPanel, refreshDraft, selectPanelValue, drilldown,
    describeCollection,
    setVisiblePanels,
  };
}

export type DashboardService = ReturnType<typeof useDashboardService>;

const DASHBOARD_SERVICE_KEY: InjectionKey<DashboardService> = Symbol("vibetable-dashboard-service");

/** Register the single workspace-owned service instance for dashboard views. */
export function provideDashboardService(service: DashboardService): void {
  provide(DASHBOARD_SERVICE_KEY, service);
}

/** Resolve the workspace-owned instance so views cannot leak duplicate timers/listeners. */
export function useProvidedDashboardService(): DashboardService {
  const service = inject(DASHBOARD_SERVICE_KEY, null);
  if (!service) throw new Error("Dashboard service was not provided by WorkspaceView.");
  return service;
}

function remapManagedConfig(
  config: DashboardManagedConfig,
  panelIds: Readonly<Record<string, string>>,
): DashboardManagedConfig {
  return {
    ...config,
    globalFilters: (config.globalFilters ?? []).flatMap((filter) => {
      const explicitTargets = filter.targetPanels.length > 0;
      const targetPanels = filter.targetPanels.flatMap((id) => panelIds[id] ? [panelIds[id]!] : []);
      const fieldBindings = Object.fromEntries(Object.entries(filter.fieldBindings ?? {}).flatMap(([id, field]) =>
        panelIds[id] ? [[panelIds[id]!, field] as const] : []));
      return explicitTargets && targetPanels.length === 0
        ? []
        : [{ ...filter, targetPanels, fieldBindings }];
    }),
    interactions: (config.interactions ?? []).flatMap((interaction) => {
      const sourcePanelId = panelIds[interaction.sourcePanelId];
      const targetPanelIds = interaction.targetPanelIds.flatMap((id) => panelIds[id] ? [panelIds[id]!] : []);
      return sourcePanelId && targetPanelIds.length > 0
        ? [{ ...interaction, sourcePanelId, targetPanelIds }]
        : [];
    }),
  };
}

function rewriteWorkspaceReferences(
  workspace: unknown,
  clientPanelIds: Readonly<Record<string, string>>,
): unknown {
  if (!isRecord(workspace) || !isRecord(workspace.config)) return workspace;
  const rewriteId = (value: unknown): string | null => {
    if (typeof value !== "string") return null;
    if (typeof clientPanelIds[value] === "string") return clientPanelIds[value]!;
    return value.startsWith("draft:") ? null : value;
  };
  const config = workspace.config;
  const globalFilters = Array.isArray(config.globalFilters)
    ? config.globalFilters.flatMap((value) => {
      if (!isRecord(value)) return [];
      const originalTargets = stringArray(value.targetPanels);
      const targetPanels = originalTargets.flatMap((id) => rewriteId(id) ?? []);
      const originalBindings = isRecord(value.fieldBindings) ? value.fieldBindings : {};
      const fieldBindings = Object.fromEntries(Object.entries(originalBindings).flatMap(([id, field]) => {
        const rewritten = rewriteId(id);
        return rewritten && typeof field === "string" ? [[rewritten, field] as const] : [];
      }));
      return originalTargets.length > 0 && targetPanels.length === 0
        ? []
        : [{ ...value, targetPanels, fieldBindings }];
    })
    : [];
  const interactions = Array.isArray(config.interactions)
    ? config.interactions.flatMap((value) => {
      if (!isRecord(value)) return [];
      const sourcePanelId = rewriteId(value.sourcePanelId);
      const targetPanelIds = stringArray(value.targetPanelIds).flatMap((id) => rewriteId(id) ?? []);
      return sourcePanelId && targetPanelIds.length > 0
        ? [{ ...value, sourcePanelId, targetPanelIds }]
        : [];
    })
    : [];
  return { ...workspace, config: { ...config, globalFilters, interactions } };
}

function withRuntimeFilters(
  query: DashboardPanelQueryPayload,
  panelId: string,
  config: DashboardManagedConfigPayload,
  values: Readonly<Record<string, unknown>>,
): DashboardPanelQueryPayload {
  const filters = [...(query.filters ?? [])];
  for (const item of config.globalFilters ?? []) {
    if (item.targetPanels.length > 0 && !item.targetPanels.includes(panelId)) continue;
    const value = Object.prototype.hasOwnProperty.call(values, item.key)
      ? values[item.key]
      : item.defaultValue;
    const field = item.fieldBindings?.[panelId]
      ?? (item.allowedFields.length === 1 ? item.allowedFields[0] : undefined);
    if (!field || value === null || value === undefined || value === "") continue;
    const normalizedValue = item.type === "date-range" && Array.isArray(value)
      ? value.map((part) => typeof part === "number" ? new Date(part).toISOString() : part)
      : value;
    filters.push({
      field,
      operator: item.type === "date-range" || item.type === "number-range"
        ? "between"
        : Array.isArray(normalizedValue) ? "in" : "eq",
      value: normalizedValue,
    });
  }
  for (const item of config.interactions ?? []) {
    if (!item.targetPanelIds.includes(panelId)) continue;
    const selection = values[`selection:${item.sourcePanelId}`];
    const value = dashboardSelectionValue(selection, item.sourceField);
    if (value === null || value === undefined || value === "") continue;
    filters.push({ field: item.targetField, operator: Array.isArray(value) ? "in" : "eq", value });
  }
  return { ...query, filters };
}

function dashboardSelectionValue(selection: unknown, sourceField?: string | null): unknown {
  const fields = dashboardSelectionFieldValues(selection);
  if (sourceField) {
    return Object.prototype.hasOwnProperty.call(fields, sourceField) ? fields[sourceField] : undefined;
  }
  if (Array.isArray(selection)) {
    const values = selection
      .map((item) => dashboardSelectionValue(item, sourceField))
      .filter((value) => value !== null && value !== undefined && value !== "");
    return [...new Set(values.map((value) => stableSelectionKey(value)))]
      .map((key) => values.find((value) => stableSelectionKey(value) === key));
  }
  if (isRecord(selection) && Object.prototype.hasOwnProperty.call(selection, "primaryValue")) {
    return selection.primaryValue;
  }
  return selection;
}

function dashboardSelectionFieldValues(selection: unknown): Record<string, unknown> {
  if (Array.isArray(selection)) {
    const merged = new Map<string, unknown[]>();
    for (const item of selection) {
      for (const [field, value] of Object.entries(dashboardSelectionFieldValues(item))) {
        if (value === null || value === undefined || value === "") continue;
        const values = merged.get(field) ?? [];
        for (const part of Array.isArray(value) ? value : [value]) {
          if (!values.some((existing) => stableSelectionKey(existing) === stableSelectionKey(part))) values.push(part);
        }
        merged.set(field, values);
      }
    }
    return Object.fromEntries(merged);
  }
  if (!isRecord(selection) || !isRecord(selection.values)) return {};
  return { ...selection.values };
}

function stableSelectionKey(value: unknown): string {
  if (value === null) return "null";
  if (typeof value === "object") return JSON.stringify(value);
  return `${typeof value}:${String(value)}`;
}

class DashboardQueryQueue {
  private readonly pending: Array<{ priority: number; run: () => Promise<void>; resolve: () => void }> = [];
  private active = 0;
  private epoch = 0;

  constructor(private readonly limit: number) {}

  enqueue(run: () => Promise<void>, priority: number): Promise<void> {
    const epoch = this.epoch;
    return new Promise((resolve) => {
      this.pending.push({ priority, run: async () => {
        if (epoch === this.epoch) await run();
      }, resolve });
      this.pending.sort((left, right) => left.priority - right.priority);
      this.drain();
    });
  }

  clear(): void {
    this.epoch += 1;
    for (const item of this.pending.splice(0)) item.resolve();
  }

  private drain(): void {
    while (this.active < this.limit && this.pending.length > 0) {
      const item = this.pending.shift()!;
      this.active += 1;
      void item.run().finally(() => {
        this.active -= 1;
        item.resolve();
        this.drain();
      });
    }
  }
}

function editableType(type: DashboardPanel["productType"]): DashboardPanelType {
  return type === "custom" || type === "unknown" ? "custom" : type;
}

function panelPointWeight(panel: DashboardPanel): number {
  if (panel.query.kind !== "aggregate" || !Array.isArray(panel.query.measures)) return 1;
  return Math.max(1, panel.query.measures.filter((item) => isRecord(item) && typeof item.key === "string").length);
}

function toWireDashboardQuery(panel: DashboardPanel): DashboardPanelQueryPayload | null {
  const query = panel.query;
  const collection = typeof query.collection === "string" ? query.collection : "";
  if (!collection) return null;
  if (query.kind === "records") {
    const fields = stringArray(query.fields);
    if (fields.length === 0) return null;
    return {
      kind: "records",
      collection,
      fields,
      filters: filterConditions(query.filters),
      sorts: sortConditions(query.sorts),
      limit: boundedInteger(query.limit, 20, 1, 100),
    };
  }
  if (query.kind === "aggregate") {
    const measures = Array.isArray(query.measures)
      ? query.measures.flatMap((value, index) => {
        if (!isRecord(value) || !aggregateOp(value.op)) return [];
        return [{
          key: typeof value.key === "string" ? value.key : `measure${index + 1}`,
          op: value.op,
          field: typeof value.field === "string" ? value.field : null,
        }];
      })
      : [];
    if (measures.length === 0) return null;
    const timeBucket = isRecord(query.timeBucket) && typeof query.timeBucket.field === "string" &&
      timeUnit(query.timeBucket.unit)
      ? { field: query.timeBucket.field, unit: query.timeBucket.unit, timezone: "UTC" }
      : null;
    return {
      kind: "aggregate",
      collection,
      dimensions: stringArray(query.dimensions),
      measures,
      filters: filterConditions(query.filters),
      timeBucket,
      limit: boundedInteger(query.limit, 100, 1, 100_000),
      topN: typeof query.topN === "number" ? boundedInteger(query.topN, 100, 1, 5_000) : null,
    };
  }
  return null;
}

function filterConditions(value: unknown): FilterCondition[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item) => {
    if (!isRecord(item) || typeof item.field !== "string" || !filterOperator(item.operator)) return [];
    return [{ field: item.field, operator: item.operator, value: item.value }];
  });
}

function filterOperator(value: unknown): value is FilterCondition["operator"] {
  return value === "eq" || value === "ne" || value === "in" || value === "contains" ||
    value === "starts_with" || value === "ends_with" || value === "gt" || value === "gte" ||
    value === "lt" || value === "lte" || value === "between" || value === "is_null" || value === "is_not_null";
}

function sortConditions(value: unknown): SortCondition[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item) => {
    if (!isRecord(item) || typeof item.field !== "string") return [];
    return [{ field: item.field, direction: item.direction === "desc" ? "desc" as const : "asc" as const }];
  });
}

function stringArray(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

function boundedInteger(value: unknown, fallback: number, min: number, max: number): number {
  return typeof value === "number" && Number.isInteger(value)
    ? Math.max(min, Math.min(max, value))
    : fallback;
}

function aggregateOp(value: unknown): value is "count" | "countDistinct" | "sum" | "avg" | "min" | "max" {
  return value === "count" || value === "countDistinct" || value === "sum" ||
    value === "avg" || value === "min" || value === "max";
}

function timeUnit(value: unknown): value is "day" | "week" | "month" {
  return value === "day" || value === "week" || value === "month";
}

function timeBucketFilters(field: string, unit: string, value: string): FilterCondition[] {
  const start = new Date(value);
  if (!Number.isFinite(start.getTime())) return [{ field, operator: "eq", value }];
  const end = new Date(start.getTime());
  if (unit === "week") end.setUTCDate(end.getUTCDate() + 7);
  else if (unit === "month") end.setUTCMonth(end.getUTCMonth() + 1);
  else end.setUTCDate(end.getUTCDate() + 1);
  return [
    { field, operator: "gte", value: start.toISOString() },
    { field, operator: "lt", value: end.toISOString() },
  ];
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
