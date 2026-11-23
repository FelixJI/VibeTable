import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import type { PluginAuditSummaryEvent, PluginSnapshot } from "@/contracts";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import { usePluginService } from "@/services/pluginService";
import { usePluginStore } from "@/stores/pluginStore";
import { useUiStore } from "@/stores/uiStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import PluginCenterView from "./PluginCenterView.vue";

const blockedPlugin: PluginSnapshot = {
  installationId: "record-clean",
  projectKey: "local:default",
  pluginId: "com.acme.clean",
  version: "1.2.0",
  packageHash: "sha256:abc123",
  sourceType: "package",
  sourceLocation: "host-managed",
  manifest: {
    $schema: "vibetable.plugin-manifest.v1",
    pluginId: "com.acme.clean",
    version: "1.2.0",
    displayName: { "zh-CN": "Clean rows" },
    description: {},
    compatibility: {},
    permissions: { data: ["customers:read", "customers:write"] },
    actions: [{
      actionId: "normalize",
      displayName: { "zh-CN": "规范化" },
      description: {},
      mode: "local",
      risk: "write",
      invocation: "manual",
      placements: ["table.toolbar"],
      requires: {},
      workerEntry: "dist/worker.js",
      formSchema: null,
      inputSchema: null,
      outputSchema: null,
    }],
    ui: {},
  },
  schemas: {},
  status: "disabled",
  disabledReason: "本地 worker 不可用；契约版本不兼容",
  blockingReasons: ["本地 worker 不可用", "契约版本不兼容"],
  revision: 7,
};

function auditSummary(eventId: string, eventType = "install", metadataLimited = false): PluginAuditSummaryEvent {
  return {
    eventId,
    startedAt: "2026-07-22T08:00:00Z",
    eventType,
    outcome: "succeeded",
    runId: null,
    packageHash: blockedPlugin.packageHash,
    durationMs: 12,
    metadataLimited,
  };
}

function openWorkspaceSession(session: ReturnType<typeof useWorkspaceSessionStore>): void {
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
}

function replyingBridge(response: (type: string) => unknown) {
  const posted: { type: string; requestId: string; payload: Record<string, unknown> }[] = [];
  let listener: ((event: { data: unknown }) => void) | undefined;
  let sequence = 0;
  const bridge = createHostBridge({
    generateRequestId: () => `review-${++sequence}`,
    webview: {
      postMessage: (message) => {
        const request = message as (typeof posted)[number];
        posted.push(request);
        queueMicrotask(() => listener?.({ data: {
          type: request.type, requestId: request.requestId, payload: response(request.type),
        } }));
      },
      addEventListener: (_type, handler) => { listener = handler; },
      removeEventListener: () => undefined,
    },
  });
  bridge.start();
  setHostBridgeForTesting(bridge);
  return { posted };
}

describe("PluginCenterView", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    usePluginStore().setProjectContext("local:default", "r2");
    usePluginStore().applyPlugin(blockedPlugin);
  });
  afterEach(() => setHostBridgeForTesting(null));

  it("shows a legacy package migration reason and refuses its commit before transport", async () => {
    const { posted } = replyingBridge(() => null);
    const plan = { planId: "legacy-plan", projectKey: "local:default", projectRevision: "r2",
      sourceType: "package" as const, sourceLocation: "old.vtplugin", packageHash: blockedPlugin.packageHash,
      manifest: blockedPlugin.manifest, schemas: {}, blockingReasons: ["plugin_worker_retired"] };
    usePluginStore().projectInstallPlan(plan);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    expect(wrapper.get('[data-testid="plugin-install-blocked"]').text()).toContain("旧 Worker 插件已停止支持");
    expect(wrapper.get('[data-testid="plugin-install-plan"]').text()).toContain("已阻断");
    expect(wrapper.get('[data-testid="plugin-install-commit"]').attributes("disabled")).toBeDefined();
    await expect(usePluginService().commitInstall(plan)).rejects.toThrow("插件需要迁移");
    expect(posted.some(request => request.type === "plugin.install.commit")).toBe(false);
    wrapper.unmount();
  });

  it("preserves enabled historical installations without allowing their blocked actions to start", async () => {
    const legacy: PluginSnapshot = { ...blockedPlugin, status: "enabled", disabledReason: null,
      blockingReasons: ["plugin_worker_retired"], revision: 8 };
    usePluginStore().applyPlugin(legacy);
    const { posted } = replyingBridge(type => type === "pluginPreservation.exportRequested"
      ? { cancelled: false, outputDisplayName: "historical.zip", missingPackages: 0 }
      : { available: true, reasons: [] });
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    try {
      expect(usePluginStore().selectedPlugin).toMatchObject({ status: "enabled", blockingReasons: ["plugin_worker_retired"] });
      // A synthetic event exercises the handler too, even on a disabled button.
      wrapper.get(".run-button").element.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await flushPromises();
      expect(posted.some(request => request.type === "plugin.action.describe" || request.type === "plugin.action.start")).toBe(false);
      expect(wrapper.get(".run-button").attributes("disabled")).toBeDefined();
      expect(wrapper.get(".blockers").text()).toContain("迁移到新版插件");
      expect(usePluginStore().selectedPlugin?.status).toBe("enabled");
      expect(wrapper.get('[data-testid="plugin-preservation-export"]').attributes("disabled")).toBeUndefined();
      await wrapper.get('[data-testid="plugin-preservation-export"]').trigger("click");
      await flushPromises();
      expect(posted.find(request => request.type === "pluginPreservation.exportRequested")?.payload).toEqual({
        pluginId: legacy.pluginId, installationId: legacy.installationId, installationRevision: legacy.revision,
      });
    } finally { wrapper.unmount(); }
  });

  it("exports the selected installation through the Host and cancels the exact request on unmount", async () => {
    const posted: { type: string; requestId?: string; payload: unknown }[] = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({ generateRequestId: () => `preserve-${++sequence}`, webview: {
      postMessage: message => posted.push(message as (typeof posted)[number]),
      addEventListener: (_type, handler) => { listener = handler; },
      removeEventListener: () => undefined,
    } });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    await wrapper.get('[data-testid="plugin-preservation-export"]').trigger("click");
    const request = posted.find(item => item.type === "pluginPreservation.exportRequested")!;
    expect(request.payload).toEqual({ pluginId: blockedPlugin.pluginId,
      installationId: blockedPlugin.installationId, installationRevision: blockedPlugin.revision });
    wrapper.unmount();
    expect(posted.at(-1)).toMatchObject({ type: "pluginPreservation.cancelRequested", payload: { targetRequestId: request.requestId } });
    listener?.({ data: { type: "operation.failed", requestId: request.requestId, payload: { code: "CANCELLED", message: "cancelled" } } });
    await flushPromises();
    bridge.stop();
  });

  it("projects canonical manifest blockers and permissions", () => {
    setHostBridgeForTesting(createHostBridge({ webview: { postMessage: () => undefined, addEventListener: () => undefined, removeEventListener: () => undefined } }));
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    expect(wrapper.find('[data-testid="legacy-layout-preservation"]').exists()).toBe(false);
    expect(wrapper.text()).toContain("本地 worker 不可用");
    expect(wrapper.text()).toContain("契约版本不兼容");
    expect(wrapper.text()).toContain("data:customers:write");
    expect(wrapper.text()).toContain("2 个阻断原因");
  });

  it("refreshes plugin audit times when the app regains focus in a new time zone", async () => {
    const previousTimeZone = process.env.TZ;
    process.env.TZ = "Asia/Shanghai";
    let listener: ((event: { data: unknown }) => void) | undefined;
    const bridge = createHostBridge({
      generateRequestId: () => "audit-list-1",
      webview: {
        postMessage: (message) => {
          const request = message as { type: string; requestId: string };
          queueMicrotask(() => listener?.({ data: {
            type: request.type,
            requestId: request.requestId,
            payload: { events: [auditSummary("audit-1")], nextCursor: null },
          } }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    try {
      await wrapper.get(".plugin-row").trigger("click");
      await flushPromises();
      expect(wrapper.get(".audit-log time").text()).toContain("16:00:00");

      process.env.TZ = "America/Los_Angeles";
      window.dispatchEvent(new Event("focus"));
      await wrapper.vm.$nextTick();

      expect(wrapper.get(".audit-log time").text()).toContain("01:00:00");
    } finally {
      wrapper.unmount();
      process.env.TZ = previousTimeZone;
    }
  });

  it("loads audit summaries page by page and reaches the explicit end state", async () => {
    const posted: { type: string; requestId: string; payload: Record<string, unknown> }[] = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `audit-page-${++sequence}`,
      webview: {
        postMessage: (message) => {
          const request = message as (typeof posted)[number];
          posted.push(request);
          queueMicrotask(() => listener?.({ data: {
            type: request.type,
            requestId: request.requestId,
            payload: request.payload.cursor === null
              ? { events: [auditSummary("page-1-a"), auditSummary("page-1-b", "model.progress")], nextCursor: "cursor-2" }
              : { events: [auditSummary("page-2-a", "transaction.preview")], nextCursor: null },
          } }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    try {
      await wrapper.get(".plugin-row").trigger("click");
      await flushPromises();
      const first = posted.find(request => request.type === "plugin.audit.page");
      expect(first?.payload).toEqual({ projectKey: "local:default", pluginId: blockedPlugin.pluginId, cursor: null });
      expect(wrapper.findAll(".audit-log article")).toHaveLength(2);
      expect(wrapper.get('[data-testid="plugin-audit-more"]').text()).toContain("加载更多");

      await wrapper.get('[data-testid="plugin-audit-more"]').trigger("click");
      await flushPromises();
      const second = posted.filter(request => request.type === "plugin.audit.page").at(-1);
      expect(second?.payload).toEqual({ projectKey: "local:default", pluginId: blockedPlugin.pluginId, cursor: "cursor-2" });
      expect(wrapper.findAll(".audit-log article")).toHaveLength(3);
      expect(wrapper.text()).toContain("已加载全部审计记录。");
      expect(wrapper.find('[data-testid="plugin-audit-more"]').exists()).toBe(false);
    } finally {
      wrapper.unmount();
    }
  });

  it("shows a retryable audit error instead of silently clearing the list", async () => {
    const posted: { type: string; requestId: string; payload: Record<string, unknown> }[] = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    let fail = true;
    const bridge = createHostBridge({
      generateRequestId: () => `audit-page-${++sequence}`,
      webview: {
        postMessage: (message) => {
          const request = message as (typeof posted)[number];
          posted.push(request);
          queueMicrotask(() => listener?.({ data: fail
            ? { type: "operation.failed", requestId: request.requestId,
                payload: { code: "PLUGIN_AUDIT_UNAVAILABLE", message: "audit unavailable" } }
            : { type: request.type, requestId: request.requestId,
                payload: { events: [auditSummary("retry-1")], nextCursor: null } } }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    try {
      await wrapper.get(".plugin-row").trigger("click");
      await flushPromises();
      expect(wrapper.get('[data-testid="plugin-audit-retry"]').text()).toContain("重试");
      expect(wrapper.text()).toContain("audit unavailable");
      expect(wrapper.text()).not.toContain("暂无审计记录");

      fail = false;
      await wrapper.get('[data-testid="plugin-audit-retry"]').trigger("click");
      await flushPromises();
      expect(wrapper.findAll(".audit-log article")).toHaveLength(1);
      expect(wrapper.text()).toContain("已加载全部审计记录。");
    } finally {
      wrapper.unmount();
    }
  });

  it("never accepts a late audit reply from a previously selected plugin", async () => {
    const other: PluginSnapshot = { ...blockedPlugin, pluginId: "com.acme.other", revision: 3 };
    usePluginStore().applyPlugin(other);
    const releasers = new Map<string, (payload: unknown) => void>();
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `audit-race-${++sequence}`,
      webview: {
        postMessage: (message) => {
          const request = message as { type: string; requestId: string; payload: { pluginId: string } };
          if (request.type !== "plugin.audit.page") {
            queueMicrotask(() => listener?.({ data: {
              type: request.type, requestId: request.requestId,
              payload: request.type === "plugin.catalog.list" ? [] : { events: [], nextCursor: null },
            } }));
            return;
          }
          releasers.set(request.payload.pluginId, payload => queueMicrotask(() => listener?.({ data: {
            type: request.type, requestId: request.requestId, payload,
          } })));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    try {
      await wrapper.get(".plugin-row").trigger("click"); // com.acme.clean, reply withheld.
      const rows = wrapper.findAll(".plugin-row");
      await rows[rows.length - 1].trigger("click"); // com.acme.other, resolves first.
      releasers.get(other.pluginId)?.({ events: [auditSummary("other-1")], nextCursor: null });
      await flushPromises();
      expect(wrapper.findAll(".audit-log article")).toHaveLength(1);
      expect(wrapper.get(".audit-log article code").text()).toContain(other.packageHash);

      // The late first-plugin reply arrives after the switch and must be dropped.
      releasers.get(blockedPlugin.pluginId)?.({ events: [auditSummary("stale-1"), auditSummary("stale-2")], nextCursor: null });
      await flushPromises();
      expect(wrapper.findAll(".audit-log article")).toHaveLength(1);
      expect(wrapper.text()).not.toContain("stale-1");
    } finally {
      wrapper.unmount();
    }
  });

  it("issues exactly one audit page request per selection gesture without double firing", async () => {
    const { posted } = replyingBridge(type => type === "plugin.audit.page"
      ? { events: [auditSummary("single-1")], nextCursor: null }
      : null);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    try {
      await wrapper.get(".plugin-row").trigger("click");
      await flushPromises();
      expect(posted.filter(request => request.type === "plugin.audit.page")).toHaveLength(1);
      expect(wrapper.findAll(".audit-log article")).toHaveLength(1);

      await wrapper.get(".plugin-row").trigger("click"); // Same plugin, explicit gesture.
      await flushPromises();
      expect(posted.filter(request => request.type === "plugin.audit.page")).toHaveLength(2);
      expect(wrapper.findAll(".audit-log article")).toHaveLength(1); // Reopened, not appended.
    } finally {
      wrapper.unmount();
    }
  });

  it("discards the previous workspace's late audit reply after a project switch", async () => {
    const releasers = new Map<string, (payload: unknown) => void>();
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `audit-ws-${++sequence}`,
      webview: {
        postMessage: (message) => {
          const request = message as { type: string; requestId: string; payload: Record<string, unknown> };
          if (request.type !== "plugin.audit.page") {
            queueMicrotask(() => listener?.({ data: {
              type: request.type, requestId: request.requestId,
              payload: request.type === "plugin.catalog.list" ? [] : { available: true, reasons: [] },
            } }));
            return;
          }
          releasers.set(`${request.payload.projectKey}:${request.payload.cursor ?? ""}`, payload =>
            queueMicrotask(() => listener?.({ data: {
              type: request.type, requestId: request.requestId, payload,
            } })));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    try {
      await wrapper.get(".plugin-row").trigger("click"); // local:default request, reply withheld.
      usePluginStore().setProjectContext("local:switched", "r3");
      usePluginStore().applyPlugin({ ...blockedPlugin, projectKey: "local:switched" }); // Workspace catalog rebuild.
      await flushPromises();
      releasers.get("local:switched:")?.({ events: [auditSummary("fresh-1")], nextCursor: null });
      await flushPromises();
      expect(wrapper.findAll(".audit-log article")).toHaveLength(1);

      releasers.get("local:default:")?.({ events: [auditSummary("stale-1")], nextCursor: null });
      await flushPromises();
      expect(wrapper.findAll(".audit-log article")).toHaveLength(1);
      expect(wrapper.text()).not.toContain("stale-1");
    } finally {
      wrapper.unmount();
    }
  });

  it("reprojects action context when a system theme arrives or changes", async () => {
    const themes: string[] = [];
    const bridge = createHostBridge({ webview: {
      postMessage: () => undefined,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    } });
    const request = vi.fn(async (_type: string, payload: { context: { theme: string } }) => {
      themes.push(payload.context.theme);
      return { available: true, reasons: [] };
    });
    setHostBridgeForTesting({ ...bridge, request } as HostBridge);
    usePluginStore().applyPlugin({ ...blockedPlugin, status: "enabled", disabledReason: null, blockingReasons: [], revision: 8 });
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    try {
      await wrapper.get(".run-button").trigger("click");
      await flushPromises();
      const ui = useUiStore();
      ui.applyThemeProjection({ preference: "system", effective: "dark", highContrast: false });
      await wrapper.get(".run-button").trigger("click");
      await flushPromises();
      ui.applyThemeProjection({ preference: "system", effective: "light", highContrast: false });
      await wrapper.get(".run-button").trigger("click");
      await flushPromises();
      expect(themes).toEqual(["light", "dark", "light"]);
    } finally {
      wrapper.unmount();
      bridge.stop();
    }
  });

  it("binds whole-plugin enable to the inspected installation identity and revision", async () => {
    const posted: unknown[] = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    const bridge = createHostBridge({
      generateRequestId: () => "enable-1",
      webview: {
        postMessage: (message) => {
          posted.push(message);
          queueMicrotask(() => listener?.({ data: {
            type: "plugin.lifecycle.setEnabled",
            requestId: "enable-1",
            payload: { ...blockedPlugin, status: "enabled", revision: 8, disabledReason: null },
          } }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    await wrapper.get('[data-testid="plugin-toggle"]').trigger("click");
    await Promise.resolve();
    expect(posted[0]).toMatchObject({
      type: "plugin.lifecycle.setEnabled",
      payload: { projectKey: "local:default", pluginId: "com.acme.clean", enabled: true },
    });
    expect(posted[0]).toMatchObject({ payload: { installationId: blockedPlugin.installationId, expectedRevision: blockedPlugin.revision } });
  });

  it("blocks lifecycle and action commands while a plugin request is in flight", async () => {
    setHostBridgeForTesting(createHostBridge({
      webview: {
        postMessage: () => undefined,
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
      },
    }));
    const store = usePluginStore();
    store.applyPlugin({
      ...blockedPlugin,
      status: "enabled",
      disabledReason: null,
      blockingReasons: [],
      revision: 8,
    });
    store.startBusy();
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });

    expect(wrapper.get('[data-testid="plugin-toggle"]').attributes("disabled")).toBeDefined();
    expect(wrapper.get(".action-row button.run-button").attributes("disabled")).toBeDefined();

    store.finishBusy();
    await wrapper.vm.$nextTick();

    expect(wrapper.get('[data-testid="plugin-toggle"]').attributes("disabled")).toBeUndefined();
    expect(wrapper.get(".action-row button.run-button").attributes("disabled")).toBeUndefined();
  });

  it("inspects a GitHub Release and explicitly cancels its host-managed plan", async () => {
    usePluginStore().setProjectContext("local:default", "r2");
    const posted: unknown[] = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `github-${++sequence}`,
      webview: {
        postMessage: (message) => {
          posted.push(message);
          const request = message as { type: string; requestId: string };
          queueMicrotask(() => listener?.({ data: {
            type: request.type,
            requestId: request.requestId,
            payload: request.type === "plugin.install.github.inspect"
              ? {
                  planId: "plan-github",
                  projectKey: "local:default",
                  projectRevision: "r2",
                  sourceType: "package",
                  sourceLocation: "host-managed",
                  packageHash: "sha256:github",
                  manifest: { ...blockedPlugin.manifest, version: "1.3.0" },
                  schemas: {},
                }
              : { cancelled: true },
          } }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });

    await wrapper.get('input[aria-label="GitHub 插件仓库"]').setValue("FelixJI/weread-plugin");
    await wrapper.get('[data-testid="plugin-install-github"]').trigger("click");
    await flushPromises();
    expect(posted[0]).toMatchObject({
      type: "plugin.install.github.inspect",
      payload: { repository: "FelixJI/weread-plugin" },
    });
    expect(wrapper.get('[data-testid="plugin-install-plan"]').text()).toContain("1.3.0");

    await wrapper.get('[data-testid="plugin-install-cancel"]').trigger("click");
    await flushPromises();
    expect(posted[1]).toMatchObject({
      type: "plugin.install.cancel",
      payload: { planId: "plan-github" },
    });
    expect(wrapper.find('[data-testid="plugin-install-plan"]').exists()).toBe(false);
  });

  it("shows the upgrade permission delta and commits only after approval", async () => {
    usePluginStore().setProjectContext("local:default", "r2");
    const posted: unknown[] = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    let authority: PluginSnapshot[] = [blockedPlugin];
    const bridge = createHostBridge({
      generateRequestId: () => `upgrade-${++sequence}`,
      webview: {
        postMessage: (message) => {
          posted.push(message);
          const request = message as { type: string; requestId: string };
          let payload: unknown;
          if (request.type === "plugin.catalog.list") payload = authority;
          else if (request.type === "plugin.install.cancel") payload = { cancelled: true };
          else if (request.type === "plugin.lifecycle.upgrade") {
            const upgraded: PluginSnapshot = { ...blockedPlugin, version: "1.3.0", packageHash: "sha256:new", revision: 8,
              manifest: { ...blockedPlugin.manifest, version: "1.3.0",
                permissions: { data: ["customers:read", "customers:write", "orders:read"] } } };
            authority = [upgraded]; payload = upgraded;
          } else if (request.type === "plugin.install.inspect") {
            payload = {
              planId: "plan-upgrade",
              targetInstallationId: blockedPlugin.installationId, targetRevision: blockedPlugin.revision,
              projectKey: "local:default",
              projectRevision: "r2",
              sourceType: "package",
              sourceLocation: "host-managed",
              packageHash: "sha256:new",
              manifest: { ...blockedPlugin.manifest, version: "1.3.0",
                permissions: { data: ["customers:read", "customers:write", "orders:read"] } },
              schemas: {},
            };
          } else throw new Error(`Unexpected request ${request.type}`);
          queueMicrotask(() => listener?.({ data: {
            type: request.type,
            requestId: request.requestId,
            payload,
          } }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    expect(wrapper.find('input[aria-label="插件包或文件夹位置"]').exists()).toBe(false);

    await wrapper.get('[data-testid="plugin-upgrade"]').trigger("click");
    await flushPromises();
    expect(posted).toHaveLength(1);
    expect(posted[0]).toMatchObject({
      type: "plugin.install.inspect",
      payload: { sourceLocation: "host-picker:package" },
    });
    expect(wrapper.get('[data-testid="plugin-install-plan"]').text()).toContain("1.3.0");

    expect(wrapper.find('[data-testid="plugin-upgrade-internal-only"]').exists()).toBe(false);
    expect(wrapper.get('[data-testid="plugin-upgrade-permission-delta"]').text()).toContain(
      "+ data:orders:read",
    );
    expect(wrapper.find('[data-testid="plugin-rollback"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="plugin-uninstall"]').exists()).toBe(true);
    expect(posted).toHaveLength(1);
    await wrapper.get('[data-testid="plugin-install-cancel"]').trigger("click");
    await flushPromises();
    expect(posted).toHaveLength(2);
    expect(posted[1]).toMatchObject({ type: "plugin.install.cancel", payload: { planId: "plan-upgrade" } });
    expect(usePluginStore().plugins[0]?.revision).toBe(7);

    await wrapper.get('[data-testid="plugin-upgrade"]').trigger("click");
    await flushPromises();
    expect(posted).toHaveLength(3);
    await wrapper.get('[data-testid="plugin-install-commit"]').trigger("click");
    await flushPromises();
    expect(posted.filter(message => !["plugin.catalog.list", "plugin.audit.page"].includes((message as { type: string }).type))).toHaveLength(4);
    expect(posted.filter(message => (message as { type: string }).type === "plugin.catalog.list")).toMatchObject([
      { payload: { projectKey: "local:default" } },
    ]);
    expect(posted[3]).toMatchObject({
      type: "plugin.lifecycle.upgrade",
      payload: { projectKey: "local:default", pluginId: blockedPlugin.pluginId,
        planId: "plan-upgrade", projectRevision: "r2" },
    });
    expect(usePluginStore().plugins[0]).toMatchObject({ revision: 8, version: "1.3.0",
      manifest: { permissions: { data: ["customers:read", "customers:write", "orders:read"] } } });
    expect(usePluginStore().installPlan).toBeNull();
  });

  it.each([false, true])("requires explicit uninstall confirmation and keeps private cleanup opt-in (%s)", async (cleanup) => {
    const posted: unknown[] = [];
    let authority: PluginSnapshot[] = [blockedPlugin];
    let sequence = 0;
    let listener: ((event: { data: unknown }) => void) | undefined;
    const bridge = createHostBridge({
      generateRequestId: () => `uninstall-approved-${++sequence}`,
      webview: {
        postMessage: (message) => {
          posted.push(message);
          const request = message as { type: string; requestId: string };
          if (request.type === "plugin.lifecycle.uninstall") authority = [];
          else if (request.type !== "plugin.catalog.list") throw new Error(`Unexpected request ${request.type}`);
          const payload = request.type === "plugin.catalog.list" ? authority
            : { uninstalled: true, privateSettingsRetained: !cleanup, cleanupPending: false };
          queueMicrotask(() => listener?.({ data: {
            type: request.type, requestId: request.requestId,
            payload,
          } }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    expect(wrapper.find('[data-testid="plugin-rollback"]').exists()).toBe(false);
    await wrapper.get('[data-testid="plugin-uninstall"]').trigger("click");
    expect(posted).toHaveLength(0);
    const privateCleanup = wrapper.get('[data-testid="plugin-uninstall-private-settings"]');
    expect((privateCleanup.element as HTMLInputElement).checked).toBe(false);
    if (cleanup) await privateCleanup.setValue(true);
    await wrapper.get('[data-testid="plugin-uninstall-confirm"]').trigger("click");
    await flushPromises();
    expect(posted.filter(message => (message as { type: string }).type === "plugin.lifecycle.uninstall")).toHaveLength(1);
    expect(posted.filter(message => (message as { type: string }).type === "plugin.catalog.list")).toMatchObject([
      { payload: { projectKey: "local:default" } },
    ]);
    expect(posted).toHaveLength(2);
    expect(posted[0]).toMatchObject({
      type: "plugin.lifecycle.uninstall",
      payload: { projectKey: "local:default", pluginId: blockedPlugin.pluginId,
        installationId: blockedPlugin.installationId, expectedRevision: blockedPlugin.revision, cleanupPrivateSettings: cleanup },
    });
    expect(usePluginStore().plugins).toHaveLength(0);
  });

  it("enables a new plugin after the user approves its permissions and install", async () => {
    const posted: unknown[] = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const newPlugin: PluginSnapshot = {
      ...blockedPlugin,
      pluginId: "com.acme.approved",
      manifest: { ...blockedPlugin.manifest, pluginId: "com.acme.approved" },
      status: "disabled",
      disabledReason: "disabled_by_user",
      blockingReasons: [],
      revision: 1,
    };
    const plan = {
      planId: "plan-approved",
      projectKey: "local:default",
      projectRevision: "r2",
      sourceType: "local-folder" as const,
      sourceLocation: "host-managed",
      packageHash: "sha256:approved",
      manifest: newPlugin.manifest,
      schemas: {},
    };
    let authority: PluginSnapshot[] = [blockedPlugin];
    const bridge = createHostBridge({
      generateRequestId: () => `install-${++sequence}`,
      webview: {
        postMessage: (message) => {
          posted.push(message);
          const request = message as { type: string; requestId: string };
          let payload: unknown;
          if (request.type === "plugin.install.inspect") payload = plan;
          else if (request.type === "plugin.install.commit") {
            authority = [...authority, newPlugin]; payload = newPlugin;
          } else if (request.type === "plugin.lifecycle.setEnabled") {
            const enabled: PluginSnapshot = { ...newPlugin, status: "enabled", disabledReason: null, revision: 2 };
            authority = authority.map(item => item.pluginId === enabled.pluginId ? enabled : item); payload = enabled;
          } else if (request.type === "plugin.catalog.list") payload = authority;
          else throw new Error(`Unexpected request ${request.type}`);
          queueMicrotask(() => listener?.({ data: {
            type: request.type,
            requestId: request.requestId,
            payload,
          } }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });

    await wrapper.get('[data-testid="plugin-install-folder"]').trigger("click");
    await flushPromises();
    await wrapper.get('[data-testid="plugin-install-commit"]').trigger("click");
    await flushPromises();

    expect(posted.filter(message => (message as { type: string }).type !== "plugin.catalog.list")).toMatchObject([
      {
        type: "plugin.install.inspect",
        payload: { sourceLocation: "host-picker:folder" },
      },
      {
        type: "plugin.install.commit",
        payload: { planId: "plan-approved" },
      },
      {
        type: "plugin.lifecycle.setEnabled",
        payload: {
          projectKey: "local:default",
          pluginId: "com.acme.approved",
          enabled: true,
        },
      },
    ]);
    expect(posted.filter(message => (message as { type: string }).type === "plugin.catalog.list")).toMatchObject([
      { payload: { projectKey: "local:default" } }, { payload: { projectKey: "local:default" } },
    ]);
    expect(
      usePluginStore().plugins.find((item) => item.pluginId === "com.acme.approved")?.status,
    ).toBe("enabled");
  });

  it("clears a consumed install plan when the convenience enable fails", async () => {
    const posted: unknown[] = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const installed: PluginSnapshot = {
      ...blockedPlugin,
      pluginId: "com.acme.enable-fails",
      manifest: {
        ...blockedPlugin.manifest,
        pluginId: "com.acme.enable-fails",
      },
      status: "disabled",
      disabledReason: "disabled_by_user",
      blockingReasons: [],
      revision: 1,
    };
    const plan = {
      planId: "plan-enable-fails",
      projectKey: "local:default",
      projectRevision: "r2",
      sourceType: "package" as const,
      sourceLocation: "host-managed",
      packageHash: "sha256:enable-fails",
      manifest: installed.manifest,
      schemas: {},
    };
    let authority: PluginSnapshot[] = [blockedPlugin];
    const bridge = createHostBridge({
      generateRequestId: () => `enable-fails-${++sequence}`,
      webview: {
        postMessage: (message) => {
          posted.push(message);
          const request = message as { type: string; requestId: string };
          if (request.type === "plugin.install.commit") authority = [...authority, installed];
          queueMicrotask(() => listener?.({ data: request.type === "plugin.install.inspect"
            ? {
                type: request.type,
                requestId: request.requestId,
                payload: plan,
              }
            : request.type === "plugin.install.commit"
            ? {
                type: request.type,
                requestId: request.requestId,
                payload: installed,
              }
            : request.type === "plugin.catalog.list"
            ? { type: request.type, requestId: request.requestId, payload: authority }
            : {
                type: "operation.failed",
                requestId: request.requestId,
                payload: { code: "PLUGIN_ENABLE_FAILED", message: "enable failed" },
              } }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    await usePluginService().inspectInstall("package");
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });

    await wrapper.get('[data-testid="plugin-install-commit"]').trigger("click");
    await flushPromises();

    expect(posted.filter((message) =>
      !["plugin.install.inspect", "plugin.catalog.list"].includes((message as { type: string }).type),
    )).toMatchObject([
      { type: "plugin.install.commit", payload: { planId: plan.planId } },
      {
        type: "plugin.lifecycle.setEnabled",
        payload: { pluginId: installed.pluginId, enabled: true },
      },
    ]);
    expect(posted.filter(message => (message as { type: string }).type === "plugin.catalog.list")).toMatchObject([
      { payload: { projectKey: "local:default" } }, { payload: { projectKey: "local:default" } },
    ]);
    expect(usePluginStore().lastError).toBe("enable failed");
    expect(usePluginStore().installPlan).toBeNull();
    expect(wrapper.find('[data-testid="plugin-install-plan"]').exists()).toBe(false);
    expect(
      usePluginStore().plugins.find((item) => item.pluginId === installed.pluginId),
    ).toMatchObject({ status: "disabled", disabledReason: "disabled_by_user" });
  });

  it("keeps a newly installed plugin disabled when the host reports blockers", async () => {
    const posted: unknown[] = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const plan = {
      planId: "plan-blocked",
      projectKey: "local:default",
      projectRevision: "r2",
      sourceType: "package" as const,
      sourceLocation: "host-managed",
      packageHash: "sha256:blocked",
      manifest: { ...blockedPlugin.manifest, pluginId: "com.acme.blocked-install" },
      schemas: {},
    };
    const installed = {
      ...blockedPlugin,
      pluginId: "com.acme.blocked-install",
      manifest: plan.manifest,
      revision: 1,
    };
    let authority: PluginSnapshot[] = [blockedPlugin];
    const bridge = createHostBridge({
      generateRequestId: () => `blocked-install-${++sequence}`,
      webview: {
        postMessage: (message) => {
          posted.push(message);
          const request = message as { type: string; requestId: string };
          if (request.type === "plugin.install.commit") authority = [...authority, installed];
          queueMicrotask(() => listener?.({ data: {
            type: request.type,
            requestId: request.requestId,
            payload: request.type === "plugin.install.inspect" ? plan
              : request.type === "plugin.catalog.list" ? authority : installed,
          } }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });

    await wrapper.get('[data-testid="plugin-install-package"]').trigger("click");
    await flushPromises();
    await wrapper.get('[data-testid="plugin-install-commit"]').trigger("click");
    await flushPromises();

    expect(posted.filter(message => (message as { type: string }).type !== "plugin.catalog.list")).toHaveLength(2);
    expect(posted.filter(message => (message as { type: string }).type === "plugin.catalog.list")).toMatchObject([
      { payload: { projectKey: "local:default" } },
    ]);
    expect(posted.filter(message => (message as { type: string }).type !== "plugin.catalog.list")).toMatchObject([
      { type: "plugin.install.inspect" },
      { type: "plugin.install.commit" },
    ]);
    expect(
      usePluginStore().plugins.find(
        (item) => item.pluginId === "com.acme.blocked-install",
      )?.status,
    ).toBe("disabled");
  });

  it("shows a reload prompt when the retained local development source changes", () => {
    usePluginStore().applyPlugin({
      ...blockedPlugin,
      sourceType: "local-folder",
      sourceChanged: true,
      revision: 8,
    });
    setHostBridgeForTesting(createHostBridge({ webview: {
      postMessage: () => undefined,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    } }));

    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });

    expect(wrapper.get('[data-testid="plugin-source-changed"]').text()).toContain("本地开发文件夹已变化");
    expect(wrapper.get('[data-testid="plugin-upgrade"]').text()).toContain("检查文件夹变更");
  });
  it("retires an uninstall confirmation on selection/revision changes and resets cleanup on cancel", async () => {
    const other = { ...blockedPlugin, pluginId: "com.acme.other", manifest: { ...blockedPlugin.manifest, pluginId: "com.acme.other" } };
    usePluginStore().applyPlugin(other);
    const { posted } = replyingBridge(() => []);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    await wrapper.get('[data-testid="plugin-uninstall"]').trigger("click");
    await wrapper.get('[data-testid="plugin-uninstall-private-settings"]').setValue(true);
    await wrapper.findAll(".plugin-row")[1]!.trigger("click");
    await flushPromises();
    expect(wrapper.find('[data-testid="plugin-uninstall-confirm"]').exists()).toBe(false);
    expect(posted.filter(item => item.type === "plugin.lifecycle.uninstall")).toHaveLength(0);
    await wrapper.get('[data-testid="plugin-uninstall"]').trigger("click");
    expect((wrapper.get('[data-testid="plugin-uninstall-private-settings"]').element as HTMLInputElement).checked).toBe(false);
    await wrapper.get('[data-testid="plugin-uninstall-private-settings"]').setValue(true);
    await wrapper.get('[data-testid="plugin-uninstall-cancel"]').trigger("click");
    await wrapper.get('[data-testid="plugin-uninstall"]').trigger("click");
    expect((wrapper.get('[data-testid="plugin-uninstall-private-settings"]').element as HTMLInputElement).checked).toBe(false);
    usePluginStore().applyPlugin({ ...other, revision: other.revision + 1 });
    await flushPromises();
    expect(wrapper.find('[data-testid="plugin-uninstall-confirm"]').exists()).toBe(false);
    expect(posted.filter(item => item.type === "plugin.lifecycle.uninstall")).toHaveLength(0);
  });

  it.each(["removed", "reinstalled", "revision"])("retires an inspected upgrade when the original target is %s", async transition => {
    const store = usePluginStore();
    const { posted } = replyingBridge(type => type === "plugin.install.inspect" ? {
      planId: "bound-upgrade", projectKey: blockedPlugin.projectKey, projectRevision: store.projectRevision,
      targetInstallationId: blockedPlugin.installationId, targetRevision: blockedPlugin.revision,
      sourceType: "package", sourceLocation: "host-managed", packageHash: "sha256:next",
      manifest: blockedPlugin.manifest, schemas: {},
    } : type === "plugin.install.cancel" ? { cancelled: true } : []);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    await wrapper.get('[data-testid="plugin-upgrade"]').trigger("click"); await flushPromises();
    expect(wrapper.find('[data-testid="plugin-install-plan"]').exists()).toBe(true);
    if (transition === "removed") store.removePlugin(blockedPlugin.pluginId);
    else store.replaceCatalog(blockedPlugin.projectKey, [{ ...blockedPlugin, installationId: transition === "reinstalled" ? "new-record" : blockedPlugin.installationId,
      revision: transition === "reinstalled" ? blockedPlugin.revision : blockedPlugin.revision + 1 }]);
    await flushPromises();
    expect(wrapper.find('[data-testid="plugin-install-plan"]').exists()).toBe(false);
    expect(posted.filter(item => item.type === "plugin.install.cancel")).toHaveLength(1);
    expect(posted.some(item => item.type === "plugin.lifecycle.upgrade" || item.type === "plugin.install.commit")).toBe(false);
    wrapper.unmount();
  });

  it("retires a late inspected plan after the catalog already replaced its target", async () => {
    const store = usePluginStore();
    const { posted } = replyingBridge(type => {
      if (type !== "plugin.install.inspect") return { cancelled: true };
      store.replaceCatalog(blockedPlugin.projectKey, [{ ...blockedPlugin, installationId: "replacement-record" }]);
      return { planId: "late-upgrade", projectKey: blockedPlugin.projectKey, projectRevision: store.projectRevision,
        targetInstallationId: blockedPlugin.installationId, targetRevision: blockedPlugin.revision,
        sourceType: "package", sourceLocation: "host-managed", packageHash: "sha256:next",
        manifest: blockedPlugin.manifest, schemas: {} };
    });
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    await wrapper.get('[data-testid="plugin-upgrade"]').trigger("click"); await flushPromises();
    expect(wrapper.find('[data-testid="plugin-install-plan"]').exists()).toBe(false);
    expect(posted.filter(item => item.type === "plugin.install.cancel")).toHaveLength(1);
    expect(posted.some(item => item.type === "plugin.lifecycle.upgrade" || item.type === "plugin.install.commit")).toBe(false);
    wrapper.unmount();
  });

  it.each([false, true])("compares complete permission declarations with semantic ordering (%s)", async (equivalent) => {
    const before = [{ collection: "A", operations: ["read", "query"], fields: ["title", "id"] },
      { collection: "B", operations: ["write"], fields: ["*"] }];
    const after = equivalent
      ? [{ fields: ["*"], operations: ["write"], collection: "B" },
        { fields: ["id", "title"], operations: ["query", "read"], collection: "A" }]
      : [{ collection: "A", operations: ["write"], fields: ["*"] },
        { collection: "B", operations: ["read", "query"], fields: ["title", "id"] }];
    usePluginStore().applyPlugin({ ...blockedPlugin, revision: 8,
      manifest: { ...blockedPlugin.manifest, permissions: { data: before } } });
    replyingBridge(() => ({ planId: "permission-plan", projectKey: "local:default", projectRevision: "r2",
      targetInstallationId: blockedPlugin.installationId, targetRevision: 8,
      sourceType: "package", sourceLocation: "host-managed", packageHash: "sha256:next",
      manifest: { ...blockedPlugin.manifest, version: "1.3.0", permissions: { data: after } }, schemas: {} }));
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    await wrapper.get('[data-testid="plugin-upgrade"]').trigger("click");
    await flushPromises();
    const delta = wrapper.find('[data-testid="plugin-upgrade-permission-delta"]');
    expect(delta.exists()).toBe(!equivalent);
    if (!equivalent) {
      expect(delta.text()).toContain('"collection":"A"');
      expect(delta.text()).toContain('"operations":["write"]');
      expect(delta.findAll("li")).toHaveLength(4);
    }
  });

  it("routes the plugin center Run button for an application to the full viewport", async () => {
    const application: PluginSnapshot = { ...blockedPlugin, status: "enabled", disabledReason: null, blockingReasons: [],
      manifest: { ...blockedPlugin.manifest, $schema: "vibetable.plugin-manifest.v2", compatibility: { pluginApi: "3.x" },
        actions: blockedPlugin.manifest.actions.map(action => ({ ...action, workerEntry: null,
          presentation: "application", entry: "ui/index.html", surfaceSrc: "https://app.plugins.vibetable.local/ui/index.html", surfaceToken: "owned" })) } };
    usePluginStore().replaceCatalog("local:default", [application]);
    const { posted } = replyingBridge(type => type === "plugin.catalog.list" ? [application] : { available: true, reasons: [] });
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    await wrapper.get(".run-button").trigger("click");
    await flushPromises();
    expect(useUiStore().activePluginApp).toEqual({ pluginId: application.pluginId, actionId: "normalize" });
    expect(usePluginStore().actionOpen).toBe(false);
    expect(posted.some(item => item.type === "plugin.action.start")).toBe(false);
  });

  it("shows the Host's unavailable reasons as a visible error when the Run describe is declined", async () => {
    const application: PluginSnapshot = { ...blockedPlugin, status: "enabled", disabledReason: null, blockingReasons: [],
      manifest: { ...blockedPlugin.manifest, $schema: "vibetable.plugin-manifest.v2", compatibility: { pluginApi: "3.x" },
        actions: blockedPlugin.manifest.actions.map(action => ({ ...action, workerEntry: null,
          presentation: "application", entry: "ui/index.html", surfaceSrc: "https://app.plugins.vibetable.local/ui/index.html", surfaceToken: "owned" })) } };
    usePluginStore().replaceCatalog("local:default", [application]);
    const { posted } = replyingBridge(type => type === "plugin.catalog.list" ? [application]
      : type === "plugin.action.describe" ? { available: false, reasons: ["插件界面暂不可用", "宿主未授权该动作"] }
      : { available: true, reasons: [] });
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    await wrapper.get(".run-button").trigger("click");
    await flushPromises();
    expect(posted.some(request => request.type === "plugin.action.describe")).toBe(true);
    expect(wrapper.get(".global-error").text()).toContain("插件界面暂不可用；宿主未授权该动作");
    expect(usePluginStore().actionOpen).toBe(false);
    expect(useUiStore().activePluginApp).toBeNull();
    expect(posted.some(request => request.type === "plugin.action.start")).toBe(false);
    wrapper.unmount();
  });

  it("cancels an uninstall confirmation when the same revision belongs to a reinstallation", async () => {
    const posted: unknown[] = [];
    setHostBridgeForTesting(createHostBridge({ webview: { postMessage: message => posted.push(message),
      addEventListener: () => undefined, removeEventListener: () => undefined } }));
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    await wrapper.get('[data-testid="plugin-uninstall"]').trigger("click");
    await wrapper.get('[data-testid="plugin-uninstall-private-settings"]').setValue(true);
    usePluginStore().replaceCatalog("local:default", [{ ...blockedPlugin, installationId: "reinstalled-record" }]);
    await flushPromises();
    expect(wrapper.find('[data-testid="plugin-uninstall-confirm"]').exists()).toBe(false);
    // The catalog swap legitimately reopens the audit summary page; the
    // check's intent is that no lifecycle side effect was issued.
    expect(posted.filter(message => (message as { type: string }).type !== "plugin.audit.page")).toEqual([]);
    await wrapper.get('[data-testid="plugin-uninstall"]').trigger("click");
    expect((wrapper.get('[data-testid="plugin-uninstall-private-settings"]').element as HTMLInputElement).checked).toBe(false);
    wrapper.unmount();
  });

  it("starts the pre-selected plugin's first audit page even when the catalog bootstrap fails", async () => {
    const posted: { type: string; payload: Record<string, unknown> }[] = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `mount-catalog-fail-${++sequence}`,
      webview: {
        postMessage: message => {
          const request = message as (typeof posted)[number] & { requestId: string };
          if (request.type === "plugin.catalog.list") {
            queueMicrotask(() => listener?.({ data: {
              type: "operation.failed", requestId: request.requestId,
              payload: { code: "PLUGIN_CATALOG_UNAVAILABLE", message: "catalog unavailable" },
            } }));
            return;
          }
          posted.push(request);
          queueMicrotask(() => listener?.({ data: {
            type: request.type, requestId: request.requestId,
            payload: request.type === "plugin.audit.page"
              ? { events: [auditSummary("independent-1")], nextCursor: null }
              : [],
          } }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    usePluginStore().selectPlugin(blockedPlugin.pluginId);
    const wrapper = mount(PluginCenterView); // Pre-selected before mount.
    try {
      await flushPromises();
      expect(wrapper.findAll(".audit-log article")).toHaveLength(1);
      expect(wrapper.text()).not.toContain("暂无审计记录");
      expect(posted.some(request => request.type === "plugin.audit.page")).toBe(true);
    } finally {
      wrapper.unmount();
      bridge.stop();
    }
  });

  it("starts the pre-selected plugin's first audit page even when the pending-cleanup read fails", async () => {
    const posted: { type: string; payload: Record<string, unknown> }[] = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `mount-cleanup-fail-${++sequence}`,
      webview: {
        postMessage: message => {
          const request = message as (typeof posted)[number] & { requestId: string };
          if (request.type === "plugin.cleanup.listPending") {
            queueMicrotask(() => listener?.({ data: {
              type: "operation.failed", requestId: request.requestId,
              payload: { code: "PLUGIN_CLEANUP_UNAVAILABLE", message: "cleanup unavailable" },
            } }));
            return;
          }
          posted.push(request);
          queueMicrotask(() => listener?.({ data: {
            type: request.type, requestId: request.requestId,
            payload: request.type === "plugin.audit.page"
              ? { events: [auditSummary("independent-2")], nextCursor: null }
              : request.type === "plugin.catalog.list" ? [blockedPlugin] : [],
          } }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    usePluginStore().selectPlugin(blockedPlugin.pluginId);
    const wrapper = mount(PluginCenterView);
    try {
      await flushPromises();
      expect(wrapper.findAll(".audit-log article")).toHaveLength(1);
      expect(wrapper.text()).not.toContain("暂无审计记录");
      expect(posted.some(request => request.type === "plugin.audit.page")).toBe(true);
    } finally {
      wrapper.unmount();
      bridge.stop();
    }
  });

  it("keeps the audit pages a user expanded while the mounted catalog bootstrap finishes", async () => {
    const releasers = new Map<string, (payload: unknown) => void>();
    const posted: { type: string; payload: Record<string, unknown> }[] = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `mount-race-${++sequence}`,
      webview: {
        postMessage: message => {
          const request = message as (typeof posted)[number] & { requestId: string; payload: { cursor: string | null } };
          if (request.type === "plugin.audit.page") {
            posted.push(request);
            queueMicrotask(() => listener?.({ data: {
              type: request.type, requestId: request.requestId,
              payload: request.payload.cursor === null
                ? { events: [auditSummary("race-1"), auditSummary("race-2", "model.progress")], nextCursor: "cursor-2" }
                : { events: [auditSummary("race-3", "transaction.preview")], nextCursor: null },
            } }));
            return;
          }
          // The bootstrap catalog and cleanup reads stay pending while the
          // user interacts with the already selected plugin.
          releasers.set(request.type, payload => queueMicrotask(() => listener?.({ data: {
            type: request.type, requestId: request.requestId, payload,
          } })));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    usePluginStore().selectPlugin(blockedPlugin.pluginId);
    const wrapper = mount(PluginCenterView); // autoLoad: the mount bootstrap runs.
    try {
      await flushPromises();
      await wrapper.get(".plugin-row").trigger("click"); // Same plugin, explicit gesture during the bootstrap.
      await flushPromises();
      await wrapper.get('[data-testid="plugin-audit-more"]').trigger("click");
      await flushPromises();
      expect(wrapper.findAll(".audit-log article")).toHaveLength(3);
      expect(posted.map(request => request.payload.cursor)).toEqual([null, null, "cursor-2"]);

      // The bootstrap replies settle late; the accumulated pages must win and
      // the mount continuation must not re-request the first page.
      releasers.get("plugin.catalog.list")?.([blockedPlugin]);
      releasers.get("plugin.cleanup.listPending")?.([]);
      await flushPromises();
      expect(wrapper.findAll(".audit-log article")).toHaveLength(3);
      expect(posted.map(request => request.payload.cursor)).toEqual([null, null, "cursor-2"]);
      expect(wrapper.find(".global-error").exists()).toBe(false);
    } finally {
      wrapper.unmount();
      bridge.stop();
    }
  });

  it("annotates bounded previews only when a summary reports metadataLimited", async () => {
    replyingBridge(type => type === "plugin.audit.page"
      ? { events: [auditSummary("full-1"), auditSummary("limited-1", "install", true)], nextCursor: null }
      : null);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    try {
      await wrapper.get(".plugin-row").trigger("click");
      await flushPromises();
      expect(wrapper.findAll(".audit-log article")).toHaveLength(2);
      expect(wrapper.get('[data-testid="plugin-audit-limited"]').text()).toBe("部分信息已简化");
      expect(wrapper.find(".global-error").exists()).toBe(false);
    } finally {
      wrapper.unmount();
    }
    replyingBridge(type => type === "plugin.audit.page"
      ? { events: [auditSummary("plain-1")], nextCursor: null }
      : null);
    const normal = mount(PluginCenterView, { props: { autoLoad: false } });
    try {
      await normal.get(".plugin-row").trigger("click");
      await flushPromises();
      expect(normal.findAll(".audit-log article")).toHaveLength(1);
      expect(normal.find('[data-testid="plugin-audit-limited"]').exists()).toBe(false);
    } finally {
      normal.unmount();
    }
  });

  it("shows a brief unknown-time note only for rows whose instant cannot be shown", async () => {
    replyingBridge(type => type === "plugin.audit.page"
      ? { events: [{ ...auditSummary("unknown-time"), startedAt: null }, auditSummary("known-time")], nextCursor: null }
      : null);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    try {
      await wrapper.get(".plugin-row").trigger("click");
      await flushPromises();
      const times = wrapper.findAll(".audit-log time");
      expect(times).toHaveLength(2);
      expect(times[0].text()).toBe("时间未知");
      expect(times[1].text()).not.toBe("时间未知");
    } finally {
      wrapper.unmount();
    }
  });

  it("clears the audit section on a phase-only workspace close and reopens when readable", async () => {
    const session = useWorkspaceSessionStore();
    openWorkspaceSession(session);
    const posted: { type: string; payload: Record<string, unknown> }[] = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `drain-${++sequence}`,
      webview: {
        postMessage: message => {
          const request = message as (typeof posted)[number] & { requestId: string };
          if (request.type === "plugin.audit.page") posted.push(request);
          queueMicrotask(() => listener?.({ data: {
            type: request.type, requestId: request.requestId,
            payload: request.type === "plugin.audit.page"
              ? { events: [auditSummary("drain-1")], nextCursor: null }
              : request.type === "plugin.catalog.list" ? [] : [],
          } }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    try {
      await wrapper.get(".plugin-row").trigger("click");
      await flushPromises();
      expect(wrapper.findAll(".audit-log article")).toHaveLength(1);

      // beginClose flips only the phase; no state or epoch changes. The old
      // scope's records must disappear immediately and no new scoped audit
      // read may be issued while the session is draining.
      expect(session.beginClose()).toBe(true);
      await wrapper.vm.$nextTick();
      expect(wrapper.findAll(".audit-log article")).toHaveLength(0);
      expect(wrapper.text()).toContain("暂无审计记录");
      const requestsBeforeReopen = posted.length;

      // The failed close restores the same session; the section reopens by itself.
      session.failSwitch("close failed");
      await flushPromises();
      expect(posted.length).toBeGreaterThan(requestsBeforeReopen);
      expect(posted.at(-1)?.payload.cursor).toBeNull();
      expect(wrapper.findAll(".audit-log article")).toHaveLength(1);
      expect(wrapper.text()).toContain("已加载全部审计记录。");
    } finally {
      wrapper.unmount();
      bridge.stop();
    }
  });

  it("renders a locally retired audit read as a scope transition, not a failure", async () => {
    const session = useWorkspaceSessionStore();
    openWorkspaceSession(session);
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    let withhold = true;
    const bridge = createHostBridge({
      generateRequestId: () => `retire-${++sequence}`,
      webview: {
        postMessage: message => {
          const request = message as { type: string; requestId: string };
          if (request.type === "plugin.audit.page" && withhold) return; // Reply withheld.
          queueMicrotask(() => listener?.({ data: {
            type: request.type, requestId: request.requestId,
            payload: request.type === "plugin.audit.page" ? { events: [auditSummary("reopen-1")], nextCursor: null }
              : request.type === "plugin.catalog.list" ? [] : [],
          } }));
        },
        addEventListener: (_type, handler) => { listener = handler; },
        removeEventListener: () => undefined,
      },
    });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    try {
      await wrapper.get(".plugin-row").trigger("click"); // First page request goes out; reply withheld.
      await flushPromises();
      expect(wrapper.text()).toContain("正在加载审计记录…");

      // The phase-only close retires the pending scoped request locally: the
      // rejection is not a user failure and the section settles empty.
      expect(session.beginClose()).toBe(true);
      await flushPromises();
      expect(wrapper.find("[data-testid='plugin-audit-retry']").exists()).toBe(false);
      expect(wrapper.find(".global-error").exists()).toBe(false);
      expect(wrapper.findAll(".audit-log article")).toHaveLength(0);

      // Lifting the withhold first: the sync scope watch reopens during failSwitch.
      withhold = false;
      session.failSwitch("close failed");
      await flushPromises();
      expect(wrapper.findAll(".audit-log article")).toHaveLength(1);
      expect(wrapper.text()).toContain("已加载全部审计记录。");
    } finally {
      wrapper.unmount();
      bridge.stop();
    }
  });

});
