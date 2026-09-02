import assert from "node:assert/strict";
import test from "node:test";

import { activateWorkspaceAndWaitForDatabaseOpened } from "./workspace_activation_readiness.mjs";

function makeDependencies(overrides = {}) {
  const calls = [];
  const {
    waitForReadiness = async (timeoutMs) => {
      calls.push(["readiness", timeoutMs]);
      return {
        type: "database.opened",
        payload: { projectKey: "local:workspace-7", projectRevision: "7" },
      };
    },
    releaseCapture = async () => calls.push(["release"]),
    ...dependencyOverrides
  } = overrides;
  return {
    calls,
    dependencies: {
      beginCapture: async (expectation) => {
        calls.push(["capture", expectation]);
        return { wait: waitForReadiness, release: releaseCapture };
      },
      activate: async () => calls.push(["activate"]),
      waitForActivation: async (timeoutMs) => {
        calls.push(["activation", timeoutMs]);
        return { kind: "opened" };
      },
      ...dependencyOverrides,
    },
  };
}

test("captures database.opened before activation and returns only after readiness", async () => {
  const { calls, dependencies } = makeDependencies();

  const opened = await activateWorkspaceAndWaitForDatabaseOpened(dependencies);

  assert.equal(opened.payload.projectRevision, "7");
  assert.equal(opened.payload.projectKey, "local:workspace-7");
  assert.deepEqual(calls, [
    ["capture", { method: "workspace.open" }],
    ["activate"],
    ["activation", 60_000],
    ["readiness", 60_000],
    ["release"],
  ]);
});

test("rejects UI activation failure even when exact bridge readiness succeeds", async () => {
  const { calls, dependencies } = makeDependencies({
    waitForActivation: async () => ({ kind: "failed", message: "workspace rejected" }),
  });

  await assert.rejects(
    activateWorkspaceAndWaitForDatabaseOpened(dependencies),
    /workspace activation failed: workspace rejected/,
  );
  assert.deepEqual(calls, [
    ["capture", { method: "workspace.open" }],
    ["activate"],
    ["readiness", 60_000],
    ["release"],
  ]);
});

test("rejects a database.opened message without a usable project revision", async () => {
  const { dependencies } = makeDependencies({
    waitForReadiness: async () => ({
      type: "database.opened",
      payload: { projectKey: "local:workspace-7", projectRevision: " " },
    }),
  });

  await assert.rejects(
    activateWorkspaceAndWaitForDatabaseOpened(dependencies),
    /invalid database context/,
  );
});

test("rejects a database.opened message without an authoritative project key", async () => {
  const { dependencies } = makeDependencies({
    waitForReadiness: async () => ({
      type: "database.opened",
      payload: { projectKey: " ", projectRevision: "7" },
    }),
  });

  await assert.rejects(
    activateWorkspaceAndWaitForDatabaseOpened(dependencies),
    /invalid database context/,
  );
});

test("propagates the database readiness timeout", async () => {
  const { dependencies } = makeDependencies({
    waitForReadiness: async () => {
      throw new Error("database.opened timed out");
    },
  });

  await assert.rejects(
    activateWorkspaceAndWaitForDatabaseOpened(dependencies),
    /database\.opened timed out/,
  );
});

test("releases exact bridge ownership when activation throws before UI readiness", async () => {
  const { calls, dependencies } = makeDependencies({
    activate: async () => {
      throw new Error("click failed");
    },
  });

  await assert.rejects(
    activateWorkspaceAndWaitForDatabaseOpened(dependencies),
    /click failed/,
  );
  assert.deepEqual(calls, [
    ["capture", { method: "workspace.open" }],
    ["release"],
  ]);
});

test("starts bridge readiness before unresolved UI readiness can hide an exact failure", async () => {
  let resolveActivation;
  let bridgeWaitStarted = false;
  const { dependencies } = makeDependencies({
    waitForActivation: () => new Promise((resolve) => { resolveActivation = resolve; }),
    waitForReadiness: async () => {
      bridgeWaitStarted = true;
      throw new Error("exact bridge readiness failed");
    },
  });

  const activation = activateWorkspaceAndWaitForDatabaseOpened(dependencies);
  const outcome = activation.then(
    () => ({ error: null }),
    error => ({ error }),
  );
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(bridgeWaitStarted, true);
  resolveActivation({ kind: "opened" });
  assert.match((await outcome).error.message, /exact bridge readiness failed/);
});
