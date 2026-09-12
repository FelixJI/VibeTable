import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { NButton, NDatePicker, NDynamicTags, NSelect } from "naive-ui";
import type { ColumnSchema, FilterExpression, NormalizedRelationDescriptor } from "@/contracts";
import FilterTreeEditor from "./FilterTreeEditor.vue";
import { parseGridStateJson } from "@/contracts/gridStateJson";

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
  it.each(["0.1234567890123456789", "1.0000000000000001", "1e400", "1e-400"])(
    "renders and edits numeric token %s without coercion, including initially ordinary values",
    async (token) => {
      for (const initial of [token, "12"]) {
        const nodes = parseGridStateJson(`[{"field":"amount","operator":"eq","value":${initial}}]`) as FilterExpression[];
        const wrapper = mount(FilterTreeEditor, { props: {
          nodes, columns: [{ ...columns[0], filterInput: "number" }],
        } });
        const input = wrapper.get('[aria-label="筛选值"] input');
        expect((input.element as HTMLInputElement).value).toBe(initial);
        await input.setValue(`-${token}`);
        expect(JSON.stringify(wrapper.emitted("update")?.at(-1)?.[0]))
          .toBe(`[{"field":"amount","operator":"eq","value":-${token}}]`);
        wrapper.unmount();
      }
    },
  );

  it("preserves exact numeric tokens when editing ranges and discrete numeric lists", async () => {
    const nodes = parseGridStateJson('[{"field":"amount","operator":"between","value":[0.1234567890123456789,1e400]}]') as FilterExpression[];
    const wrapper = mount(FilterTreeEditor, { props: {
      nodes, columns: [{ ...columns[0], filterInput: "number" }],
    } });
    expect((wrapper.get('[aria-label="筛选起始值"] input').element as HTMLInputElement).value)
      .toBe("0.1234567890123456789");
    const end = wrapper.get('[aria-label="筛选结束值"] input');
    expect((end.element as HTMLInputElement).value).toBe("1e400");
    await end.setValue("1.0000000000000001");
    expect(JSON.stringify(wrapper.emitted("update")?.at(-1)?.[0]))
      .toBe('[{"field":"amount","operator":"between","value":[0.1234567890123456789,1.0000000000000001]}]');
    wrapper.unmount();

    const list = mount(FilterTreeEditor, { props: {
      nodes: [{ field: "amount", operator: "in", value: [] }], columns,
    } });
    list.findComponent(NDynamicTags).vm.$emit("update:value", ["0.1234567890123456789", "1e400"]);
    await list.vm.$nextTick();
    expect(JSON.stringify(list.emitted("update")?.at(-1)?.[0]))
      .toBe('[{"field":"amount","operator":"in","value":[0.1234567890123456789,1e400]}]');
    list.unmount();
  });
  it("renders and edits an exact Host integer without Number coercion", async () => {
    const nodes = parseGridStateJson('[{"field":"amount","operator":"eq","value":9007199254740993}]') as FilterExpression[];
    const wrapper = mount(FilterTreeEditor, { props: {
      nodes, columns: [{ ...columns[0], dataType: "integer" }],
    } });
    const input = wrapper.get('[aria-label="筛选值"] input');
    expect((input.element as HTMLInputElement).value).toBe("9007199254740993");
    await input.setValue("9007199254740995");
    expect(JSON.stringify(wrapper.emitted("update")?.at(-1)?.[0]))
      .toBe('[{"field":"amount","operator":"eq","value":9007199254740995}]');
    wrapper.unmount();
  });
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
    values.findComponent(NDynamicTags).vm.$emit("update:value", ["1", "2", "3"]);
    await values.vm.$nextTick();
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

  it("chooses operand editors from SchemaCore capability without CSV guessing", () => {
    const typedColumns = [
      { ...columns[0], filterInput: "number" as const },
      {
        name: "status", title: "状态", dataType: "text" as const, editable: true, nullable: true,
        filterOperators: ["eq", "in"] as const, filterInput: "select" as const,
        filterOptions: [{ value: "open", label: "进行中" }, { value: "done", label: "完成" }],
      },
      {
        name: "due", title: "截止日", dataType: "date" as const, editable: true, nullable: true,
        filterOperators: ["eq", "between"] as const, filterInput: "date" as const,
      },
    ] satisfies ColumnSchema[];

    const numeric = mount(FilterTreeEditor, {
      props: { nodes: [{ field: "amount", operator: "eq", value: 12 }], columns: typedColumns },
    });
    expect((numeric.get('[aria-label="筛选值"] input').element as HTMLInputElement).value).toBe("12");

    const selected = mount(FilterTreeEditor, {
      props: { nodes: [{ field: "status", operator: "in", value: ["open"] }], columns: typedColumns },
    });
    const select = selected.findAllComponents(NSelect)
      .find(item => item.attributes("aria-label") === "筛选值列表");
    expect(select?.props("multiple")).toBe(true);
    expect(select?.props("options")).toEqual([
      { value: "open", label: "进行中" }, { value: "done", label: "完成" },
    ]);
    expect(selected.findComponent(NDynamicTags).exists()).toBe(false);

    const date = mount(FilterTreeEditor, {
      props: { nodes: [{ field: "due", operator: "eq", value: "2026-08-12" }], columns: typedColumns },
    });
    expect(date.findComponent(NDatePicker).exists()).toBe(true);
  });

  it("uses discrete tags for free-text multi-value filters instead of comma parsing", async () => {
    const wrapper = mount(FilterTreeEditor, {
      props: { nodes: [{ field: "note", operator: "in", value: ["a,b"] }], columns },
    });
    const tags = wrapper.findComponent(NDynamicTags);
    expect(tags.exists()).toBe(true);
    tags.vm.$emit("update:value", ["a,b", "c"]);
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("update")?.at(-1)?.[0]).toEqual([
      { field: "note", operator: "in", value: ["a,b", "c"] },
    ]);
  });

  it("searches authoritative relation targets for relation operands", async () => {
    const search = vi.fn().mockResolvedValue([
      { collection: "customers", itemId: "c-1", label: "Ada" },
    ]);
    const relation = {
      name: "customer", title: "客户", dataType: "text" as const, editable: true, nullable: true,
      kind: "relation" as const, relationId: "rel-customer", filterInput: "relation" as const,
      filterOperators: ["eq", "in"] as const,
    } satisfies ColumnSchema;
    const wrapper = mount(FilterTreeEditor, {
      props: {
        nodes: [{ field: "customer", operator: "eq", value: null }],
        columns: [relation],
        searchRelationTargets: search,
      },
    });
    const selector = wrapper.findAllComponents(NSelect)
      .find(item => item.attributes("aria-label") === "关联记录筛选值");
    selector?.vm.$emit("search", "ad");
    await flushPromises();

    expect(search).toHaveBeenCalledWith("rel-customer", "ad");
    expect(selector?.props("options")).toEqual([{ label: "Ada", value: "c-1" }]);
  });

  it("updates join logic, field/operator defaults, and removes conditions", async () => {
    const wrapper = mount(FilterTreeEditor, {
      props: {
        nodes: [
          { field: "amount", operator: "eq", value: 1 },
          { field: "amount", operator: "gt", value: 2, logic: "AND" },
        ],
        columns,
      },
    });
    const selects = wrapper.findAllComponents(NSelect);
    selects.find(item => item.attributes("aria-label") === "条件连接方式")!
      .vm.$emit("update:value", "OR");
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("update")?.at(-1)?.[0]).toMatchObject([
      {}, { logic: "OR" },
    ]);

    const fields = wrapper.findAllComponents(NSelect)
      .filter(item => item.attributes("aria-label") === "筛选字段");
    fields[1]!.vm.$emit("update:value", "note");
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("update")?.at(-1)?.[0]).toMatchObject([
      {}, { field: "note", operator: "eq", value: "" },
    ]);

    const operators = wrapper.findAllComponents(NSelect)
      .filter(item => item.attributes("aria-label") === "筛选操作符");
    operators[0]!.vm.$emit("update:value", "is_null");
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("update")?.at(-1)?.[0]).toMatchObject([
      { operator: "is_null", value: undefined }, {},
    ]);

    await wrapper.findAll('button[aria-label="删除筛选条件"]')[0]!.trigger("click");
    expect(wrapper.emitted("update")?.at(-1)?.[0]).toHaveLength(1);
  });

  it("adds and edits nested groups up to the depth boundary", async () => {
    const wrapper = mount(FilterTreeEditor, {
      props: {
        nodes: [{
          groupLogic: "AND",
          filters: [{ field: "note", operator: "contains", value: "old" }],
        }],
        columns,
      },
    });
    const groupLogic = wrapper.find(".filter-group__heading").findComponent(NSelect);
    groupLogic.vm.$emit("update:value", "OR");
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("update")?.at(-1)?.[0]).toMatchObject([{ groupLogic: "OR" }]);

    const child = wrapper.findAllComponents(FilterTreeEditor).at(-1)!;
    child.vm.$emit("update", [{ field: "note", operator: "contains", value: "new" }]);
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("update")?.at(-1)?.[0]).toMatchObject([{
      filters: [{ value: "new" }],
    }]);

    await wrapper.get('.filter-tree[data-depth="1"] > .filter-actions').findAllComponents(NButton)
      .find((button: { text(): string }) => button.text().includes("＋ 条件组"))!.trigger("click");
    expect(wrapper.emitted("update")?.at(-1)?.[0]).toHaveLength(2);
    await wrapper.get('button[aria-label="删除筛选条件组"]').trigger("click");
    expect(wrapper.emitted("update")?.at(-1)?.[0]).toEqual([]);

    const deepest = mount(FilterTreeEditor, { props: { nodes: [], columns, depth: 3 } });
    expect(deepest.findAllComponents(NButton)
      .find((button: { text(): string }) => button.text().includes("＋ 条件组"))?.props("disabled")).toBe(true);
  });

  it("edits date ranges, select values, relation fallback identities, and scalar text", async () => {
    const typed = [
      {
        name: "due", title: "截止", dataType: "date" as const, editable: true, nullable: true,
        filterInput: "date" as const, filterOperators: ["eq", "between"] as const,
      },
      {
        name: "status", title: "状态", dataType: "text" as const, editable: true, nullable: true,
        filterInput: "multiSelect" as const, filterOperators: ["eq", "in"] as const,
        filterOptions: [{ label: "进行中", value: "open" }],
      },
      {
        name: "owner", title: "负责人", fieldId: "fld_owner", dataType: "text" as const,
        editable: true, nullable: true, filterInput: "relation" as const,
        filterOperators: ["eq", "in"] as const,
      },
      { ...columns[1], filterInput: "text" as const },
    ] satisfies ColumnSchema[];
    const date = mount(FilterTreeEditor, {
      props: { nodes: [{ field: "due", operator: "between", value: ["2026-08-01", "2026-08-31"] }], columns: typed },
    });
    const pickers = date.findAllComponents(NDatePicker);
    pickers[0]!.vm.$emit("update:formatted-value", null);
    pickers[1]!.vm.$emit("update:formatted-value", "2026-09-01");
    await date.vm.$nextTick();
    expect(date.emitted("update")?.at(-1)?.[0]).toMatchObject([{ value: ["2026-08-01", "2026-09-01"] }]);

    const selected = mount(FilterTreeEditor, {
      props: { nodes: [{ field: "status", operator: "eq", value: "open" }], columns: typed },
    });
    selected.findAllComponents(NSelect)
      .find(item => item.attributes("aria-label") === "选项筛选值")!
      .vm.$emit("update:value", ["open"]);
    await selected.vm.$nextTick();
    expect(selected.emitted("update")?.at(-1)?.[0]).toMatchObject([{ value: ["open"] }]);

    const search = vi.fn().mockResolvedValue([]);
    const relation = mount(FilterTreeEditor, {
      props: {
        nodes: [{ field: "owner", operator: "in", value: ["u1"] }], columns: typed,
        relations: [{ relationId: "rel_owner", fieldRef: "fld_owner" } as NormalizedRelationDescriptor],
        searchRelationTargets: search,
      },
    });
    const relationSelect = relation.findAllComponents(NSelect)
      .find(item => item.attributes("aria-label") === "关联记录筛选值")!;
    relationSelect.vm.$emit("focus");
    relationSelect.vm.$emit("update:value", ["u2"]);
    await flushPromises();
    expect(search).toHaveBeenCalledWith("rel_owner", "");
    expect(relation.emitted("update")?.at(-1)?.[0]).toMatchObject([{ value: ["u2"] }]);

    const text = mount(FilterTreeEditor, {
      props: { nodes: [{ field: "note", operator: "contains", value: "" }], columns: typed },
    });
    await text.get('[aria-label="筛选值"] input').setValue("memo");
    expect(text.emitted("update")?.at(-1)?.[0]).toMatchObject([{ value: "memo" }]);
  });
});
