import { afterEach, describe, expect, it } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import ViewQueryControls from "./ViewQueryControls.vue";
import type { ColumnSchema } from "@/contracts";

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
});
