import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { NButton, NSelect } from "naive-ui";
import type { ColumnSchema, FilterExpression } from "@/contracts";
import FilterTreeEditor from "./FilterTreeEditor.vue";

const columns = [
  {
    name: "amount", title: "金额", dataType: "decimal", editable: true, nullable: true,
    filterOperators: ["eq", "ne", "gt", "gte", "lt", "lte", "between", "in", "is_null", "is_not_null"],
  },
  {
    name: "note", title: "备注", dataType: "text", editable: true, nullable: true,
    filterOperators: ["eq", "ne", "contains", "starts_with", "ends_with", "in", "is_null", "is_not_null"],
  },
] satisfies ColumnSchema[];

describe("FilterTreeEditor", () => {
  it("disables every add entrance when the whole nested tree reaches 50 conditions", () => {
    const filters = Array.from({ length: 50 }, (_, index): FilterExpression => ({
      field: index % 2 === 0 ? "amount" : "note",
      operator: "eq",
      value: index,
    }));
    const wrapper = mount(FilterTreeEditor, { props: { nodes: filters, columns } });

    const addButtons = wrapper.findAllComponents(NButton)
      .filter(button => button.text().includes("＋"));
    expect(addButtons).toHaveLength(2);
    expect(addButtons.every(button => button.props("disabled") === true)).toBe(true);
  });

  it("offers operators from the selected field type instead of one global list", () => {
    const wrapper = mount(FilterTreeEditor, {
      props: {
        nodes: [{ field: "amount", operator: "eq", value: 1 }],
        columns,
      },
    });
    const operator = wrapper.findAllComponents(NSelect)
      .find(select => select.attributes("aria-label") === "筛选操作符");
    const values = (operator?.props("options") ?? []).map(option => option.value);

    expect(values).toContain("between");
    expect(values).not.toContain("contains");
  });

  it("emits typed arrays for between and in operators", async () => {
    const between = mount(FilterTreeEditor, {
      props: { nodes: [{ field: "amount", operator: "between", value: [1, 2] }], columns },
    });
    await between.get('[aria-label="筛选起始值"] input').setValue("10");
    expect(between.emitted("update")?.at(-1)?.[0]).toEqual([
      { field: "amount", operator: "between", value: [10, 2] },
    ]);

    const values = mount(FilterTreeEditor, {
      props: { nodes: [{ field: "amount", operator: "in", value: [] }], columns },
    });
    await values.get('[aria-label="筛选值列表"] input').setValue("1, 2, 3");
    expect(values.emitted("update")?.at(-1)?.[0]).toEqual([
      { field: "amount", operator: "in", value: [1, 2, 3] },
    ]);
  });

  it("uses a boolean selector and the first operator published by capability", async () => {
    const booleanColumn = {
      name: "active", title: "启用", dataType: "boolean", editable: true, nullable: true,
      filterOperators: ["eq", "ne"],
    } satisfies ColumnSchema;
    const wrapper = mount(FilterTreeEditor, {
      props: { nodes: [{ field: "active", operator: "eq", value: false }], columns: [booleanColumn] },
    });
    const selector = wrapper.findAllComponents(NSelect)
      .find(select => select.attributes("aria-label") === "布尔筛选值");
    selector?.vm.$emit("update:value", "true");
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("update")?.at(-1)?.[0]).toEqual([
      { field: "active", operator: "eq", value: true },
    ]);

    const capabilityOnly = mount(FilterTreeEditor, {
      props: { nodes: [], columns: [{ ...booleanColumn, filterOperators: ["is_not_null"] }] },
    });
    await capabilityOnly.findAllComponents(NButton)
      .find(button => button.text().includes("＋ 条件"))?.trigger("click");
    expect(capabilityOnly.emitted("update")?.[0]?.[0]).toEqual([
      { field: "active", operator: "is_not_null", value: undefined },
    ]);
  });

	it("uses only the authoritative column capability", () => {
		const wrapper = mount(FilterTreeEditor, {
			props: {
				nodes: [{ field: "amount", operator: "eq", value: 1 }],
				columns: [{ ...columns[0], filterOperators: ["eq", "is_null"] }],
			},
		});
		const operator = wrapper.findAllComponents(NSelect)
			.find(select => select.attributes("aria-label") === "筛选操作符");
		expect((operator?.props("options") ?? []).map(option => option.value))
			.toEqual(["eq", "is_null"]);
	});
});
