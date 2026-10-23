import assert from "node:assert/strict";
import fs from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

// VM real-source seam (scenario08_stale_conflict.test.mjs pattern): runs the
// REAL scenario10 retired-paste wait/assertion from
// webview_product_scenarios.mjs against the verified current producer chain:
// Go paste_token_unknown ("paste token not found") -> productrpc paste_error
// data -> Host MapPasteApplyRejection Validation/notSubmitted=true ->
// correlated table.editRejected (kind mutation_validation).
const source = await fs.readFile(new URL("./webview_product_scenarios.mjs", import.meta.url), "utf8");
const rawBridgeRequestSource = source.slice(
  source.indexOf("async function rawBridgeRequest("),
  source.indexOf("async function rawWorkspaceV2Request("),
);
const retiredPasteSource = source.slice(
  source.indexOf("// A reopened workspace has a new sidecar owner; an old unconsumed plan"),
  source.indexOf("const writableSessionObserved"),
);
assert.ok(rawBridgeRequestSource.includes("async function rawBridgeRequest("));
assert.ok(retiredPasteSource.includes("const retiredPaste = await rawBridgeRequest("));

const WORKSPACE_ID = "11111111-2222-4333-8444-555555555555";

function structuredEditRejection(requestId) {
  return {
    type: "table.editRejected",
    requestId,
    payload: {
      kind: "mutation_validation",
      operation: "applyPaste",
      code: "paste_token_unknown",
      message: "paste token not found",
      currentRow: null,
      conflictingRowKeys: null,
      fieldErrors: null,
      notSubmitted: true,
    },
  };
}

// The legacy operation.failed terminal the old wait/assertion accepted.
function legacyOperationFailed(requestId) {
  return {
    type: "operation.failed",
    requestId,
    payload: { message: "paste token not found", code: "PASTE_APPLY_FAILED", operation: null },
  };
}

function wronglyApplied(requestId, collection) {
  return {
    type: "table.pasteApplied",
    requestId,
    payload: {
      collection,
      outcome: "committed",
      createdRowKeys: [],
      updatedRowKeys: ["row-retired"],
      skippedRowKeys: [],
      conflicts: [],
      requestId: "paste-plan-from-retired-session",
    },
  };
}

// A minimal in-page world for the REAL rawBridgeRequest evaluate body. The
// clamped timer keeps an unrecognized terminal (the old wait) failing within
// milliseconds instead of the real 20s bridge timeout.
function createHarness(replyFor) {
  const posted = [];
  const reserved = [];
  const listeners = new Set();
  let uuidCounter = 0;
  const window = {
    __vibetableE2EWorkspaceWirePort: {
      reserve(operationId) {
        reserved.push(operationId);
        return {
          scope: "workspace",
          workspaceId: WORKSPACE_ID,
          sessionEpoch: 2,
          operationId,
          sequence: 900 + reserved.length,
        };
      },
    },
    chrome: {
      webview: {
        addEventListener(type, listener) {
          assert.equal(type, "message");
          listeners.add(listener);
        },
        removeEventListener(type, listener) {
          assert.equal(type, "message");
          listeners.delete(listener);
        },
        postMessage(envelope) {
          posted.push(envelope);
          const reply = replyFor(envelope);
          if (reply) for (const listener of [...listeners]) listener({ data: reply });
        },
      },
    },
  };
  const page = {
    async evaluate(fn, arg) {
      return vm.runInNewContext(`(${fn.toString()})(argument)`, {
        window,
        argument: arg,
        crypto: { randomUUID: () => `retired-${++uuidCounter}` },
        setTimeout: (handler, ms) => setTimeout(handler, Math.min(ms, 25)),
        clearTimeout,
      });
    },
  };
  return { page, window, posted, reserved, listeners };
}

// The retired-paste slice may end with the old expected-failure
// acknowledgement; record calls so the legacy path completes its own old
// assertion and the red comes only from the current expectation.
function loadScenarioTail(acknowledgeCalls) {
  const acknowledgeExpectedBridgeFailure = async (_page, response) => {
    acknowledgeCalls.push(response?.requestId ?? null);
  };
  return vm.runInNewContext(
    `${rawBridgeRequestSource}\n(async (page, recorder, tableId, afterPythonPaste) => {\n${retiredPasteSource}\n})`,
    { acknowledgeExpectedBridgeFailure },
  );
}

test("scene10 retires a reopened session's paste plan on the structured edit rejection", async () => {
  const harness = createHarness((envelope) => structuredEditRejection(envelope.requestId));
  const checks = [];
  const acknowledgeCalls = [];
  const scenarioTail = loadScenarioTail(acknowledgeCalls);

  await scenarioTail(
    harness.page,
    {
      check(name, passed) {
        checks.push({ name, passed });
        assert.equal(passed, true, `${name} must accept the real structured rejection`);
      },
    },
    "tbl_retired_plan",
    { token: { token: "pst1.retired-session-plan" } },
  );

  assert.deepEqual(checks.map((check) => check.name), [
    "reopening retires the previous sidecar's unconsumed paste plan",
  ]);
  assert.deepEqual(acknowledgeCalls, []);
  assert.equal(harness.posted.length, 1);
  assert.equal(harness.posted[0].type, "table.applyPasteRequested");
  assert.equal(harness.posted[0].payload.collection, "tbl_retired_plan");
  assert.equal(harness.posted[0].payload.token, "pst1.retired-session-plan");
  assert.equal(harness.posted[0].payload.idempotencyKey, "paste-plan-from-retired-session");
  assert.equal(harness.posted[0].scope.sequence, 901);
  assert.equal(harness.listeners.size, 0, "the correlated wait must release its listener");
});

test("scene10 fails the retirement check when the retired plan is wrongly applied", async () => {
  const harness = createHarness((envelope) => wronglyApplied(envelope.requestId, "tbl_retired_plan"));
  const checks = [];
  const scenarioTail = loadScenarioTail([]);

  await scenarioTail(
    harness.page,
    { check(name, passed) { checks.push({ name, passed }); } },
    "tbl_retired_plan",
    { token: { token: "pst1.retired-session-plan" } },
  );

  assert.equal(checks.length, 1);
  assert.equal(checks[0].passed, false, "a committed retired plan must not pass the retirement check");
});

test("scene10 fails the retirement check on the legacy operation.failed protocol", async () => {
  const harness = createHarness((envelope) => legacyOperationFailed(envelope.requestId));
  const checks = [];
  const acknowledgeCalls = [];
  const scenarioTail = loadScenarioTail(acknowledgeCalls);

  await scenarioTail(
    harness.page,
    { check(name, passed) { checks.push({ name, passed }); } },
    "tbl_retired_plan",
    { token: { token: "pst1.retired-session-plan" } },
  );

  assert.equal(checks.length, 1);
  assert.equal(checks[0].passed, false, "the old operation.failed wait must no longer satisfy the check");
  assert.deepEqual(acknowledgeCalls, [], "the structured rejection path has no failure to acknowledge");
});
