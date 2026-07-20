import { afterEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { createHostBridge } from "@/bridge/hostBridge";
import type { PluginSurfaceThemeSnapshot } from "@/contracts";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import PluginSurfaceHost from "./PluginSurfaceHost.vue";

const revision = `${"b".repeat(32)}.${"c".repeat(32)}`;
const src = `https://${revision}.plugins.vibetable.local/index.html`;
const theme: PluginSurfaceThemeSnapshot = {
  contract: "vibetable.plugin-theme.v1",
  mode: "dark",
  locale: "zh-CN",
  density: "compact",
  variables: {
    "--vt-plugin-bg": "#17191f",
    "--vt-plugin-surface": "#1e2128",
    "--vt-plugin-text": "#c9cdd4",
    "--vt-plugin-text-muted": "#6b7280",
    "--vt-plugin-border": "#2a2e37",
    "--vt-plugin-primary": "#5b8bff",
    "--vt-plugin-danger": "#f54a45",
    "--vt-plugin-radius": "6px",
    "--vt-plugin-space-unit": "4px",
  },
};

describe("PluginSurfaceHost", () => {
  afterEach(() => setHostBridgeForTesting(null));

  it("uses the fixed sandbox and sends a token-bound theme envelope", async () => {
    const hostPostMessage = vi.fn();
    setHostBridgeForTesting(createHostBridge({ webview: {
      postMessage: hostPostMessage,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    } }));
    const wrapper = mount(PluginSurfaceHost, { props: { src, surfaceToken: "surface-1", title: "概览", theme } });
    const iframe = wrapper.get("iframe");
    expect(iframe.attributes("sandbox")).toBe("allow-scripts allow-same-origin");
    expect(iframe.attributes("referrerpolicy")).toBe("no-referrer");
    expect(iframe.attributes("data-required-csp")).toContain("connect-src 'none'");
    const postMessage = vi.fn();
    Object.defineProperty(iframe.element, "contentWindow", {
      configurable: true,
      value: { postMessage },
    });
    await iframe.trigger("load");
    expect(postMessage).toHaveBeenCalledWith({
      contract: "vibetable.plugin-surface.v1",
      surfaceToken: "surface-1",
      event: "themeChanged",
      payload: theme,
    }, `https://${revision}.plugins.vibetable.local`);
    window.dispatchEvent(new MessageEvent("message", {
      origin: `https://${revision}.plugins.vibetable.local`,
      source: iframe.element.contentWindow,
      data: {
        contract: "vibetable.plugin-surface.v1",
        surfaceToken: "surface-1",
        event: "action",
        payload: { collection: "orders" },
      },
    }));
    expect(wrapper.emitted("action")).toEqual([[{ collection: "orders" }]]);
    expect(hostPostMessage).toHaveBeenCalledWith({
      type: "plugin.surface.event",
      payload: {
        contract: "vibetable.plugin-surface.v1",
        surfaceToken: "surface-1",
        event: "action",
        payload: { collection: "orders" },
      },
    });
    wrapper.unmount();
    expect(hostPostMessage).toHaveBeenCalledWith({
      type: "plugin.surface.event",
      payload: {
        contract: "vibetable.plugin-surface.v1",
        surfaceToken: "surface-1",
        event: "close",
        payload: {},
      },
    });
  });

  it("does not navigate an iframe for an untrusted origin", () => {
    setHostBridgeForTesting(createHostBridge({ webview: {
      postMessage: () => undefined,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    } }));
    const wrapper = mount(PluginSurfaceHost, { props: { src: "https://evil.test/panel", surfaceToken: "surface-1", title: "概览", theme } });
    expect(wrapper.find("iframe").exists()).toBe(false);
    expect(wrapper.get('[role="alert"]').text()).toContain("origin 校验");
  });

  it("keeps final write confirmation in host chrome for custom pages", async () => {
    setHostBridgeForTesting(createHostBridge({ webview: {
      postMessage: () => undefined,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    } }));
    const wrapper = mount(PluginSurfaceHost, { props: {
      src,
      surfaceToken: "surface-1",
      title: "概览",
      theme,
      task: {
        taskId: "task-1", runId: "run-1", pluginId: "com.example.view",
        pluginVersion: "1.0.0", actionId: "write", projectKey: "local:default",
        collection: "orders", targetCount: 2, risk: "write", state: "running",
        cancelRequested: false, progress: { current: 1, total: 2, message: "等待确认", cancellable: true },
        result: null, error: null, revision: 2, progressPercent: 50,
        confirmation: {
          runId: "run-1", interactionId: "confirm-1", pluginId: "com.example.view",
          actionId: "write", title: "确认写入", summary: "将修改 2 条记录",
          risk: "write", targetCount: 2, expiresAt: "2026-07-20T12:00:00Z",
        },
      },
    } });

    await wrapper.get('[data-testid="plugin-surface-confirm-approve"]').trigger("click");

    expect(wrapper.text()).toContain("HOST FINAL WRITE CONFIRMATION");
    expect(wrapper.emitted("resolve")?.[0]).toEqual(["approved"]);
  });

  it("keeps safe task errors visible in host chrome", () => {
    setHostBridgeForTesting(createHostBridge({ webview: {
      postMessage: () => undefined,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    } }));
    const wrapper = mount(PluginSurfaceHost, { props: {
      src, surfaceToken: "surface-1", title: "概览", theme,
      task: {
        taskId: "task-failed", runId: "run-failed", pluginId: "com.example.view",
        pluginVersion: "1.0.0", actionId: "write", projectKey: "local:default",
        collection: "orders", targetCount: 2, risk: "write", state: "failed",
        cancelRequested: false, progress: null, result: null, confirmation: null,
        error: {
          contract: "vibetable.plugin-error.v1", code: "plugin_output_invalid",
          message: "输出不符合契约", recoverability: "reinstall",
          pluginId: "com.example.view", actionId: "write", runId: "run-failed",
          details: {}, causeId: "cause-output",
        },
        revision: 3, progressPercent: 100,
      },
    } });

    expect(wrapper.get('[data-testid="plugin-surface-task-error"]').text()).toContain("plugin_output_invalid");
    expect(wrapper.text()).toContain("输出不符合契约");
    expect(wrapper.text()).toContain("reinstall");
  });
});
