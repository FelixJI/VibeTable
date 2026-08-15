import { flushPromises, mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";
import { NInput, NInputNumber, NSelect } from "naive-ui";
import type { BindingCollectionSchema, DashboardPanel } from "@/dashboard";
import DashboardPanelEditor from "./DashboardPanelEditor.vue";

const query = {
  kind: "aggregate",
  collection: "orders",
  dimensions: ["region", "status"],
  measures: [
    { key: "revenue", op: "sum", field: "amount" },
    { key: "orders", op: "countDistinct", field: "order_no" },
  ],
  filters: [
    { field: "status", operator: "in", value: ["paid", "open"] },
    { field: "amount", operator: "gt", value: 10 },
  ],
  timeBucket: { field: "created_at", unit: "week", timezone: "UTC" },
  limit: 500,
  topN: 30,
} as const;

const panel: DashboardPanel = {
  id: "p1", dashboardId: "d1", name: "Revenue", type: "bar", rawType: "bar",
  productType: "bar", editable: true, position: { x: 0, y: 0, width: 6, height: 4 },
  options: { fillType: "gradient" }, rawOptions: { fillType: "gradient" },
  query, rawQuery: query,
};

const schema: BindingCollectionSchema = {
  collectionId: "orders", revision: "schema_7",
  fields: [
    field("region", "text", ["count", "countDistinct", "min", "max"]),
    field("status", "text", ["count", "countDistinct", "min", "max"]),
    field("amount", "decimal", ["count", "countDistinct", "sum", "avg", "min", "max"]),
    field("order_no", "text", ["count", "countDistinct", "min", "max"]),
    field("created_at", "datetime", ["count", "countDistinct", "min", "max"]),
  ],
};

function field(ref: string, dataType: "text" | "decimal" | "datetime", summaryOperations: readonly string[]) {
  return {
    ref, fieldId: `fld_${ref}`, label: ref, dataType,
    filterOperators: ["eq", "in", "gt", "gte", "lt", "between"],
    groupable: true, summaryOperations,
  };
}

describe("DashboardPanelEditor", () => {
  it("round-trips every canonical multi-binding without physical-name text inputs", async () => {
    const loadSchema = vi.fn(async () => schema);
    const wrapper = mount(DashboardPanelEditor, {
      props: {
        show: true, panel, dashboardId: "d1", collections: ["orders"],
        allowedTypes: ["bar"], loadSchema,
        manifest: {
          bar: {
            type: "bar", minSize: { x: 0, y: 0, width: 4, height: 3 },
            optionsSchema: {}, rendererVersion: "2",
          },
        },
      },
      global: { stubs: {
        teleport: true,
        NDrawer: { template: "<div><slot /></div>" },
        NDrawerContent: { template: "<section><slot /><slot name='footer' /></section>" },
        NSelect: { props: ["value", "options"], template: "<div class='select-stub' />" },
        NInput: { props: ["value"], template: "<input class='input-stub' />" },
        NInputNumber: { props: ["value"], template: "<input class='number-stub' />" },
        NButton: { template: "<button><slot /></button>" },
        NAlert: { template: "<aside><slot /><slot name='action' /></aside>" },
        NSpin: true,
      } },
    });
    await flushPromises();
    expect(loadSchema).toHaveBeenCalledTimes(1);
    expect(wrapper.findAll("input.input-stub")).toHaveLength(0);
    await wrapper.get('[data-testid="dashboard-panel-submit"]').trigger("click");
    const emitted = wrapper.emitted("submit")?.[0]?.[0] as DashboardPanel;
    expect(emitted.query).toEqual(query);
    expect(emitted.options).toEqual({ fillType: "gradient" });
  });

  it("shows drift and requires explicit repair before save", async () => {
    const stale = { ...panel, query: { ...query, dimensions: ["deleted_region"] }, rawQuery: query };
    const wrapper = mount(DashboardPanelEditor, {
      props: {
        show: true, panel: stale, dashboardId: "d1", collections: ["orders"],
        allowedTypes: ["bar"], loadSchema: async () => schema,
        manifest: { bar: { type: "bar", minSize: { x: 0, y: 0, width: 4, height: 3 }, optionsSchema: {}, rendererVersion: "2" } },
      },
      global: { stubs: {
        teleport: true,
        NDrawer: { template: "<div><slot /></div>" },
        NDrawerContent: { template: "<section><slot /><slot name='footer' /></section>" },
        NSelect: true, NInput: true, NInputNumber: true,
        NButton: { template: "<button><slot /></button>" },
        NAlert: { template: "<aside><slot /><slot name='action' /></aside>" },
        NSpin: true,
      } },
    });
    await flushPromises();
    expect(wrapper.get('[data-testid="dashboard-binding-drift"]').text()).toContain("deleted_region");
    expect(wrapper.get('[data-testid="dashboard-panel-submit"]').attributes("disabled")).toBeDefined();
    await wrapper.get('[data-testid="dashboard-repair-bindings"]').trigger("click");
    expect(wrapper.find('[data-testid="dashboard-binding-drift"]').exists()).toBe(false);
  });

  it("authors label, record, and aggregate panels through one schema-aware builder", async () => {
    const wrapper = mount(DashboardPanelEditor, {
      props: {
        show: true,
        panel: null,
        dashboardId: "d1",
        collections: ["orders"],
        allowedTypes: ["label", "list", "bar", "pie"],
        loadSchema: vi.fn(async () => schema),
        manifest: {
          label: { type: "label", minSize: { x: 0, y: 0, width: 2, height: 1 }, optionsSchema: {}, rendererVersion: "2" },
          list: { type: "list", minSize: { x: 0, y: 0, width: 4, height: 3 }, optionsSchema: {}, rendererVersion: "2" },
          bar: { type: "bar", minSize: { x: 0, y: 0, width: 5, height: 4 }, optionsSchema: {}, rendererVersion: "2" },
          pie: { type: "pie", minSize: { x: 0, y: 0, width: 4, height: 4 }, optionsSchema: {}, rendererVersion: "2" },
        },
      },
      global: { stubs: { teleport: true } },
    });
    await flushPromises();
    const typeSelect = wrapper.findAllComponents(NSelect).find(select =>
      (select.props("options") as Array<{ value: string }> | undefined)?.some(option => option.value === "label"))!;
    const inputs = wrapper.findAllComponents(NInput);
    inputs[0]!.vm.$emit("update:value", "说明卡");
    inputs.at(-1)!.vm.$emit("update:value", "离线指标说明");
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-testid="dashboard-panel-submit"]').trigger("click");
    expect((wrapper.emitted("submit")?.at(-1)?.[0] as DashboardPanel)).toMatchObject({
      name: "说明卡", productType: "label", options: { text: "离线指标说明" }, query: {},
    });

    typeSelect.vm.$emit("update:value", "list");
    await flushPromises();
    const fieldSelect = wrapper.findAllComponents(NSelect).find(select =>
      select.props("multiple") === true &&
      (select.props("options") as Array<{ value: string }> | undefined)?.some(option => option.value === "amount"))!;
    fieldSelect.vm.$emit("update:value", ["region", "amount"]);
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-testid="dashboard-panel-submit"]').trigger("click");
    expect((wrapper.emitted("submit")?.at(-1)?.[0] as DashboardPanel).query).toMatchObject({
      kind: "records", collection: "orders", fields: ["region", "amount"], limit: 100,
    });

    typeSelect.vm.$emit("update:value", "bar");
    await wrapper.vm.$nextTick();
    const multiSelects = wrapper.findAllComponents(NSelect).filter(select => select.props("multiple") === true);
    multiSelects[0]!.vm.$emit("update:value", ["region"]);
    const timeSelect = wrapper.findAllComponents(NSelect).find(select =>
      (select.props("options") as Array<{ value: string }> | undefined)?.length === 1 &&
      (select.props("options") as Array<{ value: string }>)[0]?.value === "created_at")!;
    timeSelect.vm.$emit("update:value", "created_at");
    await wrapper.vm.$nextTick();
    const topNInput = wrapper.findAllComponents(NInputNumber).at(-1)!;
    topNInput.vm.$emit("update:value", 9999);
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-testid="dashboard-panel-submit"]').trigger("click");
    expect((wrapper.emitted("submit")?.at(-1)?.[0] as DashboardPanel).query).toMatchObject({
      kind: "aggregate", collection: "orders", dimensions: ["region"],
      timeBucket: { field: "created_at", unit: "day", timezone: "UTC" }, topN: 5000,
    });
    wrapper.unmount();
  });

  it("shows schema load failures and stays non-submittable", async () => {
    const wrapper = mount(DashboardPanelEditor, {
      props: {
        show: true, panel, dashboardId: "d1", collections: ["orders"],
        allowedTypes: ["bar"], loadSchema: async () => { throw "schema.offline"; },
        manifest: { bar: { type: "bar", minSize: { x: 0, y: 0, width: 4, height: 3 }, optionsSchema: {}, rendererVersion: "2" } },
      },
      global: { stubs: { teleport: true } },
    });
    await flushPromises();
    expect(wrapper.text()).toContain("schema.offline");
    expect(wrapper.get('[data-testid="dashboard-panel-submit"]').attributes("disabled")).toBeDefined();
    await wrapper.setProps({ show: false });
    wrapper.unmount();
  });
});
