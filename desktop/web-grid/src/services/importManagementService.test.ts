import { afterEach, describe, expect, it, vi } from "vitest";
import { BridgeOperationError, type HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useImportManagementService } from "./importManagementService";
import { SOURCE_IMPORT_CONTRACT, type ImportHistoryEntry, type SourceImportEntry } from "@/contracts/importManagement";

function entry(overrides: Partial<ImportHistoryEntry> = {}): ImportHistoryEntry {
  return {
    taskId: "task-1",
    collection: "orders",
    sourceType: "csv",
    sourceName: "orders.csv",
    state: "succeeded",
    commitState: "committed",
    createdCount: 3,
    updatedCount: 1,
    startedAt: "2026-10-01T08:00:00Z",
    finishedAt: "2026-10-01T08:00:05Z",
    sessionEpoch: 1,
    errorCode: null,
    ...overrides,
  };
}

/** Wire-shaped source-migration receipt (Go sourceimport.Result). */
function migrationReceipt(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    contract: SOURCE_IMPORT_CONTRACT,
    jobId: "job-1",
    provider: "feishu",
    containerId: "space-1",
    sourceName: "云端空间",
    state: "interrupted",
    stage: "records",
    created: 400,
    total: 403,
    notSubmitted: 3,
    unknownRecords: 0,
    unknownBatch: "b-relations",
    targets: [{ sourceTableId: "src_orders", tableId: "tbl_orders", name: "Orders", collection: "t_orders9f2a" }],
    batches: [{
      jobId: "job-1", batchId: "b-records", stage: "records", tableId: "tbl_orders",
      created: 400, relationWrites: 12, attachmentWrites: 2,
    }],
    diagnostics: [{ code: "source_import.relation_edges", tableId: "", fieldId: "", recordId: "", message: "raw 内部诊断文本", blocking: true }],
    startedAt: "2026-10-01T08:00:00Z",
    finishedAt: null,
    sessionEpoch: 1,
    readWindow: { startedAt: "2026-10-01T08:00:00Z", finishedAt: "2026-10-01T08:05:00Z", consistency: "window" },
    fields: [
      { source: { provider: "feishu", containerId: "space-1", tableId: "src_orders", fieldId: "f_title" }, kind: "text", policy: "native", definition: "SUM(SECRET-EXPRESSION)" },
      { source: { provider: "feishu", containerId: "space-1", tableId: "src_orders", fieldId: "f_calc" }, kind: "formula", policy: "snapshot", definition: "SUM({a})" },
      { source: { provider: "feishu", containerId: "space-1", tableId: "src_orders", fieldId: "f_person" }, kind: "person", policy: "skip", definition: "" },
    ],
    ...overrides,
  };
}

describe("importManagementService", () => {
  afterEach(() => {
    setHostBridgeForTesting(null);
    vi.restoreAllMocks();
  });

  it("loads history once and exposes current-session active entries", async () => {
    const request = vi.fn(async () => ({ items: [
      entry({ taskId: "run-1", state: "running", commitState: "unknown",
        createdCount: null, updatedCount: null, finishedAt: null }),
      entry({ taskId: "done-1" }),
    ] }));
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useImportManagementService();
    service.bindScope(() => ({ workspaceId: "ws-1", sessionEpoch: 1 }));

    await service.refresh();

    expect(request).toHaveBeenCalledWith("data.importHistory", {});
    expect(service.loaded.value).toBe(true);
    expect(service.items.value?.map((item) => item.taskId)).toEqual(["run-1", "done-1"]);
    expect(service.activeEntries.value.map((item) => item.taskId)).toEqual(["run-1"]);
    expect(service.error.value).toBeNull();
  });

  it("drops a late history reply whose workspace scope retired", async () => {
    let resolveHistory!: (value: { items: ImportHistoryEntry[] }) => void;
    const pending = new Promise<{ items: ImportHistoryEntry[] }>((resolve) => { resolveHistory = resolve; });
    const request = vi.fn(() => pending);
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useImportManagementService();
    let scope = { workspaceId: "ws-1", sessionEpoch: 1 };
    service.bindScope(() => scope);

    const loading = service.refresh();
    scope = { workspaceId: "ws-2", sessionEpoch: 2 };
    resolveHistory({ items: [entry()] });
    await loading;

    expect(service.items.value).toBeNull();
    expect(service.loaded.value).toBe(false);
    expect(service.loading.value).toBe(false);
  });

  it("surfaces a failed refresh as a fixed localized error without echoing raw transport text", async () => {
    const request = vi.fn(async () => {
      throw new Error("GET https://host/rpc?token=SECRET-TOKEN-URL failed");
    });
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useImportManagementService();
    service.bindScope(() => ({ workspaceId: "ws-1", sessionEpoch: 1 }));

    await service.refresh();

    expect(service.error.value).toContain("IMPORT_HISTORY_UNAVAILABLE");
    expect(service.error.value).not.toContain("SECRET-TOKEN-URL");
    expect(service.error.value).not.toContain("https://");
    expect(service.items.value).toBeNull();
    expect(service.loaded.value).toBe(false);
  });

  it("keeps a stable bridge error code as the locating hint", async () => {
    const request = vi.fn(async () => {
      throw new BridgeOperationError({
        message: "raw detail with https://token",
        code: "PRODUCT_DATA_UNAVAILABLE",
      });
    });
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useImportManagementService();
    service.bindScope(() => ({ workspaceId: "ws-1", sessionEpoch: 1 }));

    await service.refresh();

    expect(service.error.value).toContain("PRODUCT_DATA_UNAVAILABLE");
    expect(service.error.value).not.toContain("https://");
  });

  it("rejects entries that break the state or count invariants instead of rendering them", async () => {
    const request = vi.fn(async () => ({ items: [
      entry({ state: "weird-state" as ImportHistoryEntry["state"] }),
    ] }));
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useImportManagementService();
    service.bindScope(() => ({ workspaceId: "ws-1", sessionEpoch: 1 }));

    await service.refresh();

    expect(service.error.value).toBeTruthy();
    expect(service.items.value).toBeNull();
    expect(service.loaded.value).toBe(false);

    const counts = vi.fn(async () => ({ items: [
      entry({ commitState: "committed", createdCount: "3" as unknown as number }),
    ] }));
    setHostBridgeForTesting({ request: counts } as unknown as HostBridge);
    const second = useImportManagementService();
    second.bindScope(() => ({ workspaceId: "ws-1", sessionEpoch: 1 }));
    await second.refresh();
    expect(second.items.value).toBeNull();
    expect(second.error.value).toBeTruthy();
  });

  it.each([
    { commitState: "unknown", createdCount: 0, updatedCount: 0 },
    { commitState: "committed", createdCount: null },
    { createdCount: -1 },
    { state: "running" },
  ] as Partial<ImportHistoryEntry>[])("rejects false commit evidence %j", async (overrides) => {
    setHostBridgeForTesting({ request: vi.fn(async () => ({ items: [entry(overrides)] })) } as unknown as HostBridge);
    const service = useImportManagementService();
    await service.refresh();
    expect(service.items.value).toBeNull();
    expect(service.error.value).toBeTruthy();
  });

  it("keeps an in-use projection and stops polling inputs after retire", async () => {
    const request = vi.fn(async () => ({ items: [entry()] }));
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useImportManagementService();
    service.bindScope(() => ({ workspaceId: "ws-1", sessionEpoch: 1 }));

    await service.refresh();
    service.retire();

    expect(service.items.value).toBeNull();
    expect(service.loaded.value).toBe(false);
    expect(service.loading.value).toBe(false);
    expect(service.activeEntries.value).toEqual([]);

    // A late reply from before retire must not resurrect the projection.
    await service.refresh();
    expect(service.loaded.value).toBe(true);
  });

  it("rejects a malformed history payload without rendering it", async () => {
    const request = vi.fn(async () => ({ items: "not-a-list" }));
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useImportManagementService();
    service.bindScope(() => ({ workspaceId: "ws-1", sessionEpoch: 1 }));

    await service.refresh();

    expect(service.error.value).toBeTruthy();
    expect(service.items.value).toBeNull();
    expect(service.loaded.value).toBe(false);
  });

  it("cancels a projected task through the existing task.cancel bridge method", async () => {
    const request = vi.fn(async () => ({ state: "cancelling" }));
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useImportManagementService();

    await service.cancelTask("task-9");

    expect(request).toHaveBeenCalledWith("task.cancel", { taskId: "task-9" });
  });

  it("treats a legacy host without migrations as an empty migration history", async () => {
    const request = vi.fn(async () => ({ items: [entry()] }));
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useImportManagementService();
    service.bindScope(() => ({ workspaceId: "ws-1", sessionEpoch: 1 }));

    await service.refresh();

    expect(service.error.value).toBeNull();
    expect(service.loaded.value).toBe(true);
    expect(service.sourceEntries.value).toEqual([]);
    // The strict CSV/XLSX entry invariants stay untouched.
    expect(service.items.value?.map((item) => item.taskId)).toEqual(["task-1"]);
  });

  it("validates migration receipts and reduces provenance to policy counts", async () => {
    const request = vi.fn(async () => ({ items: [entry()], migrations: [
      migrationReceipt({ jobId: "job-live", state: "running", created: 0, notSubmitted: 403, unknownBatch: null }),
      migrationReceipt({
        jobId: "job-done", state: "succeeded", stage: "settled", created: 403,
        notSubmitted: 0, unknownRecords: 0, unknownBatch: null,
        finishedAt: "2026-10-01T08:06:00Z",
      }),
    ] }));
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useImportManagementService();
    service.bindScope(() => ({ workspaceId: "ws-1", sessionEpoch: 1 }));

    await service.refresh();

    expect(service.error.value).toBeNull();
    const jobs = service.sourceEntries.value?.map((item) => item.jobId);
    expect(jobs).toEqual(["job-live", "job-done"]);
    expect(service.activeSourceEntries.value.map((item) => item.jobId)).toEqual(["job-live"]);
    const projected = service.sourceEntries.value?.[0] as SourceImportEntry;
    // Raw definitions/identities never survive into UI state; only counts.
    expect(projected.snapshotFieldCount).toBe(1);
    expect(projected.skippedFieldCount).toBe(1);
    expect(JSON.stringify(projected)).not.toContain("SECRET-EXPRESSION");
    expect(projected.diagnostics).toEqual([{ code: "source_import.relation_edges", blocking: true }]);
    // The committed physical identity passes through for target navigation.
    expect(projected.targets[0]?.collection).toBe("t_orders9f2a");
  });

  it.each([
    { name: "count conflict", overrides: { created: 400, notSubmitted: 3, unknownRecords: 1, total: 403 } },
    { name: "succeeded while not terminal", overrides: { state: "succeeded", created: 403, notSubmitted: 0, unknownRecords: 0, unknownBatch: null } },
    {
      name: "succeeded before settled stage",
      overrides: {
        state: "succeeded", stage: "constraints", created: 403, notSubmitted: 0,
        unknownRecords: 0, unknownBatch: null, finishedAt: "2026-10-01T08:06:00Z",
      },
    },
    {
      name: "unknown rows without unknown batch",
      overrides: { unknownRecords: 3, notSubmitted: 0, created: 400, unknownBatch: null },
    },
    { name: "wrong contract", overrides: { contract: "vibetable.source-import.v2" } },
    { name: "target without identity", overrides: { targets: [{ sourceTableId: "", tableId: "tbl_orders", name: "Orders" }] } },
    {
      name: "present but empty target collection",
      overrides: { targets: [{ sourceTableId: "src_orders", tableId: "tbl_orders", name: "Orders", collection: "" }] },
    },
    { name: "migrations not an array", raw: { migrations: "nope" } },
    { name: "unknown field policy", overrides: { fields: [{ source: {}, kind: "text", policy: "native-copy", definition: "" }] } },
    { name: "negative count", overrides: { created: -1, notSubmitted: 404 } },
  ])("reports a load failure for bad migrations: %s", async ({ overrides, raw }) => {
    const request = vi.fn(async () => ({
      items: [entry()],
      ...(raw ?? {}),
      ...(raw ? {} : { migrations: [migrationReceipt(overrides)] }),
    }));
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useImportManagementService();
    service.bindScope(() => ({ workspaceId: "ws-1", sessionEpoch: 1 }));

    await service.refresh();

    // A broken migrations field is a failed load, never an empty history.
    expect(service.error.value).toBeTruthy();
    expect(service.items.value).toBeNull();
    expect(service.sourceEntries.value).toBeNull();
    expect(service.loaded.value).toBe(false);
  });

  it("rejects a succeeded receipt that still hides unknown work", async () => {
    const request = vi.fn(async () => ({ items: [], migrations: [migrationReceipt({
      state: "succeeded", created: 400, notSubmitted: 0, unknownRecords: 3, total: 403,
      finishedAt: "2026-10-01T08:06:00Z", unknownBatch: null,
    })] }));
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useImportManagementService();
    service.bindScope(() => ({ workspaceId: "ws-1", sessionEpoch: 1 }));

    await service.refresh();

    expect(service.error.value).toBeTruthy();
    expect(service.sourceEntries.value).toBeNull();
  });

  it("drops a late migration reply whose workspace scope retired", async () => {
    let resolveHistory!: (value: { items: ImportHistoryEntry[]; migrations: unknown[] }) => void;
    const pending = new Promise<{ items: ImportHistoryEntry[]; migrations: unknown[] }>((resolve) => {
      resolveHistory = resolve;
    });
    const request = vi.fn(() => pending);
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useImportManagementService();
    let scope = { workspaceId: "ws-1", sessionEpoch: 1 };
    service.bindScope(() => scope);

    const loading = service.refresh();
    scope = { workspaceId: "ws-2", sessionEpoch: 2 };
    resolveHistory({ items: [entry()], migrations: [migrationReceipt()] });
    await loading;

    expect(service.items.value).toBeNull();
    expect(service.sourceEntries.value).toBeNull();
    expect(service.loaded.value).toBe(false);
  });

  it("retire drops source-migration projections too", async () => {
    const request = vi.fn(async () => ({ items: [entry()], migrations: [migrationReceipt({ state: "running", created: 0, notSubmitted: 403, unknownBatch: null })] }));
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useImportManagementService();
    service.bindScope(() => ({ workspaceId: "ws-1", sessionEpoch: 1 }));

    await service.refresh();
    expect(service.activeSourceEntries.value).toHaveLength(1);
    service.retire();

    expect(service.sourceEntries.value).toBeNull();
    expect(service.activeSourceEntries.value).toEqual([]);
  });
});
