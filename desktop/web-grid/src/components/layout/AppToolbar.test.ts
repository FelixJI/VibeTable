import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { NDropdown } from "naive-ui";
import AppToolbar from "./AppToolbar.vue";
import { useWorkspaceStore } from "@/stores/workspaceStore";

describe("AppToolbar", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("does not render the former connect, refresh, row-count, or theme controls", () => {
    const wrapper = mount(AppToolbar);
    expect(wrapper.find('[data-testid="toolbar-connect"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="toolbar-refresh"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="toolbar-row-count"]').exists()).toBe(false);
    expect(wrapper.find('[aria-label="主题"]').exists()).toBe(false);
  });

  it("shows a restrained empty title when no table is selected", () => {
    const wrapper = mount(AppToolbar);
    expect(wrapper.get('[data-testid="toolbar-table-title"]').text()).toBe("选择一张数据表");
  });

  it("shows the selected table label while preserving the physical key as secondary text", () => {
    const workspace = useWorkspaceStore();
    workspace.setOpened([
      { collection: "vt_t_01abc", metadata: { displayName: "客户清单" } },
    ]);
    workspace.selectTable("vt_t_01abc");
    const wrapper = mount(AppToolbar);
    expect(wrapper.get('[data-testid="toolbar-table-title"]').text()).toBe("客户清单");
    expect(wrapper.text()).toContain("vt_t_01abc");
  });

  it("exposes refresh and help through the More menu", () => {
    const workspace = useWorkspaceStore();
    workspace.selectTable("orders");
    const wrapper = mount(AppToolbar);
    const dropdown = wrapper.findAllComponents(NDropdown).find((candidate) =>
      (candidate.props("options") as Array<{ key: string }>).some((option) => option.key === "refresh"),
    );
    expect(dropdown).toBeTruthy();
    const select = dropdown!.props("onSelect") as (key: string) => void;
    select("refresh");
    select("help");
    expect(wrapper.emitted("refresh")).toHaveLength(1);
    expect(wrapper.emitted("openHelp")).toHaveLength(1);
  });

  it("provides an accessible tooltip trigger for More", () => {
    const wrapper = mount(AppToolbar);
    expect(wrapper.get('[data-testid="toolbar-more"]').attributes("aria-label")).toBe("更多操作");
  });

  it("wires the icon-based insert-row action and disables it without a table", async () => {
    const wrapper = mount(AppToolbar);
    expect(wrapper.get('[data-testid="toolbar-insert-row"]').attributes("disabled")).toBeDefined();

    const workspace = useWorkspaceStore();
    workspace.selectTable("orders");
    await wrapper.vm.$nextTick();
    const button = wrapper.get('[data-testid="toolbar-insert-row"]');
    expect(button.attributes("aria-label")).toBe("插入新行");
    await button.trigger("click");
    expect(wrapper.emitted("insertRow")).toHaveLength(1);
  });

  it("opens the current history scope and exposes deleted records separately", async () => {
    const workspace = useWorkspaceStore();
    workspace.selectTable("orders");
    const wrapper = mount(AppToolbar, { props: { historyScopeLabel: "记录 42 · status" } });

    await wrapper.get('[data-testid="toolbar-history"]').trigger("click");
    expect(wrapper.emitted("openHistory")).toHaveLength(1);

    const dropdown = wrapper.findAllComponents(NDropdown).find((candidate) =>
      (candidate.props("options") as Array<{ key: string }>).some((option) => option.key === "archived"),
    );
    expect(dropdown).toBeTruthy();
    const select = dropdown!.props("onSelect") as (key: string) => void;
    select("archived");
    expect(wrapper.emitted("openArchivedHistory")).toHaveLength(1);
  });

  it("disables only the current-scope history action for a multi-cell selection", () => {
    const workspace = useWorkspaceStore();
    workspace.selectTable("orders");
    const wrapper = mount(AppToolbar, { props: { historyDisabled: true } });
    expect(wrapper.get('[data-testid="toolbar-history"]').attributes("disabled")).toBeDefined();
    expect(wrapper.get('[data-testid="toolbar-history-menu"]').attributes("disabled")).toBeUndefined();
  });

  it("renders host-controlled plugin placement actions and emits only their closed key", async () => {
    const workspace = useWorkspaceStore();
    workspace.selectTable("orders");
    const wrapper = mount(AppToolbar, { props: { pluginActions: [{
      key: "com.example.reader/read",
      label: "读取概览",
      risk: "read",
      disabled: false,
    }] } });

    await wrapper.get('[data-testid="plugin-toolbar-com.example.reader/read"]').trigger("click");

    expect(wrapper.emitted("pluginAction")?.[0]).toEqual(["com.example.reader/read"]);
  });
});
