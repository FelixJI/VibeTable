import assert from "node:assert/strict";
import test from "node:test";
import { sdk, testing } from "./load-sdk.mjs";

const request = { contract: "vibetable.plugin-dataset.v1", sources: [{ query: { contract: "vibetable.plugin-query.v2", collection: "synthetic", fields: ["id", "amount"] }, groupBy: [], sums: [{ field: "amount", storage: "decimalText", scale: 2, digits: 38 }] }] };
const info = { contract: "vibetable.plugin-dataset.v1", datasetId: "owned", versions: { synthetic: { schemaRevision: "s1", dataRevision: 1, clockPeriod: "", computed: {} } }, sources: [{ collection: "synthetic", fields: ["id", "amount"], computedFields: [], filteredRows: 201, totalRows: 201, complete: true }], expiresAt: "2026-10-29T00:05:00Z" };

function channel() {
  const calls = [], listeners = new Set();
  return { calls, postMessage: message => calls.push(message), addEventListener: (_, listener) => listeners.add(listener), removeEventListener: (_, listener) => listeners.delete(listener),
    reply(call, result, error) { for (const listener of listeners) listener({ data: { contract: sdk.SURFACE_API_CONTRACT, requestId: call.requestId, ...(error ? { error: { code: error } } : { result }) } }); } };
}

test("dataset preserves authoritative computed field metadata and ordinary JSON object keys", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const metadata = structuredClone(info); metadata.sources[0].fields = ["payload", "computed"]; metadata.sources[0].computedFields = ["computed"];
  const opening = client.dataset.open(request); transport.reply(transport.calls.at(-1), metadata); const dataset = await opening;
  assert.deepEqual(dataset.sources[0].computedFields, ["computed"]);
  const items = [{ payload: { fresh: true, value: "x", other: 42 }, computed: { state: "ready", fresh: true, value: "12.34" } }, { payload: { fresh: true }, computed: { state: "ready", fresh: true, value: null } }];
  const reading = dataset.page("synthetic"); transport.reply(transport.calls.at(-1), { items, nextOffset: null });
  assert.deepEqual((await reading).items, items);
  client.dispose();
});

test("dataset page/summary/export keep one handle/version and generator finally releases on consumer failure", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const opening = client.dataset.open(request); transport.reply(transport.calls.at(-1), info);
  const dataset = await opening;
  const summary = dataset.summary("synthetic");
  assert.equal(transport.calls.at(-1).method, "dataset.summary");
  transport.reply(transport.calls.at(-1), { datasetId: info.datasetId, versions: info.versions, summary: { sums: [{ value: "9007199254740993.01", complete: true }] } });
  assert.equal((await summary).summary.sums[0].value, "9007199254740993.01");
  const exporting = dataset.export("synthetic", "synthetic.csv");
  assert.deepEqual(transport.calls.at(-1).params, { datasetId: "owned", collection: "synthetic", suggestedName: "synthetic.csv" });
  transport.reply(transport.calls.at(-1), { committed: true, datasetId: "owned", versions: info.versions, rowsWritten: 201 });
  assert.deepEqual((await exporting).versions, info.versions);
  const work = (async () => { for await (const page of dataset.pages("synthetic")) { assert.equal(page.items[0].amount, "0.01"); throw new Error("consumer failed"); } })();
  const rejected = assert.rejects(work, /consumer failed/);
  transport.reply(transport.calls.at(-1), { datasetId: "owned", items: [{ id: "a", amount: "0.01" }], nextOffset: 200 });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(transport.calls.at(-1).method, "dataset.release");
  transport.reply(transport.calls.at(-1), { released: true });
  await rejected;
  await assert.rejects(dataset.summary("synthetic"), error => error.code === "plugin_dataset_stale");
  client.dispose();
});

test("dataset stale propagates without opening another snapshot; release stays reachable at normal concurrency limit", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const opening = client.dataset.open(request); transport.reply(transport.calls.at(-1), info); const dataset = await opening;
  const page = dataset.page("synthetic"); const rejected = assert.rejects(page, error => error.code === "plugin_dataset_stale");
  transport.reply(transport.calls.at(-1), null, "plugin_dataset_stale"); await rejected;
  assert.equal(transport.calls.filter(call => call.method === "dataset.open").length, 1);
  const pending = Array.from({ length: 8 }, () => client.context.read());
  const failures = pending.map(promise => assert.rejects(promise, error => error.code === "plugin_surface_closed"));
  const released = dataset.release(), repeated = dataset.release();
  assert.equal(transport.calls.at(-1).method, "dataset.release");
  transport.reply(transport.calls.at(-1), { released: true });
  await Promise.all([released, repeated]);
  client.dispose(); await Promise.all(failures);
});

test("dispose cancels an outstanding open and never registers a late dataset", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const opening = client.dataset.open(request); const rejected = assert.rejects(opening, error => error.code === "plugin_surface_closed");
  const sent = transport.calls.at(-1); client.dispose();
  assert.ok(transport.calls.some(call => call.method === "request.cancel" && call.params.requestId === sent.requestId));
  transport.reply(sent, info); await rejected;
});

test("lost native dataset export receipt is unknown and never replayed", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const opening = client.dataset.open(request); transport.reply(transport.calls.at(-1), info); const dataset = await opening;
  const exporting = dataset.export("synthetic", "synthetic.csv");
  const rejected = assert.rejects(exporting, error => error.code === "plugin_commit_unknown");
  client.dispose(); await rejected;
  assert.equal(transport.calls.filter(call => call.method === "dataset.export").length, 1);
});

test("offline surface requires an explicit dataset authority corpus and forwards the versioned request", async () => {
  const host = testing.createOfflineSurfaceHost({ pluginId: "synthetic", workspaceId: "synthetic", surfaceToken: "surface" });
  const client = sdk.createSurfaceClient("surface", host.transport);
  await assert.rejects(client.dataset.open(request), error => error.code === "plugin_api_unsupported");
  client.dispose(); host.retire();
  const calls = [];
  const backed = testing.createOfflineSurfaceHost({ pluginId: "synthetic", workspaceId: "synthetic", surfaceToken: "other", datasetAdapter: async (method, params) => { calls.push({ method, params }); return method === "dataset.open" ? info : { released: true }; } });
  const other = sdk.createSurfaceClient("other", backed.transport), dataset = await other.dataset.open(request);
  assert.deepEqual(calls[0], { method: "dataset.open", params: request });
  await dataset.release(); other.dispose(); backed.retire();
});
