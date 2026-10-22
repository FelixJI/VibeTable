import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

import {
  sameRows,
  slowPreviewCancelEvidenceProblems,
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

test("cancelled slow-preview receipts accept honest phase measurements, not timing races", () => {
  // A real cancelled receipt reached the final row before cancellation was accepted.
  const lateCancelReceipt = {
    taskId: "task-f2ac4947857e",
    acceptedAt: 1792594130861,
    terminalAt: 1792594130921,
    terminal: {
      taskId: "task-f2ac4947857e",
      kind: "data.previewImport",
      state: "cancelled",
      progress: { done: 1000, total: 0, message: "解析来源文件：1000 行" },
      result: null,
      error: null,
      commitReceipt: null,
    },
    phase: "解析来源文件：100 行",
    fixture: { rows: 1000, columns: 128, bytes: 1098751 },
    acceptanceMs: 1772,
    drainMs: 60,
  };
  assert.deepEqual(slowPreviewCancelEvidenceProblems(lateCancelReceipt), []);
  // Cancellation during parsing remains valid.
  assert.deepEqual(slowPreviewCancelEvidenceProblems({
    ...lateCancelReceipt,
    terminal: {
      ...lateCancelReceipt.terminal,
      progress: { done: 500, total: 0, message: "解析来源文件：500 行" },
    },
  }), []);
  // Budget timeouts, wrong terminals, smuggled results/plans/receipts and
  // fabricated progress readings are each rejected on their own.
  for (const [corrupt, expectedProblem] of [
    [{ ...lateCancelReceipt, acceptanceMs: 10_000 }, "acceptanceMs"],
    [{ ...lateCancelReceipt, drainMs: 10_000 }, "drainMs"],
    [{ ...lateCancelReceipt, phase: "" }, "phase"],
    [{ ...lateCancelReceipt, acceptedAt: null }, "acceptedAt"],
    [{ ...lateCancelReceipt, terminal: { ...lateCancelReceipt.terminal, state: "succeeded" } }, "terminal state"],
    [{ ...lateCancelReceipt, terminal: { ...lateCancelReceipt.terminal, result: { plan: {} } } }, "terminal result"],
    [{ ...lateCancelReceipt, terminal: { ...lateCancelReceipt.terminal, error: "boom" } }, "terminal error"],
    [{ ...lateCancelReceipt, terminal: { ...lateCancelReceipt.terminal, commitReceipt: "committed" } }, "terminal commitReceipt"],
    [{
      ...lateCancelReceipt,
      terminal: {
        ...lateCancelReceipt.terminal,
        progress: { done: 1001, total: 0, message: "解析来源文件：1001 行" },
      },
    }, "terminal progress done"],
  ]) {
    const problems = slowPreviewCancelEvidenceProblems(corrupt);
    assert.equal(problems.length, 1, JSON.stringify(problems));
    assert.ok(problems[0].startsWith(expectedProblem), problems[0]);
  }
});
