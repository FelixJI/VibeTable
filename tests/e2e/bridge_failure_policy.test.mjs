import assert from "node:assert/strict";
import test from "node:test";

import {
  acknowledgeExpectedSidecarRecoveryFailure,
  isExpectedSidecarRecoveryFailure,
} from "./bridge_failure_policy.mjs";

const expected = {
  type: "operation.failed",
  requestId: "e2e-query",
  payload: { code: "BACKEND_UNAVAILABLE" },
};

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

test("propagates acknowledgement failures", async () => {
  await assert.rejects(
    acknowledgeExpectedSidecarRecoveryFailure(expected, async () => {
      throw new Error("diagnostics acknowledgement failed");
    }),
    /diagnostics acknowledgement failed/,
  );
});
