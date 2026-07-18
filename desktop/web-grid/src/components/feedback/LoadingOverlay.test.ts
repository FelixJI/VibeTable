import { describe, it, expect, beforeEach } from "vitest";
import { mount } from "@vue/test-utils";

import LoadingOverlay from "./LoadingOverlay.vue";

describe("LoadingOverlay", () => {
  beforeEach(() => {
    // NSpin renders into the document body; keep tests isolated per run.
    document.body.innerHTML = "";
  });

  it("renders nothing when show is false", () => {
    const wrapper = mount(LoadingOverlay, { props: { show: false } });
    expect(wrapper.find(".overlay--loading").exists()).toBe(false);
    expect(wrapper.text()).toBe("");
  });

  it("renders the overlay with an NSpin when show is true", () => {
    const wrapper = mount(LoadingOverlay, { props: { show: true } });
    const overlay = wrapper.find(".overlay--loading");
    expect(overlay.exists()).toBe(true);
    // NSpin renders a spin element; assert the role-less container has spin DOM.
    // We don't assert on naive-ui internals; the presence of the overlay + a
    // child element is enough to confirm the spinner is mounted.
    expect(overlay.element.children.length).toBeGreaterThan(0);
  });

  it("toggles between hidden and shown when the show prop changes", async () => {
    const wrapper = mount(LoadingOverlay, { props: { show: false } });
    expect(wrapper.find(".overlay--loading").exists()).toBe(false);

    await wrapper.setProps({ show: true });
    expect(wrapper.find(".overlay--loading").exists()).toBe(true);

    await wrapper.setProps({ show: false });
    expect(wrapper.find(".overlay--loading").exists()).toBe(false);
  });
});
