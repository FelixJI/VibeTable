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

export type ColumnDataType = "text" | "integer" | "decimal" | "boolean" | "date";

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
  readonly dateType: "date" | "datetime";
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

/** Field types supported by the backend table_admin contract.
 *  Mirrors backend/contracts/table_admin.py:FieldType and
 *  TableAdminWindow.SupportedFieldTypes. Keep all three in sync. */
export const TABLE_FIELD_TYPES = [
  "string",
  "integer",
  "decimal",
  "date",
  "boolean",
  "text",
] as const;
export type TableFieldType = (typeof TABLE_FIELD_TYPES)[number];

/** Identifier rule for table names and field keys.
 *  Mirrors backend/contracts/table_admin.py:_IDENTIFIER. */
export const TABLE_NAME_PATTERN = /^[A-Za-z][A-Za-z0-9_]{0,63}$/;

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
  // G1 full-field history requests.
  | "history.readRequested"
  | "history.previewRestoreRequested"
  | "history.applyRestoreRequested"
  // G3 document workspace requests.
  | "document.folderRequested"
  | "document.historyRequested"
  | "document.openRequested"
  | "document.openFolderRequested"
  // Table-admin requests.
  | "tableAdmin.createRequested"
  | "tableAdmin.deleteRequested"
  // Open the embedded Directus admin (Data Studio) in this webview.
  | "admin.openRequested";

/**
 * Inbound (host -> web) message types consumed by this layer.
 * Unknown inbound types are dropped after a diagnostic callback.
 */
export type HostMessageType =
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
  // G1 full-field history outcomes.
  | "history.loaded"
  | "history.restorePreviewReady"
  | "history.restoreApplied"
  // G3 document workspace outcomes.
  | "document.folderLoaded"
  | "document.historyLoaded"
  // Collections-changed notifications.
  | "database.collectionsChanged";

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
  "history.loaded": HistoryPage;
  "history.restorePreviewReady": RestorePreview;
  "history.restoreApplied": RestoreResult;
  "document.folderLoaded": DocumentFolderResult;
  "document.historyLoaded": DocumentHistoryResult;
  // Collections-changed notifications.
  "database.collectionsChanged": CollectionsChangedPayload;
}

/** Map of (outbound) message type -> payload type, for typed requests. */
export interface WebPayloadMap {
  "app.ready": Record<string, never>;
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
  // G1 full-field history requests.
  "history.readRequested": HistoryReadPayload;
  "history.previewRestoreRequested": HistoryPreviewRestorePayload;
  "history.applyRestoreRequested": HistoryApplyRestorePayload;
  "document.folderRequested": DocumentFolderRequestPayload;
  "document.historyRequested": DocumentHistoryRequestPayload;
  "document.openRequested": DocumentOpenPayload;
  "document.openFolderRequested": DocumentOpenFolderPayload;
  // Table-admin requests.
  "tableAdmin.createRequested": TableAdminCreatePayload;
  "tableAdmin.deleteRequested": TableAdminDeletePayload;
  // Open Directus admin. Empty payload.
  "admin.openRequested": Record<string, never>;
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
}

/** One logical business change aggregating scalar + relation field changes. */
export interface HistoryChangeSet {
  readonly rootRevisionId: string;
  readonly activityId: string | null;
  readonly action: string;
  readonly timestamp: string;
  readonly actor: HistoryActor;
  readonly scalarChanges: readonly ScalarFieldChange[];
  readonly relationChanges: readonly RelationFieldChange[];
}

/** Paged result of reading ChangeSets. */
export interface HistoryPage {
  readonly collection: string;
  readonly itemId: string;
  readonly changeSets: readonly HistoryChangeSet[];
  readonly total: number;
  readonly capabilityHash: string;
  readonly schemaRevision: string;
}

/** A diagnostic for a single field in a restore preview. */
export interface RestoreDiagnostic {
  readonly field: string;
  readonly classification: "recoverable" | "readonly_system" | "derived" | "sensitive" | "schema_retired";
  readonly severity: "warning" | "error";
  readonly code: string;
  readonly message: string;
}

/** Result of a restore preview (two-phase safe restore). */
export interface RestorePreview {
  readonly collection: string;
  readonly itemId: string;
  readonly targetRevision: string;
  readonly currentHash: string;
  readonly schemaRevision: string;
  readonly scalarChanges: readonly ScalarFieldChange[];
  readonly relationChanges: readonly RelationFieldChange[];
  readonly diagnostics: readonly RestoreDiagnostic[];
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

/** Payload for `history.readRequested`. */
export interface HistoryReadPayload {
  readonly collection: string;
  readonly itemId: string;
  readonly limit: number;
  readonly offset: number;
}

/** Payload for `history.previewRestoreRequested`. */
export interface HistoryPreviewRestorePayload {
  readonly collection: string;
  readonly itemId: string;
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
