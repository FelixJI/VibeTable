import test from "node:test";
import assert from "node:assert/strict";
import { assertSidebarPluginReachable, commitInspectedApplicationUpgrade, installPluginUpgradeCaptureInPage } from "./plugin_applications.mjs";
import { captureCompletedInPage, waitForCapturedBridgeMessage } from "./bridge_capture_wait.mjs";

const plan = { planId: "lease", projectKey: "local:workspace", projectRevision: "revision",
  packageHash: "package-v2", manifest: { pluginId: "application", version: "2.0.0" } };
const request = { type: "plugin.lifecycle.upgrade", requestId: "approved-upgrade", payload: {
  projectKey: plan.projectKey, projectRevision: plan.projectRevision, pluginId: plan.manifest.pluginId, planId: plan.planId } };
const updated = { pluginId: plan.manifest.pluginId, projectKey: plan.projectKey, version: "2.0.0", packageHash: plan.packageHash };

test("renderer recovery waits for the installed application projection before checking reachability", async () => {
  let reveal;
  const projected = new Promise(resolve => { reveal = resolve; });
  let visible = false;
  const observed = [];
  const page = { getByTestId(id) { return {
    async waitFor(options) {
      assert.equal(options.state, "visible");
      assert.equal(options.timeout, 60000);
      if (id.startsWith("nav-plugin-app-")) await projected;
    },
    async isVisible() { return id === "nav-plugins" || visible; },
  }; } };
  const waiting = assertSidebarPluginReachable(page, { check(...args) { observed.push(args); } }, "recovered");
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(observed, []);
  visible = true; reveal();
  await waiting;
  assert.deepEqual(observed, [["recovered", true, {}]]);
});

test("renderer recovery cannot pass when its installed application never becomes visible", async () => {
  const missing = new Error("installed application projection timed out");
  const page = { getByTestId(id) { return {
    async waitFor() { if (id.startsWith("nav-plugin-app-")) throw missing; },
    async isVisible() { return id === "nav-plugins"; },
  }; } };
  await assert.rejects(assertSidebarPluginReachable(page, {
    check() { assert.fail("an unavailable application must not reach the successful check"); },
  }, "recovered"), error => error === missing);
});

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
