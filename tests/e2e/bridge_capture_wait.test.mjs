import assert from "node:assert/strict";
import test from "node:test";

import { waitForCapturedBridgeMessage } from "./bridge_capture_wait.mjs";

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
  assert.equal(calls[0][2], undefined);
  assert.deepEqual(calls[0][3], { timeout: 60_000 });
  assert.equal(calls[1][0], "evaluate");
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

test("keeps the stable timeout error without polling the page", async () => {
  const timeout = Object.assign(new Error("playwright details"), { name: "TimeoutError" });
  const { page, calls } = makePage(null, {
    async waitForFunction() {
      calls.push(["waitForFunction"]);
      throw timeout;
    },
  });

  await assert.rejects(
    waitForCapturedBridgeMessage(page, 123),
    /captured bridge response timed out/,
  );
  assert.deepEqual(calls, [["waitForFunction"]]);
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
