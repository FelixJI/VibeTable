import { afterEach, describe, expect, it } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { NInputNumber, NSelect } from "naive-ui";
import ViewQueryControls from "./ViewQueryControls.vue";
import type { ColumnSchema, NormalizedRelationDescriptor } from "@/contracts";

const columns = [
  { name: "status", title: "状态", dataType: "text", editable: true, nullable: true },
  { name: "amount", title: "金额", dataType: "decimal", editable: true, nullable: true },
] satisfies ColumnSchema[];

describe("ViewQueryControls", () => {
  afterEach(() => { document.body.innerHTML = ""; });

  it("provides real record search, density and column freeze controls", async () => {
    const wrapper = mount(ViewQueryControls, { attachTo: document.body, props: {
      columns, filters: [], groups: [], summaries: [], visibleFields: ["status", "amount"],
      keyword: "", density: "comfortable", frozenFields: [],
    } });
    await wrapper.get('[data-testid="view-keyword"] input').setValue("two words");
    expect(wrapper.emitted("keywordChange")?.at(-1)).toEqual(["two words"]);
    const density = wrapper.findAllComponents(NSelect).find(component => component.attributes("data-testid") === "view-density")!;
    density.vm.$emit("update:value", "compact");
    expect(wrapper.emitted("densityChange")).toEqual([["compact"]]);
    await wrapper.get('[data-testid="view-hidden-trigger"]').trigger("click");
    await flushPromises();
    const freeze = document.querySelector<HTMLElement>('[data-testid="view-freeze-status"]');
    expect(freeze).not.toBeNull(); freeze!.click(); await flushPromises();
    expect(wrapper.emitted("freeze")).toEqual([["status", true]]);
    wrapper.unmount();
  });

  it("exposes filter, ordinary grouping, summary and per-view hiding entrances", () => {
    const wrapper = mount(ViewQueryControls, {
      attachTo: document.body,
      props: {
        columns,
        filters: [{ field: "status", operator: "eq", value: "open" }],
        groups: [{ field: "status" }],
        summaries: [{ field: "amount", function: "sum" }],
        visibleFields: ["status"],
      },
    });

    expect(wrapper.get('[data-testid="view-filter-trigger"]').text()).toContain("1");
    expect(wrapper.get('[data-testid="view-group-trigger"]').text()).toContain("1");
    expect(wrapper.get('[data-testid="view-summary-trigger"]').text()).toContain("1");
    expect(wrapper.get('[data-testid="view-hidden-trigger"]').text()).toContain("1");
  });

  it("applies filter definitions through the public control surface", async () => {
    const wrapper = mount(ViewQueryControls, {
      attachTo: document.body,
      props: { columns, filters: [], groups: [], summaries: [], visibleFields: ["status", "amount"] },
    });

    await wrapper.get('[data-testid="view-filter-trigger"]').trigger("click");
    await flushPromises();
    const apply = document.querySelector<HTMLElement>('[data-testid="view-filter-apply"]');
    expect(apply).not.toBeNull();
    apply!.click();
    await flushPromises();

    expect(wrapper.emitted("change")?.[0]?.[0]).toEqual({
      filters: [],
      groups: [],
      summaries: [],
      visibleFields: ["status", "amount"],
    });
  });

  it("applies filter definitions without leaking pending visibility drafts", async () => {
    const wrapper = mount(ViewQueryControls, {
      attachTo: document.body,
      props: { columns, filters: [], groups: [], summaries: [], visibleFields: ["status", "amount"] },
    });

    // 先在“隐藏”面板里批量隐藏但不应用（草稿状态）。
    await wrapper.get('[data-testid="view-hidden-trigger"]').trigger("click");
    await flushPromises();
    document.querySelector<HTMLElement>('[data-testid="view-hidden-hide-filtered"]')!.click();
    await flushPromises();

    // 再打开“筛选”面板并应用：不得把未应用的隐藏草稿一起提交。
    await wrapper.get('[data-testid="view-filter-trigger"]').trigger("click");
    await flushPromises();
    document.querySelector<HTMLElement>('[data-testid="view-filter-apply"]')!.click();
    await flushPromises();

    const change = wrapper.emitted("change")?.at(-1)?.[0] as {
      visibleFields: string[];
    };
    expect(change.visibleFields).toEqual(["status", "amount"]);
  });

  it("冻结列后的同值新数组回显不得重置未应用的显示字段草稿", async () => {
    // 33-host-grid-presentation 复现：冻结列 → 异步保存回显（父组件把
    // filters/groups/summaries/visibleFields 全部换成同值新数组）发生在
    // 用户已取消勾选 Status 但尚未应用之间。
    const wrapper = mount(ViewQueryControls, {
      attachTo: document.body,
      props: {
        columns,
        filters: [{ field: "status", operator: "eq", value: "open" }],
        groups: [], summaries: [], visibleFields: ["status", "amount"],
        frozenFields: [],
      },
    });

    await wrapper.get('[data-testid="view-hidden-trigger"]').trigger("click");
    await flushPromises();
    // 用户搜索并批量隐藏“状态”（仅取消 Status，未应用）。
    const search = document.querySelector<HTMLInputElement>('[data-testid="view-hidden-search"] input')!;
    search.value = "状态";
    search.dispatchEvent(new Event("input", { bubbles: true }));
    await flushPromises();
    document.querySelector<HTMLElement>('[data-testid="view-hidden-hide-filtered"]')!.click();
    await flushPromises();

    // 冻结列的异步保存回显：全部 props 同值新数组 + frozenFields 更新。
    await wrapper.setProps({
      filters: [{ field: "status", operator: "eq", value: "open" }],
      groups: [],
      summaries: [],
      visibleFields: ["status", "amount"],
      frozenFields: ["status"],
    });
    await flushPromises();

    document.querySelector<HTMLElement>('[data-testid="view-hidden-apply"]')!.click();
    await flushPromises();
    expect(wrapper.emitted("change")?.at(-1)?.[0]).toMatchObject({
      visibleFields: ["amount"],
    });
    wrapper.unmount();
  });

  it("visibleFields 同集合新数组回显不覆盖草稿；语义确变仍同步", async () => {
    const wrapper = mount(ViewQueryControls, {
      attachTo: document.body,
      props: { columns, filters: [], groups: [], summaries: [], visibleFields: ["status", "amount"] },
    });

    await wrapper.get('[data-testid="view-hidden-trigger"]').trigger("click");
    await flushPromises();
    const search = document.querySelector<HTMLInputElement>('[data-testid="view-hidden-search"] input')!;
    search.value = "状态";
    search.dispatchEvent(new Event("input", { bubbles: true }));
    await flushPromises();
    document.querySelector<HTMLElement>('[data-testid="view-hidden-hide-filtered"]')!.click();
    await flushPromises();

    // 同集合新数组（仅换引用）：不覆盖草稿。
    await wrapper.setProps({ visibleFields: ["amount", "status"] });
    await flushPromises();
    document.querySelector<HTMLElement>('[data-testid="view-hidden-apply"]')!.click();
    await flushPromises();
    expect(wrapper.emitted("change")?.at(-1)?.[0]).toMatchObject({ visibleFields: ["amount"] });

    // 语义确变（别处隐藏了 amount）：草稿同步新值。
    await wrapper.setProps({ visibleFields: ["status"] });
    await flushPromises();
    document.querySelector<HTMLElement>('[data-testid="view-hidden-apply"]')!.click();
    await flushPromises();
    expect(wrapper.emitted("change")?.at(-1)?.[0]).toMatchObject({ visibleFields: ["status"] });
    wrapper.unmount();
  });

  it("keeps applied filters untouched when only visibility is applied", async () => {
    const wrapper = mount(ViewQueryControls, {
      attachTo: document.body,
      props: {
        columns,
        filters: [{ field: "status", operator: "eq", value: "open" }],
        groups: [], summaries: [], visibleFields: ["status"],
      },
    });

    await wrapper.get('[data-testid="view-hidden-trigger"]').trigger("click");
    await flushPromises();
    document.querySelector<HTMLElement>('[data-testid="view-hidden-show-all"]')!.click();
    document.querySelector<HTMLElement>('[data-testid="view-hidden-apply"]')!.click();
    await flushPromises();

    const change = wrapper.emitted("change")?.at(-1)?.[0] as {
      filters: Array<{ field: string }>;
      visibleFields: string[];
    };
    expect(change.filters).toEqual([{ field: "status", operator: "eq", value: "open" }]);
    expect(change.visibleFields).toEqual(["status", "amount"]);
  });

  it("offers single Relation grouping and a numeric interval control", async () => {
    const relation = {
      relationId: "orders.customer", fieldRef: "customer", sourceCollection: "orders",
      kind: "m2o", relatedCollection: "customers",
      unique: false, nullable: true, onDelete: "nullify", selfRelation: false,
      managed: true, state: "valid", diagnostics: [],
    } satisfies NormalizedRelationDescriptor;
    const relationColumn = {
      name: "customer", title: "客户", fieldId: "fld_customer", kind: "relation",
      relationId: relation.relationId, dataType: "text", editable: true, nullable: true,
    } satisfies ColumnSchema;
    const wrapper = mount(ViewQueryControls, {
      attachTo: document.body,
      props: {
        columns: [...columns, relationColumn], relations: [relation], lookups: [],
        filters: [], groups: [
          { field: "customer" },
          { field: "amount", bucket: "number", numberInterval: 25 },
        ],
        summaries: [], visibleFields: ["status", "amount", "customer"],
      },
    });

    await wrapper.get('[data-testid="view-group-trigger"]').trigger("click");
    await flushPromises();
    const fieldSelect = wrapper.findAllComponents(NSelect)
      .find(select => select.attributes("data-testid") === "view-group-field-0");
    const values = (fieldSelect?.props("options") ?? []).map(option => option.value);
    expect(values).toContain("customer");
    expect(wrapper.findComponent(NInputNumber).props("value")).toBe(25);
  });

  it("searches fields and supports bulk show plus show all", async () => {
    const wrapper = mount(ViewQueryControls, {
      attachTo: document.body,
      props: {
        columns, filters: [], groups: [], summaries: [], visibleFields: ["status"],
      },
    });

    await wrapper.get('[data-testid="view-hidden-trigger"]').trigger("click");
    await flushPromises();
    const search = document.querySelector<HTMLInputElement>(
      '[data-testid="view-hidden-search"] input',
    );
    expect(search).not.toBeNull();
    search!.value = "金额";
    search!.dispatchEvent(new Event("input", { bubbles: true }));
    await flushPromises();
    document.querySelector<HTMLElement>('[data-testid="view-hidden-show-filtered"]')!.click();
    document.querySelector<HTMLElement>('[data-testid="view-hidden-apply"]')!.click();
    await flushPromises();
    expect(wrapper.emitted("change")?.at(-1)?.[0]).toMatchObject({
      visibleFields: ["status", "amount"],
    });

    document.querySelector<HTMLElement>('[data-testid="view-hidden-show-all"]')!.click();
    document.querySelector<HTMLElement>('[data-testid="view-hidden-apply"]')!.click();
    await flushPromises();
    expect(wrapper.emitted("change")?.at(-1)?.[0]).toMatchObject({
      visibleFields: ["status", "amount"],
    });
  });

  it("删除筛选条件立即提交生效，不等待应用按钮", async () => {
    const wrapper = mount(ViewQueryControls, {
      attachTo: document.body,
      props: {
        columns,
        filters: [
          { field: "status", operator: "eq", value: "open" },
          { field: "amount", operator: "gt", value: 5 },
        ],
        groups: [], summaries: [], visibleFields: ["status", "amount"],
      },
    });

    await wrapper.get('[data-testid="view-filter-trigger"]').trigger("click");
    await flushPromises();
    document.querySelectorAll<HTMLButtonElement>('button[aria-label="删除筛选条件"]')[0]!
      .click();
    await flushPromises();

    const change = wrapper.emitted("change")?.at(-1)?.[0] as {
      filters: Array<{ field: string }>;
    };
    expect(change.filters).toEqual([{ field: "amount", operator: "gt", value: 5 }]);
    wrapper.unmount();
  });
});
