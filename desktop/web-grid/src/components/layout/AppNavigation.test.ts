import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import AppNavigation from "./AppNavigation.vue";
import { useDashboardStore } from "@/stores/dashboardStore";

describe("AppNavigation", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
  });

  it("emits navigation intent so the workspace can guard dirty drafts", async () => {
    const wrapper = mount(AppNavigation);
    await wrapper.get('[data-testid="nav-tables"]').trigger("click");
    await wrapper.get('[data-testid="nav-files"]').trigger("click");
    await wrapper.get('[data-testid="nav-plugins"]').trigger("click");
    await wrapper.get('[data-testid="nav-settings"]').trigger("click");
    expect(wrapper.emitted("navigate")?.map((args) => args[0])).toEqual([
      "tables", "files", "plugins", "settings",
    ]);
  });

  it("emits Directus and help actions without pretending they are routes", async () => {
    const wrapper = mount(AppNavigation);
    await wrapper.get('[data-testid="nav-directus"]').trigger("click");
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

  it("keeps dashboards hidden until the host feature gate is enabled", async () => {
    const dashboards = useDashboardStore();
    const wrapper = mount(AppNavigation);
    expect(wrapper.find('[data-testid="nav-dashboard"]').exists()).toBe(false);
    dashboards.setFeatureEnabled(true);
    await wrapper.vm.$nextTick();
    const button = wrapper.get('[data-testid="nav-dashboard"]');
    expect(button.attributes("aria-label")).toBe("仪表盘");
    await button.trigger("click");
    expect(wrapper.emitted("navigate")?.at(-1)).toEqual(["dashboard"]);
  });
});
