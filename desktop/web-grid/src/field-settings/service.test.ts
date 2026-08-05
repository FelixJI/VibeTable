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
