import assert from "node:assert/strict";
import test from "node:test";
import { sdk } from "./load-sdk.mjs";

test("display helpers keep 0/false/empty-string distinct from null and undefined", () => {
  assert.deepEqual(sdk.describeCellValue(undefined), { kind: "missing", text: "", fresh: null });
  assert.deepEqual(sdk.describeCellValue(null), { kind: "null", text: "", fresh: null });
  assert.deepEqual(sdk.describeCellValue(0), { kind: "number", text: "0", fresh: null });
  assert.deepEqual(sdk.describeCellValue(false), { kind: "boolean", text: "false", fresh: null });
  assert.deepEqual(sdk.describeCellValue(""), { kind: "string", text: "", fresh: null });
  assert.equal(sdk.isMissingCell(undefined), true);
  assert.equal(sdk.isMissingCell(null), true);
  assert.equal(sdk.isMissingCell(0), false);
  assert.equal(sdk.isMissingCell(false), false);
  assert.equal(sdk.isMissingCell(""), false);
});

test("display helpers serialize relations, arrays and plain objects without expanding them", () => {
  assert.deepEqual(sdk.describeCellValue(["rel_1", "rel_2"]), { kind: "array", text: '["rel_1","rel_2"]', fresh: null });
  assert.equal(sdk.formatCellValue({ nested: { value: 1 } }), '{"nested":{"value":1}}');
  // A JSON field that merely contains the three computed keys stays serialized as-is.
  const accidental = { state: "ready", value: 1, fresh: true };
  assert.equal(sdk.describeCellValue(accidental).kind, "object");
});

test("display helpers only unpack computed cells when the caller declares the field computed", () => {
  const fresh = { state: "ready", value: 42, fresh: true };
  const stale = { state: "pending", value: null, fresh: false };
  assert.deepEqual(sdk.describeCellValue(fresh, { computed: true }), { kind: "computed", text: "42", fresh: true });
  assert.deepEqual(sdk.describeCellValue(stale, { computed: true }), { kind: "computed", text: "", fresh: false });
  assert.equal(sdk.formatCellValue(fresh, { computed: true }), "42");
  assert.equal(sdk.formatCellValue(stale, { computed: true }), "");
});

test("data.query accepts an optional abort signal that cancels one in-flight page", async () => {
  const listeners = new Set();
  const calls = [];
  const transport = {
    postMessage: message => calls.push(message),
    addEventListener: (_type, listener) => listeners.add(listener),
    removeEventListener: (_type, listener) => listeners.delete(listener),
  };
  const client = sdk.createSurfaceClient("surface-query", transport);
  const abort = new AbortController();
  const request = client.data.query({ contract: "vibetable.plugin-query.v2", collection: "orders", fields: ["id"], pageSize: 1 }, abort.signal);
  const wire = calls[0];
  assert.equal(wire.method, "data.query");
  abort.abort();
  assert.equal(calls[1].method, "request.cancel");
  assert.deepEqual(calls[1].params, { requestId: wire.requestId });
  for (const listener of listeners) listener({ data: { contract: sdk.SURFACE_API_CONTRACT, requestId: wire.requestId, error: { code: "plugin_cancel_requested", message: "cancelled" } } });
  await assert.rejects(request, error => error.code === "plugin_cancel_requested");
  assert.equal(calls.filter(item => item.method === "data.query").length, 1);
  client.dispose();
});
