/**
 * Wire contracts shared between the web grid (TypeScript) and the .NET host.
 *
 * The host owns this camelCase wire shape and forwards it over
 * `window.chrome.webview`. Directus/Python transport details stay behind the
 * WPF gateway boundary.
 *
 * Message flow:
 *   web -> host : app.ready, database.openRequested, table.selected, table.pageRequested
 *   host -> web : database.opened, table.pageLoaded, operation.failed
 *
 * `rowKey` is transport metadata: every row carries it, but the grid hides it
 * from visible columns; no synthetic `rowid` column is exposed.
 */

// ---------------------------------------------------------------------------
// Table domain (host/WebView contract)
// ---------------------------------------------------------------------------

export type ColumnDataType =
  | "text"
  | "integer"
  | "decimal"
  | "boolean"
  | "date"
  | "datetime"
  | "time";

export interface ColumnSchema {
  /** Programmatic column name; matches the row-dict key. */
  readonly name: string;
  /** Human-readable column heading. */
  readonly title: string;
  /** Grid renderer type hint. */
  readonly dataType: ColumnDataType;
  /** Whether the current Directus capability schema permits editing. */
  readonly editable: boolean;
  /** Whether the column may hold NULL. */
  readonly nullable: boolean;
  /**
   * Numeric scale (digits after the decimal point) from Directus
   * `numeric_scale`. `null` for non-numeric fields or when Directus does not
   * report it. Drives decimal display precision and edit-side scale checks.
   */
  readonly scale?: number | null;
  /** Numeric precision (total significant digits) from Directus. */
  readonly precision?: number | null;
}

export type TableMode = "client" | "remote";

/**
 * One page of rows. `rows` is a list of plain objects keyed by column name
 * plus the hidden `rowKey`.
 */
export interface TablePage {
  readonly table: string;
  readonly columns: readonly ColumnSchema[];
  readonly rows: ReadonlyArray<Record<string, unknown>>;
  readonly offset: number;
  readonly limit: number;
  readonly totalRows: number;
  readonly mode: TableMode;
  /** B3/Directus metadata used to bind mutations to the rendered page. */
  readonly filteredRows?: number | null;
  readonly querySnapshot?: QuerySnapshot | null;
  readonly revision?: MutationRevision | null;
}

/**
 * One page of rows delivered incrementally by the host's multi-page client-mode
 * fetch. Same shape as `TablePage` plus `loadedRows` (cumulative count of rows
 * fetched so far) and `totalRows`. Mirrors the host-side
 * `TableNotification` payload for `table.pageLoaded`.
 */
export interface TablePageLoadedPayload {
  readonly table: string;
  readonly columns: readonly ColumnSchema[];
  readonly rows: ReadonlyArray<Record<string, unknown>>;
  readonly offset: number;
  readonly limit: number;
  readonly totalRows: number;
  readonly mode: TableMode;
  /** Cumulative rows fetched so far in the client-mode multi-page load. */
  readonly loadedRows: number;
  /** B3/Directus query metadata required by mutations and paste preview. */
  readonly filteredRows?: number | null;
  readonly querySnapshot?: QuerySnapshot | null;
  readonly revision?: MutationRevision | null;
}

/**
 * Payload for `table.datasetReady` — the host emits this ONCE, after the full
 * client-mode dataset has loaded (loadedRows == totalRows). The renderer
 * renders the complete grid on this signal.
 */
export interface DatasetReadyPayload extends TablePage {
  /** Cumulative rows fetched; equals `totalRows` for client-mode tables. */
  readonly loadedRows: number;
  readonly filteredRows?: number | null;
  readonly querySnapshot?: QuerySnapshot | null;
  readonly revision?: MutationRevision | null;
}

/** Result payload for `database.opened`. */
export interface DatabaseOpenedPayload {
  readonly tables: readonly string[];
  readonly views: readonly string[];
  /** Stable host-normalized Directus identity used for project-local plugin state. */
  readonly projectKey?: string;
  /** Host workspace revision used to invalidate stale install plans. */
  readonly projectRevision?: string;
  /** Safe display identity; authentication secrets are never included. */
  readonly currentUser?: Readonly<Record<string, unknown>>;
  readonly hostVersion?: string;
  /** Physical collection -> user-facing label. Optional for old hosts. */
  readonly displayNames?: Readonly<Record<string, string>>;
}

/** Payload produced by the web layer for `database.openRequested`. */
export interface DatabaseOpenRequestedPayload {
  readonly path: string;
}

/** Payload produced by the web layer for `table.selected`. */
export interface TableSelectedPayload {
  readonly table: string;
}

/** Payload produced by the web layer for `table.pageRequested`. */
export interface TablePageRequestedPayload {
  readonly table: string;
  readonly offset: number;
  readonly limit: number;
}

// ---------------------------------------------------------------------------
// B1 mutation contracts (mirror backend.contracts.mutation)
// ---------------------------------------------------------------------------

/** Editor discriminator kind. Mirrors the Python `EditorKind` literal. */
export type EditorKind =
  | "text"
  | "number"
  | "boolean"
  | "date"
  | "single_select"
  | "multi_select";

/** Base editor shape (discriminated by `kind`). */
export interface EditorBase {
  readonly kind: EditorKind;
}

export interface TextEditor extends EditorBase {
  readonly kind: "text";
  readonly multiline?: boolean;
  readonly maxLength?: number | null;
}

export interface NumberEditor extends EditorBase {
  readonly kind: "number";
  readonly storage: "integer" | "decimal";
  readonly precision?: number | null;
  readonly scale?: number | null;
  readonly minValue?: number | null;
  readonly maxValue?: number | null;
}

export interface BooleanEditor extends EditorBase {
  readonly kind: "boolean";
}

export interface DateEditor extends EditorBase {
  readonly kind: "date";
  readonly dateType: "date" | "datetime" | "time";
  readonly format?: string | null;
  readonly showWeekday?: boolean;
}

export interface SingleSelectEditor extends EditorBase {
  readonly kind: "single_select";
  readonly options: readonly string[];
  readonly allowCustom?: boolean;
}

export interface MultiSelectEditor extends EditorBase {
  readonly kind: "multi_select";
  readonly options: readonly string[];
  readonly allowCustom?: boolean;
}

export type Editor =
  | TextEditor
  | NumberEditor
  | BooleanEditor
  | DateEditor
  | SingleSelectEditor
  | MultiSelectEditor;

/** Validation rule discriminator kind. */
export type RuleKind =
  | "required"
  | "range"
  | "precision"
  | "choice"
  | "foreign_key";

export interface ValidationRule {
  readonly kind: RuleKind;
  readonly [key: string]: unknown;
}

/** One column's editable schema. Mirrors `ColumnEditSchema`. */
export interface ColumnEditSchema {
  readonly name: string;
  readonly storageName: string;
  readonly dataType:
    | "text"
    | "integer"
    | "decimal"
    | "boolean"
    | "date"
    | "datetime"
    | "time"
    | "single_select"
    | "multi_select";
  readonly editable: boolean;
  readonly nullable: boolean;
  readonly primaryKey: boolean;
  readonly editor: Editor;
  readonly validation: readonly ValidationRule[];
}

/** Result of `table.getEditSchema`. Mirrors `EditSchemaResult`. */
export interface EditSchemaResult {
  readonly table: string;
  readonly schemaRevision: string;
  readonly rowKeyKind: "primary_key" | "rowid" | "mapped_view" | "none";
  readonly rowKeyStable: boolean;
  readonly editable: boolean;
  readonly columns: readonly ColumnEditSchema[];
}

/** Mutation revision. Mirrors `MutationRevision`. */
export interface MutationRevision {
  readonly databaseSessionId: string;
  readonly schemaRevision: string;
  readonly dataRevision: number;
}

/** Result of `table.updateCell`. Mirrors `UpdateCellResult`. */
export interface UpdateCellResult {
  readonly rowKey: number | string;
  readonly column: string;
  readonly storedValue: unknown;
  readonly currentRow: Record<string, unknown>;
  readonly revision: MutationRevision;
}

/** Result of `table.insertRow`. Mirrors `InsertRowResult`. */
export interface InsertRowResult {
  readonly rowKey: number | string;
  readonly row: Record<string, unknown>;
  readonly revision: MutationRevision;
}

/** Result of `table.deleteRows`. Mirrors `DeleteRowsResult`. */
export interface DeleteRowsResult {
  readonly deletedRowKeys: readonly (number | string)[];
  readonly revision: MutationRevision;
}

/** A mapped mutation error forwarded by the host. */
export interface MutationErrorPayload {
  readonly kind:
    | "edit_conflict"
    | "mutation_validation"
    | "schema_mismatch"
    | "not_writable"
    | "backend_unavailable"
    | "cancelled"
    | "unknown";
  readonly message: string;
  readonly currentRow?: Record<string, unknown> | null;
  readonly conflictingRowKeys?: readonly (number | string)[] | null;
  readonly fieldErrors?: Record<string, string> | null;
}

// ---------------------------------------------------------------------------
// B1 outbound mutation request payloads (web -> host)
// ---------------------------------------------------------------------------

/** Payload produced by the web layer for `table.updateCellRequested`. */
export interface UpdateCellRequestedPayload {
  readonly table: string;
  readonly rowKey: number | string;
  readonly column: string;
  readonly oldValue: unknown;
  readonly newValue: unknown;
  readonly schemaRevision: string;
}

/** Payload produced by the web layer for `table.insertRowRequested`. */
export interface InsertRowRequestedPayload {
  readonly table: string;
  readonly values: Readonly<Record<string, unknown>>;
  readonly schemaRevision: string;
}

/** One row targeted by `table.deleteRowsRequested`. */
export interface DeleteRowRequestItem {
  readonly rowKey: number | string;
  readonly expectedDigest: string;
}

/** Payload produced by the web layer for `table.deleteRowsRequested`. */
export interface DeleteRowsRequestedPayload {
  readonly table: string;
  readonly rows: readonly DeleteRowRequestItem[];
  readonly schemaRevision: string;
}

/** Payload for `operation.failed` (rejections). */
export interface OperationFailedPayload {
  readonly message: string;
  readonly code?: string;
}

// ---------------------------------------------------------------------------
// B3 query/state contracts (mirror backend.contracts.query / selection /
// grid_state)
// ---------------------------------------------------------------------------

/** The 14 filter operator values. Mirrors `FilterOperator`. */
export type FilterOperator =
  | "contains"
  | "eq"
  | "ne"
  | "starts_with"
  | "ends_with"
  | "gt"
  | "lt"
  | "gte"
  | "lte"
  | "between"
  | "in"
  | "is_null"
  | "is_not_null"
  | "regex";

/** One filter condition. Mirrors `FilterCondition`. */
export interface FilterCondition {
  readonly field: string;
  readonly operator: FilterOperator;
  readonly value?: unknown;
  readonly logic?: "AND" | "OR";
}

/** One sort condition. Mirrors `SortCondition`. */
export interface SortCondition {
  readonly field: string;
  readonly direction?: "asc" | "desc";
  readonly nullsLast?: boolean;
}

/** The typed query AST. Mirrors `TableQuery`. */
export interface TableQuery {
  readonly keyword?: string | null;
  readonly filters?: readonly FilterCondition[];
  readonly sorts?: readonly SortCondition[];
  readonly offset?: number;
  readonly limit?: number;
}

/** A stable query-view snapshot. Mirrors `QuerySnapshot`. */
export interface QuerySnapshot {
  readonly snapshotId: string;
  readonly digest: string;
  readonly databaseId: string;
  readonly table: string;
  readonly schemaRevision: string;
  readonly dataRevision: number;
  readonly normalizedQuery: Record<string, unknown>;
}

/** A selection bound to a query snapshot. Mirrors `SelectionSnapshot`. */
export interface SelectionSnapshot {
  readonly querySnapshot: QuerySnapshot;
  readonly dataRevision: number;
  readonly rowKeys: readonly (number | string)[];
}

/** Result of validating a snapshot. Mirrors `SnapshotValidation`. */
export interface SnapshotValidation {
  readonly valid: boolean;
  readonly reason?:
    | "query_changed"
    | "schema_changed"
    | "application_write"
    | "external_write"
    | null;
  readonly currentDataRevision?: number | null;
  readonly currentSchemaRevision?: string | null;
}

/** One column's persisted grid state. Mirrors `ColumnState`. */
export interface ColumnState {
  readonly name: string;
  readonly width?: number | null;
  readonly visible?: boolean;
  readonly frozen?: boolean;
  readonly order?: number | null;
}

/** The full persisted grid state for one table. Mirrors `GridState`. */
export interface GridState {
  readonly columns?: readonly ColumnState[];
  readonly sorts?: readonly SortCondition[];
  readonly filters?: readonly FilterCondition[];
  readonly keyword?: string | null;
  readonly density?: "compact" | "comfortable" | "cozy";
  readonly forcedRemote?: boolean;
  readonly revision?: string | null;
}

/** Result of `gridState.get` / `gridState.save`. Mirrors `GridStateResult`. */
export interface GridStateResult {
  readonly state: GridState;
  readonly revision: string;
  readonly conflict: boolean;
}

/** B3 extension to `TablePage`: the query/snapshot fields are optional. */
export interface TablePageWithQuery extends TablePage {
  readonly filteredRows?: number | null;
  readonly querySnapshot?: QuerySnapshot | null;
  readonly revision?: MutationRevision | null;
}

// ---------------------------------------------------------------------------
// B2 paste contracts (mirror backend.contracts.paste)
// ---------------------------------------------------------------------------

/** One cell submitted in a paste preview request. Mirrors `PasteCell`. */
export interface PasteCellPayload {
  readonly rowIndex: number;
  readonly columnIndex: number;
  readonly column?: string | null;
  readonly rawValue: string;
  readonly parsedValue?: unknown;
}

/** The paste anchor. Mirrors `PasteStartCell`. */
export interface PasteStartCellPayload {
  readonly rowKey?: number | string | null;
  readonly column: string;
}

/** One planned change to a target row. Mirrors `PastePlanRow`. */
export interface PastePlanRow {
  readonly kind: "update" | "insert" | "skip";
  readonly targetRowKey?: number | string | null;
  readonly expectedDateUpdated?: string | null;
  readonly changes: Readonly<Record<string, { before: unknown; after: unknown }>>;
  readonly diagnostics: readonly PasteCellDiagnostic[];
}

/** A localized diagnostic for one paste cell. Mirrors `PasteCellDiagnostic`. */
export interface PasteCellDiagnostic {
  readonly rowIndex: number;
  readonly columnIndex: number;
  readonly severity: "error" | "warning";
  readonly code: string;
  readonly message: string;
}

/** Aggregated counts for a paste plan. Mirrors `PasteSummary`. */
export interface PasteSummary {
  readonly updateRows: number;
  readonly insertRows: number;
  readonly skipRows: number;
  readonly errorCount: number;
  readonly warningCount: number;
}

/** An opaque, single-use, server-bound paste handle. Mirrors `PasteToken`. */
export interface PasteToken {
  readonly token: string;
  readonly expiresAt: number;
  readonly consumed: boolean;
}

/** Result of `table.previewPaste`. Mirrors `PastePlan`. */
export interface PastePlan {
  readonly collection: string;
  readonly schemaRevision: string;
  readonly capabilityHash: string;
  readonly summary: PasteSummary;
  readonly rows: readonly PastePlanRow[];
  readonly diagnostics: readonly PasteCellDiagnostic[];
  readonly token: PasteToken;
  readonly overflow: boolean;
}

/** One row that blocked an apply. Mirrors `ApplyPasteConflict`. */
export interface ApplyPasteConflict {
  readonly rowKey: number | string;
  readonly currentValue: Readonly<Record<string, unknown>>;
  readonly expectedDateUpdated?: string | null;
}

/** Result of `table.applyPaste`. Mirrors `ApplyPasteResult`. */
export interface ApplyPasteResult {
  readonly collection: string;
  readonly outcome: "committed" | "conflict" | "pending";
  readonly createdRowKeys: readonly (number | string)[];
  readonly updatedRowKeys: readonly (number | string)[];
  readonly skippedRowKeys: readonly (number | string)[];
  readonly conflicts: readonly ApplyPasteConflict[];
  readonly requestId: string;
}

// ---------------------------------------------------------------------------
// Table-admin contracts (mirror backend/contracts/table_admin.py)
// ---------------------------------------------------------------------------

/** Persisted Directus field types supported by the create-table wizard.
 *  `alias` is intentionally excluded: it is a virtual relationship/display
 *  field and requires relation-specific configuration rather than a column. */
export const TABLE_FIELD_TYPES = [
  "string",
  "text",
  "integer",
  "bigInteger",
  "float",
  "decimal",
  "boolean",
  "date",
  "dateTime",
  "timestamp",
  "time",
  "json",
  "csv",
  "uuid",
  "hash",
  "binary",
] as const;
export type TableFieldType = (typeof TABLE_FIELD_TYPES)[number];

/** Unicode display-name rule. Physical Directus identifiers are host-owned. */
export const TABLE_NAME_PATTERN = /^[^\u0000-\u001F\u007F-\u009F]{1,128}$/u;

export interface TableAdminFieldInput {
  readonly key: string;
  readonly type: TableFieldType;
}
export interface TableAdminCreatePayload {
  readonly name: string;
  readonly fields: readonly TableAdminFieldInput[];
}
export interface TableAdminDeletePayload {
  readonly collection: string;
}
export interface CollectionsChangedPayload {
  readonly tables: readonly string[];
  readonly capabilityHashes?: Readonly<Record<string, string>>;
  /** Physical collection -> user-facing label. */
  readonly displayNames?: Readonly<Record<string, string>>;
  readonly projectRevision?: string;
}

export interface IdentifierMappingEntry {
  readonly id: string;
  readonly entityKind: "collection" | "field";
  readonly parentPhysicalName?: string | null;
  readonly physicalName: string;
  readonly displayName: string;
  readonly locale: string;
  readonly aliases: readonly string[];
  readonly origin: "vibetable" | "directus" | "import";
  readonly status: "pending" | "active" | "orphaned" | "deleted";
}

export interface IdentifierMappingImportItem {
  readonly entityKind: "collection" | "field";
  readonly parentPhysicalName?: string | null;
  readonly physicalName: string;
  readonly displayName: string;
  readonly aliases: readonly string[];
}

export interface IdentifierMappingsResult {
  readonly mappings: readonly IdentifierMappingEntry[];
}

// ---------------------------------------------------------------------------
// Flow-first plugin platform (host/WebView contract v1)
// ---------------------------------------------------------------------------

export type PluginRisk = "read" | "write" | "destructive";
export type PluginSourceType = "package" | "local-folder";
export type PluginStatus = "disabled" | "enabled" | "error";
export type PluginFlowOwnership = "managed" | "external";
export type PluginFlowTrigger = "manual" | "webhook" | "schedule" | "event";
export type PluginFlowHealth = "healthy" | "missing" | "incompatible" | "drifted";
export type PluginTaskState =
  | "queued"
  | "running"
  | "succeeded"
  | "failed"
  | "cancelled"
  | "aborted";

export interface PluginAction {
  readonly actionId: string;
  readonly displayName: Readonly<Record<string, string>>;
  readonly description: Readonly<Record<string, string>>;
  readonly mode: "flow" | "local" | "hybrid";
  readonly risk: PluginRisk;
  readonly invocation: "manual" | "webhook";
  readonly placements: readonly string[];
  readonly requires: Readonly<Record<string, unknown>>;
  readonly entryFlow: string | null;
  readonly workerEntry: string | null;
  readonly formSchema: string | null;
  readonly inputSchema: string | null;
  readonly outputSchema: string | null;
}

export interface PluginManifest {
  readonly $schema: "vibetable.plugin-manifest.v1";
  readonly pluginId: string;
  readonly version: string;
  readonly displayName: Readonly<Record<string, string>>;
  readonly description: Readonly<Record<string, string>>;
  readonly compatibility: Readonly<Record<string, unknown>>;
  readonly permissions: Readonly<Record<string, unknown>>;
  readonly actions: readonly PluginAction[];
  readonly flows: readonly Readonly<Record<string, unknown>>[];
  readonly ui: Readonly<Record<string, unknown>>;
}

export interface PluginFlowRequirement {
  readonly logicalFlowId: string;
  readonly ownership: PluginFlowOwnership;
  readonly trigger: PluginFlowTrigger;
  readonly risk: PluginRisk;
  readonly contractVersion: string;
  readonly requiresOperations: readonly string[];
  readonly inputSchema: Readonly<Record<string, unknown>>;
  readonly outputSchema: Readonly<Record<string, unknown>>;
  readonly definition: Readonly<Record<string, unknown>> | null;
}

/** Canonical Python PluginSnapshot; WPF forwards it unchanged. */
export interface PluginSnapshot {
  readonly projectKey: string;
  readonly pluginId: string;
  readonly version: string;
  readonly packageHash: string;
  readonly sourceType: PluginSourceType;
  readonly sourceLocation: string;
  readonly sourceChanged?: boolean;
  readonly manifest: PluginManifest;
  readonly flowRequirements: readonly PluginFlowRequirement[];
  readonly flowBindings?: readonly PluginFlowBindingSnapshot[];
  readonly schemas: Readonly<Record<string, Readonly<Record<string, unknown>>>>;
  readonly status: PluginStatus;
  readonly disabledReason: string | null;
  readonly blockingReasons?: readonly string[];
  readonly revision: number;
}

export interface PluginInstallPlan {
  readonly planId: string;
  readonly projectKey: string;
  readonly projectRevision: string;
  readonly sourceType: PluginSourceType;
  readonly sourceLocation: string;
  readonly packageHash: string;
  readonly manifest: PluginManifest;
  readonly flowRequirements: readonly PluginFlowRequirement[];
  readonly schemas: Readonly<Record<string, Readonly<Record<string, unknown>>>>;
}

export interface PluginExternalFlowCandidate {
  readonly directusFlowUuid: string;
  readonly name: string;
  readonly triggerType: PluginFlowTrigger;
  readonly status: "active" | "inactive";
  readonly operationKeys: readonly string[];
  readonly compatible: boolean;
  readonly reasons: readonly string[];
}

export interface PluginFlowBindingSnapshot {
  readonly projectKey: string;
  readonly pluginId: string;
  readonly logicalFlowId: string;
  readonly ownership: PluginFlowOwnership;
  readonly directusFlowUuid: string;
  readonly rollbackFlowUuid: string | null;
  readonly rollbackContractVersion: string | null;
  readonly rollbackDefinitionHash: string | null;
  readonly triggerType: PluginFlowTrigger;
  readonly contractVersion: string;
  readonly installedDefinitionHash: string | null;
  readonly observedDefinitionHash: string;
  readonly revision: number;
  readonly health: PluginFlowHealth;
  readonly driftStatus: "clean" | "drifted" | "not-applicable";
  readonly lastError: string | null;
}

export interface PluginUninstallResult {
  readonly managedFlowsRemoved: number;
  readonly externalFlowsUnbound: number;
  readonly uninstalled: boolean;
  readonly privateSettingsRetained: boolean;
  readonly cleanupPending: boolean;
}

export interface PluginAuditEvent {
  readonly eventId: string;
  readonly projectKey: string;
  readonly pluginId: string;
  readonly pluginVersion: string;
  readonly packageHash: string;
  readonly eventType: string;
  readonly outcome: string;
  readonly actionId: string | null;
  readonly runId: string | null;
  readonly actor: string;
  readonly risk: PluginRisk | null;
  readonly targetCollection: string | null;
  readonly targetCount: number | null;
  readonly startedAt: string;
  readonly finishedAt: string | null;
  readonly durationMs: number | null;
  readonly errorCode: string | null;
  readonly details: Readonly<Record<string, unknown>>;
}

export interface PluginActionAvailability {
  readonly available: boolean;
  readonly reasons: readonly string[];
}

export interface PluginCommandContext {
  readonly contract: "vibetable.command-context.v1";
  readonly projectKey: string;
  readonly collection: string | null;
  readonly selectedKeys: readonly (string | number)[];
  readonly querySnapshot: Readonly<Record<string, unknown>> | null;
  readonly locale: string;
  readonly theme: "light" | "dark";
  readonly density: string;
  readonly user: Readonly<Record<string, unknown>>;
  readonly hostVersion: string;
}

/** Web-only standard/custom surface projection. It never crosses HostBridge. */
export interface WebPluginActionDescription {
  readonly pluginId: string;
  readonly actionId: string;
  readonly title: string;
  readonly description?: string | null;
  readonly risk: PluginRisk;
  readonly inputSchema: Readonly<Record<string, unknown>>;
  readonly uiSchema?: Readonly<Record<string, unknown>>;
  readonly presentation?: "standard" | "custom";
  readonly surface?: {
    readonly src: string;
    readonly surfaceToken: string;
    readonly title: string;
  } | null;
}

export interface WebPluginConfirmationPreview {
  readonly runId: string;
  readonly interactionId: string;
  readonly pluginId: string;
  readonly actionId: string;
  readonly title: string;
  readonly summary: string;
  readonly risk: "write" | "destructive";
  readonly targetCount?: number | null;
  readonly sample?: readonly Readonly<Record<string, unknown>>[];
  readonly expiresAt: string;
}

export interface PluginMetric {
  readonly label: string;
  readonly value: string | number;
}

/** Python runtime result — this exact shape is shared through WPF unchanged. */
export interface PluginResult {
  readonly contract: "vibetable.plugin-result.v1";
  readonly status: "success" | "warning" | "error";
  readonly summary: string;
  readonly metrics: readonly PluginMetric[];
  readonly table: Readonly<Record<string, unknown>> | null;
  readonly artifacts: readonly Readonly<Record<string, unknown>>[];
  readonly refresh: Readonly<Record<string, unknown>> | null;
  readonly warnings: readonly string[];
}

/** Python runtime task — revision lives on PluginEventEnvelope, not the task. */
export interface PluginTaskSnapshot {
  readonly taskId: string;
  readonly runId: string;
  readonly pluginId: string;
  readonly pluginVersion: string;
  readonly actionId: string;
  readonly projectKey: string;
  readonly collection: string | null;
  readonly targetCount: number;
  readonly risk: PluginRisk;
  readonly state: PluginTaskState;
  readonly cancelRequested: boolean;
  readonly progress?: PluginRuntimeProgress | null;
  readonly result: PluginResult | null;
  readonly error: PluginSafeError | null;
}

export interface PluginRuntimeProgress {
  readonly current: number;
  readonly total: number;
  readonly message: string;
  readonly cancellable: boolean;
}

export interface PluginRuntimeConfirmationPreview {
  readonly summary: readonly Readonly<Record<string, unknown>>[];
  readonly sampleRows: readonly Readonly<Record<string, unknown>>[];
  readonly affectedCount: number;
  readonly warnings: readonly string[];
}

export interface PluginRuntimePendingConfirmation {
  readonly interactionId: string;
  readonly risk: PluginRisk;
  readonly title: string;
  readonly preview: PluginRuntimeConfirmationPreview;
  readonly expiresAt: number;
}

export interface PluginInteractionSnapshot {
  readonly runId: string;
  readonly projectKey: string;
  readonly pluginId: string;
  readonly actionId: string;
  readonly caller: string;
  readonly progress: PluginRuntimeProgress | null;
  readonly pendingConfirmation: PluginRuntimePendingConfirmation | null;
  readonly cancelRequested: boolean;
}

export interface PluginSafeError {
  readonly contract: "vibetable.plugin-error.v1";
  readonly code: string;
  readonly message: string;
  readonly recoverability: "retry" | "rebind" | "reconfigure" | "reinstall" | "none";
  readonly pluginId: string | null;
  readonly actionId: string | null;
  readonly runId: string | null;
  readonly details: Readonly<Record<string, unknown>>;
  readonly causeId: string | null;
}

export interface PluginInteractionResolveResult {
  readonly status: "resolved" | "already-resolved" | "expired";
  readonly decision: "approved" | "rejected" | null;
}

/** Web-only projection. It never crosses HostBridge. */
export interface PluginTaskViewSnapshot extends PluginTaskSnapshot {
  readonly revision: number;
  readonly progressPercent?: number | null;
  readonly progressMessage?: string | null;
  readonly confirmation?: WebPluginConfirmationPreview | null;
}

export interface PluginSurfaceThemeSnapshot {
  readonly contract: "vibetable.plugin-theme.v1";
  readonly mode: "light" | "dark";
  readonly locale: "zh-CN" | "en-US";
  readonly density: "comfortable" | "compact";
  readonly variables: Readonly<Record<
    | "--vt-plugin-bg"
    | "--vt-plugin-surface"
    | "--vt-plugin-text"
    | "--vt-plugin-text-muted"
    | "--vt-plugin-border"
    | "--vt-plugin-primary"
    | "--vt-plugin-danger"
    | "--vt-plugin-radius"
    | "--vt-plugin-space-unit",
    string
  >>;
}

export interface PluginSurfaceEventPayload {
  readonly contract: "vibetable.plugin-surface.v1";
  readonly surfaceToken: string;
  readonly event: "ready" | "close" | "action";
  readonly payload: Readonly<Record<string, unknown>>;
}

export interface PluginSurfaceHostMessage {
  readonly contract: "vibetable.plugin-surface.v1";
  readonly surfaceToken: string;
  readonly event: "themeChanged";
  readonly payload: PluginSurfaceThemeSnapshot;
}

export interface PluginEventEnvelope {
  readonly contract: "vibetable.plugin-event.v1";
  readonly eventType: "plugin.catalog.changed" | "plugin.task.changed" | "plugin.interaction.requested" | "plugin.surface.message";
  readonly projectKey: string;
  readonly entityId: string;
  readonly revision: number;
  readonly snapshot: Readonly<Record<string, unknown>>;
}

// ---------------------------------------------------------------------------
// Host bridge envelope
// ---------------------------------------------------------------------------

/**
 * Outbound (web -> host) message types produced by this layer.
 * The bridge never forwards arbitrary types; this is a closed whitelist.
 */
export type WebMessageType =
  | "app.ready"
  | "host.firstRunSubmitted"
  | "host.loginSubmitted"
  | "host.startupRetryRequested"
  | "host.startupCancelRequested"
  | "database.openRequested"
  | "table.selected"
  | "table.pageRequested"
  | "table.updateCellRequested"
  | "table.insertRowRequested"
  | "table.deleteRowsRequested"
  // B3 query + state requests.
  | "table.queryRequested"
  | "gridState.saveRequested"
  // B2 paste preview + apply requests.
  | "table.previewPasteRequested"
  | "table.applyPasteRequested"
  // Revision audit + two-phase safe restore requests.
  | "history.queryRequested"
  | "history.previewRestoreRequested"
  | "history.applyRestoreRequested"
  // Web-first document workspace requests. Every local action uses an opaque handle.
  | "document.listRequested"
  | "document.importRequested"
  | "document.externalDropRequested"
  | "document.dragOutRequested"
  | "document.historyRequested"
  | "document.openRequested"
  | "document.previewRequested"
  | "document.revealRequested"
  | "document.relinkRequested"
  // Table-admin requests.
  | "tableAdmin.createRequested"
  | "tableAdmin.deleteRequested"
  | "identifierMappings.listRequested"
  | "identifierMappings.updateAliasesRequested"
  | "identifierMappings.importRequested"
  | "identifierMappings.reconcileRequested"
  | "identifierMappings.deleteRequested"
  | "identifierMappings.purgeRequested"
  | "plugin.catalog.list"
  | "plugin.audit.list"
  | "plugin.cleanup.listPending"
  | "plugin.install.inspect"
  | "plugin.install.commit"
  | "plugin.externalFlow.listCandidates"
  | "plugin.externalFlow.bind"
  | "plugin.lifecycle.setEnabled"
  | "plugin.lifecycle.upgrade"
  | "plugin.lifecycle.rollback"
  | "plugin.lifecycle.resolveDrift"
  | "plugin.lifecycle.uninstall"
  | "plugin.action.describe"
  | "plugin.action.start"
  | "plugin.interaction.resolve"
  | "plugin.task.cancel"
  | "plugin.task.get"
  | "plugin.surface.event"
  // Open the embedded Directus admin (Data Studio) in this webview.
  | "admin.openRequested";

/**
 * Inbound (host -> web) message types consumed by this layer.
 * Unknown inbound types are dropped after a diagnostic callback.
 */
export type HostMessageType =
  | "host.startupStateChanged"
  | "database.opened"
  | "table.pageLoaded"
  | "table.datasetReady"
  | "operation.failed"
  | "table.editSchemaLoaded"
  | "table.editCommitted"
  | "table.editRejected"
  | "table.rowsInserted"
  | "table.rowsDeleted"
  | "directus.changed"
  // B2 paste preview + apply outcomes.
  | "table.pastePreviewReady"
  | "table.pasteApplied"
  // Revision audit + two-phase safe restore outcomes.
  | "history.pageLoaded"
  | "history.restorePreviewReady"
  | "history.restoreApplied"
  // Web-first document workspace outcomes.
  | "document.listLoaded"
  | "document.historyLoaded"
  | "document.actionCompleted"
  | "document.operationFailed"
  | "document.workspaceChanged"
  // Collections-changed notifications.
  | "database.collectionsChanged"
  | "identifierMappings.result"
  | "plugin.catalog.changed"
  | "plugin.task.changed"
  | "plugin.interaction.requested"
  | "plugin.surface.message"
  // Fixed request replies echo their use-case type and requestId.
  | "plugin.catalog.list"
  | "plugin.audit.list"
  | "plugin.cleanup.listPending"
  | "plugin.install.inspect"
  | "plugin.install.commit"
  | "plugin.externalFlow.listCandidates"
  | "plugin.externalFlow.bind"
  | "plugin.lifecycle.setEnabled"
  | "plugin.lifecycle.upgrade"
  | "plugin.lifecycle.rollback"
  | "plugin.lifecycle.resolveDrift"
  | "plugin.lifecycle.uninstall"
  | "plugin.action.describe"
  | "plugin.action.start"
  | "plugin.interaction.resolve"
  | "plugin.task.cancel"
  | "plugin.task.get"
  | "plugin.surface.event";

export interface DirectusChangePayload {
  readonly uid: string;
  readonly collection: string;
  readonly event: "create" | "update" | "delete";
  readonly data: ReadonlyArray<unknown>;
  readonly invalidateQuery: boolean;
}

/** Envelope posted to / received from the host. */
export interface BridgeMessage<P = unknown> {
  readonly type: string;
  readonly requestId?: string;
  readonly payload?: P;
}

/** Map of (inbound) message type -> resolved payload type, for typed handlers. */
export interface HostPayloadMap {
  "host.startupStateChanged": StartupStatePayload;
  "database.opened": DatabaseOpenedPayload;
  "table.pageLoaded": TablePageLoadedPayload;
  "table.datasetReady": DatasetReadyPayload;
  "operation.failed": OperationFailedPayload;
  "table.editSchemaLoaded": EditSchemaResult;
  "table.editCommitted": UpdateCellResult;
  "table.editRejected": MutationErrorPayload;
  "table.rowsInserted": InsertRowResult;
  "table.rowsDeleted": DeleteRowsResult;
  "directus.changed": DirectusChangePayload;
  "table.pastePreviewReady": PastePlan;
  "table.pasteApplied": ApplyPasteResult;
  "history.pageLoaded": HistoryPage;
  "history.restorePreviewReady": RestorePreview;
  "history.restoreApplied": RestoreResult;
  "document.listLoaded": DocumentListLoadedPayload;
  "document.historyLoaded": DocumentHistoryLoadedPayload;
  "document.actionCompleted": DocumentActionCompletedPayload;
  "document.operationFailed": DocumentOperationFailedPayload;
  "document.workspaceChanged": DocumentWorkspaceChangedPayload;
  // Collections-changed notifications.
  "database.collectionsChanged": CollectionsChangedPayload;
  "identifierMappings.result": IdentifierMappingsResult;
  "plugin.catalog.changed": PluginEventEnvelope;
  "plugin.task.changed": PluginEventEnvelope;
  "plugin.interaction.requested": PluginEventEnvelope;
  "plugin.surface.message": PluginSurfaceHostMessage;
  "plugin.catalog.list": readonly PluginSnapshot[];
  "plugin.audit.list": readonly PluginAuditEvent[];
  "plugin.cleanup.listPending": readonly PluginAuditEvent[];
  "plugin.install.inspect": PluginInstallPlan;
  "plugin.install.commit": PluginSnapshot;
  "plugin.externalFlow.listCandidates": readonly PluginExternalFlowCandidate[];
  "plugin.externalFlow.bind": PluginFlowBindingSnapshot;
  "plugin.lifecycle.setEnabled": PluginSnapshot;
  "plugin.lifecycle.upgrade": PluginSnapshot;
  "plugin.lifecycle.rollback": PluginSnapshot;
  "plugin.lifecycle.resolveDrift": PluginSnapshot;
  "plugin.lifecycle.uninstall": PluginUninstallResult;
  "plugin.action.describe": PluginActionAvailability;
  "plugin.action.start": PluginTaskSnapshot;
  "plugin.interaction.resolve": PluginInteractionResolveResult;
  "plugin.task.cancel": PluginTaskSnapshot;
  "plugin.task.get": PluginTaskSnapshot;
  "plugin.surface.event": { readonly accepted: boolean };
}

/** Map of (outbound) message type -> payload type, for typed requests. */
export interface WebPayloadMap {
  "app.ready": Record<string, never>;
  "host.firstRunSubmitted": FirstRunSubmittedPayload;
  "host.loginSubmitted": LoginSubmittedPayload;
  "host.startupRetryRequested": Record<string, never>;
  "host.startupCancelRequested": Record<string, never>;
  "database.openRequested": DatabaseOpenRequestedPayload;
  "table.selected": TableSelectedPayload;
  "table.pageRequested": TablePageRequestedPayload;
  "table.updateCellRequested": UpdateCellRequestedPayload;
  "table.insertRowRequested": InsertRowRequestedPayload;
  "table.deleteRowsRequested": DeleteRowsRequestedPayload;
  // B3 query + state requests.
  "table.queryRequested": TableQueryRequestedPayload;
  "gridState.saveRequested": GridStateSaveRequestedPayload;
  // B2 paste preview + apply requests.
  "table.previewPasteRequested": PreviewPasteRequestedPayload;
  "table.applyPasteRequested": ApplyPasteRequestedPayload;
  // Revision audit + two-phase safe restore requests.
  "history.queryRequested": HistoryQueryPayload;
  "history.previewRestoreRequested": HistoryPreviewRestorePayload;
  "history.applyRestoreRequested": HistoryApplyRestorePayload;
  "document.listRequested": DocumentListRequestedPayload;
  "document.importRequested": DocumentImportRequestedPayload;
  "document.externalDropRequested": DocumentImportRequestedPayload;
  "document.dragOutRequested": DocumentOpaqueHandlePayload;
  "document.historyRequested": DocumentHandlePayload & {
    readonly limit?: number;
    readonly offset?: number;
  };
  "document.openRequested": DocumentHandlePayload;
  "document.previewRequested": DocumentHandlePayload;
  "document.revealRequested": DocumentHandlePayload;
  "document.relinkRequested": DocumentOpaqueHandlePayload;
  // Table-admin requests.
  "tableAdmin.createRequested": TableAdminCreatePayload;
  "tableAdmin.deleteRequested": TableAdminDeletePayload;
  "identifierMappings.listRequested": { readonly search?: string | null };
  "identifierMappings.updateAliasesRequested": {
    readonly mappingId: string;
    readonly aliases: readonly string[];
  };
  "identifierMappings.importRequested": {
    readonly mappings: readonly IdentifierMappingImportItem[];
  };
  "identifierMappings.reconcileRequested": Record<string, never>;
  "identifierMappings.deleteRequested": { readonly mappingId: string };
  "identifierMappings.purgeRequested": Record<string, never>;
  "plugin.catalog.list": { readonly projectKey: string };
  "plugin.audit.list": { readonly projectKey: string; readonly pluginId: string };
  "plugin.cleanup.listPending": { readonly projectKey: string };
  "plugin.install.inspect": {
    readonly projectKey: string;
    readonly projectRevision: string;
    readonly sourceLocation: string;
  };
  "plugin.install.commit": { readonly planId: string; readonly projectRevision: string };
  "plugin.externalFlow.listCandidates": {
    readonly projectKey: string;
    readonly pluginId: string;
    readonly logicalFlowId: string;
  };
  "plugin.externalFlow.bind": {
    readonly projectKey: string;
    readonly pluginId: string;
    readonly logicalFlowId: string;
    readonly directusFlowUuid: string;
    readonly acceptsUnknownSideEffects: boolean;
  };
  "plugin.lifecycle.setEnabled": {
    readonly projectKey: string;
    readonly pluginId: string;
    readonly enabled: boolean;
  };
  "plugin.lifecycle.upgrade": {
    readonly projectKey: string;
    readonly pluginId: string;
    readonly planId: string;
    readonly projectRevision: string;
  };
  "plugin.lifecycle.rollback": { readonly projectKey: string; readonly pluginId: string };
  "plugin.lifecycle.resolveDrift": {
    readonly projectKey: string;
    readonly pluginId: string;
    readonly logicalFlowId: string;
    readonly strategy: "restore" | "detach";
  };
  "plugin.lifecycle.uninstall": {
    readonly projectKey: string;
    readonly pluginId: string;
    readonly cleanupPrivateSettings: boolean;
  };
  "plugin.action.describe": {
    readonly projectKey: string;
    readonly pluginId: string;
    readonly actionId: string;
    readonly context: PluginCommandContext;
  };
  "plugin.action.start": {
    readonly projectKey: string;
    readonly pluginId: string;
    readonly actionId: string;
    readonly context: PluginCommandContext;
    readonly input: Readonly<Record<string, unknown>>;
  };
  "plugin.interaction.resolve": {
    readonly runId: string;
    readonly interactionId: string;
    readonly decision: "approved" | "rejected";
  };
  "plugin.task.cancel": { readonly taskId: string };
  "plugin.task.get": { readonly taskId: string };
  "plugin.surface.event": PluginSurfaceEventPayload;
  "admin.openRequested": {
    readonly floatingButtonEnabled: boolean;
    readonly confirmClose: boolean;
    readonly releaseWhenIdle: boolean;
  };
}

// ---------------------------------------------------------------------------
// Web-first startup lifecycle contracts
// ---------------------------------------------------------------------------

export type StartupPhase = "starting" | "firstRun" | "login" | "ready" | "faulted";

export interface StartupLogEntry {
  readonly time: string;
  readonly source: string;
  readonly message: string;
}

export interface StartupStatePayload {
  readonly phase: StartupPhase;
  readonly stage?: string | null;
  readonly detail?: string | null;
  readonly email?: string | null;
  readonly rememberPassword?: boolean;
  readonly autoLogin?: boolean;
  readonly canRetry?: boolean;
  readonly canCancel?: boolean;
  readonly logs?: readonly StartupLogEntry[];
}

export interface FirstRunSubmittedPayload {
  readonly email: string;
  readonly password: string;
  readonly managedLogin: boolean;
  readonly rememberPassword: boolean;
  readonly autoLogin: boolean;
}

export interface LoginSubmittedPayload {
  readonly email: string;
  readonly password: string;
  readonly otp?: string;
  readonly rememberPassword: boolean;
  readonly autoLogin: boolean;
}

/** Payload produced by the web layer for `table.queryRequested`. */
export interface TableQueryRequestedPayload {
  readonly table: string;
  readonly query: TableQuery;
}

/** Payload produced by the web layer for `gridState.saveRequested`. */
export interface GridStateSaveRequestedPayload {
  readonly databaseId: string;
  readonly table: string;
  readonly state: GridState;
}

/** Payload produced by the web layer for `table.previewPasteRequested`. */
export interface PreviewPasteRequestedPayload {
  readonly collection: string;
  readonly schemaRevision: string;
  readonly selection: SelectionSnapshot;
  readonly startCell: PasteStartCellPayload;
  readonly cells: readonly (readonly PasteCellPayload[])[];
}

/** Payload produced by the web layer for `table.applyPasteRequested`. */
export interface ApplyPasteRequestedPayload {
  readonly collection: string;
  readonly token: string;
  readonly idempotencyKey: string;
}

// ---------------------------------------------------------------------------
// G1 full-field history contracts
// ---------------------------------------------------------------------------

/** The user who triggered a ChangeSet. */
export interface HistoryActor {
  readonly userId: string | null;
  readonly displayName: string | null;
}

/** A scalar field change (text, number, date, boolean, enum, JSON). */
export interface ScalarFieldChange {
  readonly field: string;
  readonly before: unknown;
  readonly after: unknown;
}

/** A relation field change (M2O, O2M, M2M, M2A, file). */
export interface RelationFieldChange {
  readonly field: string;
  readonly kind: "m2o" | "o2m" | "m2m" | "m2a" | "file";
  readonly relatedCollection: string | null;
  readonly relatedItemId: string | null;
  readonly displayValue: string | null;
  readonly beforeItemId: string | null;
  readonly afterItemId: string | null;
  readonly beforeDisplayValue?: string | null;
  readonly afterDisplayValue?: string | null;
  readonly targetAvailable?: boolean;
}

/** One record inside an Activity-grouped change set. */
export interface HistoryRecordChange {
  readonly revisionId: string;
  readonly itemId: string;
  readonly recordLabel: string | null;
  readonly action: string;
  readonly scalarChanges: readonly ScalarFieldChange[];
  readonly relationChanges: readonly RelationFieldChange[];
}

/** One logical business change aggregating scalar + relation field changes. */
export interface HistoryChangeSet {
  readonly rootRevisionId: string;
  readonly activityId: string | null;
  readonly itemId?: string | null;
  readonly recordLabel?: string | null;
  readonly revisionIds?: readonly string[];
  readonly affectedRecords?: number;
  readonly action: string;
  readonly timestamp: string;
  readonly actor: HistoryActor;
  readonly scalarChanges: readonly ScalarFieldChange[];
  readonly relationChanges: readonly RelationFieldChange[];
  readonly recordChanges?: readonly HistoryRecordChange[];
}

/** Paged result of reading ChangeSets. */
export interface HistoryPage {
  readonly collection: string;
  readonly scope?: RevisionHistoryScope;
  readonly itemId?: string | null;
  readonly field?: string | null;
  readonly changeSets: readonly HistoryChangeSet[];
  readonly total: number;
  readonly hasMore?: boolean;
  readonly capabilityHash: string;
  readonly schemaRevision: string;
  readonly archivedDefaultRevisionIds?: Readonly<Record<string, string>>;
}

/** A diagnostic for a single field in a restore preview. */
export interface RestoreDiagnostic {
  readonly field: string;
  readonly classification: "recoverable" | "readonly_system" | "derived" | "sensitive" | "schema_retired" | "permission_denied" | "incompatible" | "relation_unsafe";
  readonly severity: "warning" | "error";
  readonly code: string;
  readonly message: string;
}

/** Result of a restore preview (two-phase safe restore). */
export interface RestorePreview {
  readonly collection: string;
  readonly itemId: string;
  readonly scope?: Exclude<RevisionHistoryScope, "table">;
  readonly field?: string | null;
  readonly targetRevision: string;
  readonly currentHash: string;
  readonly schemaRevision: string;
  readonly scalarChanges: readonly ScalarFieldChange[];
  readonly relationChanges: readonly RelationFieldChange[];
  readonly diagnostics: readonly RestoreDiagnostic[];
  readonly canApply?: boolean;
  readonly restorableFields?: readonly string[];
  readonly token: string;
  readonly expiresAt: string;
}

/** Result of applying a restore. */
export interface RestoreResult {
  readonly collection: string;
  readonly itemId: string;
  readonly restoredToRevision: string;
  readonly newRevisionId: string | null;
  readonly item: Record<string, unknown>;
}

export type RevisionHistoryScope = "table" | "row" | "cell" | "archived";

/** Payload for `history.queryRequested`. Search is always evaluated by the host. */
export interface HistoryQueryPayload {
  readonly collection: string;
  readonly scope: RevisionHistoryScope;
  readonly itemId?: string;
  readonly field?: string;
  readonly search?: string;
  readonly dateFrom?: string;
  readonly dateTo?: string;
  readonly actorId?: string;
  readonly actions?: readonly string[];
  readonly recordId?: string;
  readonly limit: number;
  readonly offset: number;
}

/** Payload for `history.previewRestoreRequested`. */
export interface HistoryPreviewRestorePayload {
  readonly collection: string;
  readonly itemId: string;
  readonly scope: Exclude<RevisionHistoryScope, "table">;
  readonly field?: string;
  readonly targetRevision: string;
}

/** Payload for `history.applyRestoreRequested`. */
export interface HistoryApplyRestorePayload {
  readonly collection: string;
  readonly itemId: string;
  readonly token: string;
}

// ---------------------------------------------------------------------------
// G3 document workspace contracts
// ---------------------------------------------------------------------------

export interface DocumentSummary {
  readonly documentId: string;
  readonly fileName: string;
  readonly mimeType: string | null;
  readonly mainHead: string | null;
  readonly mainHash: string | null;
  readonly status: string;
  readonly linkType: string | null;
  readonly folderRelativePath: string | null;
  readonly isMissing: boolean;
}

export interface DocumentFolderResult {
  readonly collection: string;
  readonly itemId: string;
  readonly folderId: string | null;
  readonly documents: readonly DocumentSummary[];
}

export interface DocumentRevisionEntry {
  readonly revisionId: string;
  readonly schemeName: string | null;
  readonly sequence: number;
  readonly versionLabel: string;
  readonly kind: string;
  readonly hash: string;
  readonly size: number;
  readonly createdAt: string;
  readonly createdBy: string | null;
}

export interface DocumentHistoryResult {
  readonly documentId: string;
  readonly revisions: readonly DocumentRevisionEntry[];
  readonly total: number;
}

export interface DocumentFolderRequestPayload {
  readonly collection: string;
  readonly itemId: string;
}

export interface DocumentHistoryRequestPayload {
  readonly documentId: string;
  readonly limit: number;
  readonly offset: number;
}

export interface DocumentOpenPayload {
  readonly documentId: string;
  readonly fileName: string;
}

export interface DocumentOpenFolderPayload {
  readonly folderId: string | null;
  readonly collection: string;
  readonly itemId: string;
}

export type DocumentBridgeScope =
  | { readonly kind: "global" }
  | {
      readonly kind: "record";
      readonly collection: string;
      readonly itemId: string | number;
    };

export interface DocumentListRequestedPayload {
  readonly scope: DocumentBridgeScope;
  readonly authority: "workspace" | "cloud";
}

export interface DocumentImportRequestedPayload {
  readonly scope: DocumentBridgeScope;
}

export interface DocumentOpaqueHandlePayload {
  readonly handle: string;
}

export interface DocumentHandlePayload {
  readonly entryHandle: string;
}

export interface DocumentBridgeEntry {
  readonly entryHandle: string;
  readonly documentId: string;
  readonly displayName: string;
  readonly mimeType: string | null;
  readonly availability: "available" | "missing" | "unmounted" | "unmanaged" | "unsafe" | "remote";
  readonly previewKind: "web" | "system" | "none";
  readonly currentRevision: string | null;
  readonly linkType: string;
  readonly capabilities: readonly string[];
}

export interface DocumentListLoadedPayload {
  readonly collection: string | null;
  readonly itemId: string | null;
  readonly entries: readonly DocumentBridgeEntry[];
}

export interface DocumentRevisionBridgeEntry {
  readonly revisionHandle: string;
  readonly label: string;
  readonly createdAt: string;
  readonly size: number;
  readonly author: string | null;
}

export interface DocumentHistoryLoadedPayload {
  readonly entryHandle: string;
  readonly revisions: readonly DocumentRevisionBridgeEntry[];
  readonly total: number;
}

export interface DocumentActionCompletedPayload {
  readonly entryHandle: string;
  readonly action: "open" | "preview" | "reveal";
}

export interface DocumentOperationFailedPayload {
  readonly message: string;
  readonly code?: string | null;
}

export interface DocumentWorkspaceChangedPayload {
  readonly reason: "import" | "relink";
  readonly affectedCount: number;
}
