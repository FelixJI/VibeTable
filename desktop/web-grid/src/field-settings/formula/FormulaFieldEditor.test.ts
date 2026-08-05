import { afterEach, describe, expect, it, vi } from "vitest";
import { mount, type VueWrapper } from "@vue/test-utils";
import { NSelect } from "naive-ui";
import FormulaFieldEditor from "./FormulaFieldEditor.vue";

const mounted: VueWrapper[] = [];

function mountEditor(): VueWrapper {
  const wrapper = mount(FormulaFieldEditor, {
    props: {
      value: {
        language: "cel-v1",
        source: "f_price * 2",
        resultType: "number",
      },
      localFields: [
        { label: "单价", canonicalName: "f_price", dataType: "number" },
        { label: "备注", canonicalName: "f_note", dataType: "text" },
      ],
      relations: [{
        label: "明细",
        canonicalName: "f_lines",
        many: true,
        targetFields: [
          { label: "金额", canonicalName: "f_amount", dataType: "decimal" },
          { label: "说明", canonicalName: "f_description", dataType: "text" },
        ],
      }, {
        label: "客户",
        canonicalName: "f_customer",
        many: false,
        targetFields: [
          { label: "信用额度", canonicalName: "f_credit", dataType: "decimal" },
        ],
      }],
    },
  });
  mounted.push(wrapper);
  return wrapper;
}

afterEach(() => {
  mounted.splice(0).forEach(wrapper => wrapper.unmount());
});

describe("FormulaFieldEditor", () => {
  it("通过专用入口暂存编辑，取消时不污染字段草稿", async () => {
    const wrapper = mountEditor();
    expect(wrapper.text()).toContain("{单价} * 2");
    expect(wrapper.text()).not.toContain("f_price");
    expect(wrapper.find('[data-testid="formula-source"]').exists()).toBe(false);

    await wrapper.get('[data-testid="formula-editor-entry"]').trigger("click");
    await wrapper.get('[data-testid="formula-source"]').find("textarea")
      .setValue("{单价} * 3");
    await wrapper.get('[data-testid="formula-editor-cancel"]').trigger("click");

    expect(wrapper.emitted("commit")).toBeUndefined();
    expect(wrapper.text()).toContain("{单价} * 2");
  });

  it("只提交经过 sidecar 校验并自动推断类型的展示名公式", async () => {
    vi.useFakeTimers();
    const wrapper = mountEditor();
    await wrapper.get('[data-testid="formula-editor-entry"]').trigger("click");
    await wrapper.get('[data-testid="formula-source"]').find("textarea")
      .setValue("  {单价} * 3  ");
    await vi.advanceTimersByTimeAsync(250);
    expect(wrapper.emitted("validate")?.at(-1)).toEqual(["{单价} * 3"]);
    await wrapper.setProps({
      validatedSource: "{单价} * 3",
      validation: {
        canonicalSource: "f_price * 3",
        resultType: "number",
        dependencies: ["f_price"],
        relationAggregatePaths: [],
      },
    });
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-testid="formula-editor-commit"]').trigger("click");

    expect(wrapper.emitted("commit")).toEqual([[
      {
        language: "cel-v1",
        source: "{单价} * 3",
        resultType: "number",
      },
    ]]);
    expect(wrapper.find('[data-testid="formula-source"]').exists()).toBe(false);
    vi.useRealTimers();
  });

  it("通过选择器插入 Relation 聚合且不暴露物理名称", async () => {
    const wrapper = mountEditor();
    await wrapper.get('[data-testid="formula-editor-entry"]').trigger("click");
    const selects = wrapper.findAllComponents(NSelect);
    selects.find(select => select.attributes("data-testid") === "formula-relation-field")
      ?.vm.$emit("update:value", "f_lines");
    await wrapper.vm.$nextTick();
    selects.find(select => select.attributes("data-testid") === "formula-target-field")
      ?.vm.$emit("update:value", "f_amount");
    await wrapper.vm.$nextTick();
    await wrapper.findAll("button").find(button => button.text().includes("插入聚合"))
      ?.trigger("click");

    const source = wrapper.get('[data-testid="formula-source"]').find("textarea");
    expect((source.element as HTMLTextAreaElement).value).toContain("SUM({明细}.{金额})");
    expect((source.element as HTMLTextAreaElement).value).not.toContain("f_lines");
  });

  it("通过选择器插入单条 Relation 的目标字段", async () => {
    const wrapper = mountEditor();
    await wrapper.get('[data-testid="formula-editor-entry"]').trigger("click");
    const selects = wrapper.findAllComponents(NSelect);
    selects.find(select => select.attributes("data-testid") === "formula-direct-relation")
      ?.vm.$emit("update:value", "f_customer");
    await wrapper.vm.$nextTick();
    selects.find(select => select.attributes("data-testid") === "formula-direct-target")
      ?.vm.$emit("update:value", "f_credit");
    await wrapper.vm.$nextTick();
    await wrapper.findAll("button").find(button => button.text().includes("插入引用"))
      ?.trigger("click");

    const source = wrapper.get('[data-testid="formula-source"]').find("textarea");
    expect((source.element as HTMLTextAreaElement).value).toContain("{客户}.{信用额度}");
    expect((source.element as HTMLTextAreaElement).value).not.toContain("f_customer");
  });
});
