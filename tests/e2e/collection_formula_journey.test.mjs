import assert from "node:assert/strict";
import test from "node:test";
import { openCollectionLookupEditor } from "./collection_formula_journey.mjs";

test("S37 waits for the delayed Lookup catalog before clicking its entry", { timeout: 1_000 }, async () => {
  const events = [];
  let completeCatalog;
  const catalog = new Promise(resolve => { completeCatalog = resolve; });
  const page = {
    getByTestId(id) {
      assert.equal(id, "lookup-editor-entry");
      return { click: async () => { events.push("entry"); } };
    },
  };
  const pending = openCollectionLookupEditor(page, async () => {
    events.push("drawer");
  }, {
    beginBridgeMessageCapture: async (_page, types) => {
      assert.deepEqual(types, ["schema.describe", "operation.failed"]);
      events.push("capture");
    },
    waitForCapturedBridgeMessage: async (_page, timeout) => {
      assert.equal(timeout, 30_000);
      events.push("loading");
      return catalog;
    },
  });
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(events, ["capture", "drawer", "loading"]);
  completeCatalog({ type: "schema.describe", payload: {} });
  await pending;
  assert.deepEqual(events, ["capture", "drawer", "loading", "entry"]);
});

test("S37 catalog failure or timeout prevents an entry click", { timeout: 1_000 }, async () => {
  for (const outcome of [
    { type: "operation.failed", payload: { code: "CATALOG_FAILED" } },
    { type: "schema.describe", payload: { error: "catalog invalid" } },
    new Error("catalog timed out"),
  ]) {
    let clicks = 0;
    const page = { getByTestId: () => ({ click: async () => { clicks += 1; } }) };
    await assert.rejects(openCollectionLookupEditor(page, async () => {}, {
      beginBridgeMessageCapture: async () => {},
      waitForCapturedBridgeMessage: async () => {
        if (outcome instanceof Error) throw outcome;
        return outcome;
      },
    }), /catalog/);
    assert.equal(clicks, 0);
  }
});
