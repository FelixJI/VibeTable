import fs from "node:fs/promises";
import path from "node:path";
import { installApplication, openApplication, nativePluginInput, selectPlugin } from "./plugin_applications.mjs";
import { picker } from "./plugin_capability_services.mjs";
const ID = "com.vibetable.examples.record-manager";
const ACTION = "open-manager";
const ENTRY = `nav-plugin-app-${ID}/${ACTION}`;

// Navigation can be projected by SSE before the lifecycle request commits.
// Wait for the existing UI command to settle, then verify its authority receipt.
export async function setRecordManagerEnabled(page, recorder, rawBridgeRequest, projectKey, enabled) {
  const read = async () => (await rawBridgeRequest(page, "plugin.catalog.list", { projectKey }))
    .payload.find(plugin => plugin.pluginId === ID);
  const before = await read();
  if (!before?.installationId || before.status !== (enabled ? "disabled" : "enabled"))
    throw new Error("record-manager lifecycle source is not the expected installation state");
  await page.getByTestId("plugin-toggle").click();
  await page.waitForFunction(expected => {
    const toggle = document.querySelector('[data-testid="plugin-toggle"]');
    return toggle instanceof HTMLButtonElement && !toggle.disabled
      && toggle.classList.contains("enabled") === expected;
  }, enabled, { timeout: 30000 });
  const after = await read();
  recorder.check(`record-manager ${enabled ? "enable" : "disable"} completes its exact lifecycle revision before navigation`,
    after?.installationId === before.installationId && after.revision === before.revision + 1
      && after.packageHash === before.packageHash && after.status === (enabled ? "enabled" : "disabled"),
    { before, after });
  return after;
}

// The stale create form can hold the same value until the authoritative edit query settles.
export async function waitForRecordEditForm(frame, fieldId, authoritativeValue) {
  await frame.waitForFunction(({ fieldId, value }) => {
    const form = document.querySelector("#record-form");
    const save = form?.querySelector("#record-save");
    if (!(save instanceof HTMLButtonElement) || save.textContent !== "预览并保存") return false;
    const input = form.querySelector(`input[data-field-id="${fieldId}"]`);
    return input instanceof HTMLInputElement && input.value === value;
  }, { fieldId, value: authoritativeValue }, { timeout: 30000 });
}

/** Independent same-package WPF scenario; the original scenario 52 keeps its budget. */
export async function runRecordManagerProduct(page, recorder, runtime, helpers) {
  const evidence = { authority: [], native: [] };
  const save = () => fs.writeFile(path.join(runtime.evidenceDir, "53-record-manager.json"), JSON.stringify(evidence, null, 2));
  try {
    const opened = await helpers.waitForShell(page, recorder, { requireDatabaseOpened: true });
    const projectKey = opened.payload.projectKey;
    await page.getByTestId("nav-tables").click();
    const table = await helpers.createSimpleTable(page, "Record Manager", "Title");
    const { tableId, field } = table;
    const authority = async () => {
      const response = await helpers.rawBridgeRequest(page, "query.page", { tableId, query: { filters: [], sorts: [], offset: 0, limit: 100 } });
      const rows = response.payload.rows; evidence.authority.push(rows); await save(); return rows;
    };
    await helpers.applyProductMutation(page, tableId, [{ kind: "insert", recordId: null, values: { [field.physicalName]: "  record-manager  padded  " } }], "record-manager-seed");
    await installApplication(page, runtime, "record-manager", ID, ACTION);
    let frame = await openApplication(page, ID, ACTION);
    await frame.locator("#overview-body").filter({ hasText: "权威记录数：1 条" }).waitFor({ timeout: 30000 });
    evidence.overview = await frame.locator("#overview-body").innerText();
    recorder.check("record-manager installation contributes a ready application with authoritative overview", evidence.overview.includes("授权字段") && evidence.overview.includes("权威记录数：1 条"));
    await page.screenshot({ path: path.join(runtime.evidenceDir, "53-record-manager-overview.png"), fullPage: true });
    const confirm = async (label, approved = true) => {
      await page.locator(".host-confirmation").waitFor({ timeout: 30000 });
      const receipt = await nativePluginInput(runtime, "click", approved ? "plugin-confirm-approve" : "plugin-confirm-reject", approved ? "approved" : "rejected");
      evidence.native.push({ label, receipt });
      recorder.check(`${label} has real owned native confirmation`, receipt.status === "completed", { receipt });
      await fs.copyFile(path.join(runtime.evidenceDir, approved ? "48-native-approved.png" : "48-native-rejected.png"), path.join(runtime.evidenceDir, `53-confirm-${label}.png`));
      await frame.locator("#status").filter({ hasText: approved ? "已提交。" : "宿主拒绝，未写入。" }).waitFor({ timeout: 30000 });
      await save();
    };
    await frame.locator('nav a[data-page="detail"]').click();
    const input = () => frame.locator(`#record-form input[data-field-id="${field.fieldId}"]`);
    await input().fill("record-manager-created");
    await frame.getByRole("button", { name: "预览并创建", exact: true }).click(); await confirm("create");
    let rows = await authority();
    const created = rows.find(row => row[field.physicalName] === "record-manager-created");
    recorder.check("record-manager create reaches authority exactly once", rows.length === 2 && !!created);
    await frame.locator('nav a[data-page="list"]').click();
    const createdRow = frame.locator("#list-body tr").filter({ hasText: "record-manager-created" }); await createdRow.waitFor(); await createdRow.click();
    await waitForRecordEditForm(frame, field.fieldId, "record-manager-created");
    await input().fill("record-manager-updated");
    await frame.getByRole("button", { name: "预览并保存", exact: true }).click(); await confirm("update");
    rows = await authority();
    recorder.check("record-manager edits preserve exact record identity", rows.length === 2 && rows.some(row => row.id === created.id && row[field.physicalName] === "record-manager-updated"));
    await frame.getByRole("button", { name: "预览批量规范化文本", exact: true }).click(); await confirm("normalize");
    rows = await authority();
    recorder.check("record-manager normalization changes raw whitespace only", rows.length === 2 && rows.some(row => row[field.physicalName] === "record-manager padded"));
    // A real external authority write must reach the live subscription; no swallowed wait.
    await helpers.applyProductMutation(page, tableId, [{ kind: "insert", recordId: null, values: { [field.physicalName]: "external-change" } }], "record-manager-invalidation");
    await frame.locator("#stale:not([hidden])").waitFor({ timeout: 15000 });
    await frame.locator('nav a[data-page="list"]').click(); await frame.locator("#list-body tr").filter({ hasText: "external-change" }).waitFor();
    recorder.check("record-manager invalidation refreshes the real list", (await frame.locator("#status").innerText()).includes("权威总数 3 条"));
    await fs.writeFile(path.join(runtime.controlsDir, "export-target.txt"), "native-dialog\n");
    await frame.locator("#export").click(); evidence.exportPicker = await picker(runtime, "export");
    await frame.locator("#status").filter({ hasText: "导出完成：3 行" }).waitFor({ timeout: 30000 });
    const csv = await fs.readFile(path.join(runtime.controlsDir, "52-export.csv"), "utf8");
    evidence.export = { csv, rows: await authority() };
    recorder.check("record-manager export writes actual selected bytes", csv.includes(field.fieldId) && csv.includes("record-manager-updated") && csv.includes("record-manager padded") && csv.includes("external-change"));
    await page.screenshot({ path: path.join(runtime.evidenceDir, "53-record-manager-list.png"), fullPage: true });
    // Reopen the application through its real navigation, then exercise destructive rejection and approval.
    await page.getByTestId("plugin-app-return").click(); frame = await openApplication(page, ID, ACTION);
    await frame.locator("#overview-body").filter({ hasText: "权威记录数：3 条" }).waitFor({ timeout: 30000 });
    await frame.locator('nav a[data-page="list"]').click(); await frame.locator("#list-body tr").filter({ hasText: "record-manager-updated" }).click();
    await frame.getByRole("button", { name: "删除（宿主确认）", exact: true }).click(); await confirm("delete-rejected", false);
    recorder.check("record-manager destructive rejection preserves authority", (await authority()).length === 3);
    await frame.locator("#reload").click();
    await frame.getByRole("button", { name: "删除（宿主确认）", exact: true }).click(); await confirm("delete-approved");
    rows = await authority();
    recorder.check("record-manager destructive approval deletes exactly the selected identity", rows.length === 2 && !rows.some(row => row.id === created.id));
    await page.getByTestId("plugin-app-manage").click(); await selectPlugin(page, ID);
    await setRecordManagerEnabled(page, recorder, helpers.rawBridgeRequest, projectKey, false);
    await page.getByTestId(ENTRY).waitFor({ state: "hidden", timeout: 30000 });
    recorder.check("record-manager disable retires viewport and navigation", await page.getByTestId("plugin-app-viewport").count() === 0 || !await page.getByTestId("plugin-app-viewport").isVisible());
    await setRecordManagerEnabled(page, recorder, helpers.rawBridgeRequest, projectKey, true);
    await page.getByTestId(ENTRY).waitFor({ timeout: 30000 });
    frame = await openApplication(page, ID, ACTION); await frame.locator("#overview-body").filter({ hasText: "权威记录数：2 条" }).waitFor({ timeout: 30000 });
    recorder.check("record-manager re-enabled application reads persisted authority", (await authority()).length === 2);
    await page.screenshot({ path: path.join(runtime.evidenceDir, "53-record-manager-reopened.png"), fullPage: true });
    await page.getByTestId("plugin-app-return").click();
    return { tableId, nodeWorkerDisabled: true };
  } finally { await save(); }
}
