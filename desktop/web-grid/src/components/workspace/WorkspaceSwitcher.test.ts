import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { mount } from "@vue/test-utils";
import WorkspaceSwitcher from "./WorkspaceSwitcher.vue";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";

describe("WorkspaceSwitcher", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.useFakeTimers();
  });

  it("does not announce fast transitions and reveals real stages after 300ms", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    session.setWorkspaces([{
      contractVersion: "2.0",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      displayName: "研发",
      selectedRoot: "D:\\研发",
      activityRoot: null,
      storageKind: "fixed",
      coordinationStrength: "strong",
      lastOpenedAt: null,
      lastKnownHealth: "healthy",
      lastSnapshotAt: null,
      lastSyncAt: null,
      pendingSync: false,
    }]);
    session.applySession({
      contractVersion: "2.0",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      sessionEpoch: 1,
      state: "openedWritable",
      openMode: "writable",
      writable: true,
      provisional: false,
      phase: "idle",
      errorCode: null,
    });
    const wrapper = mount(WorkspaceSwitcher);

    session.reportTransitionPhase("protecting");
    session.beginSwitch("22222222-2222-4222-8222-222222222222");
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[role="status"]').exists()).toBe(false);

    vi.advanceTimersByTime(301);
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[role="status"]').text()).toContain("保护点");
    vi.useRealTimers();
  });
});
