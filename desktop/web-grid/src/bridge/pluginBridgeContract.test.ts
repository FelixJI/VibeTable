import { describe, expect, it, vi } from "vitest";
import { createHostBridge } from "./hostBridge";

describe("plugin bridge whitelist", () => {
  it("only posts the fixed plugin use-case messages", () => {
    const postMessage = vi.fn();
    const bridge = createHostBridge({
      webview: {
        postMessage,
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
      },
    });

    const context = {
      contract: "vibetable.command-context.v1" as const,
      projectKey: "local:default",
      collection: "customers",
      selectedKeys: ["1"],
      querySnapshot: null,
      locale: "zh-CN",
      theme: "light" as const,
      density: "compact",
      user: {},
      hostVersion: "1.0.0",
    };
    bridge.notify("plugin.catalog.list", { projectKey: "local:default" });
    bridge.notify("plugin.install.inspect", {
      projectKey: "local:default",
      projectRevision: "r1",
      sourceLocation: "C:/plugins/clean.vtplugin",
    });
    bridge.notify("plugin.install.commit", { planId: "plan-1", projectRevision: "r1" });
    bridge.notify("plugin.externalFlow.listCandidates", {
      projectKey: "local:default",
      pluginId: "com.acme.clean",
      logicalFlowId: "clean",
    });
    bridge.notify("plugin.externalFlow.bind", {
      projectKey: "local:default",
      pluginId: "com.acme.clean",
      logicalFlowId: "clean",
      directusFlowUuid: "flow-1",
      acceptsUnknownSideEffects: false,
    });
    bridge.notify("plugin.lifecycle.setEnabled", {
      projectKey: "local:default",
      pluginId: "com.acme.clean",
      enabled: false,
    });
    bridge.notify("plugin.lifecycle.upgrade", {
      projectKey: "local:default", pluginId: "com.acme.clean", planId: "upgrade-1", projectRevision: "r1",
    });
    bridge.notify("plugin.lifecycle.rollback", { projectKey: "local:default", pluginId: "com.acme.clean" });
    bridge.notify("plugin.lifecycle.uninstall", {
      projectKey: "local:default", pluginId: "com.acme.clean", cleanupPrivateSettings: false,
    });
    bridge.notify("plugin.action.describe", {
      projectKey: "local:default", pluginId: "com.acme.clean", actionId: "normalize", context,
    });
    bridge.notify("plugin.action.start", {
      projectKey: "local:default",
      pluginId: "com.acme.clean",
      actionId: "normalize",
      input: { trim: true },
      context,
    });
    bridge.notify("plugin.interaction.resolve", { runId: "run-1", interactionId: "i-1", decision: "rejected" });
    bridge.notify("plugin.task.cancel", { taskId: "task-1" });
    bridge.notify("plugin.task.get", { taskId: "task-1" });
    bridge.notify("plugin.surface.event", {
      contract: "vibetable.plugin-surface.v1",
      surfaceToken: "surface-1",
      event: "ready",
      payload: {},
    });

    expect(postMessage.mock.calls.map(([message]) => message.type)).toEqual([
      "plugin.catalog.list",
      "plugin.install.inspect",
      "plugin.install.commit",
      "plugin.externalFlow.listCandidates",
      "plugin.externalFlow.bind",
      "plugin.lifecycle.setEnabled",
      "plugin.lifecycle.upgrade",
      "plugin.lifecycle.rollback",
      "plugin.lifecycle.uninstall",
      "plugin.action.describe",
      "plugin.action.start",
      "plugin.interaction.resolve",
      "plugin.task.cancel",
      "plugin.task.get",
      "plugin.surface.event",
    ]);
    expect(JSON.stringify(postMessage.mock.calls)).not.toContain("rpc.invoke");
    expect(JSON.stringify(postMessage.mock.calls)).not.toMatch(
      /sourceToken|planToken|expectedRevision|flowUuid|acknowledgeUnknownSideEffects|cleanupSettings|"decision":"approve"|"decision":"reject"/,
    );
  });
});
