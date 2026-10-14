import { afterEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { createHostBridge } from "@/bridge/hostBridge";
import type { PluginSurfaceThemeSnapshot, WebPluginActionDescription } from "@/contracts";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import PluginHeadlessRunner from "./PluginHeadlessRunner.vue";

const revision = `${"b".repeat(32)}.${"c".repeat(32)}`;
const src = `https://${revision}.plugins.vibetable.local/__vibetable_host__/headless.html?entry=ui%2Frun.js`;

const theme: PluginSurfaceThemeSnapshot = {
  contract: "vibetable.plugin-theme.v1",
  mode: "light",
  locale: "zh-CN",
  density: "comfortable",
  variables: {
    "--vt-plugin-bg": "#fff",
    "--vt-plugin-surface": "#f4f5f7",
    "--vt-plugin-text": "#222",
    "--vt-plugin-text-muted": "#777",
    "--vt-plugin-border": "#ddd",
    "--vt-plugin-primary": "#3b82f6",
    "--vt-plugin-danger": "#dc2626",
    "--vt-plugin-radius": "6px",
    "--vt-plugin-space-unit": "4px",
  },
};

const description: WebPluginActionDescription = {
  pluginId: "com.example.headless",
  actionId: "run-job",
  title: "记住当前表",
  description: "",
  risk: "read",
  inputSchema: { type: "object", properties: {} },
  presentation: "headless",
  surface: { src, surfaceToken: "headless-token", title: "记住当前表" },
};

function surfaceCloseEnvelope(payload: Readonly<Record<string, unknown>>) {
  return {
    contract: "vibetable.plugin-surface.v1",
    surfaceToken: "headless-token",
    event: "close",
    payload,
  };
}

describe("PluginHeadlessRunner", () => {
  afterEach(() => setHostBridgeForTesting(null));

  function mountRunner() {
    const hostPostMessage = vi.fn();
    setHostBridgeForTesting(createHostBridge({ webview: {
      postMessage: hostPostMessage,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    } }));
    const wrapper = mount(PluginHeadlessRunner, { props: { description, theme, task: null } });
    const iframe = wrapper.get("iframe");
    const postMessage = vi.fn();
    Object.defineProperty(iframe.element, "contentWindow", {
      configurable: true,
      value: { postMessage },
    });
    return { wrapper, hostPostMessage, iframe, dispatchFromFrame: (payload: Readonly<Record<string, unknown>>) => {
      window.dispatchEvent(new MessageEvent("message", {
        origin: `https://${revision}.plugins.vibetable.local`,
        source: iframe.element.contentWindow,
        data: surfaceCloseEnvelope(payload),
      }));
    } };
  }

  it("mounts the restricted frame only while running and settles a succeeded finish", async () => {
    const { wrapper, hostPostMessage, dispatchFromFrame } = mountRunner();
    expect(wrapper.find("iframe").exists()).toBe(true);

    dispatchFromFrame({ status: "succeeded", summary: "已记住当前活动表" });
    await Promise.resolve();

    expect(wrapper.text()).toContain("已记住当前活动表");
    expect(wrapper.text()).toContain("已完成");
    // The frame is genuinely unmounted at the terminal outcome; the Host
    // status above stays visible.
    expect(wrapper.find("iframe").exists()).toBe(false);
    expect(wrapper.find('[data-testid="plugin-headless-close"]').exists()).toBe(true);
    // The token retire is forwarded to the host bridge.
    expect(hostPostMessage).toHaveBeenCalledWith({
      type: "plugin.surface.event",
      payload: surfaceCloseEnvelope({}),
    });
    wrapper.unmount();
  });

  it("treats a bare close as UNKNOWN, never as a write success", async () => {
    const { wrapper, dispatchFromFrame } = mountRunner();
    dispatchFromFrame({});
    await Promise.resolve();
    expect(wrapper.text()).toContain("已结束（结果未知）");
    expect(wrapper.text()).not.toContain("已完成");
    expect(wrapper.find("iframe").exists()).toBe(false);
    wrapper.unmount();
  });

  it("maps unrecognized finish statuses to unknown", async () => {
    const { wrapper, dispatchFromFrame } = mountRunner();
    dispatchFromFrame({ status: "kind-of-done", summary: "" });
    await Promise.resolve();
    expect(wrapper.text()).toContain("已结束（结果未知）");
    wrapper.unmount();
  });

  it("host cancel settles cancelled, retires the surface and unmounts the frame", async () => {
    const { wrapper, hostPostMessage } = mountRunner();
    await wrapper.get('[data-testid="plugin-headless-cancel"]').trigger("click");
    expect(wrapper.text()).toContain("已取消");
    expect(wrapper.find("iframe").exists()).toBe(false);
    expect(hostPostMessage).toHaveBeenCalledWith({
      type: "plugin.surface.event",
      payload: surfaceCloseEnvelope({}),
    });
    expect(wrapper.emitted("cancel")).toHaveLength(1);
    wrapper.unmount();
  });
});
