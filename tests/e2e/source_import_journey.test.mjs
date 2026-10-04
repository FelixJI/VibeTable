import assert from "node:assert/strict";
import test from "node:test";

import {
  EXPECTED_SYNTHETIC_SOURCE, SOURCE_IMPORT_MODES, assertJournalUnchanged,
  buildCodeIndex, buildTableTargets, migrationJobIds, parseSourceImportState,
  relationValueIds, resolveFieldColumns, sourceImportRequestText,
  validateSyntheticExpectation, verifyAttachmentBinding, verifyMigrationReceipt,
  verifyRecordIdentityByCode, verifyRelationGraph,
} from "./source_import_journey.mjs";

// A mirror of the PUBLIC receipt projection: targets carry the authoritative
// sourceTableId→tableId/collection mapping, batches carry counts only (no
// mappings), field definitions are stripped. Local authority mirrors carry
// schema.describe columns (title/name/fieldId) and query.page rows keyed by
// the fixture code identity; display names never act as identity.
const LOCAL = {
  tables: { a: "tbl_a", b: "tbl_b", c: "tbl_c" },
  columns: {
    a: [
      { name: "col_a_title", title: "名称", fieldId: "f_a_title", kind: "text" },
      { name: "col_a_code", title: "编码", fieldId: "f_a_code", kind: "text" },
      { name: "col_a_ab", title: "关联 B", fieldId: "f_a_ab", kind: "relation" },
      { name: "col_a_files", title: "附件", fieldId: "f_a_files", kind: "attachment" },
    ],
    b: [
      { name: "col_b_title", title: "名称", fieldId: "f_b_title", kind: "text" },
      { name: "col_b_code", title: "编码", fieldId: "f_b_code", kind: "text" },
      { name: "col_b_ba", title: "反向 A", fieldId: "f_b_ba", kind: "relation" },
      { name: "col_b_bc", title: "关联 C", fieldId: "f_b_bc", kind: "relation" },
    ],
    c: [
      { name: "col_c_title", title: "名称", fieldId: "f_c_title", kind: "text" },
      { name: "col_c_code", title: "编码", fieldId: "f_c_code", kind: "text" },
      { name: "col_c_ca", title: "循环 A", fieldId: "f_c_ca", kind: "relation" },
    ],
  },
  rows: {
    a: [
      { id: "row_a1", col_a_title: "QA 重复显示值", col_a_code: "A-001",
        col_a_ab: ["row_b1", "row_b2"], col_a_files: "stored-a1.png" },
      { id: "row_a2", col_a_title: "QA 重复显示值", col_a_code: "A-002",
        col_a_ab: ["row_b2"], col_a_files: null },
    ],
    b: [
      { id: "row_b1", col_b_title: "QA 重复显示值", col_b_code: "B-001",
        col_b_ba: ["row_a1"], col_b_bc: "row_c1" },
      { id: "row_b2", col_b_title: "QA 重复显示值", col_b_code: "B-002",
        col_b_ba: ["row_a1", "row_a2"], col_b_bc: "row_c2" },
    ],
    c: [
      { id: "row_c1", col_c_title: "QA 重复显示值", col_c_code: "C-001", col_c_ca: "row_a2" },
      { id: "row_c2", col_c_title: "QA 重复显示值", col_c_code: "C-002", col_c_ca: "row_a1" },
    ],
  },
};

function journalEntry({ jobId = "task-success" } = {}) {
  return {
    contract: "vibetable.source-import.v1", jobId,
    provider: EXPECTED_SYNTHETIC_SOURCE.provider,
    containerId: EXPECTED_SYNTHETIC_SOURCE.containerId,
    sourceName: EXPECTED_SYNTHETIC_SOURCE.sourceName,
    state: "succeeded", stage: "settled",
    created: 6, total: 6, notSubmitted: 0, unknownRecords: 0,
    targets: EXPECTED_SYNTHETIC_SOURCE.tables.map((table) => ({
      sourceTableId: table.id, tableId: LOCAL.tables[table.id],
      name: table.name, collection: `col_${table.id}`,
    })),
    // Public projection: counts only — the full mappings stay Go-private.
    batches: [
      { batchId: "b1", stage: "schema", mappings: [], created: 0 },
      { batchId: "b2", stage: "records", mappings: [], created: 6 },
      { batchId: "b3", stage: "relations", mappings: [], created: 0, relationWrites: 8 },
      { batchId: "b4", stage: "attachments", mappings: [], created: 0, attachmentWrites: 1 },
    ],
    diagnostics: [],
    startedAt: "2026-10-04T08:00:00Z", finishedAt: "2026-10-04T08:00:05Z",
    sessionEpoch: 3,
    readWindow: { startedAt: "2026-10-04T08:00:00Z", finishedAt: "2026-10-04T08:00:01Z", consistency: "snapshot" },
  };
}

function verifiedTables(corrupt) {
  const tables = new Map();
  for (const table of EXPECTED_SYNTHETIC_SOURCE.tables) {
    const rows = JSON.parse(JSON.stringify(LOCAL.rows[table.id]));
    const columns = resolveFieldColumns(table, LOCAL.columns[table.id]);
    tables.set(table.id, {
      rows, columns, codeIndex: buildCodeIndex(table, rows, columns.get("code").name),
    });
  }
  if (corrupt) corrupt(tables);
  return tables;
}

test("control request text is the bare strict mode word only", () => {
  for (const mode of SOURCE_IMPORT_MODES) {
    assert.equal(sourceImportRequestText(mode), mode);
  }
  for (const invalid of ["success ", "SUCCESS", "drift\n", "{}", "success,cancel"]) {
    assert.throws(() => sourceImportRequestText(invalid), /mode must be one of/);
  }
});

test("state control parsing accepts only the packaged-host evidence shape", () => {
  const valid = parseSourceImportState(JSON.stringify({
    evidenceKind: "packaged-host-source-import", action: "source-import-completed",
    scenario: "success", taskId: "task-1", state: "succeeded",
    workspaceId: "w-1", sessionEpoch: 3, error: null,
  }));
  assert.deepEqual(valid, {
    action: "source-import-completed", scenario: "success", taskId: "task-1",
    state: "succeeded", workspaceId: "w-1", sessionEpoch: 3, error: null,
  });
  const terminalWithoutTask = parseSourceImportState(JSON.stringify({
    evidenceKind: "packaged-host-source-import", action: "source-import-failed",
    scenario: "invalid", workspaceId: "w-1", sessionEpoch: 3, error: "invalid scenario",
  }));
  assert.equal(terminalWithoutTask.taskId, null);
  for (const [label, raw] of [
    ["non-json", "not json"],
    ["wrong evidenceKind", JSON.stringify({ evidenceKind: "scenario", action: "source-import-completed" })],
    ["unknown action", JSON.stringify({ evidenceKind: "packaged-host-source-import", action: "x", scenario: "success" })],
    ["missing scenario", JSON.stringify({ evidenceKind: "packaged-host-source-import", action: "source-import-failed" })],
    ["missing workspaceId", JSON.stringify({ evidenceKind: "packaged-host-source-import", action: "source-import-failed", scenario: "cancel" })],
    ["epoch zero", JSON.stringify({ evidenceKind: "packaged-host-source-import", action: "source-import-completed", scenario: "success", sessionEpoch: 0 })],
    ["completed without task state", JSON.stringify({ evidenceKind: "packaged-host-source-import", action: "source-import-completed", scenario: "success", workspaceId: "w", sessionEpoch: 1 })],
  ]) {
    assert.throws(() => parseSourceImportState(raw), undefined, label);
  }
});

test("migration job ids are a strict set over well-formed entries", () => {
  assert.deepEqual([...migrationJobIds({ migrations: [{ jobId: "x" }, { jobId: "y" }] })].sort(), ["x", "y"]);
  assert.deepEqual([...migrationJobIds({})], []);
  assert.throws(() => migrationJobIds({ migrations: [{ state: "succeeded" }] }), /no jobId/);
});

test("terminal success receipt verification passes on the public projection", () => {
  assert.deepEqual(verifyMigrationReceipt(journalEntry(), EXPECTED_SYNTHETIC_SOURCE), []);
});

test("receipt verification rejects every semantic corruption", () => {
  const corrupt = (mutate) => {
    const entry = journalEntry();
    mutate(entry);
    return verifyMigrationReceipt(entry, EXPECTED_SYNTHETIC_SOURCE);
  };
  assert.ok(corrupt((entry) => { entry.state = "failed"; }).some((p) => p.includes("state is failed")));
  assert.ok(corrupt((entry) => { entry.stage = "records"; }).some((p) => p.includes("stage is records")));
  assert.ok(corrupt((entry) => { entry.notSubmitted = 1; }).some((p) => p.includes("add up") || p.includes("pending")));
  assert.ok(corrupt((entry) => { entry.created = 5; }).some((p) => p.includes("add up")));
  assert.ok(corrupt((entry) => { entry.targets = entry.targets.slice(1); }).some((p) => p.includes("targets 2")));
  assert.ok(corrupt((entry) => { entry.targets[0].name = "错名"; }).some((p) => p.includes("fixture says")));
  assert.ok(corrupt((entry) => { delete entry.targets[1].collection; }).some((p) => p.includes("physical collection")));
  assert.ok(corrupt((entry) => { delete entry.readWindow; }).some((p) => p.includes("readWindow")));
  assert.ok(corrupt((entry) => { entry.token = "leak"; }).some((p) => p.includes("forbidden key")));
  assert.ok(corrupt((entry) => { entry.containerId = "qa-source-drift"; }).some((p) => p.includes("containerId")));
  assert.ok(corrupt((entry) => { entry.total = 7; }).some((p) => p.includes("fixture records")));
  assert.deepEqual(verifyMigrationReceipt(null, EXPECTED_SYNTHETIC_SOURCE), ["migration entry is missing"]);
});

test("table targets are the only source-to-local table identity", () => {
  const targets = buildTableTargets(journalEntry(), EXPECTED_SYNTHETIC_SOURCE);
  assert.equal(targets.get("a").tableId, "tbl_a");
  assert.equal(targets.get("c").collection, "col_c");
  const collapsed = journalEntry();
  collapsed.targets[1].tableId = collapsed.targets[0].tableId;
  assert.throws(() => buildTableTargets(collapsed, EXPECTED_SYNTHETIC_SOURCE), /two source tables map/);
  const missing = journalEntry();
  missing.targets = missing.targets.slice(0, 1);
  assert.throws(() => buildTableTargets(missing, EXPECTED_SYNTHETIC_SOURCE), /no committed target/);
});

test("field columns resolve through unique schema.describe titles", () => {
  const table = EXPECTED_SYNTHETIC_SOURCE.tables[0];
  const columns = resolveFieldColumns(table, LOCAL.columns.a);
  assert.equal(columns.get("ab").name, "col_a_ab");
  assert.equal(columns.get("files").fieldId, "f_a_files");
  assert.throws(() => resolveFieldColumns(table, LOCAL.columns.b), /resolved 0 columns/);
  const duplicated = [...LOCAL.columns.a, { ...LOCAL.columns.a[2], fieldId: "f_dup" }];
  assert.throws(() => resolveFieldColumns(table, duplicated), /resolved 2 columns/);
});

test("code identity indexes exactly one row per fixture code", () => {
  const table = EXPECTED_SYNTHETIC_SOURCE.tables[0];
  const columns = resolveFieldColumns(table, LOCAL.columns.a);
  const index = buildCodeIndex(table, LOCAL.rows.a, columns.get("code").name);
  assert.equal(index.get("A-001").id, "row_a1");
  const missingCode = JSON.parse(JSON.stringify(LOCAL.rows.a));
  missingCode[0].col_a_code = "A-00X";
  assert.throws(() => buildCodeIndex(table, missingCode, columns.get("code").name), /matched 0 rows/);
  const duplicatedCode = [...LOCAL.rows.a, { ...LOCAL.rows.a[0], id: "row_a1_dup" }];
  assert.throws(() => buildCodeIndex(table, duplicatedCode, columns.get("code").name), /matched 2 rows/);
});

test("relation values normalize to sorted id lists", () => {
  assert.deepEqual(relationValueIds(null), []);
  assert.deepEqual(relationValueIds("row_b1"), ["row_b1"]);
  assert.deepEqual(relationValueIds(["row_b2", "row_b1"]), ["row_b1", "row_b2"]);
  assert.throws(() => relationValueIds({ id: "x" }), /neither id nor id list/);
  assert.throws(() => relationValueIds([1, 2]), /neither id nor id list/);
});

test("duplicate display values keep per-table identities and fixture codes", () => {
  assert.deepEqual(verifyRecordIdentityByCode({
    expectation: EXPECTED_SYNTHETIC_SOURCE, tables: verifiedTables() }), []);
  const crossContaminated = verifiedTables((tables) => {
    tables.get("a").rows[0].col_a_code = "B-001";
  });
  assert.ok(verifyRecordIdentityByCode({
    expectation: EXPECTED_SYNTHETIC_SOURCE, tables: crossContaminated })
    .some((p) => p.includes("codes")));
  const titleSplit = verifiedTables((tables) => {
    tables.get("a").rows[0].col_a_title = "另一个值";
  });
  assert.ok(verifyRecordIdentityByCode({
    expectation: EXPECTED_SYNTHETIC_SOURCE, tables: titleSplit })
    .some((p) => p.includes("titles")));
  const rowLoss = verifiedTables((tables) => {
    tables.get("c").rows.pop();
  });
  assert.ok(verifyRecordIdentityByCode({
    expectation: EXPECTED_SYNTHETIC_SOURCE, tables: rowLoss })
    .some((p) => p.includes("rows")));
});

test("the committed graph matches the fixture loop and reciprocal backlinks", () => {
  const args = { expectation: EXPECTED_SYNTHETIC_SOURCE, tables: verifiedTables() };
  assert.deepEqual(verifyRelationGraph(args), []);
  const wrongForward = verifiedTables((tables) => {
    // Code-identity violation: A-001's many-relation drops B-001 — a
    // display-name mapping would look identical, the code mapping does not.
    tables.get("a").rows[0].col_a_ab = ["row_b2"];
  });
  assert.ok(verifyRelationGraph({ ...args, tables: wrongForward })
    .some((p) => p.includes("a.A-001.ab")));
  const wrongTargetRow = verifiedTables((tables) => {
    // One-to-one edge points at the wrong local row of table C.
    tables.get("b").rows[0].col_b_bc = "row_c2";
  });
  assert.ok(verifyRelationGraph({ ...args, tables: wrongTargetRow })
    .some((p) => p.includes("b.B-001.bc")));
  const brokenReverse = verifiedTables((tables) => {
    tables.get("b").rows[1].col_b_ba = ["row_a2"];
  });
  assert.ok(verifyRelationGraph({ ...args, tables: brokenReverse })
    .some((p) => p.includes("reverse b.B-002.ba")));
  const brokenCycle = verifiedTables((tables) => {
    tables.get("c").rows[0].col_c_ca = "row_a1";
  });
  assert.ok(verifyRelationGraph({ ...args, tables: brokenCycle })
    .some((p) => p.includes("c.C-001.ca")));
  const emptied = new Map([...args.tables].map(([id]) => [id, { rows: [] }]));
  assert.throws(() => verifyRelationGraph({ ...args, tables: emptied }), /no verified schema\/code identity/);
});

test("attachment binding ties the row cell to the real stored file", () => {
  const args = {
    expectation: EXPECTED_SYNTHETIC_SOURCE, tables: verifiedTables(),
    fileListing: { attachments: [{ name: "qa-source-import.png", storedName: "stored-a1.png", sha256: "x" }] },
  };
  assert.deepEqual(verifyAttachmentBinding(args), []);
  const detached = verifiedTables((tables) => {
    tables.get("a").rows[0].col_a_files = null;
  });
  assert.ok(verifyAttachmentBinding({ ...args, tables: detached })
    .some((p) => p.includes("storedName")));
  assert.ok(verifyAttachmentBinding({ ...args, fileListing: { attachments: [] } })
    .some((p) => p.includes("exactly one")));
  assert.ok(verifyAttachmentBinding({
    ...args,
    fileListing: { attachments: [{ name: "qa-source-import.png", storedName: "s", url: "http://x" }] },
  }).some((p) => p.includes("forbidden key")));
});

test("the frozen QA fixture expectation validates as final", () => {
  assert.deepEqual(validateSyntheticExpectation(EXPECTED_SYNTHETIC_SOURCE), []);
  assert.ok(validateSyntheticExpectation({
    ...EXPECTED_SYNTHETIC_SOURCE, provider: "PLACEHOLDER-x",
  }).some((p) => p.includes("provider")));
  assert.ok(validateSyntheticExpectation({
    ...EXPECTED_SYNTHETIC_SOURCE, tables: EXPECTED_SYNTHETIC_SOURCE.tables.slice(0, 2),
  }).some((p) => p.includes("at least 3")));
  const noReverse = {
    ...EXPECTED_SYNTHETIC_SOURCE,
    relations: EXPECTED_SYNTHETIC_SOURCE.relations.map((item) => ({ ...item, reverseField: null })),
  };
  assert.ok(validateSyntheticExpectation(noReverse).some((p) => p.includes("bidirectional")));
  const noCycle = {
    ...EXPECTED_SYNTHETIC_SOURCE,
    relations: EXPECTED_SYNTHETIC_SOURCE.relations.filter((item) => item.targetTable !== "a"),
  };
  assert.ok(validateSyntheticExpectation(noCycle).some((p) => p.includes("loop")));
});

test("no-write controls leave the durable journal byte-identical", () => {
  const before = { migrations: [journalEntry()] };
  assert.deepEqual(assertJournalUnchanged(before, { migrations: [journalEntry()] }), []);
  const added = { migrations: [...before.migrations, journalEntry({ jobId: "task-drift" })] };
  assert.ok(assertJournalUnchanged(before, added).some((p) => p.includes("gained unexpected job")));
  const mutated = { migrations: [journalEntry()] };
  mutated.migrations[0].created = 5;
  mutated.migrations[0].total = 5;
  assert.ok(assertJournalUnchanged(before, mutated).some((p) => p.includes("changed")));
  const removed = { migrations: [] };
  assert.ok(assertJournalUnchanged(before, removed).some((p) => p.includes("lost job")));
});
