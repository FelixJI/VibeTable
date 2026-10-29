import test from "node:test";
import assert from "node:assert/strict";
import { assertAppliedTransaction, requestTransactionNativeInput } from "./plugin_transactions.mjs";

function confirmationHarness(nativeFailure = false) {
  const steps = [];
  let requested = false, ready = false;
  const button = { async click() { requested = true; steps.push("request"); } };
  const page = { locator(selector) {
    assert.equal(selector, ".host-confirmation");
    return { async waitFor() {
      assert.equal(requested, true);
      // Host reads current model/permissions asynchronously before publishing confirmation.
      await Promise.resolve();
      ready = true; steps.push("host-confirmation");
    } };
  } };
  const nativePluginInput = async () => {
    steps.push("native-input");
    return ready && !nativeFailure ? { status: "completed" }
      : { status: "failed", error: "expected one current owned native plugin window" };
  };
  return { steps, run: () => requestTransactionNativeInput(page, button, nativePluginInput) };
}

test("rules confirmation waits for Host publication before its one owned native input", async () => {
  const harness = confirmationHarness();
  assert.deepEqual(await harness.run(), { status: "completed" });
  assert.deepEqual(harness.steps, ["request", "host-confirmation", "native-input"]);
});

test("a failed owned native input is returned unchanged without retry", async () => {
  const harness = confirmationHarness(true);
  assert.deepEqual(await harness.run(), { status: "failed", error: "expected one current owned native plugin window" });
  assert.equal(harness.steps.filter(step => step === "native-input").length, 1);
});

test("an unpublished confirmation cannot send native input", async () => {
  let inputs = 0;
  const page = { locator: () => ({ waitFor: async () => { throw new Error("confirmation not published"); } }) };
  await assert.rejects(requestTransactionNativeInput(page, { click: async () => {} },
    async () => { inputs++; return { status: "completed" }; }), /confirmation not published/);
  assert.equal(inputs, 0);
});

test("three-table acceptance rejects partial, foreign and mismapped successful-looking receipts", () => {
  const preview = { workspaceId: "w", installationId: "i", requestId: "r", model: { bindings: {} }, newIds: {} };
  const rows = [];
  for (const entity of ["heads", "lines", "allocations"]) {
    preview.model.bindings[entity] = { tableId: `table-${entity}` };
    preview.newIds[entity] = { new: `row-${entity}` };
    rows.push({ tableId: `table-${entity}`, recordId: `row-${entity}`, operation: "insert" });
  }
  const value = { ...preview, status: "applied", receipt: { status: "applied", changeSetId: "c", rows } };
  assert.doesNotThrow(() => assertAppliedTransaction(value, preview));
  for (const changed of [
    { ...value, workspaceId: "other" },
    { ...value, status: "unknown" },
    { ...value, receipt: { ...value.receipt, rows: rows.slice(0, 2) } },
    { ...value, receipt: { ...value.receipt, rows: [rows[0], rows[1], { ...rows[2], recordId: "wrong" }] } },
  ]) assert.throws(() => assertAppliedTransaction(changed, preview));
});
