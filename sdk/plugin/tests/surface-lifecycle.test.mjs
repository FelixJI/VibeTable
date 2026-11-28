import assert from "node:assert/strict";
import test from "node:test";
import { sdk, testing } from "./load-sdk.mjs";

const CONTRACT = "vibetable.plugin-surface-lifecycle.v1";
assert.equal(sdk.SURFACE_LIFECYCLE_CONTRACT, CONTRACT);

function parentChannel() {
  const listeners = new Set();
  const parent = { postMessage: message => calls.push(message) };
  const calls = [];
  return { calls, parent, addEventListener: (_, f) => listeners.add(f), removeEventListener: (_, f) => listeners.delete(f),
    emit: (event, payload, overrides = {}) => { for (const f of listeners) f({ origin: "https://app.vibetable.local", source: parent,
      data: { contract: "vibetable.plugin-surface.v1", surfaceToken: "s1", event, payload }, ...overrides }); }, listeners };
}
function native() {
  const listeners = new Set(); const calls = [];
  return { calls, postMessage: message => calls.push(message), addEventListener: (_, f) => listeners.add(f), removeEventListener: (_, f) => listeners.delete(f),
    reply(request, result) { for (const f of listeners) f({ data: { contract: sdk.SURFACE_API_CONTRACT, requestId: request.requestId, result } }); }, listeners };
}
const context = (surfacePhase, extra = {}) => ({ contract: "vibetable.command-context.v1", projectKey: "workspace-owned", collection: "authorized",
  selectedKeys: [], querySnapshot: null, locale: "zh-CN", theme: "light", density: "comfortable", user: { role: "reader" }, hostVersion: "1.0.0", ...extra, ...(surfacePhase === undefined ? {} : { surfacePhase }) });
const quiet = { postMessage() {}, addEventListener() {}, removeEventListener() {} };
const theme = { contract: "vibetable.plugin-theme.v1", locale: "en-US", mode: "dark", density: "compact", variables: {} };
const turn = () => new Promise(resolve => setImmediate(resolve));

test("trusted lifecycleChanged announces each phase change once, replays the current phase and never rewinds the foreground", () => {
  const parent = parentChannel();
  const client = sdk.createSurfaceClient("s1", quiet, parent);
  assert.equal(client.lifecycle.current(), undefined);
  const seen = [];
  const stop = client.lifecycle.onChanged(phase => seen.push(phase));
  assert.deepEqual(seen, []);
  parent.emit("lifecycleChanged", { phase: "background" }, { origin: "https://evil.test" });
  parent.emit("lifecycleChanged", { phase: "background" }, { source: {} });
  parent.emit("lifecycleChanged", { phase: "background" }, { data: { contract: "vibetable.plugin-surface.v1", surfaceToken: "other", event: "lifecycleChanged", payload: { phase: "background" } } });
  parent.emit("lifecycleChanged", { phase: "sideways" });
  assert.equal(client.lifecycle.current(), undefined);
  parent.emit("lifecycleChanged", { phase: "background" });
  parent.emit("lifecycleChanged", { phase: "background" });
  parent.emit("lifecycleChanged", { phase: "foreground" });
  assert.deepEqual(seen, ["background", "foreground"]);
  assert.equal(client.lifecycle.current(), "foreground");
  stop();
  const late = [];
  client.lifecycle.onChanged(phase => late.push(phase));
  assert.deepEqual(late, ["foreground"]);
  // A session is only promoted: the Host never demotes, and a stale announcement cannot rewind it.
  parent.emit("lifecycleChanged", { phase: "background" });
  assert.deepEqual(late, ["foreground"]);
  assert.equal(client.lifecycle.current(), "foreground");
  client.dispose();
  assert.equal(parent.listeners.size, 0);
  assert.throws(() => client.lifecycle.onChanged(() => {}), error => error.code === "plugin_surface_closed");
});

test("context.read normalizes the Host phase (missing means a Host that never pre-warms) and adopts it into the lifecycle", async () => {
  for (const [wire, expected] of [[undefined, "foreground"], ["background", "background"], ["foreground", "foreground"], ["unknown", "foreground"]]) {
    const parent = parentChannel(); const host = native(); let presentation = 0;
    const client = sdk.createSurfaceClient("s1", host, parent);
    parent.parent.postMessage = message => { if (message.payload?.method === "context.current") { presentation++; parent.emit("uiResponse", { requestId: message.payload.requestId, result: theme }); } };
    const seen = [];
    client.lifecycle.onChanged(phase => seen.push(phase));
    const reading = client.context.read();
    assert.equal(host.calls.at(-1).method, "context.read");
    host.reply(host.calls.at(-1), context(wire));
    const value = await reading;
    assert.equal(value.surfacePhase, expected);
    assert.equal(value.collection, "authorized");
    assert.equal(value.locale, "en-US");
    assert.equal(presentation, 1);
    assert.equal(client.lifecycle.current(), expected);
    assert.deepEqual(seen, [expected]);
    client.dispose();
  }
});

test("a context.read reply that arrives after the promotion never rewinds the announced phase", async () => {
  const parent = parentChannel(); const host = native();
  const client = sdk.createSurfaceClient("s1", host, parent);
  parent.parent.postMessage = message => { if (message.payload?.method === "context.current") parent.emit("uiResponse", { requestId: message.payload.requestId, result: theme }); };
  const seen = [];
  client.lifecycle.onChanged(phase => seen.push(phase));
  const reading = client.context.read();
  host.reply(host.calls.at(-1), context("background"));
  assert.equal((await reading).surfacePhase, "background");
  parent.emit("lifecycleChanged", { phase: "foreground" });
  const stale = client.context.read();
  host.reply(host.calls.at(-1), context("background"));
  assert.equal((await stale).surfacePhase, "foreground", "The reply was already in flight when the surface was promoted.");
  assert.deepEqual(seen, ["background", "foreground"]);
  assert.equal(client.lifecycle.current(), "foreground");
  client.dispose();
});

test("whenForeground resolves immediately in the foreground, waits through the background and honours cancellation and disposal", async () => {
  const parent = parentChannel();
  const promoted = sdk.createSurfaceClient("s1", quiet, parent);
  parent.emit("lifecycleChanged", { phase: "foreground" });
  assert.equal(await promoted.lifecycle.whenForeground(), "foreground");
  promoted.dispose();
  const client = sdk.createSurfaceClient("s1", quiet, parent);
  parent.emit("lifecycleChanged", { phase: "background" });
  const waiting = client.lifecycle.whenForeground();
  let settled = false; waiting.then(() => { settled = true; });
  await turn();
  assert.equal(settled, false);
  const abort = new AbortController();
  const cancelled = client.lifecycle.whenForeground(abort.signal);
  abort.abort();
  await assert.rejects(cancelled, error => error.code === "plugin_cancel_requested");
  parent.emit("lifecycleChanged", { phase: "foreground" });
  assert.equal(await waiting, "foreground");
  const hidden = sdk.createSurfaceClient("s1", quiet, parent);
  parent.emit("lifecycleChanged", { phase: "background" });
  const abandoned = hidden.lifecycle.whenForeground();
  hidden.dispose();
  await assert.rejects(abandoned, error => error.code === "plugin_surface_closed");
  await assert.rejects(hidden.lifecycle.whenForeground(), error => error.code === "plugin_surface_closed");
  client.dispose();
  assert.equal(parent.listeners.size, 0);
});

test("whenForeground asks the Host for the phase first, so a Host that never pre-warms resolves instead of hanging", async () => {
  const host = native();
  const client = sdk.createSurfaceClient("s1", host);
  const waiting = client.lifecycle.whenForeground();
  await Promise.resolve();
  assert.equal(host.calls.at(-1).method, "context.read");
  host.reply(host.calls.at(-1), context(undefined));
  assert.equal(await waiting, "foreground");
  assert.equal(client.lifecycle.current(), "foreground");
  // A Host that did announce a background phase needs no extra read.
  let reads = host.calls.length;
  const again = client.lifecycle.whenForeground();
  await turn();
  assert.equal(host.calls.length, reads);
  host.reply(host.calls.at(-1), context("foreground"));
  assert.equal(await again, "foreground");
  // A refused Host read propagates its own receipt instead of guessing a phase.
  client.dispose();
  const fresh = sdk.createSurfaceClient("s1", host);
  const refused = fresh.lifecycle.whenForeground();
  await Promise.resolve();
  for (const f of host.listeners) f({ data: { contract: sdk.SURFACE_API_CONTRACT, requestId: host.calls.at(-1).requestId, error: { code: "plugin_source_denied", message: "denied" } } });
  await assert.rejects(refused, error => error.code === "plugin_source_denied");
  assert.equal(fresh.lifecycle.current(), undefined);
  fresh.dispose();
});

test("whenForeground cancels immediately while the phase read is still in flight and drops the late reply", async () => {
  const host = native();
  const client = sdk.createSurfaceClient("s1", host);
  const abort = new AbortController();
  const waiting = client.lifecycle.whenForeground(abort.signal);
  await Promise.resolve();
  assert.equal(host.calls.at(-1).method, "context.read");
  abort.abort();
  // Rejected before the Host answered: the wait never lingers on the in-flight read.
  await assert.rejects(waiting, error => error.code === "plugin_cancel_requested");
  const reads = host.calls.length;
  abort.abort();
  await assert.rejects(client.lifecycle.whenForeground(abort.signal), error => error.code === "plugin_cancel_requested");
  assert.equal(host.calls.length, reads, "An already cancelled wait reads nothing.");
  // The late reply settles the read quietly: the truthful phase is adopted and nothing is left unhandled.
  host.reply(host.calls.at(-1), context("background"));
  await turn();
  assert.equal(client.lifecycle.current(), "background");
  client.dispose();
});

test("readyToActivate announces application readiness once, only after the Host announced the lifecycle contract", () => {
  const parent = parentChannel();
  const client = sdk.createSurfaceClient("s1", quiet, parent);
  const signals = () => parent.calls.filter(message => message.event === "readyToActivate");
  // A Host announcement alone never posts: the application has to ask for it.
  parent.emit("lifecycleChanged", { phase: "background" });
  assert.equal(signals().length, 0);
  client.lifecycle.readyToActivate();
  client.lifecycle.readyToActivate();
  parent.emit("lifecycleChanged", { phase: "foreground" });
  assert.deepEqual(signals(), [{ contract: "vibetable.plugin-surface.v1", surfaceToken: "s1", event: "readyToActivate", payload: {} }]);
  // Asked before the Host announced: deferred until the first trusted announcement, then posted once.
  const early = sdk.createSurfaceClient("s1", quiet, parent);
  early.lifecycle.readyToActivate();
  assert.equal(signals().length, 1);
  parent.emit("lifecycleChanged", { phase: "background" }, { origin: "https://evil.test" });
  parent.emit("lifecycleChanged", { phase: "background" }, { source: {} });
  assert.equal(signals().length, 1, "An untrusted announcement does not open the channel");
  parent.emit("lifecycleChanged", { phase: "background" });
  early.lifecycle.readyToActivate();
  parent.emit("lifecycleChanged", { phase: "foreground" });
  assert.equal(signals().length, 2, "Idempotent across phases");
  early.dispose();
  early.lifecycle.readyToActivate();
  assert.equal(signals().length, 2, "A closed surface never posts");
  client.dispose();
  assert.equal(parent.listeners.size, 0);
});

test("readyToActivate stays silent on a Host that never announces the lifecycle contract and without a parent channel", async () => {
  const parent = parentChannel(); const host = native();
  const client = sdk.createSurfaceClient("s1", host, parent);
  parent.parent.postMessage = message => { if (message.payload?.method === "context.current") parent.emit("uiResponse", { requestId: message.payload.requestId, result: theme }); };
  client.lifecycle.readyToActivate();
  // context.read adopts a phase without a lifecycle announcement: an older Host must never receive an unknown event.
  const reading = client.context.read();
  host.reply(host.calls.at(-1), context(undefined));
  await reading;
  assert.equal(client.lifecycle.current(), "foreground");
  assert.equal(parent.calls.filter(message => message.event === "readyToActivate").length, 0);
  client.dispose();
  const offline = sdk.createSurfaceClient("s1", native());
  assert.doesNotThrow(() => offline.lifecycle.readyToActivate());
  offline.dispose();
});

test("context.read carries the phase as of its return when the promotion lands during the presentation read", async () => {
  const parent = parentChannel(); const host = native();
  const client = sdk.createSurfaceClient("s1", host, parent);
  let held;
  parent.parent.postMessage = message => { if (message.payload?.method === "context.current") held = message.payload.requestId; };
  parent.emit("lifecycleChanged", { phase: "background" });
  const reading = client.context.read();
  host.reply(host.calls.at(-1), context("background"));
  await turn();
  assert.equal(typeof held, "string");
  parent.emit("lifecycleChanged", { phase: "foreground" });
  parent.emit("uiResponse", { requestId: held, result: theme });
  const value = await reading;
  assert.equal(value.surfacePhase, "foreground");
  assert.equal(value.locale, "en-US");
  assert.equal(value.theme, "dark");
  client.dispose();
});

test("offline Host defaults to the foreground phase and lets a test model a pre-warmed background surface", async () => {
  for (const [options, expected] of [[{}, "foreground"], [{ surfacePhase: "background" }, "background"]]) {
    const host = testing.createOfflineSurfaceHost({ pluginId: "com.acme.lifecycle", workspaceId: "ws", surfaceToken: "token", context: options });
    const client = sdk.createSurfaceClient("token", host.transport);
    assert.equal((await client.context.read()).surfacePhase, expected);
    assert.equal(client.lifecycle.current(), expected);
    if (expected === "foreground") assert.equal(await client.lifecycle.whenForeground(), "foreground");
    client.dispose(); host.retire();
  }
});
