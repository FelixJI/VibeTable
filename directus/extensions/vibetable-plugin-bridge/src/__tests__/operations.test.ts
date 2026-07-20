import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { PluginInteractionBroker } from "../broker.ts";
import { executeConfirm } from "../confirm/handler.ts";
import { executeProgress } from "../progress/handler.ts";


describe("vibetable.confirm@1 operation", () => {
  it("waits on the bridge interaction owned by the current Directus user", async () => {
    const broker = new PluginInteractionBroker({
      createInteractionId: () => "interaction-operation",
    });
    const caller = { userId: "user-alice", projectId: "project-current" };
    broker.registerRun(
      {
        contract: "vibetable.plugin-run.v1",
        runId: "run-operation",
        pluginId: "plugin.example",
        actionId: "normalize",
      },
      caller,
    );

    const waiting = executeConfirm(
      {
        contract: "vibetable.confirm.v1",
        runId: "run-operation",
        risk: "write",
        title: "Approve operation",
        preview: { affectedCount: 3 },
      },
      {
        accountability: { user: "user-alice" },
        env: { VIBETABLE_PROJECT_ID: "project-current" },
      },
      broker,
    );
    assert.equal(
      broker.getRun("run-operation", caller).pendingConfirmation?.interactionId,
      "interaction-operation",
    );
    broker.decideConfirmation(
      "run-operation",
      "interaction-operation",
      "approve",
      caller,
    );
    assert.deepEqual(await waiting, {
      approved: true,
      interactionId: "interaction-operation",
    });
  });
});

describe("vibetable.progress@1 operation", () => {
  it("updates the owning run and returns its cancellation marker", () => {
    const broker = new PluginInteractionBroker();
    const caller = { userId: "user-alice", projectId: "project-current" };
    broker.registerRun(
      {
        contract: "vibetable.plugin-run.v1",
        runId: "run-progress-operation",
        pluginId: "plugin.example",
        actionId: "normalize",
      },
      caller,
    );
    broker.requestCancel("run-progress-operation", caller);

    assert.deepEqual(
      executeProgress(
        {
          contract: "vibetable.progress.v1",
          runId: "run-progress-operation",
          current: 8,
          total: 10,
          message: "Processing 8",
          cancellable: true,
        },
        {
          accountability: { user: "user-alice" },
          env: { VIBETABLE_PROJECT_ID: "project-current" },
        },
        broker,
      ),
      { cancelRequested: true },
    );
  });
});
