import { mount } from "@vue/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import { nextTick } from "vue";
import { setLocale } from "@/i18n";
import FieldManagementView from "./FieldManagementView.vue";
import type { ColumnSchema } from "@/contracts";

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
  it("opens settings for a real host autoDate system projection while the reserved id stays disabled", async () => {
    // 真实宿主投影：PocketBaseTableGateway.FieldKind 将 autoDate 投影为
    // kind="system"（dataType datetime，display readonly），但其 fieldId 是
    // 规范字段 ID，名称/格式/时区等设置可配置，不应被禁用。
    const autoDateColumn = {
      name: "f_created", fieldId: "fld_created", title: "创建时间",
      kind: "system", dataType: "datetime",
      editable: false, nullable: true,
      display: { kind: "readonly", preset: "plain", displayScale: 2, scaleMode: "max",
        trimTrailingZeros: true, useGrouping: true, currency: "CNY", percentStorage: "ratio",
        unit: null, precision: "minute", timezone: "system", mode: "default",
        trueLabel: "是", falseLabel: "否" },
    } satisfies ColumnSchema;
    const wrapper = mount(FieldManagementView, { props: { columns: [
      autoDateColumn,
      { name: "id", fieldId: "id", title: "ID", kind: "scalar", dataType: "text", nullable: false, editable: false },
    ] } });

    const autoDateButton = wrapper.get('[aria-label="设置字段 创建时间"]');
    expect(autoDateButton.attributes("disabled")).toBeUndefined();
    await autoDateButton.trigger("click");
    expect(wrapper.emitted("edit")).toEqual([["fld_created"]]);

    // 保留主键（真实 scalar 投影）仍不可配置：按钮禁用且点击不发出 edit。
    const idButton = wrapper.get('[aria-label="设置字段 ID"]');
    expect(idButton.attributes("disabled")).toBeDefined();
    await idButton.trigger("click");
    expect(wrapper.emitted("edit")).toHaveLength(1);
    wrapper.unmount();

    // sidecar schema-describe 的 id 投影（kind=system）同样由名字 guard 禁用。
    const sidecar = mount(FieldManagementView, { props: { columns: [
      { name: "id", fieldId: "id", title: "ID", kind: "system", dataType: "text", nullable: false, editable: false },
    ] } });
    const sidecarButton = sidecar.get('[aria-label="设置字段 ID"]');
    expect(sidecarButton.attributes("disabled")).toBeDefined();
    await sidecarButton.trigger("click");
    expect(sidecar.emitted("edit")).toBeUndefined();
    sidecar.unmount();
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
