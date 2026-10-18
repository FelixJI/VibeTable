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

test("host-consumed query failures converge the input cursor instead of leaking the 64-handle quota", async () => {
  const wire = channel(), api = sdk.createSurfaceClient("surface", wire);
  try {
    for (let i = 0; i < 65; i++) {
      const allocating = api.data.query(request);
      wire.reply(wire.calls.at(-1), page(`cursor-${i}`));
      await allocating;
      // A Host reply from after query admission consumed the input cursor on
      // the Host side; the SDK must drop it locally without a release round
      // trip, so repeated failures never accumulate toward plugin_query_limit.
      const failing = assert.rejects(api.data.query({ ...request, cursor: `cursor-${i}` }), error => error.code === "plugin_query_failed");
      wire.reply(wire.calls.at(-1), undefined, { code: "plugin_query_failed" });
      await failing;
      assert.equal(wire.calls.filter(call => call.method === "data.releaseCursor").length, 0);
      const explicit = api.data.releaseCursor(`cursor-${i}`);
      const releaseWire = wire.calls.filter(call => call.method === "data.releaseCursor").at(-1);
      if (releaseWire) wire.reply(releaseWire, { released: true });
      await explicit;
      assert.equal(wire.calls.filter(call => call.method === "data.releaseCursor").length, 0, `cycle ${i}`);
    }
  } finally { api.dispose(); }
});

test("unknown Host ownership boundaries converge through the acknowledged release", async () => {
  for (const code of ["plugin_session_stale", "plugin_capability_invalid", "plugin_message_too_large"]) {
    const wire = channel(), api = sdk.createSurfaceClient("surface", wire);
    try {
      await allocate(api, wire, "input-cursor");
      const failing = assert.rejects(api.data.query({ ...request, cursor: "input-cursor" }), error => error.code === code, code);
      wire.reply(wire.calls.at(-1), undefined, { code });
      await failing;
      const released = wire.calls.filter(call => call.method === "data.releaseCursor");
      assert.equal(released.length, 1, code);
      assert.deepEqual(released[0].params, { cursor: "input-cursor" }, code);
      // Only the success receipt removes local ownership.
      wire.reply(released[0], { released: true });
      await new Promise(resolve => setImmediate(resolve));
      await api.data.releaseCursor("input-cursor");
      assert.equal(wire.calls.filter(call => call.method === "data.releaseCursor").length, 1, code);
    } finally { api.dispose(); }
  }
});

test("a failed convergence release keeps the cursor for explicit retry and dispose", async () => {
  for (const name of ["error receipt", "send throws"]) {
    const wire = channel(), api = sdk.createSurfaceClient("surface", wire);
    const send = wire.postMessage;
    let failRelease = name === "send throws";
    wire.postMessage = message => {
      if (message.method === "data.releaseCursor" && failRelease) throw new Error("synthetic cleanup send failure");
      send(message);
    };
    try {
      await allocate(api, wire, "input-cursor");
      const failing = assert.rejects(api.data.query({ ...request, cursor: "input-cursor" }), error => error.code === "plugin_session_stale", name);
      wire.reply(wire.calls.at(-1), undefined, { code: "plugin_session_stale" });
      await failing;
      await new Promise(resolve => setImmediate(resolve));
      if (name === "error receipt") {
        const cleanup = wire.calls.filter(call => call.method === "data.releaseCursor").at(-1);
        assert.ok(cleanup, name);
        wire.reply(cleanup, undefined, { code: "plugin_transport_failed" });
        await new Promise(resolve => setImmediate(resolve));
      }
      // The failed cleanup retains local ownership, so the plugin can still
      // release explicitly once the transport recovers.
      failRelease = false;
      const explicit = api.data.releaseCursor("input-cursor");
      const retryWire = wire.calls.filter(call => call.method === "data.releaseCursor").at(-1);
      assert.ok(retryWire, name);
      wire.reply(retryWire, { released: true });
      await explicit;
    } finally { api.dispose(); }
  }
});

test("an invalid cursor reply drops the dead local entry without a release round trip", async () => {
  const wire = channel(), api = sdk.createSurfaceClient("surface", wire);
  try {
    await allocate(api, wire, "stale-cursor");
    const failing = assert.rejects(api.data.query({ ...request, cursor: "stale-cursor" }), error => error.code === "plugin_cursor_invalid");
    wire.reply(wire.calls.at(-1), undefined, { code: "plugin_cursor_invalid" });
    await failing;
    const explicit = api.data.releaseCursor("stale-cursor");
    const releaseWire = wire.calls.filter(call => call.method === "data.releaseCursor").at(-1);
    if (releaseWire) wire.reply(releaseWire, { released: true });
    await explicit;
    assert.equal(wire.calls.filter(call => call.method === "data.releaseCursor").length, 0);
  } finally { api.dispose(); }
});

test("unconsumed boundaries keep the input cursor owned and retryable on both sides", async () => {
  const boundaries = [
    ["local abort", async (wire, api) => {
      const abort = new AbortController(); abort.abort();
      await assert.rejects(api.data.query({ ...request, cursor: "retry-cursor" }, abort.signal), error => error.code === "plugin_cancel_requested");
    }],
    ["failed send", async (wire, api) => {
      const send = wire.postMessage;
      wire.postMessage = message => { if (message.method === "data.query") throw new Error("synthetic send failure"); send(message); };
      await assert.rejects(api.data.query({ ...request, cursor: "retry-cursor" }), error => error.code === "plugin_transport_failed");
    }],
    ...["plugin_request_duplicate", "plugin_concurrency_limit", "plugin_api_unsupported", "plugin_source_denied"].map(code => [code, async (wire, api) => {
      const failing = assert.rejects(api.data.query({ ...request, cursor: "retry-cursor" }), error => error.code === code, code);
      wire.reply(wire.calls.at(-1), undefined, { code });
      await failing;
    }]),
  ];
  for (const [name, fail] of boundaries) {
    const wire = channel(), api = sdk.createSurfaceClient("surface", wire);
    try {
      await allocate(api, wire, "retry-cursor");
      await fail(wire, api);
      // Nothing reached Host query admission, so the cursor stays owned: an
      // explicit release still needs the wire round trip.
      const explicit = api.data.releaseCursor("retry-cursor");
      assert.equal(wire.calls.at(-1).method, "data.releaseCursor", name);
      wire.reply(wire.calls.at(-1), { released: true });
      await explicit;
    } finally { api.dispose(); }
  }
  // A transient pre-admission rejection leaves the same query retryable.
  const wire = channel(), api = sdk.createSurfaceClient("surface", wire);
  try {
    await allocate(api, wire, "retry-cursor");
    const busy = assert.rejects(api.data.query({ ...request, cursor: "retry-cursor" }), error => error.code === "plugin_request_duplicate");
    wire.reply(wire.calls.at(-1), undefined, { code: "plugin_request_duplicate" });
    await busy;
    const retried = api.data.query({ ...request, cursor: "retry-cursor" });
    wire.reply(wire.calls.at(-1), page(null));
    await retried;
  } finally { api.dispose(); }
});

test("an SDK deadline on a cursor query converges with the acknowledged release", async t => {
  const wire = channel(), api = sdk.createSurfaceClient("surface", wire);
  await allocate(api, wire, "input-cursor");
  t.mock.timers.enable({ apis: ["setTimeout"] });
  try {
    const failing = assert.rejects(api.data.query({ ...request, cursor: "input-cursor" }), error => error.code === "plugin_timeout");
    t.mock.timers.tick(20000);
    await failing;
    assert.ok(wire.calls.some(call => call.method === "request.cancel"));
    const released = wire.calls.filter(call => call.method === "data.releaseCursor");
    assert.equal(released.length, 1);
    assert.deepEqual(released[0].params, { cursor: "input-cursor" });
    wire.reply(released[0], { released: true });
    await new Promise(resolve => setImmediate(resolve));
    await api.data.releaseCursor("input-cursor");
    assert.equal(wire.calls.filter(call => call.method === "data.releaseCursor").length, 1);
  } finally { api.dispose(); }
});
