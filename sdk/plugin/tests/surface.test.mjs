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

function invalidation(transport, id, sequence, extra = {}) {
  for (const listener of transport.listeners) listener({ data: { contract: sdk.SURFACE_API_CONTRACT,
    surfaceToken: "surface-a", event: "data.invalidated", subscriptionId: id, collection: "orders", sequence, ...extra } });
}

test("subscriptions consume only ordered table invalidations and unsubscribe drops late events", async () => {
  const transport = channel();
  const client = sdk.createSurfaceClient("surface-a", transport);
  const events = [];
  const subscribing = client.data.subscribe({ collection: "orders" }, event => events.push(event));
  const wire = transport.calls[0];
  assert.equal(wire.method, "data.subscribe");
  assert.deepEqual(wire.params, { collection: "orders" });
  invalidation(transport, "sub-1", 2); // The notification may beat its subscribe receipt.
  invalidation(transport, "sub-1", 1);
  transport.reply(wire, { subscriptionId: "sub-1" });
  assert.deepEqual(await subscribing, { subscriptionId: "sub-1" });
  invalidation(transport, "sub-1", 2);
  invalidation(transport, "sub-1", 1);
  invalidation(transport, "sub-1", 3, { surfaceToken: "another-frame" });
  invalidation(transport, "sub-1", 3, { collection: "hidden" });
  invalidation(transport, "sub-1", 3, { recordIds: ["must-not-escape"] });
  assert.deepEqual(events, [{ type: "invalidated", collection: "orders", sequence: 2 },
    { type: "invalidated", collection: "orders", sequence: 3 }]);
  const unsubscribed = client.data.unsubscribe("sub-1");
  const unsubscribe = transport.calls.at(-1);
  assert.equal(unsubscribe.method, "data.unsubscribe");
  invalidation(transport, "sub-1", 4);
  transport.reply(unsubscribe, { unsubscribed: true });
  await unsubscribed;
  assert.equal(events.length, 2);
  client.dispose();
});

test("closed subscriptions cannot be revived by out-of-order events, including before receipt", async () => {
  const transport = channel();
  const client = sdk.createSurfaceClient("surface-a", transport);
  const events = [];
  const subscribing = client.data.subscribe({ collection: "orders" }, event => events.push(event));
  for (const listener of transport.listeners) listener({ data: { contract: sdk.SURFACE_API_CONTRACT,
    surfaceToken: "surface-a", event: "data.subscription.closed", subscriptionId: "gone", code: "plugin_read_denied" } });
  invalidation(transport, "gone", 5);
  transport.reply(transport.calls[0], { subscriptionId: "gone" });
  await subscribing;
  invalidation(transport, "gone", 6);
  assert.deepEqual(events, [{ type: "closed", code: "plugin_read_denied" }]);
  client.dispose();
});

test("abort and dispose release subscriptions and cancel an in-flight allocation without replay", async () => {
  const transport = channel();
  const client = sdk.createSurfaceClient("surface-a", transport);
  const abort = new AbortController();
  const subscribed = client.data.subscribe({ collection: "orders" }, () => assert.fail("retired callback"), abort.signal);
  transport.reply(transport.calls[0], { subscriptionId: "active" });
  await subscribed;
  abort.abort();
  assert.deepEqual(transport.calls.at(-1).params, { subscriptionId: "active" });
  assert.equal(transport.calls.at(-1).method, "data.unsubscribe");
  invalidation(transport, "active", 1);
  const another = client.data.subscribe({ collection: "orders" }, () => {});
  transport.reply(transport.calls.at(-1), { subscriptionId: "dispose-owned" });
  await another;
  const pending = client.data.subscribe({ collection: "orders" }, () => {});
  const allocation = transport.calls.at(-1);
  client.dispose();
  await assert.rejects(pending, error => error.code === "plugin_surface_closed");
  assert.ok(transport.calls.some(call => call.method === "data.unsubscribe" && call.params.subscriptionId === "dispose-owned"));
  assert.ok(transport.calls.some(call => call.method === "request.cancel" && call.params.requestId === allocation.requestId));
  const count = transport.calls.length;
  client.dispose();
  assert.equal(transport.calls.length, count);
  assert.equal(transport.listeners.size, 0);
});

test("surface subscription capacity matches the shared contract and callbacks do not corrupt transport", async () => {
  const { readFile } = await import("node:fs/promises");
  const catalog = JSON.parse(await readFile(new URL("../../../contracts/plugin-surface-api.v1.json", import.meta.url), "utf8"));
  assert.ok(catalog.methods.includes("data.subscribe") && catalog.methods.includes("data.unsubscribe"));
  assert.equal(catalog.methods.includes("data.authorizeSubscription"), false);
  assert.equal(catalog.limits.subscriptionsPerSurface, 8);
  assert.equal(catalog.limits.subscriptionsTotal, 64);
  const transport = channel();
  const client = sdk.createSurfaceClient("surface-a", transport);
  for (let i = 0; i < 8; i++) {
    const pending = client.data.subscribe({ collection: "orders" }, () => { throw new Error("consumer bug"); });
    transport.reply(transport.calls.at(-1), { subscriptionId: `sub-${i}` });
    await pending;
  }
  assert.doesNotThrow(() => invalidation(transport, "sub-1", 1));
  await assert.rejects(client.data.subscribe({ collection: "orders" }, () => {}), error => error.code === "plugin_subscription_limit");
  client.dispose();
  assert.equal(transport.calls.filter(call => call.method === "data.unsubscribe").length, 8);
});
test("explicit unsubscribe remains reachable with eight normal requests and coalesces duplicates", async () => {
  const transport = channel();
  const client = sdk.createSurfaceClient("surface-a", transport);
  const notices = [];
  const allocated = client.data.subscribe({ collection: "orders" }, event => notices.push(event));
  transport.reply(transport.calls.at(-1), { subscriptionId: "owned" });
  await allocated;
  const reads = Array.from({ length: 8 }, () => client.context.read());
  const readWires = transport.calls.slice(-8);
  try {
    const first = client.data.unsubscribe("owned");
    const firstOutcome = first.then(() => null, error => error);
    const wire = transport.calls.at(-1);
    assert.equal(wire.method, "data.unsubscribe");
    const callCount = transport.calls.length;
    const duplicate = client.data.unsubscribe("owned");
    assert.equal(transport.calls.length, callCount);
    await client.data.unsubscribe("unknown");
    assert.equal(transport.calls.length, callCount);
    invalidation(transport, "owned", 1);
    assert.deepEqual(notices, []);
    transport.reply(wire, { unsubscribed: true });
    assert.equal(await firstOutcome, null);
    await duplicate;
    await client.data.unsubscribe("owned");
    assert.equal(transport.calls.length, callCount);
  } finally {
    for (const wire of readWires) transport.reply(wire, {});
    await Promise.allSettled(reads);
    client.dispose();
  }
});
test("eight owned release acknowledgements are bounded independently from eight normal requests", async () => {
  const transport = channel();
  const client = sdk.createSurfaceClient("surface-a", transport);
  for (let i = 0; i < 8; i++) {
    const allocating = client.data.subscribe({ collection: "orders" }, () => {});
    transport.reply(transport.calls.at(-1), { subscriptionId: `owned-${i}` });
    await allocating;
  }
  const reads = Array.from({ length: 8 }, () => client.context.read());
  const readWires = transport.calls.slice(-8);
  const releases = Array.from({ length: 8 }, (_, i) => client.data.unsubscribe(`owned-${i}`));
  const releaseWires = transport.calls.slice(-8);
  try {
    assert.ok(releaseWires.every(wire => wire.method === "data.unsubscribe"));
    const count = transport.calls.length;
    await assert.rejects(client.context.read(), error => error.code === "plugin_concurrency_limit");
    // Ownership remains reserved until Host acknowledges, so allocating new handles cannot grow release state.
    await assert.rejects(client.data.subscribe({ collection: "orders" }, () => {}), error => error.code === "plugin_subscription_limit");
    for (let i = 0; i < 100; i++) await client.data.unsubscribe(`unknown-${i}`);
    const duplicate = client.data.unsubscribe("owned-0");
    assert.equal(transport.calls.length, count);
    for (const wire of releaseWires) transport.reply(wire, { unsubscribed: true });
    await Promise.all([...releases, duplicate]);
    for (const wire of readWires) transport.reply(wire, {});
    await Promise.all(reads);
  } finally {
    client.dispose();
    await Promise.allSettled([...releases, ...reads]);
  }
});

test("failed release sends retain ownership for explicit retry while keeping callbacks stopped", async () => {
  const transport = channel();
  const send = transport.postMessage;
  let failRelease = true;
  transport.postMessage = wire => {
    if (wire.method === "data.unsubscribe" && failRelease) throw new Error("synthetic send failure");
    send(wire);
  };
  const client = sdk.createSurfaceClient("surface-a", transport);
  for (let i = 0; i < 8; i++) {
    const allocating = client.data.subscribe({ collection: "orders" }, () => assert.fail("stopped listener"));
    transport.reply(transport.calls.at(-1), { subscriptionId: `owned-${i}` });
    await allocating;
  }
  await assert.rejects(client.data.unsubscribe("owned-0"), error => error.code === "plugin_transport_failed");
  await assert.rejects(client.data.subscribe({ collection: "orders" }, () => {}), error => error.code === "plugin_subscription_limit");
  invalidation(transport, "owned-0", 1);
  failRelease = false;
  const retry = client.data.unsubscribe("owned-0");
  const wire = transport.calls.at(-1);
  assert.equal(wire.method, "data.unsubscribe");
  assert.deepEqual(wire.params, { subscriptionId: "owned-0" });
  transport.reply(wire, { unsubscribed: true });
  await retry;
  const count = transport.calls.length;
  await client.data.unsubscribe("owned-0");
  assert.equal(transport.calls.length, count);
  client.dispose();
});