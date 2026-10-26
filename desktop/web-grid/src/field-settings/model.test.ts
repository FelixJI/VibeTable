import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import type {
  CapabilityV2,
  FieldDefinitionV2,
  FieldSettingsDescribeResultV2,
} from "@/contracts";
import {
  autoNumberOrderValid,
  autoNumberSample,
  buildFieldChangeIntent,
  draftFromDefinition,
  draftFromCapability,
  draftsEqual,
  fieldDisplayNameIssues,
  fieldDraftIssues,
  fieldServiceErrorHint,
  findDuplicateDisplayName,
  initialDraft,
  replaceDraftType,
  relationPairPatchFromDrafts,
} from "./model";

const fixturePath = resolve(
  import.meta.dirname,
  "../../../../contracts/schema-v2/fixtures/field-definition.json",
);
const capabilityFixturePath = resolve(
  import.meta.dirname,
  "../../../../contracts/schema-v2/fixtures/capability.json",
);

function field(): FieldDefinitionV2 {
  return JSON.parse(readFileSync(fixturePath, "utf8")) as FieldDefinitionV2;
}

describe("field draft issues", () => {
  function draft(overrides: Record<string, unknown> = {}): Record<string, unknown> {
    const recommended = JSON.parse(
      readFileSync(capabilityFixturePath, "utf8") as string,
    ) as { recommended: {
      value: Record<string, unknown>;
      constraints: Record<string, unknown>;
      storage: Record<string, unknown>;
      display: Record<string, unknown>;
    } };
    return {
      displayName: "金额",
      help: "",
      logicalType: "number",
      value: { ...recommended.recommended.value, default: { enabled: false, value: null, source: "user", defaultsVersion: 1 } },
      constraints: recommended.recommended.constraints,
      storage: recommended.recommended.storage,
      display: recommended.recommended.display,
      ...overrides,
    } as Record<string, unknown>;
  }

  it("accepts a normal draft and rejects blank or over-long display names", () => {
    expect(fieldDisplayNameIssues("订单金额")).toEqual([]);
    expect(fieldDisplayNameIssues("   ")).toEqual(["显示名称不能为空"]);
    expect(fieldDisplayNameIssues("a".repeat(129))).toEqual([
      `显示名称最多 128 个字符`,
    ]);
    expect(fieldDisplayNameIssues("订单\n金额")).toEqual([
      "显示名称不能包含换行或控制字符",
    ]);
    expect(fieldDisplayNameIssues("名字\u0007")).toEqual([
      "显示名称不能包含换行或控制字符",
    ]);
  });

  it("enforces per-type default value limits before planning", () => {
    const base = draft() as { constraints: Record<string, unknown> };
    const range = { min: 10, max: 20 };
    expect(fieldDraftIssues(draft({
        value: { required: false, default: { enabled: true, value: 5, source: "user", defaultsVersion: 1 }, presence: { mode: "native" } },
        constraints: { ...base.constraints, range },
      }) as never)).toEqual(["默认值不能小于最小值 10"]);
    expect(fieldDraftIssues(draft({
        storage: { kind: "pocketbase-number", options: { onlyInt: true } },
        value: { required: false, default: { enabled: true, value: 1.5, source: "user", defaultsVersion: 1 }, presence: { mode: "native" } },
      }) as never)).toEqual(["默认值必须是整数（该字段仅允许整数）"]);
    const textConstraints = {
      ...base.constraints,
      length: { min: 2, max: 4 },
    };
    expect(fieldDraftIssues(draft({
        logicalType: "text",
        constraints: textConstraints,
        value: { required: false, default: { enabled: true, value: "过长默认文本", source: "user", defaultsVersion: 1 }, presence: { mode: "native" } },
      }) as never)).toEqual(["默认值长度不能超过 4 个字符"]);
    expect(fieldDraftIssues(draft({
        logicalType: "date",
        value: { required: false, default: { enabled: true, value: "2026/07/01", source: "user", defaultsVersion: 1 }, presence: { mode: "native" } },
      }) as never)).toEqual(["默认日期需要 YYYY-MM-DD 格式"]);
    expect(fieldDraftIssues(draft({
        logicalType: "date",
        value: { required: false, default: { enabled: true, value: "2026-07-01", source: "user", defaultsVersion: 1 }, presence: { mode: "native" } },
      }) as never)).toEqual([]);
    // 权威 normalizeDateTime 要求 RFC3339Nano：字面 T、秒与时区必需。
    // 无秒/无时区/空格分隔的新写入默认值都要被前端拦下。
    expect(fieldDraftIssues(draft({
        logicalType: "dateTime",
        value: { required: false, default: { enabled: true, value: "2026-08-12T09:00", source: "user", defaultsVersion: 1 }, presence: { mode: "native" } },
      }) as never)).toEqual(["默认日期时间需要含秒和时区的 RFC3339 格式（如 2026-08-12T09:00:00Z）"]);
    expect(fieldDraftIssues(draft({
        logicalType: "dateTime",
        value: { required: false, default: { enabled: true, value: "2026-08-12 09:00:00Z", source: "user", defaultsVersion: 1 }, presence: { mode: "native" } },
      }) as never)).toEqual(["默认日期时间需要含秒和时区的 RFC3339 格式（如 2026-08-12T09:00:00Z）"]);
    expect(fieldDraftIssues(draft({
        logicalType: "dateTime",
        value: { required: false, default: { enabled: true, value: "2026-08-12T09:00:00Z", source: "user", defaultsVersion: 1 }, presence: { mode: "native" } },
      }) as never)).toEqual([]);
    expect(fieldDraftIssues(null)).toEqual([]);
  });
});

describe("auto number configuration", () => {
  it("accepts only complete permutations and defaults to prefix,date,number", () => {
    expect(autoNumberOrderValid(undefined)).toBe(true);
    expect(autoNumberOrderValid(["prefix", "date", "number"])).toBe(true);
    expect(autoNumberOrderValid(["number", "date", "prefix"])).toBe(true);
    expect(autoNumberOrderValid(["prefix", "number"])).toBe(false);
    expect(autoNumberOrderValid(["prefix", "date", "date"])).toBe(false);
    expect(autoNumberOrderValid(["prefix", "date"])).toBe(false);
  });

  it("composes the first number sample from order, optional date segment and padding", () => {
    const now = new Date(Date.UTC(2026, 6, 8));
    expect(autoNumberSample({ prefix: "HT-", start: 1, width: 6 }, now)).toBe("HT-000001");
    expect(autoNumberSample({
      prefix: "HT-", start: 41, width: 4, dateFormat: "yyyyMMdd", order: ["prefix", "date", "number"],
    }, now)).toBe("HT-202607080041");
    expect(autoNumberSample({
      prefix: "", start: 1, width: 3, dateFormat: "yyyyMMdd", order: ["date", "number", "prefix"],
    }, now)).toBe("20260708001");
    // 日期段禁用：date token 被跳过，其余顺序保持。
    expect(autoNumberSample({
      prefix: "N", start: 7, width: 2, dateFormat: null, order: ["date", "number", "prefix"],
    }, now)).toBe("07N");
  });
});

describe("paired relation drafts", () => {
  const pair = {
    reciprocalDisplayName: "订单", reciprocalCardinality: "many" as const,
    sourceDisplayFieldId: "fld_order_number",
  };
  function original() {
    return draftFromDefinition({
      ...field(), logicalType: "relation",
      relation: {
        targetTableId: "tbl_customers", pairId: "pair_1", reciprocalFieldId: "fld_orders",
        cardinality: "many", displayFieldId: "fld_name", deletePolicy: "setNull",
      },
    });
  }

  it("maps both endpoint settings without sending immutable pair identity", () => {
    const before = original();
    const patch = relationPairPatchFromDrafts(before, {
      ...before, displayName: "客户",
      relation: { ...before.relation!, cardinality: "one", displayFieldId: "fld_alias", deletePolicy: "restrict" },
    }, pair, {
      reciprocalDisplayName: "客户订单", reciprocalCardinality: "one",
      sourceDisplayFieldId: "fld_order_title",
    });
    expect(patch).toEqual({
      sourceDisplayName: "客户", sourceCardinality: "one", sourceDisplayFieldId: "fld_alias",
      reciprocalDisplayName: "客户订单", reciprocalCardinality: "one",
      reciprocalDisplayFieldId: "fld_order_title", deletePolicy: "restrict",
    });
    expect(relationPairPatchFromDrafts(before, before, pair, pair)).toBeUndefined();
  });

  it("does not discard other edits or permit cascade in a pair patch", () => {
    const before = original();
    expect(() => relationPairPatchFromDrafts(before, {
      ...before, help: "需要保留的说明", displayName: "客户",
    }, pair, pair)).toThrow("分别保存");
    expect(() => relationPairPatchFromDrafts(before, {
      ...before, relation: { ...before.relation!, deletePolicy: "cascade" },
    }, pair, pair)).toThrow("仅允许");
    expect(relationPairPatchFromDrafts(before, { ...before, help: "说明" }, pair, pair)).toBeUndefined();
  });
});

function capability(definition: FieldDefinitionV2): CapabilityV2 {
  return {
    ...(JSON.parse(readFileSync(capabilityFixturePath, "utf8")) as CapabilityV2),
    logicalType: definition.logicalType,
    generalSettings: ["displayName", "required", "default"],
    advancedSettings: ["unique"],
    dangerSettings: ["retire", "purge"],
    recommended: {
      defaultsVersion: definition.value.default.defaultsVersion,
      value: definition.value,
      constraints: definition.constraints,
      storage: definition.storage,
      display: definition.display,
    },
    supportsRequired: true,
    supportsDefault: true,
    supportsUnique: true,
    needsPresence: false,
    displayPresets: ["plain"],
    conversionTargets: ["number"],
    conversionRules: ["strict"],
    compileStrategy: "native",
    userCreatable: true,
  };
}

describe("field settings model", () => {
  it("keeps provider and product identities out of editable drafts", () => {
    const definition = field();
    const draft = draftFromDefinition(definition);

    expect(draft).not.toHaveProperty("identity");
    expect(draft).not.toHaveProperty("contract");
    expect(draft).not.toHaveProperty("lifecycle");
    expect({ ...draft, displayName: "新标题" }).not.toHaveProperty("physicalName");
  });

  it("preserves typed zero and false defaults instead of treating them as missing", () => {
    const definition = field();
    const numeric = {
      ...capability(definition),
      logicalType: "number" as const,
      recommended: {
        ...capability(definition).recommended,
        value: {
          ...definition.value,
          default: { ...definition.value.default, enabled: true, value: 0 },
        },
      },
    };
    const boolean = {
      ...numeric,
      logicalType: "bool" as const,
      recommended: {
        ...numeric.recommended,
        value: {
          ...numeric.recommended.value,
          default: { ...numeric.recommended.value.default, value: false },
        },
      },
    };

    expect(draftFromCapability(numeric).value.default.value).toBe(0);
    expect(draftFromCapability(boolean).value.default.value).toBe(false);
  });

  it("resets type-specific settings from capabilities without changing the label", () => {
    const definition = field();
    const text = capability(definition);
    const number: CapabilityV2 = {
      ...text,
      logicalType: "number",
      recommended: {
        ...text.recommended,
        display: { ...text.recommended.display, kind: "number", displayScale: 2 },
      },
    };
    const current = {
      ...draftFromDefinition(definition),
      displayName: "金额",
      logicalType: "text" as const,
      display: { ...definition.display, kind: "text" as const, displayScale: 0 },
    };
    const next = replaceDraftType(current, [text, number], "number");

    expect(next.displayName).toBe("金额");
    expect(next.logicalType).toBe("number");
    expect(next.display.displayScale).toBe(2);
    expect(draftsEqual(current, next)).toBe(false);
  });

  it("builds a provider-neutral intent from the authoritative describe result", () => {
    const definition = field();
    const described: FieldSettingsDescribeResultV2 = {
      contract: "vibetable.schema.v2",
      tableId: "tbl_orders",
      fieldId: definition.identity.fieldId,
      schemaRevision: "schema_7",
      dataRevision: 12,
      definition,
      capabilities: [capability(definition)],
      recommendedDefaultsVersion: 1,
    };
    const intent = buildFieldChangeIntent({
      action: "update",
      result: described,
      draft: { ...draftFromDefinition(definition), displayName: "新标题" },
    });

    expect(intent).toMatchObject({
      action: "update",
      tableId: "tbl_orders",
      fieldId: definition.identity.fieldId,
      expectedSchemaRevision: "schema_7",
      expectedDataRevision: 12,
    });
    expect(intent).not.toHaveProperty("providerFieldId");
    expect(intent.draft).not.toHaveProperty("identity");
  });

  it("freezes a visual reciprocal relation draft without exposing provider names", () => {
    const definition = field();
    const described: FieldSettingsDescribeResultV2 = {
      contract: "vibetable.schema.v2",
      tableId: "tbl_orders",
      fieldId: "",
      schemaRevision: "schema_7",
      dataRevision: 12,
      definition: null,
      capabilities: [capability(definition)],
      recommendedDefaultsVersion: 1,
    };
    const relationPair = {
      reciprocalDisplayName: "订单",
      reciprocalCardinality: "many" as const,
      sourceDisplayFieldId: "fld_order_number",
    };
    const intent = buildFieldChangeIntent({
      action: "create", result: described, draft: null, relationPair,
    });

    expect(intent.relationPair).toEqual(relationPair);
    relationPair.reciprocalDisplayName = "changed";
    expect(intent.relationPair?.reciprocalDisplayName).toBe("订单");
    expect(JSON.stringify(intent)).not.toContain("physicalName");
  });

  it("chooses the preferred user-creatable capability and falls back safely", () => {
    const definition = field();
    const text: CapabilityV2 = {
      ...capability(definition),
      logicalType: "text",
      userCreatable: true,
    };
    const hidden: CapabilityV2 = {
      ...text,
      logicalType: "formula",
      userCreatable: false,
    };
    const described: FieldSettingsDescribeResultV2 = {
      contract: "vibetable.schema.v2",
      tableId: "tbl_opaque",
      fieldId: "",
      schemaRevision: "schema_9",
      dataRevision: 3,
      definition: null,
      capabilities: [hidden, text],
      recommendedDefaultsVersion: 1,
    };

    expect(initialDraft(described, "formula").logicalType).toBe("text");
    expect(() => initialDraft({ ...described, capabilities: [hidden] }))
      .toThrow("field.capability.unsupported");
  });

  it("creates isolated specialised drafts and rejects unavailable type changes", () => {
    const definition = field();
    const select: CapabilityV2 = {
      ...capability(definition),
      logicalType: "select",
      recommended: {
        ...capability(definition).recommended,
        display: { ...definition.display, kind: "select" },
      },
    };
    const first = draftFromCapability(select, "Status");
    const second = draftFromCapability(select, "Status");

    expect(first.select).toEqual({ options: [] });
    expect(first.select).not.toBe(second.select);
    expect(() => replaceDraftType(first, [select], "relation"))
      .toThrow("field.capability.unsupported: relation");
  });

  it("creates complete formula and lookup drafts for the unified v2 editors", () => {
    const base = capability(field());
    const formula = {
      ...base,
      logicalType: "formula",
      userCreatable: true,
    } as CapabilityV2;
    const lookup = {
      ...base,
      logicalType: "lookup",
      userCreatable: true,
    } as CapabilityV2;

    expect(draftFromCapability(formula, "总价").formula).toEqual({
      language: "cel-v1", source: "",
    });
    expect(draftFromCapability(lookup, "客户名称").lookup).toEqual({
      path: [{ relationFieldId: "" }], targetFieldId: "",
    });
  });

  it("keeps intent drafts immutable after the caller changes its working copy", () => {
    const definition = field();
    const described: FieldSettingsDescribeResultV2 = {
      contract: "vibetable.schema.v2",
      tableId: "tbl_orders",
      fieldId: definition.identity.fieldId,
      schemaRevision: "schema_7",
      dataRevision: 12,
      definition,
      capabilities: [capability(definition)],
      recommendedDefaultsVersion: 1,
    };
    const draft = draftFromDefinition(definition);
    const intent = buildFieldChangeIntent({ action: "retire", result: described, draft });

    (draft as { displayName: string }).displayName = "changed after intent";
    expect(intent.draft?.displayName).toBe(definition.displayName);
    expect(intent.conversionRule).toBe("");
    expect(intent.confirmation).toBe("");
    expect(intent.backupReceipt).toBe("");
  });
});

describe("display name duplicate precheck", () => {
  function fieldNamed(
    fieldId: string,
    displayName: string,
    state: "active" | "retired" = "active",
  ): FieldDefinitionV2 {
    const base = field();
    return {
      ...base,
      identity: { ...base.identity, fieldId },
      displayName,
      lifecycle: { state, retiredAt: state === "retired" ? "2026-07-01T00:00:00Z" : null },
    };
  }

  it("detects trimmed duplicates among active fields and excludes the edited field itself", () => {
    const self = fieldNamed("fld_self", "金额");
    const other = fieldNamed("fld_other", "金额");
    const retired = fieldNamed("fld_retired", "金额", "retired");

    // 自身同名不算重复；停用字段不参与比较；两端空白按权威 TrimSpace 口径对齐。
    expect(findDuplicateDisplayName("金额", [self], "fld_self")).toBeNull();
    expect(findDuplicateDisplayName("金额", [retired], null)).toBeNull();
    expect(findDuplicateDisplayName("  金额  ", [other], "fld_self")?.identity.fieldId)
      .toBe("fld_other");
    expect(findDuplicateDisplayName("", [other], null)).toBeNull();
  });
});

describe("service error hints", () => {
  it("maps known authoritative codes to Chinese hints and keeps unknown codes raw", () => {
    expect(fieldServiceErrorHint("field.display_name.duplicate"))
      .toContain("同表已存在同名");
    expect(fieldServiceErrorHint("field.change.noop")).toContain("没有产生实际变更");
    expect(fieldServiceErrorHint("field.auto_number.reformat")).toContain("序号保持不变");
    expect(fieldServiceErrorHint("field.auto_date.history_unavailable"))
      .toContain("不会回填已有记录的历史时间");
    expect(fieldServiceErrorHint("field.unknown.code")).toBeNull();
    expect(fieldServiceErrorHint(null)).toBeNull();
    expect(fieldServiceErrorHint(undefined)).toBeNull();
  });
});

describe("autoDate draft defaults", () => {
  it("seeds a creatable autoDate draft with the createdAt role", () => {
    const base = capability(field());
    const autoDate: CapabilityV2 = {
      ...base,
      logicalType: "autoDate",
      userCreatable: true,
      recommended: {
        ...base.recommended,
        display: { ...field().display, kind: "readonly" },
      },
    };
    const draft = draftFromCapability(autoDate, "创建时间");
    expect(draft.autoDate).toEqual({ role: "createdAt" });
    expect(replaceDraftType(draft, [autoDate], "autoDate").autoDate)
      .toEqual({ role: "createdAt" });
  });
});
