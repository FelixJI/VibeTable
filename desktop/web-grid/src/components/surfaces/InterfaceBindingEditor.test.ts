import { mount } from "@vue/test-utils";
import { NSelect } from "naive-ui";
import { describe, expect, it } from "vitest";

import type { DataBinding } from "@/contracts/generated/workbench";
import InterfaceBindingEditor from "./InterfaceBindingEditor.vue";

function binding(bindingId: string, tableId: string, fields: string[]): DataBinding {
  return {
    bindingId,
    query: {
      contractVersion: "1.0",
      tableId,
      fields,
      filters: [],
      sorts: [],
      cursor: null,
      pageSize: 100,
    },
    variables: [],
  };
}

describe("InterfaceBindingEditor", () => {
  it("authors typed filters and sorts without replacing the binding identity", async () => {
    const orders = binding("orders", "orders-table", ["customer", "status"]);
    const wrapper = mount(InterfaceBindingEditor, {
      props: { binding: orders, bindings: [orders] },
    });

    const addButtons = wrapper.findAll("button").filter((button) => button.text() === "添加");
    await addButtons[0]!.trigger("click");
    await wrapper.setProps({ binding: wrapper.emitted("update")!.at(-1)![0] as DataBinding });
    await addButtons[1]!.trigger("click");

    const updates = wrapper.emitted("update")!.map(([value]) => value as DataBinding);
    expect(updates[0]).toMatchObject({
      bindingId: "orders",
      query: { filters: [{ fieldId: "customer", operator: "eq", value: null }] },
    });
    expect(updates.at(-1)).toMatchObject({
      bindingId: "orders",
      query: { sorts: [{ fieldId: "customer", direction: "asc" }] },
    });
  });

  it("authors a selected-record variable with a closed source binding and field", async () => {
    const orders = binding("orders", "orders-table", ["customer", "status"]);
    const customers = binding("customers", "customers-table", ["customer_id", "name"]);
    const wrapper = mount(InterfaceBindingEditor, {
      props: { binding: customers, bindings: [orders, customers] },
    });

    const addButtons = wrapper.findAll("button").filter((button) => button.text() === "添加");
    await addButtons[2]!.trigger("click");
    const withVariable = wrapper.emitted("update")!.at(-1)![0] as DataBinding;
    await wrapper.setProps({ binding: withVariable });
    await wrapper.findAllComponents(NSelect)[2]!.vm.$emit(
      "update:value",
      "selectedRecordField",
    );

    const updated = wrapper.emitted("update")!.at(-1)![0] as DataBinding;
    expect(updated.variables[0]).toMatchObject({
      targetFieldId: "customer_id",
      source: "selectedRecordField",
      sourceBindingId: "orders",
      sourceFieldId: "customer",
    });
  });
});
