import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge } from "@/bridge/hostBridge";
import type { PluginSnapshot, PluginTaskSnapshot } from "@/contracts";
import { setHostBridgeForTesting } from "./bridgeContext";
import { createPluginCommandContext, usePluginService } from "./pluginService";
import { usePluginStore } from "@/stores/pluginStore";

const snapshot: PluginSnapshot = {
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
      formSchema: null, inputSchema: "schemas/input.json", outputSchema: null,
    }],
    ui: {},
  },
  schemas: { "schemas/input.json": { type: "object", properties: { trim: { type: "boolean" } } } },
  status: "enabled",
  disabledReason: null,
  revision: 1,
};

describe("pluginService canonical wire", () => {
  beforeEach(() => setActivePinia(createPinia()));
  afterEach(() => setHostBridgeForTesting(null));

  it("projects the live query, safe user identity and host version into command context", () => {
    const querySnapshot = {
      snapshotId: "snapshot-1",
      digest: "digest-1",
      databaseId: "local",
      table: "orders",
      schemaRevision: "schema-r1",
      dataRevision: 4,
      normalizedQuery: { sort: ["-created_at"] },
    };

    const context = createPluginCommandContext({
      projectKey: "local:workspace-a",
      collection: "orders",
      selectedKeys: [1, 2],
      querySnapshot,
      locale: "zh-CN",
      theme: "dark",
      density: "compact",
      user: { id: "user-7", displayName: "Alice" },
      hostVersion: "2.4.1",
    });

    expect(context.querySnapshot).toBe(querySnapshot);
    expect(context.user).toEqual({ id: "user-7", displayName: "Alice" });
    expect(context.hostVersion).toBe("2.4.1");
  });

  it("rebuilds the project catalog from canonical PluginSnapshot[]", async () => {
    const posted: Array<{ type: string; requestId?: string; payload?: unknown }> = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    const bridge = createHostBridge({
      generateRequestId: () => "request-1",
      webview: {
        postMessage: (message) => posted.push(message as { type: string; requestId?: string; payload?: unknown }),
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const pending = usePluginService().list();
    listener?.({ data: { type: "plugin.catalog.list", requestId: "request-1", payload: [snapshot] } });
    await pending;
    expect(posted[0]).toEqual({
      type: "plugin.catalog.list",
      requestId: "request-1",
      payload: { projectKey: "local:default" },
    });
    expect(usePluginStore().plugins[0]?.pluginId).toBe("com.acme.clean");
  });

  it("does not let a background audit refresh clear a foreground install error", async () => {
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `error-race-${++sequence}`,
      webview: {
        postMessage: (message) => {
          const request = message as { type: string; requestId: string };
          queueMicrotask(() => listener?.({
            data: request.type === "plugin.install.inspect"
              ? {
                  type: "operation.failed",
                  requestId: request.requestId,
                  payload: { code: "PLUGIN_MANIFEST_INVALID", message: "invalid manifest" },
                }
              : {
                  type: request.type,
                  requestId: request.requestId,
                  payload: [],
                },
          }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const store = usePluginStore();
    const service = usePluginService();

    await expect(service.inspectInstall("host-picker:folder")).rejects.toThrow(
      "invalid manifest",
    );
    expect(store.lastError).toBe("invalid manifest");

    await service.listAudit(snapshot.pluginId);

    expect(store.lastError).toBe("invalid manifest");
  });

  it("discards a late catalog response after the active project changes", async () => {
    let listener: ((event: { data: unknown }) => void) | undefined;
    const bridge = createHostBridge({
      generateRequestId: () => "catalog-a",
      webview: {
        postMessage: () => undefined,
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const store = usePluginStore();
    store.setProjectContext("project:a", "r1");
    const pending = usePluginService().list();
    store.setProjectContext("project:b", "r1");

    listener?.({
      data: { type: "plugin.catalog.list", requestId: "catalog-a", payload: [snapshot] },
    });
    await pending;

    expect(store.projectKey).toBe("project:b");
    expect(store.plugins).toEqual([]);
  });

  it("submits the current host project revision so a stale plan is rejected", async () => {
    const posted: Array<{ type: string; requestId?: string; payload?: unknown }> = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const plan = {
      planId: "plan-stale",
      projectKey: "local:default",
      projectRevision: "r1:state-fingerprint",
      sourceType: "package" as const,
      sourceLocation: "host-managed",
      packageHash: "sha256:abc",
      manifest: snapshot.manifest,
      schemas: snapshot.schemas,
    };
    const bridge = createHostBridge({
      generateRequestId: () => `stale-${++sequence}`,
      webview: {
        postMessage: (message) => {
          const request = message as { type: string; requestId: string; payload: unknown };
          posted.push(request);
          queueMicrotask(() => listener?.({ data: {
            type: request.type,
            requestId: request.requestId,
            payload: request.type === "plugin.install.inspect" ? plan : snapshot,
          } }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const store = usePluginStore();
    store.setProjectContext("local:default", "r1");
    const service = usePluginService();
    const inspected = await service.inspectInstall("host-picker:package");
    store.setProjectContext("local:default", "r2");

    await service.commitInstall(inspected);

    expect(posted[1]).toMatchObject({
      type: "plugin.install.commit",
      payload: { planId: "plan-stale", projectRevision: "r2" },
    });
  });

  it("uses canonical install, action and uninstall use-case payloads", async () => {
    const posted: Array<{ type: string; requestId?: string; payload?: unknown }> = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const plan = {
      planId: "plan-1",
      projectKey: "local:default",
      projectRevision: "r1",
      sourceType: "package" as const,
      sourceLocation: "C:/plugins/clean.vtplugin",
      packageHash: "sha256:abc",
      manifest: snapshot.manifest,
      schemas: snapshot.schemas,
    };
    const responses: Record<string, unknown> = {
      "plugin.install.inspect": plan,
      "plugin.action.describe": { available: true, reasons: [] },
      "plugin.lifecycle.uninstall": {
        uninstalled: true, privateSettingsRetained: false, cleanupPending: false,
      },
    };
    const bridge = createHostBridge({
      generateRequestId: () => `request-${++sequence}`,
      webview: {
        postMessage: (message) => {
          const envelope = message as { type: string; requestId?: string; payload?: unknown };
          posted.push(envelope);
          queueMicrotask(() => listener?.({ data: {
            type: envelope.type,
            requestId: envelope.requestId,
            payload: responses[envelope.type],
          } }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const store = usePluginStore();
    store.setProjectContext("local:default", "r1");
    store.applyPlugin(snapshot);
    const service = usePluginService();

    await service.inspectInstall("C:/plugins/clean.vtplugin");
    const context = createPluginCommandContext({
      projectKey: "local:default", locale: "zh-CN", theme: "light", density: "compact",
    });
    await service.describeAction("com.acme.clean", "normalize", context);
    await service.uninstall(snapshot, true);

    expect(posted.map(({ type, payload }) => ({ type, payload }))).toEqual([
      { type: "plugin.install.inspect", payload: {
        projectKey: "local:default", projectRevision: "r1", sourceLocation: "C:/plugins/clean.vtplugin",
      } },
      { type: "plugin.action.describe", payload: {
        projectKey: "local:default", pluginId: "com.acme.clean", actionId: "normalize", context,
      } },
      { type: "plugin.lifecycle.uninstall", payload: {
        projectKey: "local:default", pluginId: "com.acme.clean", cleanupPrivateSettings: true,
      } },
    ]);
  });

  it("polls task truth after resolving a fast interaction race", async () => {
    let listener: ((event: { data: unknown }) => void) | undefined;
    let requestSequence = 0;
    let taskReads = 0;
    const running: PluginTaskSnapshot = {
      taskId: "task-fast",
      runId: "run-fast",
      pluginId: "com.acme.clean",
      pluginVersion: "1.2.0",
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
    const bridge = createHostBridge({
      generateRequestId: () => `poll-${++requestSequence}`,
      webview: {
        postMessage: (message) => {
          const request = message as { type: string; requestId: string };
          let payload: unknown = {};
          if (request.type === "plugin.task.get") {
            taskReads += 1;
            payload = taskReads === 1
              ? running
              : { ...running, state: "succeeded" };
          }
          queueMicrotask(() => listener?.({
            data: { type: request.type, requestId: request.requestId, payload },
          }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const store = usePluginStore();
    const task = {
      ...store.applyTask(running),
      confirmation: {
        runId: "run-fast",
        interactionId: "confirm-fast",
        pluginId: "com.acme.clean",
        actionId: "normalize",
        title: "Confirm",
        summary: "one row",
        risk: "write" as const,
        targetCount: 1,
        sample: [],
        expiresAt: new Date(Date.now() + 60_000).toISOString(),
      },
    };

    const resolved = await usePluginService().resolveInteraction(task, "approved");

    expect(taskReads).toBe(2);
    expect(resolved.state).toBe("succeeded");
    expect(store.activeTask?.state).toBe("succeeded");
  });

  it("reconciles a terminal task when its change notification is missed", async () => {
    let listener: ((event: { data: unknown }) => void) | undefined;
    let requestSequence = 0;
    let taskReads = 0;
    const running: PluginTaskSnapshot = {
      taskId: "task-missed-terminal",
      runId: "run-missed-terminal",
      pluginId: "com.acme.clean",
      pluginVersion: "1.2.0",
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
    const bridge = createHostBridge({
      generateRequestId: () => `reconcile-${++requestSequence}`,
      webview: {
        postMessage: (message) => {
          const request = message as { type: string; requestId: string };
          let payload: unknown = {};
          if (request.type === "plugin.action.start") payload = running;
          if (request.type === "plugin.task.get") {
            taskReads += 1;
            payload = {
              ...running,
              state: "failed",
              error: {
                contract: "vibetable.plugin-error.v1",
                code: "plugin_action_failed",
                message: "field was not declared",
                recoverability: "reconfigure",
              },
            };
          }
          queueMicrotask(() => listener?.({
            data: { type: request.type, requestId: request.requestId, payload },
          }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const service = usePluginService();
    const context = createPluginCommandContext({
      projectKey: "local:default",
      collection: "orders",
      locale: "zh-CN",
      theme: "light",
      density: "compact",
    });

    await service.startAction("com.acme.clean", "normalize", {}, context);

    await vi.waitFor(() => {
      expect(usePluginStore().activeTask?.state).toBe("failed");
    });
    expect(taskReads).toBeGreaterThanOrEqual(1);
    service.dispose();
  });
});
