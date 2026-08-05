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
  return clone(draft);
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
          resultType: "text",
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
  };
}
