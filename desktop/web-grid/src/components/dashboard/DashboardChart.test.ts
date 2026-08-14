import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { parseWirePanel } from "@/dashboard";
import { useUiStore } from "@/stores/uiStore";
import DashboardChart from "./DashboardChart.vue";

const chartHarness = vi.hoisted(() => {
  const clickHandlers: Array<(event: unknown) => void> = [];
  const charts: Array<{
    setOption: ReturnType<typeof vi.fn>;
    on: ReturnType<typeof vi.fn>;
    resize: ReturnType<typeof vi.fn>;
    dispose: ReturnType<typeof vi.fn>;
  }> = [];
  const create = vi.fn((_element: HTMLElement, _dark: boolean) => {
    const chart = {
      setOption: vi.fn(),
      on: vi.fn((event: string, handler: (value: unknown) => void) => {
        if (event === "click") clickHandlers.push(handler);
      }),
      resize: vi.fn(),
      dispose: vi.fn(),
    };
    charts.push(chart);
    return chart;
  });
  return { charts, clickHandlers, create };
});

vi.mock("@/dashboard/charts/echartsCore", () => ({ createDashboardChart: chartHarness.create }));

describe("DashboardChart", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    chartHarness.charts.length = 0;
    chartHarness.clickHandlers.length = 0;
    vi.clearAllMocks();
  });

  it("mounts and updates ECharts, exposes keyboard selections, follows theme, and disposes resources", async () => {
    let resize: (() => void) | null = null;
    const disconnect = vi.fn();
    class Observer {
      observe = vi.fn();
      disconnect = disconnect;
      constructor(callback: () => void) { resize = callback; }
    }
    vi.stubGlobal("ResizeObserver", Observer);
    const panel = parseWirePanel({
      id: "p1",
      dashboardId: "d1",
      name: "年度收入",
      type: "bar",
      position: { x: 0, y: 0, width: 6, height: 4 },
      options: {},
      query: {
        kind: "aggregate",
        collection: "orders",
        dimensions: ["year"],
        measures: [{ key: "revenue", op: "sum", field: "amount" }],
      },
    });
    const wrapper = mount(DashboardChart, {
      props: { panel, rows: [{ year: 2025, revenue: 42 }, { year: 2026, revenue: 90 }] },
      global: { plugins: [createPinia()] },
    });
    await flushPromises();

    expect(chartHarness.create).toHaveBeenCalledWith(expect.any(HTMLElement), false);
    expect(chartHarness.charts[0]!.setOption).toHaveBeenCalledWith(
      expect.objectContaining({ series: expect.anything() }),
      { notMerge: true, lazyUpdate: true },
    );
    const select = wrapper.get("select");
    expect(select.findAll("option")).toHaveLength(3);
    await select.setValue("0");
    expect(wrapper.emitted("select")?.[0]).toEqual([{
      primaryField: "year",
      primaryValue: 2025,
      values: { year: 2025 },
    }]);
    let syntheticValue = "99";
    Object.defineProperty(select.element, "value", {
      get: () => syntheticValue,
      set: (value: string) => { syntheticValue = value; },
      configurable: true,
    });
    await select.trigger("change");
    expect(wrapper.emitted("select")).toHaveLength(1);

    chartHarness.clickHandlers[0]?.({ data: { value: 42, selectionValue: "north" } });
    expect(wrapper.emitted("select")?.at(-1)).toEqual(["north"]);
    expect(resize).not.toBeNull();
    (resize as unknown as () => void)();
    expect(chartHarness.charts[0]!.resize).toHaveBeenCalled();

    await wrapper.setProps({ rows: [{ year: 2027, revenue: 101 }] });
    await flushPromises();
    expect(chartHarness.charts[0]!.setOption.mock.calls.length).toBeGreaterThan(1);

    useUiStore().setThemeMode("dark");
    await flushPromises();
    expect(chartHarness.charts[0]!.dispose).toHaveBeenCalled();
    expect(chartHarness.create).toHaveBeenLastCalledWith(expect.any(HTMLElement), true);

    wrapper.unmount();
    expect(disconnect).toHaveBeenCalled();
    expect(chartHarness.charts.at(-1)!.dispose).toHaveBeenCalled();
    vi.unstubAllGlobals();
  });
});
