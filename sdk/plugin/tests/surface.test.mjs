import assert from "node:assert/strict";
import test from "node:test";
import { sdk } from "./load-sdk.mjs";

function channel() {
  const listeners = new Set();
  const calls = [];
  return {
    calls,
    postMessage: message => calls.push(message),
    addEventListener: (_type, listener) => listeners.add(listener),
    removeEventListener: (_type, listener) => listeners.delete(listener),
    reply(request, result, error) { for (const listener of listeners) listener({ data: { contract: sdk.SURFACE_API_CONTRACT, requestId: request.requestId, result, ...(error ? { error } : {}) } }); },
    listeners,
  };
}

test("surface client uses only the closed protocol and supports long-lived requests", async () => {
  const transport = channel();
  const client = sdk.createSurfaceClient("surface-a", transport);
  for (let i = 0; i < 100; i++) {
    const request = client.storage.set("settings", { value: 0, flag: false, empty: null });
    const wire = transport.calls.at(-1);
    assert.equal(wire.method, "storage.set");
    assert.deepEqual(wire.params.value, { value: 0, flag: false, empty: null });
    assert.equal(wire.surfaceToken, "surface-a");
    assert.equal("pluginId" in wire, false);
    transport.reply(wire, null);
    await request;
  }
  assert.equal(new Set(transport.calls.map(item => item.requestId)).size, 100);
  client.dispose();
  assert.equal(transport.listeners.size, 0);
  await assert.rejects(client.context.read(), error => error.code === "plugin_surface_closed");
});

test("submit requests a plan handle and abort requests cancellation without approving or retrying", async () => {
  const transport = channel();
  const client = sdk.createSurfaceClient("surface-a", transport);
  const abort = new AbortController();
  const request = client.mutation.submit("opaque-plan", abort.signal);
  const submit = transport.calls[0];
  assert.deepEqual(submit.params, { planId: "opaque-plan" });
  abort.abort();
  assert.equal(transport.calls[1].method, "request.cancel");
  assert.deepEqual(transport.calls[1].params, { requestId: submit.requestId });
  transport.reply(submit, undefined, { code: "plugin_commit_unknown", message: "核对数据" });
  await assert.rejects(request, error => error.code === "plugin_commit_unknown");
  assert.equal(transport.calls.filter(item => item.method === "mutation.submit").length, 1);
  client.dispose();
});

test("dispose reports an in-flight submit as an unknown commit without resending it", async () => {
  const transport = channel();
  const client = sdk.createSurfaceClient("surface-a", transport);
  const submitted = client.mutation.submit("plan-under-way");
  assert.equal(transport.calls[0].method, "mutation.submit");
  client.dispose();
  await assert.rejects(submitted, error => error.code === "plugin_commit_unknown");
  assert.equal(transport.calls.filter(item => item.method === "mutation.submit").length, 1);
  assert.equal(transport.calls.filter(item => item.method === "request.cancel").length, 0);
  assert.equal(transport.listeners.size, 0);
  await assert.rejects(client.mutation.submit("plan-after-close"), error => error.code === "plugin_surface_closed");
});

test("dispose keeps settled results and stays safe when repeated", async () => {
  const transport = channel();
  const client = sdk.createSurfaceClient("surface-a", transport);
  const done = client.mutation.submit("plan-done");
  const wire = transport.calls[0];
  transport.reply(wire, { status: "succeeded", taskId: "task-1" });
  assert.deepEqual(await done, { status: "succeeded", taskId: "task-1" });
  const reading = client.data.query({ contract: "vibetable.plugin-query.v2", collection: "orders", fields: ["id"] });
  client.dispose();
  await assert.rejects(reading, error => error.code === "plugin_surface_closed");
  const settled = transport.calls.length;
  client.dispose();
  assert.equal(transport.calls.length, settled);
  assert.equal(transport.listeners.size, 0);
});

test("parallel surface requests are bounded and dispose settles every outstanding call", async () => {
  const transport = channel();
  const client = sdk.createSurfaceClient("surface-a", transport);
  const calls = Array.from({ length: 8 }, () => client.context.read());
  const settled = Promise.allSettled(calls);
  await assert.rejects(client.context.read(), error => error.code === "plugin_concurrency_limit");
  client.dispose();
  assert.ok((await settled).every(result => result.status === "rejected" && result.reason.code === "plugin_surface_closed"));
});


test("SDK public root exports headless finish and addresses only the trusted parent", () => {
  assert.equal(typeof sdk.finishHeadlessRun, "function");
  const original = Object.getOwnPropertyDescriptor(globalThis, "parent");
  const messages = [];
  Object.defineProperty(globalThis, "parent", { configurable: true, value: { postMessage: (...args) => messages.push(args) } });
  try {
    sdk.finishHeadlessRun("owned-run", "succeeded", "complete");
    assert.deepEqual(messages, [[{ contract: "vibetable.plugin-surface.v1", surfaceToken: "owned-run", event: "close", payload: { status: "succeeded", summary: "complete" } }, "https://app.vibetable.local"]]);
  } finally {
    if (original) Object.defineProperty(globalThis, "parent", original);
    else delete globalThis.parent;
  }
});

test("surface delete serializes the exact guarded single-table plan without values or approval", async () => {
  const transport = channel();
  const client = sdk.createSurfaceClient("surface-delete", transport);
  const plan = { contract: "vibetable.surface-mutation-plan.v1", collection: "orders", schemaRevision: "schema_1",
    operations: [{ kind: "delete", primaryKey: "r1", expectedDigest: "opaque-query-guard" }] };
  const preview = client.mutation.preview(plan);
  const wire = transport.calls[0];
  assert.equal(wire.method, "mutation.preview");
  assert.deepEqual(JSON.parse(JSON.stringify(wire.params)), plan);
  assert.equal("values" in wire.params.operations[0], false);
  transport.reply(wire, { planId: "delete-plan", affectedCount: 1 });
  assert.deepEqual(await preview, { planId: "delete-plan", affectedCount: 1 });
  const submitted = client.mutation.submit("delete-plan");
  assert.deepEqual(transport.calls[1].params, { planId: "delete-plan" });
  transport.reply(transport.calls[1], { status: "succeeded", taskId: "delete-task" });
  assert.deepEqual(await submitted, { status: "succeeded", taskId: "delete-task" });
  client.dispose();
});
