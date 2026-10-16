import { afterEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { reactive } from "vue";
import { createHostBridge } from "@/bridge/hostBridge";
import type { PluginSurfaceThemeSnapshot, PluginTaskViewSnapshot } from "@/contracts";
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

  it("retires only the old token on replacement and preserves the active application on hash navigation", async () => {
    const post = vi.fn();
    setHostBridgeForTesting(createHostBridge({ webview: {
      postMessage: post, addEventListener: () => undefined, removeEventListener: () => undefined,
    } }));
    const wrapper = mount(PluginSurfaceHost, { props: { src, surfaceToken: "old-token", title: "A", theme } });
    await wrapper.setProps({ surfaceToken: "new-token", title: "B" });
    expect(post.mock.calls.map(([message]) => message.payload)).toEqual([
      { contract: "vibetable.plugin-surface.v1", surfaceToken: "old-token", event: "close", payload: {} },
    ]);
    expect(wrapper.emitted("close")).toBeUndefined();
    const frame = wrapper.get("iframe").element;
    await wrapper.setProps({ src: `${src}#detail` });
    expect(wrapper.get("iframe").element).toBe(frame);
    expect(post).toHaveBeenCalledTimes(1);
    wrapper.unmount();
    expect(post.mock.calls.map(([message]) => message.payload.surfaceToken)).toEqual(["old-token", "new-token"]);
  });

  it("posts cloneable envelopes for reactive theme and task inputs", async () => {
    // Reproduces the real S47 DataCloneError: the store hands Vue reactive
    // proxies to the host, and postMessage structured-clones its argument.
    // The fake contentWindow applies the real structuredClone boundary instead
    // of mocking the failure away.
    setHostBridgeForTesting(createHostBridge({ webview: {
      postMessage: () => undefined,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    } }));
    const reactiveTheme = reactive(theme);
    type MutableTaskSnapshot = { -readonly [K in keyof PluginTaskViewSnapshot]: PluginTaskViewSnapshot[K] };
    const reactiveTask = reactive<MutableTaskSnapshot>({
      taskId: "task-1", runId: "run-1", surfaceToken: "surface-1", pluginId: "com.example.view",
      pluginVersion: "1.0.0", actionId: "write", projectKey: "local:default",
      collection: "orders", targetCount: 1, risk: "write", state: "running",
      cancelRequested: false, progress: null, result: null, error: null,
      confirmation: null, revision: 1, progressPercent: 0,
    });
    const posted: unknown[] = [];
    const cloneFailures: string[] = [];
    const wrapper = mount(PluginSurfaceHost, {
      props: { src, surfaceToken: "surface-1", title: "概览", theme: reactiveTheme, task: reactiveTask },
    });
    const iframe = wrapper.get("iframe");
    Object.defineProperty(iframe.element, "contentWindow", {
      configurable: true,
      value: {
        postMessage: (message: unknown) => {
          try {
            structuredClone(message);
            posted.push(message);
          } catch (error) {
            cloneFailures.push(String(error));
          }
        },
      },
    });
    await iframe.trigger("load");
    reactiveTask.state = "succeeded";
    await wrapper.setProps({ task: reactiveTask });
    await Promise.resolve();

    expect(cloneFailures).toEqual([]);
    expect(posted.length).toBeGreaterThanOrEqual(2);
    expect(posted[0]).toMatchObject({
      contract: "vibetable.plugin-surface.v1",
      surfaceToken: "surface-1",
      event: "themeChanged",
      payload: JSON.parse(JSON.stringify(theme)),
    });
    expect(posted.some(message => (message as { event?: string }).event === "taskChanged"
      && (message as { payload?: { state?: string } }).payload?.state === "succeeded")).toBe(true);
  });

  it("handles bounded attributed UI messages without starting a Worker action or accepting foreign tasks", async () => {
    const host = vi.fn();
    setHostBridgeForTesting(createHostBridge({ webview: { postMessage: host, addEventListener: () => undefined, removeEventListener: () => undefined } }));
    const back = vi.fn(() => false);
    const task = { taskId: "owned", surfaceToken: "surface-1", state: "running", revision: 1, cancelRequested: false } as PluginTaskViewSnapshot;
    const wrapper = mount(PluginSurfaceHost, { props: { src, surfaceToken: "surface-1", title: "合成插件", theme,
      tasks: [task, { ...task, taskId: "foreign", surfaceToken: "elsewhere" }], navigateBack: back } });
    const iframe = wrapper.get("iframe").element;
    const post = vi.fn();
    Object.defineProperty(iframe, "contentWindow", { configurable: true, value: { postMessage: post } });
    const send = (method: string, params: object = {}, overrides: object = {}) => window.dispatchEvent(new MessageEvent("message", {
      origin: new URL(src).origin, source: iframe.contentWindow,
      data: { contract: "vibetable.plugin-surface.v1", surfaceToken: "surface-1", event: "action",
        payload: { service: "ui", requestId: method, method, params } }, ...overrides,
    }));
    send("notification.show", { message: "<b>合成消息</b>" });
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[aria-label="插件通知"]').text()).toContain("合成插件");
    expect(wrapper.get('[aria-label="插件通知"]').text()).toContain("<b>合成消息</b>");
    expect(wrapper.find('[aria-label="插件通知"] b').exists()).toBe(false);
    send("tasks.list");
    expect(post.mock.calls.at(-1)?.[0].payload.result.map((value: { taskId: string }) => value.taskId)).toEqual(["owned"]);
    send("tasks.cancel", { taskId: "foreign" });
    expect(post.mock.calls.at(-1)?.[0].payload.error.code).toBe("plugin_task_not_owned");
    expect(wrapper.emitted("cancel")).toBeUndefined();
    send("tasks.cancel", { taskId: "owned" });
    expect(wrapper.emitted("cancel")).toEqual([["owned"]]);
    send("navigation.back");
    expect(back).toHaveBeenCalledTimes(1);
    expect(post.mock.calls.at(-1)?.[0].payload.result).toEqual({ navigated: false });
    send("navigation.record", { recordId: "hidden" });
    expect(post.mock.calls.at(-1)?.[0].payload.error.code).toBe("plugin_api_unsupported");
    send("notification.show", { message: "evil" }, { source: window });
    expect(wrapper.text()).not.toContain("evil");
    expect(wrapper.emitted("action")).toBeUndefined();
    expect(host).not.toHaveBeenCalled();
    wrapper.unmount();
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
    expect(wrapper.emitted("resolve")?.[0]).toEqual(["approved", "task-1"]);
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
  it("recreates the native frame for a new token while internal hash routing keeps the same frame", async () => {
    const posted: unknown[] = [];
    setHostBridgeForTesting(createHostBridge({ webview: { postMessage: message => posted.push(message),
      addEventListener: () => undefined, removeEventListener: () => undefined } }));
    const wrapper = mount(PluginSurfaceHost, { props: { src, surfaceToken: "surface-a", title: "A", theme } });
    const first = wrapper.get("iframe").element;
    const firstSource = first.contentWindow;
    await wrapper.setProps({ src: src.replace("index.html", "second.html"), surfaceToken: "surface-b", title: "B" });
    const second = wrapper.get("iframe").element;
    expect(second).not.toBe(first);
    expect(wrapper.get("iframe").attributes("name")).toBe("surface-b");
    window.dispatchEvent(new MessageEvent("message", { origin: new URL(src).origin, source: firstSource,
      data: { contract: "vibetable.plugin-surface.v1", surfaceToken: "surface-a", event: "close", payload: {} } }));
    expect(JSON.stringify(posted)).not.toContain('"surfaceToken":"surface-b"');
    expect(wrapper.get("iframe").element).toBe(second);
    await wrapper.setProps({ src: src.replace("index.html", "second.html#stats") });
    expect(wrapper.get("iframe").element).toBe(second);
    wrapper.unmount();
    expect(JSON.stringify(posted)).toContain('"surfaceToken":"surface-b"');
  });

});
