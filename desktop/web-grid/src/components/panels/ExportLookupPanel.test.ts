import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { NCheckbox } from "naive-ui";
import ExportLookupPanel from "./ExportLookupPanel.vue";
import { setLocale } from "@/i18n";
import type { ExportLookupPanelState } from "@/composables/useDataIoTask";

function panel(overrides: Partial<ExportLookupPanelState> = {}): ExportLookupPanelState {
  return {
    format: "csv",
    collection: "orders",
    loading: false,
    error: null,
    options: [
      { lookupId: "lkp-1", displayName: "合作方标签", outputType: "text" },
      { lookupId: "lkp-2", displayName: "订单金额", outputType: "decimal" },
    ],
    lookupRevision: "schema-7",
    ...overrides,
  };
}

describe("ExportLookupPanel", () => {
  beforeEach(() => setLocale("zh-CN"));

  it("lists valid lookups as read-only text columns, unselected by default", () => {
    const wrapper = mount(ExportLookupPanel, {
      props: { panel: panel(), selectedIds: [] },
    });

    const root = wrapper.get('[data-testid="export-lookup-panel"]');
    expect(root.text()).toContain("orders");
    expect(root.text()).toContain("合作方标签");
    expect(root.text()).toContain("只读文本");
    const checkboxes = wrapper.findAllComponents(NCheckbox);
    expect(checkboxes[0]!.props("checked")).toBe(false);
    expect(wrapper.get('[data-testid="export-lookup-confirm"]').attributes("disabled"))
      .toBeUndefined();
  });

  it("emits the checked lookup ids and the confirm intent", async () => {
    const wrapper = mount(ExportLookupPanel, {
      props: { panel: panel(), selectedIds: [] },
    });

    wrapper.findAllComponents(NCheckbox)[0]!.vm.$emit("update:checked", true);
    await Promise.resolve();
    expect(wrapper.emitted("update:selectedIds")![0]).toEqual([["lkp-1"]]);

    await wrapper.get('[data-testid="export-lookup-confirm"]').trigger("click");
    expect(wrapper.emitted("confirm")).toHaveLength(1);
  });

  it("cancel emits without confirm and loading keeps confirm disabled", async () => {
    const loading = mount(ExportLookupPanel, {
      props: { panel: panel({ loading: true, options: [] }), selectedIds: [] },
    });
    expect(loading.find('[data-testid="export-lookup-loading"]').exists()).toBe(true);
    expect(loading.get('[data-testid="export-lookup-confirm"]').attributes("disabled"))
      .toBeDefined();

    const wrapper = mount(ExportLookupPanel, {
      props: { panel: panel(), selectedIds: [] },
    });
    await wrapper.get('[data-testid="export-lookup-cancel"]').trigger("click");
    expect(wrapper.emitted("cancel")).toHaveLength(1);
    expect(wrapper.emitted("confirm")).toBeUndefined();
  });

  it("surfaces catalog failures and the no-lookup state without blocking plain export", () => {
    const errored = mount(ExportLookupPanel, {
      props: { panel: panel({ error: "bridge unavailable" }), selectedIds: [] },
    });
    expect(errored.find('[data-testid="export-lookup-error"]').exists()).toBe(true);
    expect(errored.get('[data-testid="export-lookup-confirm"]').attributes("disabled"))
      .toBeUndefined();

    const empty = mount(ExportLookupPanel, {
      props: { panel: panel({ options: [] }), selectedIds: [] },
    });
    expect(empty.find('[data-testid="export-lookup-empty"]').exists()).toBe(true);
  });
});
