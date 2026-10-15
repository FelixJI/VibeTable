import { afterEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import type { PluginSnapshot, PluginTaskSnapshot, PluginTaskViewSnapshot, PluginSurfaceThemeSnapshot } from "@/contracts";
import { usePluginStore } from "@/stores/pluginStore";
import { createHostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import { createSurfaceClient } from "../../../../../sdk/plugin/src/surface";
import type { SurfaceParentTransport } from "../../../../../sdk/plugin/src/surface-ui";
import PluginSurfaceHost from "./PluginSurfaceHost.vue";
const token = "history-surface";
const src = `https://${"a".repeat(32)}.${"b".repeat(32)}.plugins.vibetable.local/index.html`;
const theme = { contract: "vibetable.plugin-theme.v1", mode: "light", locale: "zh-CN", density: "comfortable", variables: {} } as PluginSurfaceThemeSnapshot;
const installation: PluginSnapshot = {
  installationId: "installed", projectKey: "local:test", pluginId: "com.test.ui", version: "1.0.0", revision: 1,
  packageHash: "sha256:fixture", sourceType: "package", sourceLocation: "synthetic", schemas: {}, status: "enabled", disabledReason: null,
  manifest: { $schema: "vibetable.plugin-manifest.v2", pluginId: "com.test.ui", version: "1.0.0", displayName: { "zh-CN": "任务" },
    description: {}, compatibility: { pluginApi: "3.x" }, permissions: {}, ui: {},
    actions: [{ actionId: "write", displayName: { "zh-CN": "写入" }, description: {}, mode: "local", risk: "write", invocation: "manual",
      placements: [], requires: {}, entry: "index.html", workerEntry: null, formSchema: null, inputSchema: null, outputSchema: null }] },
};
function task(taskId: string, state: PluginTaskSnapshot["state"] = "running"): PluginTaskSnapshot {
  return { taskId, runId: taskId, pluginId: installation.pluginId, projectKey: installation.projectKey,
    pluginVersion: installation.version, installationId: installation.installationId, installationRevision: 1,
    surfaceToken: token, actionId: "write", collection: "orders", targetCount: 1, risk: "write", state,
    cancelRequested: false, progress: null, result: null, error: null };
}
function connect(wrapper: ReturnType<typeof mount>) {
  const frame = wrapper.get("iframe").element as HTMLIFrameElement;
  const listeners = new Set<Parameters<SurfaceParentTransport["addEventListener"]>[1]>();
  const parent: SurfaceParentTransport = {
    parent: { postMessage: data => window.dispatchEvent(new MessageEvent("message", { data,
      origin: new URL(src).origin, source: frame.contentWindow })) },
    addEventListener: (_, listener) => { listeners.add(listener); },
    removeEventListener: (_, listener) => { listeners.delete(listener); },
  };
  const sent: Array<{ event: string; payload: unknown }> = [];
  Object.defineProperty(frame, "contentWindow", { configurable: true, value: { postMessage: (data: { event: string; payload: unknown }) => {
    sent.push(data);
    for (const listener of listeners) listener({ data, origin: "https://app.vibetable.local", source: parent.parent });
  } } });
  const createClient = () => createSurfaceClient(token, { postMessage() {}, addEventListener() {}, removeEventListener() {} }, parent);
  return { client: createClient(), sent, createClient };
}
describe("actual surface producer to public SDK", () => {
  afterEach(() => setHostBridgeForTesting(null));
  it("keeps an old active task, then its unknown terminal and list recovery after over 256 newer completions", async () => {
    setActivePinia(createPinia());
    const store = usePluginStore(); store.setProjectContext(installation.projectKey, "r1");
    store.replaceCatalog(installation.projectKey, [installation]); store.applyTask(task("old-active"));
    setHostBridgeForTesting(createHostBridge({ webview: { postMessage() {}, addEventListener() {}, removeEventListener() {} } }));
    const getTasks = () => store.tasksForAction(installation.pluginId, "write", token);
    const wrapper = mount(PluginSurfaceHost, { props: { src, surfaceToken: token, title: "任务", theme, tasks: getTasks() } });
    const { client, sent, createClient } = connect(wrapper);
    let lazy: ReturnType<typeof createSurfaceClient> | undefined;
    let pushedState: string | undefined;
    let pushedCode: string | undefined;
    client.tasks.onChanged(tasks => { const old = tasks.find(task => task.taskId === "old-active"); pushedState = old?.state; pushedCode = old?.error?.code; });
    try {
      for (let i = 0; i < 300; i++) store.applyTask(task(`done-${i}`, "succeeded"));
      await wrapper.setProps({ tasks: getTasks() });
      const activeStillProjected = (sent.filter(item => item.event === "tasksChanged").at(-1)?.payload as Array<{ taskId: string }>).some(item => item.taskId === "old-active");
      // A lazy consumer's list must also recover the active task.
      lazy = createClient();
      const before = await lazy.tasks.list();
      store.applyTask({ ...task("old-active", "aborted"), error: { contract: "vibetable.plugin-error.v1", code: "plugin_commit_unknown",
        message: "请核对实际写入，勿重放", recoverability: "none", pluginId: installation.pluginId, actionId: "write", runId: "old-active", details: {}, causeId: null } });
      await wrapper.setProps({ tasks: getTasks() });
      expect({ pushedState, pushedCode }).toEqual({ pushedState: "aborted", pushedCode: "plugin_commit_unknown" });
      store.applyTask(task("after-old-completed", "succeeded"));
      await wrapper.setProps({ tasks: getTasks() });
      expect({ pushedState, pushedCode }).toEqual({ pushedState: "aborted", pushedCode: "plugin_commit_unknown" });
      const after = await client.tasks.list();
      expect((await lazy.tasks.list()).find(item => item.taskId === "old-active")?.state).toBe("aborted");
      const old = after.find(item => item.taskId === "old-active");
      expect({ activeStillProjected, activeInList: before.some(item => item.taskId === "old-active"), state: old?.state, code: old?.error?.code })
        .toEqual({ activeStillProjected: true, activeInList: true, state: "aborted", code: "plugin_commit_unknown" });
      expect(after.length).toBeLessThanOrEqual(256);
    } finally { lazy?.dispose(); client.dispose(); wrapper.unmount(); }
  });
  it("redacts every task event while retaining the trusted native confirmation in parent chrome", async () => {
    setHostBridgeForTesting(createHostBridge({ webview: { postMessage() {}, addEventListener() {}, removeEventListener() {} } }));
    const owned = { ...task("owned"), revision: 1,
      confirmation: { title: "精确删除确认", summary: "RAW_SAMPLE", risk: "destructive", interactionId: "RAW_APPROVAL", sample: [{ hidden: "RAW_SAMPLE" }] },
      result: { summary: "安全摘要", table: { hidden: "RAW_TABLE" } }, error: { code: "safe", message: "安全错误", recoverability: "none", details: { hidden: "RAW_DETAILS" } },
    } as unknown as PluginTaskViewSnapshot;
    const wrapper = mount(PluginSurfaceHost, { props: { src, surfaceToken: token, title: "任务", theme, variant: "app",
      tasks: [owned, { ...owned, taskId: "FOREIGN_TASK", surfaceToken: "elsewhere" }] } });
    const { client, sent } = connect(wrapper);
    try {
      await wrapper.get("iframe").trigger("load");
      const outbound = sent.filter(item => item.event === "taskChanged" || item.event === "tasksChanged");
      expect(outbound.length).toBeGreaterThan(0);
      const json = JSON.stringify(outbound);
      for (const secret of ["RAW_APPROVAL", "RAW_SAMPLE", "RAW_TABLE", "RAW_DETAILS", "FOREIGN_TASK", "confirmation"])
        expect(json).not.toContain(secret);
      expect(json).toContain("安全摘要");
      expect(wrapper.get(".host-confirmation").text()).toContain("精确删除确认");
      expect(wrapper.get(".host-confirmation").text()).toContain("原生确认窗口");
      expect(wrapper.find(".confirmation-actions").exists()).toBe(false);
      expect(wrapper.emitted("resolve")).toBeUndefined();
    } finally { client.dispose(); wrapper.unmount(); }
  });
});
