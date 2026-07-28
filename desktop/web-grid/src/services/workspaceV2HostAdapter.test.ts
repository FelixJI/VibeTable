import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { HostBridge } from "@/bridge/hostBridge";
import {
  createWorkspaceV2HostAdapter,
} from "@/services/workspaceV2HostAdapter";
import { useWorkspaceProtectionStore } from "@/stores/workspaceProtectionStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import type {
  WorkspaceV2HostMessageType,
  WorkspaceV2RequestPayload,
} from "@/contracts/workspaceV2Bridge";

const WORKSPACE_ID = "11111111-1111-4111-8111-111111111111";
const SNAPSHOT_ID = "77777777-7777-4777-8777-777777777777";

function bootstrap() {
  return {
    contractVersion: "2.0",
    capabilities: [
      "workspace.session.v2",
      "workspace.storage.relocate.v2",
      "snapshot.timeline.v2",
      "snapshot.package.v2",
      "snapshot.open-as-new.v2",
      "fileHistory.tree.v2",
      "retention.policy.v2",
      "repository.settings.v2",
      "repository.key-rotation.v2",
      "conflict.center.v2",
    ],
    workspaces: [{
      contractVersion: "2.0",
      workspaceId: WORKSPACE_ID,
      displayName: "季度规划",
      selectedRoot: "D:\\Workspaces\\Quarter",
      activityRoot: null,
      storageKind: "fixed",
      coordinationStrength: "strong",
      lastOpenedAt: "2026-07-28T08:00:00Z",
      lastKnownHealth: "healthy",
      lastSnapshotAt: "2026-07-28T07:30:00Z",
      lastSyncAt: "2026-07-28T07:40:00Z",
      pendingSync: false,
    }],
    session: {
      contractVersion: "2.0",
      workspaceId: WORKSPACE_ID,
      sessionEpoch: 7,
      state: "openedWritable",
      openMode: "writable",
      writable: true,
      provisional: false,
      phase: "idle",
      errorCode: null,
    },
    snapshots: [{
      snapshotId: SNAPSHOT_ID,
      createdAt: "2026-07-28T08:00:00Z",
      state: "ready",
      trigger: "manual",
      integrity: "verified",
      syncState: "localOnly",
      pinned: true,
      retentionReasons: ["manual"],
      logicalSize: 4096,
      physicalSize: 2048,
      note: "发布前",
      catalogRevision: 3,
    }],
    storage: null,
    retention: {
      contractVersion: "2.0",
      policyRevision: 1,
      snapshotDays: 30,
      snapshotCount: 50,
      snapshotBuckets: ["hourly", "daily", "weekly", "monthly"],
      fileRevisionDays: 30,
      fileRevisionCount: 100,
      fileRevisionBuckets: ["daily", "weekly", "monthly"],
      trashMonths: 3,
      repositoryLimitBytes: null,
    },
    conflicts: [],
    fileTrees: [],
  } as const;
}

function fakeBridge() {
  const handlers = new Map<WorkspaceV2HostMessageType, (payload: unknown) => void>();
  const request = vi.fn(async (
    type: string,
    payload: WorkspaceV2RequestPayload,
  ): Promise<unknown> => {
    expect(type).toBe("workspace.v2.request");
    let result: unknown;
    if (payload.method === "snapshot.update") {
      result = {
        snapshotId: payload.params.snapshotId,
        state: "ready",
        integrity: "verified",
      };
    } else if (payload.method === "snapshot.previewRestore") {
      result = {
        planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        protectionRequired: true,
        changes: ["table:projects"],
      };
    } else if (payload.method === "workspace.list") {
      result = { workspaces: bootstrap().workspaces };
    } else if (payload.method === "snapshot.inspectPackage") {
      result = {
        planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        trusted: false,
        workspaceId: WORKSPACE_ID,
        sourceSnapshotId: null,
        snapshotCount: 2,
        encrypted: true,
        verified: false,
        expiresAt: "2026-07-28T10:10:00Z",
      };
    } else if (payload.method === "snapshot.openAsNewWorkspace") {
      result = {
        workspaceId: "99999999-9999-4999-8999-999999999999",
        sessionEpoch: 8,
        state: "openedWritable",
      };
    } else if (payload.method === "workspace.storage.apply") {
      result = {
        workspaceId: WORKSPACE_ID,
        status: "applied",
        storage: {
          location: "E:\\Workspaces\\Quarter",
          activityRoot: "E:\\Workspaces\\Quarter",
          mode: "direct",
          provider: "fixed",
          health: "healthy",
          logicalSize: 4096,
          physicalSize: 2048,
          reclaimableSize: 0,
          encryption: "convenient",
          keyVersion: 1,
          pendingSync: false,
          remoteVerified: true,
        },
      };
    } else {
      throw new Error(`unexpected method ${payload.method}`);
    }
    return {
      method: payload.method,
      wire: payload.wire,
      ok: true,
      result,
      error: null,
    };
  });
  const on = vi.fn((
    type: WorkspaceV2HostMessageType,
    handler: (payload: unknown) => void,
  ) => {
    handlers.set(type, handler);
    return () => handlers.delete(type);
  });
  return {
    handlers,
    request,
    bridge: { request, on } as unknown as HostBridge,
  };
}

describe("workspace v2 production host adapter", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("binds the closed host topics and strictly projects bootstrap state", () => {
    const fake = fakeBridge();
    const adapter = createWorkspaceV2HostAdapter(fake.bridge);

    expect([...fake.handlers.keys()].sort()).toEqual([
      "workspace.v2.bootstrap",
      "workspace.v2.event",
      "workspace.v2.reply",
    ]);
    fake.handlers.get("workspace.v2.bootstrap")!(bootstrap());

    const session = useWorkspaceSessionStore();
    const protection = useWorkspaceProtectionStore();
    expect(session.enabled).toBe(true);
    expect(session.activeWorkspaceId).toBe(WORKSPACE_ID);
    expect(session.workspaces[0]?.displayName).toBe("季度规划");
    expect(protection.snapshots[0]?.catalogRevision).toBe(3);

    expect(() => fake.handlers.get("workspace.v2.bootstrap")!({
      ...bootstrap(),
      unknown: true,
    })).toThrow(/unknown or missing fields/);
    adapter.dispose();
    expect(fake.handlers.size).toBe(0);
  });

  it("sends typed method/params/wire and projects preview plans", async () => {
    const fake = fakeBridge();
    const { port } = createWorkspaceV2HostAdapter(fake.bridge);
    fake.handlers.get("workspace.v2.bootstrap")!(bootstrap());

    await port.request({
      method: "snapshot.update",
      params: {
        snapshotId: SNAPSHOT_ID,
        action: "unpin",
        expectedCatalogRevision: 3,
      },
    });
    const firstPayload = fake.request.mock.calls[0]![1] as WorkspaceV2RequestPayload;
    expect(firstPayload).toMatchObject({
      method: "snapshot.update",
      params: {
        snapshotId: SNAPSHOT_ID,
        action: "unpin",
        expectedCatalogRevision: 3,
      },
      wire: {
        scope: "workspace",
        workspaceId: WORKSPACE_ID,
        sessionEpoch: 7,
        sequence: 1,
      },
    });
    expect(useWorkspaceProtectionStore().snapshots[0]).toMatchObject({
      pinned: false,
      catalogRevision: 4,
    });

    await port.request({
      method: "snapshot.previewRestore",
      params: { snapshotId: SNAPSHOT_ID, targetMode: "currentWorkspace" },
    });
    expect(useWorkspaceProtectionStore().restorePlan).toMatchObject({
      planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      protectionRequired: true,
    });
  });

  it("fails closed before sending methods whose production capability is absent", async () => {
    const fake = fakeBridge();
    const { port } = createWorkspaceV2HostAdapter(fake.bridge);
    fake.handlers.get("workspace.v2.bootstrap")!({
      ...bootstrap(),
      capabilities: ["workspace.session.v2"],
    });

    expect(useWorkspaceProtectionStore().retentionHydrated).toBe(false);
    await expect(port.request({
      method: "snapshot.list",
      params: { cursor: null, limit: 50 },
    })).rejects.toMatchObject({ code: "workspace.capability_unavailable" });
    expect(fake.request).not.toHaveBeenCalled();

    await port.request({ method: "workspace.list", params: {} });
    expect(fake.request).toHaveBeenCalledOnce();
  });

  it("keeps global package inspection available without an active workspace", async () => {
    const fake = fakeBridge();
    const { port } = createWorkspaceV2HostAdapter(fake.bridge);
    fake.handlers.get("workspace.v2.bootstrap")!({
      ...bootstrap(),
      capabilities: ["workspace.session.v2", "snapshot.package.v2"],
      session: {
        ...bootstrap().session,
        workspaceId: null,
        sessionEpoch: 0,
        state: "closed",
        openMode: "readOnly",
        writable: false,
      },
    });

    await port.request({
      method: "snapshot.inspectPackage",
      params: {
        pathGrant: "host-picker://snapshot-import",
        credential: null,
      },
    });
    const payload = fake.request.mock.calls[0]![1] as WorkspaceV2RequestPayload;
    expect(payload.wire).toMatchObject({ scope: "global", sequence: 1 });
    expect(useWorkspaceProtectionStore().snapshotPackagePlan?.encrypted).toBe(true);
  });

  it("rotates to the broker-created session and projects global relocation", async () => {
    const fake = fakeBridge();
    const { port } = createWorkspaceV2HostAdapter(fake.bridge);
    fake.handlers.get("workspace.v2.bootstrap")!(bootstrap());

    await port.request({
      method: "snapshot.openAsNewWorkspace",
      params: { snapshotId: SNAPSHOT_ID },
    });
    expect(useWorkspaceSessionStore()).toMatchObject({
      activeWorkspaceId: "99999999-9999-4999-8999-999999999999",
      sessionEpoch: 8,
    });
    const openPayload = fake.request.mock.calls[0]![1] as WorkspaceV2RequestPayload;
    expect(openPayload.wire).toMatchObject({
      scope: "workspace",
      workspaceId: WORKSPACE_ID,
      sessionEpoch: 7,
    });

    await port.request({
      method: "workspace.storage.apply",
      params: {
        planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        confirmation: "季度规划",
      },
    });
    const applyPayload = fake.request.mock.calls.at(-1)![1] as WorkspaceV2RequestPayload;
    expect(applyPayload.wire).toMatchObject({ scope: "global" });
    expect(useWorkspaceProtectionStore().storage?.location)
      .toBe("E:\\Workspaces\\Quarter");
  });

  it("rejects an event whose payload values violate its closed schema", () => {
    const fake = fakeBridge();
    createWorkspaceV2HostAdapter(fake.bridge);
    fake.handlers.get("workspace.v2.bootstrap")!(bootstrap());

    expect(() => fake.handlers.get("workspace.v2.event")!({
      contractVersion: "2.0",
      topic: "replica.changed",
      wire: {
        scope: "workspace",
        workspaceId: WORKSPACE_ID,
        sessionEpoch: 7,
        operationId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        sequence: 1,
      },
      payloadModel: "ReplicaChangedEvent",
      payloadSchema: {
        type: "object",
        additionalProperties: false,
        properties: {
          syncState: { type: "string" },
          pendingSync: { type: "boolean" },
        },
        required: ["syncState", "pendingSync"],
      },
      payload: { syncState: "replicated", pendingSync: "not-a-boolean" },
    })).toThrow(/invalid type/);
  });
});
