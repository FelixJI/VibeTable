import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";

import InterfaceBuilderElement from "./InterfaceBuilderElement.vue";

describe("InterfaceBuilderElement", () => {
  const leaf = {
    elementId: "leaf",
    kind: "text" as const,
    bindingId: null,
    actionId: null,
    text: "Leaf",
    width: "half" as const,
    children: [],
  };

  it("supports pointer/keyboard selection, child insertion and recursive removal", async () => {
    const wrapper = mount(InterfaceBuilderElement, {
      props: {
        element: {
          ...leaf,
          elementId: "section",
          kind: "section",
          width: "full",
          children: [leaf],
        },
        selectedId: "leaf",
      },
    });
    expect(wrapper.get('[data-testid="interface-builder-element-leaf"]').classes()).toContain("selected");
    await wrapper.get('[data-testid="interface-builder-element-section"]').trigger("keydown", { key: "Enter" });
    expect(wrapper.emitted("select")?.[0]).toEqual(["section"]);
    await wrapper.findAll('button[aria-label="添加子元素"]')[0]!.trigger("click");
    expect(wrapper.emitted("addChild")?.[0]).toEqual(["section"]);
    await wrapper.findAll('button[aria-label="删除元素"]')[1]!.trigger("click");
    expect(wrapper.emitted("remove")?.[0]).toEqual(["leaf"]);
  });
});
