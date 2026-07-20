import { describe, it, expect, beforeEach } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";

import AppSidebar from "./AppSidebar.vue";
import { useWorkspaceStore } from "@/stores/workspaceStore";

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
    ]);
    const wrapper = mountSidebar();
    const names = wrapper.findAll('[data-testid="sidebar-table-name"]').map((n) => n.text());
    expect(names).toEqual(["users", "orders"]);
  });

  it("renders no table rows when collections is empty", () => {
    const wrapper = mountSidebar();
    expect(wrapper.findAll('[data-testid="sidebar-table-name"]')).toHaveLength(0);
  });

  it("marks the current table row as active", () => {
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "users", metadata: {} }]);
    workspace.selectTable("users");
    const wrapper = mountSidebar();
    // The active class is applied via :class on the rendered NListItem child.
    expect(wrapper.find(".table-item--active").exists()).toBe(true);
  });

  it("emits newTable when the new-table button is clicked", async () => {
    const wrapper = mountSidebar();
    await wrapper.find('[data-testid="sidebar-new-table"]').trigger("click");
    expect(wrapper.emitted("newTable")).toBeTruthy();
    expect(wrapper.emitted("newTable")!.length).toBe(1);
  });

  it("does not duplicate the Directus entry from the application navigation", () => {
    const wrapper = mountSidebar();
    expect(wrapper.find('[data-testid="sidebar-open-admin"]').exists()).toBe(false);
  });

  it("renders display names but emits the physical collection key", async () => {
    const workspace = useWorkspaceStore();
    workspace.setOpened([
      { collection: "vt_t_01abc", metadata: { displayName: "客户清单" } },
    ]);
    const wrapper = mountSidebar();
    expect(wrapper.get('[data-testid="sidebar-table-name"]').text()).toBe("客户清单");
    await wrapper.get('[data-testid="sidebar-table-name"]').trigger("click");
    expect(wrapper.emitted("select")?.[0]).toEqual(["vt_t_01abc"]);
  });

  it("emits select with the collection name when a row is clicked", async () => {
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "users", metadata: {} }]);
    const wrapper = mountSidebar();
    await wrapper.find('[data-testid="sidebar-table-name"]').trigger("click");
    expect(wrapper.emitted("select")).toBeTruthy();
    expect(wrapper.emitted("select")![0]).toEqual(["users"]);
  });

  it("uses sibling buttons and emits delete without selecting the row", async () => {
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "users", metadata: {} }]);
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
});
