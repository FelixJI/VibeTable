import { describe, it, expect, beforeEach } from "vitest";
import { mount } from "@vue/test-utils";

import ErrorOverlay from "./ErrorOverlay.vue";

describe("ErrorOverlay", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
  });

  it("renders nothing when show is false", () => {
    const wrapper = mount(ErrorOverlay, {
      props: { show: false, message: "boom" },
    });
    expect(wrapper.find(".overlay--error").exists()).toBe(false);
  });

  it("renders the overlay with the error message when show is true", () => {
    const wrapper = mount(ErrorOverlay, {
      props: { show: true, message: "something went wrong" },
    });
    const overlay = wrapper.find(".overlay--error");
    expect(overlay.exists()).toBe(true);
    expect(wrapper.text()).toContain("something went wrong");
  });

  it("updates the displayed message when the message prop changes", async () => {
    const wrapper = mount(ErrorOverlay, {
      props: { show: true, message: "first error" },
    });
    expect(wrapper.text()).toContain("first error");

    await wrapper.setProps({ message: "second error" });
    expect(wrapper.text()).toContain("second error");
    expect(wrapper.text()).not.toContain("first error");
  });

  it("toggles between hidden and shown when the show prop changes", async () => {
    const wrapper = mount(ErrorOverlay, {
      props: { show: false, message: "boom" },
    });
    expect(wrapper.find(".overlay--error").exists()).toBe(false);

    await wrapper.setProps({ show: true });
    expect(wrapper.find(".overlay--error").exists()).toBe(true);

    await wrapper.setProps({ show: false });
    expect(wrapper.find(".overlay--error").exists()).toBe(false);
  });
});
