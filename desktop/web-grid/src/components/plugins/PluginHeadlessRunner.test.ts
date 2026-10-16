import { afterEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { createHostBridge } from "@/bridge/hostBridge";
import type { PluginSurfaceThemeSnapshot, PluginTaskViewSnapshot, WebPluginActionDescription } from "@/contracts";
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

const runningTask: PluginTaskViewSnapshot & { surfaceToken: string } = {
  taskId: "native-current", runId: "run-current", pluginId: description.pluginId, actionId: description.actionId,
  surfaceToken: "headless-token", installationId: "installed", installationRevision: 1,
  pluginVersion: "1.0.0", projectKey: "local:default", collection: null, targetCount: 1, risk: "write",
  state: "running", revision: 1, cancelRequested: false, result: null, error: null,
};

describe("PluginHeadlessRunner", () => {
  afterEach(() => setHostBridgeForTesting(null));

  function mountRunner(task: PluginTaskViewSnapshot | null = null) {
    const hostPostMessage = vi.fn();
    setHostBridgeForTesting(createHostBridge({ webview: {
      postMessage: hostPostMessage,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    } }));
    const wrapper = mount(PluginHeadlessRunner, { props: { description, theme, task } });
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
      payload: surfaceCloseEnvelope({ status: "succeeded", summary: "已记住当前活动表" }),
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

  it.each(["succeeded", "cancelled", "aborted"] as const)("cancel waits for authoritative %s instead of declaring cancellation", async (state) => {
    const { wrapper } = mountRunner(runningTask);
    await wrapper.get('[data-testid="plugin-headless-cancel"]').trigger("click");
    expect(wrapper.text()).not.toContain("已取消");
    expect(wrapper.text()).toContain("等待");
    expect(wrapper.find("iframe").exists()).toBe(false);
    const error = state === "aborted" ? { contract: "vibetable.plugin-error.v1" as const,
      code: "plugin_commit_unknown", message: "核对保存结果", recoverability: "none" as const,
      pluginId: null, actionId: null, runId: null, details: {}, causeId: null } : null;
    await wrapper.setProps({ task: { ...runningTask, state, error } });
    expect(wrapper.text()).toContain(state === "succeeded" ? "已完成" : state === "cancelled" ? "已取消" : "结果未知");
    wrapper.unmount();
  });

  it("does not use an unknown late task from an older surface and accepts the current late task", async () => {
    const { wrapper } = mountRunner();
    await wrapper.get('[data-testid="plugin-headless-cancel"]').trigger("click");
    expect(wrapper.text()).not.toContain("已取消");
    await wrapper.setProps({ task: { ...runningTask, surfaceToken: "retired-token", state: "succeeded" } });
    expect(wrapper.text()).not.toContain("已完成");
    await wrapper.setProps({ task: { ...runningTask, state: "succeeded" } });
    expect(wrapper.text()).toContain("已完成");
    wrapper.unmount();
  });

  it("requests cancellation for the current task arriving after a no-task local stop", async () => {
    const { wrapper } = mountRunner();
    await wrapper.get('[data-testid="plugin-headless-cancel"]').trigger("click");
    expect(wrapper.emitted("cancel")).toBeUndefined();
    await wrapper.setProps({ task: runningTask });
    expect(wrapper.emitted("cancel")).toEqual([[runningTask.taskId]]);
    expect(wrapper.text()).toContain("等待结果");
    await wrapper.setProps({ task: { ...runningTask, state: "cancelled" } });
    expect(wrapper.text()).toContain("已取消");
    wrapper.unmount();
  });

  it("keeps a known success sticky after cancel and a late module finish", async () => {
    const { wrapper, dispatchFromFrame } = mountRunner(runningTask);
    await wrapper.get('[data-testid="plugin-headless-cancel"]').trigger("click");
    expect(wrapper.emitted("cancel")).toEqual([[runningTask.taskId]]);
    await wrapper.setProps({ task: { ...runningTask, state: "succeeded" } });
    await wrapper.setProps({ task: { ...runningTask, state: "cancelled" } });
    dispatchFromFrame({ status: "cancelled", summary: "late module" }); await Promise.resolve();
    expect(wrapper.text()).toContain("已完成"); expect(wrapper.text()).not.toContain("late module");
    wrapper.unmount();
  });

  it.each(["success", "failure", "local-stop"])("retires the frame exactly once for %s", async boundary => {
    const { wrapper, hostPostMessage, dispatchFromFrame } = mountRunner();
    if (boundary === "success" || boundary === "failure") dispatchFromFrame({ status: boundary === "success" ? "succeeded" : "failed", summary: "done" });
    else if (boundary === "local-stop") await wrapper.get('[data-testid="plugin-headless-cancel"]').trigger("click");
    else await wrapper.setProps({ task: { ...runningTask, state: "succeeded" } });
    await Promise.resolve();
    expect(wrapper.find("iframe").exists()).toBe(false);
    wrapper.unmount();
    const closes = hostPostMessage.mock.calls.filter(([message]) => message.type === "plugin.surface.event" && message.payload.event === "close");
    expect(closes).toHaveLength(1);
    expect(closes[0][0].payload.surfaceToken).toBe("headless-token");
  });

  it("keeps concurrent Host tasks visible when the newer one finishes first", async () => {
    const { wrapper, hostPostMessage } = mountRunner();
    const older = { ...runningTask, taskId: "older" };
    const newer = { ...runningTask, taskId: "newer", state: "succeeded" as const };
    await wrapper.setProps({ tasks: [older, newer] });
    expect(wrapper.find("iframe").exists()).toBe(true);
    expect(wrapper.text()).toContain("older");
    expect(wrapper.text()).toContain("newer");
    await wrapper.get('[data-task-id="older"] button').trigger("click");
    expect(wrapper.emitted("cancel")).toEqual([["older"]]);
    expect(hostPostMessage.mock.calls.filter(([m]) => m.type === "plugin.surface.event")).toHaveLength(0);
    wrapper.unmount();
  });

  it("keeps unknown and success receipts after module finish without replacing either outcome", async () => {
    const { wrapper, dispatchFromFrame, hostPostMessage } = mountRunner();
    const unknown = { ...runningTask, taskId: "uncertain", state: "aborted" as const,
      error: { contract: "vibetable.plugin-error.v1" as const, code: "plugin_commit_unknown", message: "核对保存结果",
        recoverability: "none" as const, pluginId: null, actionId: null, runId: null, details: {}, causeId: null } };
    const success = { ...runningTask, taskId: "saved", state: "succeeded" as const };
    await wrapper.setProps({ tasks: [unknown, success] });
    expect(wrapper.find("iframe").exists()).toBe(true);
    dispatchFromFrame({ status: "succeeded", summary: "module finished" }); await Promise.resolve();
    expect(wrapper.find("iframe").exists()).toBe(false);
    expect(wrapper.get('[data-task-id="uncertain"]').text()).toContain("写入结果未知");
    expect(wrapper.get('[data-task-id="saved"]').text()).toContain("succeeded");
    expect(wrapper.find('b[data-state="unknown"]').exists()).toBe(true);
    await wrapper.setProps({ tasks: [success] });
    expect(wrapper.find('[data-task-id="uncertain"]').exists()).toBe(true);
    wrapper.unmount();
    expect(hostPostMessage.mock.calls.filter(([m]) => m.type === "plugin.surface.event")).toHaveLength(1);
  });

  it("stops each pending task by its own id and leaves completed receipts untouched", async () => {
    const { wrapper } = mountRunner();
    await wrapper.setProps({ tasks: [{ ...runningTask, taskId: "first" }, { ...runningTask, taskId: "second" },
      { ...runningTask, taskId: "finished", state: "succeeded" }] });
    await wrapper.get('[data-testid="plugin-headless-cancel"]').trigger("click");
    expect(wrapper.emitted("cancel")).toEqual([["first"], ["second"]]);
    expect(wrapper.find("iframe").exists()).toBe(false);
    expect(wrapper.findAll("[data-task-id]")).toHaveLength(3);
    expect(wrapper.text()).toContain("等待");
    wrapper.unmount();
  });

  it("retains a module failure beside successful Host writes", async () => {
    const { wrapper, dispatchFromFrame } = mountRunner();
    await wrapper.setProps({ tasks: [{ ...runningTask, state: "succeeded" }] });
    dispatchFromFrame({ status: "failed", summary: "模块后续计算失败" }); await Promise.resolve();
    expect(wrapper.get('[data-testid="headless-module-result"]').text()).toContain("模块后续计算失败");
    expect(wrapper.get('[data-task-id="native-current"]').text()).toContain("succeeded");
    wrapper.unmount();
  });

  it("keeps the module running after its known tasks settle so it can submit another request", async () => {
    const { wrapper } = mountRunner();
    await wrapper.setProps({ tasks: [{ ...runningTask, state: "succeeded" }] });
    expect(wrapper.find("iframe").exists()).toBe(true);
    expect(wrapper.find('b[data-state="running"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="plugin-headless-cancel"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="plugin-headless-close"]').exists()).toBe(false);
    await wrapper.setProps({ tasks: [{ ...runningTask, state: "succeeded" }, { ...runningTask, taskId: "second-request" }] });
    expect(wrapper.findAll("[data-task-id]")).toHaveLength(2);
    expect(wrapper.find("iframe").exists()).toBe(true);
    wrapper.unmount();
  });

  it.each(["failed", "cancelled", "aborted"] as const)("summarizes successful writes beside %s as partial results", async state => {
    const { wrapper, dispatchFromFrame } = mountRunner();
    await wrapper.setProps({ tasks: [{ ...runningTask, taskId: "saved", state: "succeeded", result: { contract: "vibetable.plugin-result.v1", status: "success", summary: "已保存甲", metrics: [], table: null, artifacts: [], refresh: null, warnings: [] } },
      { ...runningTask, taskId: "other", state }] });
    dispatchFromFrame({ status: "succeeded", summary: "done" }); await Promise.resolve();
    expect(wrapper.get("header b").text()).toBe("部分完成（请核对）");
    expect(wrapper.get(".headless-summary").text()).toContain("已保存甲");
    expect(wrapper.get(".headless-summary").text()).toContain("请勿重复执行");
    expect(wrapper.get('[data-task-id="saved"]').text()).toContain("已保存甲");
    expect(wrapper.get('[data-task-id="other"]').text()).toContain(state);
    wrapper.unmount();
  });
  it.each(["cancelled", "bare-close"])("does not summarize autonomous module %s as total success after a saved Host write", async moduleStatus => {
    const { wrapper, dispatchFromFrame, hostPostMessage } = mountRunner();
    const saved = { ...runningTask, state: "succeeded" as const, result: { contract: "vibetable.plugin-result.v1" as const,
      status: "success" as const, summary: "原写入成功", metrics: [], table: null, artifacts: [], refresh: null, warnings: [] } };
    await wrapper.setProps({ tasks: [saved] });
    dispatchFromFrame(moduleStatus === "cancelled" ? { status: "cancelled", summary: "模块后续自行取消" } : {});
    await Promise.resolve();
    expect(wrapper.get("header b").attributes("data-state")).toBe(moduleStatus === "cancelled" ? "partial" : "unknown");
    expect(wrapper.get('[data-task-id="native-current"]').text()).toContain("原写入成功");
    expect(wrapper.text()).toContain("请勿重复执行");
    expect(wrapper.find("iframe").exists()).toBe(false);
    expect(wrapper.emitted("cancel")).toBeUndefined();
    expect(hostPostMessage.mock.calls.filter(([m]) => m.type === "plugin.surface.event")).toHaveLength(1);
    wrapper.unmount();
  });

  it("SDK task cancellation targets only one authority task and leaves the module and other tasks running", async () => {
    const { wrapper, iframe } = mountRunner();
    await wrapper.setProps({ tasks: [runningTask, { ...runningTask, taskId: "second" }] });
    window.dispatchEvent(new MessageEvent("message", { origin: new URL(src).origin, source: iframe.element.contentWindow,
      data: { contract: "vibetable.plugin-surface.v1", surfaceToken: "headless-token", event: "action",
        payload: { service: "ui", requestId: "cancel-one", method: "tasks.cancel", params: { taskId: "second" } } } }));
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("cancel")).toEqual([["second"]]);
    expect(wrapper.find("iframe").exists()).toBe(true);
    expect(wrapper.get("header b").attributes("data-state")).toBe("running");
    wrapper.unmount();
  });

});
