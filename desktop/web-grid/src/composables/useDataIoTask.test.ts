import { describe, expect, it, vi } from "vitest";
import { ref } from "vue";
import type { ApplyImportResult, ExportResult, ImportPlan, SessionPathGrant } from "@/contracts";
import type {
  ExportLookupContext,
  ExportLookupSelection,
  ImportColumnMappingPayload,
  ImportPreviewSession,
  RelationImportOption,
} from "@/services/dataIoService";
import { useDataIoTask, type DataIoTaskPort, type RelationMappingDraft } from "./useDataIoTask";

function previewSession(token = "preview-1"): ImportPreviewSession {
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
      token: { token, expiresAt: 1, consumed: false },
      summary: {
        totalRows: 2,
        validRows: 2,
        errorRows: 0,
        warningRows: 0,
        errorCount: 0,
        warningCount: 0,
      },
      rows: [],
      sourceColumns: ["number", "Partner Code"],
      unmatchedColumns: [],
      diagnostics: [],
    } satisfies ImportPlan,
    mode: "create_only",
  };
}

const relationOptions: readonly RelationImportOption[] = [{
  targetField: "partner",
  relationId: "orders.fld_partner",
  targetCollection: "partners",
  targetDisplayName: "Partners",
  sourceDisplayName: "Partner",
  matchFields: [{ fieldId: "fld_code", displayName: "Code" }],
}];

const exportContext: ExportLookupContext = {
  options: [{ lookupId: "lkp-1", displayName: "Partner label", outputType: "text" }],
  lookupRevision: "schema_0001",
};

type Context = {
  collection: string | null;
  schemaRevision: string | null;
  workspaceId?: string | null;
  sessionEpoch?: number | null;
  available?: boolean;
};

function setup(
  overrides: Partial<DataIoTaskPort> = {},
  resolveContext: () => Context
    = () => ({ collection: "orders", schemaRevision: "schema_0001" }),
) {
  const session = previewSession();
  const service: DataIoTaskPort = {
    busy: ref(false),
    previewImport: vi.fn(async () => session),
    previewImportWithGrant: vi.fn(async (): Promise<ImportPlan> => ({
      ...session.plan,
      token: { token: "preview-2", expiresAt: 1, consumed: false },
    })),
    loadRelationImportOptions: vi.fn(async () => relationOptions),
    loadExportLookupContext: vi.fn(async () => exportContext),
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

const mappingRow: RelationMappingDraft = {
  sourceColumn: "Partner Code",
  relationId: "orders.fld_partner",
  matchField: "fld_code",
};

describe("useDataIoTask", () => {
  it("resolves a stable relation choice to its current physical name after schema refresh", async () => {
    const context = ref<Context>({ collection: "orders", schemaRevision: "schema_0001" });
    const catalog = vi.fn(async () => relationOptions);
    const { task, service } = setup({ loadRelationImportOptions: catalog }, () => context.value);
    await task.previewImport();
    task.setRelationConfig([mappingRow]);
    catalog.mockResolvedValue([{ ...relationOptions[0]!, targetField: "renamed_partner" }]);
    context.value.schemaRevision = "schema_0002";
    await Promise.resolve();
    await task.repreviewImport();
    expect(service.previewImportWithGrant).toHaveBeenCalledWith(expect.anything(), "orders", "schema_0002", [
      { sourceColumn: "Partner Code", targetField: "renamed_partner", relationId: "orders.fld_partner", matchField: "fld_code" },
    ]);
  });

  it("keeps the newest relation catalog when earlier reads complete last", async () => {
    const context = ref<Context>({ collection: "orders", schemaRevision: "schema_0001" });
    const replies: ((value: readonly RelationImportOption[]) => void)[] = [];
    const { task } = setup({ loadRelationImportOptions: vi.fn(() => new Promise<readonly RelationImportOption[]>((resolve) => replies.push(resolve))) }, () => context.value);
    const preview = task.previewImport();
    await Promise.resolve();
    context.value.schemaRevision = "schema_0002";
    replies[1]!([{ ...relationOptions[0]!, sourceDisplayName: "Current" }]);
    await Promise.resolve();
    replies[0]!([{ ...relationOptions[0]!, sourceDisplayName: "Old" }]);
    await preview;
    expect(task.relationOptions.value?.[0]?.sourceDisplayName).toBe("Current");
    expect(task.relationOptionsLoading.value).toBe(false);
  });

  it("retires pending previews permanently when a transition starts before epoch rotation", async () => {
    const context = ref<Context>({ collection: "orders", schemaRevision: "schema_0001", available: true });
    let resolvePreview!: (value: ImportPreviewSession) => void;
    const pending = new Promise<ImportPreviewSession>((resolve) => { resolvePreview = resolve; });
    const { task, service } = setup({ previewImport: vi.fn(() => pending) }, () => context.value);
    const preview = task.previewImport();
    context.value.available = false;
    expect(task.canExport.value).toBe(false);
    context.value.available = true;
    resolvePreview(previewSession());
    await preview;
    expect(task.previewSession.value).toBeNull();
    expect(service.loadRelationImportOptions).not.toHaveBeenCalled();
  });

  it("retires a preview when leaving and returning to the same table before its reply", async () => {
    const context = ref<Context>({ collection: "orders", schemaRevision: "schema_0001" });
    let resolvePreview!: (value: ImportPreviewSession) => void;
    const pending = new Promise<ImportPreviewSession>((resolve) => { resolvePreview = resolve; });
    const { task } = setup({ previewImport: vi.fn(() => pending) }, () => context.value);
    const preview = task.previewImport();
    context.value.collection = "invoices";
    context.value.collection = "orders";
    resolvePreview(previewSession());
    await preview;
    expect(task.previewSession.value).toBeNull();
  });

  it("does not report export success in a retired session", async () => {
    const context = ref<Context>({ collection: "orders", schemaRevision: "schema_0001", sessionEpoch: 1 });
    let resolveExport!: (value: ExportResult) => void;
    const pending = new Promise<ExportResult>((resolve) => { resolveExport = resolve; });
    const { task, exportSucceeded } = setup({ exportData: vi.fn(() => pending) }, () => context.value);
    await task.exportData("csv");
    const confirmed = task.confirmExportData();
    context.value.sessionEpoch = 2;
    resolveExport({ collection: "orders", format: "csv", rowsWritten: 3, schemaRevision: "schema_0001",
      capabilityHash: "capability-1", outputDisplayName: "orders.csv" });
    await confirmed;
    expect(exportSucceeded).not.toHaveBeenCalled();
  });

  it("does not make an in-flight repreview current after schema changes", async () => {
    const context = ref<Context>({ collection: "orders", schemaRevision: "schema_0001" });
    let resolvePlan!: (value: ImportPlan) => void;
    const pending = new Promise<ImportPlan>((resolve) => { resolvePlan = resolve; });
    const { task, service } = setup({ previewImportWithGrant: vi.fn(() => pending) }, () => context.value);
    await task.previewImport();
    task.setRelationConfig([mappingRow]);
    const preview = task.repreviewImport();
    context.value.schemaRevision = "schema_0002";
    resolvePlan({ ...previewSession().plan, token: { token: "retired", expiresAt: 1, consumed: false } });
    await preview;
    expect(task.schemaDrifted.value).toBe(true);
    expect(task.previewSession.value?.plan.token.token).not.toBe("retired");
    await task.applyImport();
    expect(service.applyImport).not.toHaveBeenCalled();
  });

  it("uses one reactive admission contract for import and export commands", async () => {
    const context = ref<Context>({ collection: "orders", schemaRevision: null });
    const { task, service } = setup({}, () => context.value);

    expect(task.canPreviewImport.value).toBe(false);
    expect(task.canExport.value).toBe(true);
    await task.previewImport();
    expect(service.previewImport).not.toHaveBeenCalled();

    context.value = { collection: "orders", schemaRevision: "schema_0001" };
    expect(task.canPreviewImport.value).toBe(true);
    await task.previewImport();
    expect(service.previewImport).toHaveBeenCalledOnce();

    task.dismissPreview();
    context.value = { collection: null, schemaRevision: null };
    expect(task.canPreviewImport.value).toBe(false);
    expect(task.canExport.value).toBe(false);
  });

  it("owns the preview/apply lifecycle and refreshes only after a successful apply", async () => {
    const { task, service, session, importSucceeded, refresh } = setup();

    await task.previewImport();
    expect(task.previewSession.value).toEqual(session);
    expect(service.previewImport).toHaveBeenCalledWith("orders", "schema_0001", expect.any(Function));
    expect(task.relationOptions.value).toEqual(relationOptions);

    await task.applyImport();
    expect(service.applyImport).toHaveBeenCalledWith(session, expect.any(Function));
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
    expect(service.loadExportLookupContext).not.toHaveBeenCalled();
    resolvePreview(session);
    await pending;

    task.dismissPreview();
    await task.cancelActiveTask();
    expect(service.cancelActive).toHaveBeenCalledOnce();
    await task.exportData("csv");
    await task.confirmExportData();
    expect(exportSucceeded).not.toHaveBeenCalled();
    expect(reportError).toHaveBeenCalledWith("disk full");
  });

  it("forwards the selected interoperable export format through the task seam", async () => {
    const { task, service } = setup();

    await task.exportData("xlsx");
    await task.confirmExportData();

    expect(service.exportData).toHaveBeenCalledWith("orders", {}, "xlsx", undefined, expect.any(Function));
  });

  it("admits only one export while the target picker is pending", async () => {
    let resolveExport!: (result: ExportResult) => void;
    const pendingExport = new Promise<ExportResult>((resolve) => { resolveExport = resolve; });
    const { task, service } = setup({
      exportData: vi.fn(() => pendingExport),
    });

    const opened = task.exportData("csv");
    await task.exportData("csv");
    expect(service.loadExportLookupContext).toHaveBeenCalledOnce();
    expect(task.canPreviewImport.value).toBe(false);
    expect(task.canExport.value).toBe(false);
    await opened;

    const pending = task.confirmExportData();
    expect(service.exportData).toHaveBeenCalledOnce();

    resolveExport({
      collection: "orders",
      format: "csv",
      rowsWritten: 3,
      schemaRevision: "schema_0001",
      capabilityHash: "capability-1",
      outputDisplayName: "orders-export.csv",
    });
    await pending;
    expect(task.canPreviewImport.value).toBe(true);
    expect(task.canExport.value).toBe(true);
  });

  it("requires a re-preview after a mapping change and never applies a stale plan", async () => {
    const { task, service } = setup();

    await task.previewImport();
    expect(task.mappingDirty.value).toBe(false);
    expect(task.canRepreview.value).toBe(false);

    task.setRelationConfig([mappingRow]);
    expect(task.mappingDirty.value).toBe(true);
    expect(task.canRepreview.value).toBe(true);

    await task.applyImport();
    expect(service.applyImport).not.toHaveBeenCalled();

    await task.repreviewImport();
    expect(service.previewImportWithGrant).toHaveBeenCalledWith(
      expect.objectContaining({ grantId: "grant-1" }),
      "orders",
      "schema_0001",
      [{
        sourceColumn: "Partner Code",
        targetField: "partner",
        relationId: "orders.fld_partner",
        matchField: "fld_code",
      }] satisfies ImportColumnMappingPayload[],
    );
    expect(task.mappingDirty.value).toBe(false);
    expect(task.previewSession.value?.plan.token.token).toBe("preview-2");
    expect(task.canRepreview.value).toBe(false);
  });

  it("rejects invalid mapping rows instead of sending ambiguous mappings", async () => {
    const { task, service } = setup();

    await task.previewImport();
    task.setRelationConfig([mappingRow, { ...mappingRow, matchField: "fld_missing" }]);
    expect(task.mappingDirty.value).toBe(true);

    await task.repreviewImport();
    expect(service.previewImportWithGrant).not.toHaveBeenCalled();
    expect(task.applyError.value).toContain("映射配置无效");
    expect(task.mappingDirty.value).toBe(true);
  });

  it("drops a late preview result once the session epoch retires", async () => {
    const context = ref<Context>({ collection: "orders", schemaRevision: "schema_0001", sessionEpoch: 1 });
    let resolvePreview!: (session: ImportPreviewSession) => void;
    const preview = new Promise<ImportPreviewSession>((resolve) => { resolvePreview = resolve; });
    const { task } = setup({ previewImport: vi.fn(() => preview) }, () => context.value);

    const pending = task.previewImport();
    context.value = { ...context.value, sessionEpoch: 2 };
    resolvePreview(previewSession());
    await pending;

    expect(task.previewSession.value).toBeNull();
    expect(task.previewing.value).toBe(false);
    expect(task.canPreviewImport.value).toBe(true);
  });

  it("drops a late export catalog once the workspace session retires", async () => {
    const context = ref<Context>({ collection: "orders", schemaRevision: "schema_0001", sessionEpoch: 1 });
    let resolveContext!: (context: ExportLookupContext) => void;
    const pendingContext = new Promise<ExportLookupContext>((resolve) => { resolveContext = resolve; });
    const { task, service } = setup(
      { loadExportLookupContext: vi.fn(() => pendingContext) },
      () => context.value,
    );

    const opened = task.exportData("csv");
    context.value = { ...context.value, sessionEpoch: 2 };
    resolveContext(exportContext);
    await opened;

    expect(task.exportPanel.value).toBeNull();
    expect(service.exportData).not.toHaveBeenCalled();
  });

  it("marks a schema drift so confirmation requires a fresh preview", async () => {
    const context = ref<Context>({ collection: "orders", schemaRevision: "schema_0001" });
    const { task } = setup({}, () => context.value);

    await task.previewImport();
    expect(task.schemaDrifted.value).toBe(false);

    context.value = { ...context.value, schemaRevision: "schema_0002" };
    expect(task.schemaDrifted.value).toBe(true);
    await Promise.resolve();
    expect(task.canRepreview.value).toBe(true);
  });

  it("dismisses the open preview panel when the active table changes", async () => {
    const context = ref<Context>({ collection: "orders", schemaRevision: "schema_0001" });
    const { task } = setup({}, () => context.value);

    await task.previewImport();
    expect(task.previewSession.value).not.toBeNull();

    context.value = { ...context.value, collection: "invoices" };
    expect(task.previewSession.value).toBeNull();
    expect(task.relationOptions.value).toBeNull();
  });

  it("runs the confirmed export with the selected Lookup ids and describe revision", async () => {
    const { task, service, exportSucceeded } = setup();

    await task.exportData("csv");
    expect(task.exportPanel.value?.options).toEqual(exportContext.options);
    expect(task.exportPanel.value?.lookupRevision).toBe("schema_0001");

    task.setExportLookupIds(["lkp-1", "lkp-unknown"]);
    expect(task.exportLookupIds.value).toEqual(["lkp-1"]);

    await task.confirmExportData();
    expect(service.exportData).toHaveBeenCalledWith("orders", {}, "csv", {
      lookupIds: ["lkp-1"],
      lookupRevision: "schema_0001",
    } satisfies ExportLookupSelection, expect.any(Function));
    expect(exportSucceeded).toHaveBeenCalledOnce();
    expect(task.exportPanel.value).toBeNull();
  });

  it("cancelling the export panel issues no grant request and no task", async () => {
    const { task, service } = setup();

    await task.exportData("csv");
    task.setExportLookupIds(["lkp-1"]);
    task.cancelExportPanel();

    expect(task.exportPanel.value).toBeNull();
    expect(service.exportData).not.toHaveBeenCalled();
    expect(task.canExport.value).toBe(true);
  });
});
