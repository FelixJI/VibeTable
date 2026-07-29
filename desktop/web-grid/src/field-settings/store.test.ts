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
