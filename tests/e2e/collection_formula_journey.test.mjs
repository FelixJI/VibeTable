import assert from "node:assert/strict";
import test from "node:test";
import { openCollectionLookupEditor, runCollectionFormulaJourney } from "./collection_formula_journey.mjs";

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


test("S37 selects computed types through the filterable picker before entering the editor", { timeout: 1_000 }, async () => {
  const selected = [];
  const stop = new Error("stopped after filtered type selection");
  const locator = { click: async () => {}, waitFor: async () => {}, fill: async () => {}, locator() { return this; } };
  const page = { getByTestId: () => locator };
  const noop = async () => {};
  await assert.rejects(runCollectionFormulaJourney(page, { check() {} }, {}, {
    waitForShell: noop,
    createSimpleTable: async (_page, name) => ({ tableId: name, field: { physicalName: "title" } }),
    createV2Field: async (_page, _table, name) => ({ physicalName: name }),
    applyProductMutation: async () => ({ payload: { status: "applied" } }),
    selectTable: noop, chooseToolbarMore: noop, waitForVisibleRowCount: noop,
    selectNValue: async (_page, id, value) => { selected.push({ id, value }); throw stop; },
    selectVisibleNOption: async () => { throw new Error("unfiltered virtual list selection"); },
  }), error => error === stop);
  assert.deepEqual(selected, [{ id: "field-logical-type", value: "formula" }]);
});
