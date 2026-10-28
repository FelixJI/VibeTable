import test from "node:test";
import assert from "node:assert/strict";
import { sdk } from "./load-sdk.mjs";

function fixture() {
  const calls = [];
  let receive;
  const channel = {
    postMessage: message => calls.push(message),
    addEventListener: (_type, listener) => { receive = listener; },
    removeEventListener() {},
  };
  const client = sdk.createSurfaceClient("transaction-surface", channel);
  return { client, calls, reply(result, error) {
    receive({ data: { contract: sdk.SURFACE_API_CONTRACT, requestId: calls.at(-1).requestId, result, error } });
  } };
}

test("transaction uses persistent business identity and single calls without retry or host commit channel", async () => {
  const f = fixture();
  const declaration = { contract: "vibetable.plugin-transactions.v1", modelId: "orders", version: 1,
    decimals: [], rules: [{ id: "unique", kind: "unique", entity: "headers", fields: ["number"] }] };
  const configured = f.client.transaction.configure({ declaration });
  assert.equal(f.calls.at(-1).method, "transaction.configure");
  assert.deepEqual(f.calls.at(-1).params, { declaration });
  f.reply(null);
  assert.equal(await configured, null);
  const plan = { contract: "vibetable.transaction-plan.v1", modelId: "orders", operations: [
    { entity: "headers", kind: "create", tempId: "head", values: { number: "SYNTHETIC" } },
    { entity: "lines", kind: "create", tempId: "line", values: { header: { entity: "headers", tempId: "head" }, amount: "0.30" } },
  ] };
  const preview = f.client.transaction.preview({ plan });
  assert.deepEqual(f.calls.at(-1).params, { plan });
  f.reply({ status: "prepared", requestId: "persistent-business-id", plan });
  assert.equal((await preview).requestId, "persistent-business-id");
  const submit = f.client.transaction.submit({ requestId: "persistent-business-id" });
  assert.equal(f.calls.at(-1).method, "transaction.submit");
  assert.deepEqual(f.calls.at(-1).params, { requestId: "persistent-business-id" });
  assert.notEqual(f.calls.at(-1).requestId, "persistent-business-id");
  f.reply(undefined, { code: "plugin_commit_unknown" });
  await assert.rejects(submit, error => error.code === "plugin_commit_unknown");
  assert.equal(f.calls.length, 3);
  const status = f.client.transaction.status({ requestId: "persistent-business-id" });
  f.reply({ status: "unknown", requestId: "persistent-business-id" });
  assert.equal((await status).status, "unknown");
  assert.equal(f.calls.length, 4);
  assert.equal("commit" in f.client.transaction, false);
  f.client.dispose();
});

test("closing after submit reports unknown and a new surface can query original request without submit", async () => {
  const f = fixture();
  const submit = f.client.transaction.submit({ requestId: "original" });
  f.client.dispose();
  await assert.rejects(submit, error => error.code === "plugin_commit_unknown");
  assert.equal(f.calls.length, 1);
  const reopened = fixture();
  const status = reopened.client.transaction.status({ requestId: "original" });
  const applied = { status: "applied", requestId: "original", newIds: { headers: { head: "actual-id" } } };
  reopened.reply(applied);
  assert.deepEqual(await status, applied);
  assert.deepEqual(reopened.calls.map(call => call.method), ["transaction.status"]);
  reopened.client.dispose();
});

test("abort requests cancellation but a received success remains success", async () => {
  const f = fixture();
  const controller = new AbortController();
  const submit = f.client.transaction.submit({ requestId: "original" }, controller.signal);
  const request = f.calls[0];
  controller.abort();
  assert.equal(f.calls[1].method, "request.cancel");
  assert.deepEqual(f.calls[1].params, { requestId: request.requestId });
  const applied = { status: "applied", requestId: "original" };
  // Cancel acknowledgement has independent transport correlation.
  f.calls.pop(); f.reply(applied);
  assert.deepEqual(await submit, applied);
  f.client.dispose();
});

test("only an explicit refresh requests a new preview revision and never submits it", async () => {
  const f = fixture();
  const plan = { contract: "vibetable.transaction-plan.v1", modelId: "orders", operations: [
    { entity: "headers", kind: "create", tempId: "head", values: { number: "SYNTHETIC" } },
  ] };
  const refreshing = f.client.transaction.preview({ requestId: "original", plan, refresh: true });
  assert.deepEqual(f.calls[0].params, { requestId: "original", plan, refresh: true });
  const prepared = { status: "prepared", requestId: "original", previewRevision: 2,
    newIds: { headers: { head: "same-id" } }, dataRevisions: { "table-head": "data_2" },
    effects: [{ tableId: "table-head", recordId: "same-id", operation: "insert", schemaRevision: "schema_1" },
      { tableId: "table-target", recordId: "existing", operation: "update", schemaRevision: "schema_1", expectedDigest: "guard" }] };
  f.reply(prepared);
  assert.deepEqual(await refreshing, prepared);
  assert.deepEqual(f.calls.map(call => call.method), ["transaction.preview"]);
  f.client.dispose();
});
