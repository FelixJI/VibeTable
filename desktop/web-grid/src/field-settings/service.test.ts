import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { CapabilityV2, FieldDefinitionV2, FieldSettingsDescribeResultV2 } from "@/contracts";
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

function plan(confirmations: readonly string[] = []): Record<string, unknown> {
  return { ...fixture<Record<string, unknown>>("field-change-plan.json"), confirmations };
}

function receipt(migrationJobId = ""): Record<string, unknown> {
  return { ...fixture<Record<string, unknown>>("apply-receipt.json"), migrationJobId };
}

function migration(phase: string): Record<string, unknown> {
  return { ...fixture<Record<string, unknown>>("migration-status.json"), phase };
}

describe("field settings service", () => {
  const request = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    setActivePinia(createPinia());
    setHostBridgeForTesting({ request } as unknown as HostBridge);
  });

  afterEach(() => {
    setHostBridgeForTesting(null);
    vi.useRealTimers();
    vi.unstubAllGlobals();
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
      { collection: "tbl_opaque", displayName: "订单" },
      { collection: "tbl_customers", displayName: "客户" },
    ]);
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

  it("loads the visual formula catalog and ignores stale sidecar validation results", async () => {
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
    request.mockImplementation((method: string, params: Record<string, unknown>) => {
      if (method === "field.settings.describe") return Promise.resolve(described);
      if (method === "schema.describe") {
        return Promise.resolve(params.collection === "tbl_opaque" ? sourceSchema : targetSchema);
      }
      if (method === "schema.getTable") {
        return Promise.resolve({
          contractVersion: "2.0",
          tableId: "tbl_opaque",
          physicalName: "orders",
          displayName: "订单",
          kind: "base",
          schemaRevision: "schema_2",
          archivePolicy: { mode: "none", fieldId: null, archivedValue: null },
          indexes: [],
          fields: [{
            fieldId: "fld_price", physicalName: "f_price", displayName: "单价",
            kind: "scalar", dataType: "float", storageType: "number",
            nullable: false, defaultValue: null, constraints: [],
            editor: { kind: "number", config: {} }, readOnly: false,
            formula: null, relation: null, lookup: null, attachmentPolicy: null,
          }],
        });
      }
      if (method === "formula.draft.validate") {
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
      rows: [{ rowKey: "order-1", id: "order-1", f_price: 21.25 }],
      offset: 0, limit: 1, totalRows: 1, mode: "client",
    });

    await service.openCreate("tbl_opaque", "formula");
    await service.loadFormulaCatalog();

    expect(store.formulaSourceSchema?.columns.map(column => column.title))
      .toEqual(["单价", "明细"]);
    expect(store.formulaTargetSchemas.fld_lines?.columns[0]?.title).toBe("金额");

    const older = service.validateFormulaDraft("{单价} * 2");
    const newer = service.validateFormulaDraft("SUM({明细}.{金额})");
    resolveSecond({
      canonicalSource: 'relationSum(f_lines, "f_amount")',
      resultType: "number",
      dependencies: [],
      relationAggregatePaths: ["f_lines.f_amount"],
    });
    await newer;
    await vi.waitFor(() => {
      expect(store.formulaPreviewReady).toBe(true);
    });
    expect(store.formulaPreviewValue).toBe(42.5);
    expect(request).toHaveBeenCalledWith("formula.preview", expect.objectContaining({
      row: { id: "order-1", f_price: 21.25 },
      changedFieldIds: [],
    }));
    resolveFirst({
      canonicalSource: "f_price * 2", resultType: "number",
      dependencies: ["f_price"], relationAggregatePaths: [],
    });
    await older;

    expect(store.formulaValidatedSource).toBe("SUM({明细}.{金额})");
    expect(store.formulaValidation?.canonicalSource)
      .toBe('relationSum(f_lines, "f_amount")');
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

});
