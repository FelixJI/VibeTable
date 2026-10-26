import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import HeaderFilterMenu from "./HeaderFilterMenu.vue";

const pageOne = [
  { key: "s:a", label: "A", rawValue: "a", count: 2 },
  { key: "null", label: "（空白）", rawValue: null, count: 1 },
];
const pageTwo = [...pageOne, { key: "s:b", label: "B", rawValue: "b", count: 1 }];

function open(options: typeof pageOne, selection: readonly string[] | null = null) {
  return mount(HeaderFilterMenu, {
    props: {
      fieldTitle: "备注",
      options,
      selection,
      status: "ready",
      hasMore: true,
    },
  });
}

describe("HeaderFilterMenu", () => {
  it("toggles exactly once when clicking real Naive checkbox text or its whole option row", async () => {
    const wrapper=open(pageOne);
    const option=wrapper.get('[data-testid="vt-header-menu-option-s:a"]');
    await option.get(".vt-header-menu__option-label").trigger("click");
    expect(option.attributes("aria-checked")).toBe("false");
    await wrapper.get('[data-testid="vt-header-menu-apply"]').trigger("click");
    expect(wrapper.emitted("apply")?.[0]).toEqual([["null"]]);
    await option.trigger("click");
    expect(option.attributes("aria-checked")).toBe("true");
    await option.get("small").trigger("click");
    expect(option.attributes("aria-checked")).toBe("false");
    wrapper.unmount();
  });
  it("分页追加不重置草稿：初始全选态新增项默认勾选", async () => {
    const wrapper = open(pageOne);
    await wrapper.setProps({ options: pageTwo });
    const boxes = wrapper.findAll(".n-checkbox");
    expect(boxes.map(box => box.attributes("aria-checked")))
      .toEqual(["true", "true", "true"]);
    expect(wrapper.text()).toContain("已勾选全部已加载值，仍有更多选项");
    expect(wrapper.text()).not.toContain("未筛选（全部值）");
    wrapper.unmount();
  });

  it("加载更多后保留用户已取消的勾选；新增项默认不勾选", async () => {
    const wrapper = open(pageOne);
    await wrapper.findAll(".n-checkbox")[0]!.trigger("click");
    expect(wrapper.findAll(".n-checkbox")[0]!.attributes("aria-checked")).toBe("false");

    await wrapper.setProps({ options: pageTwo, loadingMore: false });
    const states = wrapper.findAll(".n-checkbox").map(box => box.attributes("aria-checked"));
    // 用户取消的 A 仍为取消；新增 B 默认不勾选；空白保持初始勾选。
    expect(states).toEqual(["false", "true", "false"]);
    wrapper.unmount();
  });

  it("全不选后应用禁用并提示；清除筛选独立提交", async () => {
    const wrapper = open(pageOne, ["s:a"]);
    await wrapper.get('[data-testid="vt-header-menu-none"]').trigger("click");
    expect((wrapper.get('[data-testid="vt-header-menu-apply"]').element as HTMLButtonElement).disabled)
      .toBe(true);
    expect(wrapper.find('[data-testid="vt-header-menu-empty-hint"]').exists()).toBe(true);

    await wrapper.get('[data-testid="vt-header-menu-clear"]').trigger("click");
    expect(wrapper.emitted("clear")).toHaveLength(1);
    wrapper.unmount();
  });

  it("搜索按空格分词 OR 过滤已加载选项，且不创建条件", async () => {
    const wrapper = open(pageTwo);
    await wrapper.get('[data-testid="vt-header-menu-search"] input').setValue("空白 b");
    const labels = wrapper.findAll(".vt-header-menu__option-label")
      .map(span => span.text());
    expect(labels).toEqual(["（空白）", "B"]);
    expect(wrapper.emitted("apply")).toBeUndefined();
    wrapper.unmount();
  });

  it("blocks over-budget submission with an actionable message until the selection is reduced", async () => {
    const wrapper=open(pageOne);
    await wrapper.setProps({validateSelection:(keys:readonly string[])=>keys.length>1?"请减少勾选，筛选条件最多 50 条。":null});
    expect((wrapper.get('[data-testid="vt-header-menu-apply"]').element as HTMLButtonElement).disabled).toBe(true);
    expect(wrapper.get('[role="alert"]').text()).toContain("50");
    await wrapper.get('[data-testid="vt-header-menu-apply"]').trigger("click");
    expect(wrapper.emitted("apply")).toBeUndefined();
    await wrapper.findAll(".n-checkbox")[0]!.trigger("click");
    expect((wrapper.get('[data-testid="vt-header-menu-apply"]').element as HTMLButtonElement).disabled).toBe(false);
    wrapper.unmount();
  });
});
