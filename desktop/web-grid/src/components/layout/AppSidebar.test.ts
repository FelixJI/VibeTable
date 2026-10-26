import { describe, it, expect, beforeEach } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";

import AppSidebar from "./AppSidebar.vue";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useUiStore } from "@/stores/uiStore";

/**
 * AppSidebar is pure-presentation: it reads collections/currentTable from the
 * workspace store and EMITS select / newTable / openAdmin / requestDelete. The
 * tests assert (a) store-binding (the list reflects collections + active row),
 * and (b) emit behavior (clicks emit the right events with the right payloads).
 *
 * No service is imported by the component, so we verify behavior end-to-end
 * through the store + emitted events only.
 */
function mountSidebar() {
  return mount(AppSidebar);
}

describe("AppSidebar", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("renders every collection name from the workspace store", () => {
    const workspace = useWorkspaceStore();
    workspace.setOpened([
      { collection: "users", metadata: {} },
      { collection: "orders", metadata: {} },
    ], { users: "Users", orders: "Orders" });
    const wrapper = mountSidebar();
    const names = wrapper.findAll('[data-testid="sidebar-table-name"]').map((n) => n.text());
    expect(names).toEqual(["Users", "Orders"]);
  });

  it("renders no table rows when collections is empty", () => {
    const wrapper = mountSidebar();
    expect(wrapper.findAll('[data-testid="sidebar-table-name"]')).toHaveLength(0);
  });

  it("marks the current table row as active", () => {
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "users", metadata: {} }], { users: "Users" });
    workspace.selectTable("users");
    const wrapper = mountSidebar();
    // The active class is applied via :class on the rendered NListItem child.
    expect(wrapper.find(".table-item--active").exists()).toBe(true);
  });

  it("describes tables with the local last-modified time, never the physical id", async () => {
    const workspace = useWorkspaceStore();
    const ui = useUiStore();
    workspace.setOpened([
      { collection: "vt_t_01abc", metadata: { remark: "不应被猜测的备注字段" } },
      { collection: "vt_t_02def", metadata: {} },
    ], { vt_t_01abc: "客户清单", vt_t_02def: "库存" });
    const wrapper = mountSidebar();
    // 未记录本机修改时间时不展示任何说明；未知 metadata 字段不参与猜测。
    expect(wrapper.findAll('[data-testid="sidebar-table-description"]')).toHaveLength(0);
    expect(wrapper.text()).not.toContain("vt_t_01abc");

    ui.touchTable("vt_t_02def");
    await wrapper.vm.$nextTick();
    const descriptions = wrapper.findAll('[data-testid="sidebar-table-description"]');
    expect(descriptions).toHaveLength(1);
    expect(descriptions[0]!.text()).toContain("本机修改");
    expect(descriptions[0]!.text()).not.toContain("打开");
  });

  it("emits newTable when the new-table button is clicked", async () => {
    const wrapper = mountSidebar();
    await wrapper.find('[data-testid="sidebar-new-table"]').trigger("click");
    expect(wrapper.emitted("newTable")).toBeTruthy();
    expect(wrapper.emitted("newTable")!.length).toBe(1);
  });

  it("does not duplicate the administration entry from the application navigation", () => {
    const wrapper = mountSidebar();
    expect(wrapper.find('[data-testid="sidebar-open-admin"]').exists()).toBe(false);
  });

  it("renders display names but emits the physical collection key", async () => {
    const workspace = useWorkspaceStore();
    workspace.setOpened([
      { collection: "vt_t_01abc", metadata: {} },
    ], { vt_t_01abc: "客户清单" });
    const wrapper = mountSidebar();
    expect(wrapper.get('[data-testid="sidebar-table-name"]').text()).toBe("客户清单");
    await wrapper.get('[data-testid="sidebar-table-name"]').trigger("click");
    expect(wrapper.emitted("select")?.[0]).toEqual(["vt_t_01abc"]);
  });

  it("emits select with the collection name when a row is clicked", async () => {
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "users", metadata: {} }], { users: "Users" });
    const wrapper = mountSidebar();
    await wrapper.find('[data-testid="sidebar-table-name"]').trigger("click");
    expect(wrapper.emitted("select")).toBeTruthy();
    expect(wrapper.emitted("select")![0]).toEqual(["users"]);
  });

  it("uses sibling buttons and emits delete without selecting the row", async () => {
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "users", metadata: {} }], { users: "Users" });
    const wrapper = mountSidebar();
    await wrapper.find('[data-testid="sidebar-request-delete"]').trigger("click");
    expect(wrapper.find("button button").exists()).toBe(false);
    expect(wrapper.emitted("requestDelete")).toBeTruthy();
    expect(wrapper.emitted("requestDelete")![0]).toEqual(["users"]);
    expect(wrapper.emitted("select")).toBeFalsy();
  });

  it("gives the table search input an accessible name", () => {
    const wrapper = mountSidebar();
    expect(wrapper.get('input[aria-label="搜索数据表"]')).toBeTruthy();
  });

  it("lets the user copy only structure and resets optional scope to checked on reopening", async () => {
    useWorkspaceStore().setOpened([{collection:"users",metadata:{}}],{users:"Users"});
    const wrapper=mount(AppSidebar,{attachTo:document.body});
    try {
      await wrapper.get('[data-testid="sidebar-request-duplicate"]').trigger("click");
      const box=(kind:string)=>document.querySelector<HTMLElement>(`[data-testid="table-duplicate-${kind}"]`)!;
      expect(box("structure").classList.contains("n-checkbox--disabled")).toBe(true);
      expect(box("records").getAttribute("aria-checked")).toBe("true");
      expect(box("views").getAttribute("aria-checked")).toBe("true");
      box("records").click(); box("views").click();
      await wrapper.vm.$nextTick();
      document.querySelector<HTMLElement>('[data-testid="table-duplicate-confirm"]')!.click();
      await wrapper.vm.$nextTick();
      expect(wrapper.emitted("duplicate")?.[0]).toEqual(["users","Users 副本",{copyRecords:false,copyViews:false}]);
      await wrapper.get('[data-testid="sidebar-request-duplicate"]').trigger("click");
      expect(box("records").getAttribute("aria-checked")).toBe("true");
      expect(box("views").getAttribute("aria-checked")).toBe("true");
    } finally { wrapper.unmount(); }
  });

  it("prefills a unique copy name and rejects duplicate table names", async () => {
    const workspace = useWorkspaceStore();
    workspace.setOpened(
      [
        { collection: "vt_t_01abc", metadata: {} },
        { collection: "vt_t_02def", metadata: {} },
      ],
      { vt_t_01abc: "客户清单", vt_t_02def: "客户清单 副本" },
    );
    const wrapper = mount(AppSidebar, { attachTo: document.body });
    try {
      await wrapper.findAll('[data-testid="sidebar-request-duplicate"]')[0]!.trigger("click");
      // 预填名与现有表名冲突时自动避让（客户清单 副本 → 客户清单 副本 2）。
      const input = document.body.querySelector<HTMLInputElement>(
        '[data-testid="table-duplicate-name"] input',
      );
      expect(input?.value).toBe("客户清单 副本 2");
      expect(input?.placeholder).toBe("副本表名");
      expect(input?.getAttribute("aria-label")).toBe("副本表名");

      // 手动改回已有名称：确认禁用且提示冲突，Enter 也不得提交。
      input!.value = "客户清单 副本";
      input!.dispatchEvent(new Event("input", { bubbles: true }));
      await wrapper.vm.$nextTick();
      expect(document.body.querySelector('[data-testid="table-duplicate-conflict"]')).toBeTruthy();
      expect(
        (document.body.querySelector('[data-testid="table-duplicate-confirm"]') as HTMLButtonElement)
          ?.disabled,
      ).toBe(true);
      input!.dispatchEvent(new KeyboardEvent("keyup", { key: "Enter", bubbles: true }));
      await wrapper.vm.$nextTick();
      expect(wrapper.emitted("duplicate")).toBeFalsy();

      // 改为可用名称后确认提交物理键与新显示名。
      input!.value = "客户清单 备份";
      input!.dispatchEvent(new Event("input", { bubbles: true }));
      await wrapper.vm.$nextTick();
      (
        document.body.querySelector('[data-testid="table-duplicate-confirm"]') as HTMLButtonElement
      )?.click();
      await wrapper.vm.$nextTick();
      expect(wrapper.emitted("duplicate")?.[0]).toEqual(["vt_t_01abc", "客户清单 备份", {copyRecords:true,copyViews:true}]);
    } finally {
      wrapper.unmount();
    }
  });
});
