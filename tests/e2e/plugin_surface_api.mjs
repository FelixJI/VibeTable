import fs from "node:fs/promises";
import path from "node:path";

export async function runPluginSurfaceApi(page, recorder, runtime, helpers) {
  const { waitForShell, createSimpleTable, applyProductMutation, rawBridgeRequest,
    beginBridgeMessageCapture, waitForCapturedBridgeMessage } = helpers;
  const opened = await waitForShell(page, recorder, { requireDatabaseOpened: true });
  const projectKey = opened.payload.projectKey;
  await page.getByTestId("nav-tables").click();
  const table = await createSimpleTable(page, "Native Surface 550", "Text");
  const field = table.field.physicalName;
  const seeded = await applyProductMutation(page, table.tableId, Array.from({ length: 550 }, (_, i) => ({
    kind: "insert", recordId: null, values: { [field]: `synthetic-${i}` },
  })), "surface-seed-550");
  const firstId = seeded.payload.affectedRows.map(row => row.recordId).sort()[0];
  async function value() {
    const response = await rawBridgeRequest(page, "query.page", {
      tableId: table.tableId, query: { filters: [{ field: "id", operator: "eq", value: firstId }], sorts: [], offset: 0, limit: 1 },
    });
    return response.payload.rows[0]?.[field];
  }
  const original = await value();
  const sourceControl = path.join(runtime.controlsDir, "plugin-source.txt");
  async function install(archive, id) {
    await fs.writeFile(sourceControl, `${path.join(runtime.controlsDir, archive)}\n`);
    await page.getByTestId("nav-plugins").click();
    await page.getByTestId("plugin-install-package").click();
    await page.getByTestId("plugin-install-plan").waitFor({ timeout: 30_000 });
    await page.getByTestId("plugin-install-commit").click();
    const row = page.locator("button.plugin-row").filter({ hasText: id });
    await row.waitFor({ timeout: 60_000 });
    await row.click();
    await page.waitForFunction(() => document.querySelector('[data-testid="plugin-toggle"]')?.classList.contains("enabled"));
    return row;
  }
  await install("worker-disabled-probe.vtplugin", "com.vibetable.e2e.mutation-boundary");
  const legacy = await rawBridgeRequest(page, "plugin.action.describe", {
    projectKey, pluginId: "com.vibetable.e2e.mutation-boundary", actionId: "allowed-plan",
    context: { contract: "vibetable.command-context.v1", projectKey, collection: table.tableId, selectedKeys: [], querySnapshot: null, locale: "zh-CN", theme: "light", density: "comfortable", user: {}, hostVersion: "unknown" },
  });
  recorder.check("old Node executor is explicitly unavailable in this Host process",
    legacy.payload?.available === false && legacy.payload.reasons.includes("plugin_worker_unavailable"), { legacy });
  await install("surface-data-editor.vtplugin", "com.vibetable.examples.surface-data-editor");
  await page.locator(".action-row").filter({ hasText: "open-editor" }).locator("button.run-button").click();
  const frame = page.frameLocator(".surface-shell iframe");
  const status = frame.locator("#status");
  const save = frame.locator("#save");
  await save.waitFor({ timeout: 30_000 });
  async function waitEnabled() {
    const until = Date.now() + 30_000;
    while (await save.isDisabled() && Date.now() < until) await page.waitForTimeout(100);
    if (await save.isDisabled()) throw new Error(`surface failed to load: ${await status.innerText()}`);
  }
  await waitEnabled();
  recorder.check("real isolated SDK query completes 200/200/150 pages without Worker",
    (await status.innerText()).includes("550") && (await status.innerText()).includes("200/200/150"), { status: await status.innerText() });
  await page.screenshot({ path: path.join(runtime.evidenceDir, "47-surface-read.png"), fullPage: true });
  for (const decision of ["rejected", "cancelled", "approved"]) {
    await frame.locator("#draft").fill(`native-${decision}`);
    await beginBridgeMessageCapture(page, ["plugin.task.changed"]);
    await save.click();
    const started = await waitForCapturedBridgeMessage(page, 30_000);
    const taskId = started.payload?.snapshot?.taskId;
    recorder.check(`surface ${decision} receives Host-owned task`, Boolean(taskId), { started });
    const confirmation = page.locator(".host-confirmation");
    await confirmation.waitFor({ timeout: 30_000 });
    recorder.check(`surface ${decision} cannot approve its own plan`,
      (await confirmation.innerText()).includes("HOST FINAL WRITE CONFIRMATION"), { confirmation: await confirmation.innerText() });
    if (decision === "approved") {
      await page.screenshot({ path: path.join(runtime.evidenceDir, "47-surface-confirm.png"), fullPage: true });
      await page.getByTestId("plugin-surface-confirm-approve").click();
    } else if (decision === "rejected") await confirmation.getByRole("button", { name: "拒绝", exact: true }).click();
    else await frame.locator("#cancel").click();
    await waitEnabled();
    const task = await rawBridgeRequest(page, "plugin.task.get", { taskId });
    recorder.check(`surface ${decision} has authoritative terminal and expected data`,
      task.payload?.state === (decision === "approved" ? "succeeded" : "cancelled")
        && await value() === (decision === "approved" ? "native-approved" : original), { task, current: await value() });
    const audit = await rawBridgeRequest(page, "plugin.audit.list", {
      projectKey, pluginId: "com.vibetable.examples.surface-data-editor",
    });
    const events = Array.isArray(audit.payload) ? audit.payload : [];
    recorder.check(`surface ${decision} has a durable terminal linked to its Host run`,
      events.some(event => event.eventType === "action"
        && event.runId === task.payload.runId && event.actionId === "open-editor"
        && event.details?.taskId === taskId
        && event.outcome === (decision === "approved" ? "succeeded" : decision)), { events, taskId });
  }
  await page.screenshot({ path: path.join(runtime.evidenceDir, "47-surface-saved.png"), fullPage: true });
  recorder.check("surface task preserves installation identity", (await frame.locator("#result").innerText()).includes("保存成功"));

  async function probeWebRtc() {
    const surface = page.frames().find(candidate => candidate.url().includes(".plugins.vibetable.local"));
    if (!surface) throw new Error("surface frame is not attached for the WebRTC probe");
    // Only typeof/descriptor checks; no RTC connection, ICE, or SDP is created.
    const probe = await surface.evaluate(() => {
      const outcome = {
        standard: typeof RTCPeerConnection,
        alias: typeof webkitRTCPeerConnection,
        redefine: "unknown",
        child: null,
        childAlias: null,
        childDescriptor: null,
        nested: null,
      };
      try {
        Object.defineProperty(window, "RTCPeerConnection", { value: function () {} });
        outcome.redefine = "redefined";
      } catch {
        outcome.redefine = "blocked";
      }
      const descriptor = Object.getOwnPropertyDescriptor(window, "RTCPeerConnection");
      // The document-created injection must cover synchronously created child
      // documents too; append one source-less iframe, read it in this same
      // script turn, and remove only that element before returning.
      const created = document.createElement("iframe");
      document.body.appendChild(created);
      try {
        const child = created.contentWindow;
        outcome.child = typeof child.RTCPeerConnection;
        outcome.childAlias = typeof child.webkitRTCPeerConnection;
        outcome.childDescriptor = (() => {
          const described = Object.getOwnPropertyDescriptor(child, "RTCPeerConnection");
          return described ? { value: String(described.value), writable: described.writable, configurable: described.configurable } : null;
        })();
        const nestedCreated = child.document.createElement("iframe");
        child.document.body.appendChild(nestedCreated);
        try { outcome.nested = typeof nestedCreated.contentWindow.RTCPeerConnection; }
        finally { nestedCreated.remove(); }
      } finally {
        created.remove();
      }
      outcome.descriptor = descriptor ? { value: String(descriptor.value), writable: descriptor.writable, configurable: descriptor.configurable } : null;
      return outcome;
    });
    recorder.check("real plugin frame cannot construct or redefine WebRTC",
      probe.standard === "undefined" && probe.alias === "undefined" && probe.redefine === "blocked",
      { probe });
    recorder.check("synchronous child document inherits the WebRTC lock",
      probe.child === "undefined" && probe.childAlias === "undefined" && probe.nested === "undefined"
        && probe.childDescriptor?.value === "undefined" && probe.childDescriptor.writable === false
        && probe.childDescriptor.configurable === false,
      { probe });
  }
  await probeWebRtc();

  const firstToken = await page.locator(".surface-shell iframe").getAttribute("name");
  await page.getByRole("button", { name: "关闭插件页面", exact: true }).click();
  await page.locator(".action-row").filter({ hasText: "open-editor" }).locator("button.run-button").click();
  const reopenedFrame = page.frameLocator(".surface-shell iframe");
  await reopenedFrame.locator("#save").waitFor({ timeout: 30_000 });
  const reopenDeadline = Date.now() + 30_000;
  let reopenedStatus = "";
  while (Date.now() < reopenDeadline) {
    reopenedStatus = await reopenedFrame.locator("#status").innerText();
    if (reopenedStatus.includes("550")) break;
    await page.waitForTimeout(100);
  }
  const secondToken = await page.locator(".surface-shell iframe").getAttribute("name");
  recorder.check("closed surface reopens with a fresh token that still reads",
    Boolean(firstToken) && Boolean(secondToken) && firstToken !== secondToken && reopenedStatus.includes("550"),
    { firstToken, secondToken, reopenedStatus });
  await probeWebRtc();
  await page.getByRole("button", { name: "关闭插件页面", exact: true }).click();
  return { tableId: table.tableId, firstId, nodeWorkerDisabled: true, pluginId: "com.vibetable.examples.surface-data-editor" };
}
