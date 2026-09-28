import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type {
  CapabilityV2,
  FieldChangePlanV2,
  FieldDefinitionV2,
  FieldMigrationStatusV2,
  FieldSettingsDescribeResultV2,
} from "@/contracts";
import { useFieldSettingsStore } from "./store";

const fixtures = resolve(import.meta.dirname, "../../../../contracts/schema-v2/fixtures");

function fixture<T>(name: string): T {
  return JSON.parse(readFileSync(resolve(fixtures, name), "utf8")) as T;
}

function definition(): FieldDefinitionV2 {
  return fixture("field-definition.json");
}

function capability(logicalType = "number"): CapabilityV2 {
  return { ...fixture<CapabilityV2>("capability.json"), logicalType: logicalType as CapabilityV2["logicalType"] };
}

function described(existing = true): FieldSettingsDescribeResultV2 {
  const number = capability();
  const text: CapabilityV2 = {
    ...number,
    logicalType: "text",
    conversionTargets: ["number"],
    conversionRules: [],
  };
  return {
    contract: "vibetable.schema.v2",
    tableId: "tbl_opaque",
    fieldId: existing ? definition().identity.fieldId : "",
    schemaRevision: "schema_7",
    dataRevision: 12,
    definition: existing ? definition() : null,
    capabilities: [number, text],
    recommendedDefaultsVersion: 1,
  };
}

function plan(confirmations: readonly string[] = []): FieldChangePlanV2 {
  return {
    ...fixture<FieldChangePlanV2>("field-change-plan.json"),
    confirmations,
    canApply: true,
  };
}

function migration(phase: FieldMigrationStatusV2["phase"]): FieldMigrationStatusV2 {
  return { ...fixture<FieldMigrationStatusV2>("migration-status.json"), phase };
}

describe("field settings store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
  });

  it("tracks reciprocal-only edits and blocks planning until pair metadata is ready", () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    store.load({
      ...described(), definition: {
        ...definition(), logicalType: "relation",
        relation: {
          targetTableId: "tbl_customers", pairId: "pair_1", reciprocalFieldId: "fld_orders",
          cardinality: "many", displayFieldId: "fld_name", deletePolicy: "setNull",
        },
      },
    });
    expect(store.canPlan).toBe(false);
    const pair = {
      reciprocalDisplayName: "订单", reciprocalCardinality: "many" as const,
      sourceDisplayFieldId: "fld_number",
    };
    store.loadRelationPair(pair);
    expect(store.dirty).toBe(false);
    store.patchRelationPair({ reciprocalDisplayName: "所有订单" });
    expect(store.dirty).toBe(true);
    expect(store.canPlan).toBe(true);
    store.setPlan(plan());
    store.patchRelationPair({ reciprocalCardinality: "one" });
    expect(store.plan).toBeNull();
    store.failRelationCatalog(new Error("另一端不可用"));
    expect(store.canPlan).toBe(false);
    store.close();
    expect(store.relationPair).toBeNull();
    expect(store.originalRelationPair).toBeNull();
  });

  it("keeps editor state when planning while clearing only the previous plan outcome", () => {
    const store = useFieldSettingsStore();
    store.load(described());
    store.patchDraft({ displayName: "Amount revised" });
    store.setRelationTables([{ tableId: "tbl_target", displayName: "Target" }]);
    store.setLookupMaxDepth(3);
    store.beginLookupCatalog();
    store.setFormulaPreview("preview value");
    store.setPlan(plan(["confirm"]));
    store.confirmations = ["confirm"];
    store.fail(new Error("earlier planning failed"));

    store.beginPlan();

    expect(store.phase).toBe("planning");
    expect(store.plan).toBeNull();
    expect(store.confirmations).toEqual([]);
    expect(store.error).toBeNull();
    expect(store.relationTables).toEqual([{ tableId: "tbl_target", displayName: "Target" }]);
    expect(store.lookupMaxDepth).toBe(3);
    expect(store.lookupCatalogLoading).toBe(true);
    expect(store.formulaPreviewValue).toBe("preview value");
    expect(store.formulaPreviewReady).toBe(true);

    store.beginOpen();
    expect(store.relationTables).toEqual([]);
    expect(store.lookupMaxDepth).toBe(8);
    expect(store.lookupCatalogLoading).toBe(false);
    expect(store.formulaPreviewReady).toBe(false);
  });

  it("keeps structured formula diagnostics, tokens and catalog through invalidations", () => {
    const store = useFieldSettingsStore();
    const document = {
      displaySource: "{单价} * 2",
      documentRevision: 5,
      tokens: [{
        range: { start: { line: 0, character: 0 }, end: { line: 0, character: 4 } },
        kind: "field" as const,
        fieldId: "fld_price",
        relationFieldId: null,
        targetFieldId: null,
      }],
    };
    const functions = [{
      name: "IF",
      category: "逻辑",
      signature: "IF(bool, T, T)",
      description: "分支",
      example: "IF(true, 1, 2)",
    }];

    store.setFormulaPreview("preview value");
    store.beginFormulaValidation("{单价} * 2", 5);
    expect(store.formulaValidating).toBe(true);
    expect(store.formulaPreviewReady).toBe(false);
    expect(store.formulaValidatedDocumentRevision).toBe(5);

    store.setFormulaValidation("{单价} * 2", {
      canonicalSource: "f_price * 2",
      resultType: "number",
      dependencies: ["f_price"],
      relationAggregatePaths: [],
      authorDocument: document,
      functions,
    });
    expect(store.formulaValidating).toBe(false);
    expect(store.formulaAuthorDocument).toEqual(document);
    expect(store.formulaFunctions).toEqual(functions);

    const diagnostic = {
      message: "公式语法错误",
      code: "formula.syntax",
      range: {
        start: { line: 0, character: 7 },
        end: { line: 0, character: 8 },
      },
    };
    store.failFormulaValidation("{单价} * 3", new Error("公式语法错误"), diagnostic);
    expect(store.formulaValidationError).toBe("公式语法错误");
    expect(store.formulaDiagnostic?.range).toEqual(diagnostic.range);
    expect(store.formulaValidation).toBeNull();

    store.invalidateFormulaDraft();
    expect(store.formulaValidation).toBeNull();
    expect(store.formulaValidatedSource).toBe("");
    expect(store.formulaValidatedDocumentRevision).toBeNull();
    expect(store.formulaDiagnostic).toBeNull();
    // The catalog and last backend document stay for the next request.
    expect(store.formulaFunctions).toEqual(functions);
    expect(store.formulaAuthorDocument).toEqual(document);
    store.beginFormulaValidation("pending", 4);
    store.invalidateFormulaDraft(true);
    expect(store.formulaAuthorDocument).toBeNull();
    expect(store.formulaValidating).toBe(false);

    // Empty-formula bootstrap keeps functions but never adopts "0".
    store.setFormulaRestored({
      canonicalSource: "0",
      resultType: "number",
      dependencies: [],
      relationAggregatePaths: [],
      authorDocument: { displaySource: "0", documentRevision: 1, tokens: [] },
      functions,
    }, false);
    expect(store.formulaFunctions).toEqual(functions);
    expect(store.formulaAuthorDocument).toBeNull();
    expect(store.formulaValidation).toBeNull();

    // A failed restore may still carry #REF! tokens via details.authorDocument.
    const brokenDocument = {
      displaySource: "#REF! + 1",
      documentRevision: 2,
      tokens: [],
    };
    store.failFormulaRestore(new Error("引用失效"), {
      message: "引用失效",
      code: "formula.reference",
      range: null,
    }, brokenDocument);
    expect(store.formulaValidationError).toBe("引用失效");
    expect(store.formulaAuthorDocument).toEqual(brokenDocument);

    store.beginOpen();
    expect(store.formulaFunctions).toEqual([]);
    expect(store.formulaAuthorDocument).toBeNull();
    expect(store.formulaDiagnostic).toBeNull();
    expect(store.formulaValidatedDocumentRevision).toBeNull();
  });
  it("moves through open, edit, plan and confirmation-gated apply states", () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    expect(store.phase).toBe("loading");
    expect(store.canPlan).toBe(false);

    store.load(described());
    expect(store.phase).toBe("editing");
    expect(store.isExisting).toBe(true);
    expect(store.dirty).toBe(false);
    expect(store.canPlan).toBe(false);

    store.patchDraft({ displayName: "Amount (revised)" });
    expect(store.dirty).toBe(true);
    expect(store.canPlan).toBe(true);
    store.beginPlan();
    expect(store.phase).toBe("planning");
    store.setPlan(plan(["danger.confirm", "backup.confirm"]));
    expect(store.phase).toBe("planned");
    expect(store.canApply).toBe(false);

    store.confirmations = ["danger.confirm"];
    expect(store.confirmationsComplete).toBe(false);
    store.confirmations = ["danger.confirm", "backup.confirm"];
    expect(store.confirmationsComplete).toBe(true);
    expect(store.canApply).toBe(true);
  });

  it("resets plan state after a draft edit and preserves original values after a normal receipt", () => {
    const store = useFieldSettingsStore();
    store.load(described());
    store.patchDraft({ displayName: "Amount (revised)" });
    store.setPlan(plan());
    store.patchDraft({ help: "Visible to billing" });

    expect(store.phase).toBe("editing");
    expect(store.plan).toBeNull();
    expect(store.confirmations).toEqual([]);

    const nextDefinition = { ...definition(), displayName: "Amount (revised)" };
    store.setReceipt({
      contract: "vibetable.schema.v2",
      operationId: "operation_1",
      planId: "plan_1",
      action: "update",
      tableId: "tbl_opaque",
      fieldId: nextDefinition.identity.fieldId,
      schemaRevision: "schema_8",
      definition: nextDefinition,
      migrationJobId: "",
    });

    expect(store.phase).toBe("editing");
    expect(store.dirty).toBe(false);
    expect(store.original?.displayName).toBe("Amount (revised)");
  });

  it("allows supported conversions, clears conversion choices, and fails closed for unsupported targets", () => {
    const store = useFieldSettingsStore();
    store.load(described());
    store.conversionRule = "round";
    store.changeType("text");

    expect(store.action).toBe("convert");
    expect(store.draft?.logicalType).toBe("text");
    expect(store.conversionRule).toBe("");
    store.conversionRule = "round";
    expect(store.canPlan).toBe(true);

    store.changeType("bool");
    expect(store.draft?.logicalType).toBe("text");
    expect(store.errorCode).toBe("field.capability.unsupported");
    expect(store.phase).toBe("editing");
  });

  it("invalidates a frozen plan when any plan input changes", () => {
    const store = useFieldSettingsStore();
    store.load(described());
    store.patchDraft({ displayName: "Amount revised" });

    for (const change of [
      () => store.setConversionRule("round"),
      () => store.setConfirmation("PURGE"),
      () => store.setBackupReceipt("vbr1.receipt"),
    ]) {
      store.setPlan(plan());
      expect(store.phase).toBe("planned");
      change();
      expect(store.phase).toBe("editing");
      expect(store.plan).toBeNull();
    }
  });

  it("does not leak conversion or danger inputs between drawer sessions", () => {
    const store = useFieldSettingsStore();
    store.setConversionRule("round");
    store.setConfirmation("PURGE");
    store.setBackupReceipt("vbr1.receipt");

    store.close();
    expect(store.conversionRule).toBe("");
    expect(store.confirmation).toBe("");
    expect(store.backupReceipt).toBe("");

    store.setConversionRule("block");
    store.setConfirmation("DELETE");
    store.setBackupReceipt("vbr1.other");
    store.beginOpen();
    expect(store.conversionRule).toBe("");
    expect(store.confirmation).toBe("");
    expect(store.backupReceipt).toBe("");
  });

  it("restores recommended settings as an unsaved draft without losing identity-specific options", () => {
    const store = useFieldSettingsStore();
    store.load(described());
    const originalName = store.draft?.displayName;
    store.patchDraft({
      display: { ...store.draft!.display, displayScale: 9 },
    });
    store.setPlan(plan());

    store.restoreRecommended();

    expect(store.draft?.displayName).toBe(originalName);
    expect(store.draft?.display.displayScale).toBe(
      store.capability?.recommended.display.displayScale,
    );
    expect(store.plan).toBeNull();
    expect(store.phase).toBe("editing");
  });

  it("models in-flight, terminal, failed and cancelled migrations without losing diagnostics", () => {
    const store = useFieldSettingsStore();
    store.load(described());
    store.setReceipt({
      contract: "vibetable.schema.v2",
      operationId: "operation_1",
      planId: "plan_1",
      action: "convert",
      tableId: "tbl_opaque",
      fieldId: definition().identity.fieldId,
      schemaRevision: "schema_8",
      definition: null,
      migrationJobId: "job_1",
    });
    expect(store.phase).toBe("migrating");

    store.setMigration(migration("copying"));
    expect(store.phase).toBe("migrating");
    store.setMigration(migration("cancelled"));
    expect(store.phase).toBe("editing");

    store.setMigration({
      ...migration("failed"),
      error: { code: "field.migration.failed", path: "", message: "copy failed", details: {} },
    });
    expect(store.phase).toBe("failed");
    expect(store.errorCode).toBe("field.migration.failed");
    store.resetFailure();
    expect(store.phase).toBe("editing");
  });

  it("keeps recycle-bin state scoped to the drawer and clears all transient state on close", () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    store.load(described());
    store.setRecycled([definition()]);
    store.fail(Object.assign(new Error("host rejected"), { code: "field.conflict" }));
    expect(store.recycled).toHaveLength(1);
    expect(store.errorCode).toBe("field.conflict");

    store.close();
    expect(store.open).toBe(false);
    expect(store.phase).toBe("idle");
    expect(store.result).toBeNull();
    expect(store.recycled).toEqual([]);
    expect(store.error).toBeNull();
  });
});
