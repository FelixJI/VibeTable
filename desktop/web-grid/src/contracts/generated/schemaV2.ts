// Generated from contracts/schema-v2/schema.schema.json; do not edit.

export type JsonValue =
  | null
  | boolean
  | number
  | string
  | ReadonlyArray<JsonValue>
  | { readonly [key: string]: JsonValue };

export type LogicalType = "text" | "editor" | "number" | "bool" | "date" | "dateTime" | "time" | "autoDate" | "email" | "url" | "select" | "multiSelect" | "relation" | "file" | "geoPoint" | "json" | "formula" | "lookup";

export interface FieldIdentity {
  readonly fieldId: string;
  readonly physicalName: string;
  readonly providerFieldId: string;
}

export interface Lifecycle {
  readonly state: "active" | "retired";
  readonly retiredAt: string | null;
}

export interface DefaultSpec {
  readonly enabled: boolean;
  readonly value: JsonValue;
  readonly source: "recommended" | "user";
  readonly defaultsVersion: number;
}

export interface PresenceSpec {
  readonly mode: "companion" | "native" | "computed";
  readonly providerFieldId?: string;
  readonly physicalName?: string;
}

export interface ValueSpec {
  readonly required: boolean;
  readonly default: DefaultSpec;
  readonly presence: PresenceSpec;
}

export interface UniqueSpec {
  readonly enabled: boolean;
  readonly blankPolicy: "ignoreMissing";
}

export interface RangeSpec {
  readonly min: number | string | null;
  readonly max: number | string | null;
}

export interface LengthSpec {
  readonly min: number | null;
  readonly max: number | null;
}

export interface PatternSpec {
  readonly enabled: boolean;
  readonly value: string;
}

export interface DomainSpec {
  readonly only: ReadonlyArray<string>;
  readonly except: ReadonlyArray<string>;
}

export interface SelectionSpec {
  readonly min: number;
  readonly max: number | null;
}

export interface ConstraintSpec {
  readonly unique: UniqueSpec;
  readonly range: RangeSpec;
  readonly length: LengthSpec;
  readonly pattern: PatternSpec;
  readonly domains: DomainSpec;
  readonly selection: SelectionSpec;
}

export interface StorageOptions {
  readonly onlyInt: boolean;
  readonly maxSize: number;
  readonly convertURLs: boolean;
  readonly presentable: boolean;
}

export interface StorageSpec {
  readonly kind: "pocketbase-text" | "pocketbase-editor" | "pocketbase-number" | "pocketbase-bool" | "pocketbase-date" | "pocketbase-autodate" | "pocketbase-email" | "pocketbase-url" | "pocketbase-select" | "pocketbase-relation" | "pocketbase-file" | "pocketbase-geo-point" | "pocketbase-json" | "computed";
  readonly options: StorageOptions;
}

export interface DisplaySpec {
  readonly kind: "text" | "editor" | "number" | "bool" | "date" | "dateTime" | "time" | "email" | "url" | "select" | "relation" | "file" | "geoPoint" | "json" | "readonly";
  readonly preset: string;
  readonly displayScale: number;
  readonly scaleMode: "max" | "fixed";
  readonly trimTrailingZeros: boolean;
  readonly useGrouping: boolean;
  readonly currency: string;
  readonly percentStorage: "ratio" | "percent";
  readonly unit: string | null;
  readonly precision: "exact" | "day" | "minute" | "second" | "millisecond";
  readonly timezone: string;
  readonly mode: string;
  readonly indent?: 0 | 2 | 4;
  readonly trueLabel: string;
  readonly falseLabel: string;
}

export interface SelectOption {
  readonly optionId: string;
  readonly label: string;
  readonly color: string;
  readonly order: number;
  readonly state: "active" | "retired";
}

export interface SelectSpec {
  readonly options: ReadonlyArray<SelectOption>;
}

export interface SelectOptionDraft {
  readonly optionId: string;
  readonly label: string;
  readonly color: string;
  readonly order: number;
  readonly state: "active" | "retired";
}

export interface SelectDraftSpec {
  readonly options: ReadonlyArray<SelectOptionDraft>;
}

export interface RelationSpec {
  readonly targetTableId: string;
  readonly cardinality: "one" | "many";
  readonly deletePolicy: "setNull" | "restrict" | "cascade";
  readonly displayFieldId: string;
  readonly pairId?: string;
  readonly reciprocalFieldId?: string;
}

export interface FileSpec {
  readonly maxFiles: number;
  readonly maxBytesPerFile: number;
  readonly allowedMimeTypes: ReadonlyArray<string>;
  readonly thumbs: ReadonlyArray<string>;
  readonly protected: boolean;
}

export interface JSONSpec {
  readonly rootType: "any" | "object" | "array" | "string" | "number" | "boolean" | "null";
  readonly maxSize: number;
  readonly schema: Readonly<Record<string, JsonValue>>;
}

export interface AutoDateSpec {
  readonly role: "createdAt" | "updatedAt";
}

export interface FormulaSpec {
  readonly language: "cel-v1";
  readonly source: string;
  readonly resultType: LogicalType;
}

export interface FormulaDraftSpec {
  readonly language: "cel-v1";
  readonly source: string;
}

export interface LookupSpec {
  readonly path: ReadonlyArray<LookupPathStep>;
  readonly targetFieldId: string;
}

export interface LookupPathStep {
  readonly relationFieldId: string;
}

export interface FieldDefinition {
  readonly contract: "vibetable.schema.v2";
  readonly identity: FieldIdentity;
  readonly displayName: string;
  readonly help: string;
  readonly logicalType: LogicalType;
  readonly lifecycle: Lifecycle;
  readonly value: ValueSpec;
  readonly constraints: ConstraintSpec;
  readonly storage: StorageSpec;
  readonly display: DisplaySpec;
  readonly select?: SelectSpec;
  readonly relation?: RelationSpec;
  readonly file?: FileSpec;
  readonly json?: JSONSpec;
  readonly autoDate?: AutoDateSpec;
  readonly formula?: FormulaSpec;
  readonly lookup?: LookupSpec;
}

export interface FieldDraft {
  readonly displayName: string;
  readonly help: string;
  readonly logicalType: LogicalType;
  readonly value: ValueSpec;
  readonly constraints: ConstraintSpec;
  readonly storage: StorageSpec;
  readonly display: DisplaySpec;
  readonly select?: SelectDraftSpec;
  readonly relation?: RelationSpec;
  readonly file?: FileSpec;
  readonly json?: JSONSpec;
  readonly autoDate?: AutoDateSpec;
  readonly formula?: FormulaDraftSpec;
  readonly lookup?: LookupSpec;
}

export interface RecommendedValues {
  readonly defaultsVersion: number;
  readonly value: ValueSpec;
  readonly constraints: ConstraintSpec;
  readonly storage: StorageSpec;
  readonly display: DisplaySpec;
  readonly file?: FileSpec;
  readonly json?: JSONSpec;
}

export interface Capability {
  readonly logicalType: LogicalType;
  readonly generalSettings: ReadonlyArray<string>;
  readonly advancedSettings: ReadonlyArray<string>;
  readonly dangerSettings: ReadonlyArray<string>;
  readonly recommended: RecommendedValues;
  readonly supportsRequired: boolean;
  readonly supportsDefault: boolean;
  readonly supportsUnique: boolean;
  readonly needsPresence: boolean;
  readonly displayPresets: ReadonlyArray<string>;
  readonly conversionTargets: ReadonlyArray<LogicalType>;
  readonly conversionRules: ReadonlyArray<string>;
  readonly compileStrategy: string;
  readonly userCreatable: boolean;
  readonly filterOperators: ReadonlyArray<string>;
  readonly groupable: boolean;
  readonly summaryOperations: ReadonlyArray<string>;
  readonly relationCardinalities: ReadonlyArray<"one" | "many">;
  readonly relationDeletePolicies: ReadonlyArray<"setNull" | "restrict">;
  readonly lookupMaxDepth: number;
  readonly formulaResultTypeInferred: boolean;
  readonly formulaRelationAggregates: ReadonlyArray<"SUM" | "COUNT" | "AVG" | "MIN" | "MAX" | "ANY">;
}

export interface SchemaSnapshot {
  readonly contract: "vibetable.schema.v2";
  readonly tableId: string;
  readonly displayName: string;
  readonly kind: "base" | "view";
  readonly schemaRevision: string;
  readonly dataRevision: number;
  readonly archivePolicy: ArchivePolicy;
  readonly fields: ReadonlyArray<FieldDefinition>;
  readonly capabilities: ReadonlyArray<Capability>;
}

export interface FormulaValidateRequest {
  readonly tableId: string;
  readonly field: FieldDefinition;
}

export interface FormulaPreviewRequest {
  readonly tableId: string;
  readonly field: FieldDefinition;
  readonly row: Readonly<Record<string, JsonValue>>;
  readonly changedFieldIds: ReadonlyArray<string>;
}

export interface TableCreateIntent {
  readonly displayName: string;
  readonly operationId: string;
  readonly actor: Actor;
}

export interface TableCreateReceipt {
  readonly contract: "vibetable.schema.v2";
  readonly operationId: string;
  readonly tableId: string;
  readonly displayName: string;
  readonly schemaRevision: string;
}

export interface ArchivePolicy {
  readonly mode: "none" | "status" | "deletedAt";
  readonly fieldId: string | null;
  readonly archivedValue: JsonValue;
}

export interface TableSettingsIntent {
  readonly tableId: string;
  readonly expectedSchemaRevision: string;
  readonly archivePolicy: ArchivePolicy;
  readonly operationId: string;
  readonly actor: Actor;
}

export interface TableSettingsReceipt {
  readonly contract: "vibetable.schema.v2";
  readonly operationId: string;
  readonly tableId: string;
  readonly schemaRevision: string;
  readonly archivePolicy: ArchivePolicy;
}

export interface Actor {
  readonly id: string;
  readonly kind: string;
}

export interface RelationPairDraft {
  readonly reciprocalDisplayName: string;
  readonly reciprocalCardinality: "one" | "many";
  readonly sourceDisplayFieldId: string;
}

export interface FieldChangeIntent {
  readonly action: "create" | "update" | "retire" | "restore" | "purge" | "convert" | "backfill";
  readonly tableId: string;
  readonly fieldId: string;
  readonly expectedSchemaRevision: string;
  readonly expectedDataRevision: number | null;
  readonly draft: FieldDraft | null;
  readonly actor: Actor;
  readonly conversionRule: string;
  readonly confirmation: string;
  readonly backupReceipt: string;
  readonly relationPair?: RelationPairDraft;
}

export interface Diagnostic {
  readonly code: string;
  readonly path: string;
  readonly message: string;
  readonly details: Readonly<Record<string, JsonValue>>;
}

export interface FailureSample {
  readonly recordId: string;
  readonly reason: string;
}

export interface DependencyRef {
  readonly kind: string;
  readonly id: string;
  readonly name: string;
}

export interface Impact {
  readonly records: number;
  readonly missing: number;
  readonly ambiguous: number;
  readonly failures: ReadonlyArray<FailureSample>;
  readonly dependencies: ReadonlyArray<DependencyRef>;
}

export interface PlanStep {
  readonly kind: string;
  readonly details: Readonly<Record<string, JsonValue>>;
}

export interface RelatedFieldChange {
  readonly tableId: string;
  readonly fieldId: string;
  readonly before: FieldDefinition | null;
  readonly after: FieldDefinition | null;
  readonly expectedSchemaRevision: string;
}

export interface FieldChangePlan {
  readonly contract: "vibetable.schema.v2";
  readonly planId: string;
  readonly planHash: string;
  readonly expiresAt: string;
  readonly intent: FieldChangeIntent;
  readonly before: FieldDefinition | null;
  readonly after: FieldDefinition | null;
  readonly classes: ReadonlyArray<"display" | "metadata" | "constraint" | "schema" | "migration" | "danger">;
  readonly expectedSchemaRevision: string;
  readonly expectedDataRevision: number | null;
  readonly impact: Impact;
  readonly steps: ReadonlyArray<PlanStep>;
  readonly warnings: ReadonlyArray<Diagnostic>;
  readonly errors: ReadonlyArray<Diagnostic>;
  readonly confirmations: ReadonlyArray<string>;
  readonly createsMigration: boolean;
  readonly canApply: boolean;
  readonly relatedChanges?: ReadonlyArray<RelatedFieldChange>;
}

export interface RelatedApplyReceipt {
  readonly tableId: string;
  readonly fieldId: string;
  readonly schemaRevision: string;
  readonly definition: FieldDefinition | null;
}

export interface ApplyReceipt {
  readonly contract: "vibetable.schema.v2";
  readonly operationId: string;
  readonly planId: string;
  readonly action: "create" | "update" | "retire" | "restore" | "purge" | "convert" | "backfill";
  readonly tableId: string;
  readonly fieldId: string;
  readonly schemaRevision: string;
  readonly definition: FieldDefinition | null;
  readonly migrationJobId: string;
  readonly related?: ReadonlyArray<RelatedApplyReceipt>;
}

export interface ApplyRequest {
  readonly planId: string;
  readonly planHash: string;
  readonly operationId: string;
  readonly actor: Actor;
  readonly confirmations: ReadonlyArray<string>;
  readonly protectionSnapshotId?: string;
}

export interface MigrationStatus {
  readonly contract: "vibetable.schema.v2";
  readonly jobId: string;
  readonly planId: string;
  readonly phase: "planned" | "validating" | "ready" | "copying" | "verifying" | "switching" | "completed" | "cancelled" | "failed" | "cleaning" | "rolled_back";
  readonly processed: number;
  readonly total: number;
  readonly canCancel: boolean;
  readonly error: Diagnostic | null;
  readonly updatedAt: string;
}

export interface FieldSettingsDescribeResult {
  readonly contract: "vibetable.schema.v2";
  readonly tableId: string;
  readonly fieldId: string;
  readonly schemaRevision: string;
  readonly dataRevision: number;
  readonly definition: FieldDefinition | null;
  readonly capabilities: ReadonlyArray<Capability>;
  readonly recommendedDefaultsVersion: number;
}

export interface FieldRecycleBinResult {
  readonly contract: "vibetable.schema.v2";
  readonly fields: ReadonlyArray<FieldDefinition>;
}

export interface FieldValueCorpusOption {
  readonly optionId: string;
  readonly label: string;
}

export interface FieldValueCorpusCase {
  readonly id: string;
  readonly field: string;
  readonly logicalType: LogicalType;
  readonly rawValue: string;
  readonly productValue: JsonValue;
  readonly selectOptions?: ReadonlyArray<FieldValueCorpusOption>;
}

export interface FieldValueEntryCorpus {
  readonly $schema: "vibetable.field-value-entry-corpus.v1";
  readonly description: string;
  readonly cases: ReadonlyArray<FieldValueCorpusCase>;
}

export type SchemaV2Document = FieldDefinition | Capability | FieldChangeIntent | FieldChangePlan | ApplyRequest | ApplyReceipt | SchemaSnapshot | TableCreateIntent | TableCreateReceipt | TableSettingsIntent | TableSettingsReceipt | FormulaValidateRequest | FormulaPreviewRequest | MigrationStatus | FieldSettingsDescribeResult | FieldRecycleBinResult | FieldValueEntryCorpus;
