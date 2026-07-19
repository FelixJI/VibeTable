import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import AppNavigation from "./AppNavigation.vue";
import { useUiStore } from "@/stores/uiStore";

describe("AppNavigation", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
  });

  it("navigates between Home, Tables, and Settings", async () => {
    const ui = useUiStore();
    const wrapper = mount(AppNavigation);
    expect(ui.activeView).toBe("home");
    await wrapper.get('[data-testid="nav-tables"]').trigger("click");
    expect(ui.activeView).toBe("tables");
    await wrapper.get('[data-testid="nav-settings"]').trigger("click");
    expect(ui.activeView).toBe("settings");
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
});
