import { observeTransport } from "../fixtures/plugins/capability-services/src/transport.ts";
import test from "node:test";
import assert from "node:assert/strict";
import { requireReceipt, requireScopedInvalidations, requireSingleColumnExport } from "./plugin_capability_services.mjs";
test("a failed or missing native/API receipt cannot become acceptance", () => {
  for (const value of [null, {}, { ok: false, code: "plugin_commit_unknown" }, { ok: false, code: "plugin_cancel_requested" }]) assert.throws(() => requireReceipt(value, "mutation"));
  assert.equal(requireReceipt({ ok: true, value: null }, "picker"), null);
});
test("scoped invalidations reject wrong table, payload disclosure and listener duplication", () => {
  requireScopedInvalidations([{ type: "invalidated", collection: "tbl_a", sequence: 1 }, { type: "invalidated", collection: "tbl_a", sequence: 2 }], "tbl_a");
  for (const events of [[], [{ type: "invalidated", collection: "tbl_b", sequence: 1 }], [{ type: "invalidated", collection: "tbl_a", sequence: 1, rows: [] }], [{ type: "invalidated", collection: "tbl_a", sequence: 1 }, { type: "invalidated", collection: "tbl_a", sequence: 1 }]]) assert.throws(() => requireScopedInvalidations(events, "tbl_a"));
});

test("export must contain exactly the explicitly authorized column and actual Go values", () => {
  const fieldId = "fld_capabilitytext0";
  requireSingleColumnExport(`\uFEFF${fieldId}\r\n"import-one"\r\nimport-two\r\n`, fieldId, ["import-two", "import-one"]);
  for (const csv of [`${fieldId},Secret\none,private\n`, `${fieldId}\nimport-one\n`, `${fieldId}\nimport-one\nimport-two\nprivate\n`, "Text\nimport-one\nimport-two\n", "f_capability_text\nimport-one\nimport-two\n", "fld_otherfield0000\nimport-one\nimport-two\n"]) assert.throws(() => requireSingleColumnExport(csv, fieldId, ["import-one", "import-two"]));
});

test("real transport accounting preserves messages and rejects a deliberately undisposed listener", () => {
  const listeners = new Set(), sent = [];
  const channel = { postMessage: message => sent.push(message), addEventListener: (_type, listener) => listeners.add(listener), removeEventListener: (_type, listener) => listeners.delete(listener) };
  const observed = observeTransport(channel);
  const baseline = () => {}, event = { data: "original" };
  const received = [];
  observed.transport.addEventListener("message", baseline);
  for (let i = 0; i < 24; i++) {
    const receive = value => received.push(value);
    observed.transport.addEventListener("message", receive);
    observed.requireBaseline(2);
    observed.transport.postMessage(event);
    for (const listener of listeners) listener(event);
    observed.transport.removeEventListener("message", receive);
    observed.requireBaseline(1);
  }
  assert.equal(received.length, 24);
  assert.ok(received.every(value => value === event));
  assert.ok(sent.every(value => value === event));
  const leaked = () => {};
  observed.transport.addEventListener("message", leaked);
  assert.throws(() => observed.requireBaseline(1), /listener count/);
  observed.transport.removeEventListener("message", leaked);
  observed.requireBaseline(1);
});
