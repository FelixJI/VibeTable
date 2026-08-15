import { computed, ref, shallowRef } from "vue";
import { defineStore } from "pinia";
import type {
  CapabilityV2,
  FieldApplyReceiptV2,
  FieldChangeActionV2,
  FieldChangePlanV2,
  FieldDefinitionV2,
  FieldDraftV2,
  FieldMigrationStatusV2,
  FieldSettingsDescribeResultV2,
  LogicalTypeV2,
  FormulaDraftValidationResult,
  SchemaSnapshot,
} from "@/contracts";
import {
  draftFromCapability,
  draftFromDefinition,
  draftsEqual,
  initialDraft,
  replaceDraftType,
} from "./model";

export type FieldSettingsPhase =
  | "idle"
  | "loading"
  | "editing"
  | "planning"
  | "planned"
  | "applying"
  | "migrating"
  | "failed";

export type RelationPairDraft = NonNullable<
  import("@/contracts").FieldChangeIntentV2["relationPair"]
>;

export interface RelationTableOption {
  readonly tableId: string;
  readonly displayName: string;
}

export const useFieldSettingsStore = defineStore("field-settings", () => {
  const open = ref(false);
  const phase = ref<FieldSettingsPhase>("idle");
  const result = shallowRef<FieldSettingsDescribeResultV2 | null>(null);
  const original = shallowRef<FieldDraftV2 | null>(null);
  const draft = shallowRef<FieldDraftV2 | null>(null);
  const action = ref<FieldChangeActionV2>("create");
  const conversionRule = ref("");
  const confirmation = ref("");
  const backupReceipt = ref("");
  const plan = shallowRef<FieldChangePlanV2 | null>(null);
  const receipt = shallowRef<FieldApplyReceiptV2 | null>(null);
  const migration = shallowRef<FieldMigrationStatusV2 | null>(null);
  const recycled = shallowRef<readonly FieldDefinitionV2[]>([]);
  const confirmations = ref<string[]>([]);
  const error = ref<string | null>(null);
  const errorCode = ref<string | null>(null);
  const relationPair = ref<RelationPairDraft | null>(null);
  const relationTables = ref<readonly RelationTableOption[]>([]);
  const relationSourceSchema = ref<SchemaSnapshot | null>(null);
  const relationTargetSchema = ref<SchemaSnapshot | null>(null);
  const relationCatalogLoading = ref(false);
  const relationCatalogError = ref<string | null>(null);
  const lookupSchemas = ref<readonly SchemaSnapshot[]>([]);
  const lookupCatalogLoading = ref(false);
  const lookupCatalogError = ref<string | null>(null);
  const lookupMaxDepth = ref(8);
  const formulaSourceSchema = ref<SchemaSnapshot | null>(null);
  const formulaTargetSchemas = ref<Readonly<Record<string, SchemaSnapshot>>>({});
  const formulaCatalogLoading = ref(false);
  const formulaCatalogError = ref<string | null>(null);
  const formulaValidation = ref<FormulaDraftValidationResult | null>(null);
  const formulaValidatedSource = ref("");
  const formulaValidating = ref(false);
  const formulaValidationError = ref<string | null>(null);
  const formulaPreviewValue = ref<unknown>(undefined);
  const formulaPreviewReady = ref(false);
  const formulaPreviewing = ref(false);
  const formulaPreviewError = ref<string | null>(null);
  const formulaPreviewNote = ref<string | null>(null);

  const capabilities = computed<readonly CapabilityV2[]>(
    () => result.value?.capabilities ?? [],
  );
  const capability = computed(
    () => capabilities.value.find((item) => item.logicalType === draft.value?.logicalType)
      ?? null,
  );
  const sourceCapability = computed(() => {
    const sourceType = result.value?.definition?.logicalType ?? draft.value?.logicalType;
    return capabilities.value.find((item) => item.logicalType === sourceType) ?? null;
  });
  const dirty = computed(() => !draftsEqual(original.value, draft.value));
  const isExisting = computed(() => result.value?.definition !== null);
  const canPlan = computed(() =>
    !!result.value
    && (action.value !== "update" || dirty.value)
    && phase.value !== "loading"
    && phase.value !== "planning"
    && phase.value !== "applying"
    && !!draft.value?.displayName.trim()
    && (draft.value?.logicalType !== "relation" || !!draft.value.relation?.targetTableId)
    && (draft.value?.logicalType !== "relation" || !!draft.value.relation?.displayFieldId)
    && (draft.value?.logicalType !== "lookup" || !!draft.value.lookup?.targetFieldId)
    && (draft.value?.logicalType !== "lookup"
      || !!draft.value.lookup?.path.length
      && draft.value.lookup.path.length <= lookupMaxDepth.value
      && draft.value.lookup.path.every(step => !!step.relationFieldId))
    && (draft.value?.logicalType !== "formula" || !!draft.value.formula?.source.trim())
    && (action.value !== "create" || draft.value?.logicalType !== "relation"
      || !!relationPair.value?.reciprocalDisplayName.trim()
      && !!relationPair.value?.sourceDisplayFieldId)
    && (action.value !== "convert"
      || sourceCapability.value?.conversionRules.length === 0
      || conversionRule.value.length > 0),
  );
  const confirmationsComplete = computed(() =>
    plan.value?.confirmations.every((item) => confirmations.value.includes(item)) ?? false,
  );

  function resetCatalogState(): void {
    relationPair.value = null;
    relationTables.value = [];
    relationSourceSchema.value = null;
    relationTargetSchema.value = null;
    relationCatalogLoading.value = false;
    relationCatalogError.value = null;
    lookupSchemas.value = [];
    lookupCatalogLoading.value = false;
    lookupCatalogError.value = null;
    lookupMaxDepth.value = 8;
    formulaSourceSchema.value = null;
    formulaTargetSchemas.value = {};
    formulaCatalogLoading.value = false;
    formulaCatalogError.value = null;
    formulaValidation.value = null;
    formulaValidatedSource.value = "";
    formulaValidating.value = false;
    formulaValidationError.value = null;
    resetFormulaPreview();
  }
  const canApply = computed(() =>
    phase.value === "planned"
    && plan.value?.canApply === true
    && plan.value.errors.length === 0
    && confirmationsComplete.value,
  );

  function beginOpen(): void {
    open.value = true;
    phase.value = "loading";
    conversionRule.value = "";
    confirmation.value = "";
    backupReceipt.value = "";
    error.value = null;
    errorCode.value = null;
    plan.value = null;
    receipt.value = null;
    migration.value = null;
    confirmations.value = [];
    resetCatalogState();
  }

  function load(described: FieldSettingsDescribeResultV2): void {
    result.value = described;
    const next = initialDraft(described);
    original.value = described.definition ? draftFromDefinition(described.definition) : null;
    draft.value = next;
    action.value = described.definition ? "update" : "create";
    relationPair.value = !described.definition && next.logicalType === "relation"
      ? {
        reciprocalDisplayName: "",
        reciprocalCardinality: "many",
        sourceDisplayFieldId: "",
      }
      : null;
    phase.value = "editing";
    error.value = null;
    errorCode.value = null;
  }

  function changeType(logicalType: LogicalTypeV2): void {
    if (!draft.value) return;
    const originalType = result.value?.definition?.logicalType;
    if (originalType && originalType !== logicalType
      && !sourceCapability.value?.conversionTargets.includes(logicalType)) {
      error.value = `field.capability.unsupported: ${originalType} → ${logicalType}`;
      errorCode.value = "field.capability.unsupported";
      return;
    }
    draft.value = replaceDraftType(draft.value, capabilities.value, logicalType);
    action.value = originalType && originalType !== logicalType ? "convert" : (
      result.value?.definition ? "update" : "create"
    );
    conversionRule.value = "";
    relationPair.value = !result.value?.definition && logicalType === "relation"
      ? {
        reciprocalDisplayName: "",
        reciprocalCardinality: "many",
        sourceDisplayFieldId: "",
      }
      : null;
    invalidatePlan();
  }

  function patchRelationPair(value: Partial<RelationPairDraft>): void {
    if (!relationPair.value) return;
    relationPair.value = { ...relationPair.value, ...value };
    invalidatePlan();
  }

  function setRelationTables(values: readonly RelationTableOption[]): void {
    relationTables.value = values;
  }

  function beginRelationCatalog(): void {
    relationCatalogLoading.value = true;
    relationCatalogError.value = null;
  }

  function setRelationSchema(kind: "source" | "target", value: SchemaSnapshot): void {
    if (kind === "source") relationSourceSchema.value = value;
    else relationTargetSchema.value = value;
    relationCatalogLoading.value = false;
    relationCatalogError.value = null;
  }

  function failRelationCatalog(reason: unknown): void {
    relationCatalogLoading.value = false;
    relationCatalogError.value = reason instanceof Error ? reason.message : String(reason);
  }

  function beginLookupCatalog(): void {
    lookupCatalogLoading.value = true;
    lookupCatalogError.value = null;
  }

  function setLookupSchemas(values: readonly SchemaSnapshot[]): void {
    lookupSchemas.value = values;
    lookupCatalogLoading.value = false;
    lookupCatalogError.value = null;
  }

  function setLookupMaxDepth(value: number): void {
    lookupMaxDepth.value = Math.max(1, Math.trunc(value));
  }

  function failLookupCatalog(reason: unknown): void {
    lookupCatalogLoading.value = false;
    lookupCatalogError.value = reason instanceof Error ? reason.message : String(reason);
  }

  function beginFormulaCatalog(): void {
    formulaCatalogLoading.value = true;
    formulaCatalogError.value = null;
  }

  function setFormulaCatalog(
    source: SchemaSnapshot,
    targets: Readonly<Record<string, SchemaSnapshot>>,
  ): void {
    formulaSourceSchema.value = source;
    formulaTargetSchemas.value = targets;
    formulaCatalogLoading.value = false;
    formulaCatalogError.value = null;
  }

  function failFormulaCatalog(reason: unknown): void {
    formulaCatalogLoading.value = false;
    formulaCatalogError.value = reason instanceof Error ? reason.message : String(reason);
  }

  function beginFormulaValidation(source: string): void {
    formulaValidatedSource.value = source;
    formulaValidation.value = null;
    formulaValidating.value = true;
    formulaValidationError.value = null;
  }

  function setFormulaValidation(source: string, value: FormulaDraftValidationResult): void {
    formulaValidatedSource.value = source;
    formulaValidation.value = value;
    formulaValidating.value = false;
    formulaValidationError.value = null;
  }

  function failFormulaValidation(source: string, reason: unknown): void {
    formulaValidatedSource.value = source;
    formulaValidation.value = null;
    formulaValidating.value = false;
    formulaValidationError.value = reason instanceof Error ? reason.message : String(reason);
  }

  function resetFormulaPreview(): void {
    formulaPreviewValue.value = undefined;
    formulaPreviewReady.value = false;
    formulaPreviewing.value = false;
    formulaPreviewError.value = null;
    formulaPreviewNote.value = null;
  }

  function beginFormulaPreview(): void {
    resetFormulaPreview();
    formulaPreviewing.value = true;
  }

  function setFormulaPreview(value: unknown): void {
    formulaPreviewValue.value = value;
    formulaPreviewReady.value = true;
    formulaPreviewing.value = false;
  }

  function setFormulaPreviewNote(note: string): void {
    resetFormulaPreview();
    formulaPreviewNote.value = note;
  }

  function failFormulaPreview(reason: unknown): void {
    formulaPreviewReady.value = false;
    formulaPreviewing.value = false;
    formulaPreviewError.value = reason instanceof Error ? reason.message : String(reason);
  }

  function patchDraft(patch: Partial<FieldDraftV2>): void {
    if (!draft.value) return;
    draft.value = { ...draft.value, ...patch };
    invalidatePlan();
  }

  function restoreRecommended(): void {
    if (!draft.value || !capability.value) return;
    const current = draft.value;
    const recommended = draftFromCapability(
      capability.value,
      current.displayName,
    );
    draft.value = {
      ...recommended,
      help: current.help,
      ...(current.select ? { select: current.select } : {}),
      ...(current.relation ? { relation: current.relation } : {}),
      ...(current.autoDate ? { autoDate: current.autoDate } : {}),
      ...(current.formula ? { formula: current.formula } : {}),
      ...(current.lookup ? { lookup: current.lookup } : {}),
    };
    invalidatePlan();
  }

  function setConversionRule(value: string): void {
    if (conversionRule.value === value) return;
    conversionRule.value = value;
    invalidatePlan();
  }

  function setConfirmation(value: string): void {
    if (confirmation.value === value) return;
    confirmation.value = value;
    invalidatePlan();
  }

  function setBackupReceipt(value: string): void {
    if (backupReceipt.value === value) return;
    backupReceipt.value = value;
    invalidatePlan();
  }

  function invalidatePlan(): void {
    if (phase.value === "planned") phase.value = "editing";
    plan.value = null;
    confirmations.value = [];
  }

  function beginPlan(nextAction?: FieldChangeActionV2): void {
    if (nextAction) action.value = nextAction;
    phase.value = "planning";
    plan.value = null;
    confirmations.value = [];
    error.value = null;
    errorCode.value = null;
    resetCatalogState();
  }

  function setPlan(next: FieldChangePlanV2): void {
    plan.value = next;
    phase.value = "planned";
    confirmations.value = [];
  }

  function beginApply(): void {
    phase.value = "applying";
    error.value = null;
    errorCode.value = null;
  }

  function setReceipt(next: FieldApplyReceiptV2): void {
    receipt.value = next;
    if (next.migrationJobId) {
      phase.value = "migrating";
    } else {
      phase.value = "editing";
      if (next.definition) {
        original.value = draftFromDefinition(next.definition);
        draft.value = draftFromDefinition(next.definition);
      }
    }
  }

  function setMigration(next: FieldMigrationStatusV2): void {
    migration.value = next;
    phase.value = ["completed", "cancelled", "failed", "rolled_back"].includes(next.phase)
      ? (next.phase === "failed" ? "failed" : "editing")
      : "migrating";
    if (next.error) {
      error.value = next.error.message;
      errorCode.value = next.error.code;
    }
  }

  function setRecycled(fields: readonly FieldDefinitionV2[]): void {
    recycled.value = fields;
  }

  function fail(reason: unknown): void {
    const candidate = reason as Error & { readonly code?: string };
    error.value = candidate instanceof Error ? candidate.message : String(reason);
    errorCode.value = candidate?.code ?? null;
    phase.value = "failed";
  }

  function resetFailure(): void {
    error.value = null;
    errorCode.value = null;
    if (result.value) phase.value = plan.value ? "planned" : "editing";
  }

  function close(): void {
    open.value = false;
    phase.value = "idle";
    result.value = null;
    original.value = null;
    draft.value = null;
    plan.value = null;
    receipt.value = null;
    migration.value = null;
    recycled.value = [];
    confirmations.value = [];
    conversionRule.value = "";
    confirmation.value = "";
    backupReceipt.value = "";
    error.value = null;
    errorCode.value = null;
  }

  return {
    open, phase, result, original, draft, action, conversionRule, confirmation,
    backupReceipt, plan, receipt, migration, recycled, confirmations, error,
    errorCode, relationPair, relationTables, relationSourceSchema, relationTargetSchema,
    relationCatalogLoading, relationCatalogError, lookupSchemas,
    lookupCatalogLoading, lookupCatalogError, lookupMaxDepth,
    formulaSourceSchema, formulaTargetSchemas, formulaCatalogLoading,
    formulaCatalogError, formulaValidation, formulaValidatedSource,
    formulaValidating, formulaValidationError,
    formulaPreviewValue, formulaPreviewReady, formulaPreviewing,
    formulaPreviewError, formulaPreviewNote,
    capabilities, capability, sourceCapability, dirty, isExisting, canPlan,
    confirmationsComplete, canApply, beginOpen, load, changeType, patchDraft,
    restoreRecommended, patchRelationPair, setRelationTables, beginRelationCatalog,
    setRelationSchema, failRelationCatalog,
    beginLookupCatalog, setLookupSchemas, setLookupMaxDepth, failLookupCatalog,
    beginFormulaCatalog, setFormulaCatalog, failFormulaCatalog,
    beginFormulaValidation, setFormulaValidation, failFormulaValidation,
    resetFormulaPreview, beginFormulaPreview, setFormulaPreview,
    setFormulaPreviewNote, failFormulaPreview,
    invalidatePlan, setConversionRule, setConfirmation, setBackupReceipt,
    beginPlan, setPlan, beginApply, setReceipt, setMigration,
    setRecycled, fail, resetFailure, close,
  };
});
