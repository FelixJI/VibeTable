import fs from "node:fs/promises";
import path from "node:path";

const A = "com.vibetable.e2e.application-a";
const B = "com.vibetable.e2e.application-b";
const APP = id => `nav-plugin-app-${id}/open-app`;

// Observe the one UI-approved lease; hiding the plan is not a Host terminal.
export function installPluginUpgradeCaptureInPage(plan) {
  const webview = window.chrome.webview;
  window.__vibetableE2EBridgeCapture?.release?.();
  const previous = webview.postMessage;
  const capture = { id: `plugin-upgrade/${plan.planId}`, message: null, error: null, requestId: null };
  window.__vibetableE2EBridgeCapture = capture;
  const parse = value => {
    if (typeof value !== "string") return value;
    try { return JSON.parse(value); } catch { return null; }
  };
  function release() {
    webview.removeEventListener("message", onMessage);
    if (webview.postMessage === postMessage) webview.postMessage = previous;
  }
  function onMessage(event) {
    const message = parse(event.data);
    if (!capture.requestId || message?.requestId !== capture.requestId) return;
    if (message.type === "operation.failed") {
      capture.error = { method: "plugin.lifecycle.upgrade", code: message.payload?.code ?? "unknown",
        message: message.payload?.message ?? "approved upgrade failed" };
    } else if (message.type === "plugin.lifecycle.upgrade") capture.message = message;
    else return;
    release();
  }
  function postMessage(...args) {
    const message = parse(args[0]);
    const payload = message?.payload;
    if (message?.type === "plugin.lifecycle.upgrade" && payload?.planId === plan.planId
      && payload.projectKey === plan.projectKey && payload.pluginId === plan.manifest.pluginId
      && payload.projectRevision === plan.projectRevision) {
      if (typeof message.requestId !== "string" || !message.requestId || capture.requestId) {
        capture.error = { method: "plugin.lifecycle.upgrade", code: "CAPTURE_UPGRADE_IDENTITY_INVALID",
          message: "approved lease must have exactly one correlated request" };
        release();
      } else capture.requestId = message.requestId;
    }
    return Reflect.apply(previous, this, args);
  }
  capture.release = release;
  webview.addEventListener("message", onMessage);
  webview.postMessage = postMessage;
  return capture.id;
}

export async function commitInspectedApplicationUpgrade(page, plan, helpers) {
  const captureId = await page.evaluate(installPluginUpgradeCaptureInPage, plan);
  await page.getByTestId("plugin-install-commit").click();
  const terminal = await helpers.waitForCapturedBridgeMessage(page, 30000, captureId);
  if (terminal.type !== "plugin.lifecycle.upgrade" || terminal.payload?.pluginId !== plan.manifest.pluginId
    || terminal.payload?.projectKey !== plan.projectKey || terminal.payload?.packageHash !== plan.packageHash
    || terminal.payload?.version !== plan.manifest.version)
    throw new Error("approved upgrade terminal does not match its inspected complete package");
  await page.getByTestId("plugin-install-plan").waitFor({ state: "hidden", timeout: 30000 });
  return (await helpers.rawBridgeRequest(page, "plugin.catalog.list", { projectKey: plan.projectKey }))
    .payload.find(item => item.pluginId === plan.manifest.pluginId);
}

export async function nativePluginInput(runtime, action, buttonId, stage) {
  const requestId = `native-plugin-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  const target = path.join(runtime.evidenceDir, "plugin-native-request.json");
  await fs.writeFile(`${target}.tmp`, JSON.stringify({ requestId, action, buttonId, stage }));
  await fs.rename(`${target}.tmp`, target);
  const deadline = Date.now() + 15000;
  while (Date.now() < deadline) {
    const result = await fs.readFile(path.join(runtime.evidenceDir, "plugin-native-result.json"), "utf8")
      .then(JSON.parse).catch(error => { if (error.code === "ENOENT") return null; throw error; });
    if (result?.requestId === requestId) return result;
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  throw new Error("owned native plugin input deadline");
}

export async function installApplication(page, runtime, archive = "app-a", id = A) {
  await fs.writeFile(path.join(runtime.controlsDir, "plugin-source.txt"), `${path.join(runtime.controlsDir, archive + ".vtplugin")}\n`);
  await page.getByTestId("nav-plugins").click();
  await page.getByTestId("plugin-install-package").click();
  await page.getByTestId("plugin-install-plan").waitFor({ timeout: 30000 });
  await page.getByTestId("plugin-install-commit").click();
  const row = page.locator("button.plugin-row").filter({ hasText: id });
  await row.waitFor({ timeout: 60000 });
  await row.click();
  await page.getByTestId(APP(id)).waitFor({ timeout: 30000 });
}

export async function openApplication(page, id = A) {
  await page.getByTestId(APP(id)).click();
  const frame = page.frameLocator('[data-testid="plugin-app-viewport"] iframe');
  await frame.locator("#status").filter({ hasText: /^ready$/ }).waitFor({ timeout: 30000 });
  return page.frames().find(candidate => candidate.url().includes(".plugins.vibetable.local/ui/index.html"));
}

async function terminalText(frame, text) {
  await frame.locator("#result").filter({ hasText: new RegExp(`^${text}$`) }).waitFor({ timeout: 30000 });
}

async function selectPlugin(page, id) {
  await page.getByTestId("nav-plugins").click();
  await page.locator("button.plugin-row").filter({ hasText: id }).click();
}

async function headless(page, id, actionId) {
  await selectPlugin(page, id);
  await page.locator(".action-row").filter({ hasText: actionId }).locator("button.run-button").click();
  await page.getByTestId("plugin-headless-runner").waitFor({ timeout: 30000 });
}

export async function runPluginApplications(page, recorder, runtime, helpers, statePath = null) {
  const { waitForShell, createSimpleTable, applyProductMutation, rawBridgeRequest,
    activateRestartedWorkspace, selectTable, acknowledgeExpectedBridgeFailure,
    beginBridgeMessageCapture, waitForCapturedBridgeMessage, listRetainedPluginPackages } = helpers;
  let state;
  if (statePath) {
    state = JSON.parse(await fs.readFile(statePath, "utf8"));
    const { switched } = await activateRestartedWorkspace(page, state.workspaceId);
    recorder.check("normal cold Host reopens the same application workspace at a fresh epoch",
      switched.result?.workspaceId === state.workspaceId && switched.result.sessionEpoch > state.sessionEpoch,
      { switched: switched.result, state });
    await page.getByTestId("nav-tables").click();
    await selectTable(page, "Application Tasks");
  } else {
    const opened = await waitForShell(page, recorder, { requireDatabaseOpened: true });
    await page.getByTestId("nav-tables").click();
    const table = await createSimpleTable(page, "Application Tasks", "Text");
    await applyProductMutation(page, table.tableId, [{ kind: "insert", recordId: null,
      values: { [table.field.physicalName]: "synthetic original" } }], "application-seed");
    const session = await page.evaluate(() => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
    recorder.check("application seed uses the actual opened workspace session",
      typeof session?.workspaceId === "string" && Number.isSafeInteger(session.sessionEpoch) && session.sessionEpoch > 0
        && opened.payload.projectKey === `local:${session.workspaceId.replaceAll("-", "").toLowerCase()}`, { session, projectKey: opened.payload.projectKey });
    state = { workspaceId: session.workspaceId, sessionEpoch: session.sessionEpoch,
      projectKey: opened.payload.projectKey, tableId: table.tableId, pluginId: A };
    await installApplication(page, runtime);
    await installApplication(page, runtime, "app-b", B);
  }
  const viewport = page.viewportSize() || await page.evaluate(() => ({ width: innerWidth, height: innerHeight }));
  try {
    await page.setViewportSize({ width: 800, height: 480 });
    const last = page.locator('.nav-button--plugin-app').last();
    recorder.check("synthetic installed packages contribute many real application entries", await page.locator('.nav-button--plugin-app').count() >= 38);
    await last.scrollIntoViewIfNeeded();
    const lastId = await last.getAttribute("data-testid");
    await last.click();
    await page.waitForFunction(id => document.querySelector(`[data-testid="${id}"]`)?.getAttribute("aria-current") === "page", lastId);
    const reachable = await page.evaluate(() => ["nav-admin", "nav-help", "nav-settings"].every(id => {
      const button = document.querySelector(`[data-testid="${id}"]`);
      const box = button.getBoundingClientRect();
      return box.top >= 0 && box.bottom <= innerHeight && box.height > 0
        && document.elementFromPoint(box.x + box.width/2, box.y + box.height/2)?.closest(`[data-testid="${id}"]`) === button;
    }));
    recorder.check("many real application entries scroll while native product navigation remains reachable in a small viewport", reachable);
    await page.getByTestId("nav-settings").click();
    recorder.check("settings remains clickable after scrolling the application rail", await page.getByTestId("nav-settings").getAttribute("aria-current") === "page");
    await page.screenshot({ path: path.join(runtime.evidenceDir, "application-navigation-small.png") });
  } finally { await page.setViewportSize(viewport); }
  let frame = await openApplication(page);
  const token = await frame.evaluate(() => window.name);
  const context = await frame.evaluate(() => call("context.read"));
  recorder.check("full application binds the existing table without creating an Interface", context.collection === state.tableId, { context });
  for (const route of ["detail", "stats", "list"]) {
    await frame.getByRole("link", { name: ({ detail: "详情", stats: "统计", list: "列表" })[route], exact: true }).click();
    await frame.locator(`#${route}`).waitFor({ state: "visible" });
    recorder.check(`internal hash route ${route} preserves frame token and API scope`,
      await frame.evaluate(() => window.name) === token
        && (await frame.evaluate(() => call("context.read"))).collection === state.tableId);
  }
  await frame.evaluate(() => call("storage.set", { key: "scope", value: "plugin-a" }));
  await page.getByTestId("plugin-app-return").click();
  let other = await openApplication(page, B);
  recorder.check("same display name keeps separate plugin identity, origin and private state",
    other.url() !== frame.url() && await other.evaluate(() => call("storage.get", { key: "scope" })) === null,
    { a: frame.url(), b: other.url() });
  const forged = await other.evaluate(async oldToken => {
    try { await call("context.read", {}, oldToken); return "accepted"; }
    catch (error) { return error.code; }
  }, token);
  recorder.check("cross-plugin token cannot authorize the second frame", forged === "plugin_source_denied", { forged });
  await page.getByTestId("plugin-app-return").click();
  frame = await openApplication(page);
  recorder.check("close/reopen obtains a fresh token and keeps private state",
    await frame.evaluate(() => window.name) !== token
      && await frame.evaluate(() => call("storage.get", { key: "scope" })) === "plugin-a");
  await nativePluginInput(runtime, "observe", "plugin-manage", "application").then(result =>
    recorder.check("actual WPF management chrome is visible and reachable", result.status === "completed", { result }));

  if (!statePath) {
    const began = Date.now();
    for (let index = 0; index < 70; index++) await frame.evaluate(() => call("context.read"));
    await page.waitForTimeout(Math.max(0, 16000 - (Date.now() - began)));
    recorder.check("page remains usable past 15 seconds and 64 bounded calls",
      (await frame.evaluate(() => call("context.read"))).collection === state.tableId);
    const concurrent = await frame.evaluate(async () => {
      const plans = [];
      for (let i=0;i<8;i++) plans.push(await call("mutation.preview", {contract:"vibetable.surface-mutation-plan.v1",collection:description.collection,schemaRevision:description.schemaRevision,operations:[{kind:"update",primaryKey:String(row.id),expectedDigest:guard,values:{[field]:"must-not-commit"}}]}));
      const submissions = plans.map(plan => call("mutation.submit", {planId:plan.planId}).catch(error=>error.code));
      const ids = [...pending.keys()];
      await new Promise(resolve=>setTimeout(resolve,300));
      let limit;
      try { await call("context.read"); limit="accepted"; } catch(error){limit=error.code;}
      for (const requestId of ids) await call("request.cancel",{requestId});
      const outcomes=await Promise.all(submissions);
      return {limit,outcomes};
    });
    recorder.check("eight pending tasks apply backpressure and cancel without committing",
      concurrent.limit === "plugin_concurrency_limit" && concurrent.outcomes.every(code=>code === "plugin_cancel_requested"), { concurrent });
    await page.waitForTimeout(300);
    const oversize = await frame.evaluate(async () => {
      try { await call("storage.set", { key: "oversize", value: "x".repeat(1024 * 1024 + 1) }); return "accepted"; }
      catch (error) { return error.code; }
    });
    recorder.check("oversize request is rejected without exhausting the Host", oversize === "plugin_message_too_large", { oversize });
    await page.getByTestId("plugin-app-return").click();
    for (const [action, outcome] of [["headless-success", "succeeded"], ["headless-failed", "failed"]]) {
      await headless(page, A, action);
      await page.locator(`[data-testid="plugin-headless-runner"] header b[data-state="${outcome}"]`).waitFor({ timeout: 30000 });
      recorder.check(`${action} runs under real CSP and releases its hidden frame`,
        await page.locator('[data-testid="plugin-headless-runner"] iframe').count() === 0);
      await page.getByTestId("plugin-headless-close").click();
    }
    const beforeHeadless = await rawBridgeRequest(page, "query.page", { tableId: state.tableId, query: { filters: [], sorts: [], offset: 0, limit: 10 } });
    await headless(page, A, "headless-write");
    await page.locator('[data-testid="plugin-headless-runner"] .host-confirmation').waitFor({ timeout: 30000 });
    const headlessToken = await page.locator('[data-testid="plugin-headless-runner"] iframe').getAttribute("name");
    const headlessTaskId = await page.locator('[data-testid="plugin-headless-runner"] .task-status code').innerText();
    const ownedHeadless = await rawBridgeRequest(page, "plugin.task.get", { taskId: headlessTaskId });
    recorder.check("real headless write task is bound to its current Host surface", ownedHeadless.payload.surfaceToken === headlessToken);
    await page.getByTestId("plugin-headless-cancel").click();
    // The sticky host task list renders its own state marks; scope the runner status badge to its header.
    await page.locator('[data-testid="plugin-headless-runner"] header b[data-state="cancelled"]').waitFor({ timeout: 30000 });
    const cancelledHeadless = await rawBridgeRequest(page, "plugin.task.get", { taskId: headlessTaskId });
    const afterHeadless = await rawBridgeRequest(page, "query.page", { tableId: state.tableId, query: { filters: [], sorts: [], offset: 0, limit: 10 } });
    recorder.check("headless cancel displays actual Host cancellation and reclaims its frame without a write", cancelledHeadless.payload.state === "cancelled"
      && await page.locator('[data-testid="plugin-headless-runner"] iframe').count() === 0
      && JSON.stringify(beforeHeadless.payload.rows) === JSON.stringify(afterHeadless.payload.rows));
    await page.getByTestId("plugin-headless-close").click();
    await headless(page, A, "headless-wait");
    await page.getByTestId("plugin-headless-cancel").click();
    await page.locator('[data-testid="plugin-headless-runner"] b[data-state="unknown"]').waitFor();
    recorder.check("headless local stop releases its frame without guessing a Host write outcome",
      await page.locator('[data-testid="plugin-headless-runner"] iframe').count() === 0
        && (await page.getByTestId("plugin-headless-runner").innerText()).includes("仍待宿主核对"));
    await page.getByTestId("plugin-headless-close").click();
    const catalog = await rawBridgeRequest(page, "plugin.catalog.list", { projectKey: state.projectKey });
    state.packageHash = catalog.payload.find(item => item.pluginId === A).packageHash;
    return state;
  }

  for (const [decision, expected] of [["rejected", "rejected"], ["approved", "succeeded"]]) {
    await frame.getByRole("link", { name: "详情", exact: true }).click();
    await frame.locator("#draft").fill(`native-${decision}`);
    await beginBridgeMessageCapture(page, ["plugin.interaction.requested"]);
    await frame.locator("#save").click();
    const pending = await waitForCapturedBridgeMessage(page, 30000);
    const task = pending.payload.snapshot;
    const forgedDecision = await rawBridgeRequest(page, "plugin.interaction.resolve", {
      runId: task.runId, interactionId: task.pendingConfirmation.interactionId, decision: "approved" });
    recorder.check("trusted renderer cannot approve an application Host confirmation", forgedDecision.payload.code === "PLUGIN_NATIVE_CONFIRMATION_REQUIRED", { forgedDecision });
    await acknowledgeExpectedBridgeFailure(page, forgedDecision);
    const actual = await nativePluginInput(runtime, decision === "rejected" ? "escape" : "click",
      decision === "rejected" ? "plugin-confirm-reject" : "plugin-confirm-approve", decision);
    recorder.check(`real native ${decision} uses owned input and exact plan`, actual.status === "completed", { actual });
    await terminalText(frame, expected);
  }
  await page.getByTestId("plugin-app-return").click();
  await selectPlugin(page, A);
  await fs.writeFile(path.join(runtime.controlsDir, "plugin-source.txt"), `${path.join(runtime.controlsDir, "app-upgrade.vtplugin")}\n`);
  await page.getByTestId("plugin-upgrade").click();
  await page.getByTestId("plugin-upgrade-permission-delta").waitFor();
  recorder.check("upgrade exposes the permission increment before approval",
    (await page.getByTestId("plugin-upgrade-permission-delta").innerText()).includes("create"));
  await page.getByTestId("plugin-install-cancel").click();
  const before = (await rawBridgeRequest(page, "plugin.catalog.list", { projectKey: state.projectKey })).payload.find(item => item.pluginId === A);
  await beginBridgeMessageCapture(page, ["plugin.install.inspect", "operation.failed"]);
  await page.getByTestId("plugin-upgrade").click();
  const inspected = await waitForCapturedBridgeMessage(page, 30000);
  if (inspected.type !== "plugin.install.inspect" || inspected.payload?.projectKey !== state.projectKey
    || inspected.payload?.manifest?.pluginId !== A) throw new Error("upgrade inspection did not return the requested package");
  const upgraded = await commitInspectedApplicationUpgrade(page, inspected.payload, helpers);
  recorder.check("approved upgrade replaces one complete installation snapshot", upgraded.version === "2.0.0" && upgraded.revision > before.revision, { before, upgraded });
  await fs.writeFile(path.join(runtime.controlsDir, "plugin-source.txt"), `${path.join(runtime.controlsDir, "app-incompatible.vtplugin")}\n`);
  await beginBridgeMessageCapture(page, ["operation.failed", "plugin.install.inspect"]);
  await page.getByTestId("plugin-upgrade").click();
  const incompatible = await waitForCapturedBridgeMessage(page, 30000);
  recorder.check("incompatible upgrade fails closed with a recoverable error", incompatible.type === "operation.failed", { incompatible });
  await acknowledgeExpectedBridgeFailure(page, incompatible);
  const unchanged = (await rawBridgeRequest(page, "plugin.catalog.list", { projectKey: state.projectKey })).payload.find(item => item.pluginId === A);
  recorder.check("failed upgrade preserves original complete snapshot", unchanged.packageHash === upgraded.packageHash && unchanged.revision === upgraded.revision, { unchanged });
  frame = await openApplication(page);
  recorder.check("upgrade preserves private settings", await frame.evaluate(() => call("storage.get", { key: "scope" })) === "plugin-a");
  const retiredToken = await frame.evaluate(() => window.name);
  await page.getByTestId("plugin-app-manage").click();
  await selectPlugin(page, A);
  await page.getByTestId("plugin-toggle").click();
  await page.getByTestId(APP(A)).waitFor({ state: "hidden" });
  await page.getByTestId("plugin-toggle").click();
  await page.getByTestId(APP(A)).waitFor();
  frame = await openApplication(page);
  const stale = await frame.evaluate(async old => { try { await call("context.read", {}, old); return "accepted"; } catch (e) { return e.code; } }, retiredToken);
  recorder.check("disable/re-enable does not revive a retired token", stale === "plugin_source_denied", { stale });
  await page.getByTestId("plugin-app-return").click();
  const retained = await listRetainedPluginPackages(state.workspaceRoot);
  // Package identity determines the single compact cache member through the existing contract.
  const { execFileSync } = await import("node:child_process");
  const member = execFileSync(runtime.pythonExecutable, ["-c", "from backend.infrastructure.plugin_package_lifecycle import _compact_package_digest;import sys;print(_compact_package_digest(sys.argv[1])+'.vtplugin')", upgraded.packageHash], { encoding: "utf8", windowsHide: true }).trim();
  const cached = path.join(retained.cacheDirectory, member), withheld = cached + ".withheld";
  await fs.rename(cached, withheld);
  try {
    await page.getByTestId("nav-plugins").click();
    await page.getByRole("button", { name: "刷新目录", exact: true }).click();
    await page.getByTestId(APP(A)).waitFor({ state: "hidden" });
    recorder.check("missing retained package removes unusable navigation while keeping platform management", await page.getByTestId("plugin-install-package").isVisible());
  } finally { await fs.rename(withheld, cached); }
  await page.getByRole("button", { name: "刷新目录", exact: true }).click();
  await page.getByTestId(APP(A)).waitFor();
  await selectPlugin(page, A);
  await page.getByTestId("plugin-uninstall").click();
  recorder.check("uninstall keeps private cleanup separate and explicit", !await page.getByTestId("plugin-uninstall-private-settings").isChecked());
  await page.getByTestId("plugin-uninstall-confirm").click();
  await page.getByTestId(APP(A)).waitFor({ state: "hidden" });
  const rows = await rawBridgeRequest(page, "query.page", { tableId: state.tableId, query: { filters: [], sorts: [], offset: 0, limit: 10 } });
  recorder.check("uninstall retains business table and committed records", rows.payload.rows.length === 1 && Object.values(rows.payload.rows[0]).includes("native-approved"), { rows });
  return { workspaceId: state.workspaceId, uninstalled: A };
}

export async function runHeadlessBudget(page, recorder, runtime, helpers) {
  await helpers.waitForShell(page, recorder, { requireDatabaseOpened: true });
  await page.getByTestId("nav-tables").click();
  const table = await helpers.createSimpleTable(page, "Budget Tasks", "Text");
  await helpers.applyProductMutation(page, table.tableId, [{ kind: "insert", recordId: null, values: { [table.field.physicalName]: "budget" } }], "budget-seed");
  await installApplication(page, runtime);
  await headless(page, A, "headless-wait");
  const oldToken = await page.locator('[data-testid="plugin-headless-runner"] iframe').getAttribute("name");
  const began = Date.now();
  await new Promise(resolve => setTimeout(resolve, 121000));
  page = await runtime.refreshPluginProductPage();
  await page.getByTestId("nav-home").waitFor({ timeout: 60000 });
  recorder.check("native headless deadline recovers even without a module finish signal", Date.now() - began >= 120000 && await page.locator('[data-testid="plugin-headless-runner"] iframe').count() === 0);
  await page.getByTestId("nav-tables").click();
  await helpers.selectTable(page, "Budget Tasks");
  const frame = await openApplication(page);
  const denied = await frame.evaluate(async token => { try { await call("context.read", {}, token); return "accepted"; } catch (e) { return e.code; } }, oldToken);
  recorder.check("timed-out headless authorization is dead after recovery", denied === "plugin_source_denied", { denied });
  await nativePluginInput(runtime, "observe", "plugin-recover", "recovered").then(result => recorder.check("timeout recovery remains native and visible", result.status === "completed", { result }));
  await page.getByTestId("plugin-app-return").click();
}
