import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge } from "@/bridge/hostBridge";
import type { PluginSnapshot } from "@/contracts";
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
      mode: "flow",
      risk: "write",
      invocation: "manual",
      placements: ["table.toolbar"],
      requires: {},
      entryFlow: "clean",
      workerEntry: null,
      formSchema: null,
      inputSchema: null,
      outputSchema: null,
    }],
    flows: [],
    ui: {},
  },
  flowRequirements: [{
    logicalFlowId: "clean",
    ownership: "external",
    trigger: "manual",
    risk: "write",
    contractVersion: "1",
    requiresOperations: [],
    inputSchema: {},
    outputSchema: {},
    definition: null,
  }],
  schemas: {},
  status: "disabled",
  disabledReason: "外部 Flow 未绑定；契约版本不兼容",
  blockingReasons: ["外部 Flow 未绑定", "契约版本不兼容"],
  revision: 7,
};

describe("PluginCenterView", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    usePluginStore().applyPlugin(blockedPlugin);
  });
  afterEach(() => setHostBridgeForTesting(null));

  it("projects canonical manifest blockers, permissions and external Flow ownership", () => {
    setHostBridgeForTesting(createHostBridge({ webview: { postMessage: () => undefined, addEventListener: () => undefined, removeEventListener: () => undefined } }));
    const wrapper = mount(PluginCenterView, { props: { autoLoad: false } });
    expect(wrapper.text()).toContain("外部 Flow 未绑定");
    expect(wrapper.text()).toContain("契约版本不兼容");
    expect(wrapper.text()).toContain("外部 Flow 由用户维护");
    expect(wrapper.text()).toContain("data:customers:write");
    expect(wrapper.text()).toContain("2 个阻断原因");
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

  it("uses the native picker token and requires a separate approval before upgrade", async () => {
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
                flowRequirements: blockedPlugin.flowRequirements,
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

    await wrapper.get('[data-testid="plugin-install-commit"]').trigger("click");
    await Promise.resolve();
    expect(posted[1]).toMatchObject({
      type: "plugin.lifecycle.upgrade",
      payload: { pluginId: "com.acme.clean", planId: "plan-upgrade" },
    });
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
