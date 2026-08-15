import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent } from "vue";
import { describe, expect, it, vi } from "vitest";
import type { BindingCollectionSchema, DashboardPanel } from "@/dashboard";
import DashboardSettingsDrawer from "./DashboardSettingsDrawer.vue";

const schema: BindingCollectionSchema = {
  collectionId: "orders", revision: "schema_7",
  fields: ["status", "region"].map((ref) => ({
    ref, fieldId: `fld_${ref}`, label: ref, dataType: "text",
    filterOperators: ["eq", "in"], groupable: true,
    summaryOperations: ["count", "countDistinct", "min", "max"],
  })),
};

function panel(id: string, name: string, type: "bar" | "list", query: Record<string, unknown>): DashboardPanel {
  return {
    id, dashboardId: "d1", name, type, rawType: type, productType: type, editable: true,
    position: { x: 0, y: 0, width: 4, height: 3 }, options: {}, rawOptions: {}, query, rawQuery: query,
  };
}

describe("DashboardSettingsDrawer", () => {
  it("round-trips filters and interactions through visual panel/field bindings", async () => {
    const panels = [
      panel("source", "Source", "bar", {
        kind: "aggregate", collection: "orders", dimensions: ["region"],
        measures: [{ key: "value", op: "count", field: null }],
      }),
      panel("target", "Target", "list", {
        kind: "records", collection: "orders", fields: ["status", "region"],
      }),
    ];
    const config = {
      configVersion: 1 as const,
      refreshInterval: 0 as const,
      globalFilters: [{
        key: "status", label: "Status", type: "enum" as const, allowedFields: ["status"],
        targetPanels: ["target"], fieldBindings: { target: "status" },
      }],
      interactions: [{
        sourcePanelId: "source", sourceField: "region", targetPanelIds: ["target"], targetField: "region",
      }],
    };
    const loadSchema = vi.fn(async () => schema);
    const wrapper = mount(DashboardSettingsDrawer, {
      props: { show: false, name: "Ops", note: "", config, panels, loadSchema },
      global: { stubs: {
        teleport: true,
        NDrawer: { template: "<div><slot /></div>" },
        NDrawerContent: { template: "<section><slot /><slot name='footer' /></section>" },
        NSelect: { template: "<div class='select-stub' />" },
        NInput: { template: "<input class='input-stub' />" },
        NButton: { template: "<button><slot /></button>" },
        NAlert: true, NSpin: true,
      } },
    });
    await wrapper.setProps({ show: true });
    await flushPromises();
    expect(loadSchema).toHaveBeenCalledTimes(1);
    await wrapper.get('[data-testid="dashboard-settings-submit"]').trigger("click");
    expect(wrapper.emitted("submit")?.[0]).toEqual(["Ops", "", config]);
  });

  const SelectStub = defineComponent({
    name: "NSelect",
    props: {
      value: { default: undefined },
      options: { type: Array, default: () => [] },
      multiple: { type: Boolean, default: false },
    },
    emits: ["update:value"],
    template: "<button class='select-stub' type='button'>select</button>",
  });
  const InputStub = defineComponent({
    name: "NInput",
    props: ["value"],
    emits: ["update:value"],
    template: "<input class='input-stub' />",
  });

  function mountDrawer(options: {
    panels?: DashboardPanel[];
    config?: {
      configVersion: 1;
      refreshInterval: 0;
      globalFilters: never[];
      interactions: never[];
    };
    loadSchema?: (collectionId: string, signal: AbortSignal) => Promise<BindingCollectionSchema>;
  } = {}): VueWrapper {
    return mount(DashboardSettingsDrawer, {
      props: {
        show: false,
        name: "  Ops  ",
        note: "  note  ",
        config: options.config ?? {
          configVersion: 1,
          refreshInterval: 0,
          globalFilters: [],
          interactions: [],
        },
        panels: options.panels ?? [],
        loadSchema: options.loadSchema ?? (async () => schema),
      },
      global: { stubs: {
        teleport: true,
        NDrawer: { template: "<div><slot /></div>" },
        NDrawerContent: { template: "<div><slot /><slot name='footer' /></div>" },
        NSelect: SelectStub,
        NInput: InputStub,
        NButton: { template: "<button type='button'><slot /></button>" },
        NAlert: { template: "<aside><slot /></aside>" },
        NSpin: { template: "<span>spin</span>" },
      } },
    });
  }

  it("builds filters and interactions from schema-backed visual controls", async () => {
    const panels = [
      panel("source", "Source", "bar", {
        kind: "aggregate",
        collection: "orders",
        dimensions: ["region", 42],
        measures: [{ key: "value" }, { key: 42 }],
        timeBucket: { field: "status" },
      }),
      panel("target", "Target", "list", {
        kind: "records", collection: "orders", fields: ["status", "region"],
      }),
      panel("empty", "Empty", "list", {
        kind: "records", collection: 42, fields: null,
      }),
    ];
    const loadSchema = vi.fn(async () => schema);
    const wrapper = mountDrawer({ panels, loadSchema });

    await wrapper.setProps({ show: true });
    await flushPromises();
    expect(loadSchema).toHaveBeenCalledTimes(1);

    await wrapper.get('[data-testid="dashboard-add-filter"]').trigger("click");
    const filterTargets = wrapper.getComponent('[data-testid="dashboard-filter-targets-0"]') as VueWrapper;
    filterTargets.vm.$emit("update:value", ["source", "target", 99]);
    await wrapper.vm.$nextTick();

    const sourceBinding = wrapper.getComponent('[data-testid="dashboard-filter-binding-0-source"]') as VueWrapper;
    sourceBinding.vm.$emit("update:value", 123);
    sourceBinding.vm.$emit("update:value", "status");
    await wrapper.vm.$nextTick();

    await wrapper.get('[data-testid="dashboard-add-interaction"]').trigger("click");
    const interactionTargets = wrapper.getComponent('[data-testid="dashboard-interaction-targets-0"]') as VueWrapper;
    interactionTargets.vm.$emit("update:value", ["target", "empty"]);
    await wrapper.vm.$nextTick();
    const interactionSource = wrapper.getComponent('[data-testid="dashboard-interaction-source-0"]') as VueWrapper;
    interactionSource.vm.$emit("update:value", "missing");
    await wrapper.vm.$nextTick();

    await wrapper.get('[data-testid="dashboard-settings-submit"]').trigger("click");
    const emitted = wrapper.emitted("submit")?.at(-1);
    expect(emitted?.[0]).toBe("Ops");
    expect(emitted?.[1]).toBe("note");
    expect(emitted?.[2]).toMatchObject({
      globalFilters: [{
        targetPanels: ["source", "target"],
        fieldBindings: { source: "status", target: "status" },
        allowedFields: ["status"],
      }],
      interactions: [{ sourcePanelId: "missing", sourceField: null, targetField: "" }],
    });
  });

  it("keeps empty dashboard settings deterministic without panels or schemas", async () => {
    const loadSchema = vi.fn(async () => schema);
    const wrapper = mountDrawer({ loadSchema });
    await wrapper.setProps({ show: true });
    await flushPromises();
    expect(loadSchema).not.toHaveBeenCalled();

    await wrapper.get('[data-testid="dashboard-add-filter"]').trigger("click");
    await wrapper.get('[data-testid="dashboard-add-interaction"]').trigger("click");
    await wrapper.get('[data-testid="dashboard-settings-submit"]').trigger("click");

    expect(wrapper.emitted("submit")?.at(-1)?.[2]).toMatchObject({
      globalFilters: [{ targetPanels: [], fieldBindings: {}, allowedFields: [] }],
      interactions: [{
        sourcePanelId: "", sourceField: null, targetPanelIds: [], targetField: "",
      }],
    });
  });

  it.each([
    [new Error("schema unavailable"), "schema unavailable"],
    ["offline", "offline"],
  ])("fails closed when schema loading rejects with %p", async (reason, message) => {
    const wrapper = mountDrawer({
      panels: [panel("source", "Source", "list", { collection: "orders", fields: [] })],
      loadSchema: vi.fn(async () => { throw reason; }),
    });
    await wrapper.setProps({ show: true });
    await flushPromises();
    expect(wrapper.text()).toContain(message);
    expect(wrapper.get('[data-testid="dashboard-settings-submit"]').attributes("disabled")).toBeDefined();
  });

  it("aborts an obsolete schema request when the drawer closes", async () => {
    let rejectRequest!: (reason: unknown) => void;
    const pending = new Promise<BindingCollectionSchema>((_resolve, reject) => { rejectRequest = reject; });
    const wrapper = mountDrawer({
      panels: [panel("source", "Source", "list", { collection: "orders", fields: [] })],
      loadSchema: vi.fn((_collection, signal) => {
        signal.addEventListener("abort", () => rejectRequest(new Error("aborted")), { once: true });
        return pending;
      }),
    });
    await wrapper.setProps({ show: true });
    await wrapper.setProps({ show: false });
    await flushPromises();
    expect(wrapper.text()).not.toContain("aborted");
  });
});
