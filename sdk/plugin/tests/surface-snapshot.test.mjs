import assert from "node:assert/strict";
import test from "node:test";
import { sdk, testing } from "./load-sdk.mjs";

const version = { schemaRevision: "schema_0001", dataRevision: 1, clockPeriod: "", computed: {} };
const request = { contract: "vibetable.plugin-snapshot.v1", sources: [
  { query: { contract: "vibetable.plugin-query.v2", collection: "alpha", fields: ["id", "name"] } },
  { query: { contract: "vibetable.plugin-query.v2", collection: "beta", fields: ["id"] } },
] };
function info(overrides = {}) {
  return { contract: "vibetable.plugin-snapshot.v1", snapshotId: "owned", versions: { alpha: structuredClone(version), beta: { ...structuredClone(version), dataRevision: 0 } },
    sources: [{ collection: "alpha", fields: ["id", "name"], computedFields: [], filteredRows: 3, totalRows: 3, complete: true }, { collection: "beta", fields: ["id"], computedFields: [], filteredRows: 0, totalRows: 0, complete: true }],
    expiresAt: "2026-10-29T00:05:00Z", ...overrides };
}
const rows = { a1: { id: "a1", name: "x" }, a2: { id: "a2", name: "y" }, a3: { id: "a3", name: null } };
function chunk(collection, items, nextOffset) {
  return { collection, items, rowGuards: Object.fromEntries(items.map(item => [item.id, "digest"])), nextOffset, complete: true };
}
function page(snapshotId, collection, items, nextOffset, versions) {
  return { snapshotId, collection, items, rowGuards: Object.fromEntries(items.map(item => [item.id, "digest"])), nextOffset, versions, complete: true };
}
function batch(chunks, next, versions, snapshotId = "owned") {
  return { snapshotId, chunks, next, versions };
}

function channel() {
  const calls = [], listeners = new Set();
  return { calls, postMessage: message => calls.push(message), addEventListener: (_, listener) => listeners.add(listener), removeEventListener: (_, listener) => listeners.delete(listener),
    reply(call, result, error) { for (const listener of listeners) listener({ data: { contract: sdk.SURFACE_API_CONTRACT, requestId: call.requestId, ...(error ? { error: { code: error } } : { result }) } }); } };
}
async function openSnapshot(transport, client, overrides) {
  const opening = client.data.openSnapshot(request);
  assert.deepEqual(transport.calls.at(-1).params, request);
  assert.equal(transport.calls.at(-1).method, "snapshot.open");
  const reply = info(overrides); transport.reply(transport.calls.at(-1), reply);
  return { snapshot: await opening, reply };
}

test("snapshot pages walk every source in request order with one authorized network page each and release in finally", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const { snapshot, reply } = await openSnapshot(transport, client);
  assert.deepEqual(snapshot.versions, reply.versions);
  assert.equal(snapshot.expiresAt, reply.expiresAt);
  const work = (async () => {
    const seen = [];
    for await (const current of snapshot.pages()) seen.push([current.collection, current.items.length, current.nextOffset]);
    return seen;
  })();
  // alpha: 3 rows in one page of 200 (exhausted), beta: empty source.
  transport.reply(transport.calls.at(-1), page("owned", "alpha", [rows.a1, rows.a2, rows.a3], null, reply.versions));
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(transport.calls.at(-1).method, "snapshot.page");
  assert.deepEqual(transport.calls.at(-1).params, { snapshotId: "owned", collection: "beta", offset: 0, pageSize: 200 });
  transport.reply(transport.calls.at(-1), page("owned", "beta", [], null, reply.versions));
  await new Promise(resolve => setImmediate(resolve));
  // Exhaustion closes the handle before pages() resolves: reply to the release first.
  assert.equal(transport.calls.at(-1).method, "snapshot.release");
  assert.deepEqual(transport.calls.at(-1).params, { snapshotId: "owned" });
  transport.reply(transport.calls.at(-1), { released: true });
  assert.deepEqual(await work, [["alpha", 3, null], ["beta", 0, null]]);
  assert.equal(transport.calls.filter(call => call.method === "snapshot.page").length, 2);
  assert.equal(transport.calls.filter(call => call.method === "snapshot.readBatch").length, 0);
  await new Promise(resolve => setImmediate(resolve));
  await assert.rejects(snapshot.page("alpha"), error => error.code === "plugin_snapshot_stale");
  client.dispose();
});

test("snapshot readBatch crosses sources with one wire call per batch, tiles chunks from the explicit position and keeps the handle open", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const { snapshot, reply } = await openSnapshot(transport, client);
  const first = snapshot.readBatch();
  assert.equal(transport.calls.at(-1).method, "snapshot.readBatch");
  assert.deepEqual(transport.calls.at(-1).params, { snapshotId: "owned", next: null });
  // The Host cut alpha after two rows: the batch ends mid-source and names where to continue.
  const opening = batch([chunk("alpha", [rows.a1, rows.a2], 2)], { collection: "alpha", offset: 2 }, reply.versions);
  transport.reply(transport.calls.at(-1), opening);
  assert.deepEqual(await first, opening);
  const second = snapshot.readBatch(opening.next);
  assert.deepEqual(transport.calls.at(-1).params, { snapshotId: "owned", next: { collection: "alpha", offset: 2 } });
  // beta is empty: it rides along as a zero-row chunk and the walk is exhausted.
  const rest = batch([chunk("alpha", [rows.a3], null), chunk("beta", [], null)], null, reply.versions);
  transport.reply(transport.calls.at(-1), rest);
  assert.deepEqual(await second, rest);
  // A position is explicit data: the same handle can restart from the beginning or resume elsewhere.
  const again = snapshot.readBatch(null);
  assert.deepEqual(transport.calls.at(-1).params, { snapshotId: "owned", next: null });
  transport.reply(transport.calls.at(-1), opening);
  assert.deepEqual(await again, opening);
  const tail = snapshot.readBatch({ collection: "beta", offset: 0 });
  assert.deepEqual(transport.calls.at(-1).params, { snapshotId: "owned", next: { collection: "beta", offset: 0 } });
  const end = batch([chunk("beta", [], null)], null, reply.versions);
  transport.reply(transport.calls.at(-1), end);
  assert.deepEqual(await tail, end);
  assert.equal(transport.calls.filter(call => call.method === "snapshot.release").length, 0);
  assert.equal(transport.calls.filter(call => call.method === "snapshot.page").length, 0);
  const closing = snapshot.close(), repeated = snapshot.close();
  assert.equal(transport.calls.filter(call => call.method === "snapshot.release").length, 1);
  transport.reply(transport.calls.at(-1), { released: true });
  await Promise.all([closing, repeated]);
  await snapshot.close();
  assert.equal(transport.calls.filter(call => call.method === "snapshot.release").length, 1);
  await assert.rejects(snapshot.readBatch(), error => error.code === "plugin_snapshot_stale");
  client.dispose();
});

test("snapshot page and batch bounds are validated locally and a failed release keeps the handle for a retry", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const { snapshot } = await openSnapshot(transport, client);
  const before = transport.calls.length;
  await assert.rejects(snapshot.page("gamma"), error => error.code === "plugin_capability_invalid");
  await assert.rejects(snapshot.page("alpha", 0, 201), error => error.code === "plugin_capability_invalid");
  for (const bad of ["start", 5, { collection: "gamma", offset: 0 }, { collection: "alpha" }, { collection: "alpha", offset: 0, extra: 1 }, { collection: "alpha", offset: 4 }, { collection: "alpha", offset: -1 }, { collection: "alpha", offset: 1.5 }])
    await assert.rejects(snapshot.readBatch(bad), error => error.code === "plugin_capability_invalid");
  assert.equal(transport.calls.length, before);
  const closing = snapshot.close(), failed = assert.rejects(closing, error => error.code === "plugin_transport_failed");
  transport.reply(transport.calls.at(-1), null, "plugin_transport_failed");
  await failed;
  const retry = snapshot.close();
  assert.equal(transport.calls.at(-1).method, "snapshot.release");
  transport.reply(transport.calls.at(-1), { released: true });
  await retry;
  client.dispose();
});

test("snapshot queries are strict like data.query: cursor/pageSize presence and explicit nulls never reach the wire", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const base = { contract: "vibetable.plugin-query.v2", collection: "alpha", fields: ["id", "name"] };
  const open = query => client.data.openSnapshot({ ...request, sources: [{ query }] });
  for (const query of [{ ...base, pageSize: null }, { ...base, pageSize: 50 }, { ...base, cursor: "c" }, { ...base, cursor: null }, { ...base, ids: null }, { ...base, filters: null }, { ...base, sorts: null }, null, undefined])
    await assert.rejects(open(query), error => error.code === "plugin_capability_invalid");
  await assert.rejects(client.data.openSnapshot({ ...request, sources: "alpha" }), error => error.code === "plugin_capability_invalid");
  assert.equal(transport.calls.length, 0);
  // Absent members stay legal: the ordinary request reaches the wire unchanged.
  const opening = client.data.openSnapshot(request);
  assert.equal(transport.calls.length, 1);
  assert.deepEqual(transport.calls.at(-1).params, request);
  transport.reply(transport.calls.at(-1), info());
  await opening;
  client.dispose();
});

test("snapshot row guards follow row ids without the projection: keyed by unprojected ids, empty once revoked, items never gain a field", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const narrow = { ...request, sources: [{ query: { contract: "vibetable.plugin-query.v2", collection: "alpha", fields: ["name"] } }] };
  const opening = client.data.openSnapshot(narrow);
  assert.deepEqual(transport.calls.at(-1).params, narrow);
  const reply = info({ versions: { alpha: structuredClone(version) }, sources: [{ collection: "alpha", fields: ["name"], computedFields: [], filteredRows: 2, totalRows: 2, complete: true }] });
  transport.reply(transport.calls.at(-1), reply);
  const snapshot = await opening;
  // Guards are keyed by ids the plugin may read although no row projects id: served as they are.
  const guarded = snapshot.page("alpha");
  transport.reply(transport.calls.at(-1), { ...page("owned", "alpha", [{ name: "x" }, { name: "y" }], null, reply.versions), rowGuards: { a1: "digest", a2: "digest" } });
  const served = await guarded;
  assert.deepEqual(Object.keys(served.rowGuards), ["a1", "a2"]);
  assert.deepEqual(served.items, [{ name: "x" }, { name: "y" }]);
  // Revoked id: an empty guard map is still a well-formed page and batch.
  const revoked = snapshot.page("alpha");
  transport.reply(transport.calls.at(-1), { ...page("owned", "alpha", [{ name: "x" }, { name: "y" }], null, reply.versions), rowGuards: {} });
  assert.deepEqual((await revoked).rowGuards, {});
  const batched = snapshot.readBatch();
  transport.reply(transport.calls.at(-1), batch([{ ...chunk("alpha", [{ name: "x" }, { name: "y" }], null), rowGuards: {} }], null, reply.versions));
  assert.deepEqual((await batched).chunks[0].rowGuards, {});
  // An item that gains the unprojected id is not a page of this snapshot.
  const leaked = snapshot.page("alpha");
  transport.reply(transport.calls.at(-1), page("owned", "alpha", [{ id: "a1", name: "x" }, { id: "a2", name: "y" }], null, reply.versions));
  await assert.rejects(leaked, error => error.code === "plugin_transport_failed");
  // Unprojected or not, a guard is a string digest: nothing else passes.
  const malformed = snapshot.page("alpha");
  transport.reply(transport.calls.at(-1), { ...page("owned", "alpha", [{ name: "x" }, { name: "y" }], null, reply.versions), rowGuards: { a1: 1 } });
  await assert.rejects(malformed, error => error.code === "plugin_transport_failed");
  const closing = snapshot.close();
  assert.equal(transport.calls.at(-1).method, "snapshot.release");
  transport.reply(transport.calls.at(-1), { released: true });
  await closing;
  client.dispose();
});

test("snapshot stale from the Host drops local ownership: no re-open, no further wire calls for that handle", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const { snapshot } = await openSnapshot(transport, client);
  const stale = snapshot.page("alpha");
  const rejected = assert.rejects(stale, error => error.code === "plugin_snapshot_stale");
  transport.reply(transport.calls.at(-1), null, "plugin_snapshot_stale");
  await rejected;
  const before = transport.calls.length;
  await assert.rejects(snapshot.page("alpha"), error => error.code === "plugin_snapshot_stale");
  await assert.rejects(snapshot.readBatch(), error => error.code === "plugin_snapshot_stale");
  await assert.rejects(snapshot.readBatch({ collection: "alpha", offset: 1 }), error => error.code === "plugin_snapshot_stale");
  assert.equal(transport.calls.length, before);
  assert.equal(transport.calls.filter(call => call.method === "snapshot.open").length, 1);
  // A stale batch reply has the same effect: the handle is never used again.
  const { snapshot: other } = await openSnapshot(transport, client, { snapshotId: "batch" });
  const reading = other.readBatch();
  const refused = assert.rejects(reading, error => error.code === "plugin_snapshot_stale");
  transport.reply(transport.calls.at(-1), null, "plugin_snapshot_stale");
  await refused;
  await assert.rejects(other.readBatch(), error => error.code === "plugin_snapshot_stale");
  await assert.rejects(other.page("alpha"), error => error.code === "plugin_snapshot_stale");
  assert.equal(transport.calls.filter(call => call.params?.snapshotId === "batch").length, 1);
  // A release the Host no longer knows is stale and is not retried either.
  const { snapshot: gone } = await openSnapshot(transport, client, { snapshotId: "gone" });
  const closing = gone.close(), staleRelease = assert.rejects(closing, error => error.code === "plugin_snapshot_stale");
  transport.reply(transport.calls.at(-1), null, "plugin_snapshot_stale");
  await staleRelease;
  assert.equal(transport.calls.filter(call => call.method === "snapshot.release" && call.params.snapshotId === "gone").length, 1);
  client.dispose();
});

test("snapshot replies are fail-closed: identity, projection, row shape, offsets and the relayed version vector", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const broken = [
    { snapshotId: "" },
    { sources: [info().sources[1], info().sources[0]] },
    { sources: [{ ...info().sources[0], fields: ["id", "other"] }, info().sources[1]] },
    { versions: { alpha: version } },
    { versions: { alpha: version, beta: { ...version, dataRevision: -1 } } },
    { sources: [{ ...info().sources[0], filteredRows: 4 }, info().sources[1]] },
    { expiresAt: "not a date" },
    { extra: 1 },
  ];
  for (const overrides of broken) {
    const opening = client.data.openSnapshot(request);
    transport.reply(transport.calls.at(-1), info(overrides));
    await assert.rejects(opening, error => error.code === "plugin_transport_failed");
  }
  const { snapshot, reply } = await openSnapshot(transport, client);
  const bad = [
    page("other", "alpha", [rows.a1], null, reply.versions),
    page("owned", "beta", [rows.a1], null, reply.versions),
    page("owned", "alpha", [{ id: "a1" }, rows.a2, rows.a3], null, reply.versions),
    page("owned", "alpha", [rows.a1, rows.a2], null, reply.versions),
    page("owned", "alpha", [rows.a1], 1, { ...reply.versions, alpha: { ...version, dataRevision: 2 } }),
    page("owned", "alpha", [], 0, reply.versions),
    page("owned", "alpha", [rows.a1, rows.a2, rows.a3], 4, reply.versions),
    // The relayed version vector is exactly the requested collections: no extra key.
    page("owned", "alpha", [rows.a1], 1, { ...reply.versions, gamma: version }),
    // A non-null nextOffset always points at a row still to serve; the final slice carries null.
    page("owned", "alpha", [rows.a1, rows.a2, rows.a3], 3, reply.versions),
    // Guards are string digests keyed by ids of served rows, at most one per row.
    { ...page("owned", "alpha", [rows.a1], 1, reply.versions), rowGuards: { a1: 7 } },
    { ...page("owned", "alpha", [rows.a1], 1, reply.versions), rowGuards: { a9: "digest" } },
    { ...page("owned", "alpha", [rows.a1], 1, reply.versions), rowGuards: { a1: "digest", a2: "digest" } },
    { ...page("owned", "alpha", [rows.a1], 1, reply.versions), rowGuards: { "": "digest" } },
  ];
  for (const result of bad) {
    const reading = snapshot.page("alpha");
    const rejected = assert.rejects(reading, error => error.code === "plugin_transport_failed");
    transport.reply(transport.calls.at(-1), result);
    await rejected;
  }
  // A guard map may cover a subset of the served rows; it is still keyed by served ids only.
  const partial = snapshot.page("alpha");
  transport.reply(transport.calls.at(-1), { ...page("owned", "alpha", [rows.a1, rows.a2], 2, reply.versions), rowGuards: { a2: "digest" } });
  assert.deepEqual((await partial).rowGuards, { a2: "digest" });
  client.dispose();
});

test("snapshot batch replies are fail-closed: identity, tiling across sources, row budget, explicit next and the relayed version vector", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const { snapshot, reply } = await openSnapshot(transport, client);
  const versions = reply.versions, all = [rows.a1, rows.a2, rows.a3];
  const bad = [
    batch([], null, versions),
    batch([chunk("alpha", all, null), chunk("beta", [], null)], null, versions, "other"),
    { ...batch([chunk("alpha", all, null), chunk("beta", [], null)], null, versions), extra: 1 },
    batch([chunk("beta", [], null)], null, versions),
    batch([chunk("alpha", all, null), chunk("alpha", [], null)], null, versions),
    batch([chunk("alpha", [rows.a1, rows.a2], 2), chunk("beta", [], null)], { collection: "alpha", offset: 2 }, versions),
    batch([chunk("alpha", [rows.a1, rows.a2], null), chunk("beta", [], null)], null, versions),
    batch([chunk("alpha", [rows.a1], null), chunk("beta", [], null)], { collection: "alpha", offset: 1 }, versions),
    batch([chunk("alpha", [], null), chunk("beta", [], null)], null, versions),
    batch([chunk("alpha", [], 0), chunk("beta", [], null)], { collection: "alpha", offset: 3 }, versions),
    batch([chunk("alpha", [{ id: "a1" }, rows.a2, rows.a3], null), chunk("beta", [], null)], null, versions),
    batch([{ ...chunk("alpha", all, null), snapshotId: "owned" }, chunk("beta", [], null)], null, versions),
    batch([{ collection: "alpha", items: all, nextOffset: null, complete: true }, chunk("beta", [], null)], null, versions),
    batch([chunk("alpha", all, null), chunk("beta", [], null)], null, { ...versions, alpha: { ...version, dataRevision: 2 } }),
    // next must name exactly where the Host stopped: alpha is exhausted, beta is not covered yet.
    batch([chunk("alpha", all, null)], null, versions),
    batch([chunk("alpha", all, null), chunk("beta", [], null)], { collection: "beta", offset: 0 }, versions),
    batch([chunk("alpha", all, null), chunk("beta", [], null)], { collection: "beta", offset: 0, extra: 1 }, versions),
    batch([chunk("alpha", all, null), chunk("beta", [], null)], "end", versions),
    batch([chunk("alpha", all, null), chunk("beta", [], null)], { collection: "gamma", offset: 0 }, versions),
    batch([{ ...chunk("alpha", all, null), rowGuards: { a1: 1 } }, chunk("beta", [], null)], null, versions),
    batch([{ ...chunk("alpha", all, null), rowGuards: { a9: "digest" } }, chunk("beta", [], null)], null, versions),
    batch([chunk("alpha", [rows.a1, rows.a2, rows.a3], 3), chunk("beta", [], null)], { collection: "alpha", offset: 3 }, versions),
    batch([chunk("alpha", all, null), chunk("beta", [], null)], null, { ...versions, gamma: version }),
  ];
  for (const result of bad) {
    const reading = snapshot.readBatch();
    const rejected = assert.rejects(reading, error => error.code === "plugin_transport_failed");
    transport.reply(transport.calls.at(-1), result);
    await rejected;
  }
  // A batch that continues from beta may not claim alpha again, and one from offset 2 must start there.
  for (const [from, result] of [
    [{ collection: "beta", offset: 0 }, batch([chunk("alpha", all, null), chunk("beta", [], null)], null, versions)],
    [{ collection: "alpha", offset: 2 }, batch([chunk("alpha", all, null), chunk("beta", [], null)], null, versions)],
    [{ collection: "alpha", offset: 2 }, batch([chunk("alpha", [rows.a3], null)], null, versions)],
  ]) {
    const reading = snapshot.readBatch(from);
    const rejected = assert.rejects(reading, error => error.code === "plugin_transport_failed");
    transport.reply(transport.calls.at(-1), result);
    await rejected;
  }
  // A batch may end at a source boundary (budget spent) as long as next names the first uncovered source.
  const split = snapshot.readBatch({ collection: "alpha", offset: 2 });
  const boundary = batch([chunk("alpha", [rows.a3], null)], { collection: "beta", offset: 0 }, versions);
  transport.reply(transport.calls.at(-1), boundary);
  assert.deepEqual(await split, boundary);
  const whole = snapshot.readBatch();
  const spent = batch([chunk("alpha", all, null)], { collection: "beta", offset: 0 }, versions);
  transport.reply(transport.calls.at(-1), spent);
  assert.deepEqual(await whole, spent);
  // The row budget is a hard part of the batch shape.
  const { snapshot: wide } = await openSnapshot(transport, client, { snapshotId: "wide", sources: [{ ...info().sources[0], filteredRows: 250, totalRows: 250 }, info().sources[1]] });
  const many = Array.from({ length: 201 }, (_, index) => ({ id: `a${index}`, name: "x" }));
  const over = wide.readBatch();
  const refused = assert.rejects(over, error => error.code === "plugin_transport_failed");
  transport.reply(transport.calls.at(-1), batch([chunk("alpha", many, 201)], { collection: "alpha", offset: 201 }, versions, "wide"));
  await refused;
  const exact = wide.readBatch();
  const limit = batch([chunk("alpha", many.slice(0, 200), 200)], { collection: "alpha", offset: 200 }, versions, "wide");
  transport.reply(transport.calls.at(-1), limit);
  assert.deepEqual(await exact, limit);
  client.dispose();
});

test("snapshot replies that land after cancellation are never delivered: the handle is released and dropped", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const last = method => transport.calls.findLast(call => call.method === method);
  // page: the Host's reply wins the race against request.cancel; the page is still not delivered.
  const { snapshot, reply } = await openSnapshot(transport, client);
  const controller = new AbortController();
  const reading = snapshot.page("alpha", 0, 200, controller.signal);
  const rejected = assert.rejects(reading, error => error.code === "plugin_cancel_requested");
  controller.abort();
  assert.equal(last("request.cancel").params.requestId, last("snapshot.page").requestId);
  transport.reply(last("snapshot.page"), page("owned", "alpha", [rows.a1], 1, reply.versions));
  await rejected;
  assert.equal(transport.calls.at(-1).method, "snapshot.release");
  assert.deepEqual(transport.calls.at(-1).params, { snapshotId: "owned" });
  const before = transport.calls.length;
  await assert.rejects(snapshot.page("alpha"), error => error.code === "plugin_snapshot_stale");
  await assert.rejects(snapshot.readBatch(), error => error.code === "plugin_snapshot_stale");
  assert.equal(transport.calls.length, before);
  // readBatch: the same race, the same outcome.
  const { snapshot: other, reply: otherReply } = await openSnapshot(transport, client, { snapshotId: "batch" });
  const batchAbort = new AbortController();
  const batching = other.readBatch(null, batchAbort.signal);
  const refused = assert.rejects(batching, error => error.code === "plugin_cancel_requested");
  batchAbort.abort();
  transport.reply(last("snapshot.readBatch"), batch([chunk("alpha", [rows.a1], 1), chunk("beta", [], null)], { collection: "alpha", offset: 1 }, otherReply.versions, "batch"));
  await refused;
  assert.equal(transport.calls.at(-1).method, "snapshot.release");
  assert.deepEqual(transport.calls.at(-1).params, { snapshotId: "batch" });
  const afterBatch = transport.calls.length;
  await assert.rejects(other.readBatch({ collection: "alpha", offset: 1 }), error => error.code === "plugin_snapshot_stale");
  assert.equal(transport.calls.length, afterBatch);
  // pages(): the cancelled page ends the walk and the handle; the late reply is never yielded.
  const { snapshot: walked, reply: walkReply } = await openSnapshot(transport, client, { snapshotId: "walk" });
  const walkAbort = new AbortController();
  const stepping = walked.pages(walkAbort.signal).next();
  const stopped = assert.rejects(stepping, error => error.code === "plugin_cancel_requested");
  walkAbort.abort();
  transport.reply(last("snapshot.page"), page("walk", "alpha", [rows.a1], 1, walkReply.versions));
  await stopped;
  assert.equal(transport.calls.filter(call => call.method === "snapshot.release" && call.params.snapshotId === "walk").length, 1);
  await assert.rejects(walked.page("alpha"), error => error.code === "plugin_snapshot_stale");
  client.dispose();
});

test("dispose cancels an outstanding snapshot open, releases a late handle fire-and-forget and never registers it", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const opening = client.data.openSnapshot(request);
  const rejected = assert.rejects(opening, error => error.code === "plugin_surface_closed");
  const sent = transport.calls.at(-1);
  assert.equal(sent.method, "snapshot.open");
  client.dispose();
  assert.ok(transport.calls.some(call => call.method === "request.cancel" && call.params.requestId === sent.requestId));
  transport.reply(sent, info());
  await rejected;
  // Owned handles are released on dispose without waiting for receipts.
  const transport2 = channel(), client2 = sdk.createSurfaceClient("surface", transport2);
  const opening2 = client2.data.openSnapshot(request); transport2.reply(transport2.calls.at(-1), info());
  await opening2;
  client2.dispose();
  assert.ok(transport2.calls.some(call => call.method === "snapshot.release" && call.params.snapshotId === "owned"));
});

test("pages() decides delivery in the turn the page resolves: an abort that lands as the next microtask, queued by the very read that checked it, never yields a page after it", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const { snapshot, reply } = await openSnapshot(transport, client);
  const last = method => transport.calls.findLast(call => call.method === method);
  // The signal reports "not aborted" to the first read after the reply and aborts as a microtask
  // queued by that read. A check that awaits once more before delivering resumes after that
  // microtask and yields the page; a synchronous check has already yielded, and the generator's own
  // re-check right before the yield refuses it.
  const controller = new AbortController();
  const signal = controller.signal;
  const real = Object.getOwnPropertyDescriptor(AbortSignal.prototype, "aborted");
  let replied = false, armed = true;
  Object.defineProperty(signal, "aborted", { get() {
    const value = real.get.call(this);
    if (replied && armed && !value) { armed = false; queueMicrotask(() => controller.abort()); }
    return value;
  } });
  const stepping = snapshot.pages(signal).next();
  transport.reply(last("snapshot.page"), page("owned", "alpha", [rows.a1], 1, reply.versions));
  replied = true;
  const settled = await stepping.then(value => ({ value }), error => ({ error }));
  assert.ok(settled.error, "no page may be yielded once the abort landed");
  assert.equal(settled.error.code, "plugin_cancel_requested");
  assert.equal(controller.signal.aborted, true);
  assert.deepEqual(transport.calls.filter(call => call.method === "snapshot.release").map(call => call.params), [{ snapshotId: "owned" }]);
  await assert.rejects(snapshot.page("alpha"), error => error.code === "plugin_snapshot_stale");
  client.dispose();
});

test("snapshot open sends one stable copy of the request and checks the reply against that copy, not against the caller's object", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const mutable = structuredClone(request);
  const opening = client.data.openSnapshot(mutable);
  const sent = transport.calls.at(-1);
  // The caller keeps mutating its request while the round trip is in flight.
  mutable.sources[0].query.fields.push("extra");
  mutable.sources.push({ query: { contract: "vibetable.plugin-query.v2", collection: "gamma", fields: ["id"] } });
  mutable.contract = "changed";
  transport.reply(sent, info());
  const snapshot = await opening;
  assert.deepEqual(sent.params, request);
  assert.equal(snapshot.snapshotId, "owned");
  assert.deepEqual(snapshot.sources.map(source => source.collection), ["alpha", "beta"]);
  const closing = snapshot.close();
  assert.equal(transport.calls.at(-1).method, "snapshot.release");
  transport.reply(transport.calls.at(-1), { released: true });
  await closing;
  client.dispose();
});

test("an open reply the SDK refuses releases only the handle it can confirm: a colliding or malformed id releases nothing", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const last = method => transport.calls.findLast(call => call.method === method);
  const releases = () => transport.calls.filter(call => call.method === "snapshot.release").map(call => call.params);
  const { snapshot, reply } = await openSnapshot(transport, client);
  // A reply naming the handle this surface already owns, but inconsistent otherwise, never releases it.
  const colliding = client.data.openSnapshot(request);
  transport.reply(transport.calls.at(-1), info({ sources: [] }));
  await assert.rejects(colliding, error => error.code === "plugin_transport_failed");
  assert.deepEqual(releases(), []);
  // A refused reply with a fresh id hands that handle back, exactly once.
  const fresh = client.data.openSnapshot(request);
  transport.reply(transport.calls.at(-1), info({ snapshotId: "fresh", versions: { alpha: structuredClone(version) } }));
  await assert.rejects(fresh, error => error.code === "plugin_transport_failed");
  assert.deepEqual(releases(), [{ snapshotId: "fresh" }]);
  // No confirmable id, nothing to release: no undefined or empty id ever reaches the wire.
  for (const malformed of [info({ snapshotId: 42 }), info({ snapshotId: "" }), { ...info(), extra: 1 }]) {
    const opening = client.data.openSnapshot(request);
    transport.reply(transport.calls.at(-1), malformed);
    await assert.rejects(opening, error => error.code === "plugin_transport_failed");
  }
  assert.deepEqual(releases(), [{ snapshotId: "fresh" }]);
  // An open cancelled before its reply releases the late handle it confirms, and nothing for a malformed one.
  const abort = new AbortController();
  const cancelled = client.data.openSnapshot(request, abort.signal);
  const refused = assert.rejects(cancelled, error => error.code === "plugin_cancel_requested");
  abort.abort();
  transport.reply(last("snapshot.open"), info({ snapshotId: "late" }));
  await refused;
  const raced = new AbortController();
  const racing = client.data.openSnapshot(request, raced.signal);
  const racingRefused = assert.rejects(racing, error => error.code === "plugin_cancel_requested");
  raced.abort();
  transport.reply(last("snapshot.open"), info({ snapshotId: "" }));
  await racingRefused;
  assert.deepEqual(releases(), [{ snapshotId: "fresh" }, { snapshotId: "late" }]);
  // The handle that was kept still serves.
  const reading = snapshot.page("alpha");
  transport.reply(last("snapshot.page"), page("owned", "alpha", [rows.a1, rows.a2, rows.a3], null, reply.versions));
  assert.equal((await reading).items.length, 3);
  assert.equal(releases().length, 2);
  client.dispose();
});

test("a release refused before the wire keeps the handle for a retry, and dispose releases every handle whose release is unconfirmed, never one it no longer owns", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const releases = id => transport.calls.filter(call => call.method === "snapshot.release" && call.params.snapshotId === id).length;
  const wire = () => transport.calls.length;
  // Nine handles; eight releases left unanswered fill the shared release quota of 8.
  const handles = [];
  for (let i = 1; i <= 9; i++) handles.push((await openSnapshot(transport, client, { snapshotId: `h${i}` })).snapshot);
  const closings = handles.slice(0, 8).map(snapshot => { const closing = snapshot.close(); assert.equal(transport.calls.at(-1).method, "snapshot.release"); return closing; });
  // The ninth close is refused before the wire: the handle stays owned, and a retry would go out.
  const sent = wire();
  await assert.rejects(handles[8].close(), error => error.code === "plugin_concurrency_limit");
  assert.equal(wire(), sent);
  assert.equal(releases("h9"), 0);
  // A handle whose release was acknowledged is no longer owned: dispose sends nothing for it, and
  // close() on it afterwards sends nothing either.
  transport.reply(transport.calls.find(call => call.method === "snapshot.release" && call.params.snapshotId === "h1"), { released: true });
  await closings[0];
  await handles[0].close();
  assert.equal(releases("h1"), 1);
  // dispose: the seven releases still in flight and the one never sent are all released again,
  // fire-and-forget (release is idempotent); the in-flight calls end with the surface closed.
  const closedByDispose = closings.slice(1).map(closing => assert.rejects(closing, error => error.code === "plugin_surface_closed"));
  client.dispose();
  for (const id of ["h2", "h3", "h4", "h5", "h6", "h7", "h8"]) assert.equal(releases(id), 2, id);
  assert.equal(releases("h9"), 1);
  assert.equal(releases("h1"), 1);
  await Promise.all(closedByDispose);
  // After dispose no snapshot owns anything: close() resolves and sends nothing.
  const after = wire();
  for (const snapshot of handles) await snapshot.close();
  assert.equal(wire(), after);
});

test("offline surface requires an explicit snapshot authority and forwards the versioned methods", async () => {
  const plain = testing.createOfflineSurfaceHost({ pluginId: "synthetic", workspaceId: "synthetic", surfaceToken: "surface" });
  const plainClient = sdk.createSurfaceClient("surface", plain.transport);
  await assert.rejects(plainClient.data.openSnapshot(request), error => error.code === "plugin_api_unsupported");
  plainClient.dispose(); plain.retire();
  const seen = [];
  const versions = info().versions;
  const backed = testing.createOfflineSurfaceHost({ pluginId: "synthetic", workspaceId: "synthetic", surfaceToken: "other", snapshotAdapter: async (method, params) => {
    seen.push({ method, params });
    if (method === "snapshot.open") return info();
    if (method === "snapshot.readBatch") return batch([chunk("alpha", [rows.a1, rows.a2, rows.a3], null), chunk("beta", [], null)], params.next, versions);
    if (method === "snapshot.page") return page("owned", params.collection, params.collection === "alpha" ? [rows.a1, rows.a2, rows.a3] : [], null, versions);
    return { released: true };
  } });
  const other = sdk.createSurfaceClient("other", backed.transport), snapshot = await other.data.openSnapshot(request);
  assert.deepEqual(seen[0], { method: "snapshot.open", params: request });
  const first = await snapshot.readBatch();
  assert.deepEqual(first.chunks.map(part => [part.collection, part.items.map(item => item.id)]), [["alpha", ["a1", "a2", "a3"]], ["beta", []]]);
  assert.deepEqual(first.next, null);
  assert.deepEqual(seen[1], { method: "snapshot.readBatch", params: { snapshotId: "owned", next: null } });
  const single = await snapshot.page("alpha");
  assert.deepEqual(single.items.map(item => item.id), ["a1", "a2", "a3"]);
  assert.deepEqual(seen[2], { method: "snapshot.page", params: { snapshotId: "owned", collection: "alpha", offset: 0, pageSize: 200 } });
  await snapshot.close();
  assert.deepEqual(seen.at(-1), { method: "snapshot.release", params: { snapshotId: "owned" } });
  other.dispose(); backed.retire();
  assert.equal(sdk.SNAPSHOT_LIMITS.sources, 64);
  assert.equal(sdk.SNAPSHOT_LIMITS.batchRows, sdk.SNAPSHOT_LIMITS.pageRows);
  assert.equal(sdk.SNAPSHOT_LIMITS.batchBytes, 256 * 1024);
  assert.equal(sdk.isDataVersion(version), true);
  assert.equal(sdk.isDataVersion({ ...version, computed: null }), false);
});
