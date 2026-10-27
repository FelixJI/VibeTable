import fs from "node:fs/promises";
import path from "node:path";
import { installApplication, openApplication, nativePluginInput, assertSidebarPluginReachable } from "./plugin_applications.mjs";

// Location-less CSP messages must match this attack's exact URL and captured
// directive; each fetch produces one Connecting message and one Fetch message.
export function selectExpectedPluginSecurityConsoleEntries(entries, probes, start) {
  if (!Number.isInteger(start) || start < 0 || start > entries.length) {
    throw new Error("invalid plugin security diagnostics start");
  }
  const claimed = [];
  const used = new Set();
  const attack = probes?.attack;
  const violated = (directive, blocked) => probes?.violations?.some(
    item => item.directive === directive && item.blocked === blocked);
  for (const entry of entries.slice(start)) {
    const text = entry.text.trim();
    let key;
    if (entry.type === "error") {
      for (const url of attack?.fetchTargets ?? []) {
        if (!violated("connect-src", url)) continue;
        if (text === `Connecting to '${url}' violates the following Content Security Policy directive: "connect-src 'none'". The action has been blocked.`) key = `connect:${url}`;
        if (text === `Fetch API cannot load ${url}. Refused to connect because it violates the document's Content Security Policy.`) key = `fetch:${url}`;
      }
      if (attack?.scriptTarget && violated("script-src-elem", attack.scriptTarget)
        && text === `Loading the script '${attack.scriptTarget}' violates the following Content Security Policy directive: "script-src 'self'". Note that 'script-src-elem' was not explicitly set, so 'script-src' is used as a fallback. The action has been blocked.`) key = "script";
      if (violated("script-src-elem", "inline")
        && text.startsWith("Executing inline script violates the following Content Security Policy directive 'script-src 'self''.")
        && text.endsWith("is required to enable inline execution. The action has been blocked.")) key = "inline";
      if (attack?.traversalUrl && entry.location?.url === attack.traversalUrl
        && text === "Failed to load resource: the server responded with a status of 403 (Forbidden)") key = "traversal";
      const navigation = /^Unsafe attempt to initiate navigation for frame with URL '([^']+)' from frame with URL '([^']+)'\. The frame attempting navigation of the top-level window is sandboxed, but the flag of 'allow-top-navigation' or 'allow-top-navigation-by-user-activation' is not set\.$/.exec(text);
      if (navigation && navigation[1].startsWith("https://app.vibetable.local/")
        && navigation[2] === attack?.selfDocumentUrl) key = "top-navigation";
    }
    if (entry.type === "warning" && attack?.selfDocumentUrl
      && entry.location?.url === attack.selfDocumentUrl
      && text === "Download is disallowed. The frame initiating or instantiating the download is sandboxed, but the flag ‘allow-downloads’ is not set. See https://www.chromestatus.com/feature/5706745674465280 for more details.") key = "download";
    if (key && !used.has(key)) {
      used.add(key);
      claimed.push(entry);
    }
  }
  return claimed;
}

export function installContrastRestoreProjectionCaptureInPage() {
  const webview = window.chrome.webview;
  window.__vibetableE2EBridgeCapture?.release?.();
  // Reuse the global capture sequence so consecutive installs stay
  // distinguishable owners for the shared waiter contract.
  const sequence = Number.isSafeInteger(window.__vibetableE2EBridgeCaptureSequence)
    ? window.__vibetableE2EBridgeCaptureSequence + 1
    : 1;
  window.__vibetableE2EBridgeCaptureSequence = sequence;
  const parse = value => {
    if (typeof value !== "string") return value;
    try { return JSON.parse(value); } catch { return null; }
  };
  const capture = { id: `appearance/contrast-restore/${sequence}`, message: null, error: null };
  function release() {
    webview.removeEventListener("message", onMessage);
  }
  function onMessage(event) {
    const message = parse(event.data);
    // Windows keeps broadcasting appearance notifications while the high
    // contrast transition settles and the Host re-projects those still-active
    // states by design; correlate the restoration with its terminal
    // high-contrast-off projection instead of the first intermediate one.
    if (message?.type !== "appearance.changed" || message.payload?.highContrast !== false) return;
    capture.message = message;
    release();
  }
  capture.release = release;
  window.__vibetableE2EBridgeCapture = capture;
  webview.addEventListener("message", onMessage);
  return capture.id;
}

export async function assertRecoveredApplicationShell(page, recorder, checkName) {
  await page.getByTestId("nav-home").waitFor({ timeout: 60000 });
  // The shell mounts before its asynchronous workspace plugin catalog is projected.
  await page.getByTestId("nav-plugin-app-com.vibetable.e2e.application-a/open-app")
    .waitFor({ state: "visible", timeout: 60000 });
  await assertSidebarPluginReachable(page, recorder, checkName);
}

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
  const diagnosticStart = runtime.beginPluginSecurityDiagnostics();
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
    const scriptTarget = otherSrc.replace("index.html", "app.js");
    const traversalUrl = location.origin + "/%252e%252e/ui/app.js";
    const crossScript = await load("script", scriptTarget);
    const traversal = await load("script", traversalUrl);
    const inline = document.createElement("script"); inline.textContent = "window.__inlineForged = true"; document.body.appendChild(inline); inline.remove();
    let top, objects;
    try { parent.location.href = "https://blocked.invalid/482-navigation"; top = "accepted"; } catch { top = "blocked"; }
    try { await chrome.webview.hostObjects.sync.fakeFilesystem.read("forbidden"); objects = "accepted"; } catch { objects = "blocked"; }
    let fake;
    try { await call("plugin.install.commit", { planId: "forged" }); fake = "accepted"; } catch(error) { fake = error.code; }
    parent.postMessage({ contract: "vibetable.plugin-surface.v1", surfaceToken: "forged-token", event: "close", payload: {} }, "https://app.vibetable.local");
    const anchor = document.createElement("a"); anchor.href = URL.createObjectURL(new Blob(["forbidden download"])); anchor.download = "forbidden.txt"; document.body.appendChild(anchor); anchor.click(); anchor.remove();
    await new Promise(resolve => setTimeout(resolve, 300));
    return { network, crossScript, traversal, inline: window.__inlineForged === true, top, objects, fake, violations,
      attack: {
        fetchTargets: ["https://blocked.invalid/482-probe", otherSrc, location.origin + "/ui/app.js"],
        scriptTarget, traversalUrl, selfDocumentUrl: location.href.split("#")[0],
      } };
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
  await runtime.acknowledgePluginSecurityDiagnostics(recorder, probes, diagnosticStart);
  // 旧原生插件 chrome 已移除：插件界面只从侧栏进入；安全包仍被限制在产品视口内。
  await assertSidebarPluginReachable(page, recorder,
    "the security package stays reachable only through the product sidebar");
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
    recorder.check(`plugin application stays usable inside the product shell in ${mode}`,
      await page.getByTestId("plugin-app-viewport").isVisible()
        && await page.getByTestId("nav-plugins").isVisible());
    await page.screenshot({ path: path.join(runtime.evidenceDir, `49-application-${mode}.png`) });
  }
  await helpers.beginBridgeMessageCapture(page, ["appearance.changed"]);
  // This closed parent-side scope is refused locally and runs only on the
  // controlled GitHub Windows CI runner; every driver exit restores it.
  // 系统高对比度只能由父侧原生通道切换；contrast 动作不观察任何按钮，
  // 请求仅要求闭合集中的身份字段（plugin-recover）与 contrast 舞台标签。
  const contrast = await nativePluginInput(runtime, "contrast-on", "plugin-recover", "contrast");
  recorder.check("controlled runner enables high contrast through the actual system API",
    contrast.status === "completed" && (contrast.contrast.observed.flags & 1) === 1, { contrast });
  const projectedContrast = await helpers.waitForCapturedBridgeMessage(page, 10000);
  recorder.check("actual Host projects high contrast from the system notification", projectedContrast.payload.highContrast === true, { projectedContrast });
  await page.waitForFunction(() => matchMedia("(forced-colors: active)").matches, undefined, { timeout: 10000 });
  recorder.check("real WebView receives the operating system forced-color mode", await frame.evaluate(() => matchMedia("(forced-colors: active)").matches));
  await page.screenshot({ path: path.join(runtime.evidenceDir, "49-application-contrast.png") });
  const restoredCaptureId = await page.evaluate(installContrastRestoreProjectionCaptureInPage);
  const restoredContrast = await nativePluginInput(runtime, "contrast-off", "plugin-recover", "contrast");
  recorder.check("controlled runner restores the recorded system contrast contract", restoredContrast.status === "completed"
    && restoredContrast.contrast.status === "restored"
    && restoredContrast.contrast.original.flags === restoredContrast.contrast.observed.flags, { restoredContrast });
  const restoredProjection = await helpers.waitForCapturedBridgeMessage(page, 10000, restoredCaptureId);
  recorder.check("actual Host projects high contrast off after restoration", restoredProjection.payload.highContrast === false, { restoredProjection });
  await page.waitForFunction(() => !matchMedia("(forced-colors: active)").matches, undefined, { timeout: 10000 });
  recorder.check("plugin forced colors also leave high contrast", !await frame.evaluate(() => matchMedia("(forced-colors: active)").matches));
  await page.screenshot({ path: path.join(runtime.evidenceDir, "49-application-contrast-restored.png") });
  const scaling = await page.context().newCDPSession(page);
  try {
    await scaling.send("Emulation.setPageScaleFactor", { pageScaleFactor: 1.25 });
    const scale = await page.evaluate(() => ({ pageScale: visualViewport.scale, devicePixelRatio }));
    recorder.check("actual 125 percent content scale keeps sidebar plugin entries reachable",
      Math.abs(scale.pageScale - 1.25) < 0.01
        && await page.getByTestId("nav-plugins").isVisible()
        && await page.getByTestId("plugin-app-viewport").isVisible(),
      { scale, scope: "content scale; physical DPI unchanged" });
    await page.screenshot({ path: path.join(runtime.evidenceDir, "49-application-scaled.png") });
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
  // 原生窗口菜单不依赖失控的 renderer；通过真实输入触发既有恢复命令。
  const recovered = await nativePluginInput(runtime, "recover", "plugin-recover", "loop");
  recorder.check("native window menu recovers the looping renderer", recovered.status === "completed", { recovered });
  recorder.check("native recovery observes the actual host window DPI", Number.isInteger(recovered.dpi) && recovered.dpi > 0, { dpi: recovered.dpi });
  page = await runtime.refreshPluginProductPage();
  await page.getByTestId("nav-home").waitFor({ timeout: 60000 });
  recorder.check("renderer recovery preserves workspace and does not auto-reopen the faulting plugin", await page.getByTestId("plugin-app-viewport").count() === 0);
  const after = await browserSession.send("SystemInfo.getProcessInfo");
  recorder.check("old faulting renderer leaves the shared browser process set",
    !after.processInfo.some(next => next.id === faulting[0].id), { before, looping, faulting, after });
  await assertRecoveredApplicationShell(page, recorder,
    "recovery leaves sidebar plugin entries reachable without reopening the app");
  await page.screenshot({ path: path.join(runtime.evidenceDir, "49-loop-recovered.png") });
  await page.getByTestId("nav-tables").click();
  await helpers.selectTable(page, "Security Tasks");
  frame = await openApplication(page);
  const stale = await frame.evaluate(async old => { try { await call("context.read", {}, old); return "accepted"; } catch(error) { return error.code; } }, faultToken);
  recorder.check("old token and frame authority cannot be revived after CPU recovery", stale === "plugin_source_denied", { stale });
  const session = await page.context().newCDPSession(page);
  await session.send("Page.crash").catch(error => { recorder.check("actual renderer crash closes or fails the old CDP target", /crash|closed|target|connection/i.test(String(error)), { error: String(error) }); });
  page = await runtime.refreshPluginProductPage();
  // 自动恢复成功后回到 Ready 外壳：故障层断言只在故障层进行（见下），
  // 此处验证恢复后的真实用户可达性。
  await assertRecoveredApplicationShell(page, recorder,
    "actual renderer crash recovers into a usable product shell");
  await page.screenshot({ path: path.join(runtime.evidenceDir, "49-crash-recovered.png") });
  await page.getByTestId("nav-tables").click();
  await helpers.selectTable(page, "Security Tasks");
  frame = await openApplication(page);
  recorder.check("after crash the same PocketBase business record is preserved", (await frame.locator("#rows").innerText()).includes("security original"));
  await page.getByTestId("plugin-app-return").click();
  await fs.writeFile(path.join(runtime.evidenceDir, "49-security-observations.json"), JSON.stringify({
    probes, before, looping, faulting, after, recovery: recovered, contrast, restoredContrast,
  }, null, 2));
}
