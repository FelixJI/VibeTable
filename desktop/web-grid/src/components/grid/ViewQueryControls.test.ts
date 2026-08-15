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
});
