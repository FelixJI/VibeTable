import { createPinia, setActivePinia } from "pinia";
import { ref } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useDocumentWorkspaceStore } from "@/stores/documentWorkspaceStore";
import { useWorkspaceProtectionStore } from "@/stores/workspaceProtectionStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import type { WorkspaceRegistryEntryV2, WorkspaceSessionV2 } from "@/contracts/workspaceV2";
import { createWorkspaceSessionUiController } from "./workspaceSessionUiController";

describe("workspaceSessionUiController", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("workspace.open 失败时清理 operation 并把错误投射到 session", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    const protection = useWorkspaceProtectionStore();
    const showCenter = ref(false);
    const initializeConsumers = vi.fn();
    const controller = createWorkspaceSessionUiController({
      session,
      protection,
      documents: useDocumentWorkspaceStore(),
      showCenter,
      request: vi.fn(async () => { throw new Error("open failed"); }),
      errorMessage: error => error instanceof Error ? error.message : String(error),
      initializeConsumers,
    });

    await expect(controller.open("workspace-2")).resolves.toBe(false);

    expect(controller.activationPending.value).toBe(false);
    expect(protection.busyOperation).toBeNull();
    expect(protection.operationError).toBe("open failed");
    expect(session.errorCode).toBe("open failed");
    expect(showCenter.value).toBe(true);
    expect(initializeConsumers).not.toHaveBeenCalled();
  });

  it("workspace.open 成功后先清除 activation pending 再初始化业务消费者", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    const workspace: WorkspaceRegistryEntryV2 = {
      contractVersion: "2.0",
      workspaceId: "22222222-2222-4222-8222-222222222222",
      displayName: "Workspace 2",
      selectedRoot: "D:\\Workspaces\\Two",
      activityRoot: null,
      storageKind: "fixed",
      coordinationStrength: "strong",
      lastOpenedAt: null,
      lastKnownHealth: "healthy",
      lastSnapshotAt: null,
      lastSyncAt: null,
      pendingSync: false,
    };
    session.setWorkspaces([workspace]);
    const protection = useWorkspaceProtectionStore();
    const showCenter = ref(true);
    let pendingWhenInitialized: boolean | null = null;
    let controller!: ReturnType<typeof createWorkspaceSessionUiController>;
    controller = createWorkspaceSessionUiController({
      session,
      protection,
      documents: useDocumentWorkspaceStore(),
      showCenter,
      request: vi.fn(async () => {
        const opened: WorkspaceSessionV2 = {
          contractVersion: "2.0",
          workspaceId: workspace.workspaceId,
          sessionEpoch: 1,
          state: "openedWritable",
          openMode: "writable",
          writable: true,
          provisional: false,
          phase: "idle",
          errorCode: null,
        };
        session.applySession(opened);
      }),
      errorMessage: error => error instanceof Error ? error.message : String(error),
      initializeConsumers: () => {
        pendingWhenInitialized = controller.activationPending.value;
      },
    });

    await expect(controller.open(workspace.workspaceId)).resolves.toBe(true);

    expect(pendingWhenInitialized).toBe(false);
    expect(showCenter.value).toBe(false);
  });

  it("已有保护操作时拒绝切换且不污染 session 状态", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    const protection = useWorkspaceProtectionStore();
    const activeLease = protection.beginOperation("retention.get");
    const request = vi.fn();
    const controller = createWorkspaceSessionUiController({
      session,
      protection,
      documents: useDocumentWorkspaceStore(),
      showCenter: ref(false),
      request,
      errorMessage: error => String(error),
      initializeConsumers: vi.fn(),
    });

    await expect(controller.open("workspace-2")).resolves.toBe(false);

    expect(activeLease).not.toBeNull();
    expect(session.isTransitioning).toBe(false);
    expect(session.targetWorkspaceId).toBeNull();
    expect(protection.busyOperation).toBe("retention.get");
    expect(request).not.toHaveBeenCalled();
  });

  it("session 拒绝进入切换时释放刚取得的操作租约", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    vi.spyOn(session, "beginSwitch").mockReturnValue(false);
    const protection = useWorkspaceProtectionStore();
    const request = vi.fn();
    const controller = createWorkspaceSessionUiController({
      session,
      protection,
      documents: useDocumentWorkspaceStore(),
      showCenter: ref(false),
      request,
      errorMessage: error => String(error),
      initializeConsumers: vi.fn(),
    });

    await expect(controller.open("workspace-2")).resolves.toBe(false);

    expect(protection.busyOperation).toBeNull();
    expect(protection.operationError).toBeNull();
    expect(request).not.toHaveBeenCalled();
  });

  it("新的 epoch 使旧失败失效且不会清除新操作或破坏新 session", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    const protection = useWorkspaceProtectionStore();
    const workspaceId = "workspace-2";
    const openedSession: WorkspaceSessionV2 = {
      contractVersion: "2.0",
      workspaceId,
      sessionEpoch: 2,
      state: "openedWritable",
      openMode: "writable",
      writable: true,
      provisional: false,
      phase: "idle",
      errorCode: null,
    };
    let currentLease: ReturnType<typeof protection.beginOperation> = null;
    const controller = createWorkspaceSessionUiController({
      session,
      protection,
      documents: useDocumentWorkspaceStore(),
      showCenter: ref(false),
      request: vi.fn(async () => {
        session.applySession(openedSession);
        currentLease = protection.beginOperation("retention.get");
        throw new Error("stale open failure");
      }),
      errorMessage: error => error instanceof Error ? error.message : String(error),
      initializeConsumers: vi.fn(),
    });

    await expect(controller.open(workspaceId)).resolves.toBe(false);

    expect(currentLease).not.toBeNull();
    expect(protection.busyOperation).toBe("retention.get");
    expect(protection.operationError).toBeNull();
    expect(session.activeWorkspaceId).toBe(workspaceId);
    expect(session.errorCode).toBeNull();
    expect(session.isTransitioning).toBe(false);
  });
});
