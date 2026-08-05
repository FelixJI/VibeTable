import { computed, ref } from "vue";
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
  const result = ref<FieldSettingsDescribeResultV2 | null>(null);
  const original = ref<FieldDraftV2 | null>(null);
  const draft = ref<FieldDraftV2 | null>(null);
  const action = ref<FieldChangeActionV2>("create");
  const conversionRule = ref("");
  const confirmation = ref("");
  const backupReceipt = ref("");
  const plan = ref<FieldChangePlanV2 | null>(null);
  const receipt = ref<FieldApplyReceiptV2 | null>(null);
  const migration = ref<FieldMigrationStatusV2 | null>(null);
  const recycled = ref<readonly FieldDefinitionV2[]>([]);
  const confirmations = ref<string[]>([]);
  const error = ref<string | null>(null);
  const errorCode = ref<string | null>(null);
  const relationPair = ref<RelationPairDraft | null>(null);
  const relationTables = ref<readonly RelationTableOption[]>([]);
  const relationSourceSchema = ref<SchemaSnapshot | null>(null);
  const relationTargetSchema = ref<SchemaSnapshot | null>(null);
  const relationCatalogLoading = ref(false);
  const relationCatalogError = ref<string | null>(null);

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
    relationPair.value = null;
    relationTables.value = [];
    relationSourceSchema.value = null;
    relationTargetSchema.value = null;
    relationCatalogLoading.value = false;
    relationCatalogError.value = null;
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
    relationPair.value = null;
    relationTables.value = [];
    relationSourceSchema.value = null;
    relationTargetSchema.value = null;
    relationCatalogLoading.value = false;
    relationCatalogError.value = null;
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
    relationCatalogLoading, relationCatalogError,
    capabilities, capability, sourceCapability, dirty, isExisting, canPlan,
    confirmationsComplete, canApply, beginOpen, load, changeType, patchDraft,
    restoreRecommended, patchRelationPair, setRelationTables, beginRelationCatalog,
    setRelationSchema, failRelationCatalog,
    invalidatePlan, setConversionRule, setConfirmation, setBackupReceipt,
    beginPlan, setPlan, beginApply, setReceipt, setMigration,
    setRecycled, fail, resetFailure, close,
  };
});
