import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import ViewGroupPanel from "./ViewGroupPanel.vue";

describe("ViewGroupPanel", () => {
  it("renders full-result counts and requests the next independent group page", async () => {
    const wrapper = mount(ViewGroupPanel, { props: {
      rows: [{ key: ["east"], count: 7000, summaries: [12345] }],
      groups: [{ field: "region" }],
      summaries: [{ field: "amount", function: "sum" }],
      columns: [
        { name: "region", title: "区域", dataType: "text", editable: true, nullable: true },
        { name: "amount", title: "金额", dataType: "decimal", editable: true, nullable: true },
      ],
      hasMore: true,
    } });

    expect(wrapper.text()).toContain("区域: east");
    expect(wrapper.text()).toContain("7000");
    expect(wrapper.text()).toContain("金额 合计: 12345");
    await wrapper.get('[data-testid="view-group-more"]').trigger("click");
    expect(wrapper.emitted("more")).toHaveLength(1);
  });

  it("renders a collapsible two-level tree and emits a stable persisted key", async () => {
    const wrapper = mount(ViewGroupPanel, { props: {
      rows: [
        { key: ["east", "open"], count: 3, summaries: [30], parentCount: 9, parentSummaries: [90] },
        { key: ["east", "closed"], count: 2, summaries: [20], parentCount: 9, parentSummaries: [90] },
      ],
      groups: [{ field: "region" }, { field: "status" }],
      summaries: [{ field: "amount", function: "sum" }],
      columns: [
        { name: "region", title: "区域", dataType: "text", editable: true, nullable: true },
        { name: "status", title: "状态", dataType: "text", editable: true, nullable: true },
        { name: "amount", title: "金额", dataType: "decimal", editable: true, nullable: true },
      ],
      hasMore: false,
      collapsedKeys: [],
    } });

    expect(wrapper.text()).toContain("区域: east");
    expect(wrapper.text()).toContain("状态: open");
    expect(wrapper.get(".group-toggle").text()).toContain("9");
    expect(wrapper.get(".group-toggle").text()).toContain("金额 合计: 90");
    await wrapper.get(".group-toggle").trigger("click");
    expect(wrapper.emitted("toggle")).toEqual([['["east"]']]);
  });
});
