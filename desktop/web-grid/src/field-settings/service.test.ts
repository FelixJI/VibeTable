import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type {
  CapabilityV2, FieldDefinitionV2, FieldSettingsDescribeResultV2, JsonValueV2,
} from "@/contracts";
import type { HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import { useFieldSettingsStore } from "./store";
import { useFieldSettingsService } from "./service";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useTableStore } from "@/stores/tableStore";

const fixtures = resolve(import.meta.dirname, "../../../../contracts/schema-v2/fixtures");

function fixture<T>(name: string): T {
  return JSON.parse(readFileSync(resolve(fixtures, name), "utf8")) as T;
}

function definition(): FieldDefinitionV2 {
  return fixture("field-definition.json");
}

function describeResult(existing = true): FieldSettingsDescribeResultV2 {
  return {
    contract: "vibetable.schema.v2",
    tableId: "tbl_opaque",
    fieldId: existing ? definition().identity.fieldId : "",
    schemaRevision: "schema_7",
    dataRevision: 12,
    definition: existing ? definition() : null,
    capabilities: [fixture<CapabilityV2>("capability.json")],
    recommendedDefaultsVersion: 1,
  };
}

function formulaCapability(): CapabilityV2 {
  return {
    ...fixture<CapabilityV2>("capability.json"),
    logicalType: "formula",
    userCreatable: true,
    advancedSettings: ["source", "autoType"],
  };
}

function plan(confirmations: readonly string[] = []): Record<string, unknown> {
  return { ...fixture<Record<string, unknown>>("field-change-plan.json"), confirmations };
}

function receipt(migrationJobId = ""): Record<string, unknown> {
  return { ...fixture<Record<string, unknown>>("apply-receipt.json"), migrationJobId };
}

function migration(phase: string): Record<string, unknown> {
  return { ...fixture<Record<string, unknown>>("migration-status.json"), phase };
}

function pairedDescription(reciprocal = false): FieldSettingsDescribeResultV2 {
  const fieldId = reciprocal ? "fld_orders" : "fld_customer";
  return {
    ...describeResult(), tableId: reciprocal ? "tbl_customers" : "tbl_opaque", fieldId,
    definition: {
      ...definition(), logicalType: "relation", displayName: reciprocal ? "订单" : "客户",
      identity: { fieldId, physicalName: `f_${fieldId}`, providerFieldId: `pb_${fieldId}` },
      relation: {
        targetTableId: reciprocal ? "tbl_opaque" : "tbl_customers",
        cardinality: "many", deletePolicy: "setNull", pairId: "pair_1",
        reciprocalFieldId: reciprocal ? "fld_customer" : "fld_orders",
        displayFieldId: reciprocal ? "fld_number" : "fld_alias",
      },
    },
  };
}

function relationSchema(collection: string) {
  return {
    contract: "vibetable.schema-describe.v1", collection, requestGeneration: 0,
    schema: {
      collection, primaryKey: "id", primaryDisplayFieldId: "fld_primary", columns: [],
      normalizedRelations: [], schemaRevision: "schema_7", permissionRevision: "schema_7",
      capabilityHash: "cap", lookupRevision: "lookup",
    },
    capabilities: {
      contract: "vibetable.relation-capabilities.v1",
      relationReadV1: true, relationEditV1: true, lookupQueryV1: true, reason: null,
    },
  };
}

describe("field settings service", () => {
  const request = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    request.mockReset();
    vi.useFakeTimers();
    setActivePinia(createPinia());
    useWorkspaceStore().setOpened([
      { collection: "tbl_opaque" }, { collection: "tbl_customers" },
    ], { tbl_opaque: "订单", tbl_customers: "客户" });
    setHostBridgeForTesting({ request } as unknown as HostBridge);
  });

  afterEach(() => {
    setHostBridgeForTesting(null);
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("accepts progress parameters from real describe and plan parser paths", async () => {
    const original = definition();
    const progress = { ...original, display: { ...original.display, preset: "progress", progressStart: 1, progressTarget: 2 } };
    request.mockResolvedValueOnce({ ...describeResult(), definition: progress })
      .mockResolvedValueOnce({ ...plan(), before: progress, after: progress });
    const service = useFieldSettingsService(); const store = useFieldSettingsStore();
    await service.openEdit("tbl_opaque", progress.identity.fieldId);
    expect(store.phase).toBe("editing");
    expect(store.draft?.display.progressTarget).toBe(2);
    store.patchDraft({ displayName: "进度" });
    await service.plan();
    expect(store.phase).toBe("planned");
    expect(request.mock.calls[1]?.[0]).toBe("field.change.plan");
    service.dispose();
  });

  it("keeps the requested diagnosis identity after describe fails and clears it for create/close", async () => {
    request.mockResolvedValueOnce(describeResult(true));
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    await service.openEdit("old_table", "old_field");
    request.mockRejectedValueOnce(new Error("invalid relation metadata"));
    await service.openEdit("broken_table", "broken_field");
    expect(store.inspectionTarget).toEqual({ tableId: "broken_table", fieldId: "broken_field" });
    expect(store.result).toBeNull();
    expect(store.draft).toBeNull();
    expect(store.phase).toBe("failed");
    service.requestClose();
    expect(store.inspectionTarget).toBeNull();
    request.mockResolvedValueOnce(describeResult(false));
    await service.openCreate("new_table");
    expect(store.inspectionTarget).toBeNull();
    service.dispose();
  });
  it("loads existing reciprocal metadata without changing display selection, then plans and applies a pair patch", async () => {
    request
      .mockResolvedValueOnce(pairedDescription())
      .mockResolvedValueOnce(relationSchema("tbl_opaque"))
      .mockResolvedValueOnce(relationSchema("tbl_customers"))
      .mockResolvedValueOnce(pairedDescription(true))
      .mockResolvedValueOnce(plan())
      .mockResolvedValueOnce(receipt())
      .mockResolvedValueOnce(pairedDescription());
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    await service.openEdit("tbl_opaque", "fld_customer");
    await service.loadRelationCatalog();
    expect(store.draft?.relation?.displayFieldId).toBe("fld_alias");
    expect(store.relationPair).toEqual({
      reciprocalDisplayName: "订单", reciprocalCardinality: "many", sourceDisplayFieldId: "fld_number",
    });
    expect(store.dirty).toBe(false);
    expect(request.mock.calls[3]).toEqual([
      "field.settings.describe", { tableId: "tbl_customers", fieldId: "fld_orders" },
    ]);
    await service.selectRelationTarget("tbl_other");
    expect(request).toHaveBeenCalledTimes(4);
    store.patchRelationPair({ reciprocalDisplayName: "关联订单", reciprocalCardinality: "one" });
    await service.plan();
    expect(request.mock.calls[4]).toEqual(["field.change.plan", expect.objectContaining({
      action: "update", draft: null, expectedSchemaRevision: "schema_7", expectedDataRevision: 12,
      relationPairPatch: { reciprocalDisplayName: "关联订单", reciprocalCardinality: "one" },
    })]);
    await service.apply();
    expect(request.mock.calls[5]?.[0]).toBe("field.change.apply");
    expect(request.mock.calls[6]?.[0]).toBe("field.settings.describe");
    expect(store.dirty).toBe(false);
  });

  it("rejects mismatched reciprocal identity and does not plan a partial pair", async () => {
    request
      .mockResolvedValueOnce(pairedDescription())
      .mockResolvedValueOnce(relationSchema("tbl_opaque"))
      .mockResolvedValueOnce(relationSchema("tbl_customers"))
      .mockResolvedValueOnce(pairedDescription());
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    await service.openEdit("tbl_opaque", "fld_customer");
    await service.loadRelationCatalog();
    expect(store.relationCatalogError).toContain("另一端关联字段不可用");
    expect(store.canPlan).toBe(false);
    await service.plan();
    expect(request).toHaveBeenCalledTimes(4);
  });

  it("does not attach a late relation catalog to a different editor", async () => {
    let resolveSchema!: (value: unknown) => void;
    request.mockResolvedValueOnce(pairedDescription())
      .mockImplementationOnce(() => new Promise(resolve => { resolveSchema = resolve; }))
      .mockResolvedValueOnce(describeResult());
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    await service.openEdit("tbl_opaque", "fld_customer");
    const loading = service.loadRelationCatalog();
    await service.openEdit("tbl_opaque", "fld_amount");
    resolveSchema(relationSchema("tbl_opaque"));
    await loading;
    expect(store.relationSourceSchema).toBeNull();
    expect(store.relationPair).toBeNull();
    expect(request).toHaveBeenCalledTimes(3);
  });

  it("uses closed v2 RPCs to create, plan, apply, and refresh an edited field", async () => {
    request
      .mockResolvedValueOnce(describeResult(true))
      .mockResolvedValueOnce(plan())
      .mockResolvedValueOnce(receipt())
      .mockResolvedValueOnce(describeResult(true));
    const onCommitted = vi.fn();
    const service = useFieldSettingsService({ onCommitted });
    const store = useFieldSettingsStore();

    await service.openEdit("tbl_opaque", definition().identity.fieldId);
    store.patchDraft({ displayName: "Amount revised" });
    await service.plan();
    await service.apply();

    expect(store.phase).toBe("editing");
    expect(request.mock.calls).toHaveLength(4);
    expect(request.mock.calls[0]).toEqual([
      "field.settings.describe",
      { tableId: "tbl_opaque", fieldId: definition().identity.fieldId },
    ]);
    expect(request.mock.calls[1]?.[0]).toBe("field.change.plan");
    expect(request.mock.calls[1]?.[1]).toMatchObject({
      action: "update",
      tableId: "tbl_opaque",
      expectedSchemaRevision: "schema_7",
      draft: { displayName: "Amount revised" },
    });
    expect(request.mock.calls[2]).toMatchObject([
      "field.change.apply",
      { planId: "plan_01JABCDEFGH", planHash: "sha256:0123456789abcdef", operationId: expect.any(String) },
    ]);
    expect(request.mock.calls[3]?.[0]).toBe("field.settings.describe");
    expect(onCommitted).toHaveBeenCalledOnce();
    expect(onCommitted).toHaveBeenCalledWith(expect.objectContaining({
      tableId: "tbl_orders",
      fieldId: definition().identity.fieldId,
    }));
    expect(JSON.stringify(request.mock.calls)).not.toMatch(/schema\.(apply|validate|delete)/);
  });

  it.each(["success", "failure"] as const)("ignores late plan %s after closing A and opening B", async (outcome) => {
    let settle!: () => void;
    const delayed = new Promise<unknown>((resolve, reject) => {
      settle = () => outcome === "success"
        ? resolve(plan())
        : reject(new Error("old A planning failed"));
    });
    request.mockResolvedValueOnce(describeResult(true)).mockReturnValueOnce(delayed);
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    await service.openEdit("tbl_opaque", definition().identity.fieldId);
    store.patchDraft({ displayName: "A edited" });
    const pending = service.plan();
    expect(store.phase).toBe("planning");
    vi.stubGlobal("confirm", () => true);
    expect(service.requestClose()).toBe(true);

    request.mockResolvedValueOnce({ ...describeResult(true), tableId: "tbl_b" });
    await service.openEdit("tbl_b", definition().identity.fieldId);
    store.patchDraft({ displayName: "B edited" });
    const currentDraft = store.draft;
    settle();
    await pending;

    expect(store.open).toBe(true);
    expect(store.result?.tableId).toBe("tbl_b");
    expect(store.draft).toBe(currentDraft);
    expect(store.phase).toBe("editing");
    expect(store.plan).toBeNull();
    expect(store.error).toBeNull();
    expect(store.errorCode).toBeNull();
  });

  it("accepts a delayed plan while its editor session remains current", async () => {
    let finish!: (value: unknown) => void;
    const delayed = new Promise<unknown>((resolve) => { finish = resolve; });
    request.mockResolvedValueOnce(describeResult(true)).mockReturnValueOnce(delayed);
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    await service.openEdit("tbl_opaque", definition().identity.fieldId);
    store.patchDraft({ displayName: "Current edit" });
    const pending = service.plan();
    expect(store.phase).toBe("planning");
    finish(plan());
    await pending;
    expect(store.phase).toBe("planned");
    expect(store.plan?.planId).toBe("plan_01JABCDEFGH");
    expect(store.draft?.displayName).toBe("Current edit");
    expect(store.error).toBeNull();
  });
  it("closes after a successful purge without describing the deleted field", async () => {
    request
      .mockResolvedValueOnce(describeResult(true))
      .mockResolvedValueOnce(plan(["backupReceipt", "fieldName"]))
      .mockResolvedValueOnce({
        ...receipt(),
        action: "purge",
        definition: null,
      });
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();

    await service.openEdit("tbl_opaque", definition().identity.fieldId);
    await service.plan("purge");
    store.confirmations = ["backupReceipt", "fieldName"];
    await service.apply();

    expect(store.open).toBe(false);
    expect(store.phase).toBe("idle");
    expect(request.mock.calls.map(([name]) => name)).toEqual([
      "field.settings.describe",
      "field.change.plan",
      "field.change.apply",
    ]);
  });

  it("polls a migration until completion, then reloads the authoritative description", async () => {
    request
      .mockResolvedValueOnce(describeResult(true))
      .mockResolvedValueOnce(plan())
      .mockResolvedValueOnce(receipt("job_1"))
      .mockResolvedValueOnce(migration("copying"))
      .mockResolvedValueOnce(migration("completed"))
      .mockResolvedValueOnce(describeResult(true));
    const onCommitted = vi.fn();
    const service = useFieldSettingsService({ onCommitted });
    const store = useFieldSettingsStore();

    await service.openEdit("tbl_opaque", definition().identity.fieldId);
    store.patchDraft({ displayName: "Amount revised" });
    await service.plan();
    await service.apply();
    expect(store.phase).toBe("migrating");

    await vi.advanceTimersByTimeAsync(750);
    expect(store.migration?.phase).toBe("copying");
    expect(store.phase).toBe("migrating");
    await vi.advanceTimersByTimeAsync(750);

    // Completion deliberately reloads the authoritative describe result, which
    // starts a fresh drawer state and clears the now-terminal job snapshot.
    expect(store.migration).toBeNull();
    expect(store.phase).toBe("editing");
    expect(request.mock.calls.map(([name]) => name)).toEqual([
      "field.settings.describe",
      "field.change.plan",
      "field.change.apply",
      "field.change.status",
      "field.change.status",
      "field.settings.describe",
    ]);
    expect(onCommitted).toHaveBeenCalledOnce();
  });

  it("cancels an active migration and removes the pending poll", async () => {
    request
      .mockResolvedValueOnce(describeResult(true))
      .mockResolvedValueOnce(plan())
      .mockResolvedValueOnce(receipt("job_cancel"))
      .mockResolvedValueOnce(migration("cancelled"));
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();

    await service.openEdit("tbl_opaque", definition().identity.fieldId);
    store.patchDraft({ displayName: "Amount revised" });
    await service.plan();
    await service.apply();
    await service.cancelMigration();
    await vi.advanceTimersByTimeAsync(1_000);

    expect(store.phase).toBe("editing");
    expect(store.migration?.phase).toBe("cancelled");
    expect(request.mock.calls.map(([name]) => name)).not.toContain("field.change.status");
    expect(request).toHaveBeenCalledWith("field.change.cancel", { jobId: "job_cancel" });
  });

  it("loads a recycle bin and prepares a restore through the same planner", async () => {
    request
      .mockResolvedValueOnce(describeResult(true))
      .mockResolvedValueOnce({ contract: "vibetable.schema.v2", fields: [definition()] })
      .mockResolvedValueOnce(describeResult(true))
      .mockResolvedValueOnce(plan(["restore.confirm"]));
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();

    await service.openEdit("tbl_opaque", definition().identity.fieldId);
    await service.loadRecycleBin();
    await service.restore(definition().identity.fieldId);

    expect(store.recycled).toHaveLength(1);
    expect(store.action).toBe("restore");
    expect(store.phase).toBe("planned");
    expect(store.plan?.confirmations).toEqual(["restore.confirm"]);
    expect(request.mock.calls.map(([name]) => name)).toEqual([
      "field.settings.describe",
      "field.recycleBin.list",
      "field.settings.describe",
      "field.change.plan",
    ]);
  });

  it("ignores stale describe results, reports malformed responses, and honours a rejected close", async () => {
    let resolveFirst: ((value: unknown) => void) | undefined;
    const first = new Promise<unknown>((resolve) => { resolveFirst = resolve; });
    request
      .mockImplementationOnce(() => first)
      .mockResolvedValueOnce(describeResult(true));
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();

    const pending = service.openCreate("tbl_stale");
    await service.openEdit("tbl_opaque", definition().identity.fieldId);
    resolveFirst?.(describeResult(false));
    await pending;
    expect(store.result?.tableId).toBe("tbl_opaque");

    store.patchDraft({ displayName: "unsaved" });
    const confirm = vi.fn(() => false);
    vi.stubGlobal("confirm", confirm);
    expect(service.requestClose()).toBe(false);
    expect(store.open).toBe(true);
    expect(confirm).toHaveBeenCalledTimes(1);

    request.mockResolvedValueOnce({ contract: "invalid" });
    await service.openCreate("tbl_invalid");
    expect(store.phase).toBe("failed");
    expect(store.error).toContain("field.contract.invalid");
  });

  it("does not call the bridge when no description or applicable plan exists", async () => {
    const service = useFieldSettingsService();

    await service.plan();
    await service.apply();
    await service.refreshMigration();
    await service.cancelMigration();
    await service.loadRecycleBin();
    await service.restore("fld_missing");
    service.dispose();

    expect(request).not.toHaveBeenCalled();
  });

  it("loads table and field names for a paired relation without asking for raw ids", async () => {
    const base = fixture<CapabilityV2>("capability.json");
    const relationCapability: CapabilityV2 = {
      ...base,
      logicalType: "relation",
      userCreatable: true,
      recommended: {
        ...base.recommended,
        storage: { ...base.recommended.storage, kind: "pocketbase-relation" },
        display: { ...base.recommended.display, kind: "relation" },
      },
    };
    const described: FieldSettingsDescribeResultV2 = {
      ...describeResult(false), capabilities: [relationCapability],
    };
    const schema = (collection: string, fieldId: string, title: string) => ({
      contract: "vibetable.schema-describe.v1",
      collection,
      requestGeneration: 0,
      schema: {
        collection,
        primaryKey: "id",
        primaryDisplayFieldId: fieldId,
        columns: [{
          name: `f_${fieldId}`, title, fieldId, kind: "scalar" as const,
          dataType: "text" as const, editable: true, nullable: false,
        }],
        normalizedRelations: [], schemaRevision: "schema_2",
        permissionRevision: "schema_2", capabilityHash: "cap", lookupRevision: "lookup",
      },
      capabilities: {
        contract: "vibetable.relation-capabilities.v1",
        relationReadV1: true, relationEditV1: true, lookupQueryV1: true, reason: null,
      },
    });
    request
      .mockResolvedValueOnce(described)
      .mockResolvedValueOnce(schema("tbl_opaque", "fld_order_number", "订单号"))
      .mockResolvedValueOnce(schema("tbl_customers", "fld_customer_name", "客户名称"));
    useWorkspaceStore().setOpened([
      { collection: "tbl_opaque" },
      { collection: "tbl_customers" },
    ], { tbl_opaque: "订单", tbl_customers: "客户" });
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();

    await service.openCreate("tbl_opaque", "relation");
    await service.loadRelationCatalog();
    await service.selectRelationTarget("tbl_customers");

    expect(store.relationTables.map(item => item.displayName)).toEqual(["订单", "客户"]);
    expect(store.relationPair).toEqual({
      reciprocalDisplayName: "订单",
      reciprocalCardinality: "many",
      sourceDisplayFieldId: "fld_order_number",
    });
    expect(store.draft?.relation).toMatchObject({
      targetTableId: "tbl_customers", displayFieldId: "fld_customer_name",
    });
    expect(request.mock.calls.slice(1).map(([name]) => name)).toEqual([
      "schema.describe", "schema.describe",
    ]);
    store.patchDraft({ displayName: "客户" });
    store.patchRelationPair({ reciprocalDisplayName: "客户订单", reciprocalCardinality: "one" });
    const pair = { ...store.relationPair! };
    const sourceSchema = store.relationSourceSchema;
    const targetSchema = store.relationTargetSchema;
    request.mockRejectedValueOnce(new Error("plan temporarily unavailable"));
    expect(store.canPlan).toBe(true);
    await service.plan();
    expect(request.mock.calls.at(-1)).toMatchObject([
      "field.change.plan", { relationPair: pair },
    ]);
    expect(store.phase).toBe("failed");
    expect(store.relationPair).toEqual(pair);
    expect(store.relationSourceSchema).toBe(sourceSchema);
    expect(store.relationTargetSchema).toBe(targetSchema);
    expect(store.relationTables.map(item => item.displayName)).toEqual(["订单", "客户"]);
    expect(store.canPlan).toBe(true);

    request.mockResolvedValueOnce(plan());
    await service.plan();
    expect(store.phase).toBe("planned");
    expect(request.mock.calls.at(-1)).toEqual(request.mock.calls.at(-2));
    expect(store.relationPair).toEqual(pair);
  });

  it("resolves an eight-hop-capable Lookup catalog by display metadata without mutating the draft", async () => {
    const base = fixture<CapabilityV2>("capability.json");
    const lookupCapability: CapabilityV2 = {
      ...base,
      logicalType: "lookup",
      userCreatable: true,
      advancedSettings: ["path", "targetField"],
    };
    const described: FieldSettingsDescribeResultV2 = {
      ...describeResult(false), capabilities: [lookupCapability],
    };
    const schema = (
      collection: string,
      relation?: { fieldId: string; title: string; target: string },
      scalar?: { fieldId: string; title: string },
    ) => ({
      contract: "vibetable.schema-describe.v1",
      collection,
      requestGeneration: 0,
      schema: {
        collection,
        primaryKey: "id",
        primaryDisplayFieldId: scalar?.fieldId ?? "",
        columns: [
          ...(relation ? [{
            name: `f_${relation.fieldId}`, title: relation.title,
            fieldId: relation.fieldId, kind: "relation" as const,
            dataType: "relation" as const, editable: true, nullable: true,
            relationId: `${collection}.${relation.fieldId}`,
          }] : []),
          ...(scalar ? [{
            name: `f_${scalar.fieldId}`, title: scalar.title,
            fieldId: scalar.fieldId, kind: "scalar" as const,
            dataType: "text" as const, editable: true, nullable: true,
          }] : []),
        ],
        normalizedRelations: relation ? [{
          relationId: `${collection}.${relation.fieldId}`,
          kind: "m2o" as const,
          relatedCollection: relation.target,
          relatedCollectionDisplayName: relation.target,
          junction: null,
        }] : [],
        schemaRevision: "schema_2", permissionRevision: "schema_2",
        capabilityHash: "cap", lookupRevision: "lookup",
      },
      capabilities: {
        contract: "vibetable.relation-capabilities.v1",
        relationReadV1: true, relationEditV1: true, lookupQueryV1: true, reason: null,
      },
    });
    const orders = schema("tbl_opaque", {
      fieldId: "fld_customer", title: "客户", target: "tbl_customers",
    });
    const customers = schema("tbl_customers", {
      fieldId: "fld_region", title: "区域", target: "tbl_regions",
    });
    const regions = schema("tbl_regions", undefined, {
      fieldId: "fld_region_name", title: "区域名称",
    });
    request
      .mockResolvedValueOnce(described)
      .mockResolvedValueOnce({
        contract: "vibetable.schema.v2", tableId: described.tableId, displayName: "订单",
        kind: "base", schemaRevision: described.schemaRevision, dataRevision: 1,
        archivePolicy: { mode: "none", fieldId: null, archivedValue: null },
        fields: [], capabilities: [],
      })
      .mockResolvedValueOnce(orders)
      .mockResolvedValueOnce(customers)
      .mockResolvedValueOnce(regions)
      .mockResolvedValueOnce(orders)
      .mockResolvedValueOnce(customers);
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();

    await service.openCreate("tbl_opaque", "lookup");
    store.patchDraft({
      lookup: {
        path: [
          { relationFieldId: "fld_customer" },
          { relationFieldId: "fld_region" },
        ],
        targetFieldId: "fld_region_name",
      },
    });
    await service.loadLookupCatalog();

    expect(store.lookupSchemas.map(item => item.collection)).toEqual([
      "tbl_opaque", "tbl_customers", "tbl_regions",
    ]);
    const originalLookup = JSON.parse(JSON.stringify(store.draft?.lookup)) as unknown;
    await service.resolveLookupPath([{ relationFieldId: "fld_customer" }]);
    expect(store.draft?.lookup).toEqual(originalLookup);
    expect(store.lookupSchemas.map(item => item.collection)).toEqual([
      "tbl_opaque", "tbl_customers",
    ]);
    expect(JSON.stringify(store.lookupSchemas)).not.toContain("tbl_regions.fld_region_name");
  });

  it("previews path aggregations with the terminal source revision and cancels stale results", async () => {
    request.mockResolvedValueOnce(describeResult(true));
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    await service.openEdit("tbl_opaque", definition().identity.fieldId);
    const lookup = { path: [{ relationFieldId: "fld_customer" }], targetFieldId: "fld_balance", aggregation: "sum" as const };
    store.patchDraft({ logicalType: "lookup", lookup });
    store.lookupSchemas = [relationSchema("tbl_opaque").schema, { ...relationSchema("tbl_customers").schema, schemaRevision: "schema_9" }];
    request.mockResolvedValueOnce({ cell: { value: 40 } });
    service.previewLookupDraft(lookup);
    await vi.advanceTimersByTimeAsync(250);
    expect(request).toHaveBeenLastCalledWith("lookup.draft.preview", {
      tableId: "tbl_opaque", schemaRevision: "schema_7", sourceSchemaRevision: "schema_9", lookup,
    });
    expect(store.lookupPreview.value).toBe(40);
    let finish!: (value: unknown) => void;
    request.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    service.previewLookupDraft({ ...lookup, aggregation: "average" });
    await vi.advanceTimersByTimeAsync(250);
    service.previewLookupDraft(null);
    finish({ cell: { value: 10 } });
    await vi.advanceTimersByTimeAsync(0);
    expect(store.lookupPreview.ready).toBe(false);
    service.dispose();
  });

  it("debounces condition previews and invalidates them on edits, cancellation, and workspace changes", async () => {
    request.mockResolvedValueOnce(describeResult(true));
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    await service.openEdit("tbl_opaque", definition().identity.fieldId);
    const lookup = {
      path: [], targetFieldId: "fld_name",
      condition: { sourceTableId: "tbl_customers", match: "all" as const, distinct: false,
        rules: [{ sourceFieldId: "fld_code", operator: "eq" as const,
          operand: { kind: "constant" as const, value: "" } }] },
    };
    store.patchDraft({ logicalType: "lookup", lookup });
    store.lookupConditionSchema = relationSchema("tbl_customers").schema;
    let resolveFirst!: (value: unknown) => void;
    request.mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve; }));
    service.previewLookupDraft(lookup);
    await vi.advanceTimersByTimeAsync(249);
    expect(request).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(request).toHaveBeenLastCalledWith("lookup.draft.preview", {
      tableId: "tbl_opaque", schemaRevision: "schema_7", sourceSchemaRevision: "schema_7", lookup,
    });
    request.mockResolvedValueOnce({ cell: { value: [false, 0, ""] } });
    service.previewLookupDraft({ ...lookup, condition: { ...lookup.condition, distinct: true } });
    expect(store.lookupPreview.ready).toBe(false);
    resolveFirst({ cell: { value: ["stale"] } });
    await vi.advanceTimersByTimeAsync(250);
    expect(store.lookupPreview.value).toEqual([false, 0, ""]);
    service.previewLookupDraft(lookup);
    const requestsBeforeCancellation = request.mock.calls.length;
    service.previewLookupDraft(null);
    await vi.advanceTimersByTimeAsync(250);
    expect(request).toHaveBeenCalledTimes(requestsBeforeCancellation);
    expect(store.lookupPreview.ready).toBe(false);
    let resolveLate!: (value: unknown) => void;
    request.mockImplementationOnce(() => new Promise(resolve => { resolveLate = resolve; }));
    service.previewLookupDraft(lookup);
    await vi.advanceTimersByTimeAsync(250);
    useWorkspaceStore().setOpened([{ collection: "different_workspace" }], {});
    resolveLate({ cell: { value: ["wrong workspace"] } });
    await vi.advanceTimersByTimeAsync(0);
    expect(store.lookupPreview.ready).toBe(false);
    expect(store.lookupPreview.value).toBeUndefined();
    service.dispose();
  });

  it("discards a late source catalog and reports current catalog errors", async () => {
    request.mockResolvedValueOnce(describeResult(true));
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    await service.openEdit("tbl_opaque", definition().identity.fieldId);
    let finishSchema!: (value: unknown) => void;
    let finishDefinition!: (value: unknown) => void;
    request
      .mockImplementationOnce(() => new Promise(resolve => { finishSchema = resolve; }))
      .mockImplementationOnce(() => new Promise(resolve => { finishDefinition = resolve; }));
    const pending = service.selectLookupSource("tbl_customers");
    await service.selectLookupSource("");
    expect(store.lookupCatalogLoading).toBe(false);
    finishSchema(relationSchema("tbl_customers"));
    finishDefinition({
      contract: "vibetable.schema.v2", tableId: "tbl_customers", displayName: "客户", kind: "base",
      schemaRevision: "schema_7", dataRevision: 0,
      archivePolicy: { mode: "none", fieldId: null, archivedValue: null }, fields: [], capabilities: [],
    });
    await pending;
    expect(store.lookupConditionSchema).toBeNull();
    request.mockRejectedValueOnce(new Error("catalog unavailable")).mockResolvedValueOnce({});
    await service.selectLookupSource("tbl_customers");
    expect(store.lookupCatalogLoading).toBe(false);
    expect(store.lookupCatalogError).toBe("catalog unavailable");
    service.dispose();
  });

  it("loads the visual formula catalog from '0' and ignores stale sidecar validation results", async () => {
    const base = fixture<CapabilityV2>("capability.json");
    const formulaCapability: CapabilityV2 = {
      ...base,
      logicalType: "formula",
      userCreatable: true,
      advancedSettings: ["source", "autoType"],
    };
    const described: FieldSettingsDescribeResultV2 = {
      ...describeResult(false), capabilities: [formulaCapability],
    };
    const sourceSchema = {
      contract: "vibetable.schema-describe.v1",
      collection: "tbl_opaque",
      requestGeneration: 0,
      schema: {
        collection: "tbl_opaque", primaryKey: "id", primaryDisplayFieldId: "fld_price",
        columns: [
          {
            name: "id", title: "ID", fieldId: "id", kind: "system" as const,
            dataType: "text" as const, editable: false, nullable: false,
          },
          {
            name: "f_created", title: "Created", fieldId: "fld_created", kind: "system" as const,
            dataType: "dateTime" as const, editable: false, nullable: false,
          },
          {
            name: "f_price", title: "单价", fieldId: "fld_price", kind: "scalar" as const,
            dataType: "number" as const, editable: true, nullable: false,
          },
          {
            name: "f_lines", title: "明细", fieldId: "fld_lines", kind: "relation" as const,
            dataType: "relation" as const, editable: true, nullable: true,
            relationId: "tbl_opaque.fld_lines",
          },
        ],
        normalizedRelations: [{
          relationId: "tbl_opaque.fld_lines", kind: "o2m" as const,
          relatedCollection: "tbl_lines", relatedCollectionDisplayName: "明细",
          junction: null,
        }],
        schemaRevision: "schema_2", permissionRevision: "schema_2",
        capabilityHash: "cap", lookupRevision: "lookup",
      },
      capabilities: {
        contract: "vibetable.relation-capabilities.v1",
        relationReadV1: true, relationEditV1: true, lookupQueryV1: true, reason: null,
      },
    };
    const targetSchema = {
      ...sourceSchema,
      collection: "tbl_lines",
      schema: {
        ...sourceSchema.schema,
        collection: "tbl_lines",
        primaryDisplayFieldId: "fld_amount",
        columns: [{
          name: "f_amount", title: "金额", fieldId: "fld_amount", kind: "scalar" as const,
          dataType: "number" as const, editable: true, nullable: false,
        }],
        normalizedRelations: [],
      },
    };
    let resolveFirst!: (value: unknown) => void;
    let resolveSecond!: (value: unknown) => void;
    const first = new Promise(resolve => { resolveFirst = resolve; });
    const second = new Promise(resolve => { resolveSecond = resolve; });
    const functionInfo = {
      name: "IF",
      category: "逻辑",
      signature: "IF(bool, T, T)",
      description: "按条件返回分支",
      example: "IF({单价} > 0, {数量}, 0)",
    };
    const workingDocument = (displaySource: string) => ({
      displaySource,
      documentRevision: 1,
      tokens: [],
    });
    request.mockImplementation((method: string, params: Record<string, unknown>) => {
      if (method === "field.settings.describe") return Promise.resolve(described);
      if (method === "schema.describe") {
        return Promise.resolve(params.collection === "tbl_opaque" ? sourceSchema : targetSchema);
      }
      if (method === "formula.draft.validate") {
        if (params.restoreSource === true) {
          return Promise.resolve({
            canonicalSource: "0",
            resultType: "number",
            dependencies: [],
            relationAggregatePaths: [],
            authorDocument: workingDocument("0"),
            functions: [functionInfo],
          });
        }
        return params.displaySource === "{单价} * 2" ? first : second;
      }
      if (method === "formula.preview") {
        return Promise.resolve({ values: { f_formula_preview: 42.5 } });
      }
      throw new Error(`unexpected method ${method}`);
    });
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    const table = useTableStore();
    table.beginLoad();
    table.appendPage({
      table: "tbl_opaque",
      columns: [{
        name: "f_price", title: "单价", fieldId: "fld_price", kind: "scalar",
        dataType: "decimal", editable: true, nullable: false,
      }],
      rows: [{ rowKey: "order-1", id: "order-1", f_price: 21.25, f_created: "2026-09-19T00:00:00Z" }],
      offset: 0, limit: 1, totalRows: 1, mode: "remote",
    });

    await service.openCreate("tbl_opaque", "formula");
    await service.loadFormulaCatalog();

    expect(store.formulaSourceSchema?.columns.map(column => column.title))
      .toEqual(["ID", "Created", "单价", "明细"]);
    expect(store.formulaTargetSchemas.fld_lines?.columns[0]?.title).toBe("金额");

    // The empty-draft bootstrap fetched the catalog via "0" but adopted nothing.
    await vi.waitFor(() => {
      expect(store.formulaFunctions.map(item => item.name)).toEqual(["IF"]);
    });
    expect(request).toHaveBeenCalledWith("formula.draft.validate", {
      tableId: "tbl_opaque",
      displaySource: "0",
      restoreSource: true,
    });
    expect(store.formulaAuthorDocument).toBeNull();
    expect(store.draft?.formula?.source).toBe("");

    const older = service.validateFormulaDraft({
      kind: "document",
      displaySource: "{单价} * 2",
      authorDocument: workingDocument("{单价} * 2"),
    });
    const newer = service.validateFormulaDraft({
      kind: "document",
      displaySource: "SUM({明细}.{金额})",
      authorDocument: workingDocument("SUM({明细}.{金额})"),
    });
    resolveSecond({
      canonicalSource: 'relationSum(f_lines, "f_amount")',
      resultType: "number",
      dependencies: [],
      relationAggregatePaths: ["f_lines.f_amount"],
      authorDocument: workingDocument("SUM({明细}.{金额})"),
      functions: [functionInfo],
    });
    await newer;
    await vi.waitFor(() => {
      expect(store.formulaPreviewReady).toBe(true);
    });
    expect(store.formulaPreviewValue).toBe(42.5);
    expect(request).toHaveBeenCalledWith("formula.preview", expect.objectContaining({
      row: { f_price: 21.25, f_created: "2026-09-19T00:00:00Z" },
      changedFieldIds: [],
    }));
    resolveFirst({
      canonicalSource: "f_price * 2", resultType: "number",
      dependencies: ["f_price"], relationAggregatePaths: [],
      authorDocument: workingDocument("{单价} * 2"),
    });
    await older;

    expect(store.formulaValidatedSource).toBe("SUM({明细}.{金额})");
    expect(store.formulaValidation?.canonicalSource)
      .toBe('relationSum(f_lines, "f_amount")');
  });

  it("restores a persisted canonical source into a stable-token author document", async () => {
    const base = fixture<CapabilityV2>("capability.json");
    const formulaCapability: CapabilityV2 = {
      ...base,
      logicalType: "formula",
      userCreatable: true,
      advancedSettings: ["source", "autoType"],
    };
    const formulaDefinition = {
      ...definition(),
      logicalType: "formula" as const,
      displayName: "总价",
      identity: {
        fieldId: "fld_total",
        physicalName: "f_total",
        providerFieldId: "pb_total",
      },
      formula: {
        language: "cel-v1" as const,
        source: "f_price * 2",
        resultType: "number" as const,
      },
    };
    const described = {
      ...describeResult(true),
      fieldId: "fld_total",
      definition: formulaDefinition,
      capabilities: [formulaCapability],
    };
    const restoredDocument = {
      displaySource: "{单价} * 2",
      documentRevision: 3,
      tokens: [{
        range: { start: { line: 0, character: 0 }, end: { line: 0, character: 4 } },
        kind: "field" as const,
        fieldId: "fld_price",
        relationFieldId: null,
        targetFieldId: null,
      }],
    };
    const relationSchema = {
      contract: "vibetable.schema-describe.v1",
      collection: "tbl_opaque",
      requestGeneration: 0,
      schema: {
        collection: "tbl_opaque", primaryKey: "id", primaryDisplayFieldId: "fld_price",
        columns: [{
          name: "f_price", title: "单价", fieldId: "fld_price", kind: "scalar" as const,
          dataType: "number" as const, editable: true, nullable: false,
        }],
        normalizedRelations: [],
        schemaRevision: "schema_7", permissionRevision: "schema_7",
        capabilityHash: "cap", lookupRevision: "lookup",
      },
      capabilities: {
        contract: "vibetable.relation-capabilities.v1",
        relationReadV1: true, relationEditV1: true, lookupQueryV1: true, reason: null,
      },
    };
    request.mockImplementation((method: string, params: Record<string, unknown>) => {
      if (method === "field.settings.describe") return Promise.resolve(described);
      if (method === "schema.describe") return Promise.resolve(relationSchema);
      if (method === "formula.draft.validate" && params.restoreSource === true) {
        return Promise.resolve({
          canonicalSource: "f_price * 2",
          resultType: "number",
          dependencies: ["f_price"],
          relationAggregatePaths: [],
          authorDocument: restoredDocument,
          functions: [],
        });
      }
      throw new Error(`unexpected method ${method}`);
    });
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();

    await service.openEdit("tbl_opaque", "fld_total");
    await service.loadFormulaCatalog();

    await vi.waitFor(() => {
      expect(store.formulaValidationError).toBeNull();
      expect(store.formulaAuthorDocument?.displaySource).toBe("{单价} * 2");
    });
    expect(request).toHaveBeenCalledWith("formula.draft.validate", {
      tableId: "tbl_opaque",
      displaySource: "f_price * 2",
      restoreSource: true,
    });
    expect(store.formulaAuthorDocument?.tokens[0]?.fieldId).toBe("fld_price");
    expect(store.formulaValidatedSource).toBe("{单价} * 2");
    expect(store.formulaValidatedDocumentRevision).toBe(3);

    // An old workspace catalog must not bootstrap a restore in the next workspace.
    let resolveSchema!: (value: unknown) => void;
    request.mockImplementation((method: string) => {
      if (method === "schema.describe") return new Promise(resolve => { resolveSchema = resolve; });
      throw new Error(`unexpected late method ${method}`);
    });
    const pendingCatalog = service.loadFormulaCatalog();
    const workspace = useWorkspaceStore();
    workspace.setCollections([{ collection: "tbl_new" }], { tbl_new: "新工作区" });
    request.mockClear();
    resolveSchema(relationSchema);
    await pendingCatalog;
    expect(request).not.toHaveBeenCalledWith("formula.draft.validate", expect.anything());
    expect(store.formulaValidation).toBeNull();
  });

  async function previewLookupSample(value: JsonValueV2) {
    const document = { displaySource: "{匹配金额} * 2.0", documentRevision: 1, tokens: [] };
    request.mockImplementation((method: string) => {
      if (method === "field.settings.describe") {
        return Promise.resolve({ ...describeResult(false), capabilities: [formulaCapability()] });
      }
      if (method === "formula.draft.validate") {
        return Promise.resolve({
          canonicalSource: "f_lookup * 2.0", resultType: "number",
          dependencies: ["fld_lookup"], relationAggregatePaths: [], authorDocument: document,
        });
      }
      if (method === "formula.preview") {
        return Promise.resolve({ values: { f_formula_preview: 0 } });
      }
      throw new Error(`unexpected method ${method}`);
    });
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    const columns = [{
      name: "f_lookup", title: "匹配金额", fieldId: "fld_lookup", kind: "lookup" as const,
      dataType: "decimal" as const, editable: false, nullable: true,
    }, {
      name: "f_json", title: "用户 JSON", fieldId: "fld_json", kind: "scalar" as const,
      dataType: "json" as const, editable: true, nullable: true,
    }];
    const userObject = { state: "ok", value: 991, provenance: [] };
    const table = useTableStore();
    table.beginLoad();
    table.appendPage({
      table: "tbl_opaque", columns,
      rows: [{ rowKey: "r1", id: "r1", f_lookup: value, f_json: userObject }],
      offset: 0, limit: 1, totalRows: 1, mode: "remote",
    });
    await service.openCreate("tbl_opaque", "formula");
    store.setFormulaCatalog({ ...relationSchema("tbl_opaque").schema, columns }, {});
    await service.validateFormulaDraft({
      kind: "document", displaySource: document.displaySource, authorDocument: document,
    });
    await vi.advanceTimersByTimeAsync(300);
    return { store, userObject };
  }

  it.each([0, false, null, [0, 991]].map(value => ({ value })))(
    "previews successful Lookup cell values without changing user JSON ($value)", async ({ value }) => {
      const cell = {
        state: "ok", value, provenance: [], provenanceTotal: 0,
        provenanceTotalKnown: true, provenanceOffset: 0, provenanceLimit: 100,
        provenanceHasMore: false,
      };
      const { store, userObject } = await previewLookupSample(cell);
      expect(request).toHaveBeenCalledWith("formula.preview", expect.objectContaining({
        row: { f_lookup: value, f_json: userObject },
      }));
      expect(store.formulaPreviewReady).toBe(true);
      expect(useTableStore().allRows[0]?.f_lookup).toEqual(cell);
    },
  );

  it.each([0, false, null, [0, 991]].map(value => ({ value })))(
    "preserves already projected Lookup values ($value)", async ({ value }) => {
      const { userObject } = await previewLookupSample(value);
      expect(request).toHaveBeenCalledWith("formula.preview", expect.objectContaining({
        row: { f_lookup: value, f_json: userObject },
      }));
    },
  );

  it.each(["pending", "invalid", "restricted", "too_expensive"])(
    "does not preview a %s Lookup using its stale value", async state => {
      const { store } = await previewLookupSample({ state, value: 991, provenance: [] });
      expect(request.mock.calls.filter(([method]) => method === "formula.preview")).toEqual([]);
      expect(store.formulaPreviewReady).toBe(false);
      expect(store.formulaPreviewError).toContain("匹配金额");
      expect(store.formulaPreviewError).toContain(state);
    },
  );

  /** S38 catalog/preview race: source catalog resolves, relation target defers. */
  async function deferredFormulaCatalog() {
    const described = { ...describeResult(false), capabilities: [formulaCapability()] };
    const lookupColumn = {
      name: "f_lookup", title: "匹配金额", fieldId: "fld_lookup", kind: "lookup" as const,
      dataType: "decimal" as const, editable: false, nullable: true,
    };
    const relationColumn = {
      name: "f_lines", title: "明细", fieldId: "fld_lines", kind: "relation" as const,
      dataType: "relation" as const, editable: true, nullable: true,
      relationId: "tbl_opaque.fld_lines",
    };
    const sourceSchema = {
      contract: "vibetable.schema-describe.v1",
      collection: "tbl_opaque",
      requestGeneration: 0,
      schema: {
        collection: "tbl_opaque", primaryKey: "id", primaryDisplayFieldId: "fld_lookup",
        columns: [lookupColumn, relationColumn],
        normalizedRelations: [{
          relationId: "tbl_opaque.fld_lines", kind: "o2m" as const,
          relatedCollection: "tbl_lines", relatedCollectionDisplayName: "明细",
          junction: null,
        }],
        schemaRevision: "schema_7", permissionRevision: "schema_7",
        capabilityHash: "cap", lookupRevision: "lookup",
      },
      capabilities: {
        contract: "vibetable.relation-capabilities.v1",
        relationReadV1: true, relationEditV1: true, lookupQueryV1: true, reason: null,
      },
    };
    const document = { displaySource: "{匹配金额} + 1", documentRevision: 1, tokens: [] };
    let resolveTargetValue!: (value: unknown) => void;
    const targetSchemaDescribe = new Promise<unknown>(resolve => {
      resolveTargetValue = resolve;
    });
    request.mockImplementation((method: string, params: Record<string, unknown>) => {
      if (method === "field.settings.describe") return Promise.resolve(described);
      if (method === "schema.describe") {
        return params.collection === "tbl_lines"
          ? targetSchemaDescribe
          : Promise.resolve(sourceSchema);
      }
      if (method === "formula.draft.validate") {
        if (params.restoreSource === true) {
          return Promise.resolve({
            canonicalSource: "0", resultType: "number", dependencies: [],
            relationAggregatePaths: [], authorDocument: document, functions: [],
          });
        }
        if (params.displaySource !== document.displaySource) {
          return Promise.resolve({
            error: {
              code: "formula.parse", message: "公式无法解析", details: {}, retryable: false,
            },
          });
        }
        return Promise.resolve({
          canonicalSource: "f_lookup + 1", resultType: "number",
          dependencies: ["fld_lookup"], relationAggregatePaths: [], authorDocument: document,
        });
      }
      if (method === "formula.preview") {
        return Promise.resolve({ values: { f_formula_preview: 1 } });
      }
      throw new Error(`unexpected method ${method}`);
    });
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    const table = useTableStore();
    table.beginLoad();
    table.appendPage({
      table: "tbl_opaque",
      columns: [lookupColumn],
      rows: [{
        rowKey: "r1", id: "r1",
        f_lookup: {
          state: "ok", value: 0, provenance: [], provenanceTotal: 0,
          provenanceTotalKnown: true, provenanceOffset: 0, provenanceLimit: 100,
          provenanceHasMore: false,
        },
      }],
      offset: 0, limit: 1, totalRows: 1, mode: "remote",
    });
    await service.openCreate("tbl_opaque", "formula");
    const catalogLoad = service.loadFormulaCatalog();
    const previewCalls = () => request.mock.calls
      .filter(([method]) => method === "formula.preview");
    const restoreCalls = () => request.mock.calls
      .filter(([method, params]) => method === "formula.draft.validate"
        && (params as Record<string, unknown>).restoreSource === true);
    return {
      service, store, document, catalogLoad,
      resolveTarget: resolveTargetValue, previewCalls, restoreCalls,
    };
  }

  it("defers the sample preview until the formula catalog is installed", async () => {
    const { service, store, document, catalogLoad, resolveTarget, previewCalls, restoreCalls }
      = await deferredFormulaCatalog();

    await service.validateFormulaDraft({
      kind: "document", displaySource: document.displaySource, authorDocument: document,
    });
    await vi.advanceTimersByTimeAsync(300);

    // The relation-target catalog is still loading: the committed validation
    // must not leave with an empty activation row.
    expect(previewCalls()).toEqual([]);
    expect(restoreCalls()).toEqual([]);
    expect(store.formulaValidation?.canonicalSource).toBe("f_lookup + 1");
    expect(store.formulaPreviewNote).toContain("公式字段目录");

    resolveTarget(relationSchema("tbl_lines"));
    await catalogLoad;
    await vi.advanceTimersByTimeAsync(300);

    expect(previewCalls()).toHaveLength(1);
    expect(previewCalls()[0]?.[1]).toEqual(expect.objectContaining({
      row: { f_lookup: 0 },
      changedFieldIds: [],
    }));
    expect(store.formulaPreviewReady).toBe(true);
    expect(store.formulaPreviewValue).toBe(1);
    expect(store.formulaValidatedSource).toBe(document.displaySource);
  });

  it("does not replay the deferred validation preview after the drawer closes", async () => {
    const { service, store, document, catalogLoad, resolveTarget, previewCalls, restoreCalls }
      = await deferredFormulaCatalog();
    await service.validateFormulaDraft({
      kind: "document", displaySource: document.displaySource, authorDocument: document,
    });
    vi.stubGlobal("confirm", () => true);
    service.requestClose();

    resolveTarget(relationSchema("tbl_lines"));
    await catalogLoad;
    await vi.advanceTimersByTimeAsync(300);

    expect(store.open).toBe(false);
    expect(store.formulaValidation).toBeNull();
    expect(previewCalls()).toEqual([]);
    expect(restoreCalls()).toEqual([]);
  });

  it("does not replay the deferred validation preview after a newer document fails", async () => {
    const { service, store, document, catalogLoad, resolveTarget, previewCalls, restoreCalls }
      = await deferredFormulaCatalog();
    await service.validateFormulaDraft({
      kind: "document", displaySource: document.displaySource, authorDocument: document,
    });
    await service.validateFormulaDraft({
      kind: "document", displaySource: "{匹配金额} +",
      authorDocument: { displaySource: "{匹配金额} +", documentRevision: 2, tokens: [] },
    });

    resolveTarget(relationSchema("tbl_lines"));
    await catalogLoad;
    await vi.advanceTimersByTimeAsync(300);

    expect(store.formulaValidation).toBeNull();
    expect(store.formulaValidationError).toBe("公式无法解析");
    expect(previewCalls()).toEqual([]);
    expect(restoreCalls()).toEqual([]);
  });

  it("does not preview from the stale source while the catalog reloads", async () => {
    const described = { ...describeResult(false), capabilities: [formulaCapability()] };
    const priceColumn = {
      name: "f_price", title: "单价", fieldId: "fld_price", kind: "scalar" as const,
      dataType: "decimal" as const, editable: true, nullable: false,
    };
    const lookupColumn = {
      name: "f_lookup", title: "匹配金额", fieldId: "fld_lookup", kind: "lookup" as const,
      dataType: "decimal" as const, editable: false, nullable: true,
    };
    const relationColumn = {
      name: "f_lines", title: "明细", fieldId: "fld_lines", kind: "relation" as const,
      dataType: "relation" as const, editable: true, nullable: true,
      relationId: "tbl_opaque.fld_lines",
    };
    const schemaDescribe = (
      columns: readonly unknown[],
      normalizedRelations: readonly unknown[],
    ) => ({
      contract: "vibetable.schema-describe.v1",
      collection: "tbl_opaque",
      requestGeneration: 0,
      schema: {
        collection: "tbl_opaque", primaryKey: "id", primaryDisplayFieldId: "fld_price",
        columns, normalizedRelations,
        schemaRevision: "schema_7", permissionRevision: "schema_7",
        capabilityHash: "cap", lookupRevision: "lookup",
      },
      capabilities: {
        contract: "vibetable.relation-capabilities.v1",
        relationReadV1: true, relationEditV1: true, lookupQueryV1: true, reason: null,
      },
    });
    let catalogSource = schemaDescribe([priceColumn], []);
    let resolveTargetValue!: (value: unknown) => void;
    const targetSchemaDescribe = new Promise<unknown>(resolve => {
      resolveTargetValue = resolve;
    });
    const document = { displaySource: "{单价} + {匹配金额}", documentRevision: 1, tokens: [] };
    request.mockImplementation((method: string, params: Record<string, unknown>) => {
      if (method === "field.settings.describe") return Promise.resolve(described);
      if (method === "schema.describe") {
        return params.collection === "tbl_lines" ? targetSchemaDescribe
          : Promise.resolve(catalogSource);
      }
      if (method === "formula.draft.validate") {
        if (params.restoreSource === true) {
          return Promise.resolve({
            canonicalSource: "0", resultType: "number", dependencies: [],
            relationAggregatePaths: [], authorDocument: document, functions: [],
          });
        }
        return Promise.resolve({
          canonicalSource: "f_price + f_lookup", resultType: "number",
          dependencies: ["fld_price", "fld_lookup"], relationAggregatePaths: [],
          authorDocument: document,
        });
      }
      if (method === "formula.preview") {
        return Promise.resolve({ values: { f_formula_preview: 21 } });
      }
      throw new Error(`unexpected method ${method}`);
    });
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    const table = useTableStore();
    table.beginLoad();
    table.appendPage({
      table: "tbl_opaque",
      columns: [priceColumn, lookupColumn],
      rows: [{
        rowKey: "r2", id: "r2", f_price: 21,
        f_lookup: {
          state: "ok", value: 0, provenance: [], provenanceTotal: 0,
          provenanceTotalKnown: true, provenanceOffset: 0, provenanceLimit: 100,
          provenanceHasMore: false,
        },
      }],
      offset: 0, limit: 1, totalRows: 1, mode: "remote",
    });
    await service.openCreate("tbl_opaque", "formula");
    await service.loadFormulaCatalog();
    expect(store.formulaSourceSchema?.columns).toHaveLength(1);

    catalogSource = schemaDescribe([priceColumn, lookupColumn, relationColumn], [{
      relationId: "tbl_opaque.fld_lines", kind: "o2m" as const,
      relatedCollection: "tbl_lines", relatedCollectionDisplayName: "明细",
      junction: null,
    }]);
    const reload = service.loadFormulaCatalog();
    await service.validateFormulaDraft({
      kind: "document", displaySource: document.displaySource, authorDocument: document,
    });
    await vi.advanceTimersByTimeAsync(300);

    expect(request.mock.calls.filter(([method]) => method === "formula.preview")).toEqual([]);
    expect(store.formulaCatalogLoading).toBe(true);
    expect(store.formulaValidation?.canonicalSource).toBe("f_price + f_lookup");

    resolveTargetValue(relationSchema("tbl_lines"));
    await reload;
    await vi.advanceTimersByTimeAsync(300);

    const previews = request.mock.calls.filter(([method]) => method === "formula.preview");
    expect(previews).toHaveLength(1);
    expect(previews[0]?.[1]).toEqual(expect.objectContaining({
      row: { f_price: 21, f_lookup: 0 },
    }));
    expect(store.formulaSourceSchema?.columns).toHaveLength(3);
    expect(store.formulaPreviewValue).toBe(21);
  });

  it("invalidates the previous validation and preview as soon as the input changes", async () => {
    const described = { ...describeResult(false), capabilities: [formulaCapability()] };
    request.mockImplementation((method: string) => {
      if (method === "field.settings.describe") return Promise.resolve(described);
      if (method === "formula.draft.validate") {
        return Promise.resolve({
          canonicalSource: "f_price * 2",
          resultType: "number",
          dependencies: ["f_price"],
          relationAggregatePaths: [],
          authorDocument: { displaySource: "{单价} * 2", documentRevision: 1, tokens: [] },
        });
      }
      if (method === "formula.preview") {
        return Promise.resolve({ values: { f_formula_preview: 2 } });
      }
      throw new Error(`unexpected method ${method}`);
    });
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    const table = useTableStore();
    table.beginLoad();
    table.appendPage({
      table: "tbl_opaque",
      columns: [{
        name: "f_price", title: "单价", fieldId: "fld_price", kind: "scalar",
        dataType: "decimal", editable: true, nullable: false,
      }],
      rows: [{ rowKey: "r1", id: "r1", f_price: 1 }],
      offset: 0, limit: 1, totalRows: 1, mode: "remote",
    });
    await service.openCreate("tbl_opaque", "formula");
    store.setFormulaCatalog({ ...relationSchema("tbl_opaque").schema, columns: [{
      name: "f_price", title: "单价", fieldId: "fld_price", kind: "scalar",
      dataType: "decimal", editable: true, nullable: false,
    }] }, {});

    await service.validateFormulaDraft({
      kind: "document",
      displaySource: "{单价} * 2",
      authorDocument: { displaySource: "{单价} * 2", documentRevision: 1, tokens: [] },
    });
    await vi.waitFor(() => expect(store.formulaPreviewReady).toBe(true));
    expect(store.formulaValidation).not.toBeNull();

    await service.validateFormulaDraft({ kind: "invalidate" });

    expect(store.formulaValidation).toBeNull();
    expect(store.formulaValidatedSource).toBe("");
    expect(store.formulaValidatedDocumentRevision).toBeNull();
    expect(store.formulaPreviewReady).toBe(false);
    expect(store.formulaPreviewValue).toBeUndefined();
  });

  it("declares the preview field integer-only when the draft validation infers onlyInt", async () => {
    const described = { ...describeResult(false), capabilities: [formulaCapability()] };
    const previewFields: FieldDefinitionV2[] = [];
    let onlyInt: boolean | undefined = true;
    request.mockImplementation((method: string, params: Record<string, unknown>) => {
      if (method === "field.settings.describe") return Promise.resolve(described);
      if (method === "formula.draft.validate") {
        return Promise.resolve({
          canonicalSource: "YEAR(f_due)",
          resultType: "number",
          onlyInt,
          dependencies: ["fld_due"],
          relationAggregatePaths: [],
          authorDocument: { displaySource: "YEAR({到期})", documentRevision: 1, tokens: [] },
        });
      }
      if (method === "formula.preview") {
        previewFields.push((params as { field: FieldDefinitionV2 }).field);
        return Promise.resolve({ values: { f_formula_preview: 2024 } });
      }
      throw new Error(`unexpected method ${method}`);
    });
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    const table = useTableStore();
    table.beginLoad();
    table.appendPage({
      table: "tbl_opaque",
      columns: [{
        name: "f_due", title: "到期", fieldId: "fld_due", kind: "scalar",
        dataType: "date", editable: true, nullable: false,
      }],
      rows: [{ rowKey: "r1", id: "r1", f_due: "2024-05-06T00:00:00Z" }],
      offset: 0, limit: 1, totalRows: 1, mode: "remote",
    });
    await service.openCreate("tbl_opaque", "formula");
    store.setFormulaCatalog({ ...relationSchema("tbl_opaque").schema, columns: [{
      name: "f_due", title: "到期", fieldId: "fld_due", kind: "scalar",
      dataType: "date", editable: true, nullable: false,
    }] }, {});

    await service.validateFormulaDraft({
      kind: "document",
      displaySource: "YEAR({到期})",
      authorDocument: { displaySource: "YEAR({到期})", documentRevision: 1, tokens: [] },
    });
    await vi.waitFor(() => expect(store.formulaPreviewReady).toBe(true));
    expect(store.formulaPreviewValue).toBe(2024);
    expect(previewFields[0].storage.kind).toBe("computed");
    // The fresh draft inference must override the recommended decimal storage.
    expect(previewFields[0].storage.options.onlyInt).toBe(true);
    expect(previewFields[0].formula?.source).toBe("YEAR(f_due)");

    // An older host without onlyInt falls back to explicit decimal previews.
    onlyInt = undefined;
    await service.validateFormulaDraft({
      kind: "document",
      displaySource: "MAX(YEAR({到期}), 2024.5)",
      authorDocument: { displaySource: "MAX(YEAR({到期}), 2024.5)", documentRevision: 2, tokens: [] },
    });
    await vi.waitFor(() => expect(previewFields.length).toBe(2));
    expect(previewFields[1].storage.options.onlyInt).toBe(false);
  });

  it("rejects formula draft validations with a non-boolean onlyInt flag", async () => {
    const described = { ...describeResult(false), capabilities: [formulaCapability()] };
    request.mockImplementation((method: string) => {
      if (method === "field.settings.describe") return Promise.resolve(described);
      if (method === "formula.draft.validate") {
        return Promise.resolve({
          canonicalSource: "YEAR(f_due)",
          resultType: "number",
          onlyInt: "yes",
          dependencies: ["fld_due"],
          relationAggregatePaths: [],
        });
      }
      throw new Error(`unexpected method ${method}`);
    });
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    await service.openCreate("tbl_opaque", "formula");

    await service.validateFormulaDraft({
      kind: "document",
      displaySource: "YEAR({到期})",
      authorDocument: { displaySource: "YEAR({到期})", documentRevision: 1, tokens: [] },
    });

    await vi.waitFor(() => expect(store.formulaValidationError).not.toBeNull());
    expect(store.formulaValidation).toBeNull();
    expect(store.formulaPreviewReady).toBe(false);
  });

  it("drops validation results that no longer match the current schema or workspace", async () => {
    const described = { ...describeResult(false), capabilities: [formulaCapability()] };
    let resolveValidation!: (value: unknown) => void;
    request.mockImplementation((method: string) => {
      if (method === "field.settings.describe") return Promise.resolve(described);
      if (method === "formula.draft.validate") {
        return new Promise(resolve => { resolveValidation = resolve; });
      }
      throw new Error(`unexpected method ${method}`);
    });
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    const workspace = useWorkspaceStore();
    await service.openCreate("tbl_opaque", "formula");

    const first = service.validateFormulaDraft({
      kind: "document",
      displaySource: "{单价} * 2",
      authorDocument: { displaySource: "{单价} * 2", documentRevision: 1, tokens: [] },
    });
    store.result = { ...store.result!, schemaRevision: "schema_9" };
    resolveValidation({
      canonicalSource: "f_price * 2",
      resultType: "number",
      dependencies: ["f_price"],
      relationAggregatePaths: [],
      authorDocument: { displaySource: "{单价} * 2", documentRevision: 1, tokens: [] },
    });
    await first;
    expect(store.formulaValidation).toBeNull();

    workspace.setCollections([{ collection: "tbl_before" }], { tbl_before: "之前" });
    const second = service.validateFormulaDraft({
      kind: "document",
      displaySource: "{单价} * 3",
      authorDocument: { displaySource: "{单价} * 3", documentRevision: 2, tokens: [] },
    });
    workspace.setCollections([{ collection: "tbl_other" }], { tbl_other: "其他" });
    resolveValidation({
      canonicalSource: "f_price * 3",
      resultType: "number",
      dependencies: ["f_price"],
      relationAggregatePaths: [],
      authorDocument: { displaySource: "{单价} * 3", documentRevision: 2, tokens: [] },
    });
    await second;
    expect(store.formulaValidation).toBeNull();
    expect(store.formulaValidatedSource).toBe("");
  });

  it("aborts in-flight validation when the drawer closes or the service is disposed", async () => {
    const described = { ...describeResult(false), capabilities: [formulaCapability()] };
    let resolveValidation!: (value: unknown) => void;
    request.mockImplementation((method: string) => {
      if (method === "field.settings.describe") return Promise.resolve(described);
      if (method === "formula.draft.validate") {
        return new Promise(resolve => { resolveValidation = resolve; });
      }
      throw new Error(`unexpected method ${method}`);
    });
    vi.stubGlobal("confirm", () => true);
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    await service.openCreate("tbl_opaque", "formula");

    const pending = service.validateFormulaDraft({
      kind: "document",
      displaySource: "{单价} * 2",
      authorDocument: { displaySource: "{单价} * 2", documentRevision: 1, tokens: [] },
    });
    expect(store.formulaValidating).toBe(true);
    service.requestClose();
    resolveValidation({
      canonicalSource: "f_price * 2",
      resultType: "number",
      dependencies: ["f_price"],
      relationAggregatePaths: [],
      authorDocument: { displaySource: "{单价} * 2", documentRevision: 1, tokens: [] },
    });
    await pending;

    expect(store.open).toBe(false);
    expect(store.formulaValidation).toBeNull();
    expect(store.formulaValidating).toBe(false);
  });

  it("surfaces typed same-operation field errors without mis-parsing them as results", async () => {
    request
      .mockResolvedValueOnce(describeResult(true))
      .mockResolvedValueOnce({
        error: {
          code: "field.contract.invalid",
          path: "file",
          message: "file limits must be positive",
          details: {},
          retryable: false,
        },
      });
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();

    await service.openEdit("tbl_opaque", definition().identity.fieldId);
    store.patchDraft({ displayName: "Broken" });
    await service.plan();

    expect(store.phase).toBe("failed");
    expect(store.errorCode).toBe("field.contract.invalid");
    expect(store.error).toBe("file limits must be positive");
  });

  it("loads only the selected formula source and discards obsolete responses", async () => {
    request.mockResolvedValueOnce({
      ...describeResult(false), capabilities: [formulaCapability()],
    });
    const service = useFieldSettingsService();
    const store = useFieldSettingsStore();
    await service.openCreate("tbl_opaque", "formula");
    store.setRelationTables([
      { tableId: "tbl_opaque", displayName: "订单" },
      { tableId: "tbl_customers", displayName: "客户" },
    ]);
    let resolveOld!: (value: unknown) => void;
    request.mockImplementation((_method: string, params: Record<string, unknown>) => {
      if (params.collection === "tbl_opaque") return new Promise(resolve => { resolveOld = resolve; });
      return Promise.resolve(relationSchema("tbl_customers"));
    });
    const old = service.selectFormulaSource("tbl_opaque");
    expect(store.formulaCollectionLoading).toBe(true);
    await service.selectFormulaSource("tbl_customers");
    expect(store.formulaCollectionSchema?.collection).toBe("tbl_customers");
    resolveOld(relationSchema("tbl_opaque"));
    await old;
    expect(store.formulaCollectionSchema?.collection).toBe("tbl_customers");
    expect(store.formulaCollectionLoading).toBe(false);
    expect(request.mock.calls.slice(1).map(call => call[0])).toEqual([
      "schema.describe", "schema.describe",
    ]);
    service.dispose();
  });

});
