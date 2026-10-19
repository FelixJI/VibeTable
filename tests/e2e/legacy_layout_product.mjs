import fs from "node:fs/promises";
import path from "node:path";
import { isDeepStrictEqual } from "node:util";
import { installApplication, openApplication } from "./plugin_applications.mjs";
import { runApplicationPrint } from "./plugin_application_print.mjs";

export async function seedLegacyLayoutProduct(page, recorder, runtime, h) {
  const opened = await h.waitForShell(page, recorder, { requireDatabaseOpened: true });
  await page.getByTestId("nav-tables").click();
  const { tableId, field } = await h.createSimpleTable(page, "Historical business records", "Title");
  await h.applyProductMutation(page, tableId, [
    { kind: "insert", recordId: null, values: { [field.physicalName]: "Historical North" } },
    { kind: "insert", recordId: null, values: { [field.physicalName]: "Historical South" } },
  ], "legacy-preservation-seed");
  const attachmentField = await h.createV2Field(page, tableId, "Historical attachment", "file");
  await h.selectTable(page, "Historical business records");
  const rows = await h.rawBridgeRequest(page, "query.page", { tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 100 } });
  const recordId = rows.payload?.rows?.[0]?.id;
  if (!recordId) throw new Error("historical attachment requires an authoritative record ID");
  const cell = page.locator(`.tabulator-cell[tabulator-field="${attachmentField.physicalName}"]`).first();
  await cell.dblclick();
  await page.getByTestId("attachment-panel").waitFor();
  await page.getByTestId("attachment-upload").click();
  await page.getByTestId("attachment-panel").waitFor({ state: "hidden", timeout: 30000 });
  const attached = await h.waitForAttachmentList(page,
    { tableId, recordId, fieldId: attachmentField.fieldId }, items => items.length === 1);
  recorder.check("historical business record owns a real managed attachment", attached.payload.attachments.length === 1);
  const session = await page.evaluate(() => window.__vibetableE2EBridgeDiagnostics?.workspaceSession);
  recorder.check("synthetic workspace exposes its stable identity", !!session?.workspaceId);
  return { workspaceId: session.workspaceId, tableId, field, projectKey: opened.payload.projectKey };
}

export async function rejectLegacyRoutes(page, recorder, h) {
  for (const method of ["interface.listRequested", "interface.loadRequested", "interface.commitRequested",
    "interface.deleteRequested", "dashboard.listRequested", "dashboard.queryRequested",
    "dashboard.saveRequested", "dashboard.deleteRequested", "dashboard.printRequested"]) {
    const rejected = await h.rawBridgeRequest(page, method, {}, 20000, ["operation.failed"]);
    recorder.check(`${method} explicitly rejects retired execution`, rejected.payload?.code === "CAPABILITY_RETIRED", { rejected });
    await h.acknowledgeExpectedBridgeFailure(page, rejected);
  }
  recorder.check("visual builder navigation is absent", await page.getByTestId("nav-interfaces").count() === 0
    && await page.getByTestId("nav-dashboard").count() === 0);
}

export async function exportLegacyProduct(page, recorder, runtime, h) {
  const target = path.join(runtime.controlsDir, "legacy-preserved.json");
  await fs.writeFile(path.join(runtime.controlsDir, "legacy-layout-export-target.txt"), target);
  await fs.writeFile(path.join(runtime.controlsDir, "legacy-layout-export-cancel.txt"), "cancel");
  const cancelled = await h.rawBridgeRequest(page, "legacyLayout.exportRequested", {}, 20000, ["legacyLayout.exportCompleted"]);
  recorder.check("native preservation cancellation writes no output", cancelled.payload?.cancelled === true
    && !(await fs.stat(target).then(() => true, error => { if (error.code === "ENOENT") return false; throw error; })));
  await fs.rm(path.join(runtime.controlsDir, "legacy-layout-export-cancel.txt"));
  for (const payload of [{ path: target }, { content: "forged material" }]) {
    const rejected = await h.rawBridgeRequest(page, "legacyLayout.exportRequested", payload, 20000, ["operation.failed"]);
    recorder.check("renderer cannot replace authority bytes or choose a filesystem path", rejected.payload?.code === "BAD_PAYLOAD", { rejected });
    await h.acknowledgeExpectedBridgeFailure(page, rejected);
  }
  const completed = await h.rawBridgeRequest(page, "legacyLayout.exportRequested", {}, 20000, ["legacyLayout.exportCompleted"]);
  recorder.check("Host settles JSON preservation with only a display name", completed.payload?.cancelled === false
    && completed.payload?.outputDisplayName === "legacy-preserved.json", { completed });
  return JSON.parse(await fs.readFile(target, "utf8"));
}

export async function resumeLegacyLayoutProduct(page, recorder, runtime, h, statePath) {
  const state = JSON.parse(await fs.readFile(statePath, "utf8"));
  await h.activateRestartedWorkspace(page, state.workspaceId);
  await page.getByTestId("nav-tables").click();
  await h.selectTable(page, "Historical business records");
  const before = await h.requestStorageProof(runtime, state.tableId, 30000, true);
  await h.verifyLegacySidecarRecovery(page, recorder, runtime, state.tableId);
  await page.getByTestId("nav-plugins").click();
  await page.getByTestId("legacy-layout-counts").waitFor({ timeout: 30000 });
  const report = await h.rawBridgeRequest(page, "legacyLayout.reportRequested", {}, 20000, ["legacyLayout.reportLoaded"]);
  recorder.check("old workspace reports every original layout and damaged record", report.payload.interfaces === 3
    && report.payload.dashboards === 1 && report.payload.panels === 1, { report });
  recorder.check("unsupported content has actionable item/path diagnostics", report.payload.diagnostics.some(item => item.code === "legacy.payload_unparseable")
    && report.payload.diagnostics.some(item => item.path?.endsWith(".unknownExpression"))
    && report.payload.diagnostics.some(item => item.code === "legacy.plugin_action_unsupported"));
  await page.screenshot({ path: path.join(runtime.evidenceDir, "16-legacy-layout-warning.png"), fullPage: true });
  await rejectLegacyRoutes(page, recorder, h);
  const snapshot = await exportLegacyProduct(page, recorder, runtime, h);
  for (const [key, collection] of [["interfaces", "vibetable_interfaces"], ["dashboards", "vibetable_dashboards"], ["panels", "vibetable_panels"]]) {
    const expected = before.legacyPreservation[collection];
    recorder.check(`${collection} export retains raw bytes, storage identity and parent`, snapshot[key].length === expected.length
      && snapshot[key].every(item => expected.some(row => row.id === item.recordId && row.logical_id === item.logicalId
        && row.payload_json === item.rawPayload && (row.parent_id ?? "") === item.parentId)), { snapshot: snapshot[key] });
  }
  const pluginId = "com.vibetable.examples.record-manager";
  const entry = page.getByTestId(`nav-plugin-app-${pluginId}/open-manager`);
  if (await entry.count() === 0) await installApplication(page, runtime, "record-manager", pluginId, "open-manager");
  const frame = await openApplication(page, pluginId, "open-manager");
  await frame.locator("#overview-body").filter({ hasText: "权威记录数：2 条" }).waitFor({ timeout: 30000 });
  await frame.locator('nav a[data-page="list"]').click();
  await frame.locator("#list-body").filter({ hasText: "Historical North" }).waitFor({ timeout: 30000 });
  recorder.check("replacement code plugin reads the original stable business table", (await frame.locator("#list-body").innerText()).includes("Historical South"));
  await page.screenshot({ path: path.join(runtime.evidenceDir, "16-replacement-business-page.png"), fullPage: true });
  await page.getByTestId("plugin-app-return").click();
  const after = await h.requestStorageProof(runtime, state.tableId, 30000, true);
  recorder.check("detection, cancellation, rejected writes, export and plugin adoption preserve original semantics", isDeepStrictEqual(before.legacyPreservation, after.legacyPreservation), { before, after });
  recorder.check("preservation leaves the existing audit ledger, outbox and idempotency evidence unchanged",
    isDeepStrictEqual(before.counts, after.counts) && isDeepStrictEqual(before.auditLedger, after.auditLedger), { before: before.counts, after: after.counts });
  await fs.writeFile(path.join(runtime.evidenceDir, "16-legacy-reconciliation.json"), JSON.stringify({ before, after, snapshot }, null, 2));
  return { workspaceId: state.workspaceId, tableId: state.tableId, field: state.field, projectKey: state.projectKey };
}

export async function runRetiredNativeOutput(page, recorder, runtime, h) {
  await h.waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  const table = await h.createSimpleTable(page, "Preservation print context", "Text");
  await h.applyProductMutation(page, table.tableId, [{ kind: "insert", recordId: null,
    values: { [table.field.physicalName]: "synthetic print record" } }], "preservation-print-seed");
  await page.getByTestId("nav-plugins").click();
  await exportLegacyProduct(page, recorder, runtime, h);
  await installApplication(page, runtime);
  const frame = await openApplication(page);
  await runApplicationPrint(page, frame, recorder, runtime, () => openApplication(page));
}
