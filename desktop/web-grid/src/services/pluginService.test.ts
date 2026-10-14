import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { flushPromises } from "@vue/test-utils";
import { createHostBridge } from "@/bridge/hostBridge";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
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

  it("keeps overlapping background catalog refreshes off the foreground busy gate", async () => {
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `catalog-overlap-${++sequence}`,
      webview: {
        postMessage: () => undefined,
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const store = usePluginStore();
    const service = usePluginService();
    const first = service.list();
    const second = service.list();
    const busyStates = [store.busy];
    listener?.({ data: {
      type: "plugin.catalog.list", requestId: "catalog-overlap-1", payload: [snapshot],
    } });
    await first;
    busyStates.push(store.busy);
    listener?.({ data: {
      type: "plugin.catalog.list", requestId: "catalog-overlap-2", payload: [snapshot],
    } });
    await second;
    busyStates.push(store.busy);
    bridge.stop();

    expect(busyStates).toEqual([false, false, false]);
    expect(store.lastError).toBeNull();
  });

  it.each(["success", "failure"])(
    "keeps an in-flight inspection busy across background %s",
    async (outcome) => {
      let listener: ((event: { data: unknown }) => void) | undefined;
      let sequence = 0;
      const bridge = createHostBridge({
        generateRequestId: () => `inspect-busy-${++sequence}`,
        webview: {
          postMessage: () => undefined,
          addEventListener: (_type, handler) => { listener = handler; },
          removeEventListener: () => undefined,
        },
      });
      bridge.start();
      setHostBridgeForTesting(bridge);
      const store = usePluginStore();
      const service = usePluginService();
      const inspection = service.inspectInstall("host-picker:folder");
      const busyStates = [store.busy];
      const refresh = service.list();
      busyStates.push(store.busy);
      if (outcome === "success") {
        listener?.({ data: {
          type: "plugin.catalog.list", requestId: "inspect-busy-2", payload: [snapshot],
        } });
        await refresh;
      } else {
        const rejected = expect(refresh).rejects.toThrow("catalog unavailable");
        listener?.({ data: {
          type: "operation.failed", requestId: "inspect-busy-2",
          payload: { code: "PLUGIN_CATALOG_UNAVAILABLE", message: "catalog unavailable" },
        } });
        await rejected;
      }
      busyStates.push(store.busy);
      listener?.({ data: {
        type: "plugin.install.inspect", requestId: "inspect-busy-1",
        payload: {
          planId: "plan-inspect-busy", projectKey: "local:default", projectRevision: "r1",
          sourceType: "package", sourceLocation: "host-managed",
          packageHash: snapshot.packageHash, manifest: snapshot.manifest, schemas: snapshot.schemas,
        },
      } });
      await inspection;
      busyStates.push(store.busy);
      bridge.stop();

      expect(busyStates).toEqual([true, true, true, false]);
      expect(store.lastError).toBe(outcome === "failure" ? "catalog unavailable" : null);
      expect(store.installPlan?.planId).toBe("plan-inspect-busy");
    },
  );

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

  it("refreshes the complete catalog after realtime recovery", async () => {
    let listener: ((event: { data: unknown }) => void) | undefined;
    const posted: unknown[] = [];
    const bridge = createHostBridge({
      generateRequestId: () => "catalog-recovered",
      webview: {
        postMessage: (message) => posted.push(message),
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const store = usePluginStore();
    store.replaceCatalog(snapshot.projectKey, [snapshot]);
    const service = usePluginService();
    service.init();

    listener?.({ data: { type: "realtime.recovered", payload: {} } });
    expect(posted).toEqual([{
      type: "plugin.catalog.list",
      requestId: "catalog-recovered",
      payload: { projectKey: snapshot.projectKey },
    }]);
    listener?.({ data: { type: "plugin.catalog.list", requestId: "catalog-recovered", payload: [] } });
    await vi.waitFor(() => expect(store.plugins).toEqual([]));
    service.dispose();
  });

  it.each(["project:b", "project:a"])("discards a late catalog after switching to %s", async (nextProject) => {
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
    store.setProjectContext(nextProject, "r2");

    listener?.({
      data: { type: "plugin.catalog.list", requestId: "catalog-a", payload: [snapshot] },
    });
    await pending;

    expect(store.projectKey).toBe(nextProject);
    expect(store.plugins).toEqual([]);
  });

  it("keeps the next project context clean when a retired-generation shared read settles late", async () => {
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `generation-race-${++sequence}`,
      webview: {
        postMessage: () => undefined,
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const store = usePluginStore();
    const service = usePluginService();
    const stale = service.listAudit(snapshot.pluginId);
    store.setProjectContext("project:b", "r2");
    const current = service.retryCleanup(snapshot.pluginId);

    listener?.({
      data: {
        type: "operation.failed",
        requestId: "generation-race-1",
        payload: { code: "PLUGIN_AUDIT_UNAVAILABLE", message: "audit unavailable" },
      },
    });
    await expect(stale).rejects.toThrow("audit unavailable");

    expect(store.lastError).toBeNull();
    expect(store.busy).toBe(true);

    listener?.({
      data: {
        type: "plugin.lifecycle.uninstall", requestId: "generation-race-2",
        payload: { uninstalled: false, privateSettingsRetained: false, cleanupPending: false },
      },
    });
    await current;
    expect(store.busy).toBe(false);
    expect(store.lastError).toBeNull();
    bridge.stop();
  });

  it("settles the old generation when the same project key reopens without a new request", async () => {
    let listener: ((event: { data: unknown }) => void) | undefined;
    const bridge = createHostBridge({
      generateRequestId: () => "same-key-retired",
      webview: {
        postMessage: () => undefined,
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const store = usePluginStore();
    const service = usePluginService();
    const stale = service.listAudit(snapshot.pluginId);

    store.setProjectContext("local:default", "r2");

    listener?.({
      data: {
        type: "operation.failed",
        requestId: "same-key-retired",
        payload: { code: "PLUGIN_AUDIT_UNAVAILABLE", message: "audit unavailable" },
      },
    });
    await expect(stale).rejects.toThrow("audit unavailable");

    expect(store.busy).toBe(false);
    expect(store.lastError).toBeNull();
    bridge.stop();
  });

  it("does not let a same-key retired terminal clear the busy of a new generation request", async () => {
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `same-key-race-${++sequence}`,
      webview: {
        postMessage: () => undefined,
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const store = usePluginStore();
    const service = usePluginService();
    const stale = service.listAudit(snapshot.pluginId);
    store.setProjectContext("local:default", "r2");
    const current = service.retryCleanup(snapshot.pluginId);
    expect(store.busy).toBe(true);

    listener?.({
      data: {
        type: "operation.failed",
        requestId: "same-key-race-1",
        payload: { code: "PLUGIN_AUDIT_UNAVAILABLE", message: "audit unavailable" },
      },
    });
    await expect(stale).rejects.toThrow("audit unavailable");

    expect(store.lastError).toBeNull();
    expect(store.busy).toBe(true);

    listener?.({
      data: {
        type: "plugin.lifecycle.uninstall", requestId: "same-key-race-2",
        payload: { uninstalled: false, privateSettingsRetained: false, cleanupPending: false },
      },
    });
    await current;
    expect(store.busy).toBe(false);
    expect(store.lastError).toBeNull();
    bridge.stop();
  });

  it("settles an in-flight catalog read locally on close without a visible plugin failure", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    session.setWorkspaces([{
      contractVersion: "2.0",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      displayName: "E2E",
      selectedRoot: "D:\\E2E",
      activityRoot: null,
      storageKind: "fixed",
      coordinationStrength: "strong",
      lastOpenedAt: null,
      lastKnownHealth: "healthy",
      lastSnapshotAt: null,
      lastSyncAt: null,
      pendingSync: false,
    }]);
    session.applySession({
      contractVersion: "2.0",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      sessionEpoch: 7,
      state: "openedWritable",
      openMode: "writable",
      writable: true,
      provisional: false,
      phase: "idle",
      errorCode: null,
    });
    let listener: ((event: { data: unknown }) => void) | undefined;
    const bridge = createHostBridge({
      generateRequestId: () => "catalog-close-1",
      timeoutMs: 50,
      webview: {
        postMessage: () => undefined,
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const store = usePluginStore();
    const service = usePluginService();
    const pending = service.list();

    expect(session.beginClose()).toBe(true);

    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    listener?.({
      data: {
        type: "operation.failed",
        requestId: "catalog-close-1",
        payload: { code: "PLUGIN_OPERATION_FAILED", message: "Plugin operation failed." },
      },
    });
    await Promise.resolve();
    expect(store.lastError).toBeNull();
    // The retired busy state is settled by the scope/reset lifecycle.
    service.openProjectContext("", "");
    expect(store.busy).toBe(false);
    bridge.stop();
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
      "plugin.catalog.list": [],
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
      { type: "plugin.catalog.list", payload: { projectKey: "local:default" } },
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

  it("reopens a closed 3.x surface with the fresh token from a refreshed catalog", async () => {
    const posted: Array<{ type: string; requestId?: string }> = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let requestId = 0;
    let currentToken = "token-first";
    const nativeSnapshot = (token: string): PluginSnapshot => ({
      ...snapshot,
      manifest: {
        ...snapshot.manifest,
        $schema: "vibetable.plugin-manifest.v2",
        compatibility: { pluginApi: "3.x" },
        actions: [{ ...snapshot.manifest.actions[0], actionId: "open-editor", workerEntry: null }],
        ui: {
          customViews: [{
            viewId: "editor",
            actionId: "open-editor",
            entry: "ui/index.html",
            src: "https://surface.plugins.vibetable.local/ui/index.html",
            surfaceToken: token,
            title: "editor",
          }],
        },
      },
    });
    const bridge = createHostBridge({
      generateRequestId: () => `request-${++requestId}`,
      webview: {
        postMessage: message => {
          const request = message as { type: string; requestId?: string };
          posted.push(request);
          const reply = request.type === "plugin.catalog.list"
            ? { type: request.type, requestId: request.requestId, payload: [nativeSnapshot(currentToken)] }
            : { type: request.type, requestId: request.requestId, payload: { available: true, reasons: [] } };
          queueMicrotask(() => listener?.({ data: reply }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const service = usePluginService();
    service.init();
    const context = createPluginCommandContext({
      projectKey: "local:default",
      collection: "orders",
      locale: "zh-CN",
      theme: "light",
      density: "comfortable",
    });
    // Real flows start from the workspace's initial catalog load.
    await service.list();

    await service.describeAction("com.acme.clean", "open-editor", context);
    expect(usePluginStore().describedAction?.surface?.surfaceToken).toBe("token-first");

    // The page closes: the host retires token-first. The next open must refresh
    // the catalog first and open with the freshly projected token.
    usePluginStore().closeAction();
    expect(usePluginStore().describedAction).toBeNull();
    currentToken = "token-second";
    await service.describeAction("com.acme.clean", "open-editor", context);

    const describeIndexes = posted
      .map((message, index) => message.type === "plugin.action.describe" ? index : -1)
      .filter(index => index >= 0);
    const listIndexes = posted
      .map((message, index) => message.type === "plugin.catalog.list" ? index : -1)
      .filter(index => index >= 0);
    expect(describeIndexes.length).toBe(2);
    expect(listIndexes.length).toBe(3);
    expect(listIndexes[listIndexes.length - 1]).toBeLessThan(describeIndexes[1]);
    expect(usePluginStore().describedAction?.surface?.surfaceToken).toBe("token-second");
    service.dispose();
  });
  function catalogHarness(current: readonly PluginSnapshot[]) {
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const posted: Array<{ type: string; requestId: string; payload: unknown }> = [];
    let automaticLists = false;
    let authority = current;
    const reply = (request: (typeof posted)[number], payload: unknown) => listener?.({ data: {
      type: request.type, requestId: request.requestId, payload } });
    const bridge = createHostBridge({ generateRequestId: () => `catalog-${++sequence}`,
      webview: { postMessage(message) {
        const request = message as (typeof posted)[number]; posted.push(request);
        if (automaticLists && request.type === "plugin.catalog.list") queueMicrotask(() => reply(request, authority));
      }, addEventListener: (_type, handler) => { listener = handler; }, removeEventListener: () => undefined } });
    bridge.start(); setHostBridgeForTesting(bridge);
    return { posted, reply, fail(request: (typeof posted)[number], message: string, code = "PLUGIN_OPERATION_FAILED") { listener?.({ data: {
      type: "operation.failed", requestId: request.requestId, payload: { code, message } } }); }, auto(value: readonly PluginSnapshot[]) { authority = value; automaticLists = true; },
      event(value: PluginSnapshot) { listener?.({ data: { type: "plugin.catalog.changed", payload: {
        contract: "vibetable.plugin-event.v1", eventType: "plugin.catalog.changed", projectKey: value.projectKey,
        entityId: value.pluginId, revision: value.revision, snapshot: value } } }); } };
  }

  it.each([
    [false, false, true], [true, false, true],
    [false, true, true], [true, true, true],
    [false, false, false], [true, true, false],
  ])("refreshes when a retired notification read ends before the write (write=%s, cancelled=%s, refresh=%s)", async (writeSucceeds, readCancelled, refreshSucceeds) => {
    const current = { ...snapshot, installationId: "a", status: "disabled" as const, revision: 2 };
    const unrelated = { ...snapshot, pluginId: "com.acme.other", installationId: "b" };
    const changed = { ...unrelated, revision: 2 };
    const harness = catalogHarness([current, changed]);
    const store = usePluginStore(); store.applyPlugin({ ...snapshot, installationId: "a" }); store.applyPlugin(unrelated);
    const first = usePluginService(); const second = usePluginService(); second.init();
    harness.event(current); await flushPromises();
    const initialRead = harness.posted[0]!;
    const write = first.setEnabled(unrelated, true);
    const completion = writeSucceeds ? write : expect(write).rejects.toThrow("write failed");
    if (readCancelled) harness.fail(initialRead, "catalog cancelled", "PLUGIN_REQUEST_CANCELLED");
    else harness.reply(initialRead, [current, changed]);
    await flushPromises();
    expect(harness.posted).toHaveLength(2);
    expect(store.lastError).toBeNull();
    if (writeSucceeds) harness.reply(harness.posted[1]!, changed);
    else harness.fail(harness.posted[1]!, "write failed");
    await completion; await flushPromises();
    const reads = harness.posted.filter(request => request.type === "plugin.catalog.list");
    expect(reads).toHaveLength(2);
    if (refreshSucceeds) harness.reply(reads[1]!, [current, changed]);
    else harness.fail(reads[1]!, "refresh failed");
    await flushPromises();
    expect(store.plugins.find(plugin => plugin.pluginId === current.pluginId)?.status).toBe(refreshSucceeds ? "disabled" : "enabled");
    expect(store.lastError).toBe(writeSucceeds ? refreshSucceeds ? null : "refresh failed" : "write failed");
    expect(harness.posted.filter(request => request.type === "plugin.lifecycle.setEnabled")).toHaveLength(1);
    expect(harness.posted).toHaveLength(3);
    second.dispose();
  });

  it.each([true, false])("does not refresh the retired workspace when the write settles (success=%s)", async (succeeds) => {
    const current = { ...snapshot, installationId: "a", revision: 2 };
    const harness = catalogHarness([current]);
    const first = usePluginService(); const second = usePluginService(); second.init();
    harness.event(current); await flushPromises();
    const write = first.setEnabled(snapshot, true);
    const completion = succeeds ? write : expect(write).rejects.toThrow("old write failed");
    harness.reply(harness.posted[0]!, [current]); await flushPromises();
    const store = usePluginStore(); store.setProjectContext("local:next", "r2"); store.fail(new Error("next error"));
    if (succeeds) harness.reply(harness.posted[1]!, current);
    else harness.fail(harness.posted[1]!, "old write failed");
    await completion; await flushPromises();
    expect(harness.posted).toHaveLength(2);
    expect(store.plugins).toEqual([]);
    expect(store.lastError).toBe("next error");
    second.dispose();
  });

  it.each([[false, true], [false, false], [true, true], [true, false]])("preserves a consumed catalog notification when lifecycle settles first (write=%s, refresh=%s)", async (writeSucceeds, refreshSucceeds) => {
    const current = { ...snapshot, installationId: "a", status: "disabled" as const, revision: 2 };
    const unrelated = { ...snapshot, pluginId: "com.acme.other", installationId: "b" };
    const harness = catalogHarness([current]);
    const store = usePluginStore(); store.applyPlugin({ ...snapshot, installationId: "a" });
    const first = usePluginService(); const second = usePluginService(); second.init();
    const write = first.setEnabled(unrelated, true);
    const completion = writeSucceeds ? write : expect(write).rejects.toThrow("write failed");
    harness.event(current); await flushPromises();
    const initialRead = harness.posted.find(request => request.type === "plugin.catalog.list")!;
    if (writeSucceeds) harness.reply(harness.posted[0]!, { ...unrelated, revision: 2 });
    else harness.fail(harness.posted[0]!, "write failed");
    await completion;
    harness.reply(initialRead, [current]); await flushPromises();
    const reads = harness.posted.filter(request => request.type === "plugin.catalog.list");
    expect(reads).toHaveLength(2);
    if (refreshSucceeds) harness.reply(reads[1]!, [current]);
    else harness.fail(reads[1]!, "refresh failed");
    await flushPromises();
    expect(store.plugins[0]?.status).toBe(refreshSucceeds ? "disabled" : "enabled");
    expect(store.lastError).toBe(writeSucceeds ? refreshSucceeds ? null : "refresh failed" : "write failed");
    expect(harness.posted.filter(request => request.type === "plugin.lifecycle.setEnabled")).toHaveLength(1);
    expect(harness.posted).toHaveLength(3);
    second.dispose();
  });

  it("shares catalog read retirement across two service instances", async () => {
    const old = { ...snapshot, installationId: "retired", revision: 4 };
    const current = { ...snapshot, installationId: "current", revision: 2 };
    const harness = catalogHarness([current]);
    const first = usePluginService(); const second = usePluginService();
    const oldRead = first.list(); const newRead = second.list();
    harness.reply(harness.posted[1]!, [current]); await newRead;
    harness.reply(harness.posted[0]!, [old]); await oldRead;
    expect(usePluginStore().plugins[0]?.installationId).toBe("current");
  });

  it("treats old, unknown and new catalog notifications as bounded invalidations of the authority", async () => {
    const old = { ...snapshot, installationId: "retired", revision: 4 };
    const current = { ...snapshot, installationId: "current", revision: 2 };
    const harness = catalogHarness([current]); harness.auto([current]);
    const store = usePluginStore(); store.applyPlugin(old);
    const task: PluginTaskSnapshot = { taskId: "old-task", runId: "old-run", pluginId: old.pluginId,
      pluginVersion: old.version, installationId: old.installationId, installationRevision: 4,
      actionId: "normalize", projectKey: old.projectKey, collection: "orders", targetCount: 1,
      risk: "write", state: "running", cancelRequested: false, result: null, error: null };
    store.applyTask(task); store.removePlugin(old.pluginId);
    const service = usePluginService(); service.init(); await service.list(); harness.posted.length = 0;
    for (const value of [old, { ...old, installationId: "never-seen", revision: 9 },
      { ...current, revision: 1 }, current]) harness.event(value);
    await flushPromises();
    expect(store.plugins[0]?.installationId).toBe("current");
    expect(store.taskForId(task.taskId)).toBeNull();
    expect(harness.posted.filter(request => request.type === "plugin.catalog.list")).toHaveLength(1);
    const replacement = { ...current, installationId: "replacement", revision: 1 };
    harness.auto([replacement]); harness.event(replacement); await flushPromises();
    expect(store.plugins[0]?.installationId).toBe("replacement");
    harness.auto([]); harness.event(replacement); await flushPromises();
    expect(store.plugins).toEqual([]);
    service.dispose();
  });

  it("retires old lifecycle replies that follow a new installation through another service", async () => {
    const old = { ...snapshot, installationId: "retired", revision: 4 };
    const current = { ...snapshot, installationId: "current", revision: 2 };
    const harness = catalogHarness([current]); harness.auto([current]);
    const store = usePluginStore(); store.applyPlugin(old);
    const first = usePluginService(); const second = usePluginService();
    const oldEnable = first.setEnabled(old, true);
    const uninstall = second.uninstall(old, false);
    harness.reply(harness.posted[1]!, { uninstalled: true, privateSettingsRetained: true }); await uninstall;
    const newEnable = second.setEnabled(current, true);
    harness.reply(harness.posted.filter(request => request.type === "plugin.lifecycle.setEnabled").pop()!, current); await newEnable;
    harness.reply(harness.posted[0]!, old); await oldEnable; await flushPromises();
    expect(store.plugins[0]?.installationId).toBe("current");
  });

  it("invalidates reads begun during a lifecycle operation when that operation completes", async () => {
    const previous = { ...snapshot, installationId: "same-record", revision: 1 };
    const current = { ...previous, revision: 2 };
    const harness = catalogHarness([current]);
    const store = usePluginStore(); store.applyPlugin(previous);
    const first = usePluginService(); const second = usePluginService();
    const enable = first.setEnabled(previous, true);
    const olderRead = second.list();
    harness.auto([current]);
    harness.reply(harness.posted[0]!, current); await enable;
    harness.reply(harness.posted[1]!, [previous]); await olderRead; await flushPromises();
    expect(store.plugins[0]?.revision).toBe(2);
  });

});
