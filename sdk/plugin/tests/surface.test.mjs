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

test("parallel surface requests are bounded and dispose settles every outstanding call", async () => {
  const transport = channel();
  const client = sdk.createSurfaceClient("surface-a", transport);
  const calls = Array.from({ length: 8 }, () => client.context.read());
  const settled = Promise.allSettled(calls);
  await assert.rejects(client.context.read(), error => error.code === "plugin_concurrency_limit");
  client.dispose();
  assert.ok((await settled).every(result => result.status === "rejected" && result.reason.code === "plugin_surface_closed"));
});
