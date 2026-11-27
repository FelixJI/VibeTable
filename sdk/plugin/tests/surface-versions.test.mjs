import assert from "node:assert/strict";
import test from "node:test";
import { sdk, testing } from "./load-sdk.mjs";

const version = { schemaRevision: "schema_0001", dataRevision: 0, clockPeriod: "", computed: {} };
const computed = { definition: 1, watermark: "sha256:abc|minute:2025-06-01T12:30:00Z", ready: true, status: "" };
const computedVersion = { schemaRevision: "schema_0002", dataRevision: 7, clockPeriod: "day:2025-06-01", computed: { fld_minute: computed } };
const request = { contract: "vibetable.plugin-data-versions.v1", sources: [{ collection: "orders", fields: ["id", "amount"] }, { collection: "lines", fields: ["id", "fld_minute"] }] };
const result = { contract: "vibetable.plugin-data-versions.v1", versions: { orders: version, lines: computedVersion } };

function channel() {
  const calls = [], listeners = new Set();
  return { calls, postMessage: message => calls.push(message), addEventListener: (_, listener) => listeners.add(listener), removeEventListener: (_, listener) => listeners.delete(listener),
    reply(call, result, error) { for (const listener of listeners) listener({ data: { contract: sdk.SURFACE_API_CONTRACT, requestId: call.requestId, ...(error ? { error: { code: error } } : { result }) } }); } };
}

test("data.versions is exactly one bridge request carrying projections and prior versions verbatim", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  const reading = client.data.versions(request);
  assert.equal(transport.calls.length, 1);
  assert.equal(transport.calls[0].method, "data.versions");
  assert.deepEqual(transport.calls[0].params, request);
  assert.notEqual(transport.calls[0].params, request);
  assert.equal("stale" in transport.calls[0].params, false);
  transport.reply(transport.calls[0], result);
  assert.deepEqual(await reading, result);
  // The next batch echoes the versions exactly as returned, empty clock and
  // status strings included; nothing is normalized, split or cached locally.
  const comparing = client.data.versions({ ...request, versions: result.versions });
  assert.equal(transport.calls.length, 2);
  assert.deepEqual(transport.calls[1].params, { ...request, versions: result.versions });
  assert.deepEqual(transport.calls[1].params.versions.lines.computed.fld_minute, computed);
  const stale = { ...result, versions: { ...result.versions, lines: { ...computedVersion, dataRevision: 8 } }, stale: ["lines"] };
  transport.reply(transport.calls[1], stale);
  assert.deepEqual(await comparing, stale);
  client.dispose();
  assert.equal(transport.calls.filter(call => call.method === "data.versions").length, 2);
});

test("data.versions failures keep their own code, return nothing partial and never retry", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  for (const code of ["plugin_read_denied", "plugin_capability_invalid", "plugin_cursor_stale", "plugin_query_failed", "plugin_api_unsupported"]) {
    const rejected = assert.rejects(client.data.versions(request), error => error.code === code);
    transport.reply(transport.calls.at(-1), null, code);
    await rejected;
  }
  assert.equal(transport.calls.length, 5);
  // Only the explicit Host error denotes an unsupported capability.
  const rejected = assert.rejects(client.data.versions(request), error => error.code === "plugin_transport_failed");
  transport.reply(transport.calls.at(-1), { contract: "vibetable.plugin-dataset.v1", versions: {} });
  await rejected;
  // Omitting the comparison after prior versions were sent is a transport failure.
  const incomplete = assert.rejects(client.data.versions({ ...request, versions: result.versions }), error => error.code === "plugin_transport_failed");
  transport.reply(transport.calls.at(-1), result);
  await incomplete;
  client.dispose();
  assert.equal(transport.calls.length, 7);
});

test("incomplete versions and contradictory comparison results fail closed", async () => {
  const transport = channel(), client = sdk.createSurfaceClient("surface", transport);
  for (const reply of [
    { ...result, versions: {orders: version}, stale: [] },
    { ...result, versions: { ...result.versions, orders: { ...version, dataRevision: null } }, stale: [] },
    { ...result, versions: { ...result.versions, orders: { ...version, computed: null } }, stale: [] },
    { ...result, stale: ["orders"] },
  ]) {
    const rejected = assert.rejects(client.data.versions({ ...request, versions: result.versions }), error => error.code === "plugin_transport_failed");
    transport.reply(transport.calls.at(-1), reply);
    await rejected;
  }
  client.dispose();
});

test("sameDataVersion compares the exact vector and the offline surface needs an explicit versions authority", async () => {
  assert.equal(sdk.DATA_VERSIONS_LIMITS.sources, 64);
  assert.equal(sdk.sameDataVersion(version, { ...version }), true);
  assert.equal(sdk.sameDataVersion(version, { ...version, dataRevision: 1 }), false);
  assert.equal(sdk.sameDataVersion(version, { ...version, clockPeriod: "day:2025-06-01" }), false);
  assert.equal(sdk.sameDataVersion(computedVersion, { ...computedVersion, computed: {} }), false);
  assert.equal(sdk.sameDataVersion(computedVersion, { ...computedVersion, computed: { fld_minute: { ...computed, status: "pending" } } }), false);
  assert.equal(sdk.sameDataVersion(computedVersion, { ...computedVersion, computed: { fld_other: computed } }), false);
  // A prior version lacking dataRevision is not a known 0, and an absent side never matches.
  assert.equal(sdk.sameDataVersion(version, { schemaRevision: "schema_0001", clockPeriod: "", computed: {} }), false);
  assert.equal(sdk.sameDataVersion(version, undefined), false);
  const host = testing.createOfflineSurfaceHost({ pluginId: "synthetic", workspaceId: "synthetic", surfaceToken: "surface" });
  const client = sdk.createSurfaceClient("surface", host.transport);
  await assert.rejects(client.data.versions(request), error => error.code === "plugin_api_unsupported");
  client.dispose(); host.retire();
  const calls = [];
  const backed = testing.createOfflineSurfaceHost({ pluginId: "synthetic", workspaceId: "synthetic", surfaceToken: "other", versionsAdapter: async params => { calls.push(params); return { ...result, stale: [] }; } });
  const other = sdk.createSurfaceClient("other", backed.transport);
  const compared = await other.data.versions({ ...request, versions: result.versions });
  assert.deepEqual(calls, [{ ...request, versions: result.versions }]);
  assert.deepEqual(compared.stale, []);
  other.dispose(); backed.retire();
});
