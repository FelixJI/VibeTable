import { afterEach, describe, expect, it, vi } from "vitest";
import { BridgeOperationError, type HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useDataIoService } from "./dataIoService";

describe("dataIoService", () => {
  it.each(["draining", "active", "retired"] as const)("retains an admitted task after an in-flight status lease is cancelled with session %s", async (afterRejection) => {
    vi.useFakeTimers();
    let session: "active" | "draining" | "retired" = "active";
    let rejectStatus!: (reason: unknown) => void;
    const status = new Promise((_, reject) => { rejectStatus = reject; });
    let polls = 0;
    const request = vi.fn(async (method: string) => {
      if (method === "data.exportTargetRequested") return { grantId: "grant-out" };
      if (method === "task.create") return { taskId: "owned-task", state: "running" };
      if (method === "task.status") {
        if (++polls === 1) return await status;
        return { taskId: "owned-task", state: "succeeded", result: { rowsWritten: 1 } };
      }
      if (method === "task.cancel") return { state: "cancelling" };
      throw new Error(`Unexpected request ${method}`);
    });
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useDataIoService();
    const result = service.exportData("orders", {}, "csv", undefined, () => undefined, () => session)
      .catch((error: unknown) => error);
    try {
      await vi.advanceTimersByTimeAsync(100);
      expect(polls).toBe(1);
      session = afterRejection;
      rejectStatus(new BridgeOperationError({
        message: "The workspace request was cancelled because its session ended.",
        code: "workspace.session_stale",
      }));
      await vi.advanceTimersByTimeAsync(0);
      expect(service.activeTaskId.value).toBe("owned-task");
      expect(service.busy.value).toBe(true);
      if (session === "draining") {
        await vi.advanceTimersByTimeAsync(300);
        await service.cancelActive();
        expect(polls).toBe(1);
        expect(request).not.toHaveBeenCalledWith("task.cancel", expect.anything());
        session = "active";
      }
      if (session === "active") {
        await service.cancelActive();
        expect(request).toHaveBeenCalledWith("task.cancel", { taskId: "owned-task" });
        await vi.advanceTimersByTimeAsync(100);
        expect(await result).toEqual({ rowsWritten: 1 });
        expect(polls).toBe(2);
      } else {
        await vi.advanceTimersByTimeAsync(100);
        expect(await result).toBeInstanceOf(Error);
        expect(polls).toBe(1);
        await service.cancelActive();
        expect(request).not.toHaveBeenCalledWith("task.cancel", expect.anything());
      }
      expect(service.busy.value).toBe(false);
    } finally {
      session = "retired";
      await vi.advanceTimersByTimeAsync(100);
      await result;
      vi.useRealTimers();
    }
  });

  it("reports ordinary status errors without treating them as session cancellation", async () => {
    vi.useFakeTimers();
    const failure = new BridgeOperationError({ message: "Backend failed", code: "PRODUCT_DATA_FAILED" });
    const request = vi.fn(async (method: string) => {
      if (method === "data.exportTargetRequested") return { grantId: "grant-out" };
      if (method === "task.create") return { taskId: "owned-task", state: "running" };
      throw failure;
    });
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useDataIoService();
    const result = service.exportData("orders", {}).catch((error: unknown) => error);
    try {
      await vi.advanceTimersByTimeAsync(100);
      expect(await result).toBe(failure);
      expect(service.busy.value).toBe(false);
      expect(request).toHaveBeenCalledTimes(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it("holds task ownership while its workspace drains and never polls a replacement session", async () => {
    vi.useFakeTimers();
    let session: "active" | "draining" | "retired" = "active";
    const request = vi.fn(async (method: string) => {
      if (method === "data.exportTargetRequested") return { grantId: "grant-out" };
      if (method === "task.create") return { taskId: "old-workspace-task", state: "running" };
      return { taskId: "old-workspace-task", state: "succeeded", result: {} };
    });
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useDataIoService();
    const result = service.exportData("orders", {}, "csv", undefined, () => undefined, () => session)
      .catch((error: unknown) => error);
    try {
      await vi.advanceTimersByTimeAsync(0);
      session = "draining";
      await vi.advanceTimersByTimeAsync(100);
      expect(service.busy.value).toBe(true);
      await service.cancelActive();
      expect(request.mock.calls.map(([method]) => method)).toEqual(["data.exportTargetRequested", "task.create"]);
      session = "retired";
      await vi.advanceTimersByTimeAsync(100);
      await result;
      expect(service.busy.value).toBe(false);
      expect(request.mock.calls.map(([method]) => method)).toEqual(["data.exportTargetRequested", "task.create"]);
    } finally {
      session = "retired";
      await vi.advanceTimersByTimeAsync(100);
      await result;
      vi.useRealTimers();
    }
  });

  it.each(["creation receipt", "status poll"] as const)("keeps admitted tasks cancellable after scope retirement during %s", async (boundary) => {
    vi.useFakeTimers();
    let resolveCreated!: (value: unknown) => void;
    let resolveStatus!: (value: unknown) => void;
    const created = new Promise((resolve) => { resolveCreated = resolve; });
    const status = new Promise((resolve) => { resolveStatus = resolve; });
    let current = true;
    const request = vi.fn(async (method: string) => {
      if (method === "data.exportTargetRequested") return { grantId: "grant-out" };
      if (method === "task.create") return await created;
      if (method === "task.status") return await status;
      if (method === "task.cancel") return { state: "cancelling" };
      throw new Error(`Unexpected request ${method}`);
    });
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useDataIoService();
    const result = service.exportData("orders", {}, "csv", undefined, () => {
      if (!current) throw new Error("retired");
    }).catch((error: unknown) => error);
    try {
      await vi.waitFor(() => expect(request).toHaveBeenCalledWith("task.create", expect.anything()));
      if (boundary === "creation receipt") current = false;
      resolveCreated({ taskId: "owned-task", state: "running" });
      await vi.advanceTimersByTimeAsync(0);
      if (boundary === "status poll") current = false;
      await vi.advanceTimersByTimeAsync(100);
      expect(service.busy.value).toBe(true);
      await service.cancelActive();
      expect(request).toHaveBeenCalledWith("task.cancel", { taskId: "owned-task" });
    } finally {
      resolveStatus({ taskId: "owned-task", state: "succeeded", result: { rowsWritten: 1 } });
      await result;
      vi.useRealTimers();
    }
    expect(service.busy.value).toBe(false);
  });

  it.each(["import", "export"] as const)("does not continue a %s picker after its scope retires", async (kind) => {
    let resolvePicker!: (value: unknown) => void;
    const picker = new Promise((resolve) => { resolvePicker = resolve; });
    const request = vi.fn(() => picker);
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useDataIoService();
    let current = true;
    const assertCurrent = () => { if (!current) throw new Error("retired"); };
    const result = kind === "import"
      ? service.previewImport("orders", "schema-7", assertCurrent)
      : service.exportData("orders", {}, "csv", undefined, assertCurrent);
    const rejected = expect(result).rejects.toThrow("retired");
    current = false;
    resolvePicker({ grantId: "grant-retired" });
    await rejected;
    expect(request).toHaveBeenCalledTimes(1);
    expect(service.busy.value).toBe(false);
  });

  afterEach(() => {
    setHostBridgeForTesting(null);
    vi.restoreAllMocks();
  });

  it("uses opaque native grants for preview/apply and export", async () => {
    const methods: string[] = [];
    const request = vi.fn(async (type: string, _params?: unknown) => {
      methods.push(type);
      if (type === "data.importSourceRequested") {
        return {
          grantId: "grant-in", purpose: "import_source", direction: "read",
          displayName: "orders.csv", sizeBytes: 12, mimeType: "text/csv", expiresAt: 1,
        };
      }
      if (type === "data.previewImport") {
        return {
          collection: "orders", schemaRevision: "schema-7",
          summary: {
            totalRows: 2, validRows: 2, errorRows: 0,
            warningRows: 0, errorCount: 0, warningCount: 0,
          },
          rows: [],
          sourceColumns: ["number", "Partner Code"],
          unmatchedColumns: [],
          diagnostics: [],
          token: { token: "preview-token", expiresAt: 2, consumed: false },
        };
      }
      if (type === "data.applyImport") {
        return { collection: "orders", createdCount: 2, updatedCount: 0, failedRows: [] };
      }
      if (type === "task.create") {
        return {
          taskId: methods.includes("data.exportTargetRequested") ? "export-1" : "import-1",
          kind: methods.includes("data.exportTargetRequested") ? "data.export" : "data.import",
          state: "succeeded",
          progress: { done: 2, total: 2, message: "done" },
          result: methods.includes("data.exportTargetRequested")
            ? {
                collection: "orders", format: "csv", rowsWritten: 2,
                outputDisplayName: "orders.csv",
              }
            : { collection: "orders", createdCount: 2, updatedCount: 0, failedRows: [] },
          error: null,
        };
      }
      if (type === "data.exportTargetRequested") {
        return {
          grantId: "grant-out", purpose: "export_target", direction: "write",
          displayName: "orders.csv", sizeBytes: null, mimeType: null, expiresAt: 1,
        };
      }
      return {
        collection: "orders", format: "csv", rowsWritten: 2,
        outputDisplayName: "orders.csv",
      };
    });
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useDataIoService();

    const preview = await service.previewImport("orders", "schema-7");
    await service.applyImport(preview);
    await service.exportData("orders", {}, "xlsx");

    expect(methods).toEqual([
      "data.importSourceRequested",
      "data.previewImport",
      "task.create",
      "data.exportTargetRequested",
      "task.create",
    ]);
    expect(request.mock.calls[0]?.[1]).toEqual({
      accept: [".xlsx", ".xlsm", ".csv"],
    });
    expect(request).toHaveBeenCalledWith("data.exportTargetRequested", {
      defaultName: "orders-export.xlsx",
      format: "xlsx",
    });
    expect(request).toHaveBeenCalledWith("task.create", {
      kind: "data.export",
      params: {
        grantId: "grant-out",
        collection: "orders",
        query: {},
        format: "xlsx",
        includeRelations: true,
        lookupIds: [],
      },
    });
    expect(JSON.stringify(request.mock.calls)).not.toContain("\\\\");
  });

  it("surfaces a safe atomic import failure instead of reporting zero rows as success", async () => {
    const request = vi.fn(async (type: string) => {
      if (type === "data.importSourceRequested") {
        return {
          grantId: "grant-in", purpose: "import_source", direction: "read",
          displayName: "orders.csv", sizeBytes: 12, mimeType: "text/csv", expiresAt: 1,
        };
      }
      if (type === "data.previewImport") {
        return {
          collection: "orders", schemaRevision: "schema-7",
          summary: {
            totalRows: 1, validRows: 1, errorRows: 0,
            warningRows: 0, errorCount: 0, warningCount: 0,
          },
          token: { token: "preview-token", expiresAt: 2, consumed: false },
        };
      }
      return {
        taskId: "import-1",
        kind: "data.import",
        state: "succeeded",
        progress: {
          done: 1, total: 1,
          message: "atomic import failed [mutation.schema_revision_conflict]",
        },
        result: {
          collection: "orders", createdCount: 0, updatedCount: 0, failedRows: [2],
        },
        error: null,
      };
    });
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useDataIoService();
    const preview = await service.previewImport("orders", "schema-7");
    await expect(service.applyImport(preview))
      .rejects.toThrow("atomic import failed [mutation.schema_revision_conflict]");
  });

  it("cancels only the currently tracked data task", async () => {
    const request = vi.fn(async () => ({ state: "cancelled" }));
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useDataIoService();

    await service.cancelActive();
    expect(request).not.toHaveBeenCalled();

    service.activeTaskId.value = "import-1";
    await service.cancelActive();
    expect(request).toHaveBeenCalledWith("task.cancel", { taskId: "import-1" });
  });

  it("re-previews the same grant with the explicit relation mapping", async () => {
    const previewCalls: unknown[] = [];
    const request = vi.fn(async (type: string, params?: unknown) => {
      if (type === "data.previewImport") {
        previewCalls.push(params);
        return {
          collection: "orders", schemaRevision: "schema-7",
          summary: {
            totalRows: 1, validRows: 1, errorRows: 0,
            warningRows: 0, errorCount: 0, warningCount: 0,
          },
          rows: [],
          sourceColumns: ["number", "Partner Code"],
          unmatchedColumns: [],
          diagnostics: [],
          token: { token: `preview-${previewCalls.length}`, expiresAt: 2, consumed: false },
        };
      }
      return {};
    });    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useDataIoService();

    await service.previewImportWithGrant(
      { grantId: "grant-in" } as never,
      "orders",
      "schema-7",
      [{
        sourceColumn: "Partner Code",
        targetField: "partner",
        relationId: "orders.fld_partner",
        matchField: "fld_code",
      }],
    );

    expect(request).toHaveBeenCalledWith("data.previewImport", {
      grantId: "grant-in",
      collection: "orders",
      schemaRevision: "schema-7",
      mode: "create_only",
      columnMapping: [{
        sourceColumn: "Partner Code",
        targetField: "partner",
        relationId: "orders.fld_partner",
        matchField: "fld_code",
      }],
    });
  });

  it("binds selected Lookup export columns to the describe schema revision", async () => {
    const request = vi.fn(async (type: string) => {
      if (type === "data.exportTargetRequested") {
        return {
          grantId: "grant-out", purpose: "export_target", direction: "write",
          displayName: "orders.csv", sizeBytes: null, mimeType: null, expiresAt: 1,
        };
      }
      if (type === "task.create") {
        return {
          taskId: "export-1",
          kind: "data.export",
          state: "succeeded",
          progress: { done: 1, total: 1, message: "done" },
          result: {
            collection: "orders", format: "csv", rowsWritten: 1,
            outputDisplayName: "orders.csv",
          },
          error: null,
        };
      }
      return {};
    });
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useDataIoService();

    await service.exportData("orders", {}, "csv", {
      lookupIds: ["lkp-1"],
      lookupRevision: "schema-7",
    });
    expect(request).toHaveBeenCalledWith("task.create", {
      kind: "data.export",
      params: {
        grantId: "grant-out",
        collection: "orders",
        query: {},
        format: "csv",
        includeRelations: true,
        lookupIds: ["lkp-1"],
        lookupRevision: "schema-7",
      },
    });

    request.mockClear();
    await service.exportData("orders", {}, "csv");
    expect(JSON.stringify(request.mock.calls)).not.toContain("lookupRevision");
  });

  it("builds relation options from the public catalogs with unique match fields only", async () => {
    const request = vi.fn(async (type: string, params?: unknown) => {
      if (type === "schema.describe") {
        return {
          contract: "vibetable.schema-describe.v1",
          collection: "orders",
          requestGeneration: (params as { requestGeneration: number }).requestGeneration,
          schema: {
            collection: "orders",
            schemaRevision: "schema-7",
            columns: [
              { name: "number", title: "编号", kind: "scalar", editable: true },
              {
                name: "partner", title: "合作方", kind: "relation", editable: true,
                relationId: "orders.fld_partner",
              },
              {
                name: "tags", title: "标签", kind: "relation", editable: true,
                relationId: "orders.fld_tags",
              },
              {
                name: "owner", title: "负责人", kind: "relation", editable: false,
                relationId: "orders.fld_owner",
              },
            ],
            normalizedRelations: [
              {
                relationId: "orders.fld_partner", fieldRef: "fld_partner",
                sourceCollection: "orders", kind: "m2o", state: "valid",
                relatedCollection: "partners",
              },
              {
                relationId: "orders.fld_tags", fieldRef: "fld_tags",
                sourceCollection: "orders", kind: "o2m", state: "valid",
                relatedCollection: "partners",
              },
              {
                relationId: "orders.fld_owner", fieldRef: "fld_owner",
                sourceCollection: "orders", kind: "m2o", state: "valid",
                relatedCollection: "partners",
              },
              {
                relationId: "orders.fld_stale", fieldRef: "fld_stale",
                sourceCollection: "orders", kind: "m2o", state: "invalid",
                relatedCollection: "partners",
              },
            ],
          },
          capabilities: {},
        };
      }
      if (type === "schema.getTable") {
        return {
          contract: "vibetable.schema.v2",
          tableId: "partners",
          displayName: "合作方表",
          fields: [
            {
              displayName: "编码",
              identity: { fieldId: "fld_code", physicalName: "f_code", providerFieldId: "p1" },
              lifecycle: { state: "active", retiredAt: null },
              constraints: { unique: { enabled: true, blankPolicy: "ignoreMissing" } },
            },
            {
              displayName: "名称",
              identity: { fieldId: "fld_name", physicalName: "f_name", providerFieldId: "p2" },
              lifecycle: { state: "active", retiredAt: null },
              constraints: { unique: { enabled: false, blankPolicy: "ignoreMissing" } },
            },
            {
              displayName: "旧编码",
              identity: { fieldId: "fld_old", physicalName: "f_old", providerFieldId: "p3" },
              lifecycle: { state: "retired", retiredAt: "2026-01-01T00:00:00Z" },
              constraints: { unique: { enabled: true, blankPolicy: "ignoreMissing" } },
            },
          ],
        };
      }
      return {};
    });
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useDataIoService();

    const options = await service.loadRelationImportOptions("orders");

    expect(options).toEqual([{
      targetField: "partner",
      relationId: "orders.fld_partner",
      targetCollection: "partners",
      targetDisplayName: "合作方表",
      sourceDisplayName: "合作方",
      matchFields: [{ fieldId: "fld_code", displayName: "编码" }],
    }]);
    expect(request).toHaveBeenCalledWith("schema.getTable", { tableId: "partners" });
  });

  it("loads the Lookup export catalog with the source describe revision, not the list revision", async () => {
    const request = vi.fn(async (type: string, params?: unknown) => {
      if (type === "schema.describe") {
        return {
          contract: "vibetable.schema-describe.v1",
          collection: "orders",
          requestGeneration: (params as { requestGeneration: number }).requestGeneration,
          schema: { collection: "orders", schemaRevision: "schema-7" },
          capabilities: {},
        };
      }
      if (type === "lookup.list") {
        return {
          collection: "orders",
          lookupRevision: "lookup-rev-9",
          definitions: [
            {
              lookupId: "lkp-1", displayName: "合作方标签", state: "valid",
              outputType: "text",
            },
            {
              lookupId: "lkp-2", displayName: "失效列", state: "invalid",
              outputType: "text",
            },
          ],
        };
      }
      return {};
    });
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useDataIoService();

    const context = await service.loadExportLookupContext("orders");

    expect(context.lookupRevision).toBe("schema-7");
    expect(context.options).toEqual([
      { lookupId: "lkp-1", displayName: "合作方标签", outputType: "text" },
    ]);
  });

  it("rejects catalog responses that do not match the requested collection", async () => {
    const request = vi.fn(async (type: string) => {
      if (type === "schema.describe") {
        return {
          contract: "vibetable.schema-describe.v1",
          collection: "invoices",
          requestGeneration: 1,
          schema: { collection: "invoices", schemaRevision: "schema-7" },
          capabilities: {},
        };
      }
      return {};
    });
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useDataIoService();

    await expect(service.loadRelationImportOptions("orders")).rejects.toThrow();
    await expect(service.loadExportLookupContext("orders")).rejects.toThrow();
  });
});
