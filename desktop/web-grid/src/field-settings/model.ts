import type {
  CapabilityV2,
  FieldChangeActionV2,
  FieldChangeIntentV2,
  FieldDefinitionV2,
  FieldDraftV2,
  FieldSettingsDescribeResultV2,
  LogicalTypeV2,
} from "@/contracts";

function clone<T>(value: T): T {
  // Schema v2 values are JSON contracts. A JSON round-trip intentionally
  // strips every nested Vue proxy before the draft crosses a bridge boundary.
  return JSON.parse(JSON.stringify(value)) as T;
}

export function draftFromDefinition(definition: FieldDefinitionV2): FieldDraftV2 {
  const {
    contract: _contract,
    identity: _identity,
    lifecycle: _lifecycle,
    ...draft
  } = definition;
  const normalized = clone(draft) as Omit<typeof draft, "formula"> & {
    formula?: { language: "cel-v1"; source: string };
  };
  if (definition.formula) {
    normalized.formula = {
      language: definition.formula.language,
      source: definition.formula.source,
    };
  }
  return normalized;
}

export function draftFromCapability(
  capability: CapabilityV2,
  displayName = "",
): FieldDraftV2 {
  const recommended = clone(capability.recommended);
  const specialized = specializedDefaults(capability.logicalType);
  return {
    displayName,
    help: "",
    logicalType: capability.logicalType,
    value: recommended.value,
    constraints: recommended.constraints,
    storage: recommended.storage,
    display: recommended.display,
    ...(recommended.file ? { file: recommended.file } : {}),
    ...(recommended.json ? { json: recommended.json } : {}),
    ...specialized,
  };
}

function specializedDefaults(
  logicalType: LogicalTypeV2,
): Partial<FieldDraftV2> {
  switch (logicalType) {
    case "select":
    case "multiSelect":
      return { select: { options: [] } };
    case "relation":
      return {
        relation: {
          targetTableId: "",
          cardinality: "one",
          deletePolicy: "setNull",
          displayFieldId: "",
        },
      };
    case "formula":
      return {
        formula: {
          language: "cel-v1",
          source: "",
        },
      };
    case "lookup":
      return {
        lookup: {
          path: [{ relationFieldId: "" }],
          targetFieldId: "",
        },
      };
    default:
      return {};
  }
}

export function initialDraft(
  result: FieldSettingsDescribeResultV2,
  preferredType: LogicalTypeV2 = "text",
): FieldDraftV2 {
  if (result.definition) return draftFromDefinition(result.definition);
  const capability = result.capabilities.find(
    (item) => item.logicalType === preferredType && item.userCreatable,
  ) ?? result.capabilities.find((item) => item.userCreatable);
  if (!capability) throw new Error("field.capability.unsupported: 没有可创建的字段类型");
  return draftFromCapability(capability);
}

export function replaceDraftType(
  current: FieldDraftV2,
  capabilities: readonly CapabilityV2[],
  logicalType: LogicalTypeV2,
): FieldDraftV2 {
  const capability = capabilities.find(
    (item) => item.logicalType === logicalType && item.userCreatable,
  );
  if (!capability) {
    throw new Error(`field.capability.unsupported: ${logicalType}`);
  }
  return {
    ...draftFromCapability(capability, current.displayName),
    help: current.help,
  };
}

export function draftsEqual(
  left: FieldDraftV2 | null,
  right: FieldDraftV2 | null,
): boolean {
  return JSON.stringify(left) === JSON.stringify(right);
}

export function buildFieldChangeIntent(input: {
  readonly action: FieldChangeActionV2;
  readonly result: FieldSettingsDescribeResultV2;
  readonly draft: FieldDraftV2 | null;
  readonly conversionRule?: string;
  readonly confirmation?: string;
  readonly backupReceipt?: string;
  readonly relationPairPatch?: FieldChangeIntentV2["relationPairPatch"] | null;
  readonly relationPair?: FieldChangeIntentV2["relationPair"] | null;
}): FieldChangeIntentV2 {
  return {
    action: input.action,
    tableId: input.result.tableId,
    fieldId: input.result.definition?.identity.fieldId ?? input.result.fieldId,
    expectedSchemaRevision: input.result.schemaRevision,
    expectedDataRevision: input.result.dataRevision,
    draft: input.draft ? clone(input.draft) : null,
    actor: { id: "desktop-user", kind: "user" },
    conversionRule: input.conversionRule ?? "",
    confirmation: input.confirmation ?? "",
    backupReceipt: input.backupReceipt ?? "",
    ...(input.relationPair ? { relationPair: clone(input.relationPair) } : {}),
    ...(input.relationPairPatch ? { relationPairPatch: clone(input.relationPairPatch) } : {}),
  };
}

export function relationPairPatchFromDrafts(
  original: FieldDraftV2,
  draft: FieldDraftV2,
  originalPair: NonNullable<FieldChangeIntentV2["relationPair"]>,
  pair: NonNullable<FieldChangeIntentV2["relationPair"]>,
): FieldChangeIntentV2["relationPairPatch"] {
  const before = original.relation;
  const after = draft.relation;
  if (!before || !after) return undefined;
  const patch = {
    ...(original.displayName !== draft.displayName ? { sourceDisplayName: draft.displayName } : {}),
    ...(before.cardinality !== after.cardinality ? { sourceCardinality: after.cardinality } : {}),
    ...(before.displayFieldId !== after.displayFieldId
      ? { sourceDisplayFieldId: after.displayFieldId } : {}),
    ...(originalPair.reciprocalDisplayName !== pair.reciprocalDisplayName
      ? { reciprocalDisplayName: pair.reciprocalDisplayName } : {}),
    ...(originalPair.reciprocalCardinality !== pair.reciprocalCardinality
      ? { reciprocalCardinality: pair.reciprocalCardinality } : {}),
    ...(originalPair.sourceDisplayFieldId !== pair.sourceDisplayFieldId
      ? { reciprocalDisplayFieldId: pair.sourceDisplayFieldId } : {}),
    ...(before.deletePolicy !== after.deletePolicy && after.deletePolicy !== "cascade"
      ? { deletePolicy: after.deletePolicy } : {}),
  };
  if (before.deletePolicy !== after.deletePolicy && after.deletePolicy === "cascade") {
    throw new Error("双向关联仅允许置空或阻止删除");
  }
  if (Object.keys(patch).length === 0) return undefined;
  if (!draftsEqual(original, { ...draft, displayName: original.displayName, relation: before })) {
    throw new Error("请分别保存双向关联设置与其他字段设置");
  }
  return patch;
}
