import { afterEach, describe, expect, it } from "vitest";
import { mount, type VueWrapper } from "@vue/test-utils";
import { NSelect } from "naive-ui";
import LookupFieldEditor from "./LookupFieldEditor.vue";

const mounted: VueWrapper[] = [];

function mountEditor(): VueWrapper {
  const wrapper = mount(LookupFieldEditor, {
    props: {
      value: {
        path: [{ relationFieldId: "fld_customer" }],
        targetFieldId: "fld_name",
      },
      relationOptions: [[
        { label: "客户", value: "fld_customer" },
        { label: "账户", value: "fld_account", many: true },
      ]],
      targetFieldOptions: [
        { label: "名称", value: "fld_name" },
        { label: "余额", value: "fld_balance" },
      ],
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
    expect(wrapper.text()).toContain("客户");
    expect(wrapper.text()).toContain("名称");
    expect(wrapper.text()).not.toContain("fld_name");
    expect(wrapper.find('[data-testid="lookup-relation-step-0"]').exists()).toBe(false);

    await wrapper.get('[data-testid="lookup-editor-entry"]').trigger("click");
    const selects = wrapper.findAllComponents(NSelect);
    selects[0]!.vm.$emit("update:value", "fld_account");
    selects[1]!.vm.$emit("update:value", "fld_balance");
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-testid="lookup-editor-commit"]').trigger("click");

    expect(wrapper.emitted("commit")).toEqual([[
      {
        path: [{ relationFieldId: "fld_account" }],
        targetFieldId: "fld_balance",
      },
    ]]);
  });

  it("取消编辑时保留原始路径，路径不完整时禁止确认", async () => {
    const wrapper = mountEditor();
    await wrapper.get('[data-testid="lookup-editor-entry"]').trigger("click");
    wrapper.findAllComponents(NSelect)[0]!.vm.$emit("update:value", "");
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-testid="lookup-editor-commit"]').attributes("disabled"))
      .toBeDefined();

    await wrapper.get('[data-testid="lookup-editor-cancel"]').trigger("click");
    expect(wrapper.emitted("commit")).toBeUndefined();
    expect(wrapper.text()).toContain("客户");
  });
});
