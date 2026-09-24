import fs from "node:fs/promises";
import path from "node:path";
import { execFile } from "node:child_process";
import { promisify, isDeepStrictEqual } from "node:util";
import { fileURLToPath } from "node:url";

const executeFile = promisify(execFile);
const workbookHelper = fileURLToPath(new URL("./data_io_workbook.py", import.meta.url));

// Scenario 35 drives the remaining interoperability representatives through the
// visible product UI: a UTF-8 BOM CSV with Unicode/date text, an XLSX workbook
// with native date cells, a real cancel-then-reselect import, exports to
// Unicode file names, and one extended-length path granted by the real Host
// picker. Frozen values come from the declarative corpus; no value is inferred
// from product output.
export async function runDataIoInteroperability(page, recorder, runtime, helpers) {
  const {
    waitForShell, createSimpleTable, createV2Field, rawBridgeRequest,
    canonicalJsonText, chooseToolbarMore,
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
  const importThroughUi = async (sourcePath) => {
    await selectImportSource(sourcePath);
    // Same realtime-lag guard as the #349 UI phase: refresh the grid (a real
    // user action) before each toolbar import so the command is actionable.
    await chooseToolbarMore(page, "refresh");
    await page.waitForTimeout(500);
    await chooseToolbarMore(page, "import");
    await page.getByTestId("import-preview-panel").waitFor({ state: "visible", timeout: 60_000 });
    const acknowledgement = page.getByTestId("import-ack");
    if (await acknowledgement.count()) await acknowledgement.click();
    await page.getByTestId("import-confirm").click();
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

  await importThroughUi(unicodeSource);
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
  await importThroughUi(xlsxSource);
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

  // ---- Extended-length path granted through the real Host picker ----
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
  recorder.check("an extended-length Unicode path imports through the real Host grant",
    longSource.length > 260
      && longImported.rows.length === 1
      && codePoints(longImported.rows[0][longTable.field.physicalName]).join(",")
        === codePoints(longValue).join(","),
    { path: longSource, length: longSource.length, longImported });
}

function expectedRepresentative(corpus, key) {
  return corpus.unicodeRepresentatives.find((item) => item.key === key);
}

export function sameRows(actual, expected) {
  const ordered = (rows) => rows.map((row) => JSON.stringify(row)).sort();
  return isDeepStrictEqual(ordered(actual), ordered(expected));
}
