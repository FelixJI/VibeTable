import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { describe, expect, it } from "vitest";

import LookupGroupPanel from "./LookupGroupPanel.vue";
import { useTableStore } from "@/stores/tableStore";

describe("LookupGroupPanel", () => {
  it("stays absent without server groups", () => {
    const pinia = createPinia();
    setActivePinia(pinia);
    const wrapper = mount(LookupGroupPanel, { global: { plugins: [pinia] } });
    expect(wrapper.find("section").exists()).toBe(false);
  });

  it("renders nested paths, null fallbacks, aggregates and cursors", () => {
    const pinia = createPinia();
    setActivePinia(pinia);
    const store = useTableStore();
    store.lookupGroups = [{
      path: [{ fieldRef: "country", key: "CN" }, null],
      key: "CN",
      count: 4,
      aggregates: { revenue: 12, missing: null },
      childCursor: "child-1",
    }, {
      path: ["plain"],
      key: "plain",
      count: 1,
      aggregates: {},
      childCursor: null,
    }];

    const wrapper = mount(LookupGroupPanel, { global: { plugins: [pinia] } });
    expect(wrapper.text()).toContain("country: CN / —");
    expect(wrapper.text()).toContain("revenue: 12");
    expect(wrapper.text()).toContain("missing: —");
    expect(wrapper.text()).toContain("plain");
    expect(wrapper.get('[data-child-cursor="child-1"]').attributes("style"))
      .toContain("26px");
  });
});
