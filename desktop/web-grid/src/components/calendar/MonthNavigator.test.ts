import { describe, expect, it, beforeEach, vi } from "vitest";
import { mount, type DOMWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import MonthNavigator from "./MonthNavigator.vue";
import { NDatePicker, NPopover } from "naive-ui";

/**
 * Tests for MonthNavigator.
 *
 * The component is now a single always-visible NInput that is both the
 * editable month field and the popover trigger:
 *   - Prefix 📅 + suffix ⌄ open the month grid panel.
 *   - The visible text is the current month's localized label; the user can
 *     type over it (e.g. "2025-12") and commit via Enter/blur.
 *
 * jsdom notes:
 * 1. We drive the editable field through its raw `<input>` element
 *    (`wrapper.find("input")`), not via `findComponent(NInput).find(...)`.
 *    Forcing the NDatePicker panel open in the grid test leaves an unhandled
 *    `matchMedia` rejection that can break chained `findComponent` lookups in
 *    later tests; DOM selectors sidestep that entirely.
 * 2. NInput renders its v-model:value into the input element's `value`
 *    property, so we assert against the DOM value rather than the component
 *    prop.
 * 3. Commit path: Enter-to-commit (`@keyup.enter`) and blur-to-commit
 *    (`@blur`). Vue's key modifier matches against the synthetic event's
 *    `key` value.
 */
describe("MonthNavigator", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
    // Forcing the NDatePicker month grid open triggers two jsdom gaps that
    // crash the MonthPanel's mounted hook and poison later tests' Vue queries:
    //   - window.matchMedia (vueuc probe)
    //   - Element.scrollTo (panel scroll-justify)
    // Stub both. matchMedia mirrors useTheme.test.ts.
    vi.stubGlobal("matchMedia", (q: string) => ({
      matches: false,
      media: q,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    if (typeof Element.prototype.scrollTo !== "function") {
      Element.prototype.scrollTo = (() => {}) as Element["scrollTo"];
    }
  });

  function field(wrapper: ReturnType<typeof mount>): DOMWrapper<HTMLInputElement> {
    const input = wrapper.find("input");
    if (!input.exists()) throw new Error("month navigator input not found");
    return input as DOMWrapper<HTMLInputElement>;
  }

  it("renders the current month label from props in the input", () => {
    const wrapper = mount(MonthNavigator, {
      props: { monthKey: "2026-07", locale: "zh-CN" },
    });
    // The input value is the localized month label, which contains the year
    // and month digits regardless of locale formatting.
    const value = field(wrapper).element.value;
    expect(value).toContain("2026");
    expect(value).toMatch(/7/);
  });

  it("selects all text on focus so typing replaces the label", async () => {
    const wrapper = mount(MonthNavigator, {
      props: { monthKey: "2026-07", locale: "zh-CN" },
    });
    const inner = field(wrapper);
    const selectSpy = vi.spyOn(inner.element, "select");
    await inner.trigger("focus");
    expect(selectSpy).toHaveBeenCalled();
  });

  it("emits update:monthKey with the picked month from the grid", async () => {
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

  it("jumps when a parseable flexible string is committed via Enter", async () => {
    const wrapper = mount(MonthNavigator, {
      props: { monthKey: "2026-07", locale: "zh-CN" },
    });
    const inner = field(wrapper);
    await inner.setValue("2025-12");
    await inner.trigger("keyup", { key: "Enter" });
    expect(wrapper.emitted("update:monthKey")).toEqual([["2025-12"]]);
  });

  it("jumps when a parseable flexible string is committed via blur", async () => {
    const wrapper = mount(MonthNavigator, {
      props: { monthKey: "2026-07", locale: "zh-CN" },
    });
    const inner = field(wrapper);
    await inner.setValue("2025-12");
    await inner.trigger("blur");
    expect(wrapper.emitted("update:monthKey")).toEqual([["2025-12"]]);
  });

  it("does not emit when input is unparseable and rolls the text back", async () => {
    const wrapper = mount(MonthNavigator, {
      props: { monthKey: "2026-07", locale: "zh-CN" },
    });
    const inner = field(wrapper);
    await inner.setValue("hello");
    await inner.trigger("keyup", { key: "Enter" });
    expect(wrapper.emitted("update:monthKey")).toBeUndefined();
    // Failed parse restores the current month label rather than leaving garbage.
    expect(inner.element.value).toContain("2026");
  });

  it("syncs the input label when monthKey changes from outside", async () => {
    const wrapper = mount(MonthNavigator, {
      props: { monthKey: "2026-07", locale: "zh-CN" },
    });
    await wrapper.setProps({ monthKey: "2025-12" });
    const value = field(wrapper).element.value;
    expect(value).toContain("2025");
    expect(value).toMatch(/12/);
  });
});
