import path from "node:path";
import fs from "node:fs/promises";

const query = { filters: [], sorts: [], offset: 0, limit: 100 };

export async function runAutoNumberJourney(page, recorder, runtime, ports) {
  const { createEmptyTable, createSimpleTable, closeFieldSettingsDrawer, selectVisibleNOption,
    rawBridgeRequest, applyProductMutation, selectTable, waitForVisibleRowCount,
    openWorkspaceCenterFromSwitcher, replicaUiMethod, beginWritableWorkspaceBootstrapCapture,
    waitForCapturedBridgeMessage, acknowledgeExpectedBridgeFailure, insertRowFromToolbar,
    applyV2FieldChange, chooseToolbarMore, parseCsv } = ports;
  const populated = await createSimpleTable(page, "E2E AutoNumber Backfill", "Title");
  await applyProductMutation(page, populated.tableId, Array.from({ length: 3 }, () => ({
    kind: "insert", recordId: null, values: {},
  })), "number-backfill-seed");
  await selectTable(page, "E2E AutoNumber Backfill");
  await waitForVisibleRowCount(page, 3);
  const openNew = async () => {
    await page.locator(`.tabulator-col[tabulator-field="${populated.field.physicalName}"] .tabulator-col-title`).click({ button: "right" });
    await page.locator(".n-dropdown-option-body:visible").getByText("在右侧新增字段", { exact: true }).click();
    await page.getByTestId("field-display-name").waitFor();
  };
  const configure = async () => {
    await page.getByTestId("field-display-name").locator("input").fill("合同编号");
    await selectVisibleNOption(page, "field-logical-type", "自动编号");
    await page.locator('.field-settings-drawer [data-name="advanced"]').click();
    await page.getByTestId("auto-number-prefix").locator("input").fill("HT-");
    await page.getByTestId("field-plan-button").click();
    await page.getByTestId("auto-number-backfill-preview").waitFor();
  };
  await openNew();
  await configure();
  const preview = await page.getByTestId("auto-number-backfill-preview").innerText();
  recorder.check("autoNumber preview exposes deterministic count, id order and examples",
    preview.includes("3") && preview.includes("id") && preview.includes("HT-000001"), { preview });
  await closeFieldSettingsDrawer(page);
  const cancelled = await rawBridgeRequest(page, "schema.getTable", { tableId: populated.tableId });
  recorder.check("closing the numbering preview leaves schema and data untouched",
    !cancelled.payload.fields.some(field => field.logicalType === "autoNumber"), { cancelled });
  await openNew();
  await configure();
  await page.getByTestId("field-apply-button").click();
  await page.getByTestId("auto-number-prefix").locator("input:disabled").waitFor();
  await closeFieldSettingsDrawer(page);
  const backfilled = await rawBridgeRequest(page, "schema.getTable", { tableId: populated.tableId });
  const backfillField = backfilled.payload.fields.find(field => field.logicalType === "autoNumber");
  const backfillRows = await rawBridgeRequest(page, "query.page", { tableId: populated.tableId, query });
  const ordered = [...backfillRows.payload.rows].sort((a, b) => a.id.localeCompare(b.id, "en"));
  recorder.check("confirmed autoNumber backfill follows the frozen id ordering",
    ordered.length === 3 && ordered.every((row, i) => row[backfillField.identity.physicalName] === `HT-${String(i + 1).padStart(6, "0")}`), { ordered });

  const tableId = await createEmptyTable(page, "E2E AutoNumber Only");
  await configure();
  await page.getByTestId("field-apply-button").click();
  await page.getByTestId("auto-number-prefix").locator("input:disabled").waitFor();
  await closeFieldSettingsDrawer(page);
  const described = await rawBridgeRequest(page, "schema.getTable", { tableId });
  const field = described.payload.fields.find(item => item.logicalType === "autoNumber");
  recorder.check("autoNumber is a real readonly string field with a stable identity",
    field.identity.fieldId.startsWith("fld_") && field.storage.kind === "pocketbase-text"
      && field.display.kind === "readonly" && field.autoNumber.prefix === "HT-", { field });
  await page.getByTestId("grid-add-first-row").click();
  await waitForVisibleRowCount(page, 1);
  const numberCell = page.locator(`.tabulator-cell[tabulator-field="${field.identity.physicalName}"]`).filter({ hasText: "HT-000001" });
  await numberCell.waitFor();
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"], { origin: "https://app.vibetable.local" });
  await page.evaluate(async () => navigator.clipboard.writeText("number-copy-sentinel"));
  await numberCell.click();
  await page.keyboard.press("Control+c");
  await page.waitForFunction(async () => (await navigator.clipboard.readText()).includes("HT-000001"));
  const copied = await page.evaluate(async () => navigator.clipboard.readText());
  recorder.check("copy preserves the original readonly business number and leading zeroes",
    copied.trim() === "HT-000001", { copied });
  const exportTarget = path.join(runtime.controlsDir, "auto-number.csv");
  await fs.writeFile(path.join(runtime.controlsDir, "export-target.txt"), `${exportTarget}\r\n`, "utf8");
  await chooseToolbarMore(page, "export-csv");
  await page.getByTestId("export-lookup-panel").waitFor();
  await page.getByTestId("export-lookup-confirm").click();
  let exported = "";
  const exportDeadline = Date.now() + 60_000;
  while (Date.now() < exportDeadline) {
    try {
      exported = await fs.readFile(exportTarget, "utf8");
      if (exported.includes("HT-000001")) break;
    } catch (error) {
      if (error?.code !== "ENOENT") throw error;
    }
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  const exportedRows = parseCsv(exported);
  const numberIndex = exportedRows[0]?.indexOf(field.identity.physicalName) ?? -1;
  recorder.check("default raw CSV export preserves the business number string",
    numberIndex >= 0 && exportedRows[1]?.[numberIndex] === "HT-000001", { exportedRows });
  await insertRowFromToolbar(page);
  await waitForVisibleRowCount(page, 2);
  await applyProductMutation(page, tableId, Array.from({ length: 2 }, () => ({
    kind: "insert", recordId: null, values: {},
  })), "number-batch");
  const rows = await rawBridgeRequest(page, "query.page", { tableId, query });
  const values = rows.payload.rows.map(row => row[field.identity.physicalName]).sort();
  recorder.check("a table containing only autoNumber supports UI insertion and one atomic batch",
    JSON.stringify(values) === JSON.stringify(["HT-000001", "HT-000002", "HT-000003", "HT-000004"]), { values });
  const filtered = await rawBridgeRequest(page, "query.page", { tableId, query: {
    ...query, filters: [{ field: field.identity.physicalName, operator: "contains", value: "HT-" }],
    sorts: [{ field: field.identity.physicalName, direction: "desc" }],
  } });
  recorder.check("sorting and text filtering preserve every number and record identity",
    filtered.payload.rows.length === 4
      && filtered.payload.rows[0][field.identity.physicalName] === "HT-000004"
      && filtered.payload.rows.every(row => rows.payload.rows.some(original => original.id === row.id
        && original[field.identity.physicalName] === row[field.identity.physicalName])), { filtered });
  const renamed = await applyV2FieldChange(page, tableId, field.identity.fieldId, "update", {
    mutateDraft: draft => ({ ...draft, displayName: "Renamed contract number" }),
  });
  const afterRename = await rawBridgeRequest(page, "query.page", { tableId, query });
  recorder.check("renaming keeps the same field, business values and record ids",
    renamed.applied?.payload?.definition?.identity?.fieldId === field.identity.fieldId
      && afterRename.payload.rows.every(row => rows.payload.rows.some(original => original.id === row.id
        && original[field.identity.physicalName] === row[field.identity.physicalName])), { renamed, afterRename });
  const highest = rows.payload.rows.find(row => row[field.identity.physicalName] === "HT-000004");
  await applyProductMutation(page, tableId, [{ kind: "delete", recordId: highest.id }], "number-delete-highest");
  const override = await applyProductMutation(page, tableId, [{ kind: "update", recordId: rows.payload.rows[0].id,
    values: { [field.identity.physicalName]: "HT-999999" } }], "number-override", true);
  recorder.check("manual numbering override is rejected at the authority boundary",
    override.type === "operation.failed", { override });
  await acknowledgeExpectedBridgeFailure(page, override);
  await page.screenshot({ path: path.join(runtime.evidenceDir, "02-auto-number-readonly.png"), fullPage: true });
  const session = await page.evaluate(() => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
  await openWorkspaceCenterFromSwitcher(page);
  const closed = await replicaUiMethod(page, recorder, "workspace.close", () =>
    page.getByTestId("workspace-center").getByRole("button", { name: /关闭当前工作区|Close current workspace/ }).click());
  recorder.check("autoNumber journey closes the actual workspace", closed.result?.state === "closed", { closed });
  await beginWritableWorkspaceBootstrapCapture(page, session.sessionEpoch, "workspace.open");
  await page.getByTestId("workspace-center").getByRole("button", { name: /E2E Product Workspace/ }).click();
  await waitForCapturedBridgeMessage(page, 60_000);
  await page.getByTestId("nav-tables").click();
  await selectTable(page, "E2E AutoNumber Only");
  await applyProductMutation(page, tableId, [{ kind: "insert", recordId: null, values: {} }], "number-after-reopen");
  const reopened = await rawBridgeRequest(page, "query.page", { tableId, query });
  recorder.check("workspace reopen resumes above the deleted highest number",
    reopened.payload.rows.some(row => row[field.identity.physicalName] === "HT-000005")
      && !reopened.payload.rows.some(row => row[field.identity.physicalName] === "HT-000004"), { reopened });
}

export async function prepareAutoNumberSnapshot(page, recorder, tableId, field, ports) {
  const result = await ports.applyProductMutation(page, tableId, [
    { kind: "insert", recordId: null, values: {} },
    { kind: "insert", recordId: null, values: {} },
  ], "number-snapshot-high-water");
  await ports.applyProductMutation(page, tableId,
    result.payload.affectedRows.map(row => ({ kind: "delete", recordId: row.recordId })), "number-snapshot-delete-highest");
  const rows = await ports.rawBridgeRequest(page, "query.page", { tableId, query });
  recorder.check("snapshot source retains a high water mark above all remaining numbers",
    rows.payload.rows.length === 1 && rows.payload.rows[0][field.physicalName] === "HT-000001", { rows });
}

export async function verifyAutoNumberSnapshot(page, recorder, tableId, field, ports) {
  const inserted = await ports.applyProductMutation(page, tableId,
    [{ kind: "insert", recordId: null, values: {} }], "number-after-snapshot-restore");
  const rows = await ports.rawBridgeRequest(page, "query.page", { tableId, query });
  recorder.check("real snapshot restore preserves numbering state after the highest row was deleted",
    rows.payload.rows.some(row => row[field.physicalName] === "HT-000004")
      && rows.payload.rows.some(row => row[field.physicalName] === "HT-000001"), { rows });
  await ports.applyProductMutation(page, tableId,
    inserted.payload.affectedRows.map(row => ({ kind: "delete", recordId: row.recordId })), "number-snapshot-cleanup");
}
