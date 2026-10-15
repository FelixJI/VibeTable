import path from "node:path";

/** Runs last: native ShowPrintUI has no completion/cancel receipt. No printer is selected. */
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
    window.__applicationPrintTrace = [];
    for (const name of ["beforeprint", "afterprint"]) addEventListener(name, () => {
      const frame = document.querySelector('[data-testid="plugin-app-viewport"] iframe');
      window.__applicationPrintTrace.push({ name, at: Date.now(),
        mode: document.documentElement.hasAttribute("data-vt-plugin-print"),
        height: frame?.style.getPropertyValue("--vt-plugin-print-height"),
        paths: document.querySelectorAll("[data-vt-plugin-print-path]").length });
    });
  });
  const opened = await frame.evaluate(() => call("output.print"));
  recorder.check("real isolated application opens native preview with an opened-only receipt",
    JSON.stringify(opened) === '{"opened":true}', { opened });
  await page.waitForFunction(() => window.__applicationPrintTrace.some(event => event.name === "beforeprint"), null, { timeout: 30000 });
  const trace = await page.evaluate(() => window.__applicationPrintTrace);
  const viewportHeight = await page.evaluate(() => innerHeight);
  recorder.check("native preview beforeprint observes the isolated frame path and measured long layout",
    trace.some(event => event.name === "beforeprint" && event.mode && event.paths > 0 && parseInt(event.height, 10) > viewportHeight), { trace });
  await page.screenshot({ path: path.join(runtime.evidenceDir, "48-plugin-print-dialog-opened.png") });
}
