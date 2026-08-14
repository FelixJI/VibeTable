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

  it("accepts every product logical-type settings family as a closed v2 definition", () => {
    const cases: Array<[string, string, Record<string, unknown>]> = [
      ["select", "select", { options: [{
        optionId: "opt_open", label: "Open", color: "", order: 0, state: "active",
      }] }],
      ["multiSelect", "select", { options: [{
        optionId: "opt_retired", label: "Retired", color: "gray", order: 1, state: "retired",
      }] }],
      ["relation", "relation", {
        targetTableId: "tbl_users", cardinality: "many", deletePolicy: "restrict",
        displayFieldId: "", pairId: "pair_1", reciprocalFieldId: "fld_back",
      }],
      ["file", "file", {
        maxFiles: 3, maxBytesPerFile: 1024, allowedMimeTypes: ["text/plain"],
        thumbs: ["100x100"], protected: true,
      }],
      ["json", "json", { rootType: "object", maxSize: 2048, schema: { type: "object" } }],
      ["autoDate", "autoDate", { role: "updatedAt" }],
      ["formula", "formula", { language: "cel-v1", source: "1 + 1", resultType: "number" }],
      ["lookup", "lookup", {
        path: [{ relationFieldId: "fld_customer" }, { relationFieldId: "fld_region" }],
        targetFieldId: "fld_name",
      }],
    ];

    for (const [logicalType, settingsKey, settings] of cases) {
      const field = structuredClone(fixture()) as Record<string, any>;
      field.logicalType = logicalType;
      field[settingsKey] = settings;
      expect(parseFieldDefinitionV2(field).logicalType).toBe(logicalType);
    }
  });

  it("parses rich plans, receipts, migration diagnostics, and recycle-bin definitions", () => {
    const definition = fixture();
    const plan = structuredClone(fixture("field-change-plan.json")) as Record<string, any>;
    plan.before = definition;
    plan.after = definition;
    plan.classes = ["display", "metadata", "constraint", "schema", "migration", "danger"];
    plan.intent.relationPair = {
      reciprocalDisplayName: "订单", reciprocalCardinality: "one",
      sourceDisplayFieldId: "fld_name",
    };
    plan.impact.failures = [{ recordId: "rec_1", reason: "invalid" }];
    plan.impact.dependencies = [{ kind: "view", id: "view_1", name: "Main" }];
    plan.steps = [{ kind: "copy", details: { batchSize: 100 } }];
    plan.warnings = [{ code: "warning", path: "", message: "review", details: { count: 1 } }];
    plan.errors = [{ code: "error", path: "$.value", message: "invalid", details: {} }];
    plan.relatedChanges = [{
      tableId: "tbl_users", fieldId: "fld_back", before: definition, after: definition,
      expectedSchemaRevision: "schema_7",
    }];
    expect(parseFieldChangePlanV2(plan).relatedChanges).toHaveLength(1);

    const receipt = structuredClone(fixture("apply-receipt.json")) as Record<string, any>;
    receipt.definition = definition;
    receipt.related = [{
      tableId: "tbl_users", fieldId: "fld_back", schemaRevision: "schema_8",
      definition,
    }];
    expect(parseFieldApplyReceiptV2(receipt).related).toHaveLength(1);

    const status = structuredClone(fixture("migration-status.json")) as Record<string, any>;
    status.phase = "failed";
    status.error = { code: "copy.failed", path: "", message: "offline", details: {} };
    expect(parseFieldMigrationStatusV2(status).error?.code).toBe("copy.failed");

    const describe = structuredClone(fixture("field-settings-describe.json")) as Record<string, any>;
    describe.definition = null;
    expect(parseFieldSettingsDescribeResultV2(describe).definition).toBeNull();
    expect(parseFieldRecycleBinResultV2({
      contract: "vibetable.schema.v2", fields: [definition],
    }).fields).toHaveLength(1);
  });

  it("rejects malformed primitive, nullable, enum, and optional-spec boundaries", () => {
    const mutations: Array<(field: Record<string, any>) => void> = [
      field => { field.contract = "v1"; },
      field => { field.logicalType = "password"; },
      field => { field.displayName = 7; },
      field => { field.lifecycle.retiredAt = 7; },
      field => { field.value.required = "yes"; },
      field => { field.value.default.defaultsVersion = -1; },
      field => { field.constraints.range.min = Number.POSITIVE_INFINITY; },
      field => { field.constraints.range.max = {}; },
      field => { field.constraints.length.min = 1.5; },
      field => { field.constraints.domains.only = [7]; },
      field => { field.display.indent = 3; },
      field => { field.select = { options: {} }; field.logicalType = "select"; },
      field => { field.lookup = { path: [], targetFieldId: "fld_1" }; field.logicalType = "lookup"; },
      field => { field.lookup = { path: Array.from({ length: 9 }, () => ({ relationFieldId: "fld_1" })), targetFieldId: "fld_1" }; field.logicalType = "lookup"; },
      field => { field.json = { rootType: "binary", maxSize: 1, schema: {} }; field.logicalType = "json"; },
    ];
    for (const mutate of mutations) {
      const field = structuredClone(fixture()) as Record<string, any>;
      mutate(field);
      expect(() => parseFieldDefinitionV2(field)).toThrow("field.contract.invalid");
    }
    expect(() => parseFieldDefinitionV2(null)).toThrow("expected object");
    expect(() => parseFieldDefinitionV2([])).toThrow("expected object");
  });
});
