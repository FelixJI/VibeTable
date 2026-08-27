import { describe, expect, it, vi } from "vitest";
import { ref } from "vue";
import type { ApplyImportResult, ExportResult, ImportPlan, SessionPathGrant } from "@/contracts";
import type { ImportPreviewSession } from "@/services/dataIoService";
import { useDataIoTask, type DataIoTaskPort } from "./useDataIoTask";

function previewSession(): ImportPreviewSession {
  return {
    grant: {
      grantId: "grant-1",
      purpose: "import_source",
      direction: "read",
      displayName: "orders.csv",
      sizeBytes: 128,
      mimeType: "text/csv",
      expiresAt: 1,
    } satisfies SessionPathGrant,
    plan: {
      collection: "orders",
      schemaRevision: "schema_0001",
      capabilityHash: "capability-1",
      sourceHash: "source-1",
      token: { token: "preview-1", expiresAt: 1, consumed: false },
      summary: {
        totalRows: 2,
        validRows: 2,
        errorRows: 0,
        warningRows: 0,
        errorCount: 0,
        warningCount: 0,
      },
      rows: [],
      unmatchedColumns: [],
      diagnostics: [],
    } satisfies ImportPlan,
    mode: "create_only",
  };
}

function setup(
  overrides: Partial<DataIoTaskPort> = {},
  resolveContext: () => { readonly collection?: string | null; readonly schemaRevision?: string | null }
    = () => ({ collection: "orders", schemaRevision: "schema_0001" }),
) {
  const session = previewSession();
  const service: DataIoTaskPort = {
    busy: ref(false),
    previewImport: vi.fn(async () => session),
    applyImport: vi.fn(async () => ({
      collection: "orders",
      createdCount: 2,
      updatedCount: 0,
      failedRows: [],
      chunks: [],
      requestIds: [],
    } satisfies ApplyImportResult)),
    exportData: vi.fn(async () => ({
      collection: "orders",
      format: "csv",
      rowsWritten: 3,
      schemaRevision: "schema_0001",
      capabilityHash: "capability-1",
      outputDisplayName: "orders-export.csv",
    } satisfies ExportResult)),
    cancelActive: vi.fn(async () => undefined),
    ...overrides,
  };
  const importSucceeded = vi.fn();
  const exportSucceeded = vi.fn();
  const reportError = vi.fn();
  const refresh = vi.fn();
  const task = useDataIoTask({
    service,
    resolveContext,
    importSucceeded,
    exportSucceeded,
    reportError,
    refresh,
  });
  return { task, service, session, importSucceeded, exportSucceeded, reportError, refresh };
}

describe("useDataIoTask", () => {
  it("uses one reactive admission contract for import and export commands", async () => {
    const context = ref<{ collection: string | null; schemaRevision: string | null }>({
      collection: "orders",
      schemaRevision: null,
    });
    const { task, service } = setup({}, () => context.value);

    expect(task.canPreviewImport.value).toBe(false);
    expect(task.canExport.value).toBe(true);
    await task.previewImport();
    expect(service.previewImport).not.toHaveBeenCalled();

    context.value = { collection: "orders", schemaRevision: "schema_0001" };
    expect(task.canPreviewImport.value).toBe(true);
    await task.previewImport();
    expect(service.previewImport).toHaveBeenCalledOnce();

    context.value = { collection: null, schemaRevision: null };
    expect(task.canPreviewImport.value).toBe(false);
    expect(task.canExport.value).toBe(false);
  });

  it("owns the preview/apply lifecycle and refreshes only after a successful apply", async () => {
    const { task, service, session, importSucceeded, refresh } = setup();

    await task.previewImport();
    expect(task.previewSession.value).toEqual(session);
    expect(service.previewImport).toHaveBeenCalledWith("orders", "schema_0001");

    await task.applyImport();
    expect(service.applyImport).toHaveBeenCalledWith(session);
    expect(importSucceeded).toHaveBeenCalledWith(2);
    expect(refresh).toHaveBeenCalledOnce();
    expect(task.previewSession.value).toBeNull();
    expect(task.applyError.value).toBeNull();
  });

  it("keeps a failed import preview actionable and preserves cancellation state semantics", async () => {
    const applyFailure = new Error("row validation failed");
    const cancelFailure = new Error("cancel unavailable");
    let rejectApply!: (error: Error) => void;
    const applying = new Promise<ApplyImportResult>((_resolve, reject) => { rejectApply = reject; });
    const { task, service, session } = setup({
      applyImport: vi.fn(() => applying),
      cancelActive: vi.fn(async () => { throw cancelFailure; }),
    });

    await task.previewImport();
    const pendingApply = task.applyImport();
    task.dismissPreview();
    expect(task.previewSession.value).toEqual(session);

    await task.cancelImport();
    expect(service.cancelActive).toHaveBeenCalledOnce();
    expect(task.cancelling.value).toBe(false);
    expect(task.applyError.value).toBe("cancel unavailable");

    rejectApply(applyFailure);
    await pendingApply;
    expect(task.previewSession.value).toEqual(session);
    expect(task.applyError.value).toBe("row validation failed");

    task.dismissPreview();
    expect(task.previewSession.value).toBeNull();
  });

  it("serializes preview/export commands and routes foreground failures", async () => {
    let resolvePreview!: (session: ImportPreviewSession) => void;
    const preview = new Promise<ImportPreviewSession>((resolve) => { resolvePreview = resolve; });
    const { task, service, session, exportSucceeded, reportError } = setup({
      previewImport: vi.fn(() => preview),
      exportData: vi.fn(async () => { throw new Error("disk full"); }),
    });

    const pending = task.previewImport();
    await task.exportData("csv");
    expect(service.exportData).not.toHaveBeenCalled();
    resolvePreview(session);
    await pending;

    task.dismissPreview();
    await task.cancelActiveTask();
    expect(service.cancelActive).toHaveBeenCalledOnce();
    await task.exportData("csv");
    expect(exportSucceeded).not.toHaveBeenCalled();
    expect(reportError).toHaveBeenCalledWith("disk full");
  });

  it("forwards the selected interoperable export format through the task seam", async () => {
    const { task, service } = setup();

    await task.exportData("xlsx");

    expect(service.exportData).toHaveBeenCalledWith("orders", {}, "xlsx");
  });

  it("admits only one export while the target picker is pending", async () => {
    let resolveExport!: (result: ExportResult) => void;
    const pendingExport = new Promise<ExportResult>((resolve) => { resolveExport = resolve; });
    const { task, service } = setup({
      exportData: vi.fn(() => pendingExport),
    });

    const first = task.exportData("csv");
    const second = task.exportData("csv");
    expect(service.exportData).toHaveBeenCalledOnce();
    expect(task.canPreviewImport.value).toBe(false);
    expect(task.canExport.value).toBe(false);

    resolveExport({
      collection: "orders",
      format: "csv",
      rowsWritten: 3,
      schemaRevision: "schema_0001",
      capabilityHash: "capability-1",
      outputDisplayName: "orders-export.csv",
    });
    await Promise.all([first, second]);
    expect(task.canPreviewImport.value).toBe(true);
    expect(task.canExport.value).toBe(true);
  });
});
