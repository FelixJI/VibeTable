import assert from "node:assert/strict";
import test from "node:test";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import zlib from "node:zlib";

import {
  BASE_FILE_BULK_BATCHES, BASE_FILE_BULK_ROWS, BASE_FILE_NAME, BASE_FILE_TOKEN,
  EXPECTED_BASE_FILE_IMPORT, baseFileExpectedNoticeSubstrings, baseFilePolicyPlan,
  buildBaseFileFixture,
} from "./base_file_envelope.mjs";
import { verifyMigrationReceipt } from "./source_import_journey.mjs";
import {
  PICKER_REQUEST, PICKER_RESULT, SOURCE_IMPORT_NATIVE_REQUEST, SOURCE_IMPORT_NATIVE_RESULT,
} from "./feishu_base_file_journey.mjs";

function decodeSnapshot(envelopeBytes) {
  const envelope = JSON.parse(envelopeBytes.toString("utf8"));
  assert.equal(Object.keys(envelope).length, 1, "envelope stays the minimal gzipSnapshot member");
  return JSON.parse(zlib.gunzipSync(Buffer.from(envelope.gzipSnapshot, "base64")).toString("utf8"));
}

test("the synthetic .base envelope keeps the frozen FeishuBaseFileSource contract", () => {
  const snapshot = decodeSnapshot(buildBaseFileFixture());
  assert.equal(snapshot.length, 3);
  for (const item of snapshot) {
    const schema = item.schema;
    assert.equal(schema.structVersion, 1);
    assert.equal(schema.base.schemaVersion, 5);
    assert.equal(schema.base.token, BASE_FILE_TOKEN);
    const data = schema.data;
    const recordIds = Object.keys(data.recordMap);
    assert.equal(data.table.meta.schemaVersion, 5);
    assert.equal(data.table.meta.recordsNum, recordIds.length);
    assert.deepEqual(Object.keys(data.recordMeta).sort(), [...recordIds].sort());
    assert.ok(data.table.primaryKey in data.table.fieldMap);
    assert.ok(schema.tableMap[data.table.meta.id], "each snapshot table is declared in tableMap");
    for (const record of Object.values(data.recordMap)) {
      for (const fieldId of Object.keys(record)) {
        assert.ok(fieldId in data.table.fieldMap, `cell field ${fieldId} must be declared`);
        assert.equal(typeof record[fieldId].modifiedTime, "number");
      }
    }
  }
  const bulk = snapshot.find((item) => item.schema.data.table.meta.id === "tblQaBaseBulk0003");
  assert.equal(Object.keys(bulk.schema.data.recordMap).length, BASE_FILE_BULK_ROWS);
  // The single real file drives the parser AND the Go cross-batch chain:
  // 2501 rows need seven 400-record commits, so the fixture must keep the
  // original operation batch boundary intact instead of shrinking.
  assert.equal(Math.ceil(BASE_FILE_BULK_ROWS / 400), BASE_FILE_BULK_BATCHES);
});

test("every policy decision targets a declared lossy field with the closed combobox order", () => {
  const snapshot = decodeSnapshot(buildBaseFileFixture());
  const labels = ["原生迁移", "保留值快照", "跳过字段"];
  for (const decision of baseFilePolicyPlan()) {
    const [, , tableId, ...fieldIdParts] = decision.control.split("-");
    const fieldId = fieldIdParts.join("-");
    assert.equal(decision.control.startsWith(`field-policy-${tableId}-`), true);
    const item = snapshot.find((entry) => entry.schema.data.table.meta.id === tableId);
    assert.ok(item, `policy targets table ${tableId}`);
    assert.ok(fieldId in item.schema.data.table.fieldMap, `policy targets field ${fieldId}`);
    assert.ok([1, 2].includes(decision.index), "only lossy strategies need an explicit decision");
    assert.equal(decision.label, labels[decision.index]);
  }
  // No natively migratable field (text/number/select/dateTime/url) carries a
  // decision: the journey must not silently demote a native mapping.
  const decided = new Set(baseFilePolicyPlan().map((decision) => decision.control));
  for (const item of snapshot) {
    const tableId = item.schema.data.table.meta.id;
    for (const [fieldId, field] of Object.entries(item.schema.data.table.fieldMap)) {
      const native = [1, 2, 3, 5, 15].includes(field.type)
        && (fieldId !== "fldQaRich000012" || tableId !== "tblQaBaseFields01");
      if (native) assert.equal(decided.has(`field-policy-${tableId}-${fieldId}`), false);
    }
  }
});

test("the expected disclosures cover every frozen lossy disclosure family", () => {
  const substrings = baseFileExpectedNoticeSubstrings();
  for (const required of ["附件", "计算值未随文件导出", "人员字段", "视图配置", "原生映射", "富文本"]) {
    assert.ok(substrings.some((item) => item.includes(required)), `disclosure family ${required}`);
  }
  assert.equal(new Set(substrings).size, substrings.length);
});

test("the terminal .base receipt satisfies the durable projection contract", () => {
  const total = EXPECTED_BASE_FILE_IMPORT.tables.reduce((sum, table) => sum + table.records, 0);
  assert.equal(total, 2506);
  const recordBatches = [
    // The bulk table alone needs seven commits (6×400 + 101); the two small
    // tables add one batch each — 2506 rows over 9 record batches.
    ...Array.from({ length: BASE_FILE_BULK_BATCHES - 1 }, (_, index) => ({
      batchId: `bulk-${index}`, stage: "records", created: 400,
    })),
    { batchId: "bulk-tail", stage: "records", created: BASE_FILE_BULK_ROWS - 400 * (BASE_FILE_BULK_BATCHES - 1) },
    { batchId: "fields-table", stage: "records", created: 3 },
    { batchId: "values-table", stage: "records", created: 2 },
  ];
  assert.equal(recordBatches.reduce((sum, batch) => sum + batch.created, 0), total);
  const receipt = {
    contract: "vibetable.source-import.v1", jobId: "job-base-file",
    provider: EXPECTED_BASE_FILE_IMPORT.provider,
    containerId: EXPECTED_BASE_FILE_IMPORT.containerId,
    sourceName: EXPECTED_BASE_FILE_IMPORT.sourceName,
    state: "succeeded", stage: "settled",
    created: total, total, notSubmitted: 0, unknownRecords: 0, unknownBatch: null,
    targets: EXPECTED_BASE_FILE_IMPORT.tables.map((table) => ({
      sourceTableId: table.id, tableId: `tbl_${table.id}`, name: table.name,
      collection: `col_${table.id}`,
    })),
    batches: [{ batchId: "b-schema", stage: "schema", created: 0 }, ...recordBatches],
    diagnostics: [], notices: baseFileExpectedNoticeSubstrings().map((text) => `告知：${text}`),
    startedAt: "2026-10-04T08:00:00Z", finishedAt: "2026-10-04T08:00:05Z",
    sessionEpoch: 7,
    readWindow: { startedAt: "2026-10-04T08:00:00Z", finishedAt: "2026-10-04T08:00:01Z", consistency: "snapshot" },
  };
  assert.deepEqual(verifyMigrationReceipt(receipt, {
    ...EXPECTED_BASE_FILE_IMPORT, sessionEpoch: 7,
  }), []);
  const problems = verifyMigrationReceipt({ ...receipt, created: total - 1, containerId: "other" }, {
    ...EXPECTED_BASE_FILE_IMPORT, sessionEpoch: 7,
  });
  assert.ok(problems.length >= 2, problems.join("; "));
});

async function makeRuntime() {
  const evidenceDir = await mkdtemp(path.join(tmpdir(), "base-file-journey-"));
  return { evidenceDir, controlsDir: path.join(evidenceDir, "controls") };
}

test("the native and picker request protocols correlate only their own requestId", async () => {
  const runtime = await makeRuntime();
  const { beginPicker } = await import("./feishu_base_file_journey.mjs");
  const protocol = runtime.evidenceDir;

  // Stale results with foreign request ids must be ignored, the correlated
  // result must win, and a failed correlated result must surface as an error.
  await writeFile(path.join(protocol, SOURCE_IMPORT_NATIVE_RESULT),
    JSON.stringify({ requestId: "stale", status: "completed" }));
  const { requestNative } = await import("./feishu_base_file_journey.mjs");
  const pending = requestNative(runtime, { action: "observe" }, 5_000);
  const writeResult = async (payload) => writeFile(
    path.join(protocol, SOURCE_IMPORT_NATIVE_RESULT), JSON.stringify(payload), { flag: "w" });
  await new Promise((resolve) => setTimeout(resolve, 150));
  const requestText = JSON.parse(await readFile(path.join(protocol, SOURCE_IMPORT_NATIVE_REQUEST), "utf8"));
  assert.equal(requestText.action, "observe");
  assert.deepEqual(
    Object.keys(requestText).sort(),
    ["action", "contains", "index", "requestId", "shot", "target", "textKind"]);
  await writeResult({ requestId: requestText.requestId, status: "completed", present: false });
  const observed = await pending;
  assert.equal(observed.present, false);

  const awaitPicker = await beginPicker(runtime, "cancel");
  await new Promise((resolve) => setTimeout(resolve, 150));
  const pickerText = JSON.parse(await readFile(path.join(protocol, PICKER_REQUEST), "utf8"));
  assert.deepEqual(pickerText, { requestId: pickerText.requestId, kind: "base", action: "cancel" });
  await writeFile(path.join(protocol, PICKER_RESULT),
    JSON.stringify({ requestId: "foreign", status: "completed" }), { flag: "w" });
  await new Promise((resolve) => setTimeout(resolve, 120));
  await writeFile(path.join(protocol, PICKER_RESULT),
    JSON.stringify({ requestId: pickerText.requestId, status: "completed", target: BASE_FILE_NAME }),
    { flag: "w" });
  assert.equal((await awaitPicker()).target, BASE_FILE_NAME);
  await rm(protocol, { recursive: true, force: true });
});

test("the native protocol rejects a failed correlated helper result", async () => {
  const runtime = await makeRuntime();
  const { requestNative } = await import("./feishu_base_file_journey.mjs");
  const pending = requestNative(runtime, { action: "gone" }, 5_000);
  await new Promise((resolve) => setTimeout(resolve, 150));
  const requestText = JSON.parse(await readFile(
    path.join(runtime.evidenceDir, SOURCE_IMPORT_NATIVE_REQUEST), "utf8"));
  await writeFile(path.join(runtime.evidenceDir, SOURCE_IMPORT_NATIVE_RESULT),
    JSON.stringify({ requestId: requestText.requestId, status: "failed", error: "owned window missing" }),
    { flag: "w" });
  await assert.rejects(pending, /native source import helper failed/);
  await rm(runtime.evidenceDir, { recursive: true, force: true });
});

test("exact value comparison catches any fidelity drift in committed rows", async () => {
  const { verifyRowValues } = await import("./feishu_base_file_journey.mjs");
  const expected = { col_code: "00042", col_flag: false, col_raw: { flag: false } };
  assert.deepEqual(verifyRowValues({ col_code: "00042", col_flag: false, col_raw: { flag: false } }, expected), []);
  assert.equal(verifyRowValues({ col_code: "42", col_flag: false, col_raw: { flag: false } }, expected).length, 1);
  assert.equal(verifyRowValues({ col_code: "00042", col_flag: 0, col_raw: { flag: false } }, expected).length, 1);
  assert.equal(verifyRowValues(undefined, expected).length, Object.keys(expected).length);
});

test("the fixture is written under the fixed controls-root name the picker allows", () => {
  assert.equal(BASE_FILE_NAME, "35-feishu-base-import.base");
  assert.match(BASE_FILE_NAME, /^[A-Za-z0-9._-]+$/);
});
