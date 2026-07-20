import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import type { PluginTaskViewSnapshot, WebPluginActionDescription } from "@/contracts";
import PluginActionPanel from "./PluginActionPanel.vue";

const description: WebPluginActionDescription = {
  pluginId: "com.acme.clean",
  actionId: "normalize",
  title: "规范化客户",
  risk: "destructive",
  inputSchema: {
    type: "object",
    required: ["label"],
    properties: {
      label: { type: "string", title: "批次标签" },
      trim: { type: "boolean", title: "清理空格", default: true },
    },
  },
};

const task: PluginTaskViewSnapshot = {
  taskId: "task-1",
  runId: "run-1",
  pluginId: "com.acme.clean",
  pluginVersion: "1.2.0",
  actionId: "normalize",
  projectKey: "local:default",
  collection: "customers",
  targetCount: 12,
  risk: "destructive",
  state: "running",
  progress: null,
  progressPercent: 48,
  progressMessage: "已生成最终预览",
  cancelRequested: false,
  confirmation: {
    runId: "run-1",
    interactionId: "confirm-1",
    pluginId: "com.acme.clean",
    actionId: "normalize",
    title: "确认覆盖客户名称",
    summary: "将覆盖 12 条记录",
    risk: "destructive",
    targetCount: 12,
    expiresAt: "2026-07-20T15:00:00Z",
  },
  result: null,
  error: null,
  revision: 4,
};

describe("PluginActionPanel", () => {
  it("renders a host form and the final destructive confirmation", async () => {
    const wrapper = mount(PluginActionPanel, { props: { description, task } });
    await wrapper.get('[data-testid="plugin-field-label"]').setValue("夜间批次");
    await wrapper.get('[data-testid="plugin-action-start"]').trigger("click");
    expect(wrapper.emitted("start")?.[0]?.[0]).toMatchObject({ label: "夜间批次", trim: true });
    expect(wrapper.get('[data-testid="plugin-confirmation"]').text()).toContain("将覆盖 12 条记录");
    await wrapper.get('[data-testid="plugin-confirm-approve"]').trigger("click");
    expect(wrapper.emitted("resolve")?.[0]).toEqual(["approved"]);
  });

  it("closing the panel never implies task cancellation", async () => {
    const wrapper = mount(PluginActionPanel, { props: { description, task } });
    await wrapper.get('[data-testid="plugin-action-close"]').trigger("click");
    expect(wrapper.emitted("close")).toHaveLength(1);
    expect(wrapper.emitted("cancel")).toBeUndefined();
  });

  it("renders the safe error code, message and recovery guidance", () => {
    const wrapper = mount(PluginActionPanel, { props: { description, task: {
      ...task,
      state: "failed",
      confirmation: null,
      error: {
        contract: "vibetable.plugin-error.v1",
        code: "plugin_directus_unavailable",
        message: "Directus 暂时不可用",
        recoverability: "retry",
        pluginId: "com.acme.clean",
        actionId: "normalize",
        runId: "run-1",
        details: {},
        causeId: "cause-1",
      },
    } } });

    expect(wrapper.get('[data-testid="plugin-task-error"]').text()).toContain("plugin_directus_unavailable");
    expect(wrapper.text()).toContain("Directus 暂时不可用");
    expect(wrapper.text()).toContain("retry");
  });
});
