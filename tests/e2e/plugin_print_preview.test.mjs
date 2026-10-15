import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import { assertNativePrintSnapshot, cancelNativePrintPreview, nativePrintPreviewPages, readNativePrintPreviewInPage, waitForNativePrintPreview } from "./plugin_print_preview.mjs";

const report = "synthetic authorized print row 0\nsynthetic authorized print row 99";
test("actual preview snapshot requires multiple pages, first/last row and excludes parent secrets", () => {
  assert.doesNotThrow(() => assertNativePrintSnapshot({ pageCount: 4, text: report }));
  for (const snapshot of [{ pageCount: 1, text: report }, { pageCount: 4, text: "synthetic authorized print row 0" },
    { pageCount: 4, text: report + " VIBETABLE_PRINT_PRIVATE_PORTAL" }])
    assert.throws(() => assertNativePrintSnapshot(snapshot));
  assert.doesNotThrow(() => assertNativePrintSnapshot({ pageCount: 4, text: report }, report));
  assert.doesNotThrow(() => assertNativePrintSnapshot({ pageCount: 1, text: "" }, report));
  assert.throws(() => assertNativePrintSnapshot({ pageCount: 1, text: "platform rows" }, report));
});

function installPreview({ pageCount = 4, text = report, replySource = true, transportFailure = false, url = "chrome://print/" } = {}) {
  const originals = Object.fromEntries(["window", "document", "location"].map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
  const listeners = new Set(); const messages = [];
  const viewer = { contentWindow: { postMessage(message) {
    if (transportFailure) throw new Error("synthetic PDF viewer gone");
    messages.push(message.type);
    if (message.type === "getSelectedText") queueMicrotask(() => {
      for (const listener of [...listeners]) {
        // A forged sibling cannot answer the PDF text request.
        listener({ source: {}, origin: "chrome://print", data: { type: "getSelectedTextReply", selectedText: "forged private text" } });
        listener({ source: replySource ? viewer.contentWindow : {}, origin: "chrome://print", data: { type: "getSelectedTextReply", selectedText: text } });
      }
    });
  } }, src: "chrome://print/pdf/index_print.html?chrome-untrusted://print/123/0/print.pdf" };
  const area = { previewLoaded: () => true, shadowRoot: { querySelector: selector => selector === "#pdf-viewer" ? viewer : null } };
  const info = { documentSettings: { pageCount } };
  const app = { shadowRoot: { querySelector: selector => selector === "print-preview-preview-area" ? area : selector === "print-preview-document-info" ? info : null } };
  Object.defineProperty(globalThis, "window", { configurable: true, value: {
    addEventListener(_name, callback) { listeners.add(callback); }, removeEventListener(_name, callback) { listeners.delete(callback); },
  } });
  Object.defineProperty(globalThis, "document", { configurable: true, value: { querySelector: () => app } });
  Object.defineProperty(globalThis, "location", { configurable: true, value: { href: url } });
  return { messages, listeners, restore() { for (const [key, descriptor] of Object.entries(originals)) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor); else delete globalThis[key];
  } } };
}
test("preview reader binds the browser PDF viewer source and only selects/reads its actual text", async () => {
  const f = installPreview();
  try {
    const result = await readNativePrintPreviewInPage();
    assert.deepEqual(result, { pageCount: 4, text: report, viewerUrl: "chrome://print/pdf/index_print.html?chrome-untrusted://print/123/0/print.pdf" });
    assert.deepEqual(f.messages, ["selectAll", "getSelectedText"]);
    assert.equal(f.listeners.size, 0);
  } finally { f.restore(); }
});
test("preview reader rejects an application page without sending any PDF message", async () => {
  const f = installPreview({ url: "https://app.vibetable.local/" });
  try { await assert.rejects(readNativePrintPreviewInPage(), /native print preview origin/); assert.deepEqual(f.messages, []); }
  finally { f.restore(); }
});
test("preview reader bounds native page count and selected text", async () => {
  for (const invalid of [{ pageCount: 201 }, { text: "x".repeat(200001) }]) {
    const f = installPreview(invalid);
    try { await assert.rejects(readNativePrintPreviewInPage(), /preview.*limit/); assert.equal(f.listeners.size, 0); }
    finally { f.restore(); }
  }
});


test("native preview discovery excludes app and forged internal URLs", () => {
  const pages = ["https://app.vibetable.local/", "https://print/", "chrome://print/other", "edge://print/"]
    .map(url => ({ url: () => url, isClosed: () => false }));
  const page = { context: () => ({ browser: () => ({ contexts: () => [{ pages: () => pages }] }) }) };
  assert.deepEqual(nativePrintPreviewPages(page), [pages[3]]);
});
test("native cancel clicks only the single browser Cancel control and observes target closure", async () => {
  const actions = [];
  const preview = { url: () => "edge://print/", locator(selector) {
    assert.equal(selector, "print-preview-button-strip .cancel-button");
    return { count: async () => 1, click: async () => actions.push("cancel") };
  }, waitForEvent: async event => { assert.equal(event, "close"); actions.push("observe-close"); } };
  await cancelNativePrintPreview(preview);
  assert.deepEqual(actions, ["observe-close", "cancel"]);
  await assert.rejects(cancelNativePrintPreview({ ...preview, url: () => "https://app.vibetable.local/" }), /cancel target changed/);
  await assert.rejects(cancelNativePrintPreview({ ...preview, locator: () => ({ count: async () => 2 }) }), /one native.*Cancel/);
});


test("an unobservable real preview saves target diagnostics and fails without a simulated fallback", async () => {
  const now = Date.now; const write = fs.writeFile; const calls = []; let tick = 0;
  const pages = [{ url: () => "https://app.vibetable.local/", isClosed: () => false }];
  const browser = { contexts: () => [{ pages: () => pages }], async newBrowserCDPSession() {
    return { async send(method) { calls.push(method); return { targetInfos: [{ type: "page", url: pages[0].url() }] }; },
      async detach() { calls.push("detach"); } };
  } };
  const page = { context: () => ({ browser: () => browser }) };
  try {
    Date.now = () => tick++ * 30001;
    fs.writeFile = async (target, contents) => { calls.push({ target, diagnostic: JSON.parse(contents) }); };
    await assert.rejects(waitForNativePrintPreview(page, "evidence"), /native_print_preview_unobservable/);
    assert.deepEqual(calls.slice(0, 2), ["Target.getTargets", "detach"]);
    assert.match(calls[2].target, /48-plugin-print-preview-unobservable\.json$/);
    assert.equal(calls[2].diagnostic.targets[0].url, pages[0].url());
    assert.equal("emulateMedia" in calls[2].diagnostic, false);
  } finally { Date.now = now; fs.writeFile = write; }
});


test("a failed PDF read transport removes the message listener without leaving a timeout", async () => {
  const f = installPreview({ transportFailure: true });
  try {
    await assert.rejects(readNativePrintPreviewInPage(), /synthetic PDF viewer gone/);
    assert.equal(f.listeners.size, 0);
  } finally { f.restore(); }
});
