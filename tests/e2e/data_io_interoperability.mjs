import fs from "node:fs/promises";
import path from "node:path";

// The shared zip walker lives with the #349 scenario that introduced it.
import { zipEntries } from "./relation_lookup_data_io.mjs";

// Scenario 35 drives the remaining interoperability representatives through the
// visible product UI: a UTF-8 BOM CSV with Unicode/date text, an XLSX workbook
// with native date cells, a real cancel-then-reselect import, exports to
// Unicode file names, and one extended-length path granted by the real Host
// picker. Frozen values come from the declarative corpus; no value is inferred
// from product output.
export async function runDataIoInteroperability(page, recorder, runtime, helpers) {
  const {
    waitForShell, createSimpleTable, createV2Field, rawBridgeRequest,
    parseCsv, canonicalJsonText, chooseToolbarMore,
  } = helpers;
  const corpus = JSON.parse(await fs.readFile(
    new URL("../fixtures/data-io/a5-interop-matrix-corpus.json", import.meta.url), "utf8",
  ));
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

  // The exporter writes CSV through utf-8-sig (a leading BOM is part of the
  // contract); strip it before parsing so header lookups stay exact.
  const parseExportedCsv = (bytes) => parseCsv(bytes.toString("utf8").replace(/^\uFEFF/u, ""));
  const afterImport = await authority(table.tableId);
  const unicodeCsvTarget = path.join(runtime.controlsDir, `导出-结果-${nfdAccent}-${emoji}.csv`);
  const unicodeCsv = parseExportedCsv(await exportThroughUi(
    "csv", unicodeCsvTarget, "35-ui-export-csv.png",
  ));
  const [unicodeHeaders, ...unicodeDataRows] = unicodeCsv;
  const csvByLabel = new Map(unicodeDataRows
    .filter((row) => row.some((cell) => cell !== ""))
    .map((row) => [row[unicodeHeaders.indexOf(table.field.physicalName)], row]));
  recorder.check("CSV export keeps Unicode code points and date wire text byte-exact",
    csvByLabel.size === csvRows.length
      && csvRows.every((expected) => {
        const row = csvByLabel.get(expected.label);
        return row !== undefined
          && codePoints(row[unicodeHeaders.indexOf(value.physicalName)]).join(",")
            === expectedRepresentative(corpus, expected.label).codePoints.join(",")
          && row[unicodeHeaders.indexOf(eventDate.physicalName)] === isoDate.exportText
          && row[unicodeHeaders.indexOf(eventAt.physicalName)]
            === (expected.eventAt === civilAt.source.cell ? civilAt.exportText : offsetAt.exportText);
      }),
    { unicodeHeaders, rows: [...csvByLabel] });

  const unicodeXlsxTarget = path.join(runtime.controlsDir, `导出-结果-${nfdAccent}-${emoji}.xlsx`);
  const unicodeXlsx = zipEntries(await exportThroughUi(
    "xlsx", unicodeXlsxTarget, "35-ui-export-xlsx.png",
  )).filter((entry) => entry.name.startsWith("xl/worksheets/"))
    .map((entry) => entry.data.toString("utf8"));
  const expectedTexts = [
    ...csvRows.flatMap((row) => [
      row.value,
      row.eventAt === civilAt.source.cell ? civilAt.exportText : offsetAt.exportText,
    ]),
    isoDate.exportText,
  ];
  recorder.check("XLSX export stores Unicode and date wire text as strings, never formulas",
    unicodeXlsx.length > 0
      && unicodeXlsx.every((sheet) => !sheet.includes("<f"))
      && expectedTexts.every((text) => unicodeXlsx.some((sheet) => sheet.includes(text))),
    { worksheetCount: unicodeXlsx.length });
  recorder.check("Unicode exports leave the authority rows and revisions unchanged",
    canonicalJsonText(await authority(table.tableId)) === canonicalJsonText(afterImport));

  // ---- Table 2: XLSX source with native date cells and formula-like text ----
  const xlsxTable = await createSimpleTable(page, "A5 Interop Xlsx", "Note");
  const day = await createV2Field(page, xlsxTable.tableId, "Day", "date");
  const stamp = await createV2Field(page, xlsxTable.tableId, "Stamp", "dateTime");
  const nativeDate = dateCase("xlsx_native_date");
  const nativeDatetime = dateCase("xlsx_native_datetime_milliseconds");
  const xlsxRows = [
    [excelSerial(2026, 7, 29), excelSerial(2026, 7, 29, 14, 5, 6, 123), corpus.formulaLikeText.value],
    [excelSerial(2026, 7, 29), excelSerial(2026, 7, 29, 14, 5, 6, 123), "普通文本"],
  ];
  const xlsxSource = path.join(runtime.controlsDir, `数据-源-${emoji}.xlsx`);
  await fs.writeFile(xlsxSource, buildNativeDateXlsx({
    header: [day.physicalName, stamp.physicalName, xlsxTable.field.physicalName],
    rows: xlsxRows,
  }));
  await importThroughUi(xlsxSource);
  const xlsxImported = await waitForAuthorityRows(xlsxTable.tableId, xlsxRows.length);
  recorder.check("UI import turns native XLSX dates into the frozen UTC wire text",
    xlsxImported.rows.length === xlsxRows.length
      && xlsxImported.rows.every((row) =>
        row[day.physicalName] === nativeDate.queryWire
        && row[stamp.physicalName] === nativeDatetime.queryWire
        && [corpus.formulaLikeText.value, "普通文本"].includes(row[xlsxTable.field.physicalName])),
    { xlsxImported });

  const xlsxAfterImport = await authority(xlsxTable.tableId);
  const xlsxNotes = new Set([corpus.formulaLikeText.value, "普通文本"]);
  const derivedCsvTarget = path.join(runtime.controlsDir, `导出-结果-xlsx-${nfdAccent}.csv`);
  const derivedCsv = parseExportedCsv(await exportThroughUi(
    "csv", derivedCsvTarget, "35-ui-export-xlsx-source-csv.png",
  ));
  const [derivedHeaders, ...derivedDataRows] = derivedCsv;
  const derivedCells = derivedDataRows.filter((row) => row.some((cell) => cell !== ""));
  recorder.check("CSV export of the native-date table keeps the frozen wire text",
    derivedCells.length === xlsxRows.length
      && derivedCells.every((row) =>
        row[derivedHeaders.indexOf(day.physicalName)] === nativeDate.exportText
        && row[derivedHeaders.indexOf(stamp.physicalName)] === nativeDatetime.exportText
        && xlsxNotes.has(row[derivedHeaders.indexOf(xlsxTable.field.physicalName)])),
    { derivedHeaders, derivedCells });

  const derivedXlsxTarget = path.join(runtime.controlsDir, `导出-结果-xlsx-${emoji}.xlsx`);
  const derivedXlsx = zipEntries(await exportThroughUi(
    "xlsx", derivedXlsxTarget, "35-ui-export-xlsx-source-xlsx.png",
  )).filter((entry) => entry.name.startsWith("xl/worksheets/"))
    .map((entry) => entry.data.toString("utf8"));
  recorder.check("XLSX export of the native-date table writes text cells, not dates or formulas",
    derivedXlsx.length > 0
      && derivedXlsx.every((sheet) => !sheet.includes("<f"))
      && derivedXlsx.some((sheet) => sheet.includes(nativeDate.exportText)
        && sheet.includes(nativeDatetime.exportText)
        && sheet.includes(corpus.formulaLikeText.value)),
    { worksheetCount: derivedXlsx.length });
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

// ---- Minimal XLSX producer for native date cells ----
// The scenario knows the target table's physical field names only at runtime,
// so the workbook is generated here rather than pre-baked: a stored-zip writer
// plus the four minimal OOXML parts openpyxl needs to read native date cells.
const EXCEL_EPOCH_UTC = Date.UTC(1899, 11, 30);

export function excelSerial(year, monthIndex, day, hour = 0, minute = 0, second = 0, ms = 0) {
  return (Date.UTC(year, monthIndex, day, hour, minute, second, ms) - EXCEL_EPOCH_UTC)
    / 86_400_000;
}

function escapeXmlText(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;");
}

export function buildNativeDateXlsx({ header, rows }) {
  const headerCells = header
    .map((name, index) => columnCell(1, index, { text: name }));
  const bodyRows = rows.map((row, rowIndex) => {
    const cells = row.map((cell, columnIndex) => (
      typeof cell === "number"
        ? columnCell(rowIndex + 2, columnIndex, {
          serial: cell, style: columnIndex === 0 ? 1 : 2,
        })
        : columnCell(rowIndex + 2, columnIndex, { text: cell })
    ));
    return `<row r="${rowIndex + 2}">${cells.join("")}</row>`;
  });
  const sheet = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1">${headerCells.join("")}</row>${bodyRows.join("")}</sheetData></worksheet>`;
  return zipStoreSync([
    { name: "[Content_Types].xml", data: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>` },
    { name: "_rels/.rels", data: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>` },
    { name: "xl/workbook.xml", data: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>` },
    { name: "xl/_rels/workbook.xml.rels", data: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>` },
    {
      name: "xl/styles.xml",
      data: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="1"><fill><patternFill patternType="none"/></fill></fills><borders count="1"><border/></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="3"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="14" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/><xf numFmtId="22" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`,
    },
    { name: "xl/worksheets/sheet1.xml", data: sheet },
  ]);
}

function columnCell(rowNumber, columnIndex, { text, serial, style }) {
  const reference = `${columnName(columnIndex)}${rowNumber}`;
  if (typeof serial === "number") {
    return `<c r="${reference}" s="${style}"><v>${serial}</v></c>`;
  }
  return `<c r="${reference}" t="inlineStr"><is><t xml:space="preserve">${escapeXmlText(text)}</t></is></c>`;
}

function columnName(index) {
  let name = "";
  let value = index;
  while (value >= 0) {
    name = String.fromCharCode(65 + (value % 26)) + name;
    value = Math.floor(value / 26) - 1;
  }
  return name;
}

// ---- Minimal STORED zip writer (CRC-32 + local/central records) ----
const CRC_TABLE = new Int32Array(256).map((_, index) => {
  let value = index;
  for (let bit = 0; bit < 8; bit += 1) {
    value = value & 1 ? 0xedb88320 ^ (value >>> 1) : value >>> 1;
  }
  return value;
});

function crc32(buffer) {
  let crc = -1;
  for (const byte of buffer) {
    crc = (crc >>> 8) ^ CRC_TABLE[(crc ^ byte) & 0xff];
  }
  return (crc ^ -1) >>> 0;
}

export function zipStoreSync(entries) {
  const encoder = new TextEncoder();
  const localChunks = [];
  const centralChunks = [];
  let offset = 0;
  for (const entry of entries) {
    const nameBytes = encoder.encode(entry.name);
    const data = typeof entry.data === "string" ? encoder.encode(entry.data) : entry.data;
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(0x0800, 6);
    local.writeUInt16LE(0, 8);
    local.writeUInt16LE(0, 10);
    local.writeUInt16LE(0x21, 12);
    const crc = crc32(data);
    local.writeUInt32LE(crc, 14);
    local.writeUInt32LE(data.length, 18);
    local.writeUInt32LE(data.length, 22);
    local.writeUInt16LE(nameBytes.length, 26);
    localChunks.push(local, nameBytes, data);
    const central = Buffer.alloc(46);
    central.writeUInt32LE(0x02014b50, 0);
    central.writeUInt16LE(20, 4);
    central.writeUInt16LE(20, 6);
    central.writeUInt16LE(0x0800, 8);
    central.writeUInt16LE(0, 10);
    central.writeUInt16LE(0, 12);
    central.writeUInt16LE(0x21, 14);
    central.writeUInt32LE(crc, 16);
    central.writeUInt32LE(data.length, 20);
    central.writeUInt32LE(data.length, 24);
    central.writeUInt16LE(nameBytes.length, 28);
    central.writeUInt32LE(offset, 42);
    centralChunks.push(central, nameBytes);
    offset += 30 + nameBytes.length + data.length;
  }
  const centralDirectorySize = centralChunks.reduce(
    (total, chunk) => total + chunk.length, 0,
  );
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(entries.length, 8);
  end.writeUInt16LE(entries.length, 10);
  end.writeUInt32LE(centralDirectorySize, 12);
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...localChunks, ...centralChunks, end]);
}
