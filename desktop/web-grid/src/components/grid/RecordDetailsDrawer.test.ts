import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import { NDrawer } from "naive-ui";
import RecordDetailsDrawer from "./RecordDetailsDrawer.vue";

describe("记录详情", () => {
  it("renders schema fields without requiring content configuration or exposing transport metadata", async () => {
    const wrapper = mount(RecordDetailsDrawer, { props: {
      row: { rowKey: "internal-key", title: "合同审核", __vibetableDigest: "private-guard" },
      columns: [{ name: "title", title: "标题", dataType: "text", editable: true, nullable: false }],
    }, global: { stubs: { teleport: true } } });
    expect(wrapper.text()).toContain("合同审核");
    expect(wrapper.text()).not.toContain("private-guard");
    expect(wrapper.text()).not.toContain("internal-key");
    wrapper.findComponent(NDrawer).vm.$emit("update:show", false);
    expect(wrapper.emitted("close")).toHaveLength(1);
    await wrapper.setProps({ row: null });
    expect(wrapper.findComponent(NDrawer).props("show")).toBe(false);
    wrapper.unmount();
  });
});
