import fs from "node:fs/promises";
import path from "node:path";

export const isNativePrintPreviewUrl = url => /^(?:chrome|edge):\/\/print\/(?:[?#].*)?$/.test(url);

/** Fixed QA read in Chromium's actual print WebUI, never the product page. */
export async function readNativePrintPreviewInPage() {
  if (!/^(?:chrome|edge):\/\/print\/(?:[?#].*)?$/.test(location.href))
    throw new Error("native print preview origin required");
  const deadline = Date.now() + 15000;
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
  return page.context().browser().contexts().flatMap(context => context.pages())
    .filter(candidate => !candidate.isClosed() && isNativePrintPreviewUrl(candidate.url()));
}

export async function waitForNativePrintPreview(page, evidenceDir) {
  const deadline = Date.now() + 30000;
  while (Date.now() < deadline) {
    const candidates = nativePrintPreviewPages(page);
    if (candidates.length > 1) throw new Error("ambiguous native print preview targets");
    if (candidates.length === 1) return candidates[0];
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  const browser = page.context().browser();
  const diagnostics = { pages: browser.contexts().flatMap(context => context.pages()).map(candidate => ({ url: candidate.url(), closed: candidate.isClosed() })) };
  let session;
  try {
    session = await browser.newBrowserCDPSession();
    diagnostics.targets = (await session.send("Target.getTargets")).targetInfos;
  } catch (error) { diagnostics.targetObservationError = error.message; }
  finally {
    try { await session?.detach(); }
    catch (error) { diagnostics.detachError = error.message; }
  }
  await fs.writeFile(path.join(evidenceDir, "48-plugin-print-preview-unobservable.json"), JSON.stringify(diagnostics, null, 2));
  throw new Error("native_print_preview_unobservable: actual browser preview target is not exposed; no simulated fallback");
}

export async function cancelNativePrintPreview(preview) {
  if (!isNativePrintPreviewUrl(preview.url())) throw new Error("native preview cancel target changed");
  const cancel = preview.locator("print-preview-button-strip .cancel-button");
  if (await cancel.count() !== 1) throw new Error("one native print preview Cancel button required");
  await Promise.all([preview.waitForEvent("close", { timeout: 15000 }), cancel.click({ timeout: 5000 })]);
}
