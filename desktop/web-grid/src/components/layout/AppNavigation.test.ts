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

  it("emits direct navigation intent without retired layout-builder views", async () => {
    const wrapper = mount(AppNavigation);
    await wrapper.get('[data-testid="nav-tables"]').trigger("click");
    await wrapper.get('[data-testid="nav-files"]').trigger("click");
    await wrapper.get('[data-testid="nav-plugins"]').trigger("click");
    await wrapper.get('[data-testid="nav-settings"]').trigger("click");
    expect(wrapper.emitted("navigate")?.map((args) => args[0])).toEqual([
      "tables", "files", "plugins", "settings",
    ]);
    // #485: the fixed dashboard/interfaces entries retired with the visual
    // layout builder; application entries come from installed plugins only.
    expect(wrapper.find('[data-testid="nav-dashboard"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="nav-interfaces"]').exists()).toBe(false);
  });

  it("keeps import management out of the top-level navigation", () => {
    // 导入整体调制到数据表内（DataSourceViewBar），不再是独立一级入口。
    const wrapper = mount(AppNavigation);
    expect(wrapper.find('[data-testid="import-management-open"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="nav-imports"]').exists()).toBe(false);
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

  it("highlights a clicked plugin app entry immediately and hands over to the live key", async () => {
    const ui = useUiStore();
    const wrapper = mount(AppNavigation, {
      props: {
        pluginApps: [
          { key: "com.example.erp/app", label: "ERP 应用" },
          { key: "com.example.crm/app", label: "CRM 应用" },
        ],
        activePluginAppKey: null,
      },
    });
    const erp = wrapper.get('[data-testid="nav-plugin-app-com.example.erp/app"]');
    await erp.trigger("click");
    // 点击即高亮：不等待宿主 describeAction 往返。
    expect(erp.classes()).toContain("nav-button--active");
    expect(erp.attributes("aria-current")).toBe("page");
    expect(wrapper.emitted("openPluginApp")?.[0]).toEqual(["com.example.erp/app"]);

    // 真实激活键到达后由真实状态接管。
    await wrapper.setProps({ activePluginAppKey: "com.example.crm/app" });
    expect(wrapper.get('[data-testid="nav-plugin-app-com.example.crm/app"]').classes())
      .toContain("nav-button--active");
    expect(wrapper.get('[data-testid="nav-plugin-app-com.example.erp/app"]').classes())
      .not.toContain("nav-button--active");

    // 任意平台导航都会清除乐观态（父组件随 ui.activePluginApp 置空传入的键）。
    ui.navigate("tables");
    await wrapper.setProps({ activePluginAppKey: null });
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-testid="nav-plugin-app-com.example.crm/app"]').classes())
      .not.toContain("nav-button--active");
  });

  it("keeps the click target highlighted over the still-active app during the switch window", async () => {
    const ui = useUiStore();
    const wrapper = mount(AppNavigation, {
      props: {
        pluginApps: [
          { key: "com.example.erp/app", label: "ERP 应用" },
          { key: "com.example.crm/app", label: "CRM 应用" },
        ],
        // 插件 A 仍占用视口，用户点击 B：异步授权往返期间高亮必须跟随 B。
        activePluginAppKey: "com.example.erp/app",
      },
    });
    const erp = wrapper.get('[data-testid="nav-plugin-app-com.example.erp/app"]');
    const crm = wrapper.get('[data-testid="nav-plugin-app-com.example.crm/app"]');
    expect(erp.classes()).toContain("nav-button--active");

    await crm.trigger("click");
    expect(wrapper.emitted("openPluginApp")?.[0]).toEqual(["com.example.crm/app"]);
    expect(crm.classes()).toContain("nav-button--active");
    expect(erp.classes()).not.toContain("nav-button--active");

    // 切换成功（真实键 A→B）后清除乐观态，仍由真实键高亮 B。
    await wrapper.setProps({ activePluginAppKey: "com.example.crm/app" });
    expect(crm.classes()).toContain("nav-button--active");
    expect(erp.classes()).not.toContain("nav-button--active");

    // 平台导航离开：乐观态清除，随真实键清空后不再高亮。
    await wrapper.get('[data-testid="nav-tables"]').trigger("click");
    ui.navigate("tables");
    await wrapper.setProps({ activePluginAppKey: null });
    await wrapper.vm.$nextTick();
    expect(crm.classes()).not.toContain("nav-button--active");
    expect(erp.classes()).not.toContain("nav-button--active");
  });
});
