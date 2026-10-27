import test from "node:test";
import assert from "node:assert/strict";
import { assertSidebarPluginReachable, commitInspectedApplicationUpgrade, installPluginUpgradeCaptureInPage } from "./plugin_applications.mjs";
import { captureCompletedInPage, waitForCapturedBridgeMessage } from "./bridge_capture_wait.mjs";

test("recovered shell waits for the catalog-backed sidebar application before asserting reachability", async () => {
  let visible = false;
  let projectCatalog;
  const catalogReady = new Promise(resolve => { projectCatalog = () => { visible = true; resolve(); }; });
  const observations = [];
  const page = { getByTestId(id) {
    assert.ok(id === "nav-plugins" || id === "nav-plugin-app-com.vibetable.e2e.application-a/open-app");
    return {
      async isVisible() { return id === "nav-plugins" || visible; },
      async waitFor() { if (id !== "nav-plugins") await catalogReady; },
    };
  } };
  const recorder = { check(name, passed) {
    observations.push({ name, passed });
    assert.equal(passed, true);
  } };
  let failure;
  const waiting = assertSidebarPluginReachable(page, recorder, "renderer recovery")
    .catch(error => { failure = error; });
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(observations, [], "static navigation is not catalog readiness");
  projectCatalog();
  await waiting;
  assert.equal(failure, undefined);
  assert.deepEqual(observations, [{ name: "renderer recovery", passed: true }]);
});

test("sidebar recovery readiness propagates a missing application instead of accepting the static shell", async () => {
  const failure = new Error("application entry did not become visible");
  const page = { getByTestId(id) { return { async waitFor() { throw failure; }, async isVisible() { return id === "nav-plugins"; } }; } };
  let checks = 0;
  await assert.rejects(assertSidebarPluginReachable(page, { check() { checks++; } }, "renderer recovery"),
    error => error === failure);
  assert.equal(checks, 0);
});

const plan = { planId: "lease", projectKey: "local:workspace", projectRevision: "revision",
  packageHash: "package-v2", manifest: { pluginId: "application", version: "2.0.0" } };
const request = { type: "plugin.lifecycle.upgrade", requestId: "approved-upgrade", payload: {
  projectKey: plan.projectKey, projectRevision: plan.projectRevision, pluginId: plan.manifest.pluginId, planId: plan.planId } };
const updated = { pluginId: plan.manifest.pluginId, projectKey: plan.projectKey, version: "2.0.0", packageHash: plan.packageHash };
function fixture() {
  const listeners = new Set(); const outbound = []; let predicate; let argument; let resolveWait;
  let reads = 0; let hiddenChecks = 0;
  const webview = { postMessage(message) { outbound.push(message); },
    addEventListener(_type, callback) { listeners.add(callback); }, removeEventListener(_type, callback) { listeners.delete(callback); } };
  globalThis.window = { chrome: { webview } };
  const originalPost = webview.postMessage;
  const page = { async evaluate(callback, value) { return callback(value); },
    waitForFunction(callback, value) { predicate = callback; argument = value; return callback(value) ? Promise.resolve() : new Promise(resolve => { resolveWait = resolve; }); },
    getByTestId(id) { return { async click() { assert.equal(id, "plugin-install-commit"); webview.postMessage(request); },
      async waitFor() { assert.equal(id, "plugin-install-plan"); hiddenChecks++; } }; } };
  const helpers = { waitForCapturedBridgeMessage, async rawBridgeRequest() { reads++; return { payload: [updated] }; } };
  return { page, helpers, webview, outbound, originalPost, listeners, reads: () => reads, hiddenChecks: () => hiddenChecks,
    dispatch(message) { for (const callback of [...listeners]) callback({ data: message }); if (predicate?.(argument)) resolveWait?.(); } };
}

test("approved upgrade waits for its actual terminal before an authority read, even when the plan hides immediately", async () => {
  const f = fixture();
  try {
    const waiting = commitInspectedApplicationUpgrade(f.page, plan, f.helpers);
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(f.reads(), 0); assert.equal(f.hiddenChecks(), 0);
    f.dispatch({ type: "operation.failed", requestId: "other-request", payload: { code: "failure" } });
    f.dispatch({ type: "plugin.lifecycle.upgrade", requestId: "old-upgrade", payload: updated });
    assert.equal(captureCompletedInPage(`plugin-upgrade/${plan.planId}`), false);
    f.dispatch({ type: "plugin.lifecycle.upgrade", requestId: request.requestId, payload: updated });
    assert.deepEqual(await waiting, updated);
    assert.equal(f.reads(), 1); assert.equal(f.hiddenChecks(), 1); assert.equal(f.outbound.length, 1);
    assert.equal(f.listeners.size, 0); assert.equal(f.webview.postMessage, f.originalPost);
  } finally { delete globalThis.window; }
});

test("matched upgrade failure is preserved without retrying the write or reading an old catalog", async () => {
  const f = fixture();
  try {
    const waiting = commitInspectedApplicationUpgrade(f.page, plan, f.helpers);
    await new Promise(resolve => setImmediate(resolve));
    f.dispatch({ type: "operation.failed", requestId: request.requestId, payload: { code: "UPGRADE_FAILED", message: "keep current installation" } });
    await assert.rejects(waiting, /UPGRADE_FAILED: keep current installation/);
    assert.equal(f.reads(), 0); assert.equal(f.outbound.length, 1);
    assert.equal(f.listeners.size, 0); assert.equal(f.webview.postMessage, f.originalPost);
  } finally { delete globalThis.window; }
});

test("a different lease or scope cannot provide the upgrade terminal", () => {
  const f = fixture();
  try {
    installPluginUpgradeCaptureInPage(plan);
    for (const payload of [{ ...request.payload, planId: "other-lease" },
      { ...request.payload, projectKey: "local:other" }]) f.webview.postMessage({ ...request, payload });
    f.dispatch({ type: "plugin.lifecycle.upgrade", requestId: request.requestId, payload: updated });
    assert.equal(captureCompletedInPage(`plugin-upgrade/${plan.planId}`), false);
    window.__vibetableE2EBridgeCapture.release();
  } finally { delete globalThis.window; }
});


test("a terminal with a different package cannot trigger an authority read", async () => {
  const f = fixture();
  try {
    const waiting = commitInspectedApplicationUpgrade(f.page, plan, f.helpers);
    await new Promise(resolve => setImmediate(resolve));
    f.dispatch({ type: "plugin.lifecycle.upgrade", requestId: request.requestId, payload: { ...updated, packageHash: "old-package" } });
    await assert.rejects(waiting, /complete package/);
    assert.equal(f.reads(), 0); assert.equal(f.outbound.length, 1);
  } finally { delete globalThis.window; }
});

test("a missing terminal releases its capture without repeating the write", async () => {
  const f = fixture();
  try {
    f.page.waitForFunction = async () => { throw Object.assign(new Error("not settled"), { name: "TimeoutError" }); };
    await assert.rejects(commitInspectedApplicationUpgrade(f.page, plan, f.helpers), /response timed out/);
    assert.equal(f.reads(), 0); assert.equal(f.outbound.length, 1);
    assert.equal(f.listeners.size, 0); assert.equal(f.webview.postMessage, f.originalPost);
  } finally { delete globalThis.window; }
});
