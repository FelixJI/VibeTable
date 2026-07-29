import { afterEach, describe, expect, it } from "vitest";
import { mount, type VueWrapper } from "@vue/test-utils";
import { NSelect } from "naive-ui";
import LookupFieldEditor from "./LookupFieldEditor.vue";

const mounted: VueWrapper[] = [];

function mountEditor(): VueWrapper {
  const wrapper = mount(LookupFieldEditor, {
    props: {
      value: {
        relationFieldId: "fld_customer",
        targetFieldId: "fld_name",
        aggregate: "first",
        resultType: "text",
      },
    },
  });
  mounted.push(wrapper);
  return wrapper;
}

afterEach(() => {
  mounted.splice(0).forEach(wrapper => wrapper.unmount());
});

describe("LookupFieldEditor", () => {
  it("通过专用入口展示并编辑结构化引用路径", async () => {
    const wrapper = mountEditor();
    expect(wrapper.text()).toContain("fld_customer");
    expect(wrapper.text()).toContain("fld_name");
    expect(wrapper.find('[data-testid="lookup-relation-field"]').exists()).toBe(false);

    await wrapper.get('[data-testid="lookup-editor-entry"]').trigger("click");
    await wrapper.get('[data-testid="lookup-relation-field"]').find("input")
      .setValue("fld_account");
    await wrapper.get('[data-testid="lookup-target-field"]').find("input")
      .setValue("fld_balance");

    const selects = wrapper.findAllComponents(NSelect);
    selects[0]!.vm.$emit("update:value", "sum");
    selects[1]!.vm.$emit("update:value", "number");
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-testid="lookup-editor-commit"]').trigger("click");

    expect(wrapper.emitted("commit")).toEqual([[
      {
        relationFieldId: "fld_account",
        targetFieldId: "fld_balance",
        aggregate: "sum",
        resultType: "number",
      },
    ]]);
  });

  it("取消编辑时保留原始路径，路径不完整时禁止确认", async () => {
    const wrapper = mountEditor();
    await wrapper.get('[data-testid="lookup-editor-entry"]').trigger("click");
    await wrapper.get('[data-testid="lookup-relation-field"]').find("input").setValue("");
    expect(wrapper.get('[data-testid="lookup-editor-commit"]').attributes("disabled"))
      .toBeDefined();

    await wrapper.get('[data-testid="lookup-editor-cancel"]').trigger("click");
    expect(wrapper.emitted("commit")).toBeUndefined();
    expect(wrapper.text()).toContain("fld_customer");
  });
});
