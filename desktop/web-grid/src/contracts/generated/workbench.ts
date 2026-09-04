// Generated from contracts/workbench/workbench.schema.json; do not edit.

export interface ViewFilter {
  readonly fieldId: string;
  readonly operator: "eq" | "ne" | "contains" | "startsWith" | "gt" | "gte" | "lt" | "lte" | "isNull" | "isNotNull";
  readonly value: string | number | boolean | null;
}

export interface ViewSort {
  readonly fieldId: string;
  readonly direction: "asc" | "desc";
}

export interface ViewQuery {
  readonly contractVersion: "1.0";
  readonly tableId: string;
  readonly fields: ReadonlyArray<string>;
  readonly filters: ReadonlyArray<ViewFilter>;
  readonly sorts: ReadonlyArray<ViewSort>;
  readonly cursor: string | null;
  readonly pageSize: number;
}

export interface BindingVariable {
  readonly variableId: string;
  readonly targetFieldId: string;
  readonly operator: "eq" | "ne" | "contains" | "startsWith" | "gt" | "gte" | "lt" | "lte" | "isNull" | "isNotNull";
  readonly source: "literal" | "selectedRecordField";
  readonly sourceBindingId: string | null;
  readonly sourceFieldId: string | null;
  readonly value: string | number | boolean | null;
}

export interface DataBinding {
  readonly bindingId: string;
  readonly query: ViewQuery;
  readonly variables: ReadonlyArray<BindingVariable>;
}

export interface InterfaceAction {
  readonly actionId: string;
  readonly kind: "record.create" | "record.update" | "binding.refresh" | "navigate" | "plugin";
  readonly bindingId: string | null;
  readonly targetPageId: string | null;
  readonly pluginId: string | null;
  readonly pluginActionId: string | null;
  readonly requiresConfirmation: boolean;
}

export interface InterfaceElement {
  readonly elementId: string;
  readonly kind: "section" | "columns" | "tabs" | "text" | "metric" | "chart" | "record-list" | "record-detail" | "form" | "button" | "navigation";
  readonly bindingId: string | null;
  readonly actionId: string | null;
  readonly text: string | null;
  readonly width: "full" | "half" | "third";
  readonly children: ReadonlyArray<InterfaceElement>;
}

export interface InterfacePage {
  readonly pageId: string;
  readonly title: string;
  readonly elements: ReadonlyArray<InterfaceElement>;
}

export interface InterfaceDefinition {
  readonly contractVersion: "1.0";
  readonly interfaceId: string;
  readonly name: string;
  readonly bindings: ReadonlyArray<DataBinding>;
  readonly actions: ReadonlyArray<InterfaceAction>;
  readonly pages: ReadonlyArray<InterfacePage>;
}

export interface InterfaceSnapshot {
  readonly definition: InterfaceDefinition;
  readonly revision: string;
}

export interface InterfaceCommitRequest {
  readonly definition: InterfaceDefinition;
  readonly expectedRevision: string | null;
  readonly idempotencyKey: string;
}

export interface InterfaceListRequest {
}

export interface InterfaceListEntry {
  readonly interfaceId: string;
  readonly name: string;
  readonly revision: string;
}

export interface InterfaceListResult {
  readonly items: ReadonlyArray<InterfaceListEntry>;
}

export interface InterfaceLoadRequest {
  readonly interfaceId: string;
}

export interface InterfaceCancelRequest {
  readonly targetRequestId: string;
}

export interface InterfaceDeleteRequest {
  readonly interfaceId: string;
  readonly expectedRevision: string;
  readonly idempotencyKey: string;
}

export interface InterfaceDeleteResult {
  readonly interfaceId: string;
}

export interface ContentProfile {
  readonly contractVersion: "1.0";
  readonly tableId: string;
  readonly titleFieldId: string;
  readonly bodyFieldId: string;
  readonly summaryFieldId: string | null;
  readonly searchableFieldIds: ReadonlyArray<string>;
}

export interface ContentProfileSnapshot {
  readonly profile: ContentProfile;
  readonly revision: string;
}

export interface ContentProfileLoadRequest {
  readonly tableId: string;
}

export interface ContentProfileCommitRequest {
  readonly profile: ContentProfile;
  readonly expectedRevision: string | null;
  readonly idempotencyKey: string;
}

export interface ContentProfileDeleteRequest {
  readonly tableId: string;
  readonly expectedRevision: string;
  readonly idempotencyKey: string;
}

export interface ContentProfileDeleteResult {
  readonly tableId: string;
}

export interface RecordDocumentLink {
  readonly contractVersion: "1.0";
  readonly linkId: string;
  readonly tableId: string;
  readonly recordId: string;
  readonly documentId: string;
  readonly role: "source" | "reference" | "supporting" | "output";
  readonly order: number;
}

export interface RecordDocumentLinkSnapshot {
  readonly link: RecordDocumentLink;
  readonly revision: string;
}

export interface RecordDocumentLinkListRequest {
  readonly tableId: string;
  readonly recordId: string;
}

export interface RecordDocumentLinkListResult {
  readonly items: ReadonlyArray<RecordDocumentLinkSnapshot>;
}

export interface RecordDocumentLinkCommitRequest {
  readonly link: RecordDocumentLink;
  readonly expectedRevision: string | null;
  readonly idempotencyKey: string;
}

export interface RecordDocumentLinkRepairRequest {
  readonly linkId: string;
  readonly documentId: string;
  readonly expectedRevision: string;
  readonly idempotencyKey: string;
}

export interface RecordDocumentLinkDeleteRequest {
  readonly linkId: string;
  readonly expectedRevision: string;
  readonly idempotencyKey: string;
}

export interface RecordDocumentLinkDeleteResult {
  readonly linkId: string;
}

export interface SearchFilter {
  readonly field: "kind" | "tableId" | "fieldId" | "mimeType" | "extension" | "sizeBytes" | "revisionTime" | "status";
  readonly operator: "eq" | "ne" | "contains" | "gt" | "gte" | "lt" | "lte" | "before" | "after";
  readonly value: string | number | boolean | null;
}

export interface SearchSort {
  readonly field: "score" | "revisionTime" | "title" | "sizeBytes";
  readonly direction: "asc" | "desc";
}

export interface SearchOpenTarget {
  readonly kind: "record" | "attachment" | "file";
  readonly tableId: string | null;
  readonly recordId: string | null;
  readonly fieldId: string | null;
  readonly documentId: string | null;
}

export interface SearchMetadataItem {
  readonly key: string;
  readonly value: string | number | boolean | null;
}

export interface SearchRequest {
  readonly contractVersion: "1.0";
  readonly query: string;
  readonly logic: "and" | "or";
  readonly filters: ReadonlyArray<SearchFilter>;
  readonly sorts: ReadonlyArray<SearchSort>;
  readonly scope: "current" | "history";
  readonly cursor: string | null;
  readonly limit: number;
}

export interface SearchHit {
  readonly contractVersion: "1.0";
  readonly hitId: string;
  readonly kind: "record" | "attachment" | "file";
  readonly canonicalId: string;
  readonly title: string;
  readonly snippet: string | null;
  readonly highlights: ReadonlyArray<string>;
  readonly sourceRevision: string;
  readonly score: number;
  readonly revisionTime: string;
  readonly metadata: ReadonlyArray<SearchMetadataItem>;
  readonly openTarget: SearchOpenTarget;
}

export interface SearchResolveRequest {
  readonly contractVersion: "1.0";
  readonly scope: "current" | "history";
  readonly hit: SearchHit;
}

export interface SearchResolveResult {
  readonly status: "current" | "stale";
  readonly hit: SearchHit;
}

export interface SearchStatus {
  readonly state: "idle" | "building" | "ready" | "degraded" | "failed";
  readonly generation: number;
  readonly checkpoint: string | null;
  readonly processed: number;
  readonly total: number | null;
  readonly errorCode: string | null;
}

export interface FormulaTextPosition {
  readonly line: number;
  readonly character: number;
}

export interface FormulaTextRange {
  readonly start: FormulaTextPosition;
  readonly end: FormulaTextPosition;
}

export interface FormulaAuthorToken {
  readonly range: FormulaTextRange;
  readonly kind: "field" | "relation" | "relationTarget";
  readonly fieldId: string;
  readonly relationFieldId: string | null;
  readonly targetFieldId: string | null;
}

export interface FormulaAuthorDocument {
  readonly displaySource: string;
  readonly tokens: ReadonlyArray<FormulaAuthorToken>;
  readonly documentRevision: number;
}

export interface ComputedCellEnvelope {
  readonly state: "ready" | "updating" | "failed" | "cancelled" | "invalid" | "too_expensive";
  readonly value: string | number | boolean | null;
  readonly definitionVersion: number;
  readonly sourceDataRevision: number;
  readonly dependencyWatermark: number;
  readonly diagnostic: string | null;
}

export interface SchemaAuditEvent {
  readonly eventId: string;
  readonly workspaceId: string;
  readonly tableId: string;
  readonly fieldId: string | null;
  readonly operation: "field.create" | "field.update" | "field.delete" | "table.update";
  readonly schemaRevision: number;
  readonly occurredAt: string;
  readonly actorId: string;
}
