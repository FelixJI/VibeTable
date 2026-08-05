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
});
