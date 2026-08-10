import { afterEach, describe, expect, it, vi } from "vitest";
import type { HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useDataIoService } from "./dataIoService";

describe("dataIoService", () => {
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
    await service.exportData("orders", {});

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
});
