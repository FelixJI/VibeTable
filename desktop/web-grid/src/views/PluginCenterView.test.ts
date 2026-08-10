import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge } from "@/bridge/hostBridge";
import type { PluginAuditEvent, PluginSnapshot } from "@/contracts";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import { usePluginStore } from "@/stores/pluginStore";
import PluginCenterView from "./PluginCenterView.vue";

const blockedPlugin: PluginSnapshot = {
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

const auditEvent: PluginAuditEvent = {
  eventId: "audit-1",
  projectKey: blockedPlugin.projectKey,
  pluginId: blockedPlugin.pluginId,
  pluginVersion: blockedPlugin.version,
  packageHash: blockedPlugin.packageHash,
  eventType: "install",
  outcome: "succeeded",
  actionId: null,
  runId: null,
  actor: "local-user",
  risk: null,
  targetCollection: null,
  targetCount: null,
  startedAt: "2026-07-22T08:00:00Z",
  finishedAt: "2026-07-22T08:00:00Z",
  durationMs: 0,
  errorCode: null,
  details: {},
};

describe("PluginCenterView", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    usePluginStore().applyPlugin(blockedPlugin);
  });
  afterEach(() => setHostBridgeForTesting(null));

  it("projects canonical manifest blockers and permissions", () => {
    setHostBridgeForTesting(createHostBridge({ webview: { postMessage: () => undefined, addEventListener: () => undefined, removeEventListener: () => undefined } }));
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
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
            payload: [auditEvent],
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

  it("sends whole-plugin enable with canonical project identity and no presentation revision", async () => {
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
    expect(JSON.stringify(posted[0])).not.toContain("expectedRevision");
  });

  it("inspects a GitHub Release and explicitly cancels its host-managed plan", async () => {
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

  it("keeps upgrade mutation internal while retaining the native inspection path", async () => {
    const posted: unknown[] = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    let sequence = 0;
    const bridge = createHostBridge({
      generateRequestId: () => `upgrade-${++sequence}`,
      webview: {
        postMessage: (message) => {
          posted.push(message);
          const request = message as { type: string; requestId: string };
          const payload = request.type === "plugin.install.inspect"
            ? {
                planId: "plan-upgrade",
                projectKey: "local:default",
                projectRevision: "r2",
                sourceType: "package",
                sourceLocation: "host-managed",
                packageHash: "sha256:new",
                manifest: { ...blockedPlugin.manifest, version: "1.3.0" },
                schemas: {},
              }
            : { ...blockedPlugin, version: "1.3.0", packageHash: "sha256:new", revision: 8 };
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
    await Promise.resolve();
    await Promise.resolve();
    expect(posted).toHaveLength(1);
    expect(posted[0]).toMatchObject({
      type: "plugin.install.inspect",
      payload: { sourceLocation: "host-picker:package" },
    });
    expect(wrapper.get('[data-testid="plugin-install-plan"]').text()).toContain("1.3.0");

    expect(wrapper.get('[data-testid="plugin-upgrade-internal-only"]').text()).toContain(
      "升级提交暂未公开",
    );
    expect(wrapper.find('[data-testid="plugin-install-commit"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="plugin-rollback"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="plugin-uninstall"]').exists()).toBe(false);
    expect(posted).toHaveLength(1);
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
      projectRevision: "0",
      sourceType: "local-folder" as const,
      sourceLocation: "host-managed",
      packageHash: "sha256:approved",
      manifest: newPlugin.manifest,
      schemas: {},
    };
    const bridge = createHostBridge({
      generateRequestId: () => `install-${++sequence}`,
      webview: {
        postMessage: (message) => {
          posted.push(message);
          const request = message as { type: string; requestId: string };
          const payload = request.type === "plugin.install.inspect"
            ? plan
            : request.type === "plugin.install.commit"
              ? newPlugin
              : { ...newPlugin, status: "enabled", disabledReason: null, revision: 2 };
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

    expect(posted).toMatchObject([
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
      projectRevision: "0",
      sourceType: "package" as const,
      sourceLocation: "host-managed",
      packageHash: "sha256:enable-fails",
      manifest: installed.manifest,
      schemas: {},
    };
    const bridge = createHostBridge({
      generateRequestId: () => `enable-fails-${++sequence}`,
      webview: {
        postMessage: (message) => {
          posted.push(message);
          const request = message as { type: string; requestId: string };
          queueMicrotask(() => listener?.({ data: request.type === "plugin.install.commit"
            ? {
                type: request.type,
                requestId: request.requestId,
                payload: installed,
              }
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
    usePluginStore().setInstallPlan(plan);
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });

    await wrapper.get('[data-testid="plugin-install-commit"]').trigger("click");
    await flushPromises();

    expect(posted).toMatchObject([
      { type: "plugin.install.commit", payload: { planId: plan.planId } },
      {
        type: "plugin.lifecycle.setEnabled",
        payload: { pluginId: installed.pluginId, enabled: true },
      },
    ]);
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
      projectRevision: "0",
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
    const bridge = createHostBridge({
      generateRequestId: () => `blocked-install-${++sequence}`,
      webview: {
        postMessage: (message) => {
          posted.push(message);
          const request = message as { type: string; requestId: string };
          queueMicrotask(() => listener?.({ data: {
            type: request.type,
            requestId: request.requestId,
            payload: request.type === "plugin.install.inspect" ? plan : installed,
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

    expect(posted).toHaveLength(2);
    expect(posted).toMatchObject([
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
});
