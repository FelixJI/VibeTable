import { afterEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import DataSourceViewBar from "./DataSourceViewBar.vue";

describe("DataSourceViewBar", () => {
  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("shows the built-in default field view when no preset exists", () => {
    const wrapper = mount(DataSourceViewBar, {
      props: {
        collection: "orders",
        views: [],
        activeId: null,
        loading: false,
        dirty: false,
      },
    });
    expect(wrapper.get('[data-testid="view-all-records"]').text()).toContain("默认字段视图");
  });

  it("renders collection views as first-class tabs and emits switching", async () => {
    const wrapper = mount(DataSourceViewBar, {
      props: {
        collection: "orders",
        activeId: "view-1",
        loading: false,
        dirty: true,
        views: [{
          id: "view-1",
          collection: "orders",
          name: "本月待办",
          scope: "personal",
          view: {
            kind: "table",
            layout: "table",
            filters: [],
            sorts: [],
            search: "",
            visibleFields: [],
            isDefault: true,
          },
          revision: "rev-1",
          emittedEvents: [],
        }],
      },
    });
    const tab = wrapper.get('[data-testid="view-tab-view-1"]');
    expect(tab.text()).toContain("本月待办");
    await tab.trigger("click");
    expect(wrapper.emitted("switch")?.[0]?.[0]).toMatchObject({ id: "view-1" });
  });

  it("creates a named calendar view through the compact dialog", async () => {
    const wrapper = mount(DataSourceViewBar, {
      attachTo: document.body,
      props: {
        collection: "orders",
        views: [],
        activeId: null,
        loading: false,
        dirty: false,
        dateFields: [{ label: "开始时间", value: "startsAt" }],
        titleFields: [{ label: "标题", value: "title" }],
      },
    });
    await wrapper.get('[data-testid="view-create"]').trigger("click");
    document.body.querySelector<HTMLElement>('[data-testid="view-kind-calendar"]')?.click();
    await wrapper.vm.$nextTick();
    const input = document.body.querySelector<HTMLInputElement>("input");
    expect(input).toBeTruthy();
    input!.value = "库存预警";
    input!.dispatchEvent(new Event("input", { bubbles: true }));
    await wrapper.vm.$nextTick();
    document.body.querySelector<HTMLElement>('[data-testid="view-dialog-confirm"]')?.click();
    expect(wrapper.emitted("create")?.[0]).toEqual([{
      name: "库存预警",
      kind: "calendar",
      dateField: "startsAt",
      endDateField: null,
      titleField: "title",
    }]);
  });
});
