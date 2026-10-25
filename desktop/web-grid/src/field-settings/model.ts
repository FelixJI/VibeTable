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
    formula?: { language: "cel-v1" | "cel-v2"; source: string };
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
    case "autoNumber":
      return { autoNumber: { prefix: "", start: 1, width: 6 } };
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

/** 展示名称与权威 Schema 的最长限制保持一致（按 Unicode 码位计数）。 */
export const FIELD_DISPLAY_NAME_MAX_LENGTH = 128;

/** 控制字符（C0/C1）与换行分隔符不允许出现在展示名称里。 */
const DISPLAY_NAME_FORBIDDEN = /[\u0000-\u001F\u007F-\u009F\u2028\u2029]/u;

/** 创建/编辑字段前必须先在前端解决的草稿问题；空数组表示可以预览。 */
export function fieldDisplayNameIssues(displayName: string): string[] {
  const issues: string[] = [];
  const name = displayName ?? "";
  if (!name.trim()) issues.push("显示名称不能为空");
  if (Array.from(name).length > FIELD_DISPLAY_NAME_MAX_LENGTH) {
    issues.push(`显示名称最多 ${FIELD_DISPLAY_NAME_MAX_LENGTH} 个字符`);
  }
  if (DISPLAY_NAME_FORBIDDEN.test(name)) {
    issues.push("显示名称不能包含换行或控制字符");
  }
  return issues;
}

function fixedTemporalDefault(draft: FieldDraftV2): unknown {
  const value = draft.value.default.value;
  if (value && typeof value === "object" && "kind" in value) return null;
  return value;
}

/** 自动编号组合 token；等待 generated AutoNumberSpec 发布 dateFormat/order 字段。 */
export type AutoNumberToken = "prefix" | "date" | "number";
export const AUTO_NUMBER_TOKENS: readonly AutoNumberToken[] = ["prefix", "date", "number"];
export type AutoNumberDateFormat = "yyyyMMdd";

/** 前端自动编号草稿（在现有 prefix/start/width 之上扩展 dateFormat/order）。 */
export interface AutoNumberDraftSpec {
  readonly prefix: string;
  readonly start: number;
  readonly width: number;
  readonly dateFormat?: AutoNumberDateFormat | null;
  readonly order?: readonly AutoNumberToken[];
}

/** 合法顺序 = 三个 token 的完整不重复排列；缺省视为 prefix,date,number。 */
export function autoNumberOrderValid(
  order: readonly AutoNumberToken[] | undefined,
): boolean {
  if (!order) return true;
  if (order.length !== AUTO_NUMBER_TOKENS.length) return false;
  const seen = new Set<string>(order);
  return seen.size === order.length && AUTO_NUMBER_TOKENS.every(token => seen.has(token));
}

/**
 * 首个编号样例：按 order 组合前缀、可选 yyyyMMdd 日期段与补零序号；
 * 日期段禁用时跳过 date token。仅用于呈现配置效果。
 */
export function autoNumberSample(
  spec: AutoNumberDraftSpec,
  now: Date = new Date(),
): string {
  const start = Number.isSafeInteger(spec.start) && spec.start >= 1 ? spec.start : 1;
  const width = Number.isInteger(spec.width) && spec.width >= 1 && spec.width <= 16
    ? spec.width : 6;
  const date = spec.dateFormat
    ? `${String(now.getUTCFullYear()).padStart(4, "0")}`
      + `${String(now.getUTCMonth() + 1).padStart(2, "0")}`
      + `${String(now.getUTCDate()).padStart(2, "0")}`
    : null;
  const parts = new Map<AutoNumberToken, string>([
    ["prefix", spec.prefix],
    ["number", String(start).padStart(width, "0")],
    ...(date ? ([["date", date]] as const) : []),
  ]);
  return (spec.order ?? AUTO_NUMBER_TOKENS)
    .filter(token => token !== "date" || date !== null)
    .map(token => parts.get(token) ?? "")
    .join("");
}

/**
 * 草稿级值限制：展示名称长度/字符类型，以及各逻辑类型默认值
 * 与自身约束（数值范围/仅整数、文本长度、日期时间固定值格式）的一致性。
 * 返回可直接呈现的中文问题列表；不猜测后端权威校验，只拦截确定无效的草稿。
 */
export function fieldDraftIssues(draft: FieldDraftV2 | null): string[] {
  if (!draft) return [];
  const issues = [...fieldDisplayNameIssues(draft.displayName)];
  if (!draft.value.default.enabled) return issues;
  const value = draft.value.default.value;
  const constraints = draft.constraints;
  if (draft.logicalType === "number" && typeof value === "number" && Number.isFinite(value)) {
    const { min, max } = constraints.range;
    if (typeof min === "number" && value < min) {
      issues.push(`默认值不能小于最小值 ${min}`);
    }
    if (typeof max === "number" && value > max) {
      issues.push(`默认值不能大于最大值 ${max}`);
    }
    if (draft.storage.options.onlyInt && !Number.isInteger(value)) {
      issues.push("默认值必须是整数（该字段仅允许整数）");
    }
  }
  if (["text", "editor", "email", "url"].includes(draft.logicalType)
    && typeof value === "string") {
    const { min, max } = constraints.length;
    const length = Array.from(value).length;
    if (typeof min === "number" && min > 0 && length < min) {
      issues.push(`默认值长度不能少于 ${min} 个字符`);
    }
    if (typeof max === "number" && max > 0 && length > max) {
      issues.push(`默认值长度不能超过 ${max} 个字符`);
    }
  }
  if (["date", "dateTime", "time"].includes(draft.logicalType)
    && typeof fixedTemporalDefault(draft) === "string") {
    const text = String(value).trim();
    if (text) {
      const pattern = draft.logicalType === "date"
        ? /^\d{4}-\d{2}-\d{2}$/u
        : draft.logicalType === "dateTime"
          // 权威 sidecar fieldvalue/kernel.go normalizeDateTime 要求
          // Go time.RFC3339Nano：字面 T、秒与时区（Z|±hh:mm）必需；
          // 这是新写入的固定默认值，空格分隔/无秒/无时区都会被后端拒绝。
          ? /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$/u
          : /^\d{2}:\d{2}(:\d{2}(\.\d{1,6})?)?$/u;
      if (!pattern.test(text)) {
        issues.push(draft.logicalType === "date"
          ? "默认日期需要 YYYY-MM-DD 格式"
          : draft.logicalType === "dateTime"
            ? "默认日期时间需要含秒和时区的 RFC3339 格式（如 2026-08-12T09:00:00Z）"
            : "默认时间需要 HH:mm:ss 格式");
      }
    }
  }
  return issues;
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
