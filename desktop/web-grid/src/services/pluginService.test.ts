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
  beforeEach(() => {
    setActivePinia(createPinia());
    usePluginStore().setProjectContext("local:default", "r1");
  });
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

  it("does not let a failed background audit refresh replace a foreground install error", async () => {
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `error-race-failure-${++sequence}`,
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
                  type: "operation.failed",
                  requestId: request.requestId,
                  payload: { code: "PLUGIN_AUDIT_UNAVAILABLE", message: "audit unavailable" },
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
    await expect(service.listAudit(snapshot.pluginId)).rejects.toThrow("audit unavailable");

    expect(store.lastError).toBe("invalid manifest");
    expect(store.busy).toBe(false);
  });

  it("records a failed background audit refresh when no foreground error is visible", async () => {
    let listener: ((event: { data: unknown }) => void) | undefined;
    const bridge = createHostBridge({
      generateRequestId: () => "background-only",
      webview: {
        postMessage: (message) => {
          const request = message as { requestId: string };
          queueMicrotask(() => listener?.({
            data: {
              type: "operation.failed",
              requestId: request.requestId,
              payload: { code: "PLUGIN_AUDIT_UNAVAILABLE", message: "audit unavailable" },
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

    await expect(usePluginService().listAudit(snapshot.pluginId)).rejects.toThrow(
      "audit unavailable",
    );

    expect(store.lastError).toBe("audit unavailable");
    expect(store.busy).toBe(false);
  });

  it("keeps a foreground failure when it settles while a background audit refresh is in flight", async () => {
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const posted: Array<{ type: string; requestId: string }> = [];
    const bridge = createHostBridge({
      generateRequestId: () => `race-${++sequence}`,
      webview: {
        postMessage: (message) => posted.push(message as { type: string; requestId: string }),
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const service = usePluginService();
    const background = service.listAudit(snapshot.pluginId);
    const foreground = service.inspectInstall("host-picker:folder");
    const auditRequest = posted.find((request) => request.type === "plugin.audit.list");
    const inspectRequest = posted.find((request) => request.type === "plugin.install.inspect");
    expect(auditRequest).toBeDefined();
    expect(inspectRequest).toBeDefined();

    listener?.({
      data: {
        type: "operation.failed",
        requestId: inspectRequest?.requestId,
        payload: { code: "PLUGIN_MANIFEST_INVALID", message: "invalid manifest" },
      },
    });
    await expect(foreground).rejects.toThrow("invalid manifest");
    listener?.({
      data: {
        type: "operation.failed",
        requestId: auditRequest?.requestId,
        payload: { code: "PLUGIN_AUDIT_UNAVAILABLE", message: "audit unavailable" },
      },
    });
    await expect(background).rejects.toThrow("audit unavailable");

    expect(usePluginStore().lastError).toBe("invalid manifest");
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

  it("fails closed before the install bridge while project context is not ready", async () => {
    const posted: unknown[] = [];
    const bridge = createHostBridge({
      webview: {
        postMessage: (message) => posted.push(message),
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    usePluginStore().setProjectContext("", "");

    await expect(usePluginService().inspectInstall("host-picker:folder"))
      .rejects.toThrow("当前工作区尚未就绪");

    expect(posted).toEqual([]);
  });

  it.each(["success", "failure", "cancel"] as const)(
    "keeps the newer inspection state when the older one completes with %s",
    async (olderOutcome) => {
      let listener: ((event: { data: unknown }) => void) | undefined;
      let sequence = 0;
      const posted: Array<{ type: string; requestId: string; payload: Record<string, string> }> = [];
      const bridge = createHostBridge({
        generateRequestId: () => `reverse-${++sequence}`,
        webview: {
          postMessage: (message) => {
            const request = message as typeof posted[number];
            posted.push(request);
            if (request.type === "plugin.install.cancel") queueMicrotask(() => listener?.({ data: {
              type: request.type,
              requestId: request.requestId,
              payload: { cancelled: true },
            } }));
          },
          addEventListener: (_type, handler) => { listener = handler; },
          removeEventListener: () => undefined,
        },
      });
      bridge.start();
      setHostBridgeForTesting(bridge);
      const store = usePluginStore();
      const service = usePluginService();
      const older = service.inspectInstall("host-picker:folder");
      const newer = service.inspectInstall("host-picker:package");
      const newerPlan = {
        planId: "plan-newer", projectKey: "local:default", projectRevision: "r1",
        sourceType: "package" as const, sourceLocation: "host-managed",
        packageHash: "sha256:newer", manifest: snapshot.manifest, schemas: snapshot.schemas,
      };
      listener?.({ data: {
        type: "plugin.install.inspect", requestId: "reverse-2", payload: newerPlan,
      } });
      await newer;

      if (olderOutcome === "success") {
        listener?.({ data: {
          type: "plugin.install.inspect", requestId: "reverse-1",
          payload: { ...newerPlan, planId: "plan-older", packageHash: "sha256:older" },
        } });
      } else {
        listener?.({ data: {
          type: "operation.failed", requestId: "reverse-1",
          payload: {
            code: olderOutcome === "cancel" ? "PLUGIN_REQUEST_CANCELLED" : "PLUGIN_INSPECT_FAILED",
            message: olderOutcome,
          },
        } });
      }
      await expect(older).rejects.toThrow();

      expect(store.installPlan?.planId).toBe("plan-newer");
      expect(store.busy).toBe(false);
      expect(store.lastError).toBeNull();
      if (olderOutcome === "success") {
        expect(posted.at(-1)).toMatchObject({
          type: "plugin.install.cancel", payload: { planId: "plan-older" },
        });
      }
    },
  );

  it("rejects commit locally after the inspected plan context becomes stale", async () => {
    const posted: Array<{ type: string; requestId?: string; payload?: unknown }> = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const plan = {
      planId: "plan-stale",
      projectKey: "local:default",
      projectRevision: "r1",
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
    service.openProjectContext("local:default", "r2");

    await expect(service.commitInstall(inspected)).rejects.toThrow("安装计划已失效");
    await expect(service.upgrade(snapshot, inspected)).rejects.toThrow("安装计划已失效");

    expect(posted.map((request) => request.type)).toEqual([
      "plugin.install.inspect",
      "plugin.install.cancel",
    ]);
    expect(store.installPlan).toBeNull();
    expect(store.lastError).toContain("安装计划已失效");
  });

  it("discards and cancels a late install plan after database context changes", async () => {
    const posted: Array<{ type: string; requestId: string; payload: Record<string, string> }> = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `plan-race-${++sequence}`,
      webview: {
        postMessage: (message) => {
          const request = message as typeof posted[number];
          posted.push(request);
          if (request.type === "plugin.install.cancel") {
            queueMicrotask(() => listener?.({ data: {
              type: request.type,
              requestId: request.requestId,
              payload: { cancelled: true },
            } }));
          }
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const store = usePluginStore();
    store.setProjectContext("local:default", "r0");
    const service = usePluginService();
    const pending = service.inspectInstall("host-picker:folder");

    service.openProjectContext("local:default", "r1");
    const stalePlan = {
      planId: "plan-r0",
      projectKey: "local:default",
      projectRevision: "r0",
      sourceType: "local-folder" as const,
      sourceLocation: "host-managed",
      packageHash: "sha256:stale",
      manifest: snapshot.manifest,
      schemas: snapshot.schemas,
    };
    listener?.({ data: {
      type: "plugin.install.inspect",
      requestId: "plan-race-1",
      payload: stalePlan,
    } });
    await expect(pending).rejects.toThrow("安装计划已失效");
    expect(store.installPlan).toBeNull();
    expect(posted[1]).toMatchObject({
      type: "plugin.install.cancel",
      payload: { planId: "plan-r0" },
    });
    await expect(service.commitInstall(stalePlan)).rejects.toThrow("安装计划已失效");
    expect(store.lastError).toContain("安装计划已失效");
    expect(posted.map((request) => request.type)).toEqual([
      "plugin.install.inspect",
      "plugin.install.cancel",
    ]);
  });

  it("invalidates the accepted plan as soon as a newer inspection begins", async () => {
    const posted: Array<{ type: string; requestId: string; payload: Record<string, string> }> = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `replace-plan-${++sequence}`,
      webview: {
        postMessage: (message) => {
          const request = message as typeof posted[number];
          posted.push(request);
          if (request.type === "plugin.install.cancel") {
            queueMicrotask(() => listener?.({ data: {
              type: request.type,
              requestId: request.requestId,
              payload: { cancelled: true },
            } }));
          }
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
    const firstPending = service.inspectInstall("host-picker:folder");
    const firstPlan = {
      planId: "plan-first",
      projectKey: "local:default",
      projectRevision: "r1",
      sourceType: "local-folder" as const,
      sourceLocation: "host-managed",
      packageHash: "sha256:first",
      manifest: snapshot.manifest,
      schemas: snapshot.schemas,
    };
    listener?.({ data: {
      type: "plugin.install.inspect",
      requestId: "replace-plan-1",
      payload: firstPlan,
    } });
    await firstPending;

    const secondPending = service.inspectInstall("host-picker:package");
    await expect(service.commitInstall(firstPlan)).rejects.toThrow("安装计划已失效");
    await vi.waitFor(() => expect(posted.filter(
      (request) => request.type === "plugin.install.inspect",
    )).toHaveLength(2));
    expect(posted.map((request) => request.type)).toEqual([
      "plugin.install.inspect",
      "plugin.install.cancel",
      "plugin.install.inspect",
    ]);
    listener?.({ data: {
      type: "plugin.install.inspect",
      requestId: "replace-plan-3",
      payload: { ...firstPlan, planId: "plan-second", packageHash: "sha256:second" },
    } });
    await secondPending;
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
