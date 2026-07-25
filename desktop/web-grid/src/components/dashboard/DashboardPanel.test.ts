import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { parseWirePanel } from "@/dashboard";
import DashboardPanel from "./DashboardPanel.vue";

const data = { state: "ready" as const, rows: [{ value: 42 }], truncated: false, maxPoints: 100, updatedAt: null, error: null };

describe("DashboardPanel", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("degrades unknown panels to a safe read-only explanation", () => {
    const panel = parseWirePanel({ id: "p1", dashboardId: "d1", name: "Heatmap", type: "vendor-heatmap", position: { x: 0, y: 0, width: 6, height: 4 }, options: { executable: "not-run" }, query: {} });
    const wrapper = mount(DashboardPanel, { props: { panel, data, editing: false, visible: true } });
    expect(wrapper.text()).toContain("不支持的面板");
    expect(wrapper.text()).toContain("vendor-heatmap");
    expect(wrapper.find('[aria-label="删除面板"]').exists()).toBe(false);
  });

  it("honors hidden headers in read mode and exposes accessible export controls while editing", async () => {
    const panel = parseWirePanel({ id: "p2", dashboardId: "d1", name: "Revenue", showHeader: false, type: "metric", position: { x: 0, y: 0, width: 3, height: 2 }, options: {}, query: {} });
    const wrapper = mount(DashboardPanel, { props: { panel, data, editing: false, visible: true } });
    expect(wrapper.find(".panel-header").exists()).toBe(false);
    await wrapper.setProps({ editing: true });
    const exportButton = wrapper.get('[aria-label="导出面板 PNG"]');
    await exportButton.trigger("click");
    expect(wrapper.emitted("exportPng")?.[0]?.[0]).toMatchObject({ id: "p2" });
    for (const button of wrapper.findAll("button")) expect(button.attributes("aria-label")).toBeTruthy();
  });
});
