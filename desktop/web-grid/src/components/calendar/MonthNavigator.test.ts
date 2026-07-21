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
 * 3. Commit path: spec is Enter-to-commit (`@keyup.enter="commitInput"` on the
 *    NInput). We drive it with `inner.trigger("keyup", { key: "Enter" })` on
 *    the inner `<input>`; modern @vue/test-utils + jsdom handle the `.enter`
 *    modifier, and Vue's key modifier matches against the synthetic event's
 *    `key` value. The blur-commit binding was removed from the component per
 *    review (it created a UX race with month-grid clicks).
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
    // Commit path: spec is Enter-to-commit.
    await inner.trigger("keyup", { key: "Enter" });
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
    await inner.trigger("keyup", { key: "Enter" });
    expect(wrapper.emitted("update:monthKey")).toBeUndefined();
  });
});
