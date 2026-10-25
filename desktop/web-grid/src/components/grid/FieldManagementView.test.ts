import { mount } from "@vue/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import { nextTick } from "vue";
import { setLocale } from "@/i18n";
import FieldManagementView from "./FieldManagementView.vue";

describe("字段管理视图", () => {
  afterEach(() => setLocale("zh-CN"));
  it("updates field labels when the language changes", async () => {
    const wrapper = mount(FieldManagementView, { props: { columns: [
      { name: "title", title: "Title", dataType: "text", nullable: false, editable: true },
    ] } });
    setLocale("en-US");
    await nextTick();
    expect(wrapper.text()).toContain("Field view");
    expect(wrapper.text()).toContain("Text");
    expect(wrapper.text()).toContain("Editable");
    expect(wrapper.text()).not.toContain("字段");
    wrapper.unmount();
  });
  it("shows field metadata, searches names and opens settings by stable field id", async () => {
    const wrapper = mount(FieldManagementView, { props: { columns: [
      { name: "f_title", fieldId: "field-title", title: "标题", dataType: "text", nullable: false, editable: true },
      { name: "id", fieldId: "id", title: "行 ID", kind: "scalar", dataType: "text", nullable: false, editable: false },
    ] } });
    expect(wrapper.text()).toContain("2 个字段");
    expect(wrapper.text()).toContain("只读");
    await wrapper.get('[aria-label="设置字段 标题"]').trigger("click");
    expect(wrapper.emitted("edit")).toEqual([["field-title"]]);
    expect(wrapper.get('[aria-label="设置字段 行 ID"]').attributes("disabled")).toBeDefined();
    await wrapper.get('[aria-label="设置字段 行 ID"]').trigger("click");
    expect(wrapper.emitted("edit")).toEqual([["field-title"]]);
    await wrapper.get("input").setValue("标题");
    expect(wrapper.findAll("tbody tr")).toHaveLength(1);
    await wrapper.get("header button").trigger("click");
    expect(wrapper.emitted("create")).toHaveLength(1);
  });
});
