import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { HostBridge } from "@/bridge/hostBridge";
import type { PluginInteractionSnapshot, PluginSnapshot, PluginTaskSnapshot } from "@/contracts";
import type {
  InterfaceAction,
  InterfaceDefinition,
  InterfaceElement,
} from "@/contracts/generated/workbench";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import { usePluginStore } from "@/stores/pluginStore";
import InterfaceRuntime from "./InterfaceRuntime.vue";

const plugin: PluginSnapshot = {
  projectKey: "local:default",
  pluginId: "com.acme.clean",
  version: "1.2.0",
  packageHash: "sha256:abc",
  sourceType: "package",
  sourceLocation: "clean.vtplugin",
  manifest: {
    $schema: "vibetable.plugin-manifest.v1",
    pluginId: "com.acme.clean",
    version: "1.2.0",
    displayName: { "zh-CN": "清理数据" },
    description: {}, compatibility: {}, permissions: {},
    actions: [{
      actionId: "normalize",
      displayName: { "zh-CN": "规范化" },
      description: {}, mode: "local", risk: "write", invocation: "manual",
      placements: ["table.toolbar"], requires: {}, workerEntry: "dist/worker.js",
      formSchema: null, inputSchema: null, outputSchema: null,
    }],
    ui: {},
  },
  schemas: {},
  status: "enabled",
  disabledReason: null,
  revision: 1,
};

const element = (
  elementId: string,
  kind: InterfaceElement["kind"],
  patch: Partial<InterfaceElement> = {},
): InterfaceElement => ({
  elementId,
  kind,
  bindingId: null,
  actionId: null,
  text: null,
  width: "full",
  children: [],
  ...patch,
});

const action = (
  actionId: string,
  kind: InterfaceAction["kind"],
  patch: Partial<InterfaceAction> = {},
): InterfaceAction => ({
  actionId,
  kind,
  bindingId: null,
  targetPageId: null,
  pluginId: null,
  pluginActionId: null,
  requiresConfirmation: false,
  ...patch,
});

function definition(): InterfaceDefinition {
  return {
    contractVersion: "1.0",
    interfaceId: "if-orders",
    name: "订单工作台",
    bindings: [
      binding("orders", "orders", ["name", "status"]),
      binding("hidden", "customers", ["name"]),
    ],
    actions: [
      action("create", "record.create", { bindingId: "orders" }),
      action("update", "record.update", { bindingId: "orders" }),
      action("refresh", "binding.refresh", { bindingId: "orders" }),
      action("navigate", "navigate", { targetPageId: "details" }),
    ],
    pages: [
      {
        pageId: "list",
        title: "订单",
        elements: [
          element("records", "record-list", { bindingId: "orders", text: "订单" }),
          element("update-form", "form", {
            bindingId: "orders",
            actionId: "update",
            text: "更新订单",
          }),
          element("create-form", "form", {
            bindingId: "orders",
            actionId: "create",
            text: "创建订单",
          }),
          element("refresh-button", "button", { actionId: "refresh", text: "刷新" }),
          element("navigate-button", "navigation", { actionId: "navigate", text: "详情" }),
        ],
      },
      {
        pageId: "details",
        title: "详情",
        elements: [element("hidden-detail", "record-detail", { bindingId: "hidden" })],
      },
    ],
  };
}

function binding(bindingId: string, tableId: string, fields: string[]) {
  return {
    bindingId,
    query: {
      contractVersion: "1.0" as const, tableId, fields,
      filters: [], sorts: [], cursor: null, pageSize: 100,
    },
    variables: [],
  };
}

function host(options: { failUpdates?: boolean } = {}): {
  bridge: HostBridge;
  request: ReturnType<typeof vi.fn>;
  query: ReturnType<typeof vi.fn>;
  notify: ReturnType<typeof vi.fn>;
} {
  let digest = 1;
  const query = vi.fn(async (payload: Record<string, unknown>) => {
    const input = payload.query as { offset?: number; limit?: number };
    const allRows = [
      { id: "r1", name: "North", status: "open", __vibetableDigest: "d1" },
      { rowKey: "r2", name: "South", status: "closed", __vibetableDigest: "d2" },
    ];
    const offset = input.offset ?? 0;
    const limit = input.limit ?? 100;
    return {
      rows: allRows.slice(offset, offset + limit),
      nextCursor: offset + limit < allRows.length ? `${payload.tableId}:${offset + limit}` : null,
      hasMore: offset + limit < allRows.length,
      filteredRows: allRows.length,
      totalRows: allRows.length,
      querySnapshot: { dataRevision: 1, schemaRevision: "schema-1" },
    };
  });
  const request = vi.fn(async (type: string, payload: Record<string, unknown>) => {
    if (type === "query.cursorOpen") return query(payload);
    if (type === "query.cursorFetch") {
      const [tableId, rawOffset] = String(payload.cursor).split(":");
      return query({ tableId, query: { offset: Number(rawOffset), limit: 1 } });
    }
    if (type === "schema.describe") {
      return {
        collection: payload.collection,
        requestGeneration: payload.requestGeneration,
        schema: {
          schemaRevision: "schema-1",
          columns: [
            { name: "name", fieldId: "name", title: "名称", dataType: "text" },
            { name: "status", fieldId: "status", title: "状态", dataType: "text" },
          ],
        },
      };
    }
    if (type === "table.insertRowRequested") return { recordId: "r3" };
    if (type === "table.updateCellRequested") {
      if (options.failUpdates) throw new Error("record.conflict");
      digest += 1;
      return {
        currentRow: {
          rowKey: payload.rowKey,
          name: payload.column === "name" ? payload.newValue : "South",
          status: payload.column === "status" ? payload.newValue : "closed",
          __vibetableDigest: `d${digest}`,
        },
      };
    }
    throw new Error(`unexpected request: ${type}`);
  });
  const notify = vi.fn();
  const bridge = {
    request,
    requestWithHandle: vi.fn(() => ({
      requestId: `query-${query.mock.calls.length + 1}`,
      promise: query({ query: {} }),
    })),
    notify,
  } as unknown as HostBridge;
  return { bridge, request, query, notify };
}

function pluginDefinition(): InterfaceDefinition {
  const current = definition();
  return {
    ...current,
    actions: [...current.actions, action("normalize", "plugin", {
      pluginId: plugin.pluginId,
      pluginActionId: "normalize",
      requiresConfirmation: true,
    })],
    pages: [{
      ...current.pages[0]!,
      elements: [...current.pages[0]!.elements, element("plugin", "button", {
        bindingId: "orders",
        actionId: "normalize",
        text: "规范化",
      })],
    }, current.pages[1]!],
  };
}

function runningPluginTask(): PluginTaskSnapshot {
  return {
    taskId: "task-interface",
    runId: "run-interface",
    pluginId: plugin.pluginId,
    pluginVersion: plugin.version,
    actionId: "normalize",
    projectKey: plugin.projectKey,
    collection: "orders",
    targetCount: 1,
    risk: "write",
    state: "running",
    cancelRequested: false,
    result: null,
    error: null,
  };
}

function pluginInteraction(): PluginInteractionSnapshot {
  return {
    runId: "run-interface",
    projectKey: plugin.projectKey,
    pluginId: plugin.pluginId,
    actionId: "normalize",
    caller: "interface",
    progress: { current: 1, total: 2, message: "等待确认", cancellable: true },
    pendingConfirmation: {
      interactionId: "confirm-interface",
      risk: "write",
      title: "确认规范化",
      preview: {
        summary: [{ operation: "normalize" }], sampleRows: [], affectedCount: 1, warnings: [],
      },
      expiresAt: Math.floor(Date.now() / 1000) + 60,
    },
    cancelRequested: false,
  };
}

describe("InterfaceRuntime", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
  });

  afterEach(() => {
    setHostBridgeForTesting(null);
    vi.restoreAllMocks();
  });

  it("loads only visible bindings and executes create, update, refresh, and navigation actions", async () => {
    const h = host();
    setHostBridgeForTesting(h.bridge);
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    const wrapper = mount(InterfaceRuntime, {
      props: { definition: definition(), activePageId: "list", previewWidth: "tablet" },
      global: { plugins: [createPinia()] },
    });
    await flushPromises();

    expect(wrapper.get('[data-testid="interface-runtime"]').classes()).toContain("preview-tablet");
    expect(h.query).toHaveBeenCalledTimes(1);
    expect(h.request).toHaveBeenCalledWith("query.cursorOpen", expect.objectContaining({
      tableId: "orders",
      query: { filters: [], sorts: [], offset: 0, limit: 100 },
    }));
    expect(wrapper.text()).toContain("North");
    expect(wrapper.text()).not.toContain("customers");

    await wrapper.findAll("button.record-row")[1]!.trigger("click");
    const updateForm = wrapper.get('[data-testid="interface-runtime-update-form"]');
    await updateForm.findAll("input")[0]!.setValue("South updated");
    await updateForm.get("form").trigger("submit");
    await flushPromises();
    const updates = h.request.mock.calls.filter(([type]) => type === "table.updateCellRequested");
    expect(updates).toHaveLength(2);
    expect(updates[0]?.[1]).toMatchObject({ rowKey: "r2", column: "name", newValue: "South updated" });
    expect(wrapper.text()).toContain("操作已完成");

    const createForm = wrapper.get('[data-testid="interface-runtime-create-form"]');
    await createForm.findAll("input")[0]!.setValue("West");
    await createForm.get("form").trigger("submit");
    await flushPromises();
    expect(h.request).toHaveBeenCalledWith("table.insertRowRequested", expect.objectContaining({
      table: "orders",
      values: expect.objectContaining({ name: "West" }),
      schemaRevision: "schema-1",
    }));

    await wrapper.get('[data-testid="interface-runtime-refresh-button"] button').trigger("click");
    await flushPromises();
    expect(h.query.mock.calls.length).toBeGreaterThanOrEqual(4);

    await wrapper.get('[data-testid="interface-runtime-navigate-button"] button').trigger("click");
    await flushPromises();
    expect(wrapper.emitted("navigate")?.at(-1)).toEqual(["details"]);
    expect(confirm).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("keeps a plugin action running until its broker confirmation is approved", async () => {
    const pinia = createPinia();
    setActivePinia(pinia);
    usePluginStore().setProjectContext("local:default", "interface-session:1");
    const h = host();
    let currentTask = runningPluginTask();
    const request = vi.fn(async (type: string, payload: Record<string, unknown>) => {
      if (type === "plugin.action.describe") return { available: true, reasons: [] };
      if (type === "plugin.action.start") return currentTask;
      if (type === "plugin.task.get") return currentTask;
      if (type === "plugin.interaction.resolve") {
        currentTask = { ...currentTask, state: "succeeded" };
        return { status: "resolved", decision: payload.decision };
      }
      return (h.request as unknown as (
        type: string, payload: Record<string, unknown>,
      ) => Promise<unknown>)(type, payload);
    });
    setHostBridgeForTesting({ ...h.bridge, request } as HostBridge);
    const plugins = usePluginStore();
    plugins.applyPlugin(plugin);
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const wrapper = mount(InterfaceRuntime, {
      props: { definition: pluginDefinition(), activePageId: "list", previewWidth: "desktop" },
      global: { plugins: [pinia] },
    });
    await flushPromises();

    await wrapper.get('[data-testid="interface-runtime-plugin"] button').trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("正在执行操作");
    expect(wrapper.text()).not.toContain("操作已完成");

    plugins.applyInteraction(pluginInteraction(), 2);
    await flushPromises();
    expect(wrapper.get('[data-testid="plugin-confirmation"]').text()).toContain("确认规范化");
    await wrapper.get('[data-testid="plugin-confirm-approve"]').trigger("click");
    await vi.waitFor(() => expect(wrapper.text()).toContain("操作已完成"));
    expect(request).toHaveBeenCalledWith("plugin.interaction.resolve", {
      runId: "run-interface", interactionId: "confirm-interface", decision: "approved",
    });
    expect(request).toHaveBeenCalledWith("plugin.action.start", expect.objectContaining({
      pluginId: plugin.pluginId,
      actionId: "normalize",
      input: {},
      context: expect.objectContaining({ collection: "orders", selectedKeys: ["r1"] }),
    }));
    wrapper.unmount();
  });

  it("projects broker rejection as a rejected Interface action", async () => {
    const pinia = createPinia();
    setActivePinia(pinia);
    usePluginStore().setProjectContext("local:default", "interface-session:1");
    const h = host();
    let currentTask = runningPluginTask();
    const request = vi.fn(async (type: string, payload: Record<string, unknown>) => {
      if (type === "plugin.action.describe") return { available: true, reasons: [] };
      if (type === "plugin.action.start" || type === "plugin.task.get") return currentTask;
      if (type === "plugin.interaction.resolve") {
        currentTask = { ...currentTask, state: "cancelled" };
        return { status: "resolved", decision: payload.decision };
      }
      return (h.request as unknown as (
        type: string, payload: Record<string, unknown>,
      ) => Promise<unknown>)(type, payload);
    });
    setHostBridgeForTesting({ ...h.bridge, request } as HostBridge);
    const plugins = usePluginStore();
    plugins.applyPlugin(plugin);
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const wrapper = mount(InterfaceRuntime, {
      props: { definition: pluginDefinition(), activePageId: "list", previewWidth: "desktop" },
      global: { plugins: [pinia] },
    });
    await flushPromises();

    await wrapper.get('[data-testid="interface-runtime-plugin"] button').trigger("click");
    await flushPromises();
    plugins.applyInteraction(pluginInteraction(), 2);
    await flushPromises();
    await wrapper.get('[data-testid="plugin-confirm-reject"]').trigger("click");

    await vi.waitFor(() => expect(wrapper.text()).toContain("插件操作已拒绝"));
    expect(request).toHaveBeenCalledWith("plugin.interaction.resolve", {
      runId: "run-interface", interactionId: "confirm-interface", decision: "rejected",
    });
    expect(wrapper.text()).not.toContain("操作已完成");
    wrapper.unmount();
  });

  it("cancels an in-flight plugin task through the authoritative task endpoint", async () => {
    const pinia = createPinia();
    setActivePinia(pinia);
    usePluginStore().setProjectContext("local:default", "interface-session:1");
    const h = host();
    let currentTask = runningPluginTask();
    const request = vi.fn(async (type: string, payload: Record<string, unknown>) => {
      if (type === "plugin.action.describe") return { available: true, reasons: [] };
      if (type === "plugin.action.start" || type === "plugin.task.get") return currentTask;
      if (type === "plugin.task.cancel") {
        currentTask = { ...currentTask, state: "cancelled", cancelRequested: true };
        return currentTask;
      }
      return (h.request as unknown as (
        type: string, payload: Record<string, unknown>,
      ) => Promise<unknown>)(type, payload);
    });
    setHostBridgeForTesting({ ...h.bridge, request } as HostBridge);
    usePluginStore().applyPlugin(plugin);
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const wrapper = mount(InterfaceRuntime, {
      props: { definition: pluginDefinition(), activePageId: "list", previewWidth: "desktop" },
      global: { plugins: [pinia] },
    });
    await flushPromises();

    await wrapper.get('[data-testid="interface-runtime-plugin"] button').trigger("click");
    await flushPromises();
    await wrapper.get('[data-testid="plugin-task-cancel"]').trigger("click");

    await vi.waitFor(() => expect(wrapper.text()).toContain("插件操作已取消"));
    expect(request).toHaveBeenCalledWith("plugin.task.cancel", { taskId: "task-interface" });
    expect(wrapper.text()).not.toContain("操作已完成");
    wrapper.unmount();
  });

  it("updates the first visible record when the form displays it before an explicit selection", async () => {
    const h = host();
    setHostBridgeForTesting(h.bridge);
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const base = definition();
    const current: InterfaceDefinition = {
      ...base,
      actions: base.actions.map((item) => item.actionId === "update"
        ? { ...item, requiresConfirmation: true }
        : item),
    };
    const wrapper = mount(InterfaceRuntime, {
      props: { definition: current, activePageId: "list", previewWidth: "desktop" },
      global: { plugins: [createPinia()] },
    });
    await flushPromises();

    const updateForm = wrapper.get('[data-testid="interface-runtime-update-form"]');
    expect(updateForm.findAll("input")[0]!.element.value).toBe("North");
    await updateForm.findAll("input")[0]!.setValue("North updated");
    await updateForm.get("form").trigger("submit");
    await flushPromises();

    expect(window.confirm).toHaveBeenCalledTimes(1);
    expect(h.request).toHaveBeenCalledWith("table.updateCellRequested", expect.objectContaining({
      rowKey: "r1",
      column: "name",
      oldValue: "North",
      newValue: "North updated",
      expectedDigest: "d1",
    }));
    expect(wrapper.text()).toContain("操作已完成");
    wrapper.unmount();
  });

  it("executes typed filters, sorts, selected-record variables, and bounded paging", async () => {
    const h = host();
    setHostBridgeForTesting(h.bridge);
    const current = definition();
    const orders = current.bindings[0]!;
    const customers = current.bindings[1]!;
    const configured: InterfaceDefinition = {
      ...current,
      bindings: [
        {
          ...orders,
          query: {
            ...orders.query,
            filters: [{ fieldId: "status", operator: "startsWith", value: "op" }],
            sorts: [{ fieldId: "name", direction: "desc" }],
            pageSize: 1,
          },
        },
        {
          ...customers,
          variables: [{
            variableId: "selected-order",
            targetFieldId: "name",
            operator: "eq",
            source: "selectedRecordField",
            sourceBindingId: "orders",
            sourceFieldId: "name",
            value: null,
          }],
        },
      ],
      pages: [{
        ...current.pages[0]!,
        elements: [
          ...current.pages[0]!.elements,
          element("customers", "record-detail", { bindingId: "hidden" }),
        ],
      }, current.pages[1]!],
    };
    const wrapper = mount(InterfaceRuntime, {
      props: { definition: configured, activePageId: "list", previewWidth: "desktop" },
      global: { plugins: [createPinia()] },
    });
    await flushPromises();

    expect(h.request).toHaveBeenCalledWith("query.cursorOpen", expect.objectContaining({
      tableId: "orders",
      query: expect.objectContaining({
        filters: [{ field: "status", operator: "starts_with", value: "op" }],
        sorts: [{ field: "name", direction: "desc" }],
        offset: 0,
        limit: 1,
      }),
    }));
    const customerCalls = h.query.mock.calls
      .map(([payload]) => payload)
      .filter((payload) => payload.tableId === "customers");
    expect(customerCalls.at(-1)).toMatchObject({
      query: { filters: [{ field: "name", operator: "eq", value: "North" }] },
    });

    const pager = wrapper.get('[aria-label="orders 分页"]');
    expect(pager.text()).toContain("1–1 / 2");
    await pager.findAll("button")[1]!.trigger("click");
    await flushPromises();
    expect(h.request).toHaveBeenCalledWith("query.cursorFetch", { cursor: "orders:1" });
    expect(wrapper.get('[aria-label="orders 分页"]').text()).toContain("2–2 / 2");
    wrapper.unmount();
  });

  it("recomputes A to B to C variables topologically after the parent selection changes", async () => {
    const query = vi.fn(async (payload: Record<string, unknown>) => {
      const request = payload.query as { filters: Array<{ value: unknown }> };
      const selectedValue = request.filters[0]?.value;
      const rows = payload.tableId === "accounts"
        ? [{ rowKey: "a1", id: "A1" }, { rowKey: "a2", id: "A2" }]
        : payload.tableId === "orders"
          ? [{ rowKey: `b-${selectedValue}`, id: selectedValue === "A2" ? "B2" : "B1", accountId: selectedValue }]
          : [{ rowKey: `c-${selectedValue}`, orderId: selectedValue, label: selectedValue === "B2" ? "C2" : "C1" }];
      return {
        rows,
        nextCursor: null,
        hasMore: false,
        filteredRows: rows.length,
        totalRows: rows.length,
        querySnapshot: {},
      };
    });
    setHostBridgeForTesting({
      request: vi.fn(async (type: string, payload: Record<string, unknown>) => {
        if (type === "query.cursorOpen") return query(payload);
        throw new Error(`unexpected request: ${type}`);
      }),
    } as unknown as HostBridge);
    const source = binding("accounts", "accounts", ["id"]);
    const middle = {
      ...binding("orders", "orders", ["id", "accountId"]),
      variables: [{
        variableId: "account", targetFieldId: "accountId", operator: "eq" as const,
        source: "selectedRecordField" as const, sourceBindingId: "accounts", sourceFieldId: "id", value: null,
      }],
    };
    const leaf = {
      ...binding("lines", "lines", ["orderId", "label"]),
      variables: [{
        variableId: "order", targetFieldId: "orderId", operator: "eq" as const,
        source: "selectedRecordField" as const, sourceBindingId: "orders", sourceFieldId: "id", value: null,
      }],
    };
    const configured: InterfaceDefinition = {
      contractVersion: "1.0", interfaceId: "if-dag", name: "Dependency DAG",
      bindings: [leaf, middle, source], actions: [],
      pages: [{
        pageId: "dag", title: "DAG", elements: [
          element("accounts-list", "record-list", { bindingId: "accounts" }),
          element("orders-list", "record-list", { bindingId: "orders" }),
          element("lines-list", "record-list", { bindingId: "lines" }),
        ],
      }],
    };
    const wrapper = mount(InterfaceRuntime, {
      props: { definition: configured, activePageId: "dag", previewWidth: "desktop" },
      global: { plugins: [createPinia()] },
    });
    await flushPromises();

    expect(query.mock.calls.map(([payload]) => payload.tableId)).toEqual(["accounts", "orders", "lines"]);
    expect(query.mock.calls[1]?.[0]).toMatchObject({ query: { filters: [{ value: "A1" }] } });
    expect(query.mock.calls[2]?.[0]).toMatchObject({ query: { filters: [{ value: "B1" }] } });

    await wrapper.get('[data-testid="interface-runtime-accounts-list"]')
      .findAll("button.record-row")[1]!.trigger("click");
    await flushPromises();
    const recent = query.mock.calls.slice(-3).map(([payload]) => payload);
    expect(recent.map((payload) => payload.tableId)).toEqual(["accounts", "orders", "lines"]);
    expect(recent[1]).toMatchObject({ query: { filters: [{ value: "A2" }] } });
    expect(recent[2]).toMatchObject({ query: { filters: [{ value: "B2" }] } });
    expect(wrapper.get('[data-testid="interface-runtime-lines-list"]').text()).toContain("C2");
    wrapper.unmount();
  });

  it("shows stable action failures and reloads the binding when the active page changes", async () => {
    const h = host({ failUpdates: true });
    setHostBridgeForTesting(h.bridge);
    const wrapper = mount(InterfaceRuntime, {
      props: { definition: definition(), activePageId: "list", previewWidth: "mobile" },
      global: { plugins: [createPinia()] },
    });
    await flushPromises();

    await wrapper.get('[data-testid="interface-runtime-update-form"] form').trigger("submit");
    await flushPromises();
    expect(wrapper.get('[role="alert"]').text()).toContain("record.conflict");

    await wrapper.setProps({ activePageId: "details" });
    await flushPromises();
    expect(h.query).toHaveBeenCalledTimes(2);
    expect(wrapper.find('[data-testid="interface-runtime-hidden-detail"]').exists()).toBe(true);
    wrapper.unmount();
  });
});
