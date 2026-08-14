import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import type {
  CapabilityV2,
  FieldDefinitionV2,
  FieldSettingsDescribeResultV2,
} from "@/contracts";
import {
  buildFieldChangeIntent,
  draftFromDefinition,
  draftFromCapability,
  draftsEqual,
  initialDraft,
  replaceDraftType,
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
