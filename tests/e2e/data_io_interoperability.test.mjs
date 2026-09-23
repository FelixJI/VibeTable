import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

import {
  buildNativeDateXlsx, excelSerial, zipStoreSync,
} from "./data_io_interoperability.mjs";
import { zipEntries } from "./relation_lookup_data_io.mjs";

// The frozen serials were derived independently with openpyxl 3.1.5's
// to_excel (46263 and 46263.586876423615); the JS producer must emit the same
// IEEE-754 value, including its shortest round-trip decimal text.
test("excel serial matches the openpyxl-derived frozen values", () => {
  assert.equal(excelSerial(2026, 7, 29), 46263);
  assert.equal(excelSerial(2026, 7, 29, 14, 5, 6, 123).toString(), "46263.586876423615");
});

test("the native-date workbook keeps serials, inline strings and date styles", () => {
  const bytes = buildNativeDateXlsx({
    header: ["f_day", "f_stamp", "f_note"],
    rows: [
      [excelSerial(2026, 7, 29), excelSerial(2026, 7, 29, 14, 5, 6, 123), "=文本"],
    ],
  });
  const parts = new Map(zipEntries(bytes).map((entry) => [entry.name, entry.data.toString("utf8")]));
  const sheet = parts.get("xl/worksheets/sheet1.xml");
  assert.ok(sheet.includes("f_day"));
  assert.ok(sheet.includes("<v>46263</v>"));
  assert.ok(sheet.includes("<v>46263.586876423615</v>"));
  assert.ok(sheet.includes('s="1"') && sheet.includes('s="2"'));
  assert.ok(sheet.includes("=文本"));
  assert.ok(!sheet.includes("<f>"));
  assert.ok(parts.get("xl/styles.xml").includes('numFmtId="14"'));
  assert.ok(parts.get("xl/styles.xml").includes('numFmtId="22"'));
  assert.ok(parts.has("[Content_Types].xml"));
});

test("a stored zip entry round-trips through the shared zip walker", () => {
  const bytes = zipStoreSync([
    { name: "a.txt", data: "hello" },
    { name: "b/deep/name.bin", data: new Uint8Array([1, 2, 3, 250]) },
  ]);
  const entries = zipEntries(bytes);
  assert.equal(entries.length, 2);
  assert.deepEqual(
    entries.map((entry) => entry.name),
    ["a.txt", "b/deep/name.bin"],
  );
  assert.equal(entries[0].data.toString("utf8"), "hello");
  assert.deepEqual([...entries[1].data], [1, 2, 3, 250]);
});

test("the corpus keeps every declared Unicode code point exactly", async () => {
  const corpus = JSON.parse(await readFile(
    new URL("../fixtures/data-io/a5-interop-matrix-corpus.json", import.meta.url), "utf8",
  ));
  for (const item of corpus.unicodeRepresentatives) {
    assert.deepEqual(
      [...item.value].map((character) => character.codePointAt(0)),
      item.codePoints,
      item.key,
    );
  }
  const nfc = corpus.unicodeRepresentatives.find((item) => item.key === "nfc");
  const nfd = corpus.unicodeRepresentatives.find((item) => item.key === "nfd");
  assert.notEqual(nfc.value, nfd.value);
  assert.equal(nfc.value.normalize("NFD"), nfd.value);
  for (const item of corpus.dateCases) {
    assert.equal(item.exportText, item.queryWire, item.key);
  }
});
