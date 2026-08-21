import assert from "node:assert/strict";
import test from "node:test";

import { activateWorkspaceAndWaitForDatabaseOpened } from "./workspace_activation_readiness.mjs";

function makeDependencies(overrides = {}) {
  const calls = [];
  return {
    calls,
    dependencies: {
      beginCapture: async (types) => calls.push(["capture", types]),
      activate: async () => calls.push(["activate"]),
      waitForActivation: async (timeoutMs) => {
        calls.push(["activation", timeoutMs]);
        return { kind: "opened" };
      },
      waitForDatabaseOpened: async (timeoutMs) => {
        calls.push(["database.opened", timeoutMs]);
        return {
          type: "database.opened",
          payload: { projectKey: "local:workspace-7", projectRevision: "7" },
        };
      },
      ...overrides,
    },
  };
}

test("captures database.opened before activation and returns only after readiness", async () => {
  const { calls, dependencies } = makeDependencies();

  const opened = await activateWorkspaceAndWaitForDatabaseOpened(dependencies);

  assert.equal(opened.payload.projectRevision, "7");
  assert.equal(opened.payload.projectKey, "local:workspace-7");
  assert.deepEqual(calls, [
    ["capture", ["database.opened"]],
    ["activate"],
    ["activation", 60_000],
    ["database.opened", 60_000],
  ]);
});

test("does not wait for database readiness after activation failure", async () => {
  const { dependencies } = makeDependencies({
    waitForActivation: async () => ({ kind: "failed", message: "workspace rejected" }),
    waitForDatabaseOpened: async () => assert.fail("unexpected database wait"),
  });

  await assert.rejects(
    activateWorkspaceAndWaitForDatabaseOpened(dependencies),
    /workspace activation failed: workspace rejected/,
  );
});

test("rejects a database.opened message without a usable project revision", async () => {
  const { dependencies } = makeDependencies({
    waitForDatabaseOpened: async () => ({
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
    waitForDatabaseOpened: async () => ({
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
    waitForDatabaseOpened: async () => {
      throw new Error("database.opened timed out");
    },
  });

  await assert.rejects(
    activateWorkspaceAndWaitForDatabaseOpened(dependencies),
    /database\.opened timed out/,
  );
});
