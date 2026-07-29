import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { mount } from "@vue/test-utils";
import SettingsView from "./SettingsView.vue";
import WorkspaceCenter from "@/components/workspace/WorkspaceCenter.vue";
import WorkspaceProtectionSettings from "@/components/settings/WorkspaceProtectionSettings.vue";
import ConflictCenterView from "@/views/ConflictCenterView.vue";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import { useWorkspaceProtectionStore } from "@/stores/workspaceProtectionStore";

describe("workspace protection UI capability gates", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
  });

  it("shows Workspace Center cards with health, sync, and location context", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    session.setWorkspaces([{
      contractVersion: "2.0",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      displayName: "季度规划",
      selectedRoot: "D:\\Workspaces\\Quarter",
      activityRoot: null,
      storageKind: "network",
      coordinationStrength: "advisory",
      lastOpenedAt: "2026-07-28T08:00:00Z",
      lastKnownHealth: "degraded",
      lastSnapshotAt: "2026-07-28T07:30:00Z",
      lastSyncAt: "2026-07-28T07:40:00Z",
      pendingSync: true,
    }]);
    const wrapper = mount(WorkspaceCenter);

    expect(wrapper.text()).toContain("季度规划");
    expect(wrapper.text()).toContain("待同步");
    expect(wrapper.text()).toContain("建议性协调");
    await wrapper.get(
      '[data-testid="workspace-relink-11111111-1111-4111-8111-111111111111"]',
    ).trigger("click");
    expect(wrapper.emitted("action")?.[0]?.[0]).toEqual({
      method: "workspace.relink",
      params: {
        workspaceId: "11111111-1111-4111-8111-111111111111",
        selectedRootGrant: "host-picker://workspace-root",
      },
    });
    await wrapper.get('button[aria-label="打开工作区 季度规划"]').trigger("click");
    expect(wrapper.emitted("open")?.[0]?.[0]).toMatchObject({ displayName: "季度规划" });
  });

  it("collects storage and encryption choices before emitting create", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    const wrapper = mount(WorkspaceCenter, { attachTo: document.body });

    await wrapper.get('[data-testid="workspace-create"]').trigger("click");
    expect(document.body.textContent).toContain("固定公开口令：password");
    expect(document.body.textContent).toContain("程序管理的默认位置");
    expect(document.body.textContent).toContain("不使用程序安装目录");
    expect(document.body.textContent).toContain("程序管理的默认位置使用直接模式");
    expect(document.body.querySelector<HTMLInputElement>(
      'input[value="mirrored"]',
    )?.disabled).toBe(true);
    const input = document.body.querySelector<HTMLInputElement>(".workspace-flow-modal input");
    expect(input).not.toBeNull();
    input!.value = "设计档案";
    input!.dispatchEvent(new Event("input", { bubbles: true }));
    await wrapper.vm.$nextTick();
    const confirm = document.body.querySelector<HTMLButtonElement>('[data-testid="workspace-flow-confirm"]');
    confirm!.click();
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted("action")?.[0]?.[0]).toMatchObject({
      method: "workspace.create",
      params: {
        displayName: "设计档案",
        locationPolicy: "managedDefault",
        selectedRootGrant: null,
        storageMode: "direct",
        encryptionMode: "convenient",
        userMarkedSync: false,
      },
    });
    wrapper.unmount();
  });

  it("offers an explicit other-location picker for workspace creation", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    const wrapper = mount(WorkspaceCenter, { attachTo: document.body });

    await wrapper.get('[data-testid="workspace-create"]').trigger("click");
    const name = document.body.querySelector<HTMLInputElement>(".workspace-flow-modal input");
    name!.value = "自选位置";
    name!.dispatchEvent(new Event("input", { bubbles: true }));
    document.body.querySelector<HTMLInputElement>(
      '[data-testid="workspace-location-policy"] input[value="other"]',
    )!.click();
    await wrapper.vm.$nextTick();
    document.body.querySelector<HTMLButtonElement>(
      '[data-testid="workspace-flow-confirm"]',
    )!.click();
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted("action")?.[0]?.[0]).toEqual({
      method: "workspace.create",
      params: {
        displayName: "自选位置",
        locationPolicy: "other",
        selectedRootGrant: "host-picker://workspace-root",
        storageMode: "direct",
        encryptionMode: "convenient",
        userMarkedSync: false,
      },
    });
    wrapper.unmount();
  });

  it("marks an other location as sync-managed only after explicit consent", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    const wrapper = mount(WorkspaceCenter, { attachTo: document.body });

    await wrapper.get('[data-testid="workspace-create"]').trigger("click");
    const name = document.body.querySelector<HTMLInputElement>(
      '.workspace-flow-modal input[type="text"]',
    )!;
    name.value = "同步目录";
    name.dispatchEvent(new Event("input", { bubbles: true }));
    document.body.querySelector<HTMLInputElement>(
      '[data-testid="workspace-location-policy"] input[value="other"]',
    )!.click();
    await wrapper.vm.$nextTick();
    expect(document.body.textContent).toContain("此目录由同步软件管理");
    document.body.querySelector<HTMLElement>(
      '[data-testid="workspace-user-marked-sync"]',
    )!.click();
    document.body.querySelector<HTMLButtonElement>(
      '[data-testid="workspace-flow-confirm"]',
    )!.click();
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted("action")?.[0]?.[0]).toMatchObject({
      method: "workspace.create",
      params: {
        locationPolicy: "other",
        userMarkedSync: true,
      },
    });
    wrapper.unmount();
  });

  it("clears manual sync consent after returning through managed default", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    const wrapper = mount(WorkspaceCenter, { attachTo: document.body });

    await wrapper.get('[data-testid="workspace-create"]').trigger("click");
    const name = document.body.querySelector<HTMLInputElement>(
      '.workspace-flow-modal input[type="text"]',
    )!;
    name.value = "重新选择";
    name.dispatchEvent(new Event("input", { bubbles: true }));
    const location = document.body.querySelector(
      '[data-testid="workspace-location-policy"]',
    )!;
    location.querySelector<HTMLInputElement>('input[value="other"]')!.click();
    await wrapper.vm.$nextTick();
    document.body.querySelector<HTMLElement>(
      '[data-testid="workspace-user-marked-sync"]',
    )!.click();
    location.querySelector<HTMLInputElement>(
      'input[value="managedDefault"]',
    )!.click();
    await wrapper.vm.$nextTick();
    location.querySelector<HTMLInputElement>('input[value="other"]')!.click();
    await wrapper.vm.$nextTick();
    document.body.querySelector<HTMLButtonElement>(
      '[data-testid="workspace-flow-confirm"]',
    )!.click();
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted("action")?.[0]?.[0]).toMatchObject({
      method: "workspace.create",
      params: {
        locationPolicy: "other",
        userMarkedSync: false,
      },
    });
    wrapper.unmount();
  });

  it("forces direct mode when creation returns to the managed default", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities([
      "workspace.session.v2",
      "workspace.storage.mirrored-create.v2",
    ]);
    const wrapper = mount(WorkspaceCenter, { attachTo: document.body });

    await wrapper.get('[data-testid="workspace-create"]').trigger("click");
    const locationGroup = document.body.querySelector(
      '[data-testid="workspace-location-policy"]',
    )!;
    const other = locationGroup.querySelector<HTMLInputElement>('input[value="other"]')!;
    const managed = locationGroup.querySelector<HTMLInputElement>(
      'input[value="managedDefault"]',
    )!;
    const storageInput = (value: "direct" | "mirrored") =>
      document.body.querySelector<HTMLInputElement>(`input[value="${value}"]`)!;

    expect(storageInput("mirrored").disabled).toBe(true);
    expect(document.body.textContent).toContain("程序管理的默认位置使用直接模式");
    other.click();
    await wrapper.vm.$nextTick();
    expect(storageInput("mirrored").disabled).toBe(false);
    storageInput("mirrored").click();
    await wrapper.vm.$nextTick();
    expect(storageInput("mirrored").checked).toBe(true);

    managed.click();
    await wrapper.vm.$nextTick();
    expect(storageInput("mirrored").disabled).toBe(true);
    expect(storageInput("direct").checked).toBe(true);

    const name = document.body.querySelector<HTMLInputElement>(
      '.workspace-flow-modal input[type="text"]',
    )!;
    name.value = "受管直连";
    name.dispatchEvent(new Event("input", { bubbles: true }));
    await wrapper.vm.$nextTick();
    const confirm = document.body.querySelector<HTMLButtonElement>(
      '[data-testid="workspace-flow-confirm"]',
    )!;
    expect(confirm.disabled).toBe(false);
    confirm.click();
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted("action")?.[0]?.[0]).toMatchObject({
      method: "workspace.create",
      params: {
        locationPolicy: "managedDefault",
        selectedRootGrant: null,
        storageMode: "direct",
        userMarkedSync: false,
      },
    });
    wrapper.unmount();
  });

  it("imports a recovery package from Workspace Center with no open workspace", async () => {
    const session = useWorkspaceSessionStore();
    const protection = useWorkspaceProtectionStore();
    session.configureCapabilities([
      "workspace.session.v2",
      "snapshot.package.v2",
    ]);
    const wrapper = mount(WorkspaceCenter, { attachTo: document.body });

    await wrapper.get('[data-testid="workspace-import-package-empty"]').trigger("click");
    expect(wrapper.emitted("action")?.[0]?.[0]).toEqual({
      method: "snapshot.inspectPackage",
      params: {
        pathGrant: "host-picker://snapshot-import",
        credential: null,
      },
    });
    protection.setSnapshotPackagePlan({
      planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      trusted: false,
      workspaceId: "11111111-1111-4111-8111-111111111111",
      sourceSnapshotId: "22222222-2222-4222-8222-222222222222",
      snapshotCount: 3,
      encrypted: false,
      verified: true,
      expiresAt: "2026-07-28T10:10:00Z",
    });
    await wrapper.vm.$nextTick();
    document.body.querySelector<HTMLButtonElement>(
      '[data-testid="workspace-import-apply"]',
    )!.click();
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("action")?.[1]?.[0]).toEqual({
      method: "snapshot.import",
      params: {
        planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        credential: null,
        targetMode: "newWorkspace",
        targetWorkspaceId: null,
      },
    });
    wrapper.unmount();
  });

  it("uses planDelete then applyDelete with typed-name confirmation", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    session.setWorkspaces([{
      contractVersion: "2.0",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      displayName: "季度规划",
      selectedRoot: "D:\\Workspaces\\Quarter",
      activityRoot: null,
      storageKind: "fixed",
      coordinationStrength: "strong",
      lastOpenedAt: null,
      lastKnownHealth: "healthy",
      lastSnapshotAt: null,
      lastSyncAt: null,
      pendingSync: false,
    }]);
    const wrapper = mount(WorkspaceCenter, { attachTo: document.body });

    await wrapper.get(
      '[data-testid="workspace-delete-11111111-1111-4111-8111-111111111111"]',
    ).trigger("click");
    expect(wrapper.emitted("action")?.[0]?.[0]).toEqual({
      method: "workspace.planDelete",
      params: { workspaceId: "11111111-1111-4111-8111-111111111111" },
    });

    session.setDeletePlan({
      workspaceId: "11111111-1111-4111-8111-111111111111",
      planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      displayName: "季度规划",
      requiresTypedName: true,
    });
    await wrapper.vm.$nextTick();
    const input = document.body.querySelector<HTMLInputElement>(
      '.workspace-flow-modal input[type="text"]',
    )!;
    input.value = "季度规划";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await wrapper.vm.$nextTick();
    document.body.querySelector<HTMLButtonElement>(
      '[data-testid="workspace-delete-apply"]',
    )!.click();
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("action")?.[1]?.[0]).toEqual({
      method: "workspace.applyDelete",
      params: {
        planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        confirmation: "季度规划",
      },
    });
    wrapper.unmount();
  });

  it("renders the snapshot timeline as the only versions surface", async () => {
    const session = useWorkspaceSessionStore();
    const protection = useWorkspaceProtectionStore();
    session.configureCapabilities([
      "workspace.session.v2",
      "snapshot.timeline.v2",
      "snapshot.package.v2",
    ]);
    protection.setSnapshots([{
      snapshotId: "77777777-7777-4777-8777-777777777777",
      createdAt: "2026-07-28T08:00:00Z",
      state: "ready",
      trigger: "manual",
      integrity: "verified",
      syncState: "replicated",
      pinned: true,
      retentionReasons: ["manual"],
      logicalSize: 4096,
      physicalSize: 2048,
      note: "发布前",
      catalogRevision: 1,
    }]);
    const wrapper = mount(SettingsView);

    await wrapper.get('[data-testid="settings-nav-versions"]').trigger("click");
    expect(wrapper.find('[data-testid="snapshot-settings"]').exists()).toBe(true);
    expect(wrapper.text()).toContain("发布前");
    await wrapper.get('[data-testid="snapshot-create"]').trigger("click");
    expect(wrapper.emitted("workspaceV2Action")?.[0]?.[0]).toMatchObject({
      method: "snapshot.request",
    });
  });

  it("keeps restore open for preview and applies only the returned plan id", async () => {
    const protection = useWorkspaceProtectionStore();
    protection.setSnapshots([{
      snapshotId: "77777777-7777-4777-8777-777777777777",
      createdAt: "2026-07-28T08:00:00Z",
      state: "ready",
      trigger: "manual",
      integrity: "verified",
      syncState: "replicated",
      pinned: true,
      retentionReasons: ["manual"],
      logicalSize: 4096,
      physicalSize: 2048,
      note: null,
      catalogRevision: 4,
    }]);
    protection.selectSnapshot("77777777-7777-4777-8777-777777777777");
    const wrapper = mount(WorkspaceProtectionSettings, {
      props: { mode: "versions" },
      attachTo: document.body,
    });

    await wrapper.get('[data-testid="snapshot-restore-open"]').trigger("click");
    const advance = document.body.querySelector<HTMLButtonElement>(
      '[data-testid="snapshot-restore-preview"]',
    )!;
    advance.click();
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("action")?.[0]?.[0]).toMatchObject({
      method: "snapshot.previewRestore",
      params: { targetMode: "currentWorkspace" },
    });

    protection.setRestorePlan({
      planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      protectionRequired: true,
      changes: ["table:projects"],
    });
    await wrapper.vm.$nextTick();
    advance.click();
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("action")?.[1]?.[0]).toEqual({
      method: "snapshot.applyRestore",
      params: { planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", confirmed: true },
    });
    wrapper.unmount();
  });

  it("opens a selected snapshot through the dedicated new-workspace broker", async () => {
    useWorkspaceSessionStore().configureCapabilities([
      "workspace.session.v2",
      "snapshot.timeline.v2",
      "snapshot.open-as-new.v2",
    ]);
    const protection = useWorkspaceProtectionStore();
    protection.setSnapshots([{
      snapshotId: "77777777-7777-4777-8777-777777777777",
      createdAt: "2026-07-28T08:00:00Z",
      state: "ready",
      trigger: "manual",
      integrity: "verified",
      syncState: "localOnly",
      pinned: true,
      retentionReasons: ["manual"],
      logicalSize: 4096,
      physicalSize: 2048,
      note: null,
      catalogRevision: 4,
    }]);
    protection.selectSnapshot("77777777-7777-4777-8777-777777777777");
    const wrapper = mount(WorkspaceProtectionSettings, {
      props: { mode: "versions" },
    });

    await wrapper.get('[data-testid="snapshot-open-as-new"]').trigger("click");
    expect(wrapper.emitted("action")?.[0]?.[0]).toEqual({
      method: "snapshot.openAsNewWorkspace",
      params: { snapshotId: "77777777-7777-4777-8777-777777777777" },
    });
  });

  it("inspects a snapshot package before importing it as a new workspace", async () => {
    useWorkspaceSessionStore().configureCapabilities([
      "workspace.session.v2",
      "snapshot.package.v2",
    ]);
    const protection = useWorkspaceProtectionStore();
    const wrapper = mount(WorkspaceProtectionSettings, {
      props: { mode: "versions" },
      attachTo: document.body,
    });
    await wrapper.get('[data-testid="snapshot-import"]').trigger("click");
    expect(wrapper.emitted("action")?.[0]?.[0]).toEqual({
      method: "snapshot.inspectPackage",
      params: {
        pathGrant: "host-picker://snapshot-import",
        credential: null,
      },
    });

    protection.setSnapshotPackagePlan({
      planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      trusted: true,
      workspaceId: "11111111-1111-4111-8111-111111111111",
      sourceSnapshotId: "22222222-2222-4222-8222-222222222222",
      snapshotCount: 2,
      encrypted: false,
      verified: true,
      expiresAt: "2026-07-28T10:10:00Z",
    });
    await wrapper.vm.$nextTick();
    document.body.querySelector<HTMLButtonElement>(
      '[data-testid="snapshot-import-apply"]',
    )!.click();
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("action")?.[1]?.[0]).toEqual({
      method: "snapshot.import",
      params: {
        planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        credential: null,
        targetMode: "newWorkspace",
        targetWorkspaceId: null,
      },
    });
    wrapper.unmount();
  });

  it("requires credentials for encrypted imports and sends the staged plan only", async () => {
    useWorkspaceSessionStore().configureCapabilities([
      "workspace.session.v2",
      "snapshot.package.v2",
    ]);
    const protection = useWorkspaceProtectionStore();
    const wrapper = mount(WorkspaceProtectionSettings, {
      props: { mode: "versions" },
      attachTo: document.body,
    });
    protection.setSnapshotPackagePlan({
      planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      trusted: false,
      workspaceId: "11111111-1111-4111-8111-111111111111",
      sourceSnapshotId: null,
      snapshotCount: 2,
      encrypted: true,
      verified: false,
      expiresAt: "2026-07-28T10:10:00Z",
    });
    await wrapper.vm.$nextTick();

    const apply = document.body.querySelector<HTMLButtonElement>(
      '[data-testid="snapshot-import-apply"]',
    )!;
    expect(apply.disabled).toBe(true);
    const credential = document.body.querySelector<HTMLInputElement>(
      '[data-testid="snapshot-import-credential"] input',
    )!;
    credential.value = "correct horse battery staple";
    credential.dispatchEvent(new Event("input", { bubbles: true }));
    await wrapper.vm.$nextTick();
    expect(apply.disabled).toBe(false);
    apply.click();
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted("action")?.[0]?.[0]).toEqual({
      method: "snapshot.import",
      params: {
        planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        credential: "correct horse battery staple",
        targetMode: "newWorkspace",
        targetWorkspaceId: null,
      },
    });
    wrapper.unmount();
  });

  it("exports recovery packages to AGE recipients without exposing a passphrase", async () => {
    const protection = useWorkspaceProtectionStore();
    protection.setSnapshots([{
      snapshotId: "77777777-7777-4777-8777-777777777777",
      createdAt: "2026-07-28T10:00:00Z",
      state: "ready",
      trigger: "manual",
      integrity: "verified",
      syncState: "replicated",
      pinned: true,
      retentionReasons: ["manual"],
      logicalSize: 4096,
      physicalSize: 2048,
      note: null,
      catalogRevision: 4,
    }]);
    protection.selectSnapshot("77777777-7777-4777-8777-777777777777");
    const wrapper = mount(WorkspaceProtectionSettings, {
      props: { mode: "versions" },
      attachTo: document.body,
    });
    await wrapper.get('[data-testid="snapshot-export-open"]').trigger("click");
    const radios = [...document.body.querySelectorAll<HTMLButtonElement>('[role="radio"]')];
    radios[1]!.click();
    await wrapper.vm.$nextTick();
    const recipient = document.body.querySelector<HTMLTextAreaElement>(
      '[data-testid="snapshot-export-recipient"] textarea',
    )!;
    recipient.value = "age1example";
    recipient.dispatchEvent(new Event("input", { bubbles: true }));
    document.body.querySelector<HTMLButtonElement>('[data-testid="snapshot-export-apply"]')!.click();
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted("action")?.[0]?.[0]).toEqual({
      method: "snapshot.export",
      params: {
        snapshotId: "77777777-7777-4777-8777-777777777777",
        pathGrant: "host-picker://snapshot-export",
        encryption: "age",
        recipients: ["age1example"],
        credential: null,
      },
    });
    wrapper.unmount();
  });

  it("previews and applies a read-only single-file extraction through a host grant", async () => {
    const protection = useWorkspaceProtectionStore();
    protection.setDocuments([{
      contractVersion: "2.0",
      documentId: "22222222-2222-4222-8222-222222222222",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      relativePath: "归档/季度规划.docx",
      status: "active",
      effectiveRevisionId: "33333333-3333-4333-8333-333333333333",
      nextRevisionOrdinal: 4,
      nextFormalVersion: 4,
    }]);
    protection.setSnapshots([{
      snapshotId: "77777777-7777-4777-8777-777777777777",
      createdAt: "2026-07-28T10:00:00Z",
      state: "ready",
      trigger: "manual",
      integrity: "verified",
      syncState: "localOnly",
      pinned: false,
      retentionReasons: [],
      logicalSize: 4096,
      physicalSize: 2048,
      note: null,
      catalogRevision: 4,
    }]);
    protection.selectSnapshot("77777777-7777-4777-8777-777777777777");
    const wrapper = mount(WorkspaceProtectionSettings, {
      props: { mode: "versions" },
      attachTo: document.body,
    });

    await wrapper.get('[data-testid="snapshot-extract-open"]').trigger("click");
    document.body.querySelector<HTMLButtonElement>(
      '[data-testid="snapshot-extract-advance"]',
    )!.click();
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("action")?.[0]?.[0]).toEqual({
      method: "snapshot.previewExtract",
      params: {
        snapshotId: "77777777-7777-4777-8777-777777777777",
        documentId: "22222222-2222-4222-8222-222222222222",
      },
    });

    protection.setExtractPlan({
      planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      displayName: "季度规划.docx",
      size: 1024,
      expiresAt: "2026-07-28T10:10:00Z",
    });
    await wrapper.vm.$nextTick();
    document.body.querySelector<HTMLButtonElement>(
      '[data-testid="snapshot-extract-advance"]',
    )!.click();
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("action")?.[1]?.[0]).toEqual({
      method: "snapshot.applyExtract",
      params: {
        planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        pathGrant: "host-picker://snapshot-extract",
      },
    });
    wrapper.unmount();
  });

  it("shows repository verification evidence on the storage card", async () => {
    useWorkspaceSessionStore().configureCapabilities([
      "workspace.session.v2",
      "snapshot.timeline.v2",
    ]);
    const protection = useWorkspaceProtectionStore();
    protection.setStorage({
      location: "D:\\Workspaces\\Quarter",
      activityRoot: "D:\\Workspaces\\Quarter",
      mode: "direct",
      provider: "fixed",
      health: "healthy",
      logicalSize: 4096,
      physicalSize: 2048,
      reclaimableSize: 512,
      encryption: "convenient",
      keyVersion: 1,
      pendingSync: false,
      remoteVerified: true,
    });
    const wrapper = mount(WorkspaceProtectionSettings, {
      props: { mode: "storage" },
    });
    expect(wrapper.text()).toContain("固定公开口令：password");
    await wrapper.get('[data-testid="repository-verify"]').trigger("click");
    expect(wrapper.emitted("action")?.[0]?.[0]).toEqual({
      method: "repository.verify",
      params: {},
    });
    protection.setRepositoryVerification({
      state: "verified",
      snapshotCount: 3,
      objectCount: 12,
      corruptSnapshotIds: [],
    });
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain("仓库完整");
  });

  it("previews protected key rotation and applies only the host restart plan", async () => {
    useWorkspaceSessionStore().configureCapabilities([
      "workspace.session.v2",
      "snapshot.timeline.v2",
      "repository.settings.v2",
      "repository.key-rotation.v2",
    ]);
    const protection = useWorkspaceProtectionStore();
    protection.setStorage({
      location: "D:\\Workspaces\\Quarter",
      activityRoot: "D:\\Workspaces\\Quarter",
      mode: "direct",
      provider: "fixed",
      health: "healthy",
      logicalSize: 4096,
      physicalSize: 2048,
      reclaimableSize: 512,
      encryption: "protected",
      keyVersion: 3,
      pendingSync: false,
      remoteVerified: true,
    });
    const wrapper = mount(WorkspaceProtectionSettings, {
      props: { mode: "storage" },
      attachTo: document.body,
    });

    await wrapper.get('[data-testid="repository-key-rotation-preview"]').trigger("click");
    expect(wrapper.emitted("action")?.[0]?.[0]).toEqual({
      method: "repository.previewKeyRotation",
      params: {},
    });

    protection.setKeyRotationPlan({
      planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      expiresAt: "2026-07-28T10:10:00Z",
      protectionRequired: true,
    });
    await wrapper.vm.$nextTick();
    document.body.querySelector<HTMLButtonElement>(
      '[data-testid="repository-key-rotation-apply"]',
    )!.click();
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("action")?.[1]?.[0]).toEqual({
      method: "repository.applyKeyRotation",
      params: {
        planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        confirmed: true,
      },
    });
    wrapper.unmount();
  });

  it("relocates fixed direct storage through a typed-name durable plan", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities([
      "workspace.session.v2",
      "workspace.storage.relocate.v2",
      "repository.settings.v2",
    ]);
    session.setWorkspaces([{
      contractVersion: "2.0",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      displayName: "季度规划",
      selectedRoot: "D:\\Workspaces\\Quarter",
      activityRoot: null,
      storageKind: "fixed",
      coordinationStrength: "strong",
      lastOpenedAt: "2026-07-28T08:00:00Z",
      lastKnownHealth: "healthy",
      lastSnapshotAt: "2026-07-28T07:30:00Z",
      lastSyncAt: null,
      pendingSync: false,
    }]);
    session.applySession({
      contractVersion: "2.0",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      sessionEpoch: 7,
      state: "openedWritable",
      openMode: "writable",
      writable: true,
      provisional: false,
      phase: "idle",
      errorCode: null,
    });
    const protection = useWorkspaceProtectionStore();
    protection.setStorage({
      location: "D:\\Workspaces\\Quarter",
      activityRoot: "D:\\Workspaces\\Quarter",
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
    });
    const wrapper = mount(WorkspaceProtectionSettings, {
      props: { mode: "storage" },
      attachTo: document.body,
    });

    await wrapper.get('[data-testid="workspace-storage-relocate-preview"]').trigger("click");
    expect(wrapper.emitted("action")?.[0]?.[0]).toEqual({
      method: "workspace.storage.preview",
      params: {
        workspaceId: "11111111-1111-4111-8111-111111111111",
        action: "relocate",
        targetMode: "direct",
        selectedRootGrant: "host-picker://workspace-root",
      },
    });

    protection.setStoragePlan({
      planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      action: "relocate",
      source: {
        selectedRoot: "D:\\Workspaces\\Quarter",
        activityRoot: null,
        mode: "direct",
      },
      target: {
        selectedRoot: "E:\\Workspaces\\Quarter",
        activityRoot: null,
        mode: "direct",
      },
      bytesToCopy: 4096,
      requiresClosedSession: true,
      warnings: ["The verified source copy is retained after relocation."],
      expiresAt: "2026-07-28T10:10:00Z",
      verificationReceiptId: null,
    });
    await wrapper.vm.$nextTick();
    const input = document.body.querySelector<HTMLInputElement>(
      '[data-testid="workspace-storage-confirmation"] input',
    )!;
    input.value = "季度规划";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await wrapper.vm.$nextTick();
    document.body.querySelector<HTMLButtonElement>(
      '[data-testid="workspace-storage-relocate-apply"]',
    )!.click();
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("action")?.[1]?.[0]).toEqual({
      method: "workspace.storage.apply",
      params: {
        planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        confirmation: "季度规划",
      },
    });
    wrapper.unmount();
  });

  it("only exposes verified topology/cache actions and emits their closed params", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities([
      "workspace.session.v2",
      "workspace.storage.topology.v2",
      "workspace.storage.release-cache.v2",
      "repository.settings.v2",
    ]);
    session.setWorkspaces([{
      contractVersion: "2.0",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      displayName: "季度规划",
      selectedRoot: "E:\\Replica\\Quarter",
      activityRoot: "C:\\VibeTable\\Activity\\Quarter",
      storageKind: "fixed",
      coordinationStrength: "strong",
      lastOpenedAt: "2026-07-28T08:00:00Z",
      lastKnownHealth: "healthy",
      lastSnapshotAt: "2026-07-28T07:30:00Z",
      lastSyncAt: "2026-07-28T07:35:00Z",
      pendingSync: false,
    }]);
    session.applySession({
      contractVersion: "2.0",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      sessionEpoch: 7,
      state: "openedWritable",
      openMode: "writable",
      writable: true,
      provisional: false,
      phase: "idle",
      errorCode: null,
    });
    const protection = useWorkspaceProtectionStore();
    protection.setStorage({
      location: "E:\\Replica\\Quarter",
      activityRoot: "C:\\VibeTable\\Activity\\Quarter",
      mode: "mirrored",
      provider: "fixed",
      health: "healthy",
      logicalSize: 4096,
      physicalSize: 2048,
      reclaimableSize: 0,
      encryption: "protected",
      keyVersion: 2,
      pendingSync: false,
      remoteVerified: true,
    });
    const wrapper = mount(WorkspaceProtectionSettings, {
      props: { mode: "storage" },
      attachTo: document.body,
    });

    await wrapper.get('[data-testid="workspace-storage-convert-preview"]').trigger("click");
    await wrapper.get('[data-testid="workspace-storage-release-cache-preview"]').trigger("click");
    expect(wrapper.emitted("action")?.map((event) => event[0])).toEqual([
      {
        method: "workspace.storage.preview",
        params: {
          workspaceId: "11111111-1111-4111-8111-111111111111",
          action: "convertTopology",
          targetMode: "direct",
          selectedRootGrant: "host-picker://workspace-root",
        },
      },
      {
        method: "workspace.storage.preview",
        params: {
          workspaceId: "11111111-1111-4111-8111-111111111111",
          action: "releaseActivityCache",
          targetMode: null,
          selectedRootGrant: null,
        },
      },
    ]);
    wrapper.unmount();
  });

  it("applies retention and conflict plans by planId", async () => {
    const protection = useWorkspaceProtectionStore();
    protection.setRetentionPlan({
      planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      reclaimableBytes: 2048,
      blockedReasons: [],
    });
    const settings = mount(WorkspaceProtectionSettings, { props: { mode: "storage" } });
    expect(settings.get('[data-testid="retention-plan-preview"]').attributes("disabled")).toBeDefined();
    expect(settings.get('[data-testid="retention-plan-apply"]').attributes("disabled")).toBeDefined();
    expect(settings.get('[data-testid="retention-save"]').attributes("disabled")).toBeDefined();

    protection.setRetention({
      contractVersion: "2.0",
      policyRevision: 7,
      snapshotDays: 60,
      snapshotCount: 80,
      snapshotBuckets: ["hourly", "daily", "weekly", "monthly"],
      fileRevisionDays: 45,
      fileRevisionCount: 150,
      fileRevisionBuckets: ["daily", "weekly", "monthly"],
      trashMonths: 3,
      repositoryLimitBytes: null,
    });
    await settings.vm.$nextTick();
    expect(settings.text()).toContain("保存");
    expect(settings.text()).not.toContain("common.save");
    expect(settings.get('[data-testid="retention-plan-preview"]').attributes("disabled")).toBeUndefined();
    expect(settings.get('[data-testid="retention-plan-apply"]').attributes("disabled")).toBeUndefined();
    expect(settings.get('[data-testid="retention-save"]').attributes("disabled")).toBeDefined();
    const limitInput = settings.get('[data-testid="retention-repository-limit"] input');
    await limitInput.setValue("2.5");
    await limitInput.trigger("change");
    await limitInput.trigger("blur");
    await settings.vm.$nextTick();
    await settings.get('[data-testid="retention-save"]').trigger("click");
    expect(settings.emitted("action")?.[0]?.[0]).toEqual({
      method: "retention.update",
      params: {
        expectedRevision: 7,
        snapshotDays: 60,
        snapshotCount: 80,
        snapshotBuckets: ["hourly", "daily", "weekly", "monthly"],
        fileRevisionDays: 45,
        fileRevisionCount: 150,
        fileRevisionBuckets: ["daily", "weekly", "monthly"],
        repositoryLimitBytes: 2.5 * 1024 ** 3,
      },
    });
    await settings.get('[data-testid="retention-plan-apply"]').trigger("click");
    expect(settings.emitted("action")?.[1]?.[0]).toEqual({
      method: "retention.apply",
      params: { planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" },
    });

    const conflictId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
    const tableItemId = "dddddddd-dddd-4ddd-8ddd-dddddddddddd";
    const fileItemId = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee";
    const settingsItemId = "workspace-settings";
    protection.setConflictSets([{
      conflictId,
      state: "pending",
      createdAt: "2026-07-28T09:00:00Z",
      itemCount: 3,
    }]);
    protection.setConflicts([
      {
        conflictId,
        itemId: tableItemId,
        path: "Projects",
        kind: "table",
        state: "pending",
        localSummary: "Local table",
        replicaSummary: "Replica table",
        baseSummary: "Base table",
        dependencies: ["relation:Customers", "automation:Notify", "plugin:Calendar"],
        selected: "local",
      },
      {
        conflictId,
        itemId: fileItemId,
        path: "files/brief.docx",
        kind: "file",
        state: "pending",
        localSummary: "Local file",
        replicaSummary: "Replica file",
        baseSummary: "Base file",
        dependencies: [],
        selected: null,
      },
      {
        conflictId,
        itemId: settingsItemId,
        path: "workspace settings",
        kind: "settings",
        state: "pending",
        localSummary: "Local settings",
        replicaSummary: "Replica settings",
        baseSummary: "Base settings",
        dependencies: [],
        selected: "replica",
      },
    ]);
    const conflicts = mount(ConflictCenterView);
    expect(conflicts.get('[data-testid="conflict-preview"]').attributes("disabled"))
      .toBeDefined();
    protection.chooseConflict(conflictId, fileItemId, "replica");
    await conflicts.vm.$nextTick();
    await conflicts.get('[data-testid="conflict-preview"]').trigger("click");
    expect(conflicts.emitted("action")?.[0]?.[0]).toMatchObject({
      method: "conflict.preview",
      params: {
        conflictId,
        choices: [
          { itemId: tableItemId, kind: "table", side: "local" },
          { itemId: fileItemId, kind: "file", side: "replica" },
          { itemId: settingsItemId, kind: "settings", side: "replica" },
        ],
      },
    });
    protection.setConflictPlan("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", {
      planId: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
      diagnostics: [],
      valid: true,
    });
    await conflicts.vm.$nextTick();
    await conflicts.get('[data-testid="conflict-apply"]').trigger("click");
    expect(conflicts.emitted("action")?.[1]?.[0]).toEqual({
      method: "conflict.apply",
      params: { planId: "cccccccc-cccc-4ccc-8ccc-cccccccccccc" },
    });
  });

  it("requests retention only from authority and stays disabled before hydration", () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities([
      "workspace.session.v2",
      "retention.policy.v2",
      "repository.settings.v2",
    ]);
    const protection = useWorkspaceProtectionStore();
    const settings = mount(WorkspaceProtectionSettings, {
      props: { mode: "storage" },
    });

    expect(protection.retention).toBeNull();
    expect(protection.retentionHydrated).toBe(false);
    expect(settings.emitted("action")?.map((event) => event[0])).toEqual([
      { method: "retention.get", params: {} },
      { method: "retention.status", params: {} },
    ]);
    expect(settings.get('[data-testid="retention-save"]').attributes("disabled"))
      .toBeDefined();
  });

  it("explains durable quota pauses and integrity failures", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities([
      "workspace.session.v2",
      "retention.policy.v2",
      "repository.settings.v2",
    ]);
    const protection = useWorkspaceProtectionStore();
    protection.setRetentionStatus({
      repositoryUsageBytes: 3 * 1024 ** 3,
      repositoryLimitBytes: 2 * 1024 ** 3,
      automaticSnapshotsPaused: true,
      warningCode: "snapshot.repository_limit_reached",
      integrityStatus: "corrupt",
      integrityFailure: "repository.object_corrupt",
      lastIncrementalCheckAt: "2026-07-28T09:00:00Z",
      lastFullCheckAt: "2026-07-01T09:00:00Z",
      maintenanceFailure: null,
      maintenanceFailureStage: null,
      lastMaintenanceFailureAt: null,
    });

    const settings = mount(WorkspaceProtectionSettings, {
      props: { mode: "storage" },
    });
    expect(settings.get('[data-testid="retention-automatic-paused"]').text())
      .toContain("3.0 GB");
    expect(settings.get('[data-testid="retention-integrity-corrupt"]').text())
      .toContain("清理和覆盖性同步已停止");
  });

  it("offers keep-both only for file conflicts and previews it as a strict choice", async () => {
    const protection = useWorkspaceProtectionStore();
    const conflictId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
    const itemId = "dddddddd-dddd-4ddd-8ddd-dddddddddddd";
    protection.setConflictSets([{
      conflictId,
      state: "pending",
      createdAt: "2026-07-28T09:00:00Z",
      itemCount: 1,
    }]);
    protection.setConflicts([{
      conflictId,
      itemId,
      path: "files/quarterly-plan.docx",
      kind: "file",
      state: "pending",
      localSummary: "Local revision",
      replicaSummary: "Replica revision",
      baseSummary: "Shared base",
      dependencies: [],
      selected: "both",
    }]);
    const conflicts = mount(ConflictCenterView);
    expect(conflicts.text()).toContain("两者都保留");
    expect(conflicts.text()).toContain("新的文档身份和无冲突路径");
    await conflicts.get('[data-testid="conflict-preview"]').trigger("click");
    expect(conflicts.emitted("action")?.[0]?.[0]).toEqual({
      method: "conflict.preview",
      params: {
        conflictId,
        choices: [{ itemId, kind: "file", side: "both" }],
      },
    });
  });
});
