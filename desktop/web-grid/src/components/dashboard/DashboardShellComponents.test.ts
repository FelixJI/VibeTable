import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { NDataTable, NDatePicker, NDrawer, NInput, NInputNumber, NSelect } from "naive-ui";
import { defineComponent } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { parseWirePanel } from "@/dashboard";
import type { DashboardPanel } from "@/dashboard";
import DashboardCreateModal from "./DashboardCreateModal.vue";
import DashboardDrilldownDrawer from "./DashboardDrilldownDrawer.vue";
import DashboardFilterBar from "./DashboardFilterBar.vue";
import DashboardGrid from "./DashboardGrid.vue";
import DashboardSidebar from "./DashboardSidebar.vue";
import DashboardToolbar from "./DashboardToolbar.vue";

const gridHarness = vi.hoisted(() => {
  const handlers = new Map<string, (...args: unknown[]) => void>();
  const grid = {
    on: vi.fn((event: string, handler: (...args: unknown[]) => void) => handlers.set(event, handler)),
    destroy: vi.fn(),
    column: vi.fn(),
    enableMove: vi.fn(),
    enableResize: vi.fn(),
    update: vi.fn(),
  };
  return { handlers, grid, init: vi.fn(() => grid) };
});

vi.mock("gridstack", () => ({ GridStack: { init: gridHarness.init } }));

const modalStub = defineComponent({
  props: { show: Boolean },
  emits: ["close", "maskClick"],
  template: '<div v-if="show" class="modal-stub"><slot /><slot name="footer" /></div>',
});
const drawerStub = defineComponent({
  props: { show: Boolean },
  emits: ["update:show"],
  template: '<div v-if="show" class="drawer-stub"><slot /></div>',
});
const drawerContentStub = defineComponent({
  template: '<section><slot /></section>',
});
const tableStub = defineComponent({
  name: "NDataTable",
  props: ["columns", "data", "rowKey"],
  template: '<div class="data-table-stub" />',
});

function panel(id = "p1", patch: Record<string, unknown> = {}): DashboardPanel {
  return parseWirePanel({
    id,
    dashboardId: "d1",
    name: `Panel ${id}`,
    type: "metric",
    position: { x: 1, y: 2, width: 3, height: 2 },
    options: {},
    query: { kind: "aggregate", collection: "orders", measures: [{ key: "value", op: "count", field: null }] },
    ...patch,
  });
}

describe("Dashboard shell components", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    gridHarness.handlers.clear();
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("filters the sidebar, marks selection, and emits select/create", async () => {
    const wrapper = mount(DashboardSidebar, {
      props: {
        dashboards: [
          { id: "sales", name: "销售", note: "季度", panelCount: 2 },
          { id: "ops", name: "运营", note: "每日", panelCount: 3 },
        ],
        selectedId: "sales",
      },
    });
    expect(wrapper.get('[data-testid="dashboard-select-sales"]').attributes("aria-selected")).toBe("true");
    await wrapper.get('[data-testid="dashboard-select-ops"]').trigger("click");
    expect(wrapper.emitted("select")?.[0]).toEqual(["ops"]);
    await wrapper.get('[data-testid="dashboard-create"]').trigger("click");
    expect(wrapper.emitted("create")).toHaveLength(1);

    await wrapper.get("input").setValue("季度");
    expect(wrapper.find('[data-testid="dashboard-select-ops"]').exists()).toBe(false);
    await wrapper.get("input").setValue("不存在");
    expect(wrapper.text()).toContain("没有仪表盘");
  });

  it("resets create form, switches templates, validates names, and closes", async () => {
    const wrapper = mount(DashboardCreateModal, {
      props: { show: false },
      global: { plugins: [createPinia()], stubs: { teleport: true, NModal: modalStub } },
    });
    await wrapper.setProps({ show: true });
    await flushPromises();
    expect(wrapper.get('[data-testid="dashboard-create-template-blank"]').attributes("aria-checked"))
      .toBe("true");
    await wrapper.get('[data-testid="dashboard-create-template-operations-overview"]').trigger("click");
    await wrapper.get<HTMLInputElement>('[data-testid="dashboard-create-name"] input').setValue("  管理层总览  ");
    await wrapper.get('[data-testid="dashboard-create-submit"]').trigger("click");
    expect(wrapper.emitted("create")?.[0]).toEqual(["operations-overview", "管理层总览"]);

    await wrapper.get<HTMLInputElement>('[data-testid="dashboard-create-name"] input').setValue("   ");
    await wrapper.get('[data-testid="dashboard-create-submit"]').trigger("click");
    expect(wrapper.emitted("create")).toHaveLength(1);
    await wrapper.findAll("button").find((button) => button.text() === "取消")!.trigger("click");
    expect(wrapper.emitted("close")).toHaveLength(1);
  });

  it("renders every filter type and emits normalized range, enum, text, date, and clear values", async () => {
    const filters = [
      { key: "when", label: "时间", type: "date-range" },
      { key: "amount", label: "金额", type: "number-range", defaultValue: [1, 9] },
      { key: "status", label: "状态", type: "enum", defaultValue: ["open", {}, 2] },
      { key: "owner", label: "负责人", type: "user", defaultValue: "Ada" },
    ];
    const wrapper = mount(DashboardFilterBar, { props: { filters, values: {} } });
    expect(wrapper.findComponent(NDatePicker).exists()).toBe(true);
    expect(wrapper.findAllComponents(NInputNumber)).toHaveLength(2);
    expect(wrapper.findComponent(NSelect).exists()).toBe(true);
    for (const testId of [
      "dashboard-filter-value-when",
      "dashboard-filter-value-amount-min",
      "dashboard-filter-value-amount-max",
      "dashboard-filter-value-status",
      "dashboard-filter-value-owner",
      "dashboard-filters-clear",
    ]) {
      expect(wrapper.get(`[data-testid="${testId}"]`).attributes("data-testid")).toBe(testId);
    }

    wrapper.findComponent(NDatePicker).vm.$emit("update:value", [10, 20]);
    const numbers = wrapper.findAllComponents(NInputNumber);
    numbers[0]!.vm.$emit("update:value", null);
    numbers[1]!.vm.$emit("update:value", null);
    wrapper.findComponent(NSelect).vm.$emit("update:value", ["closed"]);
    wrapper.findAllComponents(NInput).at(-1)!.vm.$emit("update:value", "Grace");
    await wrapper.findAll("button").at(-1)!.trigger("click");

    expect(wrapper.emitted("change")).toEqual(expect.arrayContaining([
      ["when", [10, 20]],
      ["amount", [null, 9]],
      ["amount", [1, null]],
      ["status", ["closed"]],
      ["owner", "Grace"],
    ]));
    expect(wrapper.emitted("clear")).toHaveLength(1);

    await wrapper.setProps({ values: { amount: [null, null] } });
    wrapper.findAllComponents(NInputNumber)[0]!.vm.$emit("update:value", null);
    expect(wrapper.emitted("change")?.at(-1)).toEqual(["amount", null]);
  });

  it("exposes toolbar actions in read/edit modes and updates refresh interval", async () => {
    const wrapper = mount(DashboardToolbar, {
      props: { name: "运营", editing: false, dirty: false, saving: false, refreshInterval: 0 },
    });
    await wrapper.get('[data-testid="dashboard-refresh"]').trigger("click");
    await wrapper.findAll("button").find((button) => button.text().includes("编辑"))!.trigger("click");
    for (const label of ["删除仪表盘", "复制仪表盘", "导出仪表盘 PNG", "打印"]) {
      await wrapper.get(`[aria-label="${label}"]`).trigger("click");
    }
    expect(wrapper.emitted("refresh")).toHaveLength(1);
    expect(wrapper.emitted("edit")).toHaveLength(1);
    expect(wrapper.emitted("delete")).toHaveLength(1);
    expect(wrapper.emitted("copy")).toHaveLength(1);
    expect(wrapper.emitted("exportPng")).toHaveLength(1);
    expect(wrapper.emitted("print")).toHaveLength(1);

    await wrapper.setProps({ editing: true, dirty: true, saving: false });
    const editingButtons = wrapper.findAll("button");
    for (const index of [1, 2, 3, 4]) await editingButtons[index]!.trigger("click");
    wrapper.findComponent(NSelect).vm.$emit("update:value", 60);
    expect(wrapper.emitted("addPanel")).toHaveLength(1);
    expect(wrapper.emitted("configure")).toHaveLength(1);
    expect(wrapper.emitted("discard")).toHaveLength(1);
    expect(wrapper.emitted("save")).toHaveLength(1);
    expect(wrapper.emitted("refreshInterval")?.[0]).toEqual([60]);
    expect(wrapper.text()).toContain("未保存");
  });

  it("formats drilldown context/table cells, shows states, and closes from the drawer", async () => {
    const wrapper = mount(DashboardDrilldownDrawer, {
      props: {
        show: true,
        title: "明细",
        selection: { region: "North" },
        rows: [{ id: 1, value: null }, { name: "fallback", value: { amount: 2 } }],
        truncated: true,
        loading: false,
        error: "query.offline",
      },
      global: { stubs: { teleport: true, NDrawer: drawerStub, NDrawerContent: drawerContentStub, NDataTable: tableStub } },
    });
    expect(wrapper.text()).toContain('{"region":"North"}');
    expect(wrapper.text()).toContain("query.offline");
    expect(wrapper.text()).toContain("100 行安全上限");
    const table = wrapper.findComponent(NDataTable);
    const columns = table.props("columns") as Array<{ key: string; render: (row: Record<string, unknown>) => string }>;
    expect(columns.map((column) => column.key)).toEqual(["id", "value", "name"]);
    expect(columns.find((column) => column.key === "value")!.render({ value: null })).toBe("—");
    expect(columns.find((column) => column.key === "value")!.render({ value: { amount: 2 } })).toBe('{"amount":2}');
    const rowKey = table.props("rowKey") as (row: Record<string, unknown>) => string;
    expect(rowKey({ id: 3 })).toBe("3");
    expect(rowKey({ name: "fallback" })).toBe('{"name":"fallback"}');

    wrapper.findComponent(NDrawer).vm.$emit("update:show", false);
    expect(wrapper.emitted("close")).toHaveLength(1);
    await wrapper.setProps({ loading: true });
    expect(wrapper.text()).toContain("正在加载");
  });

  it("initializes the grid, forwards panel events, handles keyboard layout, visibility, and teardown", async () => {
    let intersectionCallback: ((entries: IntersectionObserverEntry[]) => void) | null = null;
    class Observer {
      observe = vi.fn();
      disconnect = vi.fn();
      constructor(callback: (entries: IntersectionObserverEntry[]) => void) { intersectionCallback = callback; }
    }
    vi.stubGlobal("IntersectionObserver", Observer);
    vi.stubGlobal("CSS", { escape: (value: string) => value });
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(1000);
    const panels = [panel(), panel("custom", { type: "vendor", position: { x: 0, y: 0, width: 1, height: 1 } })];
    const wrapper = mount(DashboardGrid, {
      props: {
        panels,
        data: {},
        editing: true,
        manifest: { metric: { type: "metric", minSize: { x: 0, y: 0, width: 2, height: 2 }, optionsSchema: {}, rendererVersion: "2" } },
      },
      global: {
        stubs: {
          DashboardPanelView: defineComponent({
            emits: ["remove", "edit", "refresh", "exportCsv", "exportPng", "select"],
            template: '<button class="panel-stub" @click="$emit(\'select\', 42)">panel</button>',
          }),
        },
      },
    });
    await flushPromises();
    expect(gridHarness.init).toHaveBeenCalled();
    expect(wrapper.get('[data-panel-id="p1"]').attributes("gs-min-w")).toBe("2");
    expect(wrapper.get('[data-panel-id="custom"]').attributes("gs-min-w")).toBe("1");

    await wrapper.get('[data-panel-id="p1"]').trigger("keydown", { altKey: true, key: "ArrowRight" });
    expect(wrapper.emitted("layout")?.at(-1)).toEqual(["p1", expect.objectContaining({ x: 2 })]);
    await wrapper.get('[data-panel-id="p1"]').trigger("keydown", { altKey: false, key: "ArrowLeft" });
    expect(wrapper.emitted("layout")).toHaveLength(1);
    await wrapper.findAll(".panel-stub")[0]!.trigger("click");
    expect(wrapper.emitted("select")?.[0]).toEqual([expect.objectContaining({ id: "p1" }), 42]);

    const first = wrapper.get('[data-panel-id="p1"]').element as HTMLElement;
    expect(intersectionCallback).not.toBeNull();
    (intersectionCallback as unknown as (entries: IntersectionObserverEntry[]) => void)([
      { target: first, isIntersecting: true } as unknown as IntersectionObserverEntry,
    ]);
    expect(wrapper.emitted("visibility")?.at(-1)).toEqual([["p1"]]);

    gridHarness.handlers.get("change")?.({}, [{ el: first, x: 4, y: 5, w: 6, h: 3 }]);
    expect(wrapper.emitted("layout")?.at(-1)).toEqual(["p1", { x: 4, y: 5, width: 6, height: 3 }]);
    gridHarness.handlers.get("change")?.({}, [{ el: first, x: undefined, y: 0, w: 1, h: 1 }]);
    await wrapper.setProps({ editing: false });
    await flushPromises();
    expect(gridHarness.grid.destroy).toHaveBeenCalled();
    wrapper.unmount();
    vi.unstubAllGlobals();
  });
});
