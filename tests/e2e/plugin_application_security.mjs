import fs from "node:fs/promises";
import path from "node:path";
import { installApplication, openApplication, nativePluginInput } from "./plugin_applications.mjs";

export async function runApplicationSecurity(page, recorder, runtime, helpers) {
  const opened = await helpers.waitForShell(page, recorder, { requireDatabaseOpened: true });
  await page.getByTestId("nav-tables").click();
  const table = await helpers.createSimpleTable(page, "Security Tasks", "Text");
  await helpers.applyProductMutation(page, table.tableId, [{ kind: "insert", recordId: null,
    values: { [table.field.physicalName]: "security original" } }], "security-seed");
  await installApplication(page, runtime);
  await installApplication(page, runtime, "app-b", "com.vibetable.e2e.application-b");
  let frame = await openApplication(page);
  const originalToken = await frame.evaluate(() => window.name);
  const catalog = await helpers.rawBridgeRequest(page, "plugin.catalog.list", { projectKey: opened.payload.projectKey });
  const other = catalog.payload.find(item => item.pluginId === "com.vibetable.e2e.application-b").manifest.actions[0].surfaceSrc;
  const probes = await frame.evaluate(async otherSrc => {
    const violations = [];
    document.addEventListener("securitypolicyviolation", event => violations.push({ directive: event.effectiveDirective, blocked: event.blockedURI }));
    const network = [];
    for (const uri of ["https://blocked.invalid/482-probe", otherSrc, "/ui/app.js"]) {
      try { await fetch(uri); network.push({ uri, accepted: true }); }
      catch { network.push({ uri, accepted: false }); }
    }
    const load = (tag, uri) => new Promise(resolve => {
      const element = document.createElement(tag); element.onload = () => { element.remove(); resolve("loaded"); };
      element.onerror = () => { element.remove(); resolve("blocked"); };
      element.src = uri; document.body.appendChild(element);
    });
    const crossScript = await load("script", otherSrc.replace("index.html", "app.js"));
    const traversal = await load("script", location.origin + "/%252e%252e/ui/app.js");
    const inline = document.createElement("script"); inline.textContent = "window.__inlineForged = true"; document.body.appendChild(inline); inline.remove();
    let top, objects;
    try { parent.location.href = "https://blocked.invalid/482-navigation"; top = "accepted"; } catch { top = "blocked"; }
    try { await chrome.webview.hostObjects.sync.fakeFilesystem.read("forbidden"); objects = "accepted"; } catch { objects = "blocked"; }
    let fake;
    try { await call("plugin.install.commit", { planId: "forged" }); fake = "accepted"; } catch(error) { fake = error.code; }
    parent.postMessage({ contract: "vibetable.plugin-surface.v1", surfaceToken: "forged-token", event: "close", payload: {} }, "https://app.vibetable.local");
    const anchor = document.createElement("a"); anchor.href = URL.createObjectURL(new Blob(["forbidden download"])); anchor.download = "forbidden.txt"; document.body.appendChild(anchor); anchor.click(); anchor.remove();
    await new Promise(resolve => setTimeout(resolve, 300));
    return { network, crossScript, traversal, inline: window.__inlineForged === true, top, objects, fake, violations };
  }, other);
  recorder.check("real WebView CSP rejects all direct fetch, cross-plugin script and inline execution",
    probes.network.every(item => item.accepted === false) && probes.crossScript === "blocked" && probes.inline === false
      && probes.violations.some(item => item.directive === "connect-src") && probes.violations.some(item => item.directive === "script-src-elem"), { probes });
  recorder.check("real Host denies encoded traversal, top navigation, host objects and forged approval",
    probes.traversal === "blocked" && probes.top === "blocked" && probes.objects === "blocked"
      && probes.fake === "plugin_api_unsupported" && page.url().startsWith("https://app.vibetable.local/"), { probes });
  recorder.check("forged parent surface message does not close the application",
    await page.getByTestId("plugin-app-viewport").isVisible()
      && (await frame.evaluate(() => call("context.read"))).collection === table.tableId);
  await runtime.acknowledgePluginSecurityDiagnostics(recorder, probes);
  await nativePluginInput(runtime, "observe", "plugin-manage", "contrast").then(result =>
    recorder.check("native chrome remains visible over the real security test package", result.status === "completed", { result }));
  for (const mode of ["dark", "light"]) {
    await page.getByTestId("plugin-app-return").click();
    await page.getByTestId("nav-settings").click();
    await page.getByTestId("settings-nav-general").click();
    await helpers.beginBridgeMessageCapture(page, ["appPreferences.update", "operation.failed"]);
    await page.getByTestId("theme-select").click();
    await page.locator(".n-base-select-option").filter({ hasText: mode === "dark" ? /^(深色|Dark)$/u : /^(浅色|Light)$/u }).click();
    const updated = await helpers.waitForCapturedBridgeMessage(page, 30000);
    recorder.check(`actual Host acknowledges the application ${mode} theme`, updated.type === "appPreferences.update" && updated.payload.theme === mode, { updated });
    frame = await openApplication(page);
    await frame.waitForFunction(expected => document.documentElement.dataset.theme === expected, mode);
    const screenshot = await nativePluginInput(runtime, "observe", "plugin-manage", mode === "dark" ? "dark" : "application");
    recorder.check(`real WPF and plugin application remain usable in ${mode}`, screenshot.status === "completed", { screenshot });
  }
  await helpers.beginBridgeMessageCapture(page, ["appearance.changed"]);
  // This closed parent-side scope is refused locally and runs only on the
  // controlled GitHub Windows CI runner; every driver exit restores it.
  const contrast = await nativePluginInput(runtime, "contrast-on", "plugin-manage", "contrast");
  recorder.check("actual controlled-runner system high contrast is observed in WPF",
    contrast.status === "completed" && contrast.observation.systemHighContrast === true
      && (contrast.contrast.observed.flags & 1) === 1, { contrast });
  const projectedContrast = await helpers.waitForCapturedBridgeMessage(page, 10000);
  recorder.check("actual Host projects high contrast from the system notification", projectedContrast.payload.highContrast === true, { projectedContrast });
  await page.waitForFunction(() => matchMedia("(forced-colors: active)").matches, undefined, { timeout: 10000 });
  const contrastPainted = await nativePluginInput(runtime, "observe", "plugin-manage", "contrast");
  recorder.check("WPF high-contrast screenshot follows the Host system projection", contrastPainted.status === "completed" && contrastPainted.observation.systemHighContrast === true, { contrastPainted });
  recorder.check("real WebView receives the operating system forced-color mode", await frame.evaluate(() => matchMedia("(forced-colors: active)").matches));
  const restoredContrast = await nativePluginInput(runtime, "contrast-off", "plugin-manage", "contrast");
  recorder.check("controlled-runner system high contrast is restored exactly", restoredContrast.status === "completed"
    && JSON.stringify(restoredContrast.contrast.original) === JSON.stringify(restoredContrast.contrast.observed), { restoredContrast });
  const scaling = await page.context().newCDPSession(page);
  try {
    await scaling.send("Emulation.setPageScaleFactor", { pageScaleFactor: 1.25 });
    const scale = await page.evaluate(() => ({ pageScale: visualViewport.scale, devicePixelRatio }));
    const nativeScaled = await nativePluginInput(runtime, "observe", "plugin-manage", "scaled");
    recorder.check("actual 125 percent content scale leaves native security chrome reachable",
      Math.abs(scale.pageScale - 1.25) < 0.01 && nativeScaled.status === "completed"
        && nativeScaled.observation.dpi > 0, { scale, nativeScaled, scope: "content scale; physical DPI unchanged" });
  } finally { await scaling.send("Emulation.setPageScaleFactor", { pageScaleFactor: 1 }); }
  const browserSession = await page.context().browser().newBrowserCDPSession();
  const before = await browserSession.send("SystemInfo.getProcessInfo");
  const faultToken = await frame.evaluate(() => window.name);
  await frame.evaluate(() => { setTimeout(() => document.getElementById("hang").click(), 50); });
  await new Promise(resolve => setTimeout(resolve, 750));
  const looping = await browserSession.send("SystemInfo.getProcessInfo");
  const faulting = looping.processInfo.filter(item => item.type === "renderer"
    && item.cpuTime - (before.processInfo.find(prior => prior.id === item.id)?.cpuTime ?? item.cpuTime) > 0.2);
  recorder.check("actual plugin loop consumes CPU in one identified renderer", faulting.length === 1, { before, looping, faulting });
  const stopped = await nativePluginInput(runtime, "click", "plugin-stop", "loop");
  recorder.check("actual WPF native stop receives input while the plugin loops", stopped.status === "completed" && stopped.input === "SendInput", { stopped });
  page = await runtime.refreshPluginProductPage();
  await page.getByTestId("nav-home").waitFor({ timeout: 60000 });
  recorder.check("renderer recovery preserves workspace and does not auto-reopen the faulting plugin", await page.getByTestId("plugin-app-viewport").count() === 0);
  const after = await browserSession.send("SystemInfo.getProcessInfo");
  recorder.check("old faulting renderer leaves the shared browser process set",
    !after.processInfo.some(next => next.id === faulting[0].id), { before, looping, faulting, after });
  await nativePluginInput(runtime, "observe", "plugin-manage", "recovered").then(result => recorder.check("actual WPF recovery screenshot remains actionable", result.status === "completed", { result }));
  await page.getByTestId("nav-tables").click();
  await helpers.selectTable(page, "Security Tasks");
  frame = await openApplication(page);
  const stale = await frame.evaluate(async old => { try { await call("context.read", {}, old); return "accepted"; } catch(error) { return error.code; } }, faultToken);
  recorder.check("old token and frame authority cannot be revived after CPU recovery", stale === "plugin_source_denied", { stale });
  const session = await page.context().newCDPSession(page);
  await session.send("Page.crash").catch(error => { recorder.check("actual renderer crash closes or fails the old CDP target", /crash|closed|target|connection/i.test(String(error)), { error: String(error) }); });
  page = await runtime.refreshPluginProductPage();
  await page.getByTestId("nav-home").waitFor({ timeout: 60000 });
  await nativePluginInput(runtime, "observe", "plugin-recover", "crashed").then(result => recorder.check("actual renderer crash recovers into reachable WPF chrome", result.status === "completed", { result }));
  await page.getByTestId("nav-tables").click();
  await helpers.selectTable(page, "Security Tasks");
  frame = await openApplication(page);
  recorder.check("after crash the same PocketBase business record is preserved", (await frame.locator("#rows").innerText()).includes("security original"));
  await page.getByTestId("plugin-app-return").click();
  await fs.writeFile(path.join(runtime.evidenceDir, "49-security-observations.json"), JSON.stringify({ probes, before, looping, faulting, after, stopped }, null, 2));
}
