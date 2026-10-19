import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import AppNavigation from "./AppNavigation.vue";

describe("AppNavigation", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
  });

  it("emits direct navigation intent without retired layout-builder views", async () => {
    const wrapper = mount(AppNavigation);
    await wrapper.get('[data-testid="nav-tables"]').trigger("click");
    await wrapper.get('[data-testid="import-management-open"]').trigger("click");
    await wrapper.get('[data-testid="nav-files"]').trigger("click");
    await wrapper.get('[data-testid="nav-plugins"]').trigger("click");
    await wrapper.get('[data-testid="nav-settings"]').trigger("click");
    expect(wrapper.emitted("navigate")?.map((args) => args[0])).toEqual([
      "tables", "imports", "files", "plugins", "settings",
    ]);
    // #485: the fixed dashboard/interfaces entries retired with the visual
    // layout builder; application entries come from installed plugins only.
    expect(wrapper.find('[data-testid="nav-dashboard"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="nav-interfaces"]').exists()).toBe(false);
  });

  it("exposes the import management page behind the fixed E2E entry selector", () => {
    const wrapper = mount(AppNavigation);
    const entry = wrapper.get('[data-testid="import-management-open"]');
    expect(entry.attributes("aria-label")).toBe("导入管理");
  });

  it("emits administration and help actions without pretending they are routes", async () => {
    const wrapper = mount(AppNavigation);
    await wrapper.get('[data-testid="nav-admin"]').trigger("click");
    await wrapper.get('[data-testid="nav-help"]').trigger("click");
    expect(wrapper.emitted("openAdmin")).toHaveLength(1);
    expect(wrapper.emitted("openHelp")).toHaveLength(1);
  });

  it("gives every icon-only control an accessible label", () => {
    const wrapper = mount(AppNavigation);
    for (const button of wrapper.findAll("button")) {
      expect(button.attributes("aria-label")).toBeTruthy();
    }
  });
});
