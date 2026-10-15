import { createHostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import { usePluginService } from "@/services/pluginService";
import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { PluginSnapshot, PluginTaskSnapshot } from "@/contracts";
import { usePluginStore } from "@/stores/pluginStore";
import { useTableStore } from "@/stores/tableStore";
import { useUiStore } from "@/stores/uiStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import {
  createWorkspacePluginController,
  type WorkspacePluginServicePort,
} from "./workspacePluginController";

const plugin: PluginSnapshot = {
  installationId: "record-clean",
  projectKey: "local:default",
  pluginId: "com.acme.clean",
  version: "1.0.0",
  packageHash: "sha256:test",
  sourceType: "package",
  sourceLocation: "clean.vtplugin",
  manifest: {
    $schema: "vibetable.plugin-manifest.v1",
    pluginId: "com.acme.clean",
    version: "1.0.0",
    displayName: { "zh-CN": "清理" },
    description: {},
    compatibility: {},
    permissions: {},
    actions: [{
      actionId: "normalize",
      displayName: { "zh-CN": "规范化" },
      description: {},
      mode: "local",
      risk: "write",
      invocation: "manual",
      placements: ["table.toolbar", "table.context-menu"],
      requires: {},
      workerEntry: "worker.py",
      formSchema: null,
      inputSchema: null,
      outputSchema: null,
    }],
    ui: {},
  },
  schemas: {},
  status: "enabled",
  disabledReason: null,
  revision: 1,
};

describe("workspacePluginController", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("在模块内从当前 grid 选择构造插件命令上下文", async () => {
    const plugins = usePluginStore();
    plugins.replaceCatalog("local:default", [plugin], 1);
    const workspace = useWorkspaceStore();
    workspace.selectTable("orders");
    const describeAction = vi.fn(async () => ({ available: true, reasons: [] }));
    const unexpected = async (): Promise<never> => { throw new Error("unexpected plugin call"); };
    const service: WorkspacePluginServicePort = {
      describeAction,
      startAction: vi.fn(unexpected),
      resolveInteraction: vi.fn(unexpected),
      cancelTask: vi.fn(unexpected),
    };
    const controller = createWorkspacePluginController({
      workspace,
      table: useTableStore(),
      ui: useUiStore(),
      plugins,
      service,
      selectedRows: () => [{ rowKey: "row-1" }, { rowKey: 2 }, { rowKey: null }],
      openHistory: vi.fn(),
      openFieldCreate: vi.fn(),
      openFieldEdit: vi.fn(),
      reportError: vi.fn(),
      historyCellLabel: () => "单元格历史",
      historyRowLabel: () => "行历史",
      riskLabel: risk => risk,
    });

    const ui = useUiStore();
    expect(controller.theme.value.mode).toBe("light");
    ui.applyThemeProjection({ preference: "system", effective: "dark", highContrast: false });
    expect(controller.theme.value.mode).toBe("dark");
    ui.applyThemeProjection({ preference: "system", effective: "light", highContrast: false });
    expect(controller.theme.value.mode).toBe("light");
    ui.applyThemeProjection({ preference: "dark", effective: "dark", highContrast: false });
    expect(controller.theme.value.mode).toBe("dark");

    await controller.dispatch({ type: "action.open", key: "com.acme.clean/normalize" });

    expect(describeAction).toHaveBeenCalledWith(
      "com.acme.clean",
      "normalize",
      expect.objectContaining({
        contract: "vibetable.command-context.v1",
        collection: "orders",
        theme: "dark",
        selectedKeys: ["row-1", 2],
      }),
      true,
    );
    expect(controller.toolbarActions.value).toEqual([
      expect.objectContaining({ key: "com.acme.clean/normalize", risk: "write", disabled: false }),
    ]);
  });

  it("start、resolve 与 cancel 只通过插件服务 seam，并自动补齐当前 collection", async () => {
    const plugins = usePluginStore();
    const workspace = useWorkspaceStore();
    workspace.selectTable("orders");
    plugins.setProjectContext("local:default", "r1");
    plugins.replaceCatalog("local:default", [plugin]);
    const context = {
      contract: "vibetable.command-context.v1" as const,
      projectKey: "local:default",
      collection: "orders",
      selectedKeys: ["row-1"],
      querySnapshot: null,
      locale: "zh-CN",
      theme: "light" as const,
      density: "comfortable",
      user: {},
      hostVersion: "1.0.0",
    };
    const description = {
      pluginId: "com.acme.clean",
      actionId: "normalize",
      title: "规范化",
      risk: "write" as const,
      inputSchema: {
        type: "object",
        properties: { collection: { type: "string" }, mode: { type: "string" } },
      },
    };
    plugins.beginAction(description, context);
    const running: PluginTaskSnapshot = {
      taskId: "task-1",
      runId: "run-1",
      pluginId: "com.acme.clean",
      pluginVersion: "1.0.0", installationRevision: 1, installationId: plugin.installationId,
      actionId: "normalize",
      projectKey: "local:default",
      collection: "orders",
      targetCount: 1,
      risk: "write",
      state: "running",
      cancelRequested: false,
      result: null,
      error: null,
    };
    const activeTask = plugins.applyTask(running, 1);
    const startAction = vi.fn(async () => activeTask);
    const resolveInteraction = vi.fn(async () => activeTask);
    const cancelTask = vi.fn(async () => activeTask);
    const service: WorkspacePluginServicePort = {
      describeAction: vi.fn(async () => ({ available: true, reasons: [] })),
      startAction,
      resolveInteraction,
      cancelTask,
    };
    const controller = createWorkspacePluginController({
      workspace,
      table: useTableStore(),
      ui: useUiStore(),
      plugins,
      service,
      selectedRows: () => [],
      openHistory: vi.fn(),
      openFieldCreate: vi.fn(),
      openFieldEdit: vi.fn(),
      reportError: vi.fn(),
      historyCellLabel: () => "单元格历史",
      historyRowLabel: () => "行历史",
      riskLabel: risk => risk,
    });

    await controller.dispatch({ type: "action.start", payload: { mode: "trim" } });
    await controller.dispatch({ type: "interaction.resolve", taskId: "task-1", decision: "approved" });
    await controller.dispatch({ type: "task.cancel", taskId: "task-1" });

    expect(startAction).toHaveBeenCalledWith(
      "com.acme.clean",
      "normalize",
      { mode: "trim", collection: "orders" },
      context,
    );
    expect(resolveInteraction).toHaveBeenCalledWith(activeTask, "approved");
    expect(cancelTask).toHaveBeenCalledWith("task-1");
  });
  it("binds same-action native tasks to the current surface even when an unknown old run first arrives late", async () => {
    const plugins = usePluginStore(); plugins.setProjectContext("local:default", "r1");
    const app: PluginSnapshot = { ...plugin, manifest: { ...plugin.manifest, actions: plugin.manifest.actions.map(action => ({ ...action,
      presentation: "application", workerEntry: null, entry: "ui/index.html", surfaceToken: "current-surface", surfaceSrc: "https://app.plugins.vibetable.local/ui/index.html" })) } };
    plugins.replaceCatalog("local:default", [app]); const ui = useUiStore(); ui.openPluginApp(app.pluginId, "normalize");
    const task: PluginTaskSnapshot = { taskId: "old", runId: "old-run", pluginId: app.pluginId, pluginVersion: app.version,
      installationId: app.installationId, installationRevision: app.revision, actionId: "normalize", surfaceToken: "old-surface",
      projectKey: app.projectKey, collection: null, targetCount: 1, risk: "write", state: "succeeded", cancelRequested: false, result: null, error: null };
    const cancelTask = vi.fn();
    const resolveInteraction = vi.fn();
    const controller = createWorkspacePluginController({ workspace: useWorkspaceStore(), table: useTableStore(), ui, plugins,
      service: { describeAction: vi.fn(), startAction: vi.fn(), cancelTask, resolveInteraction },
      selectedRows: () => [], openHistory: vi.fn(), openFieldCreate: vi.fn(), openFieldEdit: vi.fn(), reportError: vi.fn(),
      historyCellLabel: () => "cell", historyRowLabel: () => "row", riskLabel: value => value });
    plugins.applyTask(task); expect(controller.applicationTask.value).toBeNull();
    plugins.applyTask({ ...task, taskId: "worker", runId: "worker-run", surfaceToken: null }); expect(controller.applicationTask.value).toBeNull();
    plugins.applyTask({ ...task, taskId: "current", runId: "current-run", surfaceToken: "current-surface", state: "running" });
    expect(controller.applicationTask.value?.taskId).toBe("current");
    plugins.applyTask({ ...task, taskId: "newer", runId: "newer-run", surfaceToken: "current-surface", state: "succeeded" });
    expect(controller.applicationTasks.value.map(task => task.taskId)).toEqual(["current", "newer"]);
    await controller.dispatch({ type: "task.cancel", taskId: "current" });
    await controller.dispatch({ type: "interaction.resolve", taskId: "current", decision: "rejected" });
    expect(cancelTask).toHaveBeenCalledWith("current");
    expect(resolveInteraction).toHaveBeenCalledWith(expect.objectContaining({ taskId: "current" }), "rejected");
    plugins.applyTask({ ...task, taskId: "current", runId: "current-run", surfaceToken: "forged", state: "running" });
    expect(controller.applicationTasks.value.map(task => task.taskId)).toEqual(["current", "newer"]);
  });

  it.each(["3.x", "2.x"])("isolates native custom runs while preserving %s task admission", async (pluginApi) => {
    const plugins = usePluginStore();
    plugins.setProjectContext("local:default", "r1");
    const custom: PluginSnapshot = { ...plugin, manifest: { ...plugin.manifest,
      compatibility: { pluginApi }, actions: plugin.manifest.actions.map(action => ({ ...action, presentation: "surface" })) } };
    plugins.replaceCatalog("local:default", [custom]);
    const context = { contract: "vibetable.command-context.v1" as const, projectKey: custom.projectKey,
      collection: "orders", selectedKeys: [], querySnapshot: null, locale: "zh-CN", theme: "light" as const,
      density: "comfortable", user: {}, hostVersion: "1.0.0" };
    const description = { pluginId: custom.pluginId, actionId: "normalize", title: "custom", risk: "write" as const,
      inputSchema: {}, presentation: "custom" as const,
      surface: { src: "https://custom.plugins.vibetable.local/ui/index.html", surfaceToken: "current-surface", title: "custom" } };
    plugins.beginAction(description, context);
    const task: PluginTaskSnapshot = { taskId: "old", runId: "old-run", pluginId: custom.pluginId,
      actionId: "normalize", pluginVersion: custom.version, installationRevision: custom.revision,
      installationId: custom.installationId, projectKey: custom.projectKey, collection: "orders",
      targetCount: 1, risk: "write", state: "running", cancelRequested: false, result: null, error: null,
      surfaceToken: pluginApi === "3.x" ? "old-surface" : null };
    const cancelTask = vi.fn(async () => plugins.taskForAction(custom.pluginId, "normalize")!);
    const resolveInteraction = vi.fn(async () => plugins.taskForAction(custom.pluginId, "normalize")!);
    const controller = createWorkspacePluginController({ workspace: useWorkspaceStore(), table: useTableStore(), ui: useUiStore(), plugins,
      service: { describeAction: vi.fn(), startAction: vi.fn(), cancelTask, resolveInteraction },
      selectedRows: () => [], openHistory: vi.fn(), openFieldCreate: vi.fn(), openFieldEdit: vi.fn(), reportError: vi.fn(),
      historyCellLabel: () => "cell", historyRowLabel: () => "row", riskLabel: value => value });
    const oldView = plugins.applyTask(task);
    expect(controller.actionTask.value?.taskId ?? null).toBe(pluginApi === "3.x" ? null : "old");
    await controller.dispatch({ type: "task.cancel", taskId: "old" });
    await controller.dispatch({ type: "interaction.resolve", taskId: "old", decision: "rejected" });
    if (pluginApi === "3.x") {
      expect(cancelTask).not.toHaveBeenCalled();
      expect(resolveInteraction).not.toHaveBeenCalled();
      plugins.applyTask({ ...task, taskId: "missing-source", runId: "missing-run", surfaceToken: null });
      expect(controller.actionTask.value).toBeNull();
      plugins.applyTask({ ...task, taskId: "current", runId: "current-run", surfaceToken: "current-surface" });
      expect(controller.actionTask.value?.taskId).toBe("current");
      await controller.dispatch({ type: "task.cancel", taskId: "current" });
      expect(cancelTask).toHaveBeenCalledWith("current");
      plugins.applyTask({ ...task, taskId: "newer", runId: "newer-run", surfaceToken: "current-surface", state: "succeeded" });
      expect(controller.actionTasks.value.map(task => task.taskId)).toEqual(["current", "newer"]);
      await controller.dispatch({ type: "interaction.resolve", taskId: "current", decision: "rejected" });
      expect(resolveInteraction).toHaveBeenCalledWith(expect.objectContaining({ taskId: "current" }), "rejected");
      plugins.beginAction({ ...description, surface: { ...description.surface, surfaceToken: "reopened-surface" } }, context);
      expect(controller.actionTask.value).toBeNull();
      plugins.beginAction({ ...description, surface: null }, context);
      expect(controller.actionTask.value).toBeNull();
    } else {
      expect(cancelTask).toHaveBeenCalledWith("old");
      expect(resolveInteraction).toHaveBeenCalledWith(oldView, "rejected");
      plugins.beginAction({ ...description, surface: null }, context);
      expect(controller.actionTask.value?.taskId).toBe("old");
    }
  });

  it("keeps application and action task controls bound across other plugin/action updates", async () => {
    const plugins = usePluginStore();
    plugins.setProjectContext("local:default", "r1");
    plugins.replaceCatalog("local:default", [plugin]);
    const ui = useUiStore();
    ui.openPluginApp(plugin.pluginId, "normalize");
    const context = { contract: "vibetable.command-context.v1" as const, projectKey: "local:default",
      collection: "orders", selectedKeys: [], querySnapshot: null, locale: "zh-CN", theme: "light" as const,
      density: "comfortable", user: {}, hostVersion: "1.0.0" };
    plugins.beginAction({ pluginId: plugin.pluginId, actionId: "normalize", title: "owned", risk: "write",
      inputSchema: { type: "object" } }, context);
    const own: PluginTaskSnapshot = { taskId: "own", runId: "own-run", pluginId: plugin.pluginId,
      actionId: "normalize", pluginVersion: plugin.version, installationRevision: plugin.revision, installationId: plugin.installationId, projectKey: "local:default", collection: "orders",
      targetCount: 1, risk: "write", state: "running", cancelRequested: false, result: null, error: null };
    const ownView = plugins.applyTask(own);
    plugins.applyTask({ ...own, taskId: "foreign", runId: "foreign-run", pluginId: "com.acme.other" });
    plugins.applyTask({ ...own, taskId: "other-action", runId: "other-run", actionId: "other" });
    const cancelTask = vi.fn(async () => ownView);
    const resolveInteraction = vi.fn(async () => ownView);
    const controller = createWorkspacePluginController({ workspace: useWorkspaceStore(), table: useTableStore(), ui, plugins,
      service: { describeAction: vi.fn(async () => ({ available: true, reasons: [] })),
        startAction: vi.fn(async () => ownView), cancelTask, resolveInteraction },
      selectedRows: () => [], openHistory: vi.fn(), openFieldCreate: vi.fn(), openFieldEdit: vi.fn(),
      reportError: vi.fn(), historyCellLabel: () => "cell", historyRowLabel: () => "row", riskLabel: value => value });
    expect(controller.applicationTask.value?.taskId).toBe("own");
    expect(controller.actionTask.value?.taskId).toBe("own");
    await controller.dispatch({ type: "task.cancel", taskId: "own" });
    await controller.dispatch({ type: "interaction.resolve", taskId: "own", decision: "rejected" });
    expect(cancelTask).toHaveBeenCalledWith("own");
    expect(resolveInteraction).toHaveBeenCalledWith(ownView, "rejected");
    cancelTask.mockClear();
    await controller.dispatch({ type: "task.cancel", taskId: "foreign" });
    expect(cancelTask).not.toHaveBeenCalled();
  });

  it.each(["action.open", "rowMenu.select", "app.open"] as const)("routes the actual %s application caller without a Worker panel", async (entry) => {
    const plugins = usePluginStore();
    plugins.setProjectContext("local:default", "r1");
    const app: PluginSnapshot = { ...plugin, manifest: { ...plugin.manifest,
      $schema: "vibetable.plugin-manifest.v2", compatibility: { pluginApi: "3.x" },
      actions: plugin.manifest.actions.map(action => ({ ...action, workerEntry: null,
        presentation: "application", requires: { selection: "exactly-one" }, entry: "ui/index.html", surfaceSrc: "https://app.plugins.vibetable.local/ui/index.html", surfaceToken: "token" })) } };
    plugins.replaceCatalog("local:default", [app]);
    const posted: { type: string; payload: Record<string, unknown>; requestId: string }[] = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({ generateRequestId: () => `app-entry-${++sequence}`,
      webview: { postMessage(message) {
        const request = message as (typeof posted)[number]; posted.push(request);
        queueMicrotask(() => listener?.({ data: { type: request.type, requestId: request.requestId,
          payload: request.type === "plugin.catalog.list" ? [app] : { available: ((request.payload.context as { selectedKeys: unknown[] }).selectedKeys.length === 1), reasons: ["single_selection_required"] } } }));
      }, addEventListener: (_type, handler) => { listener = handler; }, removeEventListener: () => undefined } });
    bridge.start(); setHostBridgeForTesting(bridge);
    const ui = useUiStore();
    const workspace = useWorkspaceStore(); workspace.selectTable("orders");
    const controller = createWorkspacePluginController({ workspace, table: useTableStore(), ui, plugins, service: usePluginService(),
      selectedRows: () => [{ rowKey: "selected" }], openHistory: vi.fn(), openFieldCreate: vi.fn(), openFieldEdit: vi.fn(),
      reportError: vi.fn(), historyCellLabel: () => "cell", historyRowLabel: () => "row", riskLabel: value => value });
    if (entry === "rowMenu.select") await controller.dispatch({ type: "rowMenu.open", rowKey: "row-menu", x: 1, y: 1 });
    await controller.dispatch({ type: entry, key: `${app.pluginId}/normalize` });
    expect(ui.activePluginApp).toEqual({ pluginId: app.pluginId, actionId: "normalize" });
    expect(plugins.actionOpen).toBe(false);
    expect(plugins.activeContext?.selectedKeys).toEqual([entry === "rowMenu.select" ? "row-menu" : "selected"]);
    await expect(usePluginService().startAction(app.pluginId, "normalize", {}, plugins.activeContext!)).rejects.toThrow("Worker");
    expect(posted.some(item => item.type === "plugin.action.start")).toBe(false);
    bridge.stop(); setHostBridgeForTesting(null);
  });

  it("cancels a closed native receipt by its original task id without permitting approval or a foreign task", async () => {
    const plugins = usePluginStore(); plugins.setProjectContext("local:default", "r1");
    const pending: PluginTaskSnapshot = { taskId: "closed", runId: "closed-run", projectKey: "local:default",
      pluginId: plugin.pluginId, actionId: "normalize", pluginVersion: plugin.version, installationId: "retired-record",
      installationRevision: 1, surfaceToken: "closed-token", collection: null, targetCount: 1,
      risk: "write", state: "running", cancelRequested: false, result: null, error: null };
    const view = plugins.applyTask(pending);
    plugins.applyTask({ ...pending, taskId: "foreign-scope", projectKey: "local:other" });
    const cancelTask = vi.fn(async () => view); const resolveInteraction = vi.fn();
    const controller = createWorkspacePluginController({ workspace: useWorkspaceStore(), table: useTableStore(), ui: useUiStore(), plugins,
      service: { describeAction: vi.fn(), startAction: vi.fn(), cancelTask, resolveInteraction }, selectedRows: () => [],
      openHistory: vi.fn(), openFieldCreate: vi.fn(), openFieldEdit: vi.fn(), reportError: vi.fn(),
      historyCellLabel: () => "cell", historyRowLabel: () => "row", riskLabel: value => value });
    await controller.dispatch({ type: "task.cancel", taskId: pending.taskId });
    await controller.dispatch({ type: "interaction.resolve", taskId: pending.taskId, decision: "approved" });
    await controller.dispatch({ type: "receipt.cancel", taskId: "foreign-scope" });
    expect(cancelTask).not.toHaveBeenCalled(); expect(resolveInteraction).not.toHaveBeenCalled();
    await controller.dispatch({ type: "receipt.cancel", taskId: pending.taskId });
    expect(cancelTask).toHaveBeenCalledExactlyOnceWith("closed");
    plugins.applyTask({ ...pending, state: "succeeded" }, 2);
    await controller.dispatch({ type: "receipt.cancel", taskId: pending.taskId });
    expect(cancelTask).toHaveBeenCalledTimes(1);
  });

});
