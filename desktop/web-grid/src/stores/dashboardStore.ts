import { computed, ref, toRaw } from "vue";
import { defineStore } from "pinia";
import { PRODUCT_PANEL_TYPES, parseWireDashboard, type Dashboard, type DashboardPanel, type ProductPanelType } from "@/dashboard";
import type { DashboardFilterVariablePayload, DashboardInteractionPayload } from "@/contracts";

export interface DashboardListEntry {
  readonly id: string;
  readonly name: string;
  readonly note: string;
  readonly panelCount: number;
}

export interface DashboardManagedConfig {
  readonly configVersion: number;
  readonly globalFilters: readonly DashboardFilterVariablePayload[];
  readonly interactions: readonly DashboardInteractionPayload[];
  readonly refreshInterval: 0 | 30 | 60 | 300 | 900;
}

export interface DashboardQueryLimits {
  readonly maxConcurrentRequests: number;
  readonly maxSeriesPoints: number;
  readonly maxPanelPoints: number;
  readonly maxCategoryPoints: number;
  readonly defaultTopN: number;
  readonly maxPieSlices: number;
  readonly maxListRows: number;
}

export type DashboardPanelDataState = "idle" | "queued" | "loading" | "ready" | "stale" | "failed";

export interface DashboardPanelData {
  readonly state: DashboardPanelDataState;
  readonly rows: readonly Record<string, unknown>[];
  readonly truncated: boolean;
  readonly maxPoints: number;
  readonly updatedAt: number | null;
  readonly error: string | null;
}

const DEFAULT_CONFIG: DashboardManagedConfig = {
  configVersion: 1,
  globalFilters: [],
  interactions: [],
  refreshInterval: 0,
};

const DEFAULT_LIMITS: DashboardQueryLimits = {
  maxConcurrentRequests: 6,
  maxSeriesPoints: 50_000,
  maxPanelPoints: 100_000,
  maxCategoryPoints: 5_000,
  defaultTopN: 100,
  maxPieSlices: 50,
  maxListRows: 100,
};

export const useDashboardStore = defineStore("dashboards", () => {
  const featureEnabled = ref(false);
  const phase = ref<"idle" | "loading-list" | "loading" | "ready" | "saving" | "failed">("idle");
  const list = ref<DashboardListEntry[]>([]);
  const current = ref<Dashboard | null>(null);
  const revision = ref<string | null>(null);
  const config = ref<DashboardManagedConfig>({ ...DEFAULT_CONFIG });
  const limits = ref<DashboardQueryLimits>({ ...DEFAULT_LIMITS });
  const error = ref<string | null>(null);
  const offline = ref(false);
  const lastRefreshAt = ref<number | null>(null);
  const sessionFilterValues = ref<Record<string, unknown>>({});
  const panelData = ref<Record<string, DashboardPanelData>>({});
  const allowedPanelTypes = ref<ProductPanelType[]>([]);
  const manifestVersion = ref<string | null>(null);

  const panelCount = computed(() => current.value?.panels.length ?? 0);

  function setFeatureEnabled(enabled: boolean): void {
    featureEnabled.value = enabled;
  }

  function beginList(): void {
    phase.value = "loading-list";
    error.value = null;
  }

  function receiveList(value: unknown): void {
    const source = isRecord(value) && Array.isArray(value.dashboards) ? value.dashboards : [];
    list.value = source.map((item) => {
      const dashboard = parseWireDashboard(item);
      return {
        id: dashboard.id,
        name: dashboard.name,
        note: dashboard.note,
        panelCount: dashboard.panels.length,
      };
    });
    phase.value = current.value ? "ready" : "idle";
  }

  function beginLoad(): void {
    phase.value = "loading";
    error.value = null;
  }

  function receiveWorkspace(value: unknown): void {
    const source = isRecord(value) ? value : {};
    current.value = parseWireDashboard(source.dashboard);
    revision.value = typeof source.revision === "string" ? source.revision : null;
    config.value = normalizeConfig(source.config);
    limits.value = normalizeLimits(source.queryLimits);
    panelData.value = Object.fromEntries(
      current.value.panels.map((panel) => [panel.id, emptyPanelData()]),
    );
    sessionFilterValues.value = {};
    phase.value = "ready";
    error.value = null;
  }

  function receiveManifest(value: unknown): void {
    const source = isRecord(value) ? value : {};
    const manifest = isRecord(source.manifest) ? source.manifest : {};
    const supported = new Set<string>(PRODUCT_PANEL_TYPES);
    allowedPanelTypes.value = Array.isArray(manifest.panels)
      ? manifest.panels.flatMap((entry) => isRecord(entry) && typeof entry.type === "string" && supported.has(entry.type)
        ? [entry.type as ProductPanelType]
        : [])
      : [];
    manifestVersion.value = typeof manifest.manifestVersion === "string" ? manifest.manifestVersion : null;
    limits.value = normalizeLimits(source.queryLimits);
  }

  function beginSave(): void {
    phase.value = "saving";
    error.value = null;
  }

  function fail(message: string): void {
    phase.value = "failed";
    error.value = message;
  }

  function setPanelState(panelId: string, patch: Partial<DashboardPanelData>): void {
    panelData.value[panelId] = {
      ...(panelData.value[panelId] ?? emptyPanelData()),
      ...patch,
    };
  }

  function markAllStale(): void {
    for (const panel of current.value?.panels ?? []) {
      const existing = panelData.value[panel.id] ?? emptyPanelData();
      if (existing.state === "ready") setPanelState(panel.id, { state: "stale" });
    }
  }

  function setFilterValue(key: string, value: unknown): void {
    sessionFilterValues.value = { ...sessionFilterValues.value, [key]: value };
  }

  function clearFilterValues(): void {
    sessionFilterValues.value = {};
  }

  function reset(): void {
    phase.value = "idle";
    list.value = [];
    current.value = null;
    revision.value = null;
    config.value = { ...DEFAULT_CONFIG };
    limits.value = { ...DEFAULT_LIMITS };
    panelData.value = {};
    sessionFilterValues.value = {};
    error.value = null;
    lastRefreshAt.value = null;
    allowedPanelTypes.value = [];
    manifestVersion.value = null;
  }

  return {
    featureEnabled, phase, list, current, revision, config, limits, error,
    offline, lastRefreshAt, sessionFilterValues, panelData, panelCount,
    allowedPanelTypes, manifestVersion,
    setFeatureEnabled, beginList, receiveList, beginLoad, receiveWorkspace,
    receiveManifest, beginSave, fail, setPanelState, markAllStale, setFilterValue, clearFilterValues, reset,
  };
});

export const useDashboardDraftStore = defineStore("dashboardDraft", () => {
  const editing = ref(false);
  const dirty = ref(false);
  const baseRevision = ref<string | null>(null);
  const draft = ref<Dashboard | null>(null);
  const config = ref<DashboardManagedConfig>({ ...DEFAULT_CONFIG });
  const deletedPanelIds = ref<string[]>([]);
  const conflict = ref<{ message: string; currentRevision?: string } | null>(null);

  function begin(source: Dashboard, sourceConfig: DashboardManagedConfig, revision: string | null): void {
    editing.value = true;
    dirty.value = false;
    baseRevision.value = revision;
    draft.value = cloneDashboard(source);
    config.value = structuredClone(toRaw(sourceConfig));
    deletedPanelIds.value = [];
    conflict.value = null;
  }

  function stop(): void {
    editing.value = false;
    dirty.value = false;
    draft.value = null;
    deletedPanelIds.value = [];
    conflict.value = null;
  }

  function rename(name: string, note: string): void {
    if (!draft.value) return;
    draft.value = { ...draft.value, name, note };
    dirty.value = true;
  }

  function updatePanel(panelId: string, patch: Partial<DashboardPanel>): void {
    if (!draft.value) return;
    draft.value = {
      ...draft.value,
      panels: draft.value.panels.map((panel) =>
        panel.id === panelId ? { ...panel, ...patch } : panel,
      ),
    };
    dirty.value = true;
  }

  function addPanel(panel: DashboardPanel): void {
    if (!draft.value || draft.value.panels.length >= 100) return;
    draft.value = { ...draft.value, panels: [...draft.value.panels, panel] };
    dirty.value = true;
  }

  function removePanel(panelId: string): void {
    if (!draft.value) return;
    const panel = draft.value.panels.find((item) => item.id === panelId);
    if (!panel?.editable) return;
    if (!panel.id.startsWith("draft:")) deletedPanelIds.value.push(panel.id);
    draft.value = { ...draft.value, panels: draft.value.panels.filter((item) => item.id !== panelId) };
    dirty.value = true;
  }

  function setConflict(message: string, currentRevision?: string): void {
    conflict.value = { message, currentRevision };
  }

  function updateConfig(next: DashboardManagedConfig): void {
    config.value = structuredClone(toRaw(next));
    dirty.value = true;
  }

  return {
    editing, dirty, baseRevision, draft, config, deletedPanelIds, conflict,
    begin, stop, rename, updatePanel, addPanel, removePanel, setConflict, updateConfig,
  };
});

function normalizeConfig(value: unknown): DashboardManagedConfig {
  const source = isRecord(value) ? value : {};
  const refresh = source.refreshInterval;
  return {
    configVersion: finiteInteger(source.configVersion, 1),
    globalFilters: Array.isArray(source.globalFilters) ? source.globalFilters.flatMap(parseFilter) : [],
    interactions: Array.isArray(source.interactions) ? source.interactions.flatMap(parseInteraction) : [],
    refreshInterval: refresh === 30 || refresh === 60 || refresh === 300 || refresh === 900 ? refresh : 0,
  };
}

function normalizeLimits(value: unknown): DashboardQueryLimits {
  const source = isRecord(value) ? value : {};
  return {
    maxConcurrentRequests: finiteInteger(source.maxConcurrentRequests, 6),
    maxSeriesPoints: finiteInteger(source.maxSeriesPoints, 50_000),
    maxPanelPoints: finiteInteger(source.maxPanelPoints, 100_000),
    maxCategoryPoints: finiteInteger(source.maxCategoryPoints, 5_000),
    defaultTopN: finiteInteger(source.defaultTopN, 100),
    maxPieSlices: finiteInteger(source.maxPieSlices, 50),
    maxListRows: finiteInteger(source.maxListRows, 100),
  };
}

function parseFilter(value: unknown): DashboardFilterVariablePayload[] {
  if (!isRecord(value) || typeof value.key !== "string" || typeof value.label !== "string" || !filterType(value.type)) return [];
  return [{
    key: value.key,
    label: value.label,
    type: value.type,
    defaultValue: value.defaultValue,
    allowedFields: stringArray(value.allowedFields),
    targetPanels: stringArray(value.targetPanels),
    fieldBindings: stringRecord(value.fieldBindings),
  }];
}

function parseInteraction(value: unknown): DashboardInteractionPayload[] {
  if (!isRecord(value) || typeof value.sourcePanelId !== "string" || typeof value.targetField !== "string") return [];
  return [{
    sourcePanelId: value.sourcePanelId,
    sourceField: typeof value.sourceField === "string" ? value.sourceField : null,
    targetPanelIds: stringArray(value.targetPanelIds),
    targetField: value.targetField,
  }];
}

function filterType(value: unknown): value is DashboardFilterVariablePayload["type"] {
  return value === "date-range" || value === "enum" || value === "user" || value === "relation" || value === "number-range";
}

function stringArray(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

function stringRecord(value: unknown): Record<string, string> {
  if (!isRecord(value)) return {};
  return Object.fromEntries(Object.entries(value).filter((entry): entry is [string, string] => typeof entry[1] === "string"));
}

function emptyPanelData(): DashboardPanelData {
  return { state: "idle", rows: [], truncated: false, maxPoints: 1, updatedAt: null, error: null };
}

function cloneDashboard(source: Dashboard): Dashboard {
  return structuredClone(toRaw(source));
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function finiteInteger(value: unknown, fallback: number): number {
  return typeof value === "number" && Number.isInteger(value) && value >= 0 ? value : fallback;
}
