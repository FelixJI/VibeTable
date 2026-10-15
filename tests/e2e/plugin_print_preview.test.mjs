import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import vm from "node:vm";
import { assertNativePrintSnapshot, cancelNativePrintPreview, nativePrintPreviewPages, readNativePrintPreviewInPage, waitForNativePrintPreview, observeNativePrintPreviewFailure } from "./plugin_print_preview.mjs";

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

function installPreview({ pageCount = 4, text = report, replySource = true, transportFailure = false, url = "chrome://print/", layout = "chromium", pluginLoaded = true, documentReady = true, viewerPath = "index_print.html", viewerSource } = {}) {
  const originals = Object.fromEntries(["window", "document", "location"].map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
  const listeners = new Set(); const messages = [];
  const viewer = { contentWindow: { postMessage(message, targetOrigin) {
    assert.equal(targetOrigin, layout === "edge" ? "edge://print" : "chrome://print");
    if (transportFailure) throw new Error("synthetic PDF viewer gone");
    messages.push(message.type);
    if (message.type === "getSelectedText") queueMicrotask(() => {
      for (const listener of [...listeners]) {
        // A forged sibling cannot answer the PDF text request.
        listener({ source: {}, origin: layout === "edge" ? "edge://print" : "chrome://print", data: { type: "getSelectedTextReply", selectedText: "forged private text" } });
        listener({ source: replySource ? viewer.contentWindow : {}, origin: layout === "edge" ? "edge://print" : "chrome://print", data: { type: "getSelectedTextReply", selectedText: text } });
      }
    });
  } }, src: layout === "edge" ? `edge://print/edge_pdf/edge_pdf/${viewerPath}?chrome-untrusted://print/123/0/print.pdf` : "chrome://print/pdf/index_print.html?chrome-untrusted://print/123/0/print.pdf" };
  if (viewerSource) viewer.src = viewerSource;
  const area = { printPreviewState: { pluginLoaded, documentReady }, documentInfoService: { pageCount }, previewLoaded: () => true, shadowRoot: { querySelector: selector => selector === "#pdf-viewer" ? viewer : null } };
  const info = { documentSettings: { pageCount } };
  const app = { shadowRoot: { querySelector: selector => selector === "print-preview-preview-area" ? area : selector === "print-preview-document-info" ? info : null } };
  const edgePreview = { shadowRoot: { querySelector: selector => selector === "preview-area" ? area : null } };
  const shell = { shadowRoot: { querySelector: selector => selector === "print-preview" ? edgePreview : null } };
  Object.defineProperty(globalThis, "window", { configurable: true, value: {
    addEventListener(_name, callback) { listeners.add(callback); }, removeEventListener(_name, callback) { listeners.delete(callback); },
  } });
  Object.defineProperty(globalThis, "document", { configurable: true, value: { querySelector: selector => layout === "both" ? (selector === "app-shell" ? shell : selector === "print-preview-app" ? app : null) : layout === "edge" ? (selector === "app-shell" ? shell : null) : (selector === "print-preview-app" ? app : null) } });
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
  const page = { context: () => ({ pages: () => pages }) };
  assert.deepEqual(nativePrintPreviewPages(page), [pages[3]]);
});
test("native cancel clicks only the single browser Cancel control and observes target closure", async () => {
  const actions = [];
  const preview = { url: () => "edge://print/", locator(selector) {
    assert.equal(selector, "print-preview-button-strip .cancel-button, app-shell print-preview side-bar footer-container fluent-button.cancel");
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
  const page = { context: () => ({ pages: () => pages, browser: () => browser, newCDPSession: async () => ({ send: async () => ({ targetInfo: { browserContextId: "owned" } }), detach: async () => {} }) }) };
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


test("unreadable actual WebUI preserves its failure and bounded timed structure diagnostics", async () => {
  const f = installPreview(); const now = Date.now; let tick = 0;
  document.body = { children: Array.from({ length: 200 }, (_, index) => ({ tagName: "EDGE-PRINT-ROOT", id: `actual-root-${index}`, className: "preview", children: [], get textContent() { throw new Error("diagnostic must not read document text"); } })) };
  document.readyState = "complete";
  try {
    Date.now = () => tick++ * 15001;
    await assert.rejects(readNativePrintPreviewInPage(), error => {
      assert.match(error.message, /page limit or unreadable page count/);
      const line = error.message.split("\n").find(line => line.startsWith("VIBETABLE_PRINT_READINESS "));
      assert.ok(line, "real WebUI failure needs actual readiness evidence");
      const evidence = JSON.parse(line.slice("VIBETABLE_PRINT_READINESS ".length));
      assert.equal(evidence.observations.length, 2);
      assert.equal(evidence.observations.at(-1).elements.length, 64);
      assert.equal(evidence.observations.at(-1).readyState, "complete");
      assert.ok(evidence.observations.at(-1).elements.some(node => node.tag === "EDGE-PRINT-ROOT"));
      return true;
    });
  } finally { Date.now = now; f.restore(); }
});


test("actual failure screenshot is bounded and cannot overwrite the print error", async () => {
  const original = new Error('unreadable preview\nVIBETABLE_PRINT_READINESS {"startedAt":12,"observations":[]}');
  const calls = [];
  const preview = { isClosed: () => false, screenshot: async options => { calls.push(options); } };
  const captured = await observeNativePrintPreviewFailure(preview, original, "evidence");
  assert.equal(captured.webUiReadiness.startedAt, 12);
  assert.equal(captured.failureScreenshot, "48-plugin-print-preview-failed.png");
  assert.equal(calls[0].timeout, 3000);
  assert.match(calls[0].path, /48-plugin-print-preview-failed.png$/);
  const failed = await observeNativePrintPreviewFailure({ ...preview, screenshot: async () => { throw new Error("target capture rejected"); } }, original, "evidence");
  assert.equal(failed.failureScreenshotError, "target capture rejected");
  assert.equal(original.message.split("\n")[0], "unreadable preview");
  const closed = await observeNativePrintPreviewFailure({ ...preview, isClosed: () => true }, original, "evidence");
  assert.equal(closed.failureScreenshot, undefined);
  assert.equal(calls.length, 1);
});


test("a diagnostic DOM failure cannot mask the original reader failure", async () => {
  const f = installPreview(), now = Date.now; let tick = 0;
  Object.defineProperty(document, "body", { get() { throw new Error("DOM inspection unavailable"); } });
  try {
    Date.now = () => tick++ * 15001;
    await assert.rejects(readNativePrintPreviewInPage(), error => {
      assert.match(error.message, /^native preview page limit or unreadable page count/);
      assert.match(error.cause.message, /^native preview page limit or unreadable page count/);
      assert.match(error.message, /DOM inspection unavailable/);
      return true;
    });
  } finally { Date.now = now; f.restore(); }
});


test("actual Edge154 DOM and verified readiness services read the existing PDF viewer", async () => {
  for (const viewerPath of ["index_print.html", "index_print_minimal.html"]) {
    const f = installPreview({ layout: "edge", url: "edge://print/", viewerPath });
    const now = Date.now; let calls = 0;
    try {
      Date.now = () => ++calls < 4 ? 0 : 15001;
      const snapshot = await readNativePrintPreviewInPage();
      assert.equal(snapshot.pageCount, 4);
      assert.equal(snapshot.text, report);
      assert.equal(snapshot.viewerUrl, `edge://print/edge_pdf/edge_pdf/${viewerPath}?chrome-untrusted://print/123/0/print.pdf`);
      assert.deepEqual(f.messages, ["selectAll", "getSelectedText"]);
    } finally { Date.now = now; f.restore(); }
  }
});


test("Edge readiness requires both exact observed booleans before reading PDF text", async () => {
  for (const state of [{ pluginLoaded: false }, { documentReady: false }, { pluginLoaded: 1 }, { documentReady: "true" }]) {
    const f = installPreview({ layout: "edge", url: "edge://print/", ...state });
    const now = Date.now; let calls = 0;
    try {
      Date.now = () => ++calls < 4 ? 0 : 15001;
      await assert.rejects(readNativePrintPreviewInPage(), /native preview PDF did not load/);
      assert.deepEqual(f.messages, []);
    } finally { Date.now = now; f.restore(); }
  }
});

test("Edge rejects an unreadable page count, unsupported viewer path or non-preview PDF source", async () => {
  for (const invalid of [{ pageCount: 201 }, { viewerPath: "unexpected.html" },
    { viewerSource: "edge://print/edge_pdf/edge_pdf/index_print.html?https://app.vibetable.local/private.pdf" },
    { viewerSource: "https://print/edge_pdf/edge_pdf/index_print.html?chrome-untrusted://print/123/0/print.pdf" }]) {
    const f = installPreview({ layout: "edge", url: "edge://print/", ...invalid });
    try { await assert.rejects(readNativePrintPreviewInPage(), /native preview (page limit|PDF viewer source changed)/); assert.deepEqual(f.messages, []); }
    finally { f.restore(); }
  }
});

test("two live native WebUI roots cannot choose a report implicitly", async () => {
  const f = installPreview({ layout: "both" });
  try { await assert.rejects(readNativePrintPreviewInPage(), /ambiguous native print WebUI roots/); assert.deepEqual(f.messages, []); }
  finally { f.restore(); }
});


test("an exposed Edge preview Page also resolves its real footer Cancel", async () => {
  const actions = [];
  const preview = { url: () => "edge://print/", locator(selector) {
    return { count: async () => selector.includes("footer-container fluent-button.cancel") ? 1 : 0,
      click: async () => actions.push("cancel") };
  }, waitForEvent: async () => actions.push("close") };
  await cancelNativePrintPreview(preview);
  assert.deepEqual(actions, ["close", "cancel"]);
});


test("both native readers execute as the serialized fixed CDP function without imported closures", async () => {
  for (const layout of ["chromium", "edge"]) {
    const f = installPreview({ layout, url: layout === "edge" ? "edge://print/" : "chrome://print/" });
    try {
      const snapshot = await vm.runInNewContext(`(${readNativePrintPreviewInPage.toString()})()`,
        { window, document, location, URL, Date, setTimeout, clearTimeout });
      assert.equal(snapshot.pageCount, 4);
      assert.equal(snapshot.text, report);
      assertNativePrintSnapshot(snapshot);
      assert.equal(f.listeners.size, 0);
    } finally { f.restore(); }
  }
});
