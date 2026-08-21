import assert from "node:assert/strict";
import test from "node:test";

import {
  acknowledgeExpectedSidecarRecoveryFailure,
  beginSidecarRecoveryNotificationFailureWindowInPage,
  isExpectedSidecarRecoveryFailure,
  releaseSidecarRecoveryNotificationFailureWindowInPage,
  settleSidecarRecoveryNotificationFailureWindowInPage,
  SidecarRecoveryContractError,
  SidecarRecoveryReadWindow,
} from "./bridge_failure_policy.mjs";

const expected = {
  type: "operation.failed",
  requestId: "e2e-query",
  payload: { code: "BACKEND_UNAVAILABLE" },
};

function deferred() {
  let resolve;
  const promise = new Promise(resolvePromise => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

test("acknowledges the exact sidecar recovery failure", async () => {
  const acknowledged = [];
  assert.equal(isExpectedSidecarRecoveryFailure(expected), true);
  assert.equal(
    await acknowledgeExpectedSidecarRecoveryFailure(
      expected,
      async response => acknowledged.push(response.requestId),
    ),
    true,
  );
  assert.deepEqual(acknowledged, ["e2e-query"]);
});

test("does not acknowledge another failure code", async () => {
  const unexpected = {
    ...expected,
    payload: { code: "PRODUCT_DATA_FAILED" },
  };
  assert.equal(isExpectedSidecarRecoveryFailure(unexpected), false);
  assert.equal(
    await acknowledgeExpectedSidecarRecoveryFailure(
      unexpected,
      async () => assert.fail("unexpected acknowledgement"),
    ),
    false,
  );
});

test("does not acknowledge a response without a request identity", async () => {
  const missingRequestId = { ...expected, requestId: null };
  assert.equal(isExpectedSidecarRecoveryFailure(missingRequestId), false);
  assert.equal(
    await acknowledgeExpectedSidecarRecoveryFailure(
      missingRequestId,
      async () => assert.fail("unexpected acknowledgement"),
    ),
    false,
  );
});

test("settles only matched table selection failures inside the owned restart window", () => {
  const expectedNotification = {
    cursor: 3,
    requestId: null,
    responseType: "operation.failed",
    code: "BACKEND_UNAVAILABLE",
    operation: "table.selected",
  };
  const beforeWindow = { ...expectedNotification, cursor: 1 };
  const correlatedFailure = { ...expectedNotification, cursor: 4, requestId: "query-1" };
  const wrongOperation = { ...expectedNotification, cursor: 5, operation: "query" };
  const wrongCode = { ...expectedNotification, cursor: 6, code: "PRODUCT_DATA_FAILED" };
  const wrongTableFailure = { ...expectedNotification, cursor: 8 };
  const repeatedNotificationFailure = { ...expectedNotification, cursor: 10 };
  const excessFailure = { ...expectedNotification, cursor: 11 };
  const previous = globalThis.__vibetableE2EBridgeDiagnostics;
  globalThis.__vibetableE2EBridgeDiagnostics = {
    diagnosticCursor: 1,
    notifications: [],
    failures: [beforeWindow],
    acknowledgedFailures: [],
  };
  try {
    assert.deepEqual(beginSidecarRecoveryNotificationFailureWindowInPage({
      ownerToken: "scenario-07",
      tableId: "attachments",
    }), { state: "owned", startCursor: 1 });
    globalThis.__vibetableE2EBridgeDiagnostics.notifications.push({
      cursor: 2,
      requestType: "table.selected",
      recoveryOwnerToken: "scenario-07",
    });
    globalThis.__vibetableE2EBridgeDiagnostics.failures.push(
      expectedNotification,
      correlatedFailure,
      wrongOperation,
      wrongCode,
      wrongTableFailure,
      repeatedNotificationFailure,
      excessFailure,
    );
    globalThis.__vibetableE2EBridgeDiagnostics.notifications.push({
      cursor: 7,
      requestType: "table.selected",
      recoveryOwnerToken: null,
    }, {
      cursor: 9,
      requestType: "table.selected",
      recoveryOwnerToken: "scenario-07",
    });
    globalThis.__vibetableE2EBridgeDiagnostics.diagnosticCursor = 11;

    assert.deepEqual(settleSidecarRecoveryNotificationFailureWindowInPage({
      ownerToken: "scenario-07",
      deadlineAt: Date.now() + 1_000,
    }), {
      state: "settled",
      startCursor: 1,
      endCursor: 11,
      acknowledgedCount: 2,
    });
    assert.deepEqual(
      globalThis.__vibetableE2EBridgeDiagnostics.failures,
      [
        beforeWindow,
        correlatedFailure,
        wrongOperation,
        wrongCode,
        wrongTableFailure,
        excessFailure,
      ],
    );
    assert.deepEqual(
      globalThis.__vibetableE2EBridgeDiagnostics.acknowledgedFailures,
      [expectedNotification, repeatedNotificationFailure],
    );

    const afterWindow = { ...expectedNotification, cursor: 13 };
    globalThis.__vibetableE2EBridgeDiagnostics.notifications.push({
      cursor: 12,
      requestType: "table.selected",
      recoveryOwnerToken: "scenario-07",
    });
    globalThis.__vibetableE2EBridgeDiagnostics.failures.push(afterWindow);
    globalThis.__vibetableE2EBridgeDiagnostics.diagnosticCursor = 13;
    assert.deepEqual(settleSidecarRecoveryNotificationFailureWindowInPage({
      ownerToken: "scenario-07",
      deadlineAt: Date.now() + 1_000,
    }), { state: "stale" });
    assert.equal(globalThis.__vibetableE2EBridgeDiagnostics.failures.at(-1), afterWindow);
  } finally {
    if (previous === undefined) delete globalThis.__vibetableE2EBridgeDiagnostics;
    else globalThis.__vibetableE2EBridgeDiagnostics = previous;
  }
});

test("a replaced, released, or expired recovery owner cannot consume failures", () => {
  const expectedNotification = {
    cursor: 2,
    requestId: null,
    responseType: "operation.failed",
    code: "BACKEND_UNAVAILABLE",
    operation: "table.selected",
  };
  const previous = globalThis.__vibetableE2EBridgeDiagnostics;
  globalThis.__vibetableE2EBridgeDiagnostics = {
    diagnosticCursor: 0,
    notifications: [],
    failures: [],
    acknowledgedFailures: [],
  };
  try {
    beginSidecarRecoveryNotificationFailureWindowInPage({
      ownerToken: "old",
      tableId: "attachments",
    });
    beginSidecarRecoveryNotificationFailureWindowInPage({
      ownerToken: "current",
      tableId: "attachments",
    });
    globalThis.__vibetableE2EBridgeDiagnostics.notifications.push({
      cursor: 1,
      requestType: "table.selected",
      recoveryOwnerToken: "current",
    });
    globalThis.__vibetableE2EBridgeDiagnostics.failures.push(expectedNotification);
    globalThis.__vibetableE2EBridgeDiagnostics.diagnosticCursor = 2;

    assert.deepEqual(settleSidecarRecoveryNotificationFailureWindowInPage({
      ownerToken: "old",
      deadlineAt: Date.now() + 1_000,
    }), { state: "stale" });
    assert.deepEqual(settleSidecarRecoveryNotificationFailureWindowInPage({
      ownerToken: "current",
      deadlineAt: 0,
    }), { state: "expired" });
    assert.deepEqual(globalThis.__vibetableE2EBridgeDiagnostics.failures, [expectedNotification]);

    assert.deepEqual(releaseSidecarRecoveryNotificationFailureWindowInPage({
      ownerToken: "current",
    }), { state: "released" });
    assert.deepEqual(settleSidecarRecoveryNotificationFailureWindowInPage({
      ownerToken: "current",
      deadlineAt: Date.now() + 1_000,
    }), { state: "stale" });
    assert.deepEqual(globalThis.__vibetableE2EBridgeDiagnostics.acknowledgedFailures, []);
  } finally {
    if (previous === undefined) delete globalThis.__vibetableE2EBridgeDiagnostics;
    else globalThis.__vibetableE2EBridgeDiagnostics = previous;
  }
});

test("does not lend an owned notification to a later wrong-table failure", () => {
  const wrongTableFailure = {
    cursor: 3,
    requestId: null,
    responseType: "operation.failed",
    code: "BACKEND_UNAVAILABLE",
    operation: "table.selected",
  };
  const excessFailure = { ...wrongTableFailure, cursor: 4 };
  const previous = globalThis.__vibetableE2EBridgeDiagnostics;
  globalThis.__vibetableE2EBridgeDiagnostics = {
    diagnosticCursor: 0,
    notifications: [],
    failures: [],
    acknowledgedFailures: [],
  };
  try {
    beginSidecarRecoveryNotificationFailureWindowInPage({
      ownerToken: "scenario-07",
      tableId: "attachments",
    });
    globalThis.__vibetableE2EBridgeDiagnostics.notifications.push({
      cursor: 1,
      requestType: "table.selected",
      recoveryOwnerToken: "scenario-07",
    }, {
      cursor: 2,
      requestType: "table.selected",
      recoveryOwnerToken: null,
    });
    globalThis.__vibetableE2EBridgeDiagnostics.failures.push(
      wrongTableFailure,
      excessFailure,
    );
    globalThis.__vibetableE2EBridgeDiagnostics.diagnosticCursor = 4;

    const settled = settleSidecarRecoveryNotificationFailureWindowInPage({
      ownerToken: "scenario-07",
      deadlineAt: Date.now() + 1_000,
    });
    assert.equal(settled.acknowledgedCount, 0);
    assert.deepEqual(
      globalThis.__vibetableE2EBridgeDiagnostics.failures,
      [wrongTableFailure, excessFailure],
    );
  } finally {
    if (previous === undefined) delete globalThis.__vibetableE2EBridgeDiagnostics;
    else globalThis.__vibetableE2EBridgeDiagnostics = previous;
  }
});

test("a wrong-code terminal consumes its notification slot without hiding a later excess failure", () => {
  const wrongCode = {
    cursor: 2,
    requestId: null,
    responseType: "operation.failed",
    code: "PRODUCT_DATA_FAILED",
    operation: "table.selected",
  };
  const excessFailure = {
    ...wrongCode,
    cursor: 3,
    code: "BACKEND_UNAVAILABLE",
  };
  const previous = globalThis.__vibetableE2EBridgeDiagnostics;
  globalThis.__vibetableE2EBridgeDiagnostics = {
    diagnosticCursor: 0,
    notifications: [],
    failures: [],
    acknowledgedFailures: [],
  };
  try {
    beginSidecarRecoveryNotificationFailureWindowInPage({
      ownerToken: "scenario-07",
      tableId: "attachments",
    });
    globalThis.__vibetableE2EBridgeDiagnostics.notifications.push({
      cursor: 1,
      requestType: "table.selected",
      recoveryOwnerToken: "scenario-07",
    });
    globalThis.__vibetableE2EBridgeDiagnostics.failures.push(wrongCode, excessFailure);
    globalThis.__vibetableE2EBridgeDiagnostics.diagnosticCursor = 3;

    const settled = settleSidecarRecoveryNotificationFailureWindowInPage({
      ownerToken: "scenario-07",
      deadlineAt: Date.now() + 1_000,
    });
    assert.equal(settled.acknowledgedCount, 0);
    assert.deepEqual(
      globalThis.__vibetableE2EBridgeDiagnostics.failures,
      [wrongCode, excessFailure],
    );
  } finally {
    if (previous === undefined) delete globalThis.__vibetableE2EBridgeDiagnostics;
    else globalThis.__vibetableE2EBridgeDiagnostics = previous;
  }
});

test("propagates acknowledgement failures", async () => {
  await assert.rejects(
    acknowledgeExpectedSidecarRecoveryFailure(expected, async () => {
      throw new Error("diagnostics acknowledgement failed");
    }),
    /diagnostics acknowledgement failed/,
  );
});

test("owns a late recovery failure after the first observation expires", async () => {
  let now = 0;
  const terminals = new Map();
  const observations = [];
  const acknowledged = [];
  const released = [];
  const window = new SidecarRecoveryReadWindow({
    deadlineAt: 60_000,
    now: () => now,
    observeTerminal: async (requestId, timeoutMs) => {
      observations.push({ requestId, timeoutMs });
      if (requestId === "probe-1" && observations.length === 1) {
        now = 5_000;
        return null;
      }
      return terminals.get(requestId) ?? null;
    },
    releaseRequest: async requestId => released.push(requestId),
    acknowledge: async response => acknowledged.push(response.requestId),
  });

  window.own("probe-1");
  assert.equal(await window.observe("probe-1"), null);

  now = 5_660;
  terminals.set("probe-1", {
    type: "operation.failed",
    requestId: "probe-1",
    payload: { code: "BACKEND_UNAVAILABLE" },
  });
  terminals.set("probe-2", {
    type: "query.page",
    requestId: "probe-2",
    payload: { rows: [{ id: "row-1" }] },
  });
  window.own("probe-2");
  assert.equal((await window.observe("probe-2")).type, "query.page");

  await window.settle();

  assert.deepEqual(acknowledged, ["probe-1"]);
  assert.deepEqual(released, ["probe-2", "probe-1"]);
  assert.deepEqual(observations, [
    { requestId: "probe-1", timeoutMs: 5_000 },
    { requestId: "probe-2", timeoutMs: 5_000 },
    { requestId: "probe-1", timeoutMs: 54_340 },
  ]);
});

test("fails closed outside the owned query.page terminal contract", async () => {
  const acknowledgements = [];
  const releases = [];
  const makeWindow = terminal => new SidecarRecoveryReadWindow({
    deadlineAt: 60_000,
    now: () => 0,
    observeTerminal: async () => terminal,
    releaseRequest: async requestId => releases.push(requestId),
    acknowledge: async response => acknowledgements.push(response),
  });

  const wrongOperation = makeWindow(null);
  assert.throws(
    () => wrongOperation.own(null),
    SidecarRecoveryContractError,
  );
  wrongOperation.own("probe-1");
  assert.throws(
    () => wrongOperation.own("probe-1"),
    SidecarRecoveryContractError,
  );
  await wrongOperation.close();

  const invalidTerminals = [
    {
      type: "operation.failed",
      requestId: "probe-other",
      payload: { code: "BACKEND_UNAVAILABLE" },
    },
    {
      type: "operation.failed",
      requestId: null,
      payload: { code: "BACKEND_UNAVAILABLE" },
    },
    {
      type: "operation.failed",
      requestId: "probe-1",
      payload: { code: "PRODUCT_DATA_FAILED" },
    },
    {
      type: "workspace.v2.response",
      requestId: "probe-1",
      payload: { code: "BACKEND_UNAVAILABLE" },
    },
  ];
  for (const terminal of invalidTerminals) {
    const window = makeWindow(terminal);
    window.own("probe-1");
    await assert.rejects(window.observe("probe-1"), SidecarRecoveryContractError);
  }
  assert.deepEqual(acknowledgements, []);
  assert.deepEqual(releases, Array(5).fill("probe-1"));
});

test("fails closed when an owned failure cannot be acknowledged", async () => {
  const window = new SidecarRecoveryReadWindow({
    deadlineAt: 60_000,
    now: () => 0,
    observeTerminal: async requestId => ({
      type: "operation.failed",
      requestId,
      payload: { code: "BACKEND_UNAVAILABLE" },
    }),
    releaseRequest: async () => {},
    acknowledge: async () => {
      throw new Error("diagnostics acknowledgement failed");
    },
  });
  window.own("probe-1");

  await assert.rejects(window.settle(), SidecarRecoveryContractError);
});

test("fails closed when an owned recovery request has no terminal by the deadline", async () => {
  let now = 0;
  const observationBudgets = [];
  const acknowledged = [];
  const window = new SidecarRecoveryReadWindow({
    deadlineAt: 60_000,
    now: () => now,
    observeTerminal: async (_requestId, timeoutMs) => {
      observationBudgets.push(timeoutMs);
      now += timeoutMs;
      return null;
    },
    releaseRequest: async () => {},
    acknowledge: async response => acknowledged.push(response),
  });
  window.own("probe-1");

  assert.equal(await window.observe("probe-1"), null);
  await assert.rejects(window.settle(), SidecarRecoveryContractError);

  assert.deepEqual(observationBudgets, [5_000, 55_000]);
  assert.deepEqual(acknowledged, []);
});

test("bounds each observation by the live absolute recovery budget", async () => {
  let now = 59_000;
  const budgets = [];
  const window = new SidecarRecoveryReadWindow({
    deadlineAt: 60_000,
    now: () => now,
    observeTerminal: async (requestId, timeoutMs) => {
      budgets.push(timeoutMs);
      return { type: "query.page", requestId, payload: { rows: [] } };
    },
    releaseRequest: async () => {},
    acknowledge: async () => {},
  });
  window.own("probe-1");

  await window.observe("probe-1");

  assert.deepEqual(budgets, [1_000]);
});

test("does not accept or acknowledge a terminal returned after the deadline", async () => {
  let now = 59_000;
  const acknowledged = [];
  const released = [];
  const window = new SidecarRecoveryReadWindow({
    deadlineAt: 60_000,
    now: () => now,
    observeTerminal: async requestId => {
      now = 60_001;
      return {
        type: "operation.failed",
        requestId,
        payload: { code: "BACKEND_UNAVAILABLE" },
      };
    },
    releaseRequest: async requestId => released.push(requestId),
    acknowledge: async response => acknowledged.push(response.requestId),
  });
  window.own("probe-1");

  await assert.rejects(window.observe("probe-1"), SidecarRecoveryContractError);
  assert.deepEqual(released, ["probe-1"]);
  await window.close();

  assert.deepEqual(acknowledged, []);
  assert.deepEqual(released, ["probe-1"]);
});

test("does not acknowledge a cached failure after the absolute deadline", async () => {
  let now = 0;
  const acknowledged = [];
  const window = new SidecarRecoveryReadWindow({
    deadlineAt: 60_000,
    now: () => now,
    observeTerminal: async requestId => ({
      type: "operation.failed",
      requestId,
      payload: { code: "BACKEND_UNAVAILABLE" },
    }),
    releaseRequest: async () => {},
    acknowledge: async response => acknowledged.push(response.requestId),
  });
  window.own("probe-1");
  await window.observe("probe-1");
  now = 60_000;

  await assert.rejects(window.settle(), SidecarRecoveryContractError);

  assert.deepEqual(acknowledged, []);
});

test("settle is single-shot for concurrent and sequential callers", async () => {
  const acknowledged = [];
  const window = new SidecarRecoveryReadWindow({
    deadlineAt: 60_000,
    now: () => 0,
    observeTerminal: async requestId => ({
      type: "operation.failed",
      requestId,
      payload: { code: "BACKEND_UNAVAILABLE" },
    }),
    releaseRequest: async () => {},
    acknowledge: async response => acknowledged.push(response.requestId),
  });
  window.own("probe-1");

  const first = window.settle();
  const concurrent = window.settle();
  assert.strictEqual(concurrent, first);
  await Promise.all([first, concurrent]);
  assert.strictEqual(window.settle(), first);

  assert.deepEqual(acknowledged, ["probe-1"]);
});

test("close is idempotent and releases requests that never reached a terminal", async () => {
  const released = [];
  const window = new SidecarRecoveryReadWindow({
    deadlineAt: 60_000,
    now: () => 0,
    observeTerminal: async () => null,
    releaseRequest: async requestId => released.push(requestId),
    acknowledge: async () => {},
  });
  window.own("probe-1");
  assert.equal(await window.observe("probe-1"), null);

  const first = window.close();
  assert.strictEqual(window.close(), first);
  await first;
  assert.strictEqual(window.close(), first);

  assert.deepEqual(released, ["probe-1"]);
});

test("checks the deadline again after acknowledgement returns", async () => {
  let now = 0;
  const acknowledged = [];
  const window = new SidecarRecoveryReadWindow({
    deadlineAt: 60_000,
    now: () => now,
    observeTerminal: async requestId => ({
      type: "operation.failed",
      requestId,
      payload: { code: "BACKEND_UNAVAILABLE" },
    }),
    releaseRequest: async () => {},
    acknowledge: async response => {
      acknowledged.push(response.requestId);
      now = 60_001;
    },
  });
  window.own("probe-1");

  await assert.rejects(window.settle(), SidecarRecoveryContractError);
  assert.deepEqual(acknowledged, ["probe-1"]);
});

test("close releases ownership after an ordinary observation exception", async () => {
  const released = [];
  const window = new SidecarRecoveryReadWindow({
    deadlineAt: 60_000,
    now: () => 0,
    observeTerminal: async () => {
      throw new Error("page evaluate failed");
    },
    releaseRequest: async requestId => released.push(requestId),
    acknowledge: async () => {},
  });
  window.own("probe-1");

  await assert.rejects(window.observe("probe-1"), SidecarRecoveryContractError);
  await window.close();

  assert.deepEqual(released, ["probe-1"]);
});

test("close aggregates every release failure", async () => {
  const window = new SidecarRecoveryReadWindow({
    deadlineAt: 60_000,
    now: () => 0,
    observeTerminal: async () => null,
    releaseRequest: async requestId => {
      throw new Error(`release failed: ${requestId}`);
    },
    acknowledge: async () => {},
  });
  window.own("probe-1");
  window.own("probe-2");

  const error = await window.close().catch(reason => reason);

  assert.ok(error instanceof SidecarRecoveryContractError);
  assert.ok(error.cause instanceof AggregateError);
  assert.equal(error.cause.errors.length, 2);
});

test("closed window rejects observe and settle with one stable contract error", async () => {
  const window = new SidecarRecoveryReadWindow({
    deadlineAt: 60_000,
    now: () => 0,
    observeTerminal: async () => null,
    releaseRequest: async () => {},
    acknowledge: async () => {},
  });
  window.own("probe-1");
  await window.close();

  const observeError = await window.observe("probe-1").catch(error => error);
  const firstSettleError = await window.settle().catch(error => error);
  const secondSettleError = await window.settle().catch(error => error);

  assert.ok(observeError instanceof SidecarRecoveryContractError);
  assert.strictEqual(firstSettleError, observeError);
  assert.strictEqual(secondSettleError, observeError);
});

test("close cancels a settling observation and no late terminal can acknowledge", async () => {
  const observation = deferred();
  const acknowledged = [];
  const released = [];
  const window = new SidecarRecoveryReadWindow({
    deadlineAt: 60_000,
    now: () => 0,
    observeTerminal: async () => observation.promise,
    releaseRequest: async requestId => released.push(requestId),
    acknowledge: async response => acknowledged.push(response.requestId),
  });
  window.own("probe-1");
  let settleError = null;
  const settling = window.settle().catch(error => {
    settleError = error;
    return error;
  });
  await Promise.resolve();

  await window.close();
  await Promise.resolve();

  assert.ok(settleError instanceof SidecarRecoveryContractError);
  assert.deepEqual(released, ["probe-1"]);
  observation.resolve({
    type: "operation.failed",
    requestId: "probe-1",
    payload: { code: "BACKEND_UNAVAILABLE" },
  });
  await settling;
  await Promise.resolve();
  assert.deepEqual(acknowledged, []);
  assert.deepEqual(released, ["probe-1"]);
});

test("close waits for an in-flight acknowledgement before resolving", async () => {
  const acknowledgementStarted = deferred();
  const acknowledgementMayFinish = deferred();
  const events = [];
  const released = [];
  const window = new SidecarRecoveryReadWindow({
    deadlineAt: 60_000,
    now: () => 0,
    observeTerminal: async requestId => ({
      type: "operation.failed",
      requestId,
      payload: { code: "BACKEND_UNAVAILABLE" },
    }),
    releaseRequest: async requestId => {
      released.push(requestId);
      events.push("released");
    },
    acknowledge: async () => {
      events.push("ack-started");
      acknowledgementStarted.resolve();
      await acknowledgementMayFinish.promise;
      events.push("ack-finished");
    },
  });
  window.own("probe-1");
  const settling = window.settle().catch(error => {
    events.push("settle-closed");
    return error;
  });
  await acknowledgementStarted.promise;

  let closeResolved = false;
  const closing = window.close().then(() => {
    closeResolved = true;
    events.push("close-resolved");
  });
  await Promise.resolve();
  assert.equal(closeResolved, false);

  acknowledgementMayFinish.resolve();
  const settleError = await settling;
  await closing;

  assert.ok(settleError instanceof SidecarRecoveryContractError);
  assert.deepEqual(released, ["probe-1"]);
  assert.deepEqual(events, [
    "released",
    "ack-started",
    "ack-finished",
    "settle-closed",
    "close-resolved",
  ]);
});

test("close reuses a failed release attempt instead of retrying it", async () => {
  let releaseAttempts = 0;
  const window = new SidecarRecoveryReadWindow({
    deadlineAt: 60_000,
    now: () => 0,
    observeTerminal: async requestId => ({
      type: "query.page",
      requestId,
      payload: { rows: [] },
    }),
    releaseRequest: async () => {
      releaseAttempts += 1;
      throw new Error("release failed");
    },
    acknowledge: async () => {},
  });
  window.own("probe-1");

  await assert.rejects(window.observe("probe-1"), SidecarRecoveryContractError);
  await assert.rejects(window.close(), SidecarRecoveryContractError);

  assert.equal(releaseAttempts, 1);
});
