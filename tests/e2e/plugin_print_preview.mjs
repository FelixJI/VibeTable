import { attachNativePreview, selectOwnedPreviewTarget } from "./plugin_print_preview_target.mjs";
import fs from "node:fs/promises";
import path from "node:path";

export const isNativePrintPreviewUrl = url => /^(?:chrome|edge):\/\/print\/(?:[?#].*)?$/.test(url);

/** Fixed QA read in Chromium's actual print WebUI, never the product page. */
export async function readNativePrintPreviewInPage() {
  if (!/^(?:chrome|edge):\/\/print\/(?:[?#].*)?$/.test(location.href))
    throw new Error("native print preview origin required");
  const startedAt = Date.now(), observations = [];
  function observe() {
    const app = document.querySelector("print-preview-app"), root = app?.shadowRoot;
    const area = root?.querySelector("print-preview-preview-area");
    const info = root?.querySelector("print-preview-document-info");
    const sidebar = root?.querySelector("print-preview-sidebar");
    const strip = sidebar?.shadowRoot?.querySelector("print-preview-button-strip")
      ?? root?.querySelector("print-preview-button-strip");
    const viewer = area?.shadowRoot?.querySelector("#pdf-viewer");
    const elements = [], queue = [{ node: document.body, depth: 0 }];
    while (queue.length && elements.length < 64) {
      const { node, depth } = queue.shift();
      if (!node) continue;
      elements.push({ tag: String(node.tagName ?? "").slice(0, 48), id: String(node.id ?? "").slice(0, 96),
        classes: String(node.className ?? "").slice(0, 96), shadow: Boolean(node.shadowRoot) });
      if (depth < 8) {
        for (const parent of [node, node.shadowRoot]) {
          const children = parent?.children;
          for (let index = 0; index < Math.min(children?.length ?? 0, 64); index++) {
            if (queue.length + elements.length >= 128) break;
            queue.push({ node: children[index], depth: depth + 1 });
          }
        }
      }
    }
    const at = Date.now();
    observations.push({ at, elapsedMs: at - startedAt, href: String(location.href).slice(0, 512),
      readyState: document.readyState ?? null, userAgent: typeof navigator === "undefined" ? null : String(navigator.userAgent).slice(0, 512),
      app: Boolean(app), appShadow: Boolean(root), area: Boolean(area), info: Boolean(info),
      pageCount: typeof info?.documentSettings?.pageCount === "number" ? info.documentSettings.pageCount : null,
      previewLoadedApi: typeof area?.previewLoaded, previewState: String(area?.previewState ?? "").slice(0, 96),
      previewError: String(area?.error ?? "").slice(0, 96), viewer: Boolean(viewer),
      viewerUrl: String(viewer?.src ?? "").slice(0, 512), cancelCount: strip?.shadowRoot?.querySelectorAll(".cancel-button")?.length ?? 0,
      elements });
  }
  try { observe(); } catch (error) { observations.push({ captureError: error.message }); }
  try {
    const deadline = startedAt + 15000;
    let area, info, viewer;
    while (Date.now() < deadline) {
      const app = document.querySelector("print-preview-app");
      area = app?.shadowRoot?.querySelector("print-preview-preview-area");
      info = app?.shadowRoot?.querySelector("print-preview-document-info");
      viewer = area?.shadowRoot?.querySelector("#pdf-viewer");
      if (area?.previewLoaded?.() && viewer?.contentWindow && info?.documentSettings?.pageCount > 0) break;
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    const pageCount = info?.documentSettings?.pageCount;
    if (!Number.isSafeInteger(pageCount) || pageCount < 1 || pageCount > 200)
      throw new Error("native preview page limit or unreadable page count");
    if (!area?.previewLoaded?.() || !viewer?.contentWindow)
      throw new Error("native preview PDF did not load");
    const viewerUrl = viewer.src;
    if (!/^(?:chrome|edge):\/\/print\/pdf\/index_print\.html\?/.test(viewerUrl))
      throw new Error("native preview PDF viewer source changed");
    const viewerOrigin = new URL(viewerUrl).protocol + "//" + new URL(viewerUrl).host;
    const text = await new Promise((resolve, reject) => {
      const cleanup = () => { clearTimeout(timer); window.removeEventListener("message", receive); };
      const timer = setTimeout(() => { cleanup(); reject(new Error("native preview PDF text timeout")); }, 5000);
      const receive = event => {
        if (event.source !== viewer.contentWindow || event.origin !== viewerOrigin
          || event.data?.type !== "getSelectedTextReply") return;
        cleanup();
        if (typeof event.data.selectedText !== "string" || event.data.selectedText.length > 200000)
          reject(new Error("native preview text limit"));
        else resolve(event.data.selectedText);
      };
      window.addEventListener("message", receive);
      // Chromium PdfViewerPrint handles these read/selection messages; no clipboard or Print request.
      try {
        viewer.contentWindow.postMessage({ type: "selectAll" }, viewerOrigin);
        viewer.contentWindow.postMessage({ type: "getSelectedText" }, viewerOrigin);
      } catch (error) { cleanup(); reject(error); }
    });
    return { pageCount, text, viewerUrl };
  } catch (error) {
    try { observe(); } catch (captureError) { observations.push({ captureError: captureError.message }); }
    const diagnostic = "VIBETABLE_PRINT_READINESS " + JSON.stringify({ startedAt, observations });
    const failure = new Error(error.message + "\n" + diagnostic, { cause: error });
    failure.stack = error.stack + "\n" + diagnostic;
    throw failure;
  }
}

export function assertNativePrintSnapshot(snapshot, originalText) {
  if (!Number.isSafeInteger(snapshot.pageCount) || snapshot.pageCount < 1 || snapshot.pageCount > 200
    || typeof snapshot.text !== "string" || snapshot.text.length > 200000)
    throw new Error("invalid native print preview snapshot");
  if (snapshot.text.includes("VIBETABLE_PRINT_PRIVATE_"))
    throw new Error("native print preview leaked parent content");
  if (originalText !== undefined) {
    if (snapshot.text !== originalText && snapshot.text.trim() !== "")
      throw new Error("native preview changed to nonempty platform content after navigation");
  } else if (snapshot.pageCount < 2 || !snapshot.text.includes("synthetic authorized print row 0")
    || !snapshot.text.includes("synthetic authorized print row 99"))
    throw new Error("native print preview lost pagination or first/last report row");
}

export function nativePrintPreviewPages(page) {
  return page.context().pages()
    .filter(candidate => !candidate.isClosed() && isNativePrintPreviewUrl(candidate.url()));
}

async function owningContext(page) {
  const own = await page.context().newCDPSession(page);
  try {
    const { targetInfo } = await own.send("Target.getTargetInfo");
    if (!targetInfo.browserContextId) throw new Error("owned product browser context unavailable");
    return targetInfo.browserContextId;
  } finally { await own.detach(); }
}

export async function assertNoNativePrintPreview(page) {
  const session = await page.context().browser().newBrowserCDPSession();
  try {
    const contextId = await owningContext(page);
    const { targetInfos } = await session.send("Target.getTargets");
    if (selectOwnedPreviewTarget(targetInfos, contextId)) throw new Error("an earlier native print preview is still open");
  } finally { await session.detach(); }
}

export async function waitForNativePrintPreview(page, evidenceDir) {
  const deadline = Date.now() + 30000;
  const browser = page.context().browser();
  const diagnostics = { pages: [] };
  let session;
  try {
    session = await browser.newBrowserCDPSession();
    const contextId = await owningContext(page);
    do {
      const candidates = nativePrintPreviewPages(page);
      if (candidates.length > 1) throw new Error("ambiguous native print preview targets");
      if (candidates.length === 1) return candidates[0];
      diagnostics.pages = browser.contexts().flatMap(context => context.pages()).map(candidate => ({ url: candidate.url(), closed: candidate.isClosed() }));
      diagnostics.targets = (await session.send("Target.getTargets")).targetInfos;
      const target = selectOwnedPreviewTarget(diagnostics.targets, contextId);
      if (target) {
        const preview = await attachNativePreview(session, target);
        session = null; // The actual-target adapter now owns and releases this session.
        return preview;
      }
      if (Date.now() >= deadline) break;
      await new Promise(resolve => setTimeout(resolve, 50));
    } while (Date.now() < deadline);
  } catch (error) { diagnostics.targetObservationError = error.message; }
  finally {
    try { await session?.detach(); }
    catch (error) { diagnostics.detachError = error.message; }
  }
  await fs.writeFile(path.join(evidenceDir, "48-plugin-print-preview-unobservable.json"), JSON.stringify(diagnostics, null, 2));
  throw new Error("native_print_preview_unobservable: actual browser preview target could not be attached; no simulated fallback");
}

export async function cancelNativePrintPreview(preview) {
  if (!isNativePrintPreviewUrl(preview.url())) throw new Error("native preview cancel target changed");
  const cancel = preview.locator("print-preview-button-strip .cancel-button");
  if (await cancel.count() !== 1) throw new Error("one native print preview Cancel button required");
  await Promise.all([preview.waitForEvent("close", { timeout: 15000 }), cancel.click({ timeout: 5000 })]);
}


/** Diagnostic failure never replaces the original print failure or approves a preview. */
export async function observeNativePrintPreviewFailure(preview, error, evidenceDir) {
  const evidence = {};
  const diagnostic = error.message.split("\n").find(line => line.startsWith("VIBETABLE_PRINT_READINESS "));
  if (diagnostic) {
    try { evidence.webUiReadiness = JSON.parse(diagnostic.slice("VIBETABLE_PRINT_READINESS ".length)); }
    catch (diagnosticError) { evidence.diagnosticError = diagnosticError.message; }
  }
  if (preview && !preview.isClosed()) {
    try {
      await preview.screenshot({ path: path.join(evidenceDir, "48-plugin-print-preview-failed.png"), timeout: 3000 });
      evidence.failureScreenshot = "48-plugin-print-preview-failed.png";
    } catch (screenshotError) { evidence.failureScreenshotError = screenshotError.message; }
  }
  return evidence;
}
