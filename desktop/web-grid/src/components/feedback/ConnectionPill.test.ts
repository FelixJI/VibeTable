import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import ConnectionPill from "./ConnectionPill.vue";
import { useWorkspaceStore } from "@/stores/workspaceStore";

describe("ConnectionPill", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("shows the healthy state with the current table count", () => {
    const workspace = useWorkspaceStore();
    workspace.setOpened(
      [
        { collection: "vt_t_01", metadata: {} },
        { collection: "vt_t_02", metadata: {} },
        { collection: "vt_t_03", metadata: {} },
      ],
      {},
    );
    const wrapper = mount(ConnectionPill);
    const pill = wrapper.get('[data-testid="connection-pill"]');
    expect(pill.text()).toContain("Directus 正常");
    expect(pill.text()).toContain("3 张表");
  });

  it("falls back to zero tables when nothing is loaded", () => {
    const workspace = useWorkspaceStore();
    workspace.setOpened([], {});
    const wrapper = mount(ConnectionPill);
    expect(wrapper.get('[data-testid="connection-pill"]').text()).toContain("0 张表");
  });

  it("surfaces the retry action when the connection fails", async () => {
    const workspace = useWorkspaceStore();
    workspace.setFailed("host unreachable");
    const wrapper = mount(ConnectionPill);
    const retry = wrapper.get('[data-testid="connection-retry"]');
    expect(retry.text()).toContain("连接异常");
    await retry.trigger("click");
    expect(wrapper.emitted("reconnect")).toHaveLength(1);
  });
});
