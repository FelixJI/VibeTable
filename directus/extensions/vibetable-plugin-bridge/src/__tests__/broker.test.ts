import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  BridgeError,
  PluginInteractionBroker,
} from "../broker.ts";


const alice = { userId: "user-alice", projectId: "project-a" };


describe("PluginInteractionBroker confirmation contract", () => {
  it("never approves a confirmation without an active registered run", async () => {
    const broker = new PluginInteractionBroker();

    await assert.rejects(
      broker.requestConfirmation(
        {
          contract: "vibetable.confirm.v1",
          runId: "run-inactive",
          risk: "write",
          title: "Update records",
          preview: { affectedCount: 1 },
          timeoutMs: 1_000,
        },
        alice,
      ),
      (error: unknown) =>
        error instanceof BridgeError && error.code === "VIBETABLE_RUN_NOT_ACTIVE",
    );
  });

  it("lets the registered caller observe and approve one pending confirmation", async () => {
    const broker = new PluginInteractionBroker({
      createInteractionId: () => "interaction-1",
    });
    broker.registerRun(
      {
        contract: "vibetable.plugin-run.v1",
        runId: "run-1",
        pluginId: "plugin.example",
        actionId: "normalize",
      },
      alice,
    );

    const waiting = broker.requestConfirmation(
      {
        contract: "vibetable.confirm.v1",
        runId: "run-1",
        risk: "write",
        title: "Update records",
        preview: { affectedCount: 2 },
      },
      alice,
    );

    assert.equal(
      broker.getRun("run-1", alice).pendingConfirmation?.interactionId,
      "interaction-1",
    );
    assert.deepEqual(
      broker.decideConfirmation(
        "run-1",
        "interaction-1",
        "approve",
        alice,
      ),
      { status: "decided", decision: "approved" },
    );
    assert.deepEqual(await waiting, {
      approved: true,
      interactionId: "interaction-1",
    });
  });

  it("allows only one pending confirmation for a run", async () => {
    const ids = ["interaction-1", "interaction-2"];
    const broker = new PluginInteractionBroker({
      createInteractionId: () => ids.shift() ?? "unexpected",
    });
    broker.registerRun(
      {
        contract: "vibetable.plugin-run.v1",
        runId: "run-1",
        pluginId: "plugin.example",
        actionId: "normalize",
      },
      alice,
    );
    const first = broker.requestConfirmation(
      {
        contract: "vibetable.confirm.v1",
        runId: "run-1",
        risk: "write",
        title: "First",
        preview: {},
      },
      alice,
    );

    await assert.rejects(
      broker.requestConfirmation(
        {
          contract: "vibetable.confirm.v1",
          runId: "run-1",
          risk: "write",
          title: "Second",
          preview: {},
        },
        alice,
      ),
      (error: unknown) =>
        error instanceof BridgeError &&
        error.code === "VIBETABLE_CONFIRMATION_ALREADY_PENDING",
    );
    broker.decideConfirmation("run-1", "interaction-1", "approve", alice);
    await first;
  });

  it("returns the original rejection for duplicate decisions", async () => {
    const broker = new PluginInteractionBroker({
      createInteractionId: () => "interaction-rejected",
    });
    broker.registerRun(
      {
        contract: "vibetable.plugin-run.v1",
        runId: "run-rejected",
        pluginId: "plugin.example",
        actionId: "normalize",
      },
      alice,
    );
    const waiting = broker.requestConfirmation(
      {
        contract: "vibetable.confirm.v1",
        runId: "run-rejected",
        risk: "write",
        title: "Reject me",
        preview: {},
      },
      alice,
    );

    assert.deepEqual(
      broker.decideConfirmation(
        "run-rejected",
        "interaction-rejected",
        "reject",
        alice,
      ),
      { status: "decided", decision: "rejected" },
    );
    await assert.rejects(
      waiting,
      (error: unknown) =>
        error instanceof BridgeError &&
        error.code === "VIBETABLE_CONFIRMATION_REJECTED",
    );
    assert.deepEqual(
      broker.decideConfirmation(
        "run-rejected",
        "interaction-rejected",
        "approve",
        alice,
      ),
      { status: "already-decided", decision: "rejected" },
    );
  });

  it("expires the run and keeps an idempotent expired interaction tombstone", async () => {
    let expire: (() => void) | undefined;
    const broker = new PluginInteractionBroker({
      now: () => 1_000,
      createInteractionId: () => "interaction-expired",
      schedule: (callback) => {
        expire = callback;
        return 1;
      },
      clearSchedule: () => undefined,
    });
    broker.registerRun(
      {
        contract: "vibetable.plugin-run.v1",
        runId: "run-expired",
        pluginId: "plugin.example",
        actionId: "normalize",
      },
      alice,
    );
    const waiting = broker.requestConfirmation(
      {
        contract: "vibetable.confirm.v1",
        runId: "run-expired",
        risk: "write",
        title: "Expire me",
        preview: {},
        timeoutMs: 5_000,
      },
      alice,
    );

    assert.ok(expire);
    expire();
    await assert.rejects(
      waiting,
      (error: unknown) =>
        error instanceof BridgeError &&
        error.code === "VIBETABLE_CONFIRMATION_EXPIRED",
    );
    assert.throws(
      () => broker.getRun("run-expired", alice),
      (error: unknown) =>
        error instanceof BridgeError && error.code === "VIBETABLE_RUN_NOT_ACTIVE",
    );
    assert.deepEqual(
      broker.decideConfirmation(
        "run-expired",
        "interaction-expired",
        "approve",
        alice,
      ),
      { status: "already-decided", decision: "expired" },
    );
  });
});

describe("PluginInteractionBroker progress and cancellation contract", () => {
  it("keeps progress monotonic and reports the idempotent cancel marker", () => {
    const broker = new PluginInteractionBroker();
    broker.registerRun(
      {
        contract: "vibetable.plugin-run.v1",
        runId: "run-progress",
        pluginId: "plugin.example",
        actionId: "normalize",
      },
      alice,
    );

    assert.deepEqual(
      broker.reportProgress(
        {
          contract: "vibetable.progress.v1",
          runId: "run-progress",
          current: 4,
          total: 10,
          message: "Processing 4",
          cancellable: true,
        },
        alice,
      ),
      { cancelRequested: false },
    );
    assert.throws(
      () =>
        broker.reportProgress(
          {
            contract: "vibetable.progress.v1",
            runId: "run-progress",
            current: 3,
            total: 10,
            message: "Going backwards",
            cancellable: true,
          },
          alice,
        ),
      (error: unknown) =>
        error instanceof BridgeError && error.code === "VIBETABLE_PROGRESS_REGRESSION",
    );
    assert.deepEqual(broker.requestCancel("run-progress", alice), {
      status: "cancel-requested",
    });
    assert.deepEqual(broker.requestCancel("run-progress", alice), {
      status: "already-requested",
    });
    assert.deepEqual(
      broker.reportProgress(
        {
          contract: "vibetable.progress.v1",
          runId: "run-progress",
          current: 5,
          total: 10,
          message: "Observed cancellation",
          cancellable: true,
        },
        alice,
      ),
      { cancelRequested: true },
    );
  });

  it("cancels a confirmation wait without claiming the whole run stopped", async () => {
    const broker = new PluginInteractionBroker({
      createInteractionId: () => "interaction-cancelled",
    });
    broker.registerRun(
      {
        contract: "vibetable.plugin-run.v1",
        runId: "run-cancel-confirm",
        pluginId: "plugin.example",
        actionId: "normalize",
      },
      alice,
    );
    const waiting = broker.requestConfirmation(
      {
        contract: "vibetable.confirm.v1",
        runId: "run-cancel-confirm",
        risk: "write",
        title: "Waiting",
        preview: {},
      },
      alice,
    );

    broker.requestCancel("run-cancel-confirm", alice);
    await assert.rejects(
      waiting,
      (error: unknown) =>
        error instanceof BridgeError &&
        error.code === "VIBETABLE_CONFIRMATION_CANCELLED",
    );
    const state = broker.getRun("run-cancel-confirm", alice);
    assert.equal(state.cancelRequested, true);
    assert.equal(state.pendingConfirmation, undefined);
  });
});

describe("PluginInteractionBroker resource bounds", () => {
  it("rejects new runs after the configured active-run capacity", () => {
    const broker = new PluginInteractionBroker({ maxRuns: 1 });
    broker.registerRun(
      {
        contract: "vibetable.plugin-run.v1",
        runId: "run-1",
        pluginId: "plugin.example",
        actionId: "normalize",
      },
      alice,
    );

    assert.throws(
      () =>
        broker.registerRun(
          {
            contract: "vibetable.plugin-run.v1",
            runId: "run-2",
            pluginId: "plugin.example",
            actionId: "normalize",
          },
          alice,
        ),
      (error: unknown) =>
        error instanceof BridgeError && error.code === "VIBETABLE_RUN_CAPACITY",
    );
  });

  it("removes an active run when its bounded TTL elapses", () => {
    let expireRun: (() => void) | undefined;
    const broker = new PluginInteractionBroker({
      now: () => 10_000,
      schedule: (callback) => {
        expireRun = callback;
        return 1;
      },
      clearSchedule: () => undefined,
    });
    broker.registerRun(
      {
        contract: "vibetable.plugin-run.v1",
        runId: "run-ttl",
        pluginId: "plugin.example",
        actionId: "normalize",
        ttlMs: 5_000,
      },
      alice,
    );

    assert.ok(expireRun);
    expireRun();
    assert.throws(
      () => broker.getRun("run-ttl", alice),
      (error: unknown) =>
        error instanceof BridgeError && error.code === "VIBETABLE_RUN_NOT_ACTIVE",
    );
  });

  it("rejects oversized confirmation payloads and timeouts above 15 minutes", async () => {
    const broker = new PluginInteractionBroker({ maxPayloadBytes: 128 });
    broker.registerRun(
      {
        contract: "vibetable.plugin-run.v1",
        runId: "run-limits",
        pluginId: "plugin.example",
        actionId: "normalize",
      },
      alice,
    );

    await assert.rejects(
      broker.requestConfirmation(
        {
          contract: "vibetable.confirm.v1",
          runId: "run-limits",
          risk: "write",
          title: "Large preview",
          preview: { sampleRows: [{ value: "x".repeat(256) }] },
        },
        alice,
      ),
      (error: unknown) =>
        error instanceof BridgeError && error.code === "VIBETABLE_PAYLOAD_TOO_LARGE",
    );
    await assert.rejects(
      broker.requestConfirmation(
        {
          contract: "vibetable.confirm.v1",
          runId: "run-limits",
          risk: "write",
          title: "Too long",
          preview: {},
          timeoutMs: 15 * 60_000 + 1,
        },
        alice,
      ),
      (error: unknown) =>
        error instanceof BridgeError &&
        error.code === "VIBETABLE_CONFIRMATION_TIMEOUT_INVALID",
    );
  });

  it("bounds idempotency tombstones while preserving the newest decision", async () => {
    const ids = ["interaction-1", "interaction-2"];
    const broker = new PluginInteractionBroker({
      maxSettledInteractions: 1,
      createInteractionId: () => ids.shift() ?? "unexpected",
    });
    for (const suffix of ["1", "2"]) {
      const runId = `run-${suffix}`;
      broker.registerRun(
        {
          contract: "vibetable.plugin-run.v1",
          runId,
          pluginId: "plugin.example",
          actionId: "normalize",
        },
        alice,
      );
      const waiting = broker.requestConfirmation(
        {
          contract: "vibetable.confirm.v1",
          runId,
          risk: "write",
          title: "Approve",
          preview: {},
        },
        alice,
      );
      broker.decideConfirmation(
        runId,
        `interaction-${suffix}`,
        "approve",
        alice,
      );
      await waiting;
      broker.completeRun(runId, "succeeded", alice);
    }

    assert.throws(
      () =>
        broker.decideConfirmation(
          "run-1",
          "interaction-1",
          "approve",
          alice,
        ),
      (error: unknown) =>
        error instanceof BridgeError && error.code === "VIBETABLE_RUN_NOT_ACTIVE",
    );
    assert.deepEqual(
      broker.decideConfirmation(
        "run-2",
        "interaction-2",
        "reject",
        alice,
      ),
      { status: "already-decided", decision: "approved" },
    );
  });
});
