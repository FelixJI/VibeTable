import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { defineComponent, h } from "vue";
import { NSelect } from "naive-ui";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import { provideSurfaceService, useSurfaceService } from "@/services/surfaceService";
import {
  InMemorySurfaceRepository,
  type SurfaceRepository,
} from "@/surfaces/surfaceCore";
import InterfaceWorkspaceView from "./InterfaceWorkspaceView.vue";
import InterfaceBindingEditor from "@/components/surfaces/InterfaceBindingEditor.vue";

function bridge(): HostBridge {
  return {
    request: vi.fn(async () => { throw new Error("unexpected host request"); }),
    requestWithHandle: vi.fn(),
    notify: vi.fn(),
  } as unknown as HostBridge;
}

function mountView(repository: SurfaceRepository = new InMemorySurfaceRepository()) {
  const service = useSurfaceService(repository);
  const Host = defineComponent({
    setup() {
      provideSurfaceService(service);
      return () => h(InterfaceWorkspaceView);
    },
  });
  const wrapper = mount(Host, {
    attachTo: document.body,
    global: { plugins: [createPinia()] },
  });
  return { wrapper, service };
}

describe("InterfaceWorkspaceView", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    setActivePinia(createPinia());
    setHostBridgeForTesting(bridge());
    vi.spyOn(crypto, "randomUUID").mockReturnValueOnce("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa");
  });

  afterEach(() => {
    setHostBridgeForTesting(null);
    document.body.innerHTML = "";
    vi.restoreAllMocks();
  });

  it("builds nested pages, changes preview modes, saves, discards, reopens, and deletes", async () => {
    const prompts = vi.spyOn(window, "prompt")
      .mockReturnValueOnce("运营工作台")
      .mockReturnValueOnce("详情页");
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    const { wrapper, service } = mountView();
    await flushPromises();

    expect(wrapper.text()).toContain("还没有界面");
    await wrapper.get('[data-testid="interface-create-empty"]').trigger("click");
    expect(prompts).toHaveBeenCalled();
    expect(service.state.value.draft?.name).toBe("运营工作台");

    const palette = wrapper.findAll(".palette-panel > button");
    await palette.find((button) => button.text() === "分区")!.trigger("click");
    const section = wrapper.get(".builder-element--section");
    await section.get('button[aria-label="添加子元素"]').trigger("click");
    await wrapper.findAll(".palette-panel > button").find((button) => button.text() === "文本")!.trigger("click");
    expect(wrapper.findAll(".builder-element")).toHaveLength(2);

    await wrapper.get('button[aria-label="添加页面"]').trigger("click");
    expect(service.state.value.draft?.pages).toHaveLength(2);
    await wrapper.get('button[aria-label="删除当前页面"]').trigger("click");
    expect(service.state.value.draft?.pages).toHaveLength(1);

    await wrapper.get('button[aria-label="平板预览"]').trigger("click");
    expect(wrapper.get(".builder-canvas").classes()).toContain("preview-tablet");
    await wrapper.get('button[aria-label="手机预览"]').trigger("click");
    expect(wrapper.get(".builder-canvas").classes()).toContain("preview-mobile");

    await wrapper.get('[data-testid="interface-save"]').trigger("click");
    await flushPromises();
    expect(service.state.value.dirty).toBe(false);
    expect(service.list.value).toHaveLength(1);

    await wrapper.get<HTMLInputElement>(".surface-name input").setValue("未保存名称");
    await wrapper.vm.$nextTick();
    expect(service.state.value.dirty).toBe(true);
    await wrapper.get('button[aria-label="放弃更改"]').trigger("click");
    await flushPromises();
    expect(service.state.value.draft?.name).toBe("运营工作台");

    await wrapper.get(`[data-testid="interface-select-${service.state.value.draft?.interfaceId}"]`).trigger("click");
    await flushPromises();
    expect(service.state.value.phase).toBe("ready");
    await wrapper.get('button[aria-label="删除界面"]').trigger("click");
    await flushPromises();
    expect(confirm).toHaveBeenCalled();
    expect(service.list.value).toEqual([]);
    expect(service.state.value.draft).toBeNull();
    wrapper.unmount();
  });

  it("retains a dirty draft on CAS conflict and reloads the authoritative revision", async () => {
    const repository = new InMemorySurfaceRepository();
    const { wrapper, service } = mountView(repository);
    await flushPromises();
    vi.spyOn(window, "prompt").mockReturnValue("共享界面");

    await wrapper.get('[data-testid="interface-create-empty"]').trigger("click");
    await wrapper.get('[data-testid="interface-save"]').trigger("click");
    await flushPromises();
    const initial = service.state.value;
    expect(initial.draft).not.toBeNull();
    await repository.commit({
      definition: { ...initial.draft!, name: "外部版本" },
      expectedRevision: initial.revision,
      idempotencyKey: "external-commit",
    }, new AbortController().signal);

    service.dispatch({ type: "rename", name: "本地草稿" });
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-testid="interface-save"]').trigger("click");
    await flushPromises();
    expect(wrapper.get('[data-testid="interface-conflict"]').text()).toContain("草稿仍保留");
    expect(service.state.value.draft?.name).toBe("本地草稿");

    const reload = wrapper.get('[data-testid="interface-reload-conflict"]');
    expect(reload.isVisible()).toBe(true);
    await reload.trigger("click");
    await flushPromises();
    expect(service.state.value.draft?.name).toBe("外部版本");
    expect(service.state.value.dirty).toBe(false);
    wrapper.unmount();
  });

  it("surfaces list failures and respects the dirty navigation guard", async () => {
    const repository: SurfaceRepository = {
      list: async () => { throw "surface.offline"; },
      load: async () => { throw new Error("not used"); },
      commit: async () => { throw new Error("not used"); },
      delete: async () => { throw new Error("not used"); },
    };
    const { wrapper, service } = mountView(repository);
    await flushPromises();
    expect(wrapper.text()).toContain("surface.offline");

    vi.spyOn(window, "prompt").mockReturnValue("离线草稿");
    await wrapper.get('[data-testid="interface-create-empty"]').trigger("click");
    expect(service.state.value.dirty).toBe(true);
    wrapper.unmount();
  });

  it("authors every element kind and cleans binding/action references through the visual builder", async () => {
    const { wrapper, service } = mountView();
    await flushPromises();
    service.create("完整界面");
    await wrapper.vm.$nextTick();

    const expectedKinds = [
      "section", "columns", "tabs", "text", "metric", "chart",
      "record-list", "record-detail", "form", "button", "navigation",
    ] as const;
    for (const kind of expectedKinds) {
      await wrapper.get(`[data-testid="interface-add-${kind}"]`).trigger("click");
      expect(service.state.value.draft?.pages[0]?.elements.at(-1)?.kind).toBe(kind);
    }
    expect(wrapper.findAll(".builder-element")).toHaveLength(expectedKinds.length);
    expect(wrapper.find(".inspector-panel").text()).toContain("导航");

    const current = service.state.value.draft!;
    service.replace({
      ...current,
      bindings: [{
        bindingId: "orders",
        query: {
          contractVersion: "1.0", tableId: "orders", fields: ["id", "status"],
          filters: [], sorts: [], cursor: null, pageSize: 100,
        },
        variables: [],
      }],
      actions: [
        { actionId: "create", kind: "record.create", bindingId: "orders", targetPageId: null, pluginId: null, pluginActionId: null, requiresConfirmation: false },
        { actionId: "update", kind: "record.update", bindingId: "orders", targetPageId: null, pluginId: null, pluginActionId: null, requiresConfirmation: true },
        { actionId: "refresh", kind: "binding.refresh", bindingId: "orders", targetPageId: null, pluginId: null, pluginActionId: null, requiresConfirmation: false },
        { actionId: "navigate", kind: "navigate", bindingId: null, targetPageId: current.pages[0]!.pageId, pluginId: null, pluginActionId: null, requiresConfirmation: false },
        { actionId: "plugin", kind: "plugin", bindingId: null, targetPageId: null, pluginId: "demo", pluginActionId: "run", requiresConfirmation: true },
      ],
      pages: current.pages.map((page) => ({ ...page, elements: [] })),
    });
    await wrapper.vm.$nextTick();
    expect(wrapper.findAll(".definition-chip")).toHaveLength(5);
    expect(wrapper.findComponent(InterfaceBindingEditor).exists()).toBe(true);

    const actionType = wrapper.findAll("label").find((label) => label.text().startsWith("类型"))!;
    for (const kind of ["record.update", "navigate", "plugin"] as const) {
      actionType.findComponent(NSelect).vm.$emit("update:value", kind);
      await wrapper.vm.$nextTick();
      if (kind === "record.update") expect(wrapper.find(".inspector-panel").text()).toContain("绑定");
      if (kind === "navigate") expect(wrapper.find(".inspector-panel").text()).toContain("目标页面");
      if (kind === "plugin") expect(wrapper.find(".inspector-panel").text()).toContain("插件动作");
    }

    wrapper.findComponent(InterfaceBindingEditor).vm.$emit("remove", "orders");
    await wrapper.vm.$nextTick();
    expect(service.state.value.draft?.bindings).toEqual([]);
    expect(service.state.value.draft?.actions.map((action) => action.kind)).toEqual(["navigate", "plugin"]);

    await wrapper.get('[data-testid="interface-run"]').trigger("click");
    expect(wrapper.find(".runtime-stage").exists()).toBe(true);
    await wrapper.findAll("button").find((button) => button.text().includes("构建"))!.trigger("click");
    await wrapper.get('button[aria-label="桌面预览"]').trigger("click");
    expect(wrapper.get(".builder-canvas").classes()).toContain("preview-desktop");
    wrapper.unmount();
  });

  it("treats cancelled create/page prompts and a declined delete as no-ops", async () => {
    vi.spyOn(window, "prompt")
      .mockReturnValueOnce("   ")
      .mockReturnValueOnce("保留界面")
      .mockReturnValueOnce("   ");
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const { wrapper, service } = mountView();
    await flushPromises();
    await wrapper.get('[data-testid="interface-create-empty"]').trigger("click");
    expect(service.state.value.draft).toBeNull();
    await wrapper.get('[data-testid="interface-create-empty"]').trigger("click");
    await wrapper.get('[data-testid="interface-save"]').trigger("click");
    await flushPromises();
    await wrapper.get('button[aria-label="添加页面"]').trigger("click");
    expect(service.state.value.draft?.pages).toHaveLength(1);
    await wrapper.get('button[aria-label="删除界面"]').trigger("click");
    expect(confirm).toHaveBeenCalledOnce();
    expect(service.state.value.draft?.name).toBe("保留界面");
    wrapper.unmount();
  });
});
