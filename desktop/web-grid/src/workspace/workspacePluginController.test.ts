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

    await controller.dispatch({ type: "action.open", key: "com.acme.clean/normalize" });

    expect(describeAction).toHaveBeenCalledWith(
      "com.acme.clean",
      "normalize",
      expect.objectContaining({
        contract: "vibetable.command-context.v1",
        collection: "orders",
        selectedKeys: ["row-1", 2],
      }),
    );
    expect(controller.toolbarActions.value).toEqual([
      expect.objectContaining({ key: "com.acme.clean/normalize", risk: "write", disabled: false }),
    ]);
  });

  it("start、resolve 与 cancel 只通过插件服务 seam，并自动补齐当前 collection", async () => {
    const plugins = usePluginStore();
    const workspace = useWorkspaceStore();
    workspace.selectTable("orders");
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
      pluginVersion: "1.0.0",
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
    await controller.dispatch({ type: "interaction.resolve", decision: "approved" });
    await controller.dispatch({ type: "task.cancel" });

    expect(startAction).toHaveBeenCalledWith(
      "com.acme.clean",
      "normalize",
      { mode: "trim", collection: "orders" },
      context,
    );
    expect(resolveInteraction).toHaveBeenCalledWith(activeTask, "approved");
    expect(cancelTask).toHaveBeenCalledWith("task-1");
  });
});
