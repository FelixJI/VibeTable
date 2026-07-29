import { afterEach, describe, expect, it } from "vitest";
import { mount, type VueWrapper } from "@vue/test-utils";
import { NSelect } from "naive-ui";
import FormulaFieldEditor from "./FormulaFieldEditor.vue";

const mounted: VueWrapper[] = [];

function mountEditor(): VueWrapper {
  const wrapper = mount(FormulaFieldEditor, {
    props: {
      value: {
        language: "cel-v1",
        source: "record.price * 2",
        resultType: "number",
      },
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
    expect(wrapper.text()).toContain("record.price * 2");
    expect(wrapper.find('[data-testid="formula-source"]').exists()).toBe(false);

    await wrapper.get('[data-testid="formula-editor-entry"]').trigger("click");
    await wrapper.get('[data-testid="formula-source"]').find("textarea")
      .setValue("record.price * 3");
    await wrapper.get('[data-testid="formula-editor-cancel"]').trigger("click");

    expect(wrapper.emitted("commit")).toBeUndefined();
    expect(wrapper.text()).toContain("record.price * 2");
  });

  it("只在确认时提交完整的结构化公式定义", async () => {
    const wrapper = mountEditor();
    await wrapper.get('[data-testid="formula-editor-entry"]').trigger("click");
    await wrapper.get('[data-testid="formula-source"]').find("textarea")
      .setValue("  record.price * record.quantity  ");
    wrapper.findComponent(NSelect).vm.$emit("update:value", "text");
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-testid="formula-editor-commit"]').trigger("click");

    expect(wrapper.emitted("commit")).toEqual([[
      {
        language: "cel-v1",
        source: "record.price * record.quantity",
        resultType: "text",
      },
    ]]);
    expect(wrapper.find('[data-testid="formula-source"]').exists()).toBe(false);
  });
});
