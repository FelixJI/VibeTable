import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import {
  registerWorkspaceEpochReset,
  useWorkspaceSessionStore,
} from "./workspaceSessionStore";
import { useWorkspaceProtectionStore } from "./workspaceProtectionStore";
import { useUiStore } from "./uiStore";
import type { WorkspaceRegistryEntryV2, WorkspaceSessionV2 } from "@/contracts/workspaceV2";

const workspaceA: WorkspaceRegistryEntryV2 = {
  contractVersion: "2.0",
  workspaceId: "11111111-1111-4111-8111-111111111111",
  displayName: "项目 A",
  selectedRoot: "D:\\Workspaces\\A",
  activityRoot: null,
  storageKind: "fixed",
  coordinationStrength: "strong",
  lastOpenedAt: "2026-07-28T08:00:00Z",
  lastKnownHealth: "healthy",
  lastSnapshotAt: null,
  lastSyncAt: null,
  pendingSync: false,
};

function session(
  workspaceId: string,
  sessionEpoch: number,
): WorkspaceSessionV2 {
  return {
    contractVersion: "2.0",
    workspaceId,
    sessionEpoch,
    state: "openedWritable",
    openMode: "writable",
    writable: true,
    provisional: false,
    phase: "idle",
    errorCode: null,
  };
}

describe("workspaceSessionStore", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
  });

  it("fails closed until the host enables the v2 session capability", () => {
    const store = useWorkspaceSessionStore();
    expect(store.applySession(session(workspaceA.workspaceId, 1))).toBe(false);
    expect(store.enabled).toBe(false);

    store.configureCapabilities(["workspace.session.v2", "unknown.capability"]);
    expect(store.enabled).toBe(true);
    expect(store.capabilities).toEqual(["workspace.session.v2"]);
  });

  it("accepts the dedicated history restore capability", () => {
    const store = useWorkspaceSessionStore();
    store.configureCapabilities([
      "workspace.session.v2",
      "history.restore.v2",
    ]);
    expect(store.historyRestoreEnabled).toBe(true);
    expect(store.capabilities).toContain("history.restore.v2");
  });

  it("runs all epoch resetters before publishing the new session", () => {
    const store = useWorkspaceSessionStore();
    store.configureCapabilities(["workspace.session.v2"]);
    store.applySession(session(workspaceA.workspaceId, 3));
    const observed = vi.fn(() => store.activeWorkspaceId);
    const unregister = registerWorkspaceEpochReset("test-reset", observed);

    store.applySession(session("22222222-2222-4222-8222-222222222222", 4));

    expect(observed).toHaveBeenCalledOnce();
    expect(observed).toHaveReturnedWith(workspaceA.workspaceId);
    expect(store.activeWorkspaceId).toBe("22222222-2222-4222-8222-222222222222");
    unregister();
  });

  it("drops stale epochs, wrong workspaces, and non-monotonic sequences", () => {
    const store = useWorkspaceSessionStore();
    store.configureCapabilities(["workspace.session.v2"]);
    store.applySession(session(workspaceA.workspaceId, 7));

    expect(store.acceptEnvelope({
      scope: "workspace",
      workspaceId: workspaceA.workspaceId,
      sessionEpoch: 7,
      operationId: "33333333-3333-4333-8333-333333333333",
      sequence: 1,
    })).toBe(true);
    expect(store.acceptEnvelope({
      scope: "workspace",
      workspaceId: workspaceA.workspaceId,
      sessionEpoch: 7,
      operationId: "44444444-4444-4444-8444-444444444444",
      sequence: 1,
    })).toBe(false);
    expect(store.acceptEnvelope({
      scope: "workspace",
      workspaceId: workspaceA.workspaceId,
      sessionEpoch: 6,
      operationId: "55555555-5555-4555-8555-555555555555",
      sequence: 2,
    })).toBe(false);
  });

  it("clears protection projections when the epoch rotates", () => {
    const store = useWorkspaceSessionStore();
    const protection = useWorkspaceProtectionStore();
    store.configureCapabilities(["workspace.session.v2", "snapshot.timeline.v2"]);
    store.applySession(session(workspaceA.workspaceId, 1));
    protection.setSnapshots([{
      snapshotId: "77777777-7777-4777-8777-777777777777",
      createdAt: "2026-07-28T08:00:00Z",
      state: "ready",
      trigger: "manual",
      integrity: "verified",
      syncState: "localOnly",
      pinned: true,
      retentionReasons: ["manual"],
      logicalSize: 100,
      physicalSize: 80,
      note: null,
      catalogRevision: 1,
    }]);
    protection.setRetention({
      contractVersion: "2.0",
      policyRevision: 9,
      snapshotDays: 60,
      snapshotCount: 80,
      snapshotBuckets: ["hourly", "daily", "weekly", "monthly"],
      fileRevisionDays: 45,
      fileRevisionCount: 150,
      fileRevisionBuckets: ["daily", "weekly", "monthly"],
      trashMonths: 3,
      repositoryLimitBytes: null,
    });
    expect(protection.retentionHydrated).toBe(true);

    store.applySession(session(workspaceA.workspaceId, 2));
    expect(protection.snapshots).toEqual([]);
    expect(protection.retentionHydrated).toBe(false);
    expect(protection.retention).toBeNull();
  });

  it("keeps recent-table preferences inside the workspace namespace", () => {
    const ui = useUiStore();
    ui.setWorkspaceNamespace(workspaceA.workspaceId);
    ui.rememberTable("orders");
    ui.setWorkspaceNamespace("22222222-2222-4222-8222-222222222222");
    expect(ui.recentTables).toEqual([]);
    ui.rememberTable("invoices");
    ui.setWorkspaceNamespace(workspaceA.workspaceId);
    expect(ui.recentTables.map((item) => item.name)).toEqual(["orders"]);
  });
});
