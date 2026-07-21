import { describe, expect, it, beforeEach } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import MonthNavigator from "./MonthNavigator.vue";
import { NDatePicker, NInput, NPopover } from "naive-ui";

/**
 * Tests for MonthNavigator.
 *
 * jsdom notes (these drove the test adaptations vs. the brief's literal code):
 *
 * 1. NPopover lazy-renders its panel content. We force the panel into the DOM
 *    by emitting `update:show(true)` on the NPopover so the NDatePicker / NInput
 *    children become addressable. The component's own `v-model:show="open"`
 *    wiring makes this exercise the real open-state ref.
 *
 * 2. The NDatePicker renders its own internal NInput, so
 *    `wrapper.findComponent(NInput)` returns the date picker's input, NOT our
 *    jump field. We disambiguate by selecting the NInput whose `placeholder`
 *    prop matches our i18n key.
 *
 * 3. The brief suggested `input.vm.$emit("blur")` for the commit path, but
 *    naive-ui's NInput only fires `blur` from a native focus loss on its inner
 *    `<input>` element — `vm.$emit("blur")` does not reach the parent's
 *    `@blur` listener (no `emits: ["blur"]` declaration on NInput). We instead
 *    set the value on the inner `<input>` (`setValue`) and trigger a native
 *    `blur` on it; this is the same path a real browser takes and is the most
 *    faithful jsdom approximation. The component's `@keyup.enter` binding is
 *    unchanged (real browsers use it; jsdom doesn't synthesize it reliably).
 */
describe("MonthNavigator", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
  });

  it("renders the current month label from props", () => {
    const wrapper = mount(MonthNavigator, {
      props: { monthKey: "2026-07", locale: "zh-CN" },
    });
    expect(wrapper.text()).toContain("2026");
    expect(wrapper.text()).toContain("7");
  });

  it("emits update:monthKey with the picked month", async () => {
    const wrapper = mount(MonthNavigator, {
      props: { monthKey: "2026-07", locale: "zh-CN" },
    });
    // Force the popover panel to render so the date picker is addressable.
    await wrapper.findComponent(NPopover).vm.$emit("update:show", true);
    await wrapper.findComponent(NDatePicker).vm.$emit(
      "update:value",
      new Date(2025, 11, 1).getTime(),
    );
    expect(wrapper.emitted("update:monthKey")).toEqual([["2025-12"]]);
  });

  it("jumps when a parseable flexible string is committed via the input", async () => {
    const wrapper = mount(MonthNavigator, {
      props: { monthKey: "2026-07", locale: "zh-CN" },
    });
    await wrapper.findComponent(NPopover).vm.$emit("update:show", true);
    // NDatePicker also renders an NInput; pick ours by the i18n placeholder.
    const jumpInput = wrapper
      .findAllComponents(NInput)
      .find((c) => c.props("placeholder") === "跳转到 如 2026-7");
    expect(jumpInput, "jump NInput should be present in the popover panel").toBeTruthy();
    const inner = jumpInput!.find("input");
    expect(inner.exists()).toBe(true);
    await inner.setValue("2025-12");
    // Commit path: real browsers fire blur on focus loss; that's the same
    // handler as @keyup.enter. jsdom can't drive @keyup.enter reliably.
    await inner.trigger("blur");
    expect(wrapper.emitted("update:monthKey")).toEqual([["2025-12"]]);
  });

  it("does not emit when input is unparseable", async () => {
    const wrapper = mount(MonthNavigator, {
      props: { monthKey: "2026-07", locale: "zh-CN" },
    });
    await wrapper.findComponent(NPopover).vm.$emit("update:show", true);
    const jumpInput = wrapper
      .findAllComponents(NInput)
      .find((c) => c.props("placeholder") === "跳转到 如 2026-7");
    expect(jumpInput).toBeTruthy();
    const inner = jumpInput!.find("input");
    await inner.setValue("hello");
    await inner.trigger("blur");
    expect(wrapper.emitted("update:monthKey")).toBeUndefined();
  });
});
