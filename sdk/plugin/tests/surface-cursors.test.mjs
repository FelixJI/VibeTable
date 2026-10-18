import assert from "node:assert/strict";
import test from "node:test";
import { sdk } from "./load-sdk.mjs";
function channel() {
  const listeners = new Set(), calls = [];
  return { calls, postMessage(message) { calls.push(message); }, addEventListener(_type, listener) { listeners.add(listener); }, removeEventListener(_type, listener) { listeners.delete(listener); }, reply(request, result, error) { for (const listener of listeners) listener({ data: { contract: sdk.SURFACE_API_CONTRACT, requestId: request.requestId, ...(error ? { error } : { result }) } }); } };
}
const request = { contract: "vibetable.plugin-query.v2", collection: "articles", fields: ["id"], pageSize: 1 };
const page = cursor => ({ contract: "vibetable.plugin-query-page.v2", items: [], nextCursor: cursor, totalRows: 550, filteredRows: 550, complete: true, schemaRevision: "schema-one", dataRevision: 1, rowGuards: {} });
async function allocate(api, wire, cursor) { const pending = api.data.query(request); wire.reply(wire.calls.at(-1), page(cursor)); await pending; }

test("cursor release remains available at eight normal requests, coalesces and has eight bounded acknowledgement slots", async () => {
  const wire = channel(), api = sdk.createSurfaceClient("surface", wire);
  for (let i = 0; i < 9; i++) await allocate(api, wire, `cursor-${i}`);
  const reads = Array.from({ length: 8 }, () => api.context.read()), readWires = wire.calls.slice(-8);
  const releases = Array.from({ length: 8 }, (_, i) => api.data.releaseCursor(`cursor-${i}`));
  const releaseWires = wire.calls.slice(-8);
  try {
    assert.ok(releaseWires.every(item => item.method === "data.releaseCursor"));
    assert.equal(api.data.releaseCursor("cursor-0"), releases[0]);
    await assert.rejects(api.data.releaseCursor("cursor-8"), error => error.code === "plugin_concurrency_limit");
    const count = wire.calls.length; await api.data.releaseCursor("unknown"); assert.equal(wire.calls.length, count);
    for (const item of releaseWires) wire.reply(item, { released: true }); await Promise.all(releases);
    const retry = api.data.releaseCursor("cursor-8"); wire.reply(wire.calls.at(-1), { released: true }); await retry;
  } finally { for (const item of readWires) wire.reply(item, {}); await Promise.all(reads); api.dispose(); }
});
test("failed release retains ownership for explicit retry and never silently retries", async () => {
  const wire = channel(), api = sdk.createSurfaceClient("surface", wire);
  try {
    await allocate(api, wire, "owned");
    const first = api.data.releaseCursor("owned"); wire.reply(wire.calls.at(-1), undefined, { code: "plugin_transport_failed", message: "synthetic failure" });
    await assert.rejects(first, error => error.code === "plugin_transport_failed");
    const count = wire.calls.length; await new Promise(resolve => setImmediate(resolve)); assert.equal(wire.calls.length, count);
    const explicit = api.data.releaseCursor("owned"); assert.equal(wire.calls.length, count + 1); wire.reply(wire.calls.at(-1), { released: true }); await explicit;
    await api.data.releaseCursor("owned"); assert.equal(wire.calls.length, count + 1);
  } finally { api.dispose(); }
});
test("query response followed by abort releases its late cursor before reporting cancellation", async () => {
  const wire = channel(), api = sdk.createSurfaceClient("surface", wire), abort = new AbortController();
  try {
    const pending = api.data.query(request, abort.signal); const outcome = assert.rejects(pending, error => error.code === "plugin_cancel_requested");
    wire.reply(wire.calls.at(-1), page("late-cursor")); abort.abort(); await new Promise(resolve => setImmediate(resolve));
    const released = wire.calls.at(-1); assert.equal(released.method, "data.releaseCursor"); assert.deepEqual(released.params, { cursor: "late-cursor" });
    wire.reply(released, { released: true }); await outcome;
  } finally { api.dispose(); }
});
test("dispose before the query continuation cancels pending allocation or releases the already delivered handle", async () => {
  for (const delivered of [false, true]) {
    const wire = channel(), api = sdk.createSurfaceClient("surface", wire);
    const pending = api.data.query(request); const query = wire.calls.at(-1); const outcome = assert.rejects(pending, error => error.code === "plugin_surface_closed");
    if (delivered) wire.reply(query, page("late-cursor"));
    api.dispose(); await outcome;
    assert.ok(wire.calls.some(item => delivered ? item.method === "data.releaseCursor" && item.params.cursor === "late-cursor" : item.method === "request.cancel" && item.params.requestId === query.requestId));
    assert.equal(wire.calls.filter(item => item.method === "data.query").length, 1);
  }
});


test("unknown release receipt retains the cursor for an explicit retry", async () => {
  const wire = channel(), api = sdk.createSurfaceClient("surface", wire);
  try {
    await allocate(api, wire, "owned");
    const first = api.data.releaseCursor("owned"); wire.reply(wire.calls.at(-1), {});
    await assert.rejects(first, error => error.code === "plugin_transport_failed");
    const before = wire.calls.length; await new Promise(resolve => setImmediate(resolve)); assert.equal(wire.calls.length, before);
    const explicit = api.data.releaseCursor("owned"); assert.equal(wire.calls.length, before + 1);
    wire.reply(wire.calls.at(-1), { released: true }); await explicit;
  } finally { api.dispose(); }
});
