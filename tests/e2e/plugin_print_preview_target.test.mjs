import test from "node:test";
import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import vm from "node:vm";
import fs from "node:fs/promises";
import { attachNativePreview, selectOwnedPreviewTarget } from "./plugin_print_preview_target.mjs";
import { waitForNativePrintPreview, cancelNativePrintPreview } from "./plugin_print_preview.mjs";
const target = { targetId: "actual-preview", browserContextId: "owned", type: "page", url: "edge://print/", attached: false };
function fixture(layout = "chromium") {
  const session = new EventEmitter(), calls = [];
  let current = { ...target }, detached = 0;
  const cancel = { disabled: layout === "edge-disabled", click: () => session.emit("Target.targetDestroyed", { targetId: target.targetId }) };
  const strip = { shadowRoot: { querySelectorAll: () => [cancel] } };
  const app = { shadowRoot: { querySelector: selector => selector === "print-preview-sidebar" ? { shadowRoot: { querySelector: () => strip } } : strip } };
  const footer = { shadowRoot: { querySelectorAll: selector => { assert.equal(selector, "fluent-button.cancel"); return [cancel]; } } };
  const sidebar = { shadowRoot: { querySelector: selector => selector === "footer-container" ? footer : null } };
  const edgePreview = { shadowRoot: { querySelector: selector => selector === "side-bar" ? sidebar : null } };
  const shell = { shadowRoot: { querySelector: selector => selector === "print-preview" ? edgePreview : null } };
  session.send = async (method, params) => {
    calls.push({ method, params });
    if (method === "Target.getTargets") return { targetInfos: [current] };
    if (method === "Target.getTargetInfo") return { targetInfo: current };
    if (method === "Target.attachToTarget") return { sessionId: "nested" };
    if (method === "Target.sendMessageToTarget") {
      const message = JSON.parse(params.message); let result = {};
      if (message.method === "Runtime.evaluate") result = { result: { value: await vm.runInNewContext(message.params.expression, { location: { href: current.url }, document: { querySelector: selector => layout === "both" ? (selector === "app-shell" ? shell : selector === "print-preview-app" ? app : null) : layout.startsWith("edge") ? (selector === "app-shell" ? shell : null) : (selector === "print-preview-app" ? app : null) } }) } };
      if (message.method === "Page.captureScreenshot") result = { data: Buffer.from("actual-target-png-protocol").toString("base64") };
      queueMicrotask(() => {
        session.emit("Target.receivedMessageFromTarget", { sessionId: "foreign", message: JSON.stringify({ id: message.id, result: { result: { value: "forged" } } }) });
        session.emit("Target.receivedMessageFromTarget", { sessionId: "nested", message: JSON.stringify({ id: message.id, result }) });
      });
    }
    return {};
  };
  session.detach = async () => { detached++; };
  const browser = { contexts: () => [{ pages: () => [] }], newBrowserCDPSession: async () => session };
  const page = { context: () => ({ pages: () => [], browser: () => browser, newCDPSession: async () => ({ send: async () => ({ targetInfo: { browserContextId: "owned" } }), detach: async () => {} }) }) };
  return { session, calls, page, current: value => { current = value; }, detached: () => detached };
}
test("target discovery is restricted to one actual preview page in the product browser context", () => {
  assert.equal(selectOwnedPreviewTarget([target, { ...target, type: "iframe" }, { ...target, browserContextId: "foreign" }], "owned"), target);
  assert.equal(selectOwnedPreviewTarget([{ ...target, url: "https://print/" }], "owned"), null);
  assert.throws(() => selectOwnedPreviewTarget([target], null), /context required/);
  assert.throws(() => selectOwnedPreviewTarget([target, { ...target, targetId: "second" }], "owned"), /ambiguous/);
});
test("WebView2 preview absent from Playwright pages is attached, evaluated and cancelled through its real target protocol", async () => {
  const discovery = process.env.VIBETABLE_QA_PRINT_DISCOVERY_MODULE
    ? (await import(process.env.VIBETABLE_QA_PRINT_DISCOVERY_MODULE)).waitForNativePrintPreview : waitForNativePrintPreview;
  const now = Date.now; let tick = 0;
  const f = fixture();
  let preview;
  try { Date.now = () => ++tick * 30001; preview = await discovery(f.page, "build"); }
  finally { Date.now = now; }
  try { assert.deepEqual(await preview.evaluate(() => ({ pageCount: 4, text: "actual-target" })), { pageCount: 4, text: "actual-target" }); } finally { await preview.dispose(); }
});
test("attached target reads preserve values, bind sessions, capture that target and observe actual Cancel closure", async () => {
  const f = fixture(), write = fs.writeFile, writes = [];
  const preview = await attachNativePreview(f.session, target);
  try {
    assert.deepEqual(await preview.evaluate(() => ({ pageCount: 4, text: "target-content" })), { pageCount: 4, text: "target-content" });
    fs.writeFile = async (path, data) => writes.push({ path, data: data.toString() });
    await preview.screenshot({ path: "preview.png" });
    assert.deepEqual(writes, [{ path: "preview.png", data: "actual-target-png-protocol" }]);
    await cancelNativePrintPreview(preview);
    assert.equal(preview.isClosed(), true);
    assert.ok(f.calls.some(call => call.method === "Target.attachToTarget" && call.params.targetId === target.targetId));
    assert.ok(f.calls.filter(call => call.method === "Target.sendMessageToTarget").every(call => call.params.sessionId === "nested"));
    assert.ok(!f.calls.some(call => JSON.stringify(call).includes("printToPDF")));
  } finally { fs.writeFile = write; await preview.dispose(); }
  assert.equal(f.detached(), 1);
  assert.equal(f.session.listenerCount("Target.receivedMessageFromTarget"), 0);
});
test("attached preview rejects target navigation to another origin or context", async () => {
  const f = fixture(), preview = await attachNativePreview(f.session, target);
  try {
    f.current({ ...target, url: "https://app.vibetable.local/" });
    await assert.rejects(preview.evaluate(() => "secret"), /identity changed/);
    f.current({ ...target, browserContextId: "foreign" });
    await assert.rejects(preview.screenshot({ path: "unused" }), /identity changed/);
  } finally { await preview.dispose(); }
});

test("disposing observation clears close waiters and detaches without closing or printing a target", async () => {
  const f = fixture(), preview = await attachNativePreview(f.session, target);
  const waiting = assert.rejects(preview.waitForEvent("close"), /observation detached/);
  await preview.dispose(); await waiting;
  assert.equal(f.session.listenerCount("Target.targetDestroyed"), 0);
  assert.equal(f.detached(), 1);
  assert.ok(f.calls.some(call => call.method === "Target.detachFromTarget"));
  assert.ok(!f.calls.some(call => call.method === "Target.closeTarget"));
});


test("failure screenshot command observes its own bounded capture deadline", async () => {
  const f = fixture(), send = f.session.send, write = fs.writeFile;
  f.session.send = async (method, params) => {
    if (method === "Target.sendMessageToTarget" && JSON.parse(params.message).method === "Page.captureScreenshot")
      await new Promise(resolve => setTimeout(resolve, 25));
    return send(method, params);
  };
  const preview = await attachNativePreview(f.session, target);
  fs.writeFile = async () => assert.fail("timed-out capture must not publish a screenshot");
  try { await assert.rejects(preview.screenshot({ path: "unused", timeout: 5 }), /native preview CDP deadline: Page.captureScreenshot/); }
  finally { fs.writeFile = write; await preview.dispose(); }
});


test("failure capture identity check shares the bounded screenshot budget", async () => {
  const f = fixture(), preview = await attachNativePreview(f.session, target), send = f.session.send;
  f.session.send = async (method, params) => {
    if (method === "Target.getTargetInfo") await new Promise(resolve => setTimeout(resolve, 25));
    return send(method, params);
  };
  try {
    await assert.rejects(preview.screenshot({ path: "unused", timeout: 5 }), /native preview identity deadline/);
    assert.ok(!f.calls.some(call => call.method === "Target.sendMessageToTarget" && JSON.parse(call.params.message).method === "Page.captureScreenshot"));
  } finally { await preview.dispose(); }
});


test("actual Edge154 footer cancellation targets only fluent-button.cancel", async () => {
  const f = fixture("edge"), preview = await attachNativePreview(f.session, target);
  try { await cancelNativePrintPreview(preview); assert.equal(preview.isClosed(), true); }
  finally { await preview.dispose(); }
});


test("Edge Cancel refuses disabled controls and ambiguous native roots", async () => {
  for (const layout of ["edge-disabled", "both"]) {
    const f = fixture(layout), preview = await attachNativePreview(f.session, target);
    try {
      await assert.rejects(cancelNativePrintPreview(preview), /one (enabled native Cancel|native print preview Cancel button) required/);
      assert.equal(preview.isClosed(), false);
    } finally { await preview.dispose(); }
    assert.equal(f.detached(), 1);
    assert.equal(f.session.listenerCount("Target.targetDestroyed"), 0);
  }
});
