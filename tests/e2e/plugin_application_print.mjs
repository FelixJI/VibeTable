import path from "node:path";
import fs from "node:fs/promises";
import { assertNativePrintSnapshot, cancelNativePrintPreview, nativePrintPreviewPages,
  readNativePrintPreviewInPage, waitForNativePrintPreview } from "./plugin_print_preview.mjs";

/** Uses real browser preview targets; observes/cancels only, never selects or submits a printer. */
export async function runApplicationPrint(page, frame, recorder, runtime, reopen) {
  const token = await frame.evaluate(() => window.name);
  const invalid = await frame.evaluate(async () => {
    try { await call("output.print", { html: "forged" }); return "accepted"; }
    catch (error) { return error.code; }
  });
  recorder.check("printing accepts no caller HTML or target", invalid === "plugin_capability_invalid", { invalid });
  const height = await frame.evaluate(() => Math.ceil(Math.max(document.documentElement.scrollHeight, document.body?.scrollHeight ?? 0)));
  const prepared = await page.evaluate(({token, height}) => {
    const portal = document.createElement("div");
    portal.dataset.testid = "print-private-portal";
    portal.textContent = "synthetic private lookup attachment field history record";
    document.body.append(portal);
    const footer = document.querySelector('[data-testid="plugin-app-viewport"] .surface-shell');
    const sensitive = document.createElement("aside");
    sensitive.dataset.testid = "print-private-footer";
    sensitive.textContent = "synthetic private task confirmation error";
    footer.append(sensitive);
    return window.__vibetablePluginPrint.prepare(token, "qa-print-range", height);
  }, {token, height});
  recorder.check("trusted parent prepares the actual active application frame", prepared);
  try {
    await page.emulateMedia({ media: "print" });
    const scope = await page.evaluate(() => {
      const frame = document.querySelector('[data-testid="plugin-app-viewport"] iframe');
      const visible = element => getComputedStyle(element).display !== "none";
      const elements = [...document.body.querySelectorAll("*")];
      return { frame: visible(frame), leaked: elements.filter(element => visible(element)
        && element !== frame && !element.contains(frame)).map(element => element.tagName),
      privatePortal: visible(document.querySelector('[data-testid="print-private-portal"]')),
      privateFooter: visible(document.querySelector('[data-testid="print-private-footer"]')) };
    });
    recorder.check("actual parent print CSS excludes every non-frame branch including body portals and task footer",
      scope.frame && scope.leaked.length === 0 && !scope.privatePortal && !scope.privateFooter, scope);
    await page.screenshot({ path: path.join(runtime.evidenceDir, "48-plugin-print-scope.png") });
    await page.emulateMedia({ media: "screen" });
    await page.getByTestId("plugin-app-return").click();
    await page.emulateMedia({ media: "print" });
    const failClosed = await page.evaluate(() => document.documentElement.hasAttribute("data-vt-plugin-print")
      && [...document.body.querySelectorAll("*")].every(element => getComputedStyle(element).display === "none"));
    recorder.check("leaving an application while prepared keeps platform and portals out of printing", failClosed);
  } finally {
    await page.emulateMedia({ media: "screen" });
    await page.evaluate(() => {
      window.__vibetablePluginPrint.release("qa-print-range");
      document.querySelector('[data-testid="print-private-portal"]')?.remove();
    });
  }
  frame = await reopen();
  await frame.evaluate(() => {
    const report = document.createElement("section");
    report.id = "synthetic-print-report";
    for (let index = 0; index < 100; index++) {
      const row = document.createElement("p"); row.textContent = `synthetic authorized print row ${index}`; report.append(row);
    }
    document.body.append(report);
  });
  await page.evaluate(() => {
    const portal = document.createElement("div");
    portal.id = "native-print-private-portal";
    portal.textContent = "VIBETABLE_PRINT_PRIVATE_PORTAL VIBETABLE_PRINT_PRIVATE_PLATFORM";
    document.body.append(portal);
    const footer = document.createElement("aside");
    footer.textContent = "VIBETABLE_PRINT_PRIVATE_FOOTER";
    document.querySelector('[data-testid="plugin-app-viewport"] .surface-shell').append(footer);
    window.__applicationPrintTrace = [];
    for (const name of ["beforeprint", "afterprint"]) addEventListener(name, () => {
      const frame = document.querySelector('[data-testid="plugin-app-viewport"] iframe');
      window.__applicationPrintTrace.push({ name, at: Date.now(),
        mode: document.documentElement.hasAttribute("data-vt-plugin-print"),
        height: frame?.style.getPropertyValue("--vt-plugin-print-height"),
        paths: document.querySelectorAll("[data-vt-plugin-print-path]").length });
    });
  });
  const evidence = { status: "running", snapshots: [] };
  let preview;
  let failure;
  try {
    if (nativePrintPreviewPages(page).length) throw new Error("an earlier native print preview is still open");
    for (let cycle = 1; cycle <= 2; cycle++) {
      const opened = await frame.evaluate(() => call("output.print"));
      recorder.check(`native print cycle ${cycle} returns an opened-only receipt`, JSON.stringify(opened) === '{"opened":true}', { opened });
      preview = await waitForNativePrintPreview(page, runtime.evidenceDir);
      const snapshot = await preview.evaluate(readNativePrintPreviewInPage);
      assertNativePrintSnapshot(snapshot);
      evidence.snapshots.push({ stage: `opened-${cycle}`, at: Date.now(), ...snapshot });
      recorder.check(`actual native preview ${cycle} contains first and last report row across multiple pages and no parent secrets`, true,
        { pageCount: snapshot.pageCount, viewerUrl: snapshot.viewerUrl });
      await preview.screenshot({ path: path.join(runtime.evidenceDir,
        cycle === 1 ? "48-plugin-print-dialog-opened.png" : "48-plugin-print-preview-second.png") });
      let closeAfter = Date.now();
      if (cycle === 2) {
        // Trigger the real trusted navigation while the actual native preview is open, without emulating print media.
        await page.evaluate(() => document.querySelector('[data-testid="plugin-app-return"]').click());
        await page.waitForFunction(() => !document.querySelector('[data-testid="plugin-app-viewport"]')
          && !document.querySelector("[data-vt-plugin-print-path]"), null, { timeout: 15000 });
        if (!preview.isClosed()) {
          const navigated = await preview.evaluate(readNativePrintPreviewInPage);
          assertNativePrintSnapshot(navigated, snapshot.text);
          evidence.snapshots.push({ stage: "navigation-during-native-preview", at: Date.now(), ...navigated });
          await preview.screenshot({ path: path.join(runtime.evidenceDir, "48-plugin-print-preview-navigation.png") });
          recorder.check("actual preview after application navigation retains its authorized snapshot or becomes empty, without parent content", true);
          closeAfter = Date.now();
        } else {
          evidence.snapshots.push({ stage: "navigation-closed-native-preview", at: Date.now() });
          recorder.check("actual application navigation closes the native preview", true);
        }
      }
      if (!preview.isClosed()) await cancelNativePrintPreview(preview);
      await page.waitForFunction(({ cycle, closeAfter }) =>
        window.__applicationPrintTrace.filter(event => event.name === "afterprint").length >= cycle
        && window.__applicationPrintTrace.some(event => event.name === "afterprint" && event.at >= closeAfter)
        && !document.documentElement.hasAttribute("data-vt-plugin-print")
        && !document.querySelector("[data-vt-plugin-print-path]"), { cycle, closeAfter }, { timeout: 15000 });
      recorder.check(`real native preview close ${cycle} emits afterprint and releases the isolated parent range`, true);
      evidence.snapshots.push({ stage: `closed-${cycle}`, at: Date.now() });
      preview = null;
    }
    evidence.status = "passed";
  } catch (error) {
    evidence.status = "failed";
    evidence.error = error.message;
    failure = error;
  } finally {
    if (preview && !preview.isClosed()) {
      try { await cancelNativePrintPreview(preview); }
      catch (error) { evidence.cleanupError = error.message; failure ??= error; }
    }
    evidence.events = await page.evaluate(() => window.__applicationPrintTrace);
    await fs.writeFile(path.join(runtime.evidenceDir, "48-plugin-print-preview.json"), JSON.stringify(evidence, null, 2));
    await page.evaluate(() => document.querySelector("#native-print-private-portal")?.remove());
  }
  if (failure) throw failure;
}
