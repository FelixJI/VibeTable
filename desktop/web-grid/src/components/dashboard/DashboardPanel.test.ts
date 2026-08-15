import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { parseWirePanel } from "@/dashboard";
import DashboardPanel from "./DashboardPanel.vue";

const data = { state: "ready" as const, rows: [{ value: 42 }], truncated: false, maxPoints: 100, updatedAt: null, error: null };

function panel(type: string, options: Record<string, unknown> = {}) {
  return parseWirePanel({
    id: `panel-${type}`, dashboardId: "d1", name: type, type,
    position: { x: 0, y: 0, width: 3, height: 2 }, options,
    query: { kind: "records", collection: "orders", fields: ["label", "value"] },
  });
}

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

  it.each(["loading", "queued"] as const)("renders the %s pending state", (state) => {
    const wrapper = mount(DashboardPanel, {
      props: { panel: panel("metric"), data: { ...data, state }, editing: false, visible: true },
    });
    expect(wrapper.find(".panel-state").exists()).toBe(true);
  });

  it("renders failed and empty list states with accessible summaries", () => {
    const failed = mount(DashboardPanel, {
      props: {
        panel: panel("list"),
        data: { ...data, state: "failed", rows: [], error: null },
        editing: false, visible: true,
      },
    });
    expect(failed.attributes("aria-label")).toContain("list");
    expect(failed.find(".panel-state--error").exists()).toBe(true);

    const empty = mount(DashboardPanel, {
      props: { panel: panel("metric-list"), data: { ...data, rows: [] }, editing: false, visible: true },
    });
    expect(empty.find(".empty-copy").exists()).toBe(true);
  });

  it("renders labels, metric lists, and formatted metric options", () => {
    const label = mount(DashboardPanel, {
      props: {
        panel: panel("label", { text: "North region" }), data,
        editing: false, visible: true,
      },
    });
    expect(label.get(".label-panel").text()).toBe("North region");
    const labelFallback = mount(DashboardPanel, {
      props: {
        panel: panel("label"), data, editing: false, visible: true,
      },
    });
    expect(labelFallback.get(".label-panel").text()).toBe("label");

    const list = mount(DashboardPanel, {
      props: {
        panel: panel("metric-list"),
        data: { ...data, rows: [{ open: 2, closed: "3" }] },
        editing: false, visible: true,
      },
    });
    expect(list.findAll(".list-row")).toHaveLength(2);

    const metric = mount(DashboardPanel, {
      props: {
        panel: panel("metric", {
          style: "currency", currency: "USD", minimumFractionDigits: 2,
          maximumFractionDigits: 2, prefix: "~", suffix: " total", percentIsWhole: true,
        }),
        data, editing: false, visible: true,
      },
    });
    expect(metric.get(".metric-panel").text()).toContain("$");

    const invalid = mount(DashboardPanel, {
      props: {
        panel: panel("metric", {
          style: "invalid", currency: 7, minimumFractionDigits: -1,
          maximumFractionDigits: 21, prefix: 7, suffix: false,
        }),
        data, editing: false, visible: true,
      },
    });
    expect(invalid.get(".metric-panel").text()).toContain("42");

    const fallbacks = mount(DashboardPanel, {
      props: {
        panel: parseWirePanel({
          id: "fallback", dashboardId: "d1", name: "", type: "list",
          position: { x: 0, y: 0, width: 2, height: 2 }, options: { text: null }, query: {},
        }),
        data: { ...data, rows: [{ label: null, value: 1 }] },
        editing: false, visible: true,
      },
    });
    expect(fallbacks.find(".panel-title strong").text()).toBeTruthy();
    expect(fallbacks.find(".list-row span").text()).toBe("");

    const emptyMetric = mount(DashboardPanel, {
      props: { panel: panel("metric"), data: { ...data, rows: [] }, editing: false, visible: true },
    });
    expect(emptyMetric.find(".metric-panel").exists()).toBe(true);
  });

  it("uses chart visibility placeholders and forwards chart selections", async () => {
    const chart = panel("bar");
    const hidden = mount(DashboardPanel, {
      props: { panel: chart, data, editing: false, visible: false },
    });
    expect(hidden.find(".chart-placeholder").exists()).toBe(true);

    const visible = mount(DashboardPanel, {
      props: { panel: chart, data, editing: false, visible: true },
      global: { stubs: {
        DashboardChart: {
          emits: ["select"],
          template: '<button class="chart-stub" @click="$emit(\'select\', { region: \'North\' })">chart</button>',
        },
      } },
    });
    await visible.get(".chart-stub").trigger("click");
    expect(visible.emitted("select")?.[0]).toEqual([{ region: "North" }]);
  });

  it("toggles the bounded data table and emits every panel command", async () => {
    const rows = Array.from({ length: 501 }, (_, index) => ({ id: index, value: index * 2 }));
    const current = panel("list");
    const wrapper = mount(DashboardPanel, {
      props: {
        panel: current,
        data: { ...data, rows, truncated: true, maxPoints: 500, updatedAt: Date.UTC(2026, 7, 12, 12) },
        editing: true,
        visible: true,
      },
    });
    for (const label of ["刷新面板", "导出 CSV", "导出面板 PNG", "编辑面板", "删除面板"]) {
      await wrapper.get(`[aria-label="${label}"]`).trigger("click");
    }
    await wrapper.get('[aria-label="切换数据表"]').trigger("click");
    expect(wrapper.findAll("tbody tr")).toHaveLength(500);
    expect(wrapper.find(".table-limit-note").exists()).toBe(true);
    expect(wrapper.find(".panel-warning").exists()).toBe(true);
    await wrapper.setProps({ data: { ...data, rows: [{ id: 1, value: 2 }] } });
    expect(wrapper.find(".table-limit-note").exists()).toBe(false);
    expect(wrapper.emitted("refresh")?.[0]).toEqual([current.id]);
    expect(wrapper.emitted("exportCsv")?.[0]).toEqual([current]);
    expect(wrapper.emitted("exportPng")?.[0]).toEqual([current]);
    expect(wrapper.emitted("edit")?.[0]).toEqual([current]);
    expect(wrapper.emitted("remove")?.[0]).toEqual([current.id]);
  });
});
