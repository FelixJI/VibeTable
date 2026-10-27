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
    case "autoDate":
      return { autoDate: { role: "createdAt" } };
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

/**
 * 输入期重名预检：按权威 planner 的口径（TrimSpace + 仅活动字段）
 * 在同表现有字段里查找同名冲突；selfFieldId 用于编辑时排除自身。
 * 找不到冲突返回 null，绝不猜测远端状态。
 */
export function findDuplicateDisplayName(
  displayName: string,
  fields: readonly FieldDefinitionV2[],
  selfFieldId: string | null,
): FieldDefinitionV2 | null {
  const name = (displayName ?? "").trim();
  if (!name) return null;
  for (const field of fields) {
    if (selfFieldId && field.identity.fieldId === selfFieldId) continue;
    if (field.lifecycle.state !== "active") continue;
    if (field.displayName.trim() === name) return field;
  }
  return null;
}

/**
 * 服务端稳定错误码的中文友好说明；未收录的编码返回 null，
 * 由界面回退展示原始 code/message 以保留诊断信息。
 * 编码清单核对自 sidecar fieldchange/fieldvalue 权威实现。
 */
const FIELD_SERVICE_ERROR_HINTS: Readonly<Record<string, string>> = {
  "field.display_name.duplicate": "同表已存在同名的活动字段；请换一个显示名称后重试",
  "field.conflict.stale_revision": "表结构在预览后已发生变化；请关闭抽屉重新打开后再试",
  "field.change.schema_conflict": "表结构已被其他操作更新；请重新预览变更",
  "field.change.data_conflict": "表数据在预览后已发生变化；请重新预览变更",
  "field.change.noop": "本次设置与当前字段一致，没有产生实际变更",
  "field.change.confirmation_required": "该操作需要按提示输入确认文字后才能执行",
  "field.change.operation_conflict": "同一操作正在处理中；请稍候重试，不要重复点击",
  "field.capability.unsupported": "当前字段类型不支持该设置或该类型转换",
  "field.conversion.rule_required": "该类型转换需要先选择明确的转换规则",
  "field.contract.invalid": "请求内容与当前契约不一致；请刷新后重试",
  "field.relation.pair_required": "关联字段缺少另一端设置；请补全双向关联信息",
  "field.auto_number.immutable": "自动编号的该规则创建后不可修改",
  "field.auto_number.backfill": "将按当前编号规则为已有记录回填编号",
  "field.auto_number.reformat": "将按新规则重排已有记录编号，序号保持不变",
  "field.auto_date.history_unavailable": "不会回填已有记录的历史时间：旧记录的创建时间可能为空，最后修改时间从下一次修改该记录起才有值",
  "field.auto_date.immutable": "创建时间或最后修改时间角色在创建后不可更改",
  "field.default.invalid": "默认值不符合当前字段约束；请调整默认值或约束",
  "field.constraint.existing_data_invalid": "已有数据不满足新约束；请先处理列出的不兼容记录",
  "field.lookup.target_invalid": "查找引用的目标字段不可用；请重新选择",
  "field.value.invalid": "值不符合字段限制",
  "field.value.required": "该字段为必填",
  "field.value.read_only": "该字段由系统维护，不能手动填写",
};

export function fieldServiceErrorHint(
  code: string | null | undefined,
): string | null {
  if (!code) return null;
  return FIELD_SERVICE_ERROR_HINTS[code] ?? null;
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
