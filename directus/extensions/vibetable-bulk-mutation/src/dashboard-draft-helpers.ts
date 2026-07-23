import { createHash } from "node:crypto";

export const DASHBOARD_DRAFT_CONTRACT = "vibetable-dashboard-atomic.v1";
export const DASHBOARD_PANEL_HARD_LIMIT = 100;
export const DASHBOARD_CONFIG_MAX_BYTES = 256 * 1024;
export const DASHBOARD_PANEL_OPTIONS_MAX_BYTES = 64 * 1024;

export const DIRECTUS_STANDARD_PANEL_TYPES = new Set([
  "label",
  "metric",
  "metric-list",
  "list",
  "time-series",
  "bar-chart",
  "line-chart",
  "pie-chart",
]);

export type DashboardPanelDraft = {
  clientId: string;
  panelId?: string | null;
  name: string;
  note?: string | null;
  icon?: string | null;
  color?: string | null;
  type: string;
  showHeader?: boolean;
  position: { x: number; y: number; width: number; height: number };
  options: Record<string, unknown>;
};

export type DashboardDraftRequest = {
  contract: typeof DASHBOARD_DRAFT_CONTRACT;
  idempotencyKey: string;
  dashboardId: string | null;
  expectedRevision: string | null;
  dashboard: {
    name: string;
    note?: string | null;
    icon?: string | null;
    color?: string | null;
  };
  panels: DashboardPanelDraft[];
  deletedPanelIds: string[];
  config: Record<string, unknown>;
};

export type DashboardRevisionSnapshot = {
  dashboard: Record<string, unknown>;
  panels: Array<Record<string, unknown>>;
  config: Record<string, unknown> | null;
};

export type DashboardDraftValidation =
  | { ok: true; request: DashboardDraftRequest }
  | { ok: false; error: string };

export type DashboardCachedResult = { status: number; body: unknown };
export type DashboardIdempotencyLookup =
  | { kind: "miss" }
  | { kind: "hit"; result: DashboardCachedResult }
  | { kind: "conflict" };

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const REVISION_PATTERN = /^[0-9a-f]{64}$/i;

export function validateDashboardDraftRequest(
  value: unknown,
  idempotencyKey?: string,
): DashboardDraftValidation {
  if (!isRecord(value)) return invalid("request body must be an object");
  if (value.contract !== DASHBOARD_DRAFT_CONTRACT) {
    return invalid(`contract must be '${DASHBOARD_DRAFT_CONTRACT}'`);
  }
  const key = idempotencyKey ?? value.idempotencyKey;
  if (typeof key !== "string" || !UUID_PATTERN.test(key)) {
    return invalid("Idempotency-Key must be a UUID");
  }
  if (value.dashboardId !== null &&
      (typeof value.dashboardId !== "string" || !UUID_PATTERN.test(value.dashboardId))) {
    return invalid("dashboardId must be null or a UUID");
  }
  if (value.expectedRevision !== null &&
      (typeof value.expectedRevision !== "string" || !REVISION_PATTERN.test(value.expectedRevision))) {
    return invalid("expectedRevision must be null or a SHA-256 hex digest");
  }
  if (!isRecord(value.dashboard)) return invalid("dashboard must be an object");
  if (typeof value.dashboard.name !== "string" ||
      value.dashboard.name.trim().length === 0 || value.dashboard.name.length > 255) {
    return invalid("dashboard.name must contain 1 to 255 characters");
  }
  for (const field of ["note", "icon", "color"] as const) {
    const fieldValue = value.dashboard[field];
    if (fieldValue !== undefined && fieldValue !== null && typeof fieldValue !== "string") {
      return invalid(`dashboard.${field} must be a string or null`);
    }
  }
  if (!Array.isArray(value.panels)) return invalid("panels must be an array");
  if (value.panels.length > DASHBOARD_PANEL_HARD_LIMIT) {
    return invalid(`panels cannot contain more than ${DASHBOARD_PANEL_HARD_LIMIT} entries`);
  }
  const panelIds = new Set<string>();
  for (let index = 0; index < value.panels.length; index += 1) {
    const panel = value.panels[index];
    const error = validatePanel(panel, index, panelIds);
    if (error) return invalid(error);
  }
  if (!Array.isArray(value.deletedPanelIds) || value.deletedPanelIds.length > DASHBOARD_PANEL_HARD_LIMIT) {
    return invalid(`deletedPanelIds must be an array with at most ${DASHBOARD_PANEL_HARD_LIMIT} entries`);
  }
  const deletedIds = new Set<string>();
  for (const id of value.deletedPanelIds) {
    if (typeof id !== "string" || !UUID_PATTERN.test(id)) {
      return invalid("deletedPanelIds must contain UUIDs");
    }
    if (deletedIds.has(id)) return invalid("deletedPanelIds must be unique");
    deletedIds.add(id);
  }
  if (!isRecord(value.config)) return invalid("config must be an object");
  if (jsonBytes(value.config) > DASHBOARD_CONFIG_MAX_BYTES) {
    return invalid(`config exceeds ${DASHBOARD_CONFIG_MAX_BYTES} bytes`);
  }
  const request = value as unknown as DashboardDraftRequest;
  request.idempotencyKey = key;
  return { ok: true, request };
}

export function computeDashboardRevision(snapshot: DashboardRevisionSnapshot): string {
  const normalized = {
    dashboard: canonicalize(snapshot.dashboard),
    panels: snapshot.panels
      .map((panel) => canonicalize(panel) as Record<string, unknown>)
      .sort((left, right) => String(left.id ?? "").localeCompare(String(right.id ?? ""))),
    config: canonicalize(snapshot.config),
  };
  return createHash("sha256").update(JSON.stringify(normalized), "utf8").digest("hex");
}

export function computeDashboardConfigHash(config: Record<string, unknown>): string {
  return createHash("sha256")
    .update(JSON.stringify(canonicalize(config)), "utf8")
    .digest("hex");
}

export function computeDashboardDraftFingerprint(request: DashboardDraftRequest): string {
  return createHash("sha256")
    .update(JSON.stringify(canonicalize(request)), "utf8")
    .digest("hex");
}

/**
 * Replace temporary client panel IDs only in VibeTable-owned reference paths.
 * Every other field is cloned without interpretation so forward-compatible and
 * third-party config survives a save unchanged.
 */
export function rewriteDashboardConfigPanelIds(
  config: Record<string, unknown>,
  clientPanelIds: Readonly<Record<string, string>>,
): Record<string, unknown> {
  const rewriteId = (value: unknown): unknown =>
    typeof value === "string" ? clientPanelIds[value] ?? value : value;
  const cloneValue = (value: unknown): unknown => Array.isArray(value)
    ? value.map(cloneValue)
    : isRecord(value)
      ? Object.fromEntries(Object.entries(value).map(([key, child]) => [key, cloneValue(child)]))
      : value;
  const rewritten = cloneValue(config) as Record<string, unknown>;

  if (Array.isArray(config.globalFilters)) {
    rewritten.globalFilters = config.globalFilters.map((filter) => {
      if (!isRecord(filter)) return cloneValue(filter);
      const result = cloneValue(filter) as Record<string, unknown>;
      if (Array.isArray(filter.targetPanels)) {
        result.targetPanels = filter.targetPanels.map(rewriteId);
      }
      if (isRecord(filter.fieldBindings)) {
        result.fieldBindings = Object.fromEntries(
          Object.entries(filter.fieldBindings).map(([panelId, field]) => [
            String(rewriteId(panelId)), cloneValue(field),
          ]),
        );
      }
      return result;
    });
  }
  if (Array.isArray(config.interactions)) {
    rewritten.interactions = config.interactions.map((interaction) => {
      if (!isRecord(interaction)) return cloneValue(interaction);
      const result = cloneValue(interaction) as Record<string, unknown>;
      if ("sourcePanelId" in interaction) {
        result.sourcePanelId = rewriteId(interaction.sourcePanelId);
      }
      if (Array.isArray(interaction.targetPanelIds)) {
        result.targetPanelIds = interaction.targetPanelIds.map(rewriteId);
      }
      return result;
    });
  }
  return rewritten;
}

export class DashboardPanelSnapshotLimitError extends Error {
  readonly count: number;

  constructor(count: number) {
    super(`dashboard contains more than ${DASHBOARD_PANEL_HARD_LIMIT} panels`);
    this.count = count;
  }
}

export function assertDashboardPanelSnapshotWithinLimit<T>(rows: readonly T[]): void {
  if (rows.length > DASHBOARD_PANEL_HARD_LIMIT) {
    throw new DashboardPanelSnapshotLimitError(rows.length);
  }
}

export function isDashboardUuid(value: unknown): value is string {
  return typeof value === "string" && UUID_PATTERN.test(value);
}

export type DashboardDeletionTargets = {
  dashboardId: string;
  panelIds: string[];
  configId: string | null;
};

export function dashboardDeletionTargets(
  snapshot: DashboardRevisionSnapshot,
  dashboardId: string,
): DashboardDeletionTargets {
  assertDashboardPanelSnapshotWithinLimit(snapshot.panels);
  if (String(snapshot.dashboard.id ?? "") !== dashboardId) {
    throw new Error("dashboard snapshot does not match the delete target");
  }
  const panelIds = snapshot.panels.map((panel) => String(panel.id ?? ""));
  if (panelIds.some((id) => !isDashboardUuid(id))) {
    throw new Error("dashboard snapshot contains an invalid panel id");
  }
  const rawConfigId = snapshot.config?.id;
  const configId = rawConfigId == null ? null : String(rawConfigId);
  if (configId !== null && !isDashboardUuid(configId)) {
    throw new Error("dashboard snapshot contains an invalid config id");
  }
  return { dashboardId, panelIds, configId };
}

/** A bounded, fingerprint-aware LRU cache for dashboard request retries. */
export class DashboardIdempotencyCache {
  private readonly entries = new Map<
    string,
    { fingerprint: string; result: DashboardCachedResult }
  >();

  private readonly bound: number;

  constructor(bound: number = 1024) {
    this.bound = bound;
  }

  lookup(key: string, fingerprint: string): DashboardIdempotencyLookup {
    const entry = this.entries.get(key);
    if (!entry) return { kind: "miss" };
    this.entries.delete(key);
    this.entries.set(key, entry);
    if (entry.fingerprint !== fingerprint) return { kind: "conflict" };
    return { kind: "hit", result: entry.result };
  }

  set(key: string, fingerprint: string, result: DashboardCachedResult): void {
    if (this.entries.has(key)) this.entries.delete(key);
    while (this.entries.size >= this.bound) {
      const oldest = this.entries.keys().next();
      if (oldest.done) break;
      this.entries.delete(oldest.value);
    }
    this.entries.set(key, { fingerprint, result });
  }
}

export function stableDashboardUuid(seed: string): string {
  const hex = createHash("sha256").update(seed, "utf8").digest("hex").slice(0, 32).split("");
  hex[12] = "5";
  hex[16] = ((Number.parseInt(hex[16]!, 16) & 0x3) | 0x8).toString(16);
  const joined = hex.join("");
  return `${joined.slice(0, 8)}-${joined.slice(8, 12)}-${joined.slice(12, 16)}-${joined.slice(16, 20)}-${joined.slice(20)}`;
}

export function dashboardClientPanelIds(
  request: DashboardDraftRequest,
): Record<string, string> {
  return Object.fromEntries(
    request.panels.map((panel) => [
      panel.clientId,
      panel.panelId ?? stableDashboardUuid(`${request.idempotencyKey}:${panel.clientId}`),
    ]),
  );
}

/**
 * Recognize the exact state produced by a previously committed request. This
 * makes a retry safe after the HTTP response or extension process was lost,
 * without allowing a stale request to overwrite any divergent state.
 */
export function matchesCommittedDashboardDraft(
  snapshot: DashboardRevisionSnapshot,
  request: DashboardDraftRequest,
): boolean {
  const isCreation = request.dashboardId === null;
  if (isCreation && (
    request.expectedRevision !== null ||
    request.deletedPanelIds.length > 0 ||
    request.panels.some((panel) => panel.panelId != null)
  )) return false;
  const dashboardId = request.dashboardId ?? stableDashboardUuid(request.idempotencyKey);
  const ids = dashboardClientPanelIds(request);
  const expectedDashboard = {
    id: dashboardId,
    name: request.dashboard.name.trim(),
    note: request.dashboard.note ?? null,
    icon: request.dashboard.icon ?? "dashboard",
    color: request.dashboard.color ?? null,
  };
  const actualDashboard = pick(snapshot.dashboard, Object.keys(expectedDashboard));
  if (!canonicalEqual(actualDashboard, expectedDashboard)) return false;
  if (isCreation && snapshot.panels.length !== request.panels.length) return false;

  const actualPanels = new Map(
    snapshot.panels.map((panel) => [String(panel.id ?? ""), panel]),
  );
  for (const panel of request.panels) {
    const panelId = ids[panel.clientId];
    if (!panelId) return false;
    const actual = actualPanels.get(panelId);
    if (!actual) return false;
    const expected = {
      id: panelId,
      dashboard: dashboardId,
      name: panel.name,
      note: panel.note ?? null,
      icon: panel.icon ?? null,
      color: panel.color ?? null,
      type: panel.type,
      show_header: panel.showHeader ?? true,
      position_x: panel.position.x,
      position_y: panel.position.y,
      width: panel.position.width,
      height: panel.position.height,
      options: panel.options,
    };
    if (!canonicalEqual(pick(actual, Object.keys(expected)), expected)) return false;
  }
  for (const panelId of request.deletedPanelIds) {
    if (actualPanels.has(panelId)) return false;
  }

  if (
    !isRecord(snapshot.config) ||
    snapshot.config.status !== "active" ||
    (isCreation && Number(snapshot.config.config_version) !== 1)
  ) {
    return false;
  }
  const expectedConfig = rewriteDashboardConfigPanelIds(request.config, ids);
  return canonicalEqual(snapshot.config.config, expectedConfig) &&
    snapshot.config.content_hash === computeDashboardConfigHash(expectedConfig);
}

/** Backwards-compatible name retained for tests and callers of the helper module. */
export function matchesDeterministicDashboardCreation(
  snapshot: DashboardRevisionSnapshot,
  request: DashboardDraftRequest,
): boolean {
  return request.dashboardId === null && matchesCommittedDashboardDraft(snapshot, request);
}

export function projectedDashboardPanelCount(
  existingPanelIds: ReadonlySet<string>,
  deletedPanelIds: readonly string[],
  panels: readonly DashboardPanelDraft[],
): number {
  const deletedExisting = new Set(
    deletedPanelIds.filter((id) => existingPanelIds.has(id)),
  ).size;
  const newPanelCount = panels.filter((panel) => !panel.panelId).length;
  return existingPanelIds.size - deletedExisting + newPanelCount;
}

function validatePanel(
  value: unknown,
  index: number,
  panelIds: Set<string>,
): string | null {
  const prefix = `panels[${index}]`;
  if (!isRecord(value)) return `${prefix} must be an object`;
  if (typeof value.clientId !== "string" || value.clientId.length < 1 || value.clientId.length > 128) {
    return `${prefix}.clientId must contain 1 to 128 characters`;
  }
  if (panelIds.has(value.clientId)) return `${prefix}.clientId must be unique`;
  panelIds.add(value.clientId);
  if (value.panelId !== undefined && value.panelId !== null &&
      (typeof value.panelId !== "string" || !UUID_PATTERN.test(value.panelId))) {
    return `${prefix}.panelId must be null or a UUID`;
  }
  if (typeof value.name !== "string" || value.name.length > 255) {
    return `${prefix}.name must be a string up to 255 characters`;
  }
  if (typeof value.type !== "string" || value.type.length === 0 || value.type.length > 128) {
    return `${prefix}.type must contain 1 to 128 characters`;
  }
  if (value.showHeader !== undefined && typeof value.showHeader !== "boolean") {
    return `${prefix}.showHeader must be boolean`;
  }
  if (!isRecord(value.position)) return `${prefix}.position must be an object`;
  const x = integer(value.position.x);
  const y = integer(value.position.y);
  const width = integer(value.position.width);
  const height = integer(value.position.height);
  if (x === null || x < 0 || x > 11) return `${prefix}.positionX is outside the 12-column grid`;
  if (y === null || y < 0 || y > 100_000) return `${prefix}.positionY is invalid`;
  if (width === null || width < 1 || width > 12 || x + width > 12) {
    return `${prefix}.width is outside the 12-column grid`;
  }
  if (height === null || height < 1 || height > 1_000) return `${prefix}.height is invalid`;
  if (!isRecord(value.options)) return `${prefix}.options must be an object`;
  if (jsonBytes(value.options) > DASHBOARD_PANEL_OPTIONS_MAX_BYTES) {
    return `${prefix}.options exceeds ${DASHBOARD_PANEL_OPTIONS_MAX_BYTES} bytes`;
  }
  return null;
}

function canonicalize(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonicalize);
  if (!isRecord(value)) return value;
  return Object.fromEntries(
    Object.keys(value)
      .sort()
      .filter((key) => value[key] !== undefined)
      .map((key) => [key, canonicalize(value[key])]),
  );
}

function canonicalEqual(left: unknown, right: unknown): boolean {
  return JSON.stringify(canonicalize(left)) === JSON.stringify(canonicalize(right));
}

function pick(
  value: Record<string, unknown>,
  keys: readonly string[],
): Record<string, unknown> {
  return Object.fromEntries(keys.map((key) => [key, value[key]]));
}

function integer(value: unknown): number | null {
  return typeof value === "number" && Number.isInteger(value) ? value : null;
}

function jsonBytes(value: unknown): number {
  return Buffer.byteLength(JSON.stringify(value), "utf8");
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function invalid(error: string): DashboardDraftValidation {
  return { ok: false, error };
}
