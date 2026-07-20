import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge } from "@/bridge/hostBridge";
import type { PluginSnapshot } from "@/contracts";
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
      placements: ["table.toolbar"], requires: {}, entryFlow: null, workerEntry: "dist/worker.js",
      formSchema: null, inputSchema: "schemas/input.json", outputSchema: null,
    }],
    flows: [], ui: {},
  },
  flowRequirements: [],
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
      databaseId: "directus",
      table: "orders",
      schemaRevision: "schema-r1",
      dataRevision: 4,
      normalizedQuery: { sort: ["-created_at"] },
    };

    const context = createPluginCommandContext({
      projectKey: "remote:https://directus.example.test",
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
      flowRequirements: snapshot.flowRequirements,
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

  it("uses canonical install, binding, action and uninstall use-case payloads", async () => {
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
      flowRequirements: snapshot.flowRequirements,
      schemas: snapshot.schemas,
    };
    const responses: Record<string, unknown> = {
      "plugin.install.inspect": plan,
      "plugin.externalFlow.bind": {
        projectKey: "local:default", pluginId: "com.acme.clean", logicalFlowId: "clean",
        ownership: "external", directusFlowUuid: "flow-1", rollbackFlowUuid: null,
        rollbackContractVersion: null, rollbackDefinitionHash: null, triggerType: "manual",
        contractVersion: "1", installedDefinitionHash: null, observedDefinitionHash: "hash",
        revision: 1, health: "healthy", driftStatus: "not-applicable", lastError: null,
      },
      "plugin.action.describe": { available: true, reasons: [] },
      "plugin.lifecycle.uninstall": {
        managedFlowsRemoved: 0, externalFlowsUnbound: 1, uninstalled: true,
        privateSettingsRetained: false, cleanupPending: false,
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
    await service.bindExternalFlow({
      pluginId: "com.acme.clean", logicalFlowId: "clean", directusFlowUuid: "flow-1",
      acceptsUnknownSideEffects: true,
    });
    const context = createPluginCommandContext({
      projectKey: "local:default", locale: "zh-CN", theme: "light", density: "compact",
    });
    await service.describeAction("com.acme.clean", "normalize", context);
    await service.uninstall(snapshot, true);

    expect(posted.map(({ type, payload }) => ({ type, payload }))).toEqual([
      { type: "plugin.install.inspect", payload: {
        projectKey: "local:default", projectRevision: "r1", sourceLocation: "C:/plugins/clean.vtplugin",
      } },
      { type: "plugin.externalFlow.bind", payload: {
        projectKey: "local:default", pluginId: "com.acme.clean", logicalFlowId: "clean",
        directusFlowUuid: "flow-1", acceptsUnknownSideEffects: true,
      } },
      { type: "plugin.action.describe", payload: {
        projectKey: "local:default", pluginId: "com.acme.clean", actionId: "normalize", context,
      } },
      { type: "plugin.lifecycle.uninstall", payload: {
        projectKey: "local:default", pluginId: "com.acme.clean", cleanupPrivateSettings: true,
      } },
    ]);
  });
});
