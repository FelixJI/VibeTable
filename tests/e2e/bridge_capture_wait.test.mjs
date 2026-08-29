import assert from "node:assert/strict";
import test from "node:test";

import {
  captureCompletedInPage,
  readCaptureTimeoutEvidenceInPage,
  waitForCapturedBridgeMessage,
} from "./bridge_capture_wait.mjs";

function makePage(captured, overrides = {}) {
  const calls = [];
  return {
    calls,
    page: {
      async waitForFunction(predicate, argument, options) {
        calls.push(["waitForFunction", predicate, argument, options]);
      },
      async evaluate(reader) {
        calls.push(["evaluate", reader]);
        return captured;
      },
      ...overrides,
    },
  };
}

test("waits once in the renderer and reads the captured message once", async () => {
  const message = { type: "workspace.v2.response", payload: { ok: true } };
  const { page, calls } = makePage({ message, error: null });

  const result = await waitForCapturedBridgeMessage(page, 60_000);

  assert.equal(result, message);
  assert.equal(calls.length, 2);
  assert.equal(calls[0][0], "waitForFunction");
  assert.equal(calls[0][1], captureCompletedInPage);
  assert.equal(calls[0][2], undefined);
  assert.deepEqual(calls[0][3], { polling: 50, timeout: 60_000 });
  assert.equal(calls[1][0], "evaluate");
});

test("renderer wait completes for either a message or an error", () => {
  globalThis.window = { __vibetableE2EBridgeCapture: { message: null, error: null } };
  try {
    assert.equal(captureCompletedInPage(), false);
    window.__vibetableE2EBridgeCapture.message = { type: "database.opened" };
    assert.equal(captureCompletedInPage(), true);
    window.__vibetableE2EBridgeCapture = {
      message: null,
      error: { method: "workspace.switch", code: "busy", message: "busy" },
    };
    assert.equal(captureCompletedInPage(), true);
  } finally {
    delete globalThis.window;
  }
});

test("surfaces a captured workspace failure", async () => {
  const { page } = makePage({
    message: null,
    error: {
      method: "workspace.switch",
      code: "workspace_busy",
      message: "workspace is busy",
    },
  });

  await assert.rejects(
    waitForCapturedBridgeMessage(page),
    /captured workspace\.switch failure: workspace_busy: workspace is busy/,
  );
});

test("adds one bounded renderer evidence snapshot to the stable timeout", async () => {
  const timeout = Object.assign(new Error("playwright details"), { name: "TimeoutError" });
  const evidence = {
    expectedWorkspaceId: "workspace-b",
    minimumEpoch: 7,
    expectedLifecycleMethods: ["workspace.switch"],
    bootstrapSession: null,
    lifecycleSuccess: {
      requestId: "request-8",
      method: "workspace.switch",
      result: { workspaceId: "workspace-b", sessionEpoch: 8, state: "openedWritable" },
    },
    unexpectedBootstraps: [],
    observedWorkspaceSession: { workspaceId: "workspace-a", sessionEpoch: 7 },
    lifecycleRequests: [{ requestId: "request-8", requestType: "workspace.switch" }],
  };
  const { page, calls } = makePage(evidence, {
    async waitForFunction() {
      calls.push(["waitForFunction"]);
      throw timeout;
    },
  });

  await assert.rejects(
    waitForCapturedBridgeMessage(page, 123),
    (error) => error.message === `captured bridge response timed out: ${JSON.stringify(evidence)}`,
  );
  assert.equal(calls.length, 2);
  assert.deepEqual(calls[0], ["waitForFunction"]);
  assert.equal(calls[1][0], "evaluate");
  assert.equal(calls[1][1], readCaptureTimeoutEvidenceInPage);
});

test("timeout evidence exposes only bounded correlation state", () => {
  globalThis.window = {
    __vibetableE2EBridgeCapture: {
      expectedWorkspaceId: "workspace-b",
      minimumEpoch: 7,
      expectedLifecycleMethods: ["workspace.switch"],
      baselineRequestIds: ["old"],
      bootstrap: {
        payload: {
          session: {
            workspaceId: "workspace-b",
            sessionEpoch: 8,
            state: "openedWritable",
            writable: true,
            selectedRoot: "must-not-leak",
          },
        },
      },
      lifecycleSuccess: {
        requestId: "new",
        method: "workspace.switch",
        result: {
          workspaceId: "workspace-b",
          sessionEpoch: 8,
          state: "openedWritable",
          secret: "must-not-leak",
        },
      },
      unexpectedBootstraps: Array.from({ length: 8 }, (_, index) => ({
        workspaceId: `unexpected-${index}`,
        sessionEpoch: index,
      })),
    },
    __vibetableE2EBridgeDiagnostics: {
      workspaceSession: { workspaceId: "workspace-b", sessionEpoch: 8 },
      requests: [
        { requestId: "old", requestType: "workspace.switch", payloadShape: "must-not-leak" },
        { requestId: "ignored", requestType: "workspace.open" },
        { requestId: "new", requestType: "workspace.switch", payloadShape: "must-not-leak" },
      ],
    },
  };
  try {
    assert.deepEqual(readCaptureTimeoutEvidenceInPage(), {
      expectedWorkspaceId: "workspace-b",
      minimumEpoch: 7,
      expectedLifecycleMethods: ["workspace.switch"],
      bootstrapSession: {
        workspaceId: "workspace-b",
        sessionEpoch: 8,
        state: "openedWritable",
        writable: true,
      },
      lifecycleSuccess: {
        requestId: "new",
        method: "workspace.switch",
        result: { workspaceId: "workspace-b", sessionEpoch: 8, state: "openedWritable" },
      },
      unexpectedBootstraps: [
        { workspaceId: "unexpected-3", sessionEpoch: 3 },
        { workspaceId: "unexpected-4", sessionEpoch: 4 },
        { workspaceId: "unexpected-5", sessionEpoch: 5 },
        { workspaceId: "unexpected-6", sessionEpoch: 6 },
        { workspaceId: "unexpected-7", sessionEpoch: 7 },
      ],
      observedWorkspaceSession: { workspaceId: "workspace-b", sessionEpoch: 8 },
      lifecycleRequests: [{ requestId: "new", requestType: "workspace.switch" }],
    });
  } finally {
    delete globalThis.window;
  }
});

test("does not rewrite non-timeout Playwright failures", async () => {
  const closed = new Error("page closed");
  const { page } = makePage(null, {
    async waitForFunction() {
      throw closed;
    },
  });

  await assert.rejects(waitForCapturedBridgeMessage(page), (error) => error === closed);
});
