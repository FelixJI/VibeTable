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

test("trusted lifecycleChanged announces the phase once per change and replays the current phase to new listeners", () => {
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
  parent.emit("lifecycleChanged", { phase: "background" });
  assert.deepEqual(late, ["foreground", "background"]);
  assert.deepEqual(seen, ["background", "foreground"]);
  client.dispose();
  assert.equal(parent.listeners.size, 0);
  assert.throws(() => client.lifecycle.onChanged(() => {}), error => error.code === "plugin_surface_closed");
});

test("context.read normalizes the Host phase (missing means a Host that never pre-warms) and adopts it into the lifecycle", async () => {
  for (const [wire, expected] of [[undefined, "foreground"], ["background", "background"], ["foreground", "foreground"], ["unknown", "foreground"]]) {
    const parent = parentChannel(); const host = native(); let presentation = 0;
    const client = sdk.createSurfaceClient("s1", host, parent);
    parent.parent.postMessage = message => { if (message.payload?.method === "context.current") { presentation++; parent.emit("uiResponse", { requestId: message.payload.requestId, result: { contract: "vibetable.plugin-theme.v1", locale: "en-US", mode: "dark", density: "compact", variables: {} } }); } };
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

test("whenForeground resolves immediately in the foreground, waits through the background and honours cancellation and disposal", async () => {
  const parent = parentChannel();
  const client = sdk.createSurfaceClient("s1", quiet, parent);
  parent.emit("lifecycleChanged", { phase: "foreground" });
  assert.equal(await client.lifecycle.whenForeground(), "foreground");
  parent.emit("lifecycleChanged", { phase: "background" });
  const waiting = client.lifecycle.whenForeground();
  let settled = false; waiting.then(() => { settled = true; });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(settled, false);
  const abort = new AbortController();
  const cancelled = client.lifecycle.whenForeground(abort.signal);
  abort.abort();
  await assert.rejects(cancelled, error => error.code === "plugin_cancel_requested");
  parent.emit("lifecycleChanged", { phase: "foreground" });
  assert.equal(await waiting, "foreground");
  parent.emit("lifecycleChanged", { phase: "background" });
  const abandoned = client.lifecycle.whenForeground();
  client.dispose();
  await assert.rejects(abandoned, error => error.code === "plugin_surface_closed");
  await assert.rejects(client.lifecycle.whenForeground(), error => error.code === "plugin_surface_closed");
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
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(host.calls.length, reads);
  reads = host.calls.length;
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
