import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import type { PluginAuditEvent, PluginSnapshot } from "@/contracts";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import { usePluginService } from "@/services/pluginService";
import { usePluginStore } from "@/stores/pluginStore";
import { useUiStore } from "@/stores/uiStore";
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
    usePluginStore().applyPlugin({ ...blockedPlugin, status: "enabled", revision: 8 });
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
                manifest: { ...blockedPlugin.manifest, version: "1.3.0",
                  permissions: { data: ["customers:read", "customers:write", "orders:read"] } },
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
    expect(posted).toHaveLength(4);
    expect(posted[3]).toMatchObject({
      type: "plugin.lifecycle.upgrade",
      payload: { projectKey: "local:default", pluginId: blockedPlugin.pluginId,
        planId: "plan-upgrade", projectRevision: "r2" },
    });
    expect(usePluginStore().plugins[0]?.revision).toBe(8);
    expect(usePluginStore().installPlan).toBeNull();
  });

  it.each([false, true])("requires explicit uninstall confirmation and keeps private cleanup opt-in (%s)", async (cleanup) => {
    const posted: unknown[] = [];
    let listener: ((event: { data: unknown }) => void) | undefined;
    const bridge = createHostBridge({
      generateRequestId: () => "uninstall-approved",
      webview: {
        postMessage: (message) => {
          posted.push(message);
          const request = message as { type: string; requestId: string };
          queueMicrotask(() => listener?.({ data: {
            type: request.type, requestId: request.requestId,
            payload: { uninstalled: true, privateSettingsRetained: !cleanup, cleanupPending: false },
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
    expect(posted).toHaveLength(1);
    expect(posted[0]).toMatchObject({
      type: "plugin.lifecycle.uninstall",
      payload: { projectKey: "local:default", pluginId: blockedPlugin.pluginId,
        cleanupPrivateSettings: cleanup },
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
      projectRevision: "r2",
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
      (message as { type?: string }).type !== "plugin.install.inspect",
    )).toMatchObject([
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
    const application: PluginSnapshot = { ...blockedPlugin, status: "enabled", blockingReasons: [],
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

});
