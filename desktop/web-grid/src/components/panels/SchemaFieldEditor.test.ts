import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { reactive } from "vue";

import SchemaFieldEditor from "./SchemaFieldEditor.vue";
import { createSchemaFieldDraft } from "@/services/schemaFieldDraft";

describe("SchemaFieldEditor", () => {
  it("emits a structured preview request and renders the authoritative value", async () => {
    const field = reactive(createSchemaFieldDraft("formula"));
    field.name = "subtotal";
    const wrapper = mount(SchemaFieldEditor, {
      props: {
        field,
        index: 2,
        serverErrors: {},
        formulaPreview: { phase: "ready", value: 13, error: null },
      },
    });

    await wrapper.get('[data-testid="field-formula-source-2"]')
      .setValue("quantity * unit_price");
    await wrapper.get('[data-testid="field-formula-preview-row-2"]')
      .setValue('{"quantity":2,"unit_price":6.5}');
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted("formulaPreview")?.at(-1)).toEqual([{
      clientId: field.clientId,
      index: 2,
      row: { quantity: 2, unit_price: 6.5 },
    }]);
    expect(wrapper.get('[data-testid="field-formula-preview-2"]').text())
      .toContain("13");
  });

  it("keeps malformed sample JSON local and does not call the host", async () => {
    const field = reactive(createSchemaFieldDraft("formula"));
    field.name = "subtotal";
    field.formulaSource = "quantity * unit_price";
    const wrapper = mount(SchemaFieldEditor, {
      props: { field, index: 0, serverErrors: {} },
    });
    await wrapper.get('[data-testid="field-formula-preview-row-0"]')
      .setValue('{"quantity":');
    await wrapper.vm.$nextTick();

    expect(wrapper.get('[data-testid="field-formula-preview-error-0"]').text())
      .toContain("有效 JSON");
    expect(wrapper.emitted("formulaPreview")).toBeUndefined();
  });

  it("edits multi-select wire values, display names, and selection bounds", async () => {
    const field = reactive(createSchemaFieldDraft("multiSelect"));
    field.name = "status_tags";
    const wrapper = mount(SchemaFieldEditor, {
      props: { field, index: 1, serverErrors: {} },
    });

    await wrapper.get('[data-testid="field-enum-option-value-1-0"]').setValue("pending");
    await wrapper.get('[data-testid="field-enum-option-display-1-0"]').setValue("待处理");
    await wrapper.get('[data-testid="field-enum-add-option-1"]').trigger("click");
    await wrapper.get('[data-testid="field-enum-option-value-1-1"]').setValue("2");
    await wrapper.get('[data-testid="field-enum-option-display-1-1"]').setValue("已升级");
    await wrapper.get('[data-testid="field-enum-min-selected-1"]').setValue("1");
    await wrapper.get('[data-testid="field-enum-max-selected-1"]').setValue("2");

    expect(field.enumOptions.map(({ valueText, displayName }) => ({
      valueText,
      displayName,
    }))).toEqual([
      { valueText: "pending", displayName: "待处理" },
      { valueText: "2", displayName: "已升级" },
    ]);
    expect(field.enumMinSelected).toBe(1);
    expect(field.enumMaxSelected).toBe(2);
  });

  it("locks single-select bounds to semantic 0/1 instead of exposing bound inputs", () => {
    const field = reactive(createSchemaFieldDraft("select"));
    field.name = "status";
    const wrapper = mount(SchemaFieldEditor, {
      props: { field, index: 0, serverErrors: {} },
    });

    expect(wrapper.find('[data-testid="field-enum-min-selected-0"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="field-enum-max-selected-0"]').exists()).toBe(false);
    expect(wrapper.text()).toContain("最多选择 1 项");
  });

  it("lets lookup fields declare the target-compatible output type", async () => {
    const field = reactive(createSchemaFieldDraft("lookup"));
    field.name = "customer_name";
    const wrapper = mount(SchemaFieldEditor, {
      props: { field, index: 3, serverErrors: {} },
    });

    await wrapper.get('[data-testid="field-lookup-output-type-3"]')
      .setValue("integer");

    expect(field.lookupOutputType).toBe("integer");
  });

  it("shows an immutable autoDate role without editable constraints", () => {
    const field = reactive(createSchemaFieldDraft("autoDate", "updatedAt"));
    field.name = "最后更新时间";
    const wrapper = mount(SchemaFieldEditor, {
      props: { field, index: 4, serverErrors: {} },
    });

    expect(wrapper.get('[data-testid="field-autodate-summary-4"]').text())
      .toContain("最后成功保存");
    expect(wrapper.find('input[type="checkbox"]').exists()).toBe(false);
    expect(wrapper.find('input[type="text"]').exists()).toBe(false);
  });
});
