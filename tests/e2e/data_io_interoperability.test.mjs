import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

import {
  sameRows,
  verifyNativeImportPickerReceipt,
} from "./data_io_interoperability.mjs";

test("authority comparison rejects duplicate, missing and mispaired rows", () => {
  const rows = [["day", "stamp-a", "=文本"], ["day", "stamp-b", "普通文本"]];
  assert.ok(sameRows([...rows].reverse(), rows));
  assert.equal(sameRows([rows[0], rows[0]], rows), false);
  assert.equal(sameRows(rows.slice(1), rows), false);
  assert.equal(sameRows([...rows, rows[0]], rows), false);
  assert.equal(sameRows([["day", "stamp-b", "=文本"], ["day", "stamp-a", "普通文本"]], rows), false);
});

test("native import picker receipts must prove the exact fixed selection", () => {
  const completed = {
    requestId: "id", status: "completed", action: "select", input: "SendInput",
    target: "导入-源-Cafe\u0301-👩🏽‍💻.csv",
    observation: { title: "Import table data", controls: [] },
  };
  assert.deepEqual(
    verifyNativeImportPickerReceipt(completed, "导入-源-Cafe\u0301-👩🏽‍💻.csv"),
    [],
  );
  // A cancelled, failed, control-path or foreign-dialog receipt never counts
  // as a completed native selection of the expected source.
  for (const [corrupt, expectedProblem] of [
    [{ ...completed, status: "failed" }, "status"],
    [{ ...completed, action: "cancel" }, "action"],
    [{ ...completed, target: "52-import.csv" }, "target"],
    [{ ...completed, input: "control-path" }, "input"],
    [{ ...completed, observation: { title: "Export table data" } }, "dialog title"],
  ]) {
    const problems = verifyNativeImportPickerReceipt(corrupt, "导入-源-Cafe\u0301-👩🏽‍💻.csv");
    assert.equal(problems.length, 1, JSON.stringify(problems));
    assert.ok(problems[0].startsWith(expectedProblem), problems[0]);
  }
  // A missing receipt reports every field instead of passing silently.
  assert.ok(verifyNativeImportPickerReceipt(undefined, "导入-源-Cafe\u0301-👩🏽‍💻.csv").length >= 1);
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
