/**
 * Product relation + realtime lookup wire contracts.
 *
 * Keep these names aligned with backend/contracts/{relation_admin,lookup}.py.
 * Only direct PocketBase relation fields are exposed on the product wire.
 */
import type { ColumnSchema, FilterExpression, SortCondition } from "./index";

export type RelationKind = "m2o" | "o2m" | "m2m";
export type RelationPreset = "standard" | "file" | "files" | "translations";
export type RelationState = "valid" | "readonly" | "invalid";
export type RelationDeletePolicy = "nullify" | "restrict" | "cascade";

export interface RelationDiagnostic {
  readonly code: string;
  readonly message: string;
  readonly severity: "warning" | "error";
}

export interface NormalizedRelationDescriptor {
  readonly relationId: string;
  readonly fieldRef: string;
  readonly sourceCollection: string;
  readonly kind: RelationKind;
  readonly relatedCollection?: string | null;
  readonly manyField?: string | null;
  readonly oneField?: string | null;
  readonly unique: boolean;
  readonly nullable: boolean;
  readonly onDelete: RelationDeletePolicy;
  readonly preset?: RelationPreset;
  readonly selfRelation: boolean;
  readonly managed: boolean;
  readonly pairId?: string;
  readonly reciprocalFieldId?: string;
  readonly quickCreateEligible?: boolean;
  readonly quickCreateReason?: string;
  readonly state: RelationState;
  /** Explicit display template. Never inferred by the renderer. */
  readonly displayTemplate?: string | null;
  readonly diagnostics: readonly RelationDiagnostic[];
}

export interface RelationTargetRef {
  readonly collection: string;
  readonly itemId: string;
  readonly label: string;
  readonly secondaryLabel?: string | null;
}

export interface RelationSearchParams {
  readonly relationId: string;
  readonly query?: string;
  readonly offset?: number;
  readonly limit?: number;
}

export interface RelationSearchResult {
  readonly items: readonly RelationTargetRef[];
  readonly total: number;
}

export interface RelationCreateTargetParams {
  readonly relationId: string;
  readonly label?: string;
  readonly values?: Readonly<Record<string, unknown>>;
  readonly idempotencyKey: string;
}

export interface RelationCreateTargetResult {
  readonly outcome: "committed";
  readonly target: RelationTargetRef;
  readonly requestId: string;
}

export interface RelationDelta {
  readonly relationId: string;
  readonly sourceItemId: string;
  readonly expectedSchemaRevision: string;
  readonly expectedDateUpdated?: string | null;
  readonly adds: ReadonlyArray<{ readonly target: RelationTargetRef }>;
  readonly removes: ReadonlyArray<{
    readonly target: RelationTargetRef;
  }>;
  readonly idempotencyKey: string;
}

export interface RelationDeltaResult {
  readonly outcome: "committed" | "conflict";
  readonly current: readonly RelationTargetRef[];
  readonly schemaRevision: string;
  readonly requestId: string;
}

/** Result of an immediate M2O/O2O update; unlike a staged delta, current is scalar. */
export interface RelationSingleUpdateResult {
  readonly outcome: "committed" | "conflict";
  readonly current: RelationTargetRef | null;
  readonly schemaRevision: string;
  readonly requestId: string;
}

export type LookupOutputType =
  | "text" | "integer" | "decimal" | "boolean" | "date"
  | "datetime" | "time" | "json";
export type LookupState = "valid" | "restricted" | "invalid";
export type LookupCellState = "ok" | "restricted" | "invalid" | "too_expensive";

export interface LookupPathStep {
  readonly relationId: string;
}

export type LookupSource =
  | { readonly kind: "target_field"; readonly fieldRef: string }
  | { readonly kind: "lookup"; readonly lookupId: string };

export interface LookupDiagnostic {
  readonly code: string;
  readonly message: string;
  readonly pathIndex?: number | null;
}

export interface LookupDefinition {
  readonly lookupId: string;
  readonly collection: string;
  readonly fieldKey: string;
  readonly displayName: string;
  readonly path: readonly LookupPathStep[];
  readonly source: LookupSource;
  readonly outputType: LookupOutputType;
  readonly outputScale?: number | null;
  readonly revision: number;
  readonly state: LookupState;
  readonly diagnostics: readonly LookupDiagnostic[];
  readonly dependencies: readonly string[];
}

export interface LookupValueProvenance {
  readonly collection: string;
  readonly collectionLabel: string;
  readonly itemId: string;
  readonly recordLabel: string;
  readonly fieldId: string;
  readonly fieldLabel: string;
  readonly value: unknown;
}

export interface LookupCellValue {
  readonly state: LookupCellState;
  readonly value: unknown;
  readonly provenance: readonly LookupValueProvenance[];
  readonly provenanceTotal: number;
  readonly provenanceTotalKnown: boolean;
  readonly provenanceOffset: number;
  readonly provenanceLimit: number;
  readonly provenanceHasMore: boolean;
  readonly diagnostic?: LookupDiagnostic | null;
}

export interface LookupValuePageParams {
	readonly collection: string;
	readonly fieldRef: string;
	readonly sourceRecordId: string;
	readonly offset: number;
	readonly limit: number;
	readonly schemaRevision: string;
	readonly permissionRevision: string;
	readonly lookupRevision: string;
}

export interface LookupSourcePageIntent {
	readonly sourceRecordId: string;
	readonly fieldRef: string;
	readonly cell: LookupCellValue;
}

export interface LookupGroup {
  readonly fieldRef: string;
  readonly direction?: "asc" | "desc";
}

export interface LookupQueryParams {
  readonly contract: "vibetable.lookup-query.v1";
  readonly collection: string;
  readonly fieldRefs: readonly string[];
  readonly query: {
    readonly filters: readonly FilterExpression[];
    readonly sorts: readonly SortCondition[];
    readonly groups: readonly LookupGroup[];
    readonly offset: number;
    readonly limit: number;
  };
  readonly requestGeneration: number;
  readonly schemaRevision: string;
  readonly permissionRevision: string;
  readonly lookupRevision: string;
}

export interface LookupColumnResult {
  readonly fieldRef: string;
  readonly title: string;
  readonly outputType: LookupOutputType;
  readonly nullable: boolean;
  readonly scale?: number | null;
  readonly state: LookupState;
}

export interface LookupQueryResult {
  readonly contract: "vibetable.lookup-query.v1";
  readonly collection: string;
  readonly requestGeneration: number;
  readonly schemaRevision: string;
  readonly permissionRevision: string;
  readonly lookupRevision: string;
  readonly columns: readonly LookupColumnResult[];
  readonly rows: ReadonlyArray<Record<string, unknown>>;
  readonly groups: ReadonlyArray<{
    readonly path: readonly unknown[];
    readonly key: unknown;
    readonly count: number;
    readonly aggregates: Readonly<Record<string, unknown>>;
    readonly childCursor?: string | null;
  }>;
  readonly offset: number;
  readonly limit: number;
  readonly filteredRows: number;
  readonly totalRows: number;
  /** QueryPort snapshot used to reject responses older than a local mutation. */
  readonly snapshot: {
    readonly snapshotId: string;
    readonly digest: string;
    readonly databaseId: string;
    readonly table: string;
    readonly dataRevision: number;
    readonly schemaRevision: string;
    readonly normalizedQuery: Readonly<Record<string, unknown>>;
  };
}

export interface SchemaSnapshot {
  readonly collection: string;
  readonly primaryKey: string;
  readonly primaryDisplayFieldId?: string;
  readonly columns: readonly ColumnSchema[];
  readonly normalizedRelations: readonly NormalizedRelationDescriptor[];
  readonly schemaRevision: string;
  readonly permissionRevision: string;
  readonly capabilityHash: string;
  readonly lookupRevision: string;
}

export interface RelationLookupCapabilities {
  readonly contract: "vibetable.relation-capabilities.v1";
  readonly relationReadV1: boolean;
  readonly relationEditV1: boolean;
  readonly lookupQueryV1: boolean;
  readonly lookupMaxDepth?: number;
  readonly reason?: "extension_missing" | "incompatible" | "permission_denied" | null;
}

export interface SchemaDescribeParams {
  readonly collection: string;
  readonly requestGeneration: number;
  readonly accepts: readonly ["vibetable.relation-capabilities.v1", "vibetable.lookup-query.v1"];
}

export interface SchemaDescribeResult {
  readonly contract: "vibetable.schema-describe.v1";
  readonly collection: string;
  readonly requestGeneration: number;
  readonly schema: SchemaSnapshot;
  readonly capabilities: RelationLookupCapabilities;
}

export interface LookupListResult {
  readonly collection: string;
  readonly definitions: readonly LookupDefinition[];
  readonly lookupRevision: string;
}

export interface RelationUpdateSingleParams {
  readonly relationId: string;
  readonly sourceItemId: string;
  readonly target: RelationTargetRef | null;
  readonly expectedSchemaRevision: string;
  readonly expectedDateUpdated?: string | null;
  readonly idempotencyKey: string;
}

export interface RelationDeltaPreview {
  readonly delta: RelationDelta;
  readonly current: readonly RelationTargetRef[];
  readonly diagnostics: readonly RelationDiagnostic[];
  readonly canApply: boolean;
}
