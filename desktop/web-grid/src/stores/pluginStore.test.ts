import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { PluginInteractionSnapshot, PluginSnapshot, PluginTaskSnapshot } from "@/contracts";
import { usePluginStore } from "./pluginStore";

const plugin = (revision: number, status: PluginSnapshot["status"] = "enabled"): PluginSnapshot => ({
  projectKey: "local:default",
  pluginId: "com.acme.clean",
  version: "1.2.0",
  packageHash: "sha256:abc",
  sourceType: "package",
  sourceLocation: "clean.vtplugin",
  manifest: {
    $schema: "vibetable.plugin-manifest.v1",
    pluginId: "com.acme.clean",
    version: "1.2.0",
    displayName: { "zh-CN": "Clean rows" },
    description: {},
    compatibility: {},
    permissions: { data: ["customers:read"] },
    actions: [],
    ui: {},
  },
  schemas: {},
  status,
  disabledReason: status === "enabled" ? null : "本地 worker 不可用",
  revision,
});

describe("pluginStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("drops stale canonical plugin snapshots by revision", () => {
    const store = usePluginStore();
    store.replaceCatalog("local:default", [plugin(8, "enabled")], 8);
    store.applyPlugin(plugin(9, "disabled"), 9);
    store.applyPlugin(plugin(8, "enabled"), 8);
    expect(store.catalogRevision).toBe(9);
    expect(store.plugins[0]?.status).toBe("disabled");
    expect(store.plugins[0]?.revision).toBe(9);
  });

  it("keeps task truth after an action panel closes", () => {
    const store = usePluginStore();
    const running: PluginTaskSnapshot = {
      taskId: "task-1",
      runId: "run-1",
      pluginId: "com.acme.clean",
      pluginVersion: "1.2.0",
      actionId: "normalize",
      projectKey: "local:default",
      collection: "customers",
      targetCount: 2,
      risk: "read",
      state: "running",
      cancelRequested: false,
      result: null,
      error: null,
    };
    store.applyTask(running, 2);
    store.closeAction();
    store.applyTask({ ...running, state: "succeeded" }, 3);
    expect(store.activeTask?.state).toBe("succeeded");
    expect(store.actionOpen).toBe(false);
  });

  it("replays an interaction notification that arrives before the task snapshot", () => {
    const store = usePluginStore();
    const interaction: PluginInteractionSnapshot = {
      runId: "run-early",
      projectKey: "local:default",
      pluginId: "com.acme.clean",
      actionId: "normalize",
      caller: "alice",
      progress: null,
      pendingConfirmation: {
        interactionId: "confirm-1",
        risk: "write",
        title: "确认修改",
        preview: {
          summary: [],
          sampleRows: [{ id: 1 }],
          affectedCount: 1,
          warnings: [],
        },
        expiresAt: 1_800_000_000,
      },
      cancelRequested: false,
    };
    store.applyInteraction(interaction, 2);
    store.applyTask({
      taskId: "task-early",
      runId: "run-early",
      pluginId: "com.acme.clean",
      pluginVersion: "1.2.0",
      actionId: "normalize",
      projectKey: "local:default",
      collection: "customers",
      targetCount: 1,
      risk: "write",
      state: "running",
      cancelRequested: false,
      result: null,
      error: null,
    }, 1);

    expect(store.pendingConfirmation?.interactionId).toBe("confirm-1");
    expect(store.activeTask?.revision).toBe(2);
  });

  it("keeps task and interaction revisions independent and clears resolved confirmation", () => {
    const store = usePluginStore();
    const running: PluginTaskSnapshot = {
      taskId: "task-independent",
      runId: "run-independent",
      pluginId: "com.acme.clean",
      pluginVersion: "1.2.0",
      actionId: "normalize",
      projectKey: "local:default",
      collection: "customers",
      targetCount: 1,
      risk: "write",
      state: "running",
      cancelRequested: false,
      result: null,
      error: null,
    };
    const interaction: PluginInteractionSnapshot = {
      runId: "run-independent",
      projectKey: "local:default",
      pluginId: "com.acme.clean",
      actionId: "normalize",
      caller: "alice",
      progress: null,
      pendingConfirmation: {
        interactionId: "confirm-independent",
        risk: "write",
        title: "确认修改",
        preview: { summary: [], sampleRows: [], affectedCount: 1, warnings: [] },
        expiresAt: 1_800_000_000,
      },
      cancelRequested: false,
    };

    store.applyTask(running, 10);
    store.applyInteraction(interaction, 2);
    expect(store.pendingConfirmation?.interactionId).toBe("confirm-independent");

    store.applyTask({ ...running, progress: { current: 1, total: 2, message: "继续", cancellable: true } }, 11);
    expect(store.pendingConfirmation?.interactionId).toBe("confirm-independent");

    store.applyInteraction({ ...interaction, pendingConfirmation: null }, 3);
    expect(store.pendingConfirmation).toBeNull();
  });

  it("clears confirmation when a task reaches a terminal state", () => {
    const store = usePluginStore();
    const running: PluginTaskSnapshot = {
      taskId: "task-terminal", runId: "run-terminal", pluginId: "com.acme.clean",
      pluginVersion: "1.2.0", actionId: "normalize", projectKey: "local:default",
      collection: "customers", targetCount: 1, risk: "write", state: "running",
      cancelRequested: false, result: null, error: null,
    };
    store.applyTask(running, 1);
    store.applyInteraction({
      runId: "run-terminal", projectKey: "local:default", pluginId: "com.acme.clean",
      actionId: "normalize", caller: "alice", progress: null, cancelRequested: false,
      pendingConfirmation: {
        interactionId: "confirm-terminal", risk: "write", title: "确认",
        preview: { summary: [], sampleRows: [], affectedCount: 1, warnings: [] },
        expiresAt: 1_800_000_000,
      },
    }, 2);

    store.applyTask({ ...running, state: "failed" }, 3);

    expect(store.pendingConfirmation).toBeNull();
  });
});
