import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { NButton, NSelect } from "naive-ui";
import type { ColumnSchema, FilterExpression } from "@/contracts";
import FilterTreeEditor from "./FilterTreeEditor.vue";

const columns = [
  { name: "amount", title: "金额", dataType: "decimal", editable: true, nullable: true },
  { name: "note", title: "备注", dataType: "text", editable: true, nullable: true },
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

	it("uses the authoritative column capability when it is present", () => {
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
