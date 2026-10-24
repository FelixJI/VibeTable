import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { PluginInteractionSnapshot, PluginSnapshot, PluginTaskSnapshot } from "@/contracts";
import { usePluginStore } from "./pluginStore";

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

const plugin = (revision: number, status: PluginSnapshot["status"] = "enabled"): PluginSnapshot => ({
  installationId: "record-clean",
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

  it("settles busy and error synchronously when the same project key reopens a new generation", () => {
    const store = usePluginStore();
    store.setProjectContext("local:default", "r1");
    store.fail(new Error("旧世代请求失败"));
    store.startBusy(false);
    expect(store.busy).toBe(true);
    expect(store.lastError).toBe("旧世代请求失败");

    store.setProjectContext("local:default", "r2");

    expect(store.busy).toBe(false);
    expect(store.lastError).toBeNull();
    expect(store.projectContextGeneration).toBe(2);
  });

  it("settles busy and error when the project revision advances in place", () => {
    const store = usePluginStore();
    store.setProjectContext("local:default", "r1");
    store.fail(new Error("旧世代请求失败"));
    store.startBusy(false);

    expect(store.updateProjectRevision("r2")).toBe(true);

    expect(store.busy).toBe(false);
    expect(store.lastError).toBeNull();
    expect(store.projectContextGeneration).toBe(2);
    expect(store.projectRevision).toBe("r2");
  });

  it("drops stale canonical plugin snapshots by revision", () => {
    const store = usePluginStore();
    store.setProjectContext("local:default", "r1");
    store.replaceCatalog("local:default", [plugin(8, "enabled")], 8);
    store.applyPlugin(plugin(9, "disabled"), 9);
    store.applyPlugin(plugin(8, "enabled"), 8);
    expect(store.catalogRevision).toBe(9);
    expect(store.plugins[0]?.status).toBe("disabled");
    expect(store.plugins[0]?.revision).toBe(9);
  });

  it("keeps task truth after an action panel closes", () => {
    const store = usePluginStore();
    store.setProjectContext("local:default", "r1");
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

  it("retains an embedded action description without opening the global panel", () => {
    const store = usePluginStore();
    store.setProjectContext("local:default", "r1");
    const description = {
      pluginId: "com.acme.clean",
      actionId: "normalize",
      title: "Normalize",
      risk: "write" as const,
      inputSchema: { type: "object" },
    };
    const context = {
      contract: "vibetable.command-context.v1" as const,
      projectKey: "local:default",
      collection: "orders",
      selectedKeys: [],
      querySnapshot: null,
      locale: "zh-CN",
      theme: "light" as const,
      density: "compact",
      user: {},
      hostVersion: "1.0.0",
    };

    store.beginAction(description, context, false);

    expect(store.describedAction).toEqual(description);
    expect(store.actionOpen).toBe(false);
  });

  it("replays an interaction notification that arrives before the task snapshot", () => {
    const store = usePluginStore();
    store.setProjectContext("local:default", "r1");
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
    store.setProjectContext("local:default", "r1");
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

  it("keeps a terminal task closed when older interaction or task events arrive", () => {
    const store = usePluginStore();
    store.setProjectContext("local:default", "r1");
    const running: PluginTaskSnapshot = {
      taskId: "task-sticky", runId: "run-sticky", pluginId: "com.acme.clean",
      pluginVersion: "1.2.0", actionId: "normalize", projectKey: "local:default",
      collection: "customers", targetCount: 1, risk: "write", state: "running",
      cancelRequested: false, result: null, error: null,
    };
    const interaction: PluginInteractionSnapshot = {
      runId: running.runId, projectKey: running.projectKey,
      pluginId: running.pluginId, actionId: running.actionId,
      caller: "desktop-host", progress: null, cancelRequested: false,
      pendingConfirmation: {
        interactionId: "late-confirm", risk: "write", title: "确认",
        preview: { summary: [], sampleRows: [], affectedCount: 1, warnings: [] },
        expiresAt: 1_800_000_000,
      },
    };
    store.applyTask({ ...running, state: "aborted" }, 5);
    store.applyInteraction(interaction, 9);
    store.applyTask(running, 10);
    expect(store.activeTask?.state).toBe("aborted");
    expect(store.pendingConfirmation).toBeNull();

    const early = usePluginStore();
    early.applyInteraction({ ...interaction, runId: "run-before-task" }, 2);
    early.applyTask({ ...running, taskId: "task-before", runId: "run-before-task",
      state: "failed" }, 3);
    expect(early.activeTask?.state).toBe("failed");
    expect(early.pendingConfirmation).toBeNull();
  });
  it("clears confirmation when a task reaches a terminal state", () => {
    const store = usePluginStore();
    store.setProjectContext("local:default", "r1");
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
  it("retires scoped task lookups on same-workspace session rotation and checks the installation", () => {
    const store = usePluginStore(); store.setProjectContext("local:default", "r1");
    const installed = plugin(1);
    store.applyPlugin({ ...installed, manifest: { ...installed.manifest, actions: [{ actionId: "normalize",
      displayName: {}, description: {}, mode: "local", risk: "write", invocation: "manual", placements: [],
      requires: {}, workerEntry: "dist/worker.js", formSchema: null, inputSchema: null, outputSchema: null }] } });
    const own: PluginTaskSnapshot = { taskId: "own", runId: "run", pluginId: installed.pluginId,
      pluginVersion: installed.version, installationRevision: installed.revision, installationId: installed.installationId, actionId: "normalize", projectKey: "local:default", collection: "orders",
      targetCount: 1, risk: "write", state: "running", cancelRequested: false, result: null, error: null };
    store.applyTask(own);
    store.applyTask({ ...own, taskId: "old-version", pluginVersion: "0.1.0" });
    expect(store.taskForAction(installed.pluginId, "normalize")?.taskId).toBe("own");
    expect(store.taskForId("old-version")).toBeNull();
    store.applyPlugin({ ...store.plugins[0]!, revision: 2, packageHash: "sha256:replacement" });
    expect(store.taskForId("own")?.state).toBe("running");
    store.applyPlugin({ ...store.plugins[0]!, status: "disabled", revision: 3 });
    store.applyTask({ ...own, state: "aborted", error: { contract: "vibetable.plugin-error.v1", code: "plugin_commit_unknown", message: "Check receipt", recoverability: "none", pluginId: own.pluginId, actionId: own.actionId, runId: own.runId, details: {}, causeId: null } }, 3);
    expect(store.taskForId("own")?.error?.code).toBe("plugin_commit_unknown");
    store.applyTask({ ...own, state: "succeeded" }, 4);
    expect(store.taskForId("own")?.state).toBe("aborted");
    store.applyPlugin({ ...store.plugins[0]!, status: "enabled", revision: 4 });
    // The first report of an old task can arrive after a same-version replacement.
    store.applyTask({ ...own, taskId: "late-first", runId: "late-first-run", installationRevision: 1 });
    expect(store.taskForId("late-first")).toBeNull();
    store.applyTask({ ...own, taskId: "unbound", runId: "unbound-run", installationRevision: null });
    expect(store.taskForId("unbound")).toBeNull();
    store.applyTask({ ...own, taskId: "fresh", runId: "fresh-run", installationRevision: 4 });
    expect(store.taskForAction(installed.pluginId, "normalize")?.taskId).toBe("fresh");
    store.setProjectContext("local:default", "r1");
    expect(store.taskForId("fresh")).toBeNull();
    expect(store.taskForAction(installed.pluginId, "normalize")).toBeNull();
  });

  it("does not attach known or first-seen old tasks to a same-version same-package reinstall", () => {
    const store = usePluginStore(); store.setProjectContext("local:default", "r1");
    const installed = { ...plugin(1), installationId: "old-record", manifest: { ...plugin(1).manifest,
      actions: [{ actionId: "open", displayName: {}, description: {}, mode: "local" as const, risk: "write" as const,
        invocation: "manual" as const, placements: [], requires: {}, workerEntry: "worker.js", formSchema: null, inputSchema: null, outputSchema: null }] } };
    const task: PluginTaskSnapshot = { taskId: "old-known", runId: "old-run", pluginId: installed.pluginId,
      pluginVersion: installed.version, installationId: "old-record", installationRevision: 1, actionId: "open",
      projectKey: "local:default", collection: null, targetCount: 1, risk: "write", state: "succeeded",
      cancelRequested: false, result: null, error: null };
    store.applyPlugin(installed); store.applyTask(task);
    store.removePlugin(installed.pluginId);
    store.applyPlugin({ ...installed, installationId: "new-record" });
    expect(store.taskForId(task.taskId)).toBeNull();
    store.applyTask({ ...task, taskId: "old-first", runId: "old-first-run", state: "running" });
    expect(store.taskForAction(installed.pluginId, "open")).toBeNull();
    store.applyTask({ ...task, taskId: "unidentified", runId: "unidentified-run", installationId: null });
    expect(store.taskForId("unidentified")).toBeNull();
    store.applyTask({ ...task, taskId: "new-task", runId: "new-run", installationId: "new-record" });
    expect(store.taskForAction(installed.pluginId, "open")?.taskId).toBe("new-task");
  });

  it.each(["local:default", "local:other"])("keeps closed native receipts independent of installation replacement and clears them on %s session reset", (nextProject) => {
    const store = usePluginStore(); store.setProjectContext("local:default", "r1");
    const installed = plugin(2); store.applyPlugin(installed);
    const late: PluginTaskSnapshot = { taskId: "late-native", runId: "late-run", projectKey: installed.projectKey,
      pluginId: installed.pluginId, actionId: "old-action", pluginVersion: installed.version,
      installationId: "retired-record", installationRevision: 1, surfaceToken: "retired-token",
      collection: null, targetCount: 1, risk: "write", state: "aborted", cancelRequested: false,
      result: null, error: { contract: "vibetable.plugin-error.v1", code: "plugin_commit_unknown", message: "check original receipt",
        recoverability: "none", pluginId: null, actionId: null, runId: null, details: {}, causeId: null } };
    store.applyTask(late);
    store.applyTask({ ...late, taskId: "foreign-scope", projectKey: "local:foreign" });
    store.applyTask({ ...late, taskId: "no-source", surfaceToken: null });
    store.applyTask({ ...late, taskId: "no-installation", installationId: null });
    expect(store.nativeTaskReceipts.map(task => task.taskId)).toEqual(["late-native"]);
    expect(store.taskForId(late.taskId)).toBeNull();
    expect(store.tasksForAction(installed.pluginId, late.actionId, "new-token")).toEqual([]);
    store.applyTask({ ...late, state: "succeeded", error: null }, 5);
    expect(store.nativeTaskReceipts[0]?.error?.code).toBe("plugin_commit_unknown");
    store.setProjectContext(nextProject, "new-session");
    expect(store.nativeTaskReceipts).toEqual([]);
  });

  // Bare test readers wrap the receipt protocol the real list() implements:
  // every read registers as the newest activity and settles a terminal.
  function observedReader(
    store: ReturnType<typeof usePluginStore>,
    impl: () => Promise<{ snapshots: readonly PluginSnapshot[]; outcome: "projected" | "superseded" | "retired" }>,
  ): () => Promise<readonly PluginSnapshot[]> {
    return () => {
      const receipt = store.beginObservedCatalogRead(() => true);
      return impl().then(
        ({ snapshots, outcome }) => { store.settleObservedCatalogRead(receipt, outcome); return snapshots; },
        (error: unknown) => { store.settleObservedCatalogRead(receipt, "failed-current"); throw error; },
      );
    };
  }

  it("merges repeated notifications from one owner into a single follow-up read", async () => {
    const store = usePluginStore();
    store.setProjectContext("local:default", "r1");
    const first = deferred<readonly PluginSnapshot[]>();
    let started = 0;
    const project = (snapshots: readonly PluginSnapshot[]) => Promise.resolve(snapshots).then((value) => {
      store.replaceCatalog("local:default", value);
      return value;
    });
    const reader = observedReader(store, () => {
      started += 1;
      return started === 1
        ? first.promise.then((snapshots) => ({ snapshots, outcome: "superseded" as const }))
        : project([plugin(1)]).then((snapshots) => ({ snapshots, outcome: "projected" as const }));
    });
    store.refreshCatalog(reader);
    await Promise.resolve(); // the first read starts
    expect(started).toBe(1);
    // Two more notifications from the same owner arrive while it is reading.
    store.refreshCatalog(reader);
    store.refreshCatalog(reader);
    first.resolve([plugin(1)]);
    for (let i = 0; i < 16; i += 1) await Promise.resolve();
    expect(started).toBe(2); // one merged follow-up, not one per notification
  });

  it("lets a newer sequence preempt a retired old batch instead of reading catch-up", async () => {
    const store = usePluginStore();
    store.setProjectContext("local:default", "r1");
    const oldRead = deferred<readonly PluginSnapshot[]>();
    let oldStarted = 0;
    let freshStarted = 0;
    let oldOwnerRetired = false;
    const project = (snapshots: readonly PluginSnapshot[]) => Promise.resolve(snapshots).then((value) => {
      store.replaceCatalog("local:default", value);
      return value;
    });
    const oldReader = observedReader(store, () => {
      oldStarted += 1;
      return oldRead.promise.then((snapshots) => ({ snapshots, outcome: "retired" as const }));
    });
    const oldOwnerCurrent = () => !oldOwnerRetired;
    const freshReader = observedReader(store, () => {
      freshStarted += 1;
      return project([plugin(1)]).then((snapshots) => ({ snapshots, outcome: "projected" as const }));
    });

    store.refreshCatalog(oldReader, oldOwnerCurrent);
    await Promise.resolve(); // the old batch starts reading
    expect(oldStarted).toBe(1);

    // A new notification advances the catalog sequence while the old read is
    // still in flight, then the old owner retires.
    store.refreshCatalog(freshReader);
    oldOwnerRetired = true;
    oldRead.resolve([plugin(1)]);
    for (let i = 0; i < 16; i += 1) await Promise.resolve();

    // The stale batch must not run a catch-up read; only the fresh batch reads.
    expect(oldStarted).toBe(1);
    expect(freshStarted).toBe(1);
  });

  it("takes over with another live candidate when the reading owner retires mid-flight", async () => {
    const store = usePluginStore();
    store.setProjectContext("local:default", "r1");
    const oldRead = deferred<readonly PluginSnapshot[]>();
    let oldStarted = 0;
    let freshStarted = 0;
    let oldOwnerRetired = false;
    const project = (snapshots: readonly PluginSnapshot[]) => Promise.resolve(snapshots).then((value) => {
      store.replaceCatalog("local:default", value);
      return value;
    });
    const oldReader = observedReader(store, () => {
      oldStarted += 1;
      return oldRead.promise.then((snapshots) => ({ snapshots, outcome: "retired" as const }));
    });
    const oldOwnerCurrent = () => !oldOwnerRetired;
    const freshReader = observedReader(store, () => {
      freshStarted += 1;
      return project([plugin(1)]).then((snapshots) => ({ snapshots, outcome: "projected" as const }));
    });

    store.refreshCatalog(oldReader, oldOwnerCurrent);
    store.refreshCatalog(freshReader);
    await Promise.resolve();
    expect(oldStarted).toBe(1);

    oldOwnerRetired = true;
    oldRead.resolve([plugin(1)]);
    for (let i = 0; i < 16; i += 1) await Promise.resolve();

    expect(oldStarted).toBe(1);
    expect(freshStarted).toBe(1); // one takeover read serves the notification
  });

  it("keeps a barriered demand waiting for the write terminal instead of racing it", async () => {
    const store = usePluginStore();
    store.setProjectContext("local:default", "r1");
    let rootStarted = 0;
    let writerStarted = 0;
    let releaseRoot!: (snapshots: readonly PluginSnapshot[]) => void;
    const rootRead = new Promise<readonly PluginSnapshot[]>((done) => { releaseRoot = done; });
    const project = (snapshots: readonly PluginSnapshot[]) => Promise.resolve(snapshots).then((value) => {
      store.replaceCatalog("local:default", value);
      return value;
    });
    const rootReader = observedReader(store, () => {
      rootStarted += 1;
      return rootStarted === 1
        ? rootRead.then((snapshots) => ({ snapshots, outcome: "superseded" as const }))
        : project([plugin(2)]).then((snapshots) => ({ snapshots, outcome: "projected" as const }));
    });
    const writerReader = observedReader(store, () => {
      writerStarted += 1;
      return project([plugin(2)]).then((snapshots) => ({ snapshots, outcome: "projected" as const }));
    });

    store.refreshCatalog(rootReader);
    await Promise.resolve();
    expect(rootStarted).toBe(1);
    // mutateCatalog-style write barrier: the sequence advances behind a write.
    const barrier = store.beginCatalogWriteBarrier();
    releaseRoot([]); // the superseded read settles without projecting
    for (let i = 0; i < 16; i += 1) await Promise.resolve();
    // The unsatisfied demand waits for the write terminal; no racing read.
    expect(rootStarted).toBe(1);
    // The write terminal releases only its own token and queues the refresh.
    store.endCatalogWriteBarrier(barrier);
    store.refreshCatalog(writerReader);
    for (let i = 0; i < 16; i += 1) await Promise.resolve();
    // Exactly one catch-up read serves the pending demand; the write itself
    // executed exactly once and nothing re-sends it.
    expect(rootStarted).toBe(2);
    expect(writerStarted).toBe(0);
  });

  it("projects but never consumes demands from a read begun behind an active write barrier", async () => {
    const store = usePluginStore();
    store.setProjectContext("local:default", "r1");
    let demandStarted = 0;
    const project = (snapshots: readonly PluginSnapshot[]) => Promise.resolve(snapshots).then((value) => {
      store.replaceCatalog("local:default", value);
      return value;
    });
    const demandReader = observedReader(store, () => {
      demandStarted += 1;
      return project([plugin(1)]).then((snapshots) => ({ snapshots, outcome: "projected" as const }));
    });
    const directReader = observedReader(store, () =>
      project([plugin(1)]).then((snapshots) => ({ snapshots, outcome: "projected" as const })));

    const barrier = store.beginCatalogWriteBarrier();
    store.refreshCatalog(demandReader); // queued behind the barrier
    // A direct read (real recovery entry) starts inside the barrier window.
    await directReader();
    for (let i = 0; i < 8; i += 1) await Promise.resolve();
    expect(demandStarted).toBe(0); // scheduler still held by the barrier

    // The write terminal releases its own token; the surviving demand gets
    // its post-write authority read.
    store.endCatalogWriteBarrier(barrier);
    for (let i = 0; i < 16; i += 1) await Promise.resolve();
    expect(demandStarted).toBe(1);
  });

  it("ignores an old-generation barrier release while a new write is pending", async () => {
    const store = usePluginStore();
    store.setProjectContext("local:default", "r1");
    let freshStarted = 0;
    const project = (snapshots: readonly PluginSnapshot[]) => Promise.resolve(snapshots).then((value) => {
      store.replaceCatalog("local:default", value);
      return value;
    });
    const freshReader = observedReader(store, () => {
      freshStarted += 1;
      return project([plugin(1)]).then((snapshots) => ({ snapshots, outcome: "projected" as const }));
    });

    const staleBarrier = store.beginCatalogWriteBarrier();
    store.setProjectContext("local:default", "r2"); // generation switch clears it
    store.refreshCatalog(freshReader);
    const newBarrier = store.beginCatalogWriteBarrier(); // a new write is pending
    store.endCatalogWriteBarrier(staleBarrier);         // old terminal: inert
    for (let i = 0; i < 16; i += 1) await Promise.resolve();
    expect(freshStarted).toBe(0); // the new barrier still holds the demand

    store.endCatalogWriteBarrier(newBarrier);
    for (let i = 0; i < 16; i += 1) await Promise.resolve();
    expect(freshStarted).toBe(1);
  });

});
