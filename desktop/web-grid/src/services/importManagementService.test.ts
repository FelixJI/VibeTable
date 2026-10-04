import { afterEach, describe, expect, it, vi } from "vitest";
import { BridgeOperationError, type HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useImportManagementService } from "./importManagementService";
import type { ImportHistoryEntry } from "@/contracts/importManagement";

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
});
