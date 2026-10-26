import { afterEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { NDropdown } from "naive-ui";
import DataSourceViewBar from "./DataSourceViewBar.vue";

describe("DataSourceViewBar", () => {
  afterEach(() => {
    document.body.innerHTML = "";
    localStorage.clear();
  });

  it("reorders tabs by dragging and keeps the preference within the workspace", async () => {
    const views = ["a", "b"].map(id => ({ id, collection: "orders", name: id, scope: "personal" as const,
      revision: id, emittedEvents: [], view: { layout: "table", filters: [], sorts: [], search: "", visibleFields: [] } }));
    const props = { collection: "orders", workspaceKey: "workspace-one", views, activeId: null, loading: false, dirty: false };
    const wrapper = mount(DataSourceViewBar, { props });
    await wrapper.findAll(".view-tab-wrap")[1]!.trigger("dragstart");
    await wrapper.findAll(".view-tab-wrap")[0]!.trigger("drop");
    expect(wrapper.findAll(".view-tab-wrap .view-tab").map(tab => tab.text())).toEqual(["b", "a"]);
    wrapper.unmount();
    const reopened = mount(DataSourceViewBar, { props });
    expect(reopened.findAll(".view-tab-wrap .view-tab").map(tab => tab.text())).toEqual(["b", "a"]);
    await reopened.get('[data-testid="view-tab-a"]').trigger("keydown", { key: "ArrowLeft", altKey: true });
    expect(reopened.findAll(".view-tab-wrap .view-tab").map(tab => tab.text())).toEqual(["a", "b"]);
    await reopened.get('[data-testid="view-tab-a"]').trigger("keydown", { key: "ArrowRight", altKey: true });
    expect(reopened.findAll(".view-tab-wrap .view-tab").map(tab => tab.text())).toEqual(["b", "a"]);
    await reopened.setProps({ workspaceKey: "workspace-two" });
    expect(reopened.findAll(".view-tab-wrap .view-tab").map(tab => tab.text())).toEqual(["a", "b"]);
    reopened.unmount();
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
    expect(wrapper.get('[data-testid="view-all-records"]').text()).toContain("完整视图");
  });

  it("keeps a failed local draft visible until the user explicitly reloads views", async () => {
    const wrapper = mount(DataSourceViewBar, {
      props: {
        collection: "orders",
        views: [],
        activeId: null,
        loading: false,
        dirty: true,
        error: "视图已在其他位置更新",
      },
    });

    expect(wrapper.get('[data-testid="view-operation-error"]').text())
      .toContain("视图已在其他位置更新");
    await wrapper.get('[data-testid="view-reload"]').trigger("click");
    expect(wrapper.emitted("reload")).toHaveLength(1);
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
    expect(wrapper.get('[data-testid="view-actions-view-1"]').attributes("aria-label"))
      .toContain("本月待办");
    await tab.trigger("click");
    expect(wrapper.emitted("switch")?.[0]?.[0]).toMatchObject({ id: "view-1" });
    await wrapper.get('[data-testid="view-all-records"]').trigger("click");
    expect(wrapper.emitted("reset")).toHaveLength(1);
    await wrapper.get('[data-testid="view-fields"]').trigger("click");
    expect(wrapper.emitted("fields")).toHaveLength(1);
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
    expect(document.body.querySelector('[data-testid="view-temporal-date-field"]')).toBeTruthy();
    expect(document.body.querySelector('[data-testid="view-temporal-title-field"]')).toBeTruthy();
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
      groupField: null,
      coverField: null,
    }]);
  });

  it("requires a grouping field and creates a kanban view", async () => {
    const wrapper = mount(DataSourceViewBar, {
      attachTo: document.body,
      props: {
        collection: "orders",
        views: [],
        activeId: null,
        loading: false,
        dirty: false,
        groupFields: [{ label: "状态", value: "status" }],
        titleFields: [{ label: "标题", value: "title" }],
      },
    });
    await wrapper.get('[data-testid="view-create"]').trigger("click");
    document.body.querySelector<HTMLElement>('[data-testid="view-kind-kanban"]')?.click();
    await wrapper.vm.$nextTick();
    expect(document.body.querySelector('[data-testid="view-kanban-group-field"]')).toBeTruthy();
    expect(document.body.querySelector('[data-testid="view-kanban-title-field"]')).toBeTruthy();
    const input = document.body.querySelector<HTMLInputElement>("input");
    input!.value = "任务看板";
    input!.dispatchEvent(new Event("input", { bubbles: true }));
    await wrapper.vm.$nextTick();
    document.body.querySelector<HTMLElement>('[data-testid="view-dialog-confirm"]')?.click();
    expect(wrapper.emitted("create")?.[0]).toEqual([{
      name: "任务看板",
      kind: "kanban",
      dateField: null,
      endDateField: null,
      titleField: "title",
      groupField: "status",
      coverField: null,
    }]);
  });

  it("creates a gallery view without guessing an optional cover field", async () => {
    const wrapper = mount(DataSourceViewBar, {
      attachTo: document.body,
      props: {
        collection: "assets",
        views: [],
        activeId: null,
        loading: false,
        dirty: false,
        coverFields: [{ label: "封面", value: "cover" }],
        titleFields: [{ label: "名称", value: "name" }],
      },
    });
    await wrapper.get('[data-testid="view-create"]').trigger("click");
    document.body.querySelector<HTMLElement>('[data-testid="view-kind-gallery"]')?.click();
    await wrapper.vm.$nextTick();
    expect(document.body.querySelector('[data-testid="view-gallery-cover-field"]')).toBeTruthy();
    expect(document.body.querySelector('[data-testid="view-gallery-title-field"]')).toBeTruthy();
    const input = document.body.querySelector<HTMLInputElement>("input");
    input!.value = "素材画册";
    input!.dispatchEvent(new Event("input", { bubbles: true }));
    await wrapper.vm.$nextTick();
    document.body.querySelector<HTMLElement>('[data-testid="view-dialog-confirm"]')?.click();
    expect(wrapper.emitted("create")?.[0]).toEqual([{
      name: "素材画册",
      kind: "gallery",
      dateField: null,
      endDateField: null,
      titleField: "name",
      groupField: null,
      coverField: null,
    }]);
  });

  it("emits the in-table import intents from the view bar", async () => {
    const wrapper = mount(DataSourceViewBar, {
      props: {
        collection: "orders",
        views: [],
        activeId: null,
        loading: false,
        dirty: false,
      },
    });
    const button = wrapper.get('[data-testid="table-import-data"]');
    expect(button.attributes("aria-label")).toBe("导入数据到当前表");
    await button.trigger("click");
    expect(wrapper.emitted("importData")).toHaveLength(1);

    const management = wrapper.get('[data-testid="table-import-management"]');
    expect(management.attributes("aria-label")).toBe("打开当前表的导入管理");
    await management.trigger("click");
    expect(wrapper.emitted("openImportManagement")).toHaveLength(1);

    await wrapper.setProps({ loading: true });
    // 视图列表加载不门控导入入口（导入不依赖预设列表）。
    expect(wrapper.get('[data-testid="table-import-data"]').attributes("disabled")).toBeUndefined();
    expect(wrapper.get('[data-testid="table-import-management"]').attributes("disabled")).toBeUndefined();
    await wrapper.setProps({ importDisabled: true });
    expect(wrapper.get('[data-testid="table-import-data"]').attributes("disabled")).toBeDefined();
    expect(wrapper.get('[data-testid="table-import-management"]').attributes("disabled")).toBeUndefined();
  });

  it("prefills a unique duplicate name and blocks same-table duplicate view names", async () => {
    const views = [
      { id: "v1", collection: "orders", name: "本月待办", scope: "personal" as const,
        revision: "r1", emittedEvents: [], view: { layout: "table", filters: [], sorts: [], search: "", visibleFields: [] } },
      { id: "v2", collection: "orders", name: "本月待办 副本", scope: "personal" as const,
        revision: "r2", emittedEvents: [], view: { layout: "table", filters: [], sorts: [], search: "", visibleFields: [] } },
    ];
    const wrapper = mount(DataSourceViewBar, {
      attachTo: document.body,
      props: { collection: "orders", views, activeId: null, loading: false, dirty: false },
    });
    // 打开 v1 的操作菜单选择“复制视图”。
    const menu = wrapper.findAllComponents(NDropdown)[0]!;
    (menu.props("onSelect") as (key: string) => void)("duplicate");
    await wrapper.vm.$nextTick();
    const input = document.body.querySelector<HTMLInputElement>("input");
    // 预填名与现有视图名冲突时自动避让（本月待办 副本 → 本月待办 副本 2）。
    expect(input?.value).toBe("本月待办 副本 2");

    // 手动改回已有名称：确认禁用、提示冲突，Enter 也不得提交。
    input!.value = "本月待办 副本";
    input!.dispatchEvent(new Event("input", { bubbles: true }));
    await wrapper.vm.$nextTick();
    expect(document.body.querySelector('[data-testid="view-name-conflict"]')).toBeTruthy();
    expect(
      (document.body.querySelector('[data-testid="view-dialog-confirm"]') as HTMLButtonElement)
        ?.disabled,
    ).toBe(true);
    input!.dispatchEvent(new KeyboardEvent("keyup", { key: "Enter", bubbles: true }));
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("duplicate")).toBeFalsy();

    // 改为可用名称后提交复制。
    input!.value = "本月待办 备份";
    input!.dispatchEvent(new Event("input", { bubbles: true }));
    await wrapper.vm.$nextTick();
    (
      document.body.querySelector('[data-testid="view-dialog-confirm"]') as HTMLButtonElement
    )?.click();
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("duplicate")?.[0]?.[1]).toBe("本月待办 备份");
  });

  it("keeps an unchanged own name valid while renaming a view", async () => {
    const views = [
      { id: "v1", collection: "orders", name: "本月待办", scope: "personal" as const,
        revision: "r1", emittedEvents: [], view: { layout: "table", filters: [], sorts: [], search: "", visibleFields: [] } },
    ];
    const wrapper = mount(DataSourceViewBar, {
      attachTo: document.body,
      props: { collection: "orders", views, activeId: null, loading: false, dirty: false },
    });
    const menu = wrapper.findAllComponents(NDropdown)[0]!;
    (menu.props("onSelect") as (key: string) => void)("rename");
    await wrapper.vm.$nextTick();
    expect(document.body.querySelector('[data-testid="view-name-conflict"]')).toBeNull();
    expect(
      (document.body.querySelector('[data-testid="view-dialog-confirm"]') as HTMLButtonElement)
        ?.disabled,
    ).toBe(false);
  });
});
