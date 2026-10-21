import fs from "node:fs/promises";
import path from "node:path";
import { execFile } from "node:child_process";
import { promisify, isDeepStrictEqual } from "node:util";
import { fileURLToPath } from "node:url";

import { runSourceImportJourney, withRendererOffline } from "./source_import_journey.mjs";
import { runFeishuBaseFileJourney } from "./feishu_base_file_journey.mjs";
import { picker } from "./plugin_capability_services.mjs";

const executeFile = promisify(execFile);
const workbookHelper = fileURLToPath(new URL("./data_io_workbook.py", import.meta.url));

// Scenario 35 drives the remaining interoperability representatives through the
// visible product UI: a UTF-8 BOM CSV with Unicode/date text, an XLSX workbook
// with native date cells, a real cancel-then-reselect import, exports to
// Unicode file names, and one extended-length path granted by the Host
// test-mode selection control (the control seam, not the native dialog; the
// native-dialog path is exercised by the main CSV/XLSX imports below). Frozen
// values come from the declarative corpus; no value is inferred from product
// output.
//
// The main CSV and XLSX import passes select their fixed synthetic Unicode
// sources through the REAL native import dialog: import-source.txt carries
// the exact "native-dialog" marker, so WindowsNativeProductFileHost falls
// through to the real OpenFileDialog titled "Import table data", and the
// CI-only SendInput helper types the fixed file name and accepts. Receipts in
// 35-native-import-picker.json prove the native selection per format; no
// bridge call or control path may choose these sources.
export async function runDataIoInteroperability(page, recorder, runtime, helpers) {
  const {
    waitForShell, createSimpleTable, createV2Field, rawBridgeRequest,
    canonicalJsonText, chooseToolbarMore,
    openWorkspaceCenterFromSwitcher, replicaUiMethod,
    beginWritableWorkspaceBootstrapCapture, waitForCapturedBridgeMessage,
  } = helpers;
  const corpus = JSON.parse(await fs.readFile(
    new URL("../fixtures/data-io/a5-interop-matrix-corpus.json", import.meta.url), "utf8",
  ));
  const workbook = async (action, target, payload) => {
    if (!runtime.pythonExecutable) throw new Error("The runner's locked Python is required.");
    const { stdout } = await executeFile(runtime.pythonExecutable,
      [workbookHelper, action, target, JSON.stringify(payload)],
      { encoding: "utf8", timeout: 30_000, maxBuffer: 1024 * 1024,
        env: { ...process.env, PYTHONUTF8: "1" } });
    return JSON.parse(stdout);
  };
  const verifyExport = async (format, target, columns, rows, screenshot) => {
    await exportThroughUi(format, target, screenshot);
    return workbook("verify", target, { columns, rows });
  };
  const dateCase = (key) => corpus.dateCases.find((item) => item.key === key);
  const request = async (type, payload) => {
    const response = await rawBridgeRequest(page, type, payload);
    if (response.type === "operation.failed" || response.payload?.error) {
      throw new Error(`${type} failed: ${JSON.stringify(response)}`);
    }
    return response.payload;
  };
  const query = { filters: [], sorts: [], offset: 0, limit: 100 };
  const authority = async (tableId) => {
    const result = await request("query.page", { tableId, query });
    return {
      rows: [...result.rows].sort((left, right) => left.id.localeCompare(right.id)),
      table: result.snapshot.table,
      schemaRevision: result.snapshot.schemaRevision,
      dataRevision: result.snapshot.dataRevision,
    };
  };
  const controls = async (name, contents) => {
    const target = path.join(runtime.controlsDir, name);
    await fs.writeFile(target, contents, "utf8");
    return target;
  };
  const selectImportSource = (sourcePath) => controls(
    "import-source.txt", `${sourcePath}\r\n`,
  );
  // Test-mode marker from WindowsNativeProductFileHost.IsNativeDialogControl:
  // on the controlled CI runner the Host skips the control path and shows the
  // real OpenFileDialog instead. Outside CI the Host fails closed, so this
  // marker is only ever written together with the picker helper below.
  const selectImportSourceThroughNativeDialog = () => controls(
    "import-source.txt", "native-dialog\r\n",
  );
  const nativeImportPickerReceipts = {};
  const savePickerReceipts = () => fs.writeFile(
    path.join(runtime.evidenceDir, "35-native-import-picker.json"),
    JSON.stringify(nativeImportPickerReceipts, null, 2), "utf8");
  const waitForExportFile = async (targetPath) => {
    const deadline = Date.now() + 60_000;
    while (Date.now() < deadline) {
      try {
        return await fs.readFile(targetPath);
      } catch (error) {
        if (error?.code !== "ENOENT") throw error;
        await page.waitForTimeout(100);
      }
    }
    throw new Error(`UI export did not write ${targetPath} in time`);
  };
  const exportThroughUi = async (format, targetPath, screenshot) => {
    await fs.rm(targetPath, { force: true });
    await controls("export-target.txt", `${targetPath}\r\n`);
    await chooseToolbarMore(page, `export-${format}`);
    await page.getByTestId("export-lookup-panel").waitFor({ state: "visible", timeout: 60_000 });
    await page.screenshot({
      path: path.join(runtime.evidenceDir, screenshot), fullPage: true,
    });
    await page.getByTestId("export-lookup-confirm").click();
    return waitForExportFile(targetPath);
  };
  const confirmImportPreviewThroughUi = async () => {
    await page.getByTestId("import-preview-panel").waitFor({ state: "visible", timeout: 60_000 });
    const acknowledgement = page.getByTestId("import-ack");
    if (await acknowledgement.count()) await acknowledgement.click();
    await page.getByTestId("import-confirm").click();
  };
  const importThroughUi = async (sourcePath, managedTarget = null) => {
    await selectImportSource(sourcePath);
    // Same realtime-lag guard as the #349 UI phase: refresh the grid (a real
    // user action) before each toolbar import so the command is actionable.
    await chooseToolbarMore(page, "refresh");
    await page.waitForTimeout(500);
    if (managedTarget) {
      await page.getByTestId("import-management-open").click();
      await page.getByTestId("import-management").waitFor({ state: "visible" });
      recorder.check("local base import is available and online sources are retired",
        await page.getByTestId("import-source-feishuBase").isEnabled()
          && await page.getByTestId("import-source-feishu").count() === 0
          && await page.getByTestId("import-source-wps").count() === 0);
      await page.getByTestId("import-source-xlsx").click();
      await page.getByTestId("import-target-option")
        .filter({ hasText: managedTarget }).click();
    } else {
      await chooseToolbarMore(page, "import");
    }
    await confirmImportPreviewThroughUi();
  };
  // The main CSV (toolbar) and XLSX (managed import-management) passes select
  // their fixed synthetic source through the real native dialog. The picker
  // helper is armed before the triggering click, and the receipt is verified
  // against the exact fixed file name — a control-path selection or a mocked
  // dialog cannot produce a completed SendInput receipt.
  const importThroughNativePicker = async (kind, expectedTargetName, managedTarget = null) => {
    await chooseToolbarMore(page, "refresh");
    await page.waitForTimeout(500);
    await selectImportSourceThroughNativeDialog();
    const pendingPicker = picker(runtime, kind);
    if (managedTarget) {
      await page.getByTestId("import-management-open").click();
      await page.getByTestId("import-management").waitFor({ state: "visible" });
      recorder.check("local base import is available and online sources are retired",
        await page.getByTestId("import-source-feishuBase").isEnabled()
          && await page.getByTestId("import-source-feishu").count() === 0
          && await page.getByTestId("import-source-wps").count() === 0);
      await page.getByTestId("import-source-xlsx").click();
    } else {
      await chooseToolbarMore(page, "import");
    }
    const receipt = await pendingPicker;
    nativeImportPickerReceipts[kind] = receipt;
    await savePickerReceipts();
    const receiptProblems = verifyNativeImportPickerReceipt(receipt, expectedTargetName);
    recorder.check(
      `the ${managedTarget ? "managed XLSX" : "main CSV"} import selects its fixed Unicode source through the real native picker`,
      receiptProblems.length === 0, { receipt, receiptProblems });
    if (managedTarget) {
      // The managed flow only renders the target chooser after the grant
      // returns, so wait for the real option instead of assuming it exists.
      const targetOption = page.getByTestId("import-target-option")
        .filter({ hasText: managedTarget });
      await targetOption.waitFor({ state: "visible", timeout: 60_000 });
      await targetOption.click();
    }
    await confirmImportPreviewThroughUi();
  };
  const waitForAuthorityRows = async (tableId, expectedRows) => {
    const deadline = Date.now() + 60_000;
    let snapshot = await authority(tableId);
    while (snapshot.rows.length !== expectedRows && Date.now() < deadline) {
      await page.waitForTimeout(200);
      snapshot = await authority(tableId);
    }
    return snapshot;
  };
  const codePoints = (value) => [...value].map((character) => character.codePointAt(0));

  await waitForShell(page, recorder, { requireDatabaseOpened: true });
  await page.getByTestId("nav-tables").click();

  // ---- Table 1: UTF-8 BOM CSV with Unicode and frozen date text ----
  const table = await createSimpleTable(page, "A5 Interop CSV", "Label");
  // Parse progress can reach the final row before the UI cancel arrives.
  // Deterministic early-stop coverage lives in test_file_execution.py.
  const slowSource = path.join(runtime.controlsDir, "slow-preview.xlsx");
  const slowFixture = await workbook("slow-preview", slowSource, { field: table.field.physicalName });
  const beforeSlowPreview = await authority(table.tableId);
  await page.evaluate(() => {
    const state = { taskId: null, acceptedAt: null, terminalAt: null, terminal: null };
    const listener = (event) => {
      let message = event.data;
      if (typeof message === "string") {
        try { message = JSON.parse(message); } catch { return; }
      }
      const payload = message?.payload;
      if (message?.type === "task.create" && payload?.kind === "data.previewImport") {
        state.taskId = payload.taskId;
      }
      if (!state.taskId || payload?.taskId !== state.taskId) return;
      if (message.type === "task.cancel") state.acceptedAt = Date.now();
      if (["task.create", "task.status"].includes(message.type)
          && ["succeeded", "failed", "cancelled", "aborted"].includes(payload.state)) {
        state.terminalAt = Date.now();
        state.terminal = payload;
      }
    };
    window.chrome.webview.addEventListener("message", listener);
    window.__vibetableSlowPreview = {
      state, release: () => window.chrome.webview.removeEventListener("message", listener),
    };
  });
  try {
    await selectImportSource(slowSource);
    await chooseToolbarMore(page, "refresh");
    await chooseToolbarMore(page, "import");
    await page.getByTestId("data-io-progress").waitFor({ state: "visible", timeout: 10_000 });
    const phase = await page.getByTestId("data-io-progress").innerText();
    await page.screenshot({
      path: path.join(runtime.evidenceDir, "35-file-preview-progress.png"), fullPage: true,
    });
    await page.getByTestId("toolbar-more").click();
    const cancel = page.locator(".n-dropdown-option-body")
      .filter({ hasText: /取消数据任务|Cancel data task/i }).last();
    await cancel.waitFor();
    const requestedAt = Date.now();
    await cancel.click();
    await page.waitForFunction(() =>
      window.__vibetableSlowPreview.state.terminal !== null, null, { timeout: 10_000 });
    const observed = await page.evaluate(() => window.__vibetableSlowPreview.state);
    const evidence = {
      ...observed, phase, fixture: slowFixture,
      acceptanceMs: observed.acceptedAt - requestedAt,
      drainMs: observed.terminalAt - observed.acceptedAt,
    };
    await fs.writeFile(path.join(runtime.evidenceDir, "35-file-preview-cancel.json"),
      JSON.stringify(evidence, null, 2), "utf8");
    const cancelProblems = slowPreviewCancelEvidenceProblems(evidence);
    recorder.check("slow XLSX preview reports a real measured phase and cancels cooperatively within the existing drain budget",
      cancelProblems.length === 0,
      { ...evidence, cancelProblems });
    recorder.check("cancelled slow preview does not refill a plan or change authoritative rows/revisions",
      !await page.getByTestId("import-preview-panel").isVisible()
        && canonicalJsonText(await authority(table.tableId)) === canonicalJsonText(beforeSlowPreview));
  } finally {
    await page.evaluate(() => {
      window.__vibetableSlowPreview.release();
      delete window.__vibetableSlowPreview;
    });
  }
  const value = await createV2Field(page, table.tableId, "Value", "text");
  const eventDate = await createV2Field(page, table.tableId, "Event Date", "date");
  const eventAt = await createV2Field(page, table.tableId, "Event At", "dateTime");
  const nfdAccent = String.fromCodePoint(67, 97, 102, 101, 769);
  const emoji = String.fromCodePoint(0x1f469, 0x1f3fd, 0x200d, 0x1f4bb);
  const isoDate = dateCase("csv_iso_date_text");
  const civilAt = dateCase("csv_civil_datetime_text");
  const offsetAt = dateCase("csv_iso_offset_text");
  const csvRows = corpus.unicodeRepresentatives.map((item, index) => ({
    label: item.key,
    value: item.value,
    eventDate: isoDate.source.cell,
    eventAt: index % 2 === 0 ? civilAt.source.cell : offsetAt.source.cell,
  }));
  const csvCell = (cellValue) => `"${String(cellValue).replaceAll('"', '""')}"`;
  const unicodeSource = path.join(runtime.controlsDir, `导入-源-${nfdAccent}-${emoji}.csv`);
  const csvLines = [
    [table.field.physicalName, value.physicalName, eventDate.physicalName, eventAt.physicalName],
    ...csvRows.map((row) => [row.label, row.value, row.eventDate, row.eventAt]),
  ].map((cells) => cells.map(csvCell).join(","));
  await fs.writeFile(unicodeSource, `\uFEFF${csvLines.join("\r\n")}\r\n`, "utf8");

  const baseline = await authority(table.tableId);
  // Field creation goes through the bridge; the renderer's post-commit
  // refresh relies on realtime delivery and may lag, leaving the toolbar
  // import command disabled. Refresh explicitly (a real user action) first.
  await chooseToolbarMore(page, "refresh");
  await page.waitForTimeout(500);
  await selectImportSource(unicodeSource);
  await chooseToolbarMore(page, "import");
  await page.getByTestId("import-preview-panel").waitFor({ state: "visible", timeout: 60_000 });
  await page.screenshot({
    path: path.join(runtime.evidenceDir, "35-ui-cancel-preview.png"), fullPage: true,
  });
  await page.getByTestId("import-cancel").click();
  await page.getByTestId("import-preview-panel").waitFor({ state: "hidden", timeout: 60_000 });
  recorder.check("a real UI cancellation leaves the authority empty with revisions unchanged",
    baseline.rows.length === 0
      && canonicalJsonText(await authority(table.tableId)) === canonicalJsonText(baseline),
    { baseline });

  await importThroughNativePicker("import-unicode-csv", `导入-源-${nfdAccent}-${emoji}.csv`);
  const imported = await waitForAuthorityRows(table.tableId, csvRows.length);
  recorder.check("UI import commits Unicode code points and frozen date wires verbatim",
    imported.rows.length === csvRows.length
      && csvRows.every((expected) => imported.rows.some((row) =>
        row[table.field.physicalName] === expected.label
        && codePoints(row[value.physicalName]).join(",")
          === expectedRepresentative(corpus, expected.label).codePoints.join(",")
        && row[eventDate.physicalName] === isoDate.queryWire
        && row[eventAt.physicalName]
          === (expected.eventAt === civilAt.source.cell ? civilAt.queryWire : offsetAt.queryWire))),
    { imported });

  const afterImport = await authority(table.tableId);
  const unicodeColumns = [table.field.physicalName, value.physicalName,
    eventDate.physicalName, eventAt.physicalName];
  const unicodeExpectedRows = csvRows.map((row) => [row.label, row.value,
    isoDate.exportText, row.eventAt === civilAt.source.cell ? civilAt.exportText : offsetAt.exportText]);
  for (const format of ["csv", "xlsx"]) {
    const target = path.join(runtime.controlsDir, `导出-结果-${nfdAccent}-${emoji}.${format}`);
    const verified = await verifyExport(format, target, unicodeColumns, unicodeExpectedRows,
      `35-ui-export-${format}.png`);
    recorder.check(`${format.toUpperCase()} export preserves every Unicode/date row and string value`,
      verified.rows === csvRows.length, verified);
  }
  recorder.check("Unicode exports leave the authority rows and revisions unchanged",
    canonicalJsonText(await authority(table.tableId)) === canonicalJsonText(afterImport));

  // ---- Table 2: XLSX source with native date cells and formula-like text ----
  const xlsxTable = await createSimpleTable(page, "A5 Interop Xlsx", "Note");
  const day = await createV2Field(page, xlsxTable.tableId, "Day", "date");
  const stamp = await createV2Field(page, xlsxTable.tableId, "Stamp", "dateTime");
  const nativeDate = dateCase("xlsx_native_date");
  const nativeDatetime = dateCase("xlsx_native_datetime_milliseconds");
  const xlsxSource = path.join(runtime.controlsDir, `数据-源-${emoji}.xlsx`);
  const producer = await workbook("native", xlsxSource, {
    columns: [day.physicalName, stamp.physicalName, xlsxTable.field.physicalName],
    date: nativeDate.source.cell, stamp: nativeDatetime.source.cell,
    notes: [corpus.formulaLikeText.value, "普通文本"],
  });
  await fs.writeFile(path.join(runtime.evidenceDir, "35-producer-metadata.json"),
    JSON.stringify(producer, null, 2), "utf8");
  const xlsxExpectedRows = [corpus.formulaLikeText.value, "普通文本"].map((note) =>
    [nativeDate.queryWire, nativeDatetime.queryWire, note]);
  await importThroughNativePicker("import-unicode-xlsx", `数据-源-${emoji}.xlsx`, "A5 Interop Xlsx");
  const xlsxImported = await waitForAuthorityRows(xlsxTable.tableId, xlsxExpectedRows.length);
  recorder.check("UI import turns native XLSX dates into the frozen UTC wire text",
    sameRows(xlsxImported.rows.map((row) =>
      [row[day.physicalName], row[stamp.physicalName], row[xlsxTable.field.physicalName]]),
    xlsxExpectedRows),
    { xlsxImported });

  const xlsxAfterImport = await authority(xlsxTable.tableId);
  for (const format of ["csv", "xlsx"]) {
    const target = path.join(runtime.controlsDir, `导出-结果-xlsx-${nfdAccent}-${emoji}.${format}`);
    const verified = await verifyExport(format, target,
      [day.physicalName, stamp.physicalName, xlsxTable.field.physicalName], xlsxExpectedRows,
      `35-ui-export-xlsx-source-${format}.png`);
    recorder.check(`${format.toUpperCase()} native-date export preserves the exact row multiset`,
      verified.rows === xlsxExpectedRows.length, verified);
  }
  recorder.check("native-date exports leave the authority rows and revisions unchanged",
    canonicalJsonText(await authority(xlsxTable.tableId)) === canonicalJsonText(xlsxAfterImport));

  // ---- Extended-length path granted through the Host test-mode selection
  // control (import-source.txt), not the native dialog: the long directory
  // chain exercises the Host path grant/read side only. The seam stays
  // accurately labeled and never masquerades as a native picker pass.
  const longTable = await createSimpleTable(page, "A5 Long Path", "Value");
  let directory = runtime.controlsDir;
  while (directory.length < 280) {
    directory = path.join(directory, `中文-${nfdAccent}-${"a".repeat(35)}`);
  }
  await fs.mkdir(directory, { recursive: true });
  const longValue = "原样 Cafe\u0301 \u{1F469}\u{1F3FD}\u{200D}\u{1F4BB}";
  const longSource = path.join(directory, `长-源-${emoji}.csv`);
  await fs.writeFile(
    longSource,
    `\uFEFF${[longTable.field.physicalName].map(csvCell).join(",")}\r\n${csvCell(longValue)}\r\n`,
    "utf8",
  );
  await importThroughUi(longSource);
  const longImported = await waitForAuthorityRows(longTable.tableId, 1);
  recorder.check("an extended-length Unicode path imports through the Host test-mode selection control",
    longSource.length > 260
      && longImported.rows.length === 1
      && codePoints(longImported.rows[0][longTable.field.physicalName]).join(",")
        === codePoints(longValue).join(","),
    { path: longSource, length: longSource.length, longImported });

  const expectedHistory = [
    { collection: table.tableId, count: csvRows.length, sourceType: "csv" },
    { collection: xlsxTable.tableId, count: xlsxExpectedRows.length, sourceType: "xlsx" },
    { collection: longTable.tableId, count: 1, sourceType: "csv" },
  ];
  const history = await request("data.importHistory", {});
  const committed = expectedHistory.map((expected) => history.items.find((entry) =>
    entry.collection === expected.collection && entry.commitState === "committed"));
  recorder.check("both entry points retain Go-confirmed import counts and safe source metadata",
    committed.every((entry, index) => entry?.state === "succeeded"
      && entry.sourceType === expectedHistory[index].sourceType
      && entry.createdCount === expectedHistory[index].count && entry.updatedCount === 0
      && entry.startedAt && entry.finishedAt && entry.sourceName
      && !/[\\/]/.test(entry.sourceName)
      && !["token", "grantId", "idempotencyKey", "accessToken"].some(key => key in entry)),
    { history });
  await page.getByTestId("import-management-open").click();
  await page.getByTestId("import-management").waitFor({ state: "visible" });
  await page.getByTestId("import-history-refresh").click();
  for (const entry of committed) {
    await page.locator(`[data-testid="import-history-row"][data-task-id="${entry.taskId}"]`)
      .waitFor({ state: "visible" });
  }
  await page.screenshot({ path: path.join(runtime.evidenceDir, "35-import-management-history.png"), fullPage: true });
  await page.getByTestId(`import-history-target-${committed[0].taskId}`).click();
  await page.getByTestId("toolbar-table-title").waitFor({ state: "visible" });
  recorder.check("the history target action returns to the original import table",
    (await page.getByTestId("toolbar-table-title").textContent()).includes("A5 Interop CSV"));

  await withRendererOffline(page, async () => {
    const originalSession = await page.evaluate(() => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
    await openWorkspaceCenterFromSwitcher(page);
    const closed = await replicaUiMethod(page, recorder, "workspace.close", () =>
      page.getByTestId("workspace-center").getByRole("button", {
        name: /关闭当前工作区|Close current workspace/,
      }).click());
    recorder.check("import history acceptance closes the actual workspace", closed.result?.state === "closed", { closed });
    await beginWritableWorkspaceBootstrapCapture(page, originalSession.sessionEpoch, "workspace.open");
    await page.getByTestId("workspace-center").getByRole("button", { name: /E2E Product Workspace/ }).click();
    const reopened = await waitForCapturedBridgeMessage(page, 60_000);
    const reopenedHistory = await request("data.importHistory", {});
    recorder.check("reopening retains terminal receipts without resurrecting old tasks or tokens",
      reopened.payload.session.workspaceId === originalSession.workspaceId
        && reopened.payload.session.sessionEpoch > originalSession.sessionEpoch
        && committed.every((before) => {
          const after = reopenedHistory.items.find(entry => entry.taskId === before.taskId);
          return after?.state === "succeeded" && after.commitState === "committed"
            && after.createdCount === before.createdCount && after.updatedCount === before.updatedCount;
        })
        && reopenedHistory.items.every(entry => !["queued", "running"].includes(entry.state)),
      { reopenedHistory });
    for (const [format, tableId, expected] of [
      ["CSV", table.tableId, afterImport],
      ["XLSX", xlsxTable.tableId, xlsxAfterImport],
    ]) {
      const actual = await authority(tableId);
      recorder.check(`the ${format} rows, IDs and revisions survive reopening offline`,
        canonicalJsonText(actual) === canonicalJsonText(expected), { expected, actual });
    }
  });
  await page.getByTestId("import-management-open").click();
  await page.getByTestId("import-history-row").first().waitFor({ state: "visible" });
  await page.screenshot({ path: path.join(runtime.evidenceDir, "35-import-management-reopened.png"), fullPage: true });

  // ---- #435 synthetic source migration through the Host TestMode seam ----
  // Scenario 35 keeps its #434 evidence untouched; the source migration
  // appends its own controls, receipts, targets and reopen persistence on
  // the same real WPF management page.
  await runSourceImportJourney(page, recorder, runtime, {
    waitForShell, rawBridgeRequest, openWorkspaceCenterFromSwitcher, replicaUiMethod,
    beginWritableWorkspaceBootstrapCapture, waitForCapturedBridgeMessage,
  });

  // ---- #436/#437 formal .base local-file import through the real wizard ----
  // The TestMode seam above stays untouched: it remains the synthetic
  // snapshot contract and is never presented as a file path. The formal
  // pass appends after it on the same workspace, real picker included.
  await runFeishuBaseFileJourney(page, recorder, runtime, {
    rawBridgeRequest, openWorkspaceCenterFromSwitcher, replicaUiMethod,
    beginWritableWorkspaceBootstrapCapture, waitForCapturedBridgeMessage,
  });
}

function expectedRepresentative(corpus, key) {
  return corpus.unicodeRepresentatives.find((item) => item.key === key);
}

// Contract for the per-format native import selection receipt produced by the
// CI-only SendInput helper: only a completed real dialog pass typed the exact
// fixed file name into the owned "Import table data" dialog. A control-path
// selection, a cancelled dialog or a mocked result cannot satisfy it.
export function verifyNativeImportPickerReceipt(receipt, expectedTargetName) {
  const problems = [];
  if (receipt?.status !== "completed") {
    problems.push(`status is ${JSON.stringify(receipt?.status)}`);
  }
  if (receipt?.action !== "select") {
    problems.push(`action is ${JSON.stringify(receipt?.action)}`);
  }
  if (receipt?.target !== expectedTargetName) {
    problems.push(`target is ${JSON.stringify(receipt?.target)}, expected ${JSON.stringify(expectedTargetName)}`);
  }
  if (receipt?.input !== "SendInput") {
    problems.push(`input is ${JSON.stringify(receipt?.input)}`);
  }
  if (receipt?.observation?.title !== "Import table data") {
    problems.push(`dialog title is ${JSON.stringify(receipt?.observation?.title)}`);
  }
  return problems;
}

export function sameRows(actual, expected) {
  const ordered = (rows) => rows.map((row) => JSON.stringify(row)).sort();
  return isDeepStrictEqual(ordered(actual), ordered(expected));
}

// A cancelled preview may have read every row but must not return a plan or commit.
export function slowPreviewCancelEvidenceProblems(evidence) {
  const problems = [];
  const { phase, acceptedAt, terminal, fixture, acceptanceMs, drainMs } = evidence;
  if (typeof phase !== "string" || phase.length === 0) {
    problems.push(`phase is ${JSON.stringify(phase)}`);
  }
  if (typeof acceptedAt !== "number") {
    problems.push(`acceptedAt is ${JSON.stringify(acceptedAt)}`);
  }
  if (!(acceptanceMs >= 0 && acceptanceMs < 10_000)) {
    problems.push(`acceptanceMs is ${JSON.stringify(acceptanceMs)}`);
  }
  if (!(drainMs >= 0 && drainMs < 10_000)) {
    problems.push(`drainMs is ${JSON.stringify(drainMs)}`);
  }
  if (terminal?.state !== "cancelled") {
    problems.push(`terminal state is ${JSON.stringify(terminal?.state)}`);
  }
  const done = terminal?.progress?.done;
  if (!Number.isInteger(done) || done < 0 || done > fixture.rows) {
    problems.push(
      `terminal progress done is ${JSON.stringify(done)}, fixture rows ${fixture.rows}`,
    );
  }
  if (terminal?.result !== null) {
    problems.push(`terminal result is ${JSON.stringify(terminal?.result)}`);
  }
  if (terminal?.error !== null) {
    problems.push(`terminal error is ${JSON.stringify(terminal?.error)}`);
  }
  if (terminal?.commitReceipt !== null) {
    problems.push(`terminal commitReceipt is ${JSON.stringify(terminal?.commitReceipt)}`);
  }
  return problems;
}
