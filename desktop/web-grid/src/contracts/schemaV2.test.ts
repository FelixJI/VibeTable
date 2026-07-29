import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import {
  parseFieldApplyReceiptV2,
  parseFieldChangePlanV2,
  parseFieldDefinitionV2,
  parseFieldMigrationStatusV2,
  parseFieldRecycleBinResultV2,
  parseFieldSettingsDescribeResultV2,
  SCHEMA_V2_LOGICAL_TYPES,
  type FieldDefinitionV2,
} from "./schemaV2";

function fixture(name = "field-definition.json"): unknown {
  return JSON.parse(readFileSync(
    resolve(import.meta.dirname, "../../../../contracts/schema-v2/fixtures", name),
    "utf8",
  )) as unknown;
}

describe("Schema v2 contracts", () => {
  it("strictly parses the shared field fixture", () => {
    const field: FieldDefinitionV2 = parseFieldDefinitionV2(fixture());
    expect(field.contract).toBe("vibetable.schema.v2");
    expect(field.value.default).toEqual({
      enabled: false,
      value: null,
      source: "recommended",
      defaultsVersion: 1,
    });
  });

  it("rejects unknown nested properties", () => {
    const field = fixture() as Record<string, any>;
    field.storage.options.ddl = "DROP TABLE users";
    expect(() => parseFieldDefinitionV2(field)).toThrow(
      "field.contract.invalid at $.storage.options.ddl: unknown property",
    );
  });

  it("does not expose provider-only field families", () => {
    expect(SCHEMA_V2_LOGICAL_TYPES).not.toEqual(
      expect.arrayContaining(["hash", "secret", "decimal", "password"]),
    );
  });

  it("strictly parses every v2 RPC result fixture", () => {
    expect(parseFieldChangePlanV2(fixture("field-change-plan.json")).canApply).toBe(true);
    expect(parseFieldApplyReceiptV2(fixture("apply-receipt.json")).schemaRevision)
      .toBe("schema_8");
    expect(parseFieldMigrationStatusV2(fixture("migration-status.json")).phase)
      .toBe("copying");
    expect(parseFieldSettingsDescribeResultV2(fixture("field-settings-describe.json"))
      .dataRevision).toBe(12);
    expect(parseFieldRecycleBinResultV2(fixture("field-recycle-bin.json")).fields)
      .toEqual([]);
  });

  it("rejects a type-specific settings block on the wrong logical type", () => {
    const field = fixture() as Record<string, any>;
    field.relation = {
      targetTableId: "tbl_customers",
      cardinality: "one",
      deletePolicy: "setNull",
      displayFieldId: "fld_name",
    };
    expect(() => parseFieldDefinitionV2(field)).toThrow(
      "field.contract.invalid at $.relation: not allowed for logicalType number",
    );
  });

  it("deeply rejects an incomplete draft nested inside a plan", () => {
    const plan = fixture("field-change-plan.json") as Record<string, any>;
    plan.intent.draft = {};
    expect(() => parseFieldChangePlanV2(plan)).toThrow(
      "field.contract.invalid at $.intent.draft.displayName: missing property",
    );
  });

  it("rejects every shared negative field fixture", () => {
    const cases = fixture("invalid/field-definition-cases.json") as Array<{
      name: string;
      path: string[];
      value?: unknown;
      remove?: boolean;
    }>;
    for (const testCase of cases) {
      const field = fixture() as Record<string, any>;
      let target = field;
      for (const segment of testCase.path.slice(0, -1)) {
        target = target[segment] as Record<string, any>;
      }
      const key = testCase.path.at(-1)!;
      if (testCase.remove) delete target[key];
      else target[key] = testCase.value;
      expect(
        () => parseFieldDefinitionV2(field),
        testCase.name,
      ).toThrow();
    }
  });
});
