export const SCHEMA_V2_CONTRACT = "vibetable.schema.v2" as const;

export const SCHEMA_V2_LOGICAL_TYPES = [
  "text",
  "editor",
  "number",
  "bool",
  "date",
  "dateTime",
  "time",
  "autoDate",
  "email",
  "url",
  "select",
  "multiSelect",
  "relation",
  "file",
  "geoPoint",
  "json",
  "formula",
  "lookup",
] as const;

export type LogicalTypeV2 = (typeof SCHEMA_V2_LOGICAL_TYPES)[number];

export interface FieldIdentityV2 {
  readonly fieldId: string;
  readonly physicalName: string;
  readonly providerFieldId: string;
}

export interface LifecycleV2 {
  readonly state: "active" | "retired";
  readonly retiredAt: string | null;
}

export interface DefaultSpecV2 {
  readonly enabled: boolean;
  readonly value: unknown;
  readonly source: "recommended" | "user";
  readonly defaultsVersion: number;
}

export interface PresenceSpecV2 {
  readonly mode: "companion" | "native" | "computed";
  readonly providerFieldId?: string;
  readonly physicalName?: string;
}

export interface ValueSpecV2 {
  readonly required: boolean;
  readonly default: DefaultSpecV2;
  readonly presence: PresenceSpecV2;
}

export interface ConstraintSpecV2 {
  readonly unique: {
    readonly enabled: boolean;
    readonly blankPolicy: "ignoreMissing";
  };
  readonly range: {
    readonly min: number | string | null;
    readonly max: number | string | null;
  };
  readonly length: { readonly min: number | null; readonly max: number | null };
  readonly pattern: { readonly enabled: boolean; readonly value: string };
  readonly domains: { readonly only: readonly string[]; readonly except: readonly string[] };
  readonly selection: { readonly min: number; readonly max: number | null };
}

export interface StorageSpecV2 {
  readonly kind:
    | "pocketbase-text"
    | "pocketbase-editor"
    | "pocketbase-number"
    | "pocketbase-bool"
    | "pocketbase-date"
    | "pocketbase-autodate"
    | "pocketbase-email"
    | "pocketbase-url"
    | "pocketbase-select"
    | "pocketbase-relation"
    | "pocketbase-file"
    | "pocketbase-geo-point"
    | "pocketbase-json"
    | "computed";
  readonly options: {
    readonly onlyInt: boolean;
    readonly maxSize: number;
    readonly convertURLs: boolean;
    readonly presentable: boolean;
  };
}

export interface DisplaySpecV2 {
  readonly kind:
    | "text"
    | "editor"
    | "number"
    | "bool"
    | "date"
    | "dateTime"
    | "time"
    | "email"
    | "url"
    | "select"
    | "relation"
    | "file"
    | "geoPoint"
    | "json"
    | "readonly";
  readonly preset: string;
  readonly displayScale: number;
  readonly scaleMode: string;
  readonly trimTrailingZeros: boolean;
  readonly useGrouping: boolean;
  readonly currency: string;
  readonly percentStorage: string;
  readonly unit: string | null;
  readonly precision: string;
  readonly timezone: string;
  readonly mode: string;
  readonly indent?: number;
  readonly trueLabel: string;
  readonly falseLabel: string;
}

export interface SelectOptionV2 {
  readonly optionId: string;
  readonly label: string;
  readonly color: string;
  readonly order: number;
  readonly state: "active" | "retired";
}

export interface FieldDefinitionV2 {
  readonly contract: typeof SCHEMA_V2_CONTRACT;
  readonly identity: FieldIdentityV2;
  readonly displayName: string;
  readonly help: string;
  readonly logicalType: LogicalTypeV2;
  readonly lifecycle: LifecycleV2;
  readonly value: ValueSpecV2;
  readonly constraints: ConstraintSpecV2;
  readonly storage: StorageSpecV2;
  readonly display: DisplaySpecV2;
  readonly select?: { readonly options: readonly SelectOptionV2[] };
  readonly relation?: {
    readonly targetTableId: string;
    readonly cardinality: "one" | "many";
    readonly deletePolicy: "setNull" | "restrict" | "cascade";
    readonly displayFieldId: string;
    readonly pairId?: string;
    readonly reciprocalFieldId?: string;
  };
  readonly file?: {
    readonly maxFiles: number;
    readonly maxBytesPerFile: number;
    readonly allowedMimeTypes: readonly string[];
    readonly thumbs: readonly string[];
    readonly protected: boolean;
  };
  readonly json?: {
    readonly rootType: "any" | "object" | "array" | "string" | "number" | "boolean" | "null";
    readonly maxSize: number;
    readonly schema: Readonly<Record<string, unknown>>;
  };
  readonly autoDate?: { readonly role: "createdAt" | "updatedAt" };
  readonly formula?: {
    readonly language: "cel-v1";
    readonly source: string;
    readonly resultType: LogicalTypeV2;
  };
  readonly lookup?: {
    readonly path: readonly { readonly relationFieldId: string }[];
    readonly targetFieldId: string;
  };
}

export interface RecommendedValuesV2 {
  readonly defaultsVersion: number;
  readonly value: ValueSpecV2;
  readonly constraints: ConstraintSpecV2;
  readonly storage: StorageSpecV2;
  readonly display: DisplaySpecV2;
  readonly file?: FieldDefinitionV2["file"];
  readonly json?: FieldDefinitionV2["json"];
}

type FieldDraftBaseV2 = Omit<
  FieldDefinitionV2,
  "contract" | "identity" | "lifecycle" | "formula"
>;

export type FieldDraftV2 = FieldDraftBaseV2 & {
  readonly formula?: {
    readonly language: "cel-v1";
    readonly source: string;
  };
};

export interface CapabilityV2 {
  readonly logicalType: LogicalTypeV2;
  readonly generalSettings: readonly string[];
  readonly advancedSettings: readonly string[];
  readonly dangerSettings: readonly string[];
  readonly recommended: RecommendedValuesV2;
  readonly supportsRequired: boolean;
  readonly supportsDefault: boolean;
  readonly supportsUnique: boolean;
  readonly needsPresence: boolean;
  readonly displayPresets: readonly string[];
  readonly conversionTargets: readonly LogicalTypeV2[];
  readonly conversionRules: readonly string[];
  readonly compileStrategy: string;
  readonly userCreatable: boolean;
}

export type FieldChangeActionV2 =
  | "create"
  | "update"
  | "retire"
  | "restore"
  | "purge"
  | "convert"
  | "backfill";

export interface ActorV2 {
  readonly id: string;
  readonly kind: string;
}

export interface FieldChangeIntentV2 {
  readonly action: FieldChangeActionV2;
  readonly tableId: string;
  readonly fieldId: string;
  readonly expectedSchemaRevision: string;
  readonly expectedDataRevision: number | null;
  readonly draft: FieldDraftV2 | null;
  readonly actor: ActorV2;
  readonly conversionRule: string;
  readonly confirmation: string;
  readonly backupReceipt: string;
  readonly relationPair?: {
    readonly reciprocalDisplayName: string;
    readonly reciprocalCardinality: "one" | "many";
    readonly sourceDisplayFieldId: string;
  };
}

export interface RelatedFieldChangeV2 {
  readonly tableId: string;
  readonly fieldId: string;
  readonly before: FieldDefinitionV2 | null;
  readonly after: FieldDefinitionV2 | null;
  readonly expectedSchemaRevision: string;
}

export interface FieldDiagnosticV2 {
  readonly code: string;
  readonly path: string;
  readonly message: string;
  readonly details: Readonly<Record<string, unknown>>;
}

export interface FieldChangePlanV2 {
  readonly contract: typeof SCHEMA_V2_CONTRACT;
  readonly planId: string;
  readonly planHash: string;
  readonly expiresAt: string;
  readonly intent: FieldChangeIntentV2;
  readonly before: FieldDefinitionV2 | null;
  readonly after: FieldDefinitionV2 | null;
  readonly classes: readonly (
    | "display"
    | "metadata"
    | "constraint"
    | "schema"
    | "migration"
    | "danger"
  )[];
  readonly expectedSchemaRevision: string;
  readonly expectedDataRevision: number | null;
  readonly impact: {
    readonly records: number;
    readonly missing: number;
    readonly ambiguous: number;
    readonly failures: readonly { readonly recordId: string; readonly reason: string }[];
    readonly dependencies: readonly {
      readonly kind: string;
      readonly id: string;
      readonly name: string;
    }[];
  };
  readonly steps: readonly {
    readonly kind: string;
    readonly details: Readonly<Record<string, unknown>>;
  }[];
  readonly warnings: readonly FieldDiagnosticV2[];
  readonly errors: readonly FieldDiagnosticV2[];
  readonly confirmations: readonly string[];
  readonly createsMigration: boolean;
  readonly canApply: boolean;
  readonly relatedChanges?: readonly RelatedFieldChangeV2[];
}

export interface FieldApplyReceiptV2 {
  readonly contract: typeof SCHEMA_V2_CONTRACT;
  readonly operationId: string;
  readonly planId: string;
  readonly action: FieldChangeActionV2;
  readonly tableId: string;
  readonly fieldId: string;
  readonly schemaRevision: string;
  readonly definition: FieldDefinitionV2 | null;
  readonly migrationJobId: string;
  readonly related?: readonly {
    readonly tableId: string;
    readonly fieldId: string;
    readonly schemaRevision: string;
    readonly definition: FieldDefinitionV2 | null;
  }[];
}

export interface FieldMigrationStatusV2 {
  readonly contract: typeof SCHEMA_V2_CONTRACT;
  readonly jobId: string;
  readonly planId: string;
  readonly phase:
    | "planned"
    | "validating"
    | "ready"
    | "copying"
    | "verifying"
    | "switching"
    | "completed"
    | "cancelled"
    | "failed"
    | "cleaning"
    | "rolled_back";
  readonly processed: number;
  readonly total: number;
  readonly canCancel: boolean;
  readonly error: FieldDiagnosticV2 | null;
  readonly updatedAt: string;
}

export interface FieldSettingsDescribeResultV2 {
  readonly contract: typeof SCHEMA_V2_CONTRACT;
  readonly tableId: string;
  readonly fieldId: string;
  readonly schemaRevision: string;
  readonly dataRevision: number;
  readonly definition: FieldDefinitionV2 | null;
  readonly capabilities: readonly CapabilityV2[];
  readonly recommendedDefaultsVersion: number;
}

export interface FieldRecycleBinResultV2 {
  readonly contract: typeof SCHEMA_V2_CONTRACT;
  readonly fields: readonly FieldDefinitionV2[];
}

export interface FieldApplyRequestV2 {
  readonly planId: string;
  readonly planHash: string;
  readonly operationId: string;
  readonly actor: ActorV2;
  readonly confirmations: readonly string[];
  readonly protectionSnapshotId?: string;
}

const FIELD_KEYS = [
  "contract",
  "identity",
  "displayName",
  "help",
  "logicalType",
  "lifecycle",
  "value",
  "constraints",
  "storage",
  "display",
  "select",
  "relation",
  "file",
  "json",
  "autoDate",
  "formula",
  "lookup",
] as const;

export function parseFieldDefinitionV2(value: unknown): FieldDefinitionV2 {
  const field = {
    ...exactObject(value, "$", FIELD_KEYS, FIELD_KEYS.slice(0, 10)),
  };
  for (const key of [
    "select", "relation", "file", "json", "autoDate", "formula", "lookup",
  ]) {
    if (field[key] === null) delete field[key];
  }
  if (field.contract !== SCHEMA_V2_CONTRACT) fail("$.contract", "unsupported contract");
  if (!SCHEMA_V2_LOGICAL_TYPES.includes(field.logicalType as LogicalTypeV2)) {
    fail("$.logicalType", "unsupported logical type");
  }
  const identity = exactObject(
    field.identity,
    "$.identity",
    ["fieldId", "physicalName", "providerFieldId"],
  );
  const lifecycle = exactObject(
    field.lifecycle,
    "$.lifecycle",
    ["state", "retiredAt"],
  );
  const valueSpec = exactObject(field.value, "$.value", ["required", "default", "presence"]);
  const defaultSpec = exactObject(valueSpec.default, "$.value.default", [
    "enabled", "value", "source", "defaultsVersion",
  ]);
  exactObject(valueSpec.presence, "$.value.presence", [
    "mode", "providerFieldId", "physicalName",
  ], ["mode"]);
  const constraints = exactObject(field.constraints, "$.constraints", [
    "unique", "range", "length", "pattern", "domains", "selection",
  ]);
  const unique = exactObject(
    constraints.unique,
    "$.constraints.unique",
    ["enabled", "blankPolicy"],
  );
  const range = exactObject(constraints.range, "$.constraints.range", ["min", "max"]);
  const length = exactObject(constraints.length, "$.constraints.length", ["min", "max"]);
  const pattern = exactObject(
    constraints.pattern,
    "$.constraints.pattern",
    ["enabled", "value"],
  );
  const domains = exactObject(
    constraints.domains,
    "$.constraints.domains",
    ["only", "except"],
  );
  const selection = exactObject(
    constraints.selection,
    "$.constraints.selection",
    ["min", "max"],
  );
  const storage = exactObject(field.storage, "$.storage", ["kind", "options"]);
  const storageOptions = exactObject(storage.options, "$.storage.options", [
    "onlyInt", "maxSize", "convertURLs", "presentable",
  ]);
  const display = exactObject(field.display, "$.display", [
    "kind", "preset", "displayScale", "scaleMode", "trimTrailingZeros",
    "useGrouping", "currency", "percentStorage", "unit", "precision",
    "timezone", "mode", "indent", "trueLabel", "falseLabel",
  ], [
    "kind", "preset", "displayScale", "scaleMode", "trimTrailingZeros",
    "useGrouping", "currency", "percentStorage", "unit", "precision",
    "timezone", "mode", "trueLabel", "falseLabel",
  ]);
  expectString(field.displayName, "$.displayName");
  expectString(field.help, "$.help", true);
  expectString(identity.fieldId, "$.identity.fieldId");
  expectString(identity.physicalName, "$.identity.physicalName");
  expectString(identity.providerFieldId, "$.identity.providerFieldId");
  expectEnum(lifecycle.state, "$.lifecycle.state", ["active", "retired"]);
  if (lifecycle.retiredAt !== null) {
    expectString(lifecycle.retiredAt, "$.lifecycle.retiredAt");
  }
  expectBoolean(valueSpec.required, "$.value.required");
  expectBoolean(defaultSpec.enabled, "$.value.default.enabled");
  expectEnum(defaultSpec.source, "$.value.default.source", ["recommended", "user"]);
  expectSafeInteger(defaultSpec.defaultsVersion, "$.value.default.defaultsVersion");
  const presence = valueSpec.presence as Readonly<Record<string, unknown>>;
  expectEnum(presence.mode, "$.value.presence.mode", [
    "companion", "native", "computed",
  ]);
  expectBoolean(unique.enabled, "$.constraints.unique.enabled");
  expectEnum(unique.blankPolicy, "$.constraints.unique.blankPolicy", [
    "ignoreMissing",
  ]);
  expectNullableRangeValue(range.min, "$.constraints.range.min");
  expectNullableRangeValue(range.max, "$.constraints.range.max");
  expectNullableSafeInteger(length.min, "$.constraints.length.min");
  expectNullableSafeInteger(length.max, "$.constraints.length.max");
  expectBoolean(pattern.enabled, "$.constraints.pattern.enabled");
  expectString(pattern.value, "$.constraints.pattern.value", true);
  expectStringArray(domains.only, "$.constraints.domains.only");
  expectStringArray(domains.except, "$.constraints.domains.except");
  expectSafeInteger(selection.min, "$.constraints.selection.min");
  expectNullableSafeInteger(selection.max, "$.constraints.selection.max");
  expectBoolean(storageOptions.onlyInt, "$.storage.options.onlyInt");
  expectBoolean(storageOptions.convertURLs, "$.storage.options.convertURLs");
  expectBoolean(storageOptions.presentable, "$.storage.options.presentable");
  expectSafeInteger(storageOptions.maxSize, "$.storage.options.maxSize");
  expectEnum(storage.kind, "$.storage.kind", [
    "pocketbase-text", "pocketbase-editor", "pocketbase-number",
    "pocketbase-bool", "pocketbase-date", "pocketbase-autodate",
    "pocketbase-email", "pocketbase-url", "pocketbase-select",
    "pocketbase-relation", "pocketbase-file", "pocketbase-geo-point",
    "pocketbase-json", "computed",
  ]);
  expectEnum(display.kind, "$.display.kind", [
    "text", "editor", "number", "bool", "date", "dateTime", "time",
    "email", "url", "select", "relation", "file", "geoPoint", "json",
    "readonly",
  ]);
  expectEnum(display.scaleMode, "$.display.scaleMode", ["max", "fixed"]);
  expectEnum(display.percentStorage, "$.display.percentStorage", [
    "ratio", "percent",
  ]);
  expectEnum(display.precision, "$.display.precision", [
    "exact", "day", "minute", "second", "millisecond",
  ]);
  expectSafeInteger(display.displayScale, "$.display.displayScale");
  if (display.indent !== undefined) {
    expectSafeInteger(display.indent, "$.display.indent");
    if (![0, 2, 4].includes(display.indent as number)) {
      fail("$.display.indent", "expected one of 0, 2, 4");
    }
  }
  validateOptionalFieldSpecs(field);
  validateLogicalTypeSpec(field, field.logicalType as LogicalTypeV2);
  return field as unknown as FieldDefinitionV2;
}

export function parseFieldSettingsDescribeResultV2(
  value: unknown,
): FieldSettingsDescribeResultV2 {
  const result = { ...exactObject(value, "$", [
    "contract", "tableId", "fieldId", "schemaRevision", "dataRevision",
    "definition", "capabilities", "recommendedDefaultsVersion",
  ]) };
  expectContract(result.contract, "$.contract");
  expectString(result.tableId, "$.tableId");
  expectString(result.fieldId, "$.fieldId", true);
  expectString(result.schemaRevision, "$.schemaRevision");
  expectSafeInteger(result.dataRevision, "$.dataRevision");
  expectSafeInteger(result.recommendedDefaultsVersion, "$.recommendedDefaultsVersion");
  if (result.definition !== null) {
    result.definition = parseFieldDefinitionV2(result.definition);
  }
  expectArray(result.capabilities, "$.capabilities").forEach(parseCapabilityV2);
  return result as unknown as FieldSettingsDescribeResultV2;
}

export function parseFieldChangePlanV2(value: unknown): FieldChangePlanV2 {
  const plan = { ...exactObject(value, "$", [
    "contract", "planId", "planHash", "expiresAt", "intent", "before", "after",
    "classes", "expectedSchemaRevision", "expectedDataRevision", "impact", "steps",
    "warnings", "errors", "confirmations", "createsMigration", "canApply",
    "relatedChanges",
  ], [
    "contract", "planId", "planHash", "expiresAt", "intent", "before", "after",
    "classes", "expectedSchemaRevision", "expectedDataRevision", "impact", "steps",
    "warnings", "errors", "confirmations", "createsMigration", "canApply",
  ]) };
  expectContract(plan.contract, "$.contract");
  ["planId", "planHash", "expiresAt", "expectedSchemaRevision"].forEach((key) =>
    expectString(plan[key], `$.${key}`));
  parseIntent(plan.intent, "$.intent");
  if (plan.before !== null) plan.before = parseFieldDefinitionV2(plan.before);
  if (plan.after !== null) plan.after = parseFieldDefinitionV2(plan.after);
  expectStringArray(plan.classes, "$.classes");
  (plan.classes as readonly unknown[]).forEach((item, index) =>
    expectEnum(item, `$.classes[${index}]`, [
      "display", "metadata", "constraint", "schema", "migration", "danger",
    ]));
  expectNullableSafeInteger(plan.expectedDataRevision, "$.expectedDataRevision");
  const impact = exactObject(plan.impact, "$.impact", [
    "records", "missing", "ambiguous", "failures", "dependencies",
  ]);
  ["records", "missing", "ambiguous"].forEach((key) =>
    expectSafeInteger(impact[key], `$.impact.${key}`));
  expectArray(impact.failures, "$.impact.failures").forEach((item, index) => {
    const failure = exactObject(item, `$.impact.failures[${index}]`, ["recordId", "reason"]);
    expectString(failure.recordId, `$.impact.failures[${index}].recordId`);
    expectString(failure.reason, `$.impact.failures[${index}].reason`);
  });
  expectArray(impact.dependencies, "$.impact.dependencies").forEach((item, index) => {
    const dependency = exactObject(item, `$.impact.dependencies[${index}]`, [
      "kind", "id", "name",
    ]);
    ["kind", "id", "name"].forEach((key) =>
      expectString(dependency[key], `$.impact.dependencies[${index}].${key}`));
  });
  expectArray(plan.steps, "$.steps").forEach((item, index) => {
    const step = exactObject(item, `$.steps[${index}]`, ["kind", "details"]);
    expectString(step.kind, `$.steps[${index}].kind`);
    exactObject(step.details, `$.steps[${index}].details`, Object.keys(
      step.details as Readonly<Record<string, unknown>>,
    ), []);
  });
  expectArray(plan.warnings, "$.warnings").forEach((item, index) =>
    parseDiagnostic(item, `$.warnings[${index}]`));
  expectArray(plan.errors, "$.errors").forEach((item, index) =>
    parseDiagnostic(item, `$.errors[${index}]`));
  expectStringArray(plan.confirmations, "$.confirmations");
  expectBoolean(plan.createsMigration, "$.createsMigration");
  expectBoolean(plan.canApply, "$.canApply");
  if (plan.relatedChanges !== undefined) {
    expectArray(plan.relatedChanges, "$.relatedChanges").forEach((item, index) => {
      const path = `$.relatedChanges[${index}]`;
      const related = exactObject(item, path, [
        "tableId", "fieldId", "before", "after", "expectedSchemaRevision",
      ]);
      ["tableId", "fieldId", "expectedSchemaRevision"].forEach(key =>
        expectString(related[key], `${path}.${key}`));
      if (related.before !== null) parseFieldDefinitionV2(related.before);
      if (related.after !== null) parseFieldDefinitionV2(related.after);
    });
  }
  return plan as unknown as FieldChangePlanV2;
}

export function parseFieldApplyReceiptV2(value: unknown): FieldApplyReceiptV2 {
  const receipt = { ...exactObject(value, "$", [
    "contract", "operationId", "planId", "action", "tableId", "fieldId",
    "schemaRevision", "definition", "migrationJobId", "related",
  ], [
    "contract", "operationId", "planId", "action", "tableId", "fieldId",
    "schemaRevision", "definition", "migrationJobId",
  ]) };
  expectContract(receipt.contract, "$.contract");
  ["operationId", "planId", "action", "tableId", "fieldId", "schemaRevision",
    "migrationJobId"].forEach((key) => expectString(receipt[key], `$.${key}`, key === "migrationJobId"));
  if (receipt.definition !== null) {
    receipt.definition = parseFieldDefinitionV2(receipt.definition);
  }
  if (receipt.related !== undefined) {
    expectArray(receipt.related, "$.related").forEach((item, index) => {
      const path = `$.related[${index}]`;
      const related = exactObject(item, path, [
        "tableId", "fieldId", "schemaRevision", "definition",
      ]);
      ["tableId", "fieldId", "schemaRevision"].forEach(key =>
        expectString(related[key], `${path}.${key}`));
      if (related.definition !== null) parseFieldDefinitionV2(related.definition);
    });
  }
  return receipt as unknown as FieldApplyReceiptV2;
}

export function parseFieldMigrationStatusV2(value: unknown): FieldMigrationStatusV2 {
  const status = exactObject(value, "$", [
    "contract", "jobId", "planId", "phase", "processed", "total", "canCancel",
    "error", "updatedAt",
  ]);
  expectContract(status.contract, "$.contract");
  ["jobId", "planId", "phase", "updatedAt"].forEach((key) =>
    expectString(status[key], `$.${key}`));
  expectEnum(status.phase, "$.phase", [
    "planned", "validating", "ready", "copying", "verifying", "switching",
    "completed", "cancelled", "failed", "cleaning", "rolled_back",
  ]);
  expectSafeInteger(status.processed, "$.processed");
  expectSafeInteger(status.total, "$.total");
  expectBoolean(status.canCancel, "$.canCancel");
  if (status.error !== null) parseDiagnostic(status.error, "$.error");
  return status as unknown as FieldMigrationStatusV2;
}

export function parseFieldRecycleBinResultV2(value: unknown): FieldRecycleBinResultV2 {
  const result = {
    ...exactObject(value, "$", ["contract", "fields"]),
  };
  expectContract(result.contract, "$.contract");
  result.fields = expectArray(result.fields, "$.fields").map(parseFieldDefinitionV2);
  return result as unknown as FieldRecycleBinResultV2;
}

function validateOptionalFieldSpecs(field: Readonly<Record<string, unknown>>): void {
  if (field.select !== undefined) {
    const select = exactObject(field.select, "$.select", ["options"]);
    if (!Array.isArray(select.options)) fail("$.select.options", "expected array");
    select.options.forEach((option, index) => {
      const path = `$.select.options[${index}]`;
      const item = exactObject(
        option,
        path,
        ["optionId", "label", "color", "order", "state"],
      );
      expectString(item.optionId, `${path}.optionId`);
      expectString(item.label, `${path}.label`);
      expectString(item.color, `${path}.color`, true);
      expectSafeInteger(item.order, `${path}.order`);
      expectEnum(item.state, `${path}.state`, ["active", "retired"]);
    });
  }
  if (field.relation !== undefined) {
    const relation = exactObject(field.relation, "$.relation", [
      "targetTableId", "cardinality", "deletePolicy", "displayFieldId",
      "pairId", "reciprocalFieldId",
    ], ["targetTableId", "cardinality", "deletePolicy", "displayFieldId"]);
    ["targetTableId", "cardinality", "deletePolicy", "displayFieldId"].forEach(
      key => expectString(relation[key], `$.relation.${key}`, key === "displayFieldId"),
    );
    expectEnum(relation.cardinality, "$.relation.cardinality", ["one", "many"]);
    expectEnum(relation.deletePolicy, "$.relation.deletePolicy", [
      "setNull", "restrict", "cascade",
    ]);
    if (relation.pairId !== undefined) expectString(relation.pairId, "$.relation.pairId");
    if (relation.reciprocalFieldId !== undefined) {
      expectString(relation.reciprocalFieldId, "$.relation.reciprocalFieldId");
    }
  }
  if (field.file !== undefined) {
    const file = exactObject(field.file, "$.file", [
      "maxFiles", "maxBytesPerFile", "allowedMimeTypes", "thumbs", "protected",
    ]);
    expectSafeInteger(file.maxFiles, "$.file.maxFiles");
    expectSafeInteger(file.maxBytesPerFile, "$.file.maxBytesPerFile");
    expectStringArray(file.allowedMimeTypes, "$.file.allowedMimeTypes");
    expectStringArray(file.thumbs, "$.file.thumbs");
    expectBoolean(file.protected, "$.file.protected");
  }
  if (field.json !== undefined) {
    const json = exactObject(field.json, "$.json", [
      "rootType", "maxSize", "schema",
    ]);
    expectEnum(json.rootType, "$.json.rootType", [
      "any", "object", "array", "string", "number", "boolean", "null",
    ]);
    expectSafeInteger(json.maxSize, "$.json.maxSize");
    exactObject(
      json.schema,
      "$.json.schema",
      Object.keys(json.schema as Readonly<Record<string, unknown>>),
      [],
    );
  }
  if (field.autoDate !== undefined) {
    const autoDate = exactObject(field.autoDate, "$.autoDate", ["role"]);
    expectEnum(autoDate.role, "$.autoDate.role", ["createdAt", "updatedAt"]);
  }
  if (field.formula !== undefined) {
    const formula = exactObject(
      field.formula,
      "$.formula",
      ["language", "source", "resultType"],
    );
    expectEnum(formula.language, "$.formula.language", ["cel-v1"]);
    expectString(formula.source, "$.formula.source");
    expectEnum(
      formula.resultType,
      "$.formula.resultType",
      SCHEMA_V2_LOGICAL_TYPES,
    );
  }
  if (field.lookup !== undefined) {
    const lookup = exactObject(field.lookup, "$.lookup", ["path", "targetFieldId"]);
    const path = expectArray(lookup.path, "$.lookup.path");
    if (path.length < 1 || path.length > 8) fail("$.lookup.path", "expected one to eight steps");
    path.forEach((item, index) => {
      const step = exactObject(item, `$.lookup.path[${index}]`, ["relationFieldId"]);
      expectString(step.relationFieldId, `$.lookup.path[${index}].relationFieldId`);
    });
    expectString(lookup.targetFieldId, "$.lookup.targetFieldId");
  }
}

function validateLogicalTypeSpec(
  field: Readonly<Record<string, unknown>>,
  logicalType: LogicalTypeV2,
): void {
  const requiredSpec: Partial<Record<LogicalTypeV2, string>> = {
    select: "select",
    multiSelect: "select",
    relation: "relation",
    file: "file",
    json: "json",
    autoDate: "autoDate",
    formula: "formula",
    lookup: "lookup",
  };
  const expected = requiredSpec[logicalType];
  if (expected && field[expected] === undefined) {
    fail(`$.${expected}`, `required for logicalType ${logicalType}`);
  }
  for (const key of ["select", "relation", "file", "json", "autoDate", "formula", "lookup"]) {
    if (field[key] !== undefined && key !== expected) {
      fail(`$.${key}`, `not allowed for logicalType ${logicalType}`);
    }
  }
}

function parseCapabilityV2(value: unknown, index: number): void {
  const path = `$.capabilities[${index}]`;
  const capability = exactObject(value, path, [
    "logicalType", "generalSettings", "advancedSettings", "dangerSettings",
    "recommended", "supportsRequired", "supportsDefault", "supportsUnique",
    "needsPresence", "displayPresets", "conversionTargets", "conversionRules",
    "compileStrategy", "userCreatable",
  ]);
  if (!SCHEMA_V2_LOGICAL_TYPES.includes(capability.logicalType as LogicalTypeV2)) {
    fail(`${path}.logicalType`, "unsupported logical type");
  }
  ["generalSettings", "advancedSettings", "dangerSettings", "displayPresets",
    "conversionTargets", "conversionRules"].forEach((key) =>
    expectStringArray(capability[key], `${path}.${key}`));
  (capability.conversionTargets as readonly unknown[]).forEach((item, targetIndex) =>
    expectEnum(
      item,
      `${path}.conversionTargets[${targetIndex}]`,
      SCHEMA_V2_LOGICAL_TYPES,
    ));
  ["supportsRequired", "supportsDefault", "supportsUnique", "needsPresence",
    "userCreatable"].forEach((key) => expectBoolean(capability[key], `${path}.${key}`));
  expectString(capability.compileStrategy, `${path}.compileStrategy`);
  const recommended = exactObject(capability.recommended, `${path}.recommended`, [
    "defaultsVersion", "value", "constraints", "storage", "display", "file", "json",
  ], ["defaultsVersion", "value", "constraints", "storage", "display"]);
  expectSafeInteger(recommended.defaultsVersion, `${path}.recommended.defaultsVersion`);
}

function parseIntent(value: unknown, path: string): void {
  const intent = exactObject(value, path, [
    "action", "tableId", "fieldId", "expectedSchemaRevision", "expectedDataRevision",
    "draft", "actor", "conversionRule", "confirmation", "backupReceipt", "relationPair",
  ], [
    "action", "tableId", "fieldId", "expectedSchemaRevision", "expectedDataRevision",
    "draft", "actor", "conversionRule", "confirmation", "backupReceipt",
  ]);
  ["action", "tableId", "fieldId", "expectedSchemaRevision", "conversionRule",
    "confirmation", "backupReceipt"].forEach((key) =>
    expectString(intent[key], `${path}.${key}`, ["fieldId", "conversionRule", "confirmation", "backupReceipt"].includes(key)));
  expectEnum(intent.action, `${path}.action`, [
    "create", "update", "retire", "restore", "purge", "convert", "backfill",
  ]);
  expectNullableSafeInteger(intent.expectedDataRevision, `${path}.expectedDataRevision`);
  const actor = exactObject(intent.actor, `${path}.actor`, ["id", "kind"]);
  expectString(actor.id, `${path}.actor.id`);
  expectString(actor.kind, `${path}.actor.kind`);
  if (intent.relationPair !== undefined) {
    const pair = exactObject(intent.relationPair, `${path}.relationPair`, [
      "reciprocalDisplayName", "reciprocalCardinality", "sourceDisplayFieldId",
    ]);
    expectString(pair.reciprocalDisplayName, `${path}.relationPair.reciprocalDisplayName`);
    expectEnum(pair.reciprocalCardinality, `${path}.relationPair.reciprocalCardinality`, [
      "one", "many",
    ]);
    expectString(pair.sourceDisplayFieldId, `${path}.relationPair.sourceDisplayFieldId`);
  }
  if (intent.draft !== null) {
    const draft = exactObject(
      intent.draft,
      `${path}.draft`,
      [
        "displayName", "help", "logicalType", "value", "constraints",
        "storage", "display", "select", "relation", "file", "json",
        "autoDate", "formula", "lookup",
      ],
      ["displayName", "help", "logicalType", "value", "constraints", "storage", "display"],
    );
    let definitionFormula = draft.formula;
    if (draft.formula !== undefined) {
      const formula = exactObject(
        draft.formula,
        `${path}.draft.formula`,
        ["language", "source"],
      );
      definitionFormula = { ...formula, resultType: "text" };
    }
    parseFieldDefinitionV2({
      ...draft,
      formula: definitionFormula,
      contract: SCHEMA_V2_CONTRACT,
      identity: {
        fieldId: "fld_contract_validation",
        physicalName: "f_contract_validation",
        providerFieldId: "pb_contract_validation",
      },
      lifecycle: { state: "active", retiredAt: null },
    });
  }
}

function parseDiagnostic(value: unknown, path: string): void {
  const diagnostic = exactObject(value, path, ["code", "path", "message", "details"]);
  ["code", "path", "message"].forEach((key) =>
    expectString(diagnostic[key], `${path}.${key}`, key === "path"));
  if (diagnostic.details === null || typeof diagnostic.details !== "object"
      || Array.isArray(diagnostic.details)) {
    fail(`${path}.details`, "expected object");
  }
}

function exactObject(
  value: unknown,
  path: string,
  allowed: readonly string[],
  required: readonly string[] = allowed,
): Readonly<Record<string, unknown>> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    fail(path, "expected object");
  }
  const object = value as Readonly<Record<string, unknown>>;
  for (const key of Object.keys(object)) {
    if (!allowed.includes(key)) fail(`${path}.${key}`, "unknown property");
  }
  for (const key of required) {
    if (!(key in object)) fail(`${path}.${key}`, "missing property");
  }
  return object;
}

function expectContract(value: unknown, path: string): void {
  if (value !== SCHEMA_V2_CONTRACT) fail(path, "unsupported contract");
}

function expectString(value: unknown, path: string, allowEmpty = false): void {
  if (typeof value !== "string" || (!allowEmpty && value.length === 0)) {
    fail(path, "expected string");
  }
}

function expectEnum(
  value: unknown,
  path: string,
  allowed: readonly string[],
): void {
  if (typeof value !== "string" || !allowed.includes(value)) {
    fail(path, `expected one of ${allowed.join(", ")}`);
  }
}

function expectBoolean(value: unknown, path: string): void {
  if (typeof value !== "boolean") fail(path, "expected boolean");
}

function expectSafeInteger(value: unknown, path: string): void {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) {
    fail(path, "expected non-negative safe integer");
  }
}

function expectNullableSafeInteger(value: unknown, path: string): void {
  if (value !== null) expectSafeInteger(value, path);
}

function expectNullableRangeValue(value: unknown, path: string): void {
  if (value !== null && typeof value !== "number" && typeof value !== "string") {
    fail(path, "expected number, temporal string, or null");
  }
  if (typeof value === "number" && !Number.isFinite(value)) {
    fail(path, "expected finite number");
  }
}

function expectArray(value: unknown, path: string): unknown[] {
  if (!Array.isArray(value)) fail(path, "expected array");
  return value;
}

function expectStringArray(value: unknown, path: string): void {
  expectArray(value, path).forEach((item, index) =>
    expectString(item, `${path}[${index}]`, true));
}

function fail(path: string, message: string): never {
  throw new Error(`field.contract.invalid at ${path}: ${message}`);
}
