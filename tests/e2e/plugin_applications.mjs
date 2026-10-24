import fs from "node:fs/promises";
import path from "node:path";
import { runApplicationPrint } from "./plugin_application_print.mjs";

const A = "com.vibetable.e2e.application-a";
const B = "com.vibetable.e2e.application-b";
const APP = (id, actionId = "open-app") => `nav-plugin-app-${id}/${actionId}`;

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

export async function installApplication(page, runtime, archive = "app-a", id = A, actionId = "open-app") {
  await fs.writeFile(path.join(runtime.controlsDir, "plugin-source.txt"), `${path.join(runtime.controlsDir, archive + ".vtplugin")}\n`);
  await page.getByTestId("nav-plugins").click();
  await page.getByTestId("plugin-install-package").click();
  await page.getByTestId("plugin-install-plan").waitFor({ timeout: 30000 });
  await page.getByTestId("plugin-install-commit").click();
  const row = page.locator("button.plugin-row").filter({ hasText: id });
  await row.waitFor({ timeout: 60000 });
  await row.click();
  // The row/nav can render before commitPlan's follow-up setEnabled lands, so wait for the toggle.
  await page.locator('button[data-testid="plugin-toggle"].enabled:not([disabled])').waitFor({ timeout: 30000 });
  await page.getByTestId(APP(id, actionId)).waitFor({ timeout: 30000 });
}

export async function openApplication(page, id = A, actionId = "open-app") {
  await page.getByTestId(APP(id, actionId)).click();
  const frame = page.frameLocator('[data-testid="plugin-app-viewport"] iframe');
  await frame.locator("#status").filter({ hasText: /^ready$/ }).waitFor({ timeout: 30000 });
  return page.frames().find(candidate => candidate.url().includes(".plugins.vibetable.local/ui/index.html"));
}

async function terminalText(frame, text) {
  await frame.locator("#result").filter({ hasText: new RegExp(`^${text}$`) }).waitFor({ timeout: 30000 });
}

export async function selectPlugin(page, id) {
  await page.getByTestId("nav-plugins").click();
  await page.locator("button.plugin-row").filter({ hasText: id }).click();
}
async function headless(page, id, actionId) {
  await selectPlugin(page, id);
  await page.locator(".action-row").filter({ hasText: actionId }).locator("button.run-button").click();
  await page.getByTestId("plugin-headless-runner").waitFor({ timeout: 30000 });
}

export async function exerciseApplicationConfirmationExpiry(page, frame, recorder, runtime, helpers, state) {
  const { rawBridgeRequest, beginBridgeMessageCapture, waitForCapturedBridgeMessage } = helpers;
  const rows = async () => (await rawBridgeRequest(page, "query.page", {
    tableId: state.tableId, query: { filters: [], sorts: [], offset: 0, limit: 10 },
  })).payload.rows;
  const audit = async () => (await rawBridgeRequest(page, "plugin.audit.list", {
    projectKey: state.projectKey, pluginId: A,
  })).payload;
  const task = async taskId => (await rawBridgeRequest(page, "plugin.task.get", { taskId })).payload;
  const unchanged = (left, right) => JSON.stringify(left) === JSON.stringify(right);
  async function begin(value) {
    await beginBridgeMessageCapture(page, ["plugin.interaction.requested"]);
    const began = Date.now();
    const planId = await frame.evaluate(async value => {
      const context = await call("context.read");
      const description = await call("data.describe", { accepts: ["vibetable.plugin-data.v2"], collection: context.collection });
      const field = description.fields.find(item => !item.readonly && item.logicalType === "text").fieldId;
      const page = await call("data.query", { contract: "vibetable.plugin-query.v2", collection: context.collection,
        fields: ["id", field], sorts: [{ field: "id", direction: "asc" }], pageSize: 200 });
      const row = page.items[0];
      const preview = await call("mutation.preview", { contract: "vibetable.surface-mutation-plan.v1",
        collection: description.collection, schemaRevision: description.schemaRevision,
        operations: [{ kind: "update", primaryKey: String(row.id), expectedDigest: page.rowGuards[String(row.id)], values: { [field]: value } }] });
      const probe = window.__applicationConfirmationQualification = { planId: preview.planId, terminal: null };
      void call("mutation.submit", { planId: preview.planId }).then(
        result => { probe.terminal = { result }; }, error => { probe.terminal = { error: error.code ?? error.message }; });
      return preview.planId;
    }, value);
    const pending = await waitForCapturedBridgeMessage(page, 30000);
    await page.locator(".host-confirmation").waitFor({ timeout: 30000 });
    const taskId = await page.locator(".host-runtime:has(.host-confirmation)").getAttribute("data-task-id");
    const snapshot = await task(taskId);
    recorder.check("confirmation qualification binds the real Host task and exact interaction",
      Boolean(planId) && snapshot.state === "running" && snapshot.runId === pending.payload?.snapshot?.runId
        && snapshot.pluginId === A && snapshot.projectKey === state.projectKey
        && snapshot.collection === state.tableId, { planId, task: snapshot, pending });
    return { planId, taskId, runId: snapshot.runId, pending: pending.payload.snapshot.pendingConfirmation, began };
  }
  async function terminal() {
    await frame.waitForFunction(() => Boolean(window.__applicationConfirmationQualification?.terminal), null, { timeout: 30000 });
    return frame.evaluate(() => window.__applicationConfirmationQualification.terminal);
  }
  async function replay(planId) {
    return frame.evaluate(async planId => {
      try { return { result: await call("mutation.submit", { planId }) }; }
      catch (error) { return { error: error.code ?? error.message }; }
    }, planId);
  }

  const original = await rows();
  const expired = await begin("must-not-commit-after-expiry");
  const observed = await nativePluginInput(runtime, "observe", "plugin-confirm-approve", "confirm");
  const expiresAt = expired.pending.expiresAt * 1000;
  recorder.check("real native confirmation retains the product's full 120 second validity",
    observed.status === "completed" && Number.isFinite(expiresAt) && expiresAt - expired.began >= 120000,
    { observed, began: expired.began, expiresAt });
  // Leave the real WPF confirmation untouched through expiry and two 250 ms native timer ticks.
  while (Date.now() <= expiresAt + 500) await page.waitForTimeout(Math.min(1000, expiresAt + 501 - Date.now()));
  const expiredReceipt = await terminal();
  await page.locator(".host-confirmation").waitFor({ state: "hidden", timeout: 30000 });
  const expiredTask = await task(expired.taskId);
  const expiredAudit = await audit();
  recorder.check("unanswered confirmation expires as a known timeout with zero business writes",
    expiredReceipt.error === "plugin_timeout" && expiredTask.state === "failed"
      && expiredTask.error?.code === "plugin_timeout" && unchanged(original, await rows())
      && expiredAudit.filter(event => event.eventType === "action" && event.runId === expired.runId
        && event.outcome === "failed" && event.errorCode === "plugin_timeout").length === 1,
    { expiredReceipt, task: expiredTask, events: expiredAudit, original });
  const lateNative = await nativePluginInput(runtime, "click", "plugin-confirm-approve", "confirm");
  const late = await rawBridgeRequest(page, "plugin.interaction.resolve", {
    runId: expired.runId, interactionId: expired.pending.interactionId, decision: "approved",
  });
  const stale = await replay(expired.planId);
  recorder.check("expired native confirmation and consumed plan cannot be revived or automatically replayed",
    lateNative.status === "failed" && lateNative.error === "expected one current owned native plugin window"
      && late.type === "plugin.interaction.resolve" && late.payload?.status === "expired"
      && stale.error === "plugin_plan_stale" && unchanged(original, await rows())
      && unchanged(expiredTask, await task(expired.taskId)) && unchanged(expiredAudit, await audit()),
    { lateNative, late, stale });

  const fresh = await begin("native-approved");
  recorder.check("expiry recovery requests a fresh plan and confirmation", fresh.planId !== expired.planId
    && fresh.pending.interactionId !== expired.pending.interactionId);
  const approved = await nativePluginInput(runtime, "click", "plugin-confirm-approve", "approved");
  const receipt = await terminal();
  const confirmedTask = await task(fresh.taskId);
  const confirmedRows = await rows();
  const confirmedAudit = await audit();
  recorder.check("fresh plan recovers through one actual native approval and one durable success",
    approved.status === "completed" && receipt.result?.status === "succeeded" && receipt.result.taskId === fresh.taskId
      && receipt.result.receipt && confirmedTask.state === "succeeded"
      && confirmedRows.length === original.length && Object.values(confirmedRows[0]).includes("native-approved")
      && confirmedAudit.filter(event => event.eventType === "action" && event.runId === fresh.runId
        && event.outcome === "succeeded").length === 1, { approved, receipt, task: confirmedTask, rows: confirmedRows });
  const repeated = await replay(fresh.planId);
  recorder.check("repeated confirmed mutation.submit preserves its acknowledged receipt without another write or audit",
    repeated.error === "plugin_plan_stale"
      && unchanged(confirmedTask, await task(fresh.taskId)) && unchanged(confirmedRows, await rows())
      && unchanged(confirmedAudit, await audit()), { repeated, receipt, task: confirmedTask });
}

export async function exerciseApplicationWorkspaceIsolation(page, recorder, runtime, helpers, state, originalFrame) {
  const { rawBridgeRequest, createSimpleTable, applyProductMutation, openWorkspaceCenterFromSwitcher,
    beginWritableWorkspaceBootstrapCapture, waitForCapturedBridgeMessage, switchWorkspaceByName, selectTable } = helpers;
  const rows = async tableId => (await rawBridgeRequest(page, "query.page", {
    tableId, query: { filters: [], sorts: [], offset: 0, limit: 10 },
  })).payload.rows;
  const catalog = async projectKey => (await rawBridgeRequest(page, "plugin.catalog.list", { projectKey })).payload;
  const oldRows = await rows(state.tableId);
  const first = (await catalog(state.projectKey)).find(item => item.pluginId === A);
  const oldToken = await originalFrame.evaluate(() => window.name);
  const readControl = path.join(runtime.controlsDir, "attachment-replacement-source.txt");
  const previousRead = await fs.readFile(readControl, "utf8");
  const firstFile = path.join(runtime.controlsDir, "48-workspace-a-private.txt");
  const secondFile = path.join(runtime.controlsDir, "48-workspace-b-private.txt");
  await fs.writeFile(firstFile, "WORKSPACE_A_PRIVATE_FILE");
  await fs.writeFile(secondFile, "WORKSPACE_B_FILE");
  try {
    // Normal package inspection/install grants pickRead; the existing Host
    // test-mode picker only chooses these scoped synthetic files, never a path RPC.
    await fs.writeFile(readControl, `${firstFile}\n`);
    const held = await originalFrame.evaluate(() => call("file.pickRead", { mediaTypes: ["text/plain"] }));
    recorder.check("first workspace issues a real unread surface file grant", typeof held?.grantId === "string" && held.grantId.length > 0);
    const originalSession = await page.evaluate(() => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
    await openWorkspaceCenterFromSwitcher(page);
    await page.getByTestId("workspace-create").click();
    await page.getByTestId("workspace-flow-modal").locator("input").first().fill("E2E Plugin Scope Isolation");
    await page.getByTestId("workspace-flow-confirm").click();
    const target = page.getByTestId("workspace-center").getByRole("button", { name: /E2E Plugin Scope Isolation/ });
    await target.waitFor({ state: "visible", timeout: 60000 });
    await beginWritableWorkspaceBootstrapCapture(page, originalSession.sessionEpoch, "workspace.open");
    await target.click();
    const opened = await waitForCapturedBridgeMessage(page, 60000);
    const session = opened.payload.session;
    recorder.check("same-plugin isolation uses a second real writable workspace and newer epoch",
      session.workspaceId !== state.workspaceId && session.sessionEpoch > originalSession.sessionEpoch, { session });
    const projectKey = `local:${session.workspaceId.replaceAll("-", "").toLowerCase()}`;
    await page.getByTestId("nav-tables").click();
    const table = await createSimpleTable(page, "Other Application Tasks", "Text");
    await applyProductMutation(page, table.tableId, [{ kind: "insert", recordId: null,
      values: { [table.field.physicalName]: "WORKSPACE_B_PRIVATE_RECORD" } }], "application-other-workspace");
    await installApplication(page, runtime);
    const installed = (await catalog(projectKey)).find(item => item.pluginId === A);
    recorder.check("the same package has a distinct installation authority in each workspace",
      installed.pluginId === first.pluginId && installed.packageHash === first.packageHash
        && installed.installationId !== first.installationId && installed.projectKey === projectKey, { first, installed });
    let frame = await openApplication(page);
    const freshContext = await frame.evaluate(() => call("context.read"));
    const privateBefore = await frame.evaluate(() => call("storage.get", { key: "scope" }));
    const visibleRows = await frame.locator("#rows").innerText();
    recorder.check("new workspace has a working fresh plugin scope without old private settings",
      freshContext.projectKey === projectKey && freshContext.collection === table.tableId && privateBefore === null
        && visibleRows.includes("WORKSPACE_B_PRIVATE_RECORD") && !visibleRows.includes("synthetic original"), { freshContext, privateBefore, visibleRows });
    const denied = await frame.evaluate(async ({ oldToken, grantId, collection }) => {
      const attempt = async (method, params, token) => {
        try { return { accepted: true, value: await call(method, params, token) }; }
        catch (error) { return { code: error.code, message: error.message }; }
      };
      return {
        token: await attempt("context.read", {}, oldToken),
        grant: await attempt("file.read", { grantId }),
        table: await attempt("data.query", { contract: "vibetable.plugin-query.v2", collection, fields: ["id"], pageSize: 1 }),
      };
    }, { oldToken, grantId: held.grantId, collection: state.tableId });
    recorder.check("old workspace token, unread grant and table cannot authorize the same plugin in the new workspace",
      denied.token.code === "plugin_source_denied" && denied.grant.code === "plugin_file_denied"
        && denied.table.code === "plugin_read_denied" && originalFrame.isDetached()
        && !JSON.stringify(denied).includes("synthetic original") && !JSON.stringify(denied).includes("WORKSPACE_A_PRIVATE_FILE"), { denied, oldFrameDetached: originalFrame.isDetached() });
    await fs.writeFile(readControl, `${secondFile}\n`);
    const freshFile = await frame.evaluate(async () => {
      const grant = await call("file.pickRead", { mediaTypes: ["text/plain"] });
      const chunks = [];
      let bytes = 0;
      try {
        for (;;) {
          const block = await call("file.read", { grantId: grant.grantId });
          const chunk = atob(block.base64);
          if (typeof block.eof !== "boolean" || chunk.length > 256 * 1024
            || bytes + chunk.length > 1024 * 1024 || chunk.length === 0 && !block.eof)
            throw new Error("invalid workspace file block");
          chunks.push(chunk);
          bytes += chunk.length;
          if (block.eof) return { base64: btoa(chunks.join("")), eof: true, blocks: chunks.length, bytes };
        }
      } finally { await call("file.release", { grantId: grant.grantId }); }
    });
    recorder.check("new workspace file authorization reads only its newly selected source",
      freshFile.eof === true && Buffer.from(freshFile.base64, "base64").toString("utf8") === "WORKSPACE_B_FILE",
      { eof: freshFile.eof, blocks: freshFile.blocks, bytes: freshFile.bytes });
    await frame.evaluate(() => call("storage.set", { key: "scope", value: "workspace-b" }));
    const secondRows = await rows(table.tableId);
    const returned = await switchWorkspaceByName(page, "E2E Product Workspace", session.sessionEpoch);
    recorder.check("workspace UI switches back to the exact original identity", returned.payload.session.workspaceId === state.workspaceId);
    await page.getByTestId("nav-tables").click(); await selectTable(page, "Application Tasks");
    frame = await openApplication(page);
    recorder.check("returning to the original workspace preserves its private value and exact business rows",
      await frame.evaluate(() => call("storage.get", { key: "scope" })) === "plugin-a"
        && JSON.stringify(await rows(state.tableId)) === JSON.stringify(oldRows));
    return { frame, sessionEpoch: returned.payload.session.sessionEpoch,
      crossWorkspace: { workspaceId: session.workspaceId, projectKey, tableId: table.tableId,
        installationId: installed.installationId, revision: installed.revision, packageHash: installed.packageHash,
        version: installed.version, rows: secondRows } };
  } finally { await fs.writeFile(readControl, previousRead); }
}

export async function verifyOtherWorkspaceAfterLifecycle(page, recorder, helpers, state) {
  const { rawBridgeRequest, switchWorkspaceByName, selectTable } = helpers;
  const other = state.crossWorkspace;
  // Prefer the authoritative sessions already returned by switches; the diagnostics snapshot may stay null.
  const opened = await switchWorkspaceByName(page, "E2E Plugin Scope Isolation", state.sessionEpoch);
  recorder.check("lifecycle isolation reopens the same second workspace", opened.payload.session.workspaceId === other.workspaceId);
  const installed = (await rawBridgeRequest(page, "plugin.catalog.list", { projectKey: other.projectKey })).payload.find(item => item.pluginId === A);
  recorder.check("upgrading and uninstalling the original workspace cannot change the other installation",
    installed?.installationId === other.installationId && installed.revision === other.revision
      && installed.packageHash === other.packageHash && installed.version === other.version, { installed, expected: other });
  await page.getByTestId("nav-tables").click(); await selectTable(page, "Other Application Tasks");
  const frame = await openApplication(page);
  const context = await frame.evaluate(() => call("context.read"));
  const privateValue = await frame.evaluate(() => call("storage.get", { key: "scope" }));
  const rows = (await rawBridgeRequest(page, "query.page", { tableId: other.tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 10 } })).payload.rows;
  recorder.check("other workspace still has a usable plugin with its own unchanged settings and business rows",
    context.projectKey === other.projectKey && context.collection === other.tableId && privateValue === "workspace-b"
      && JSON.stringify(rows) === JSON.stringify(other.rows), { context, rows });
  const returned = await switchWorkspaceByName(page, "E2E Product Workspace", opened.payload.session.sessionEpoch);
  recorder.check("post-lifecycle return restores the original workspace", returned.payload.session.workspaceId === state.workspaceId);
  await page.getByTestId("nav-tables").click(); await selectTable(page, "Application Tasks");
  const catalog = (await rawBridgeRequest(page, "plugin.catalog.list", { projectKey: state.projectKey })).payload;
  recorder.check("original workspace keeps its explicit uninstall and the second plugin", !catalog.some(item => item.pluginId === A) && catalog.some(item => item.pluginId === B));
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
    // A missed auto-open bootstrap leaves the diagnostics snapshot null; keep the authoritative switch epoch.
    state = { ...state, sessionEpoch: switched.result.sessionEpoch };
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
  if (!statePath) {
    const isolated = await exerciseApplicationWorkspaceIsolation(page, recorder, runtime, helpers, state, frame);
    frame = isolated.frame;
    state.crossWorkspace = isolated.crossWorkspace;
    state.sessionEpoch = isolated.sessionEpoch;
  }
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
  await exerciseApplicationConfirmationExpiry(page, frame, recorder, runtime, helpers, state);
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
  await verifyOtherWorkspaceAfterLifecycle(page, recorder, helpers, state);
  frame = await openApplication(page, B);
  await runApplicationPrint(page, frame, recorder, runtime, () => openApplication(page, B));
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
