import fs from "node:fs/promises";
import path from "node:path";
import { isDeepStrictEqual } from "node:util";

// #435 synthetic source-migration journey on the real WPF management page.
//
// The Host TestMode seam (Astra) owns the synthetic provider and its fixture;
// this journey drives only the fixed, JSON-free control protocol and then
// verifies the RESULT through public surfaces: the #434 management page UI,
// the `data.importHistory` Go receipts, `schema.describe`, `query.page` and
// `file.list`. Nothing fabricates history — every assertion runs against
// real committed tables and the durable Go journal.
//
// Control protocol (as implemented by TestModeHostController/TestModeSourceImport):
// - request  : controls dir file `host-source-import.request`, strict UTF-8
//              text `success` | `drift` | `cancel` (no JSON, paths, or source).
// - response : controls dir file `host-source-import-state.json`:
//              { evidenceKind: "packaged-host-source-import", action, scenario,
//                workspaceId, sessionEpoch, taskId, state, error }.
//              `action` only reports control completion; the terminal task
//              `state` carries succeeded/failed/cancelled. drift/cancel end
//              as completed controls with failed/cancelled tasks, and Go
//              durably records the SAME job as a pre-submission terminal
//              receipt (private /start persists "preparing" before any
//              download; /finish writes failed/cancelled with zero
//              submitted work) — AC7 requires the durable failure result,
//              never a silently missing journal entry.
//
// Public receipt projection contract: `data.importHistory` migrations strip
// all batch mappings and field definitions. Identity therefore comes only
// from Target.sourceTableId→tableId, schema.describe column titles→field
// identities, and fixture code values→local record ids. Source record ids
// never leave the Go journal and are never assumed here.
export const SOURCE_IMPORT_REQUEST_CONTROL = "host-source-import.request";
export const SOURCE_IMPORT_STATE_CONTROL = "host-source-import-state.json";
export const SOURCE_IMPORT_STATE_EVIDENCE_KIND = "packaged-host-source-import";
export const SOURCE_IMPORT_ACTIONS = ["source-import-completed", "source-import-failed"];
export const SOURCE_IMPORT_MODES = ["success", "drift", "cancel"];

/**
 * Frozen mirror of the Host TestMode synthetic fixture (#435 AC1/AC2): 3
 * tables, a bidirectional pair (A.ab ⇄ B.ba), a cycle (A→B→C→A), identical
 * display titles across every table with distinct code identities, and one
 * real built-in PNG attachment on A-001. Record edges are expressed in code
 * values — the only source-side stable identity the public surface exposes.
 */
export const EXPECTED_SYNTHETIC_SOURCE = {
  provider: "synthetic",
  containerId: "qa-source-success",
  sourceName: "QA 三表合成来源",
  titleValue: "QA 重复显示值",
  tables: [
    {
      id: "a", name: "QA 来源迁移 A", records: 2, codes: ["A-001", "A-002"],
      fields: { title: "名称", code: "编码", ab: "关联 B", files: "附件" },
    },
    {
      id: "b", name: "QA 来源迁移 B", records: 2, codes: ["B-001", "B-002"],
      fields: { title: "名称", code: "编码", ba: "反向 A", bc: "关联 C" },
    },
    {
      id: "c", name: "QA 来源迁移 C", records: 2, codes: ["C-001", "C-002"],
      fields: { title: "名称", code: "编码", ca: "循环 A" },
    },
  ],
  relations: [
    { table: "a", field: "ab", targetTable: "b", reverseField: "ba", cardinality: "many" },
    { table: "b", field: "bc", targetTable: "c", reverseField: null, cardinality: "one" },
    { table: "c", field: "ca", targetTable: "a", reverseField: null, cardinality: "one" },
  ],
  edges: [
    { table: "a", code: "A-001", field: "ab", targets: ["B-001", "B-002"] },
    { table: "a", code: "A-002", field: "ab", targets: ["B-002"] },
    { table: "b", code: "B-001", field: "bc", targets: ["C-001"] },
    { table: "b", code: "B-002", field: "bc", targets: ["C-002"] },
    { table: "c", code: "C-001", field: "ca", targets: ["A-002"] },
    { table: "c", code: "C-002", field: "ca", targets: ["A-001"] },
  ],
  attachment: { table: "a", field: "files", code: "A-001", name: "qa-source-import.png" },
  // Pre-execute terminal scenarios run the same 6-record source shape under
  // scenario-specific identities; their durable receipts must show zero
  // submitted work (AC7) with the current session epoch.
  // TestModeSourceImport keeps ONE SourceName constant for all scenarios
  // (drift/cancel prefixes only rename target tables), so every negative
  // receipt carries the same sourceName and differs only by containerId.
  negative: {
    drift: { containerId: "qa-source-drift", sourceName: "QA 三表合成来源", state: "failed" },
    cancel: { containerId: "qa-source-cancel", sourceName: "QA 三表合成来源", state: "cancelled" },
  },
};

// Byte mirror of TestModeSourceImport.AttachmentBytes — the fixed built-in
// 1x1 PNG the provider serves. Used only for exact Buffer.equals of the
// Host-downloaded artifact; no hash layer is introduced.
export const TESTMODE_ATTACHMENT_PNG_BASE64 =
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aL1sAAAAASUVORK5CYII=";

export function sourceImportRequestText(mode) {
  if (!SOURCE_IMPORT_MODES.includes(mode)) {
    throw new Error(`Source import control mode must be one of ${SOURCE_IMPORT_MODES.join("/")}: ${mode}`);
  }
  // Bare word only: the Host parser trims at most 64 bytes and rejects
  // anything that is not a fixed scenario word.
  return mode;
}

export function parseSourceImportState(rawText) {
  let parsed;
  try {
    parsed = JSON.parse(rawText);
  } catch {
    throw new Error("Source import state control is not valid JSON.");
  }
  const state = parsed ?? {};
  if (state.evidenceKind !== SOURCE_IMPORT_STATE_EVIDENCE_KIND) {
    throw new Error(`Source import state evidenceKind must be "${SOURCE_IMPORT_STATE_EVIDENCE_KIND}": ${JSON.stringify(state)}`);
  }
  if (!SOURCE_IMPORT_ACTIONS.includes(state.action)) {
    throw new Error(`Source import state action must be ${SOURCE_IMPORT_ACTIONS.join("/")}: ${JSON.stringify(state)}`);
  }
  if (typeof state.scenario !== "string" || !state.scenario) {
    throw new Error(`Source import state scenario is missing: ${JSON.stringify(state)}`);
  }
  if (typeof state.workspaceId !== "string" || !state.workspaceId) {
    throw new Error(`Source import state workspaceId is missing: ${JSON.stringify(state)}`);
  }
  if (!Number.isInteger(state.sessionEpoch) || state.sessionEpoch < 1) {
    throw new Error(`Source import state sessionEpoch is invalid: ${JSON.stringify(state)}`);
  }
  if (state.action === "source-import-completed"
    && (typeof state.taskId !== "string" || !state.taskId
      || typeof state.state !== "string" || !state.state)) {
    throw new Error(`Completed source import control must carry taskId and state: ${JSON.stringify(state)}`);
  }
  if (state.taskId != null && typeof state.taskId !== "string") {
    throw new Error(`Source import state taskId is invalid: ${JSON.stringify(state)}`);
  }
  if (state.state != null && typeof state.state !== "string") {
    throw new Error(`Source import state state is invalid: ${JSON.stringify(state)}`);
  }
  if (state.error != null && typeof state.error !== "string") {
    throw new Error(`Source import state error is invalid: ${JSON.stringify(state)}`);
  }
  return {
    action: state.action,
    scenario: state.scenario,
    taskId: state.taskId ?? null,
    state: state.state ?? null,
    workspaceId: state.workspaceId,
    sessionEpoch: state.sessionEpoch,
    error: state.error ?? null,
  };
}

export function migrationJobIds(history) {
  return new Set((history?.migrations ?? []).map((entry) => {
    if (typeof entry?.jobId !== "string" || !entry.jobId) {
      throw new Error(`Migration history entry has no jobId: ${JSON.stringify(entry)}`);
    }
    return entry.jobId;
  }));
}

// A terminal success receipt keeps the strict arithmetic, all targets, and
// the read window; it never carries provider credentials, URLs, paths, or
// plan tokens (those stay Host-internal by #435's channel contract).
export function verifyMigrationReceipt(entry, expectation) {
  const problems = [];
  const require = (condition, message) => { if (!condition) problems.push(message); };
  require(entry != null && typeof entry === "object", "migration entry is missing");
  if (problems.length) return problems;
  require(entry.state === "succeeded", `state is ${entry.state}`);
  require(entry.stage === "settled", `stage is ${entry.stage}`);
  require(Number.isInteger(entry.created), "created is not an integer");
  require(Number.isInteger(entry.total), "total is not an integer");
  require(Number.isInteger(entry.notSubmitted), "notSubmitted is not an integer");
  require(Number.isInteger(entry.unknownRecords), "unknownRecords is not an integer");
  require(entry.created + entry.notSubmitted + entry.unknownRecords === entry.total,
    `counts do not add up: ${entry.created}+${entry.notSubmitted}+${entry.unknownRecords}!==${entry.total}`);
  require(entry.notSubmitted === 0 && entry.unknownRecords === 0,
    "terminal success still reports pending or unknown records");
  require(!entry.unknownBatch, `success carries unknownBatch ${entry.unknownBatch}`);
  require(typeof entry.sourceName === "string" && entry.sourceName.length > 0, "sourceName is empty");
  if (expectation?.sourceName) {
    require(entry.sourceName === expectation.sourceName,
      `sourceName ${entry.sourceName} !== fixture ${expectation.sourceName}`);
  }
  if (expectation?.provider) {
    require(entry.provider === expectation.provider,
      `provider ${entry.provider} !== fixture ${expectation.provider}`);
  }
  if (expectation?.containerId) {
    require(entry.containerId === expectation.containerId,
      `containerId ${entry.containerId} !== fixture ${expectation.containerId}`);
  }
  const totalRecords = (expectation?.tables ?? []).reduce((sum, table) => sum + table.records, 0);
  if (totalRecords > 0) {
    require(entry.total === totalRecords, `total ${entry.total} !== fixture records ${totalRecords}`);
    require(entry.created === totalRecords, `created ${entry.created} !== fixture records ${totalRecords}`);
  }
  const targets = Array.isArray(entry.targets) ? entry.targets : [];
  const expectedTables = expectation?.tables ?? [];
  require(targets.length === expectedTables.length,
    `targets ${targets.length} !== fixture tables ${expectedTables.length}`);
  for (const table of expectedTables) {
    const target = targets.find((item) => item.sourceTableId === table.id);
    require(target != null, `no committed target for source table ${table.id}`);
    if (target) {
      require(typeof target.tableId === "string" && target.tableId.length > 0,
        `target for ${table.id} has no local tableId`);
      require(target.name === table.name,
        `target name for ${table.id} is ${target.name}, fixture says ${table.name}`);
      require(typeof target.collection === "string" && target.collection.length > 0,
        `target for ${table.id} has no physical collection name`);
    }
  }
  require(Array.isArray(entry.batches) && entry.batches.length > 0, "no committed batches");
  const window = entry.readWindow;
  require(window != null && (window.consistency === "snapshot" || window.consistency === "window"),
    "readWindow consistency is missing");
  require(typeof window?.startedAt === "string" && window.startedAt
    && typeof window.finishedAt === "string" && window.finishedAt,
  "readWindow timestamps are missing");
  require(typeof entry.startedAt === "string" && entry.startedAt, "startedAt is missing");
  require(typeof entry.finishedAt === "string" && entry.finishedAt, "finishedAt is missing");
  if (Number.isInteger(expectation?.sessionEpoch)) {
    require(entry.sessionEpoch === expectation.sessionEpoch,
      `sessionEpoch ${entry.sessionEpoch} !== ${expectation.sessionEpoch}`);
  }
  for (const key of ["token", "sessionSecret", "url", "path", "accessToken", "credential"]) {
    require(!(key in entry), `receipt leaks forbidden key "${key}"`);
  }
  return problems;
}

// The public projection keeps one authoritative identity: Target
// sourceTableId → committed local table/collection. Everything else below is
// verified against the local authority itself, never against stripped
// journal mappings.
export function buildTableTargets(entry, expectation) {
  const targets = new Map();
  const localIds = new Set();
  for (const table of expectation.tables) {
    const target = (entry?.targets ?? []).find((item) => item.sourceTableId === table.id);
    if (!target) throw new Error(`receipt has no committed target for source table ${table.id}`);
    if (typeof target.tableId !== "string" || !target.tableId
      || typeof target.collection !== "string" || !target.collection) {
      throw new Error(`target for ${table.id} lacks tableId/collection`);
    }
    if (localIds.has(target.tableId)) throw new Error(`two source tables map to ${target.tableId}`);
    localIds.add(target.tableId);
    targets.set(table.id, { tableId: target.tableId, collection: target.collection, name: target.name });
  }
  return targets;
}

// schema.describe is the only public field-identity surface: a fixture field
// display name must resolve to exactly one committed column.
export function resolveFieldColumns(table, columns) {
  const resolved = new Map();
  for (const [fieldId, title] of Object.entries(table.fields)) {
    const matches = (columns ?? []).filter((column) => column.title === title);
    if (matches.length !== 1) {
      throw new Error(`field "${title}" of table ${table.id} resolved ${matches.length} columns, expected 1`);
    }
    const column = matches[0];
    if (typeof column.name !== "string" || !column.name
      || typeof column.fieldId !== "string" || !column.fieldId) {
      throw new Error(`column "${title}" of table ${table.id} has no name/fieldId`);
    }
    resolved.set(fieldId, column);
  }
  return resolved;
}

// Codes are the fixture's stable per-table record identity; duplicate titles
// are deliberately useless as identity. Each code must own exactly one row.
export function buildCodeIndex(table, rows, codeColumn) {
  const index = new Map();
  for (const code of table.codes) {
    const matches = (rows ?? []).filter((row) => row[codeColumn] === code);
    if (matches.length !== 1) {
      throw new Error(`code ${code} of table ${table.id} matched ${matches.length} rows, expected 1`);
    }
    index.set(code, matches[0]);
  }
  return index;
}

export function relationValueIds(value) {
  if (value == null) return [];
  if (typeof value === "string") return [value];
  if (Array.isArray(value) && value.every((item) => typeof item === "string")) {
    return [...value].sort();
  }
  throw new Error(`relation value is neither id nor id list: ${JSON.stringify(value)}`);
}

/**
 * Duplicate display titles across all tables must not blur identities: each
 * table keeps its own distinct row ids, its fixture code values, and a
 * single shared title text.
 */
export function verifyRecordIdentityByCode({ expectation, tables }) {
  const problems = [];
  for (const [tableId, verified] of tables) {
    const table = expectation.tables.find((item) => item.id === tableId);
    if (verified.rows.length !== table.records) {
      problems.push(`${table.id} has ${verified.rows.length} rows, fixture says ${table.records}`);
    }
    const titles = new Set(verified.rows.map((row) => row[verified.columns.get("title").name]));
    if (titles.size !== 1 || !titles.has(expectation.titleValue)) {
      problems.push(`${table.id} titles ${JSON.stringify([...titles])} !== single fixture value`);
    }
    const codes = verified.rows.map((row) => row[verified.columns.get("code").name]).sort();
    if (JSON.stringify(codes) !== JSON.stringify([...table.codes].sort())) {
      problems.push(`${table.id} codes ${JSON.stringify(codes)} !== ${JSON.stringify(table.codes)}`);
    }
    const ids = new Set(verified.rows.map((row) => row.id));
    if (ids.size !== verified.rows.length) problems.push(`${table.id} row ids are not distinct`);
  }
  return problems;
}

/**
 * Verifies the committed relation graph against the frozen fixture edges.
 * Edges are expressed in code identities; target rows resolve through the
 * target table's own code index, so a swapped or display-name-based mapping
 * cannot satisfy the check. For a declared reverse field the reciprocal
 * backlinks are asserted too.
 */
export function verifyRelationGraph({ expectation, tables }) {
  const problems = [];
  const verifiedOf = (tableId) => {
    const verified = tables.get(tableId);
    if (!verified || !verified.columns || !verified.codeIndex) {
      throw new Error(`table ${tableId} has no verified schema/code identity`);
    }
    return verified;
  };
  const rowOf = (tableId, code) => {
    const row = verifiedOf(tableId).codeIndex.get(code);
    if (!row) throw new Error(`code ${code} is not indexed in table ${tableId}`);
    return row;
  };
  for (const edge of expectation.edges) {
    const relation = expectation.relations.find(
      (item) => item.table === edge.table && item.field === edge.field);
    if (!relation) throw new Error(`fixture edge ${edge.table}.${edge.field} has no relation declaration`);
    const origin = verifiedOf(edge.table);
    const column = origin.columns.get(edge.field);
    if (!column) throw new Error(`table ${edge.table} has no resolved column for field ${edge.field}`);
    const row = rowOf(edge.table, edge.code);
    const expected = edge.targets.map((code) => rowOf(relation.targetTable, code).id).sort();
    const actual = relationValueIds(row[column.name]);
    if (JSON.stringify(actual) !== JSON.stringify(expected)) {
      problems.push(`${edge.table}.${edge.code}.${edge.field} is ${JSON.stringify(actual)},`
        + ` expected ${JSON.stringify(expected)} by code identity`);
    }
    if (!relation.reverseField) continue;
    const target = verifiedOf(relation.targetTable);
    const reverseColumn = target.columns.get(relation.reverseField);
    if (!reverseColumn) {
      throw new Error(`table ${relation.targetTable} has no resolved reverse column ${relation.reverseField}`);
    }
    for (const code of edge.targets) {
      const reverseActual = relationValueIds(rowOf(relation.targetTable, code)[reverseColumn.name]);
      if (!reverseActual.includes(row.id)) {
        problems.push(`reverse ${relation.targetTable}.${code}.${relation.reverseField}`
          + ` does not backlink ${edge.table}.${edge.code} (${row.id})`);
      }
    }
  }
  return problems;
}

// The migrated attachment must land as a real local file reference on the
// committed row: the physical cell holds the stored name the file capability
// reports, and the canonical ManagedAttachmentRef identifies the fixture by
// `originalName` (contracts/v2/fixtures/managed-attachment-ref.json) with no
// legacy `name` fallback. Capability-grade metadata never enters evidence.
export function verifyAttachmentBinding({ expectation, tables, fileListing }) {
  const problems = [];
  const table = expectation.tables.find((item) => item.id === expectation.attachment.table);
  const verified = tables.get(expectation.attachment.table);
  const column = verified.columns.get(expectation.attachment.field);
  if (!column) throw new Error(`table ${table.id} has no resolved column for the attachment field`);
  const row = verified.codeIndex.get(expectation.attachment.code);
  if (!row) throw new Error(`attachment code ${expectation.attachment.code} is not indexed`);
  const files = fileListing?.attachments;
  if (!Array.isArray(files)) {
    return ["file.list attachments is not an array, expected exactly one migrated attachment"];
  }
  if (files.length !== 1) {
    // Count-only diagnostics: whole refs are never stringified because they
    // carry capability-grade metadata.
    return [`file.list returned ${files.length} attachment(s), expected exactly one`];
  }
  const file = files[0];
  if (file.originalName !== expectation.attachment.name) {
    problems.push(`attachment originalName ${JSON.stringify(file.originalName ?? null)}`
      + ` !== ${expectation.attachment.name}`);
  }
  if (row[column.name] !== file.storedName) {
    problems.push(`row cell ${column.name} is ${JSON.stringify(row[column.name])}`
      + ` but storedName is ${file.storedName}`);
  }
  for (const key of ["url", "remoteUrl", "token"]) {
    if (key in file) problems.push(`attachment leaks forbidden key "${key}"`);
  }
  return problems;
}

/**
 * Safe evidence projection for file.list diagnostics: only the request
 * anchors and the necessary display/storage metadata survive. Capability
 * strings, hashes, thumbnails and any other ref internals are dropped — a
 * failure report must never widen the attachment channel's exposure.
 */
export function safeAttachmentEvidence({ tableId, recordId, fieldId, fileListing }) {
  const attachments = (Array.isArray(fileListing?.attachments) ? fileListing.attachments : [])
    .map((file) => ({
      originalName: typeof file?.originalName === "string" ? file.originalName : null,
      storedName: typeof file?.storedName === "string" ? file.storedName : null,
      mimeType: typeof file?.mimeType === "string" ? file.mimeType : null,
      size: typeof file?.size === "number" ? file.size : null,
    }));
  return { requested: { tableId, recordId, fieldId }, count: attachments.length, attachments };
}

/**
 * Correlated native download outcome: the Host reply must be a
 * file.downloadRequested response with outcome "saved" and no path echo
 * (NativeProductFileRequestController posts outcome only).
 */
export function verifyDownloadOutcome(response) {
  const problems = [];
  if (response?.type !== "file.downloadRequested") {
    problems.push(`download reply type is ${response?.type ?? "missing"}, expected file.downloadRequested`);
    return problems;
  }
  if (response.payload?.outcome !== "saved") {
    problems.push(`download outcome is ${JSON.stringify(response.payload?.outcome ?? null)}, expected saved`);
  }
  if (response.payload != null && "path" in response.payload) {
    problems.push("download reply leaks a filesystem path");
  }
  return problems;
}

/**
 * Strict offline-bytes contract for the AC6 evidence: the Host-saved file
 * must equal the fixed TestMode PNG byte for byte. Diagnostics report
 * lengths only — never the raw bytes or the base64 encoding.
 */
export function verifyOfflineAttachmentBytes({ savedBytes, expectedBase64 }) {
  if (!Buffer.isBuffer(savedBytes)) {
    return ["saved attachment bytes must be a Buffer"];
  }
  const problems = [];
  const expected = Buffer.from(expectedBase64, "base64");
  if (savedBytes.length === 0) problems.push("saved attachment is empty");
  if (savedBytes.length !== 0 && savedBytes.subarray(1, 4).toString("latin1") !== "PNG") {
    problems.push("saved attachment does not carry the PNG magic signature");
  }
  if (savedBytes.length !== expected.length) {
    problems.push(`saved length ${savedBytes.length} !== fixture length ${expected.length}`);
  } else if (!savedBytes.equals(expected)) {
    problems.push("saved bytes differ from the fixed TestMode fixture PNG");
  }
  return problems;
}

export function validateSyntheticExpectation(expectation) {
  const problems = [];
  if (!expectation || !Array.isArray(expectation.tables) || expectation.tables.length < 3) {
    problems.push("fixture expectation must declare at least 3 tables");
    return problems;
  }
  for (const key of ["provider", "containerId", "sourceName", "titleValue"]) {
    if (typeof expectation[key] !== "string" || !expectation[key]
      || expectation[key].includes("PLACEHOLDER")) {
      problems.push(`fixture expectation field "${key}" is not finalized`);
    }
  }
  const ids = new Set(expectation.tables.map((table) => table.id));
  if (ids.size !== expectation.tables.length) problems.push("fixture table ids are not unique");
  for (const table of expectation.tables) {
    if (!Number.isInteger(table.records) || table.records < 1) {
      problems.push(`fixture table ${table.id} record count is invalid`);
    }
    if (typeof table.name !== "string" || !table.name) {
      problems.push(`fixture table ${table.id} name is missing`);
    }
    if (!Array.isArray(table.codes) || table.codes.length !== table.records) {
      problems.push(`fixture table ${table.id} codes do not match its record count`);
    }
    if (!table.fields || typeof table.fields !== "object" || Object.keys(table.fields).length === 0) {
      problems.push(`fixture table ${table.id} declares no field names`);
    }
  }
  const relations = expectation.relations ?? [];
  if (!relations.some((item) => item.reverseField)) {
    problems.push("fixture must declare a bidirectional reverse field");
  }
  const firstTable = expectation.tables[0]?.id;
  const cycle = relations.some((item) => item.targetTable === firstTable && item.table !== firstTable);
  if (!cycle) problems.push("fixture must close a loop back into the first table");
  if (!Array.isArray(expectation.edges) || expectation.edges.length === 0) {
    problems.push("fixture must declare relation edges");
  }
  for (const [mode, negative] of Object.entries(expectation.negative ?? {})) {
    if (typeof negative?.containerId !== "string" || !negative.containerId
      || typeof negative?.sourceName !== "string" || !negative.sourceName
      || ![
        "failed", "cancelled",
      ].includes(negative.state)) {
      problems.push(`fixture negative scenario ${mode} is not finalized`);
    } else if (negative.sourceName !== expectation.sourceName) {
      problems.push(`fixture negative scenario ${mode} must keep the shared sourceName`
        + ` ${expectation.sourceName} (scenario prefixes only rename targets)`);
    }
  }
  if (!expectation.negative?.drift || !expectation.negative?.cancel) {
    problems.push("fixture must declare drift and cancel negative scenarios");
  }
  return problems;
}

/**
 * A pre-execute terminal receipt (drift/cancel after user confirmation):
 * Go persists the same job id with a durable failed/cancelled result while
 * NOTHING was submitted — created 0, notSubmitted covering the full total,
 * no targets, no batches, and at least one diagnostic explaining the stop.
 * This is the AC7 contract: the failure is reported authoritatively, not
 * silently omitted from history.
 */
export function verifyPreSubmissionTerminalReceipt(entry, expectation) {
  const problems = [];
  const require = (condition, message) => { if (!condition) problems.push(message); };
  require(entry != null && typeof entry === "object", "pre-submission receipt is missing");
  if (problems.length) return problems;
  require(entry.state === expectation.state,
    `state is ${entry.state}, expected terminal ${expectation.state}`);
  require(entry.state !== "succeeded", "pre-submission receipt claims success");
  require(Number.isInteger(entry.created), "created is not an integer");
  require(Number.isInteger(entry.total), "total is not an integer");
  require(Number.isInteger(entry.notSubmitted), "notSubmitted is not an integer");
  require(Number.isInteger(entry.unknownRecords), "unknownRecords is not an integer");
  require(entry.created + entry.notSubmitted + entry.unknownRecords === entry.total,
    `counts do not add up: ${entry.created}+${entry.notSubmitted}+${entry.unknownRecords}!==${entry.total}`);
  require(entry.created === 0, `created is ${entry.created}, no work may be submitted`);
  require(entry.unknownRecords === 0, "pre-submission stop cannot leave unknown records");
  require(entry.notSubmitted === entry.total,
    `notSubmitted ${entry.notSubmitted} must cover the whole total ${entry.total}`);
  if (Number.isInteger(expectation.totalRecords)) {
    require(entry.total === expectation.totalRecords,
      `total ${entry.total} !== fixture records ${expectation.totalRecords}`);
  }
  require(!entry.unknownBatch, `receipt carries unknownBatch ${entry.unknownBatch}`);
  require(entry.provider === expectation.provider,
    `provider ${entry.provider} !== ${expectation.provider}`);
  require(entry.containerId === expectation.containerId,
    `containerId ${entry.containerId} !== ${expectation.containerId}`);
  require(entry.sourceName === expectation.sourceName,
    `sourceName ${entry.sourceName} !== ${expectation.sourceName}`);
  require(Array.isArray(entry.targets) && entry.targets.length === 0,
    `targets must stay empty, found ${entry.targets?.length}`);
  require(Array.isArray(entry.batches) && entry.batches.length === 0,
    `batches must stay empty, found ${entry.batches?.length}`);
  require(Array.isArray(entry.diagnostics) && entry.diagnostics.length > 0
    && entry.diagnostics.every((item) => typeof item?.code === "string" && item.code),
  "diagnostics must carry at least one coded reason");
  // Go's pre-execute finish settles an untouched admission: stage stays
  // "preparing" and finishedAt is always written (Host validates the same).
  require(entry.stage === "preparing",
    `stage is ${entry.stage}, a settled admission must stay in preparing`);
  require(typeof entry.startedAt === "string" && entry.startedAt, "startedAt is missing");
  require(typeof entry.finishedAt === "string" && entry.finishedAt,
    "a settled pre-submission receipt must carry finishedAt");
  if (Number.isInteger(expectation.sessionEpoch)) {
    require(entry.sessionEpoch === expectation.sessionEpoch,
      `sessionEpoch ${entry.sessionEpoch} !== ${expectation.sessionEpoch}`);
  }
  for (const key of ["token", "sessionSecret", "url", "path", "accessToken", "credential"]) {
    require(!(key in entry), `receipt leaks forbidden key "${key}"`);
  }
  return problems;
}

/**
 * Structural contract for the negative controls: the durable journal gains
 * EXACTLY the expected new pre-submission terminal jobs, every older entry
 * keeps identical FACTS, and nothing is lost. Equality is structural
 * (util.isDeepStrictEqual): PocketBase 0.40.4 HTTP JSON is not deterministic
 * (encoding/json v2 without Deterministic), so the same receipt may
 * re-serialize map keys (e.g. schemaRevisions) in a different order across
 * reads — key order is not a fact. Values, field presence, the null/missing
 * distinction, and array ORDER all remain strictly compared; nothing is
 * sorted, dropped, or reduced to a subset. Errors report locating job ids
 * only, never receipt payloads.
 */
export function assertPreSubmissionTerminal(beforeHistory, afterHistory, expectedNewJobIds) {
  const before = new Map((beforeHistory?.migrations ?? []).map((entry) => [entry.jobId, entry]));
  const after = new Map((afterHistory?.migrations ?? []).map((entry) => [entry.jobId, entry]));
  const expected = new Set(expectedNewJobIds ?? []);
  const problems = [];
  if (expected.size !== (expectedNewJobIds ?? []).length) {
    problems.push("expected new job ids contain duplicates");
  }
  for (const id of expected) {
    if (!after.has(id)) problems.push(`expected new terminal job ${id} is missing from history`);
  }
  for (const [id, entry] of after) {
    if (before.has(id)) {
      if (!isDeepStrictEqual(entry, before.get(id))) {
        problems.push(`existing job ${id} changed after the negative controls`);
      }
    } else if (!expected.has(id)) {
      problems.push(`journal gained unexpected job ${id}`);
    }
  }
  for (const [id] of before) {
    if (!after.has(id)) problems.push(`journal lost job ${id}`);
  }
  return problems;
}

/**
 * The file-import journey has already reopened the workspace and may have
 * its management view open. Verify that session and its real history port.
 */
export async function awaitSourceImportEntryReady(page, rawBridgeRequest) {
  await page.getByTestId("nav-home").waitFor({ state: "visible", timeout: 60_000 });
  const session = await page.evaluate(
    () => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
  if (typeof session?.workspaceId !== "string" || !session.workspaceId
    || !Number.isInteger(session.sessionEpoch) || session.sessionEpoch < 1) {
    throw new Error(`Source import entry requires an active workspace session: ${JSON.stringify(session)}`);
  }
  const response = await rawBridgeRequest(page, "data.importHistory", {});
  if (response.type === "operation.failed" || response.payload?.error) {
    throw new Error(`data.importHistory failed at source import entry: ${JSON.stringify(response)}`);
  }
  return session;
}

/**
 * AC6 executing-state offline stage. Mirrors the repo's scenario05/26
 * pattern (page.context().setOffline + !navigator.onLine) with a local
 * try/finally: business runs ONLY after the renderer has proven offline, and
 * the connection is restored on every exit path (success, rejection, or
 * cancellation). This is the RENDERER's network posture only — the Host
 * process's own network is covered fail-closed by the runner's product
 * process-network observation, not by this switch.
 */
export async function withRendererOffline(page, action) {
  const context = page.context();
  await context.setOffline(true);
  try {
    const online = await page.evaluate(() => navigator.onLine);
    if (online !== false) {
      throw new Error(`renderer offline mode was not applied: navigator.onLine is ${JSON.stringify(online)}`);
    }
    return await action();
  } finally {
    await context.setOffline(false);
  }
}

export async function runSourceImportJourney(page, recorder, runtime, helpers) {
  const {
    rawBridgeRequest, openWorkspaceCenterFromSwitcher, replicaUiMethod,
    beginWritableWorkspaceBootstrapCapture, waitForCapturedBridgeMessage,
  } = helpers;
  const fixtureProblems = validateSyntheticExpectation(EXPECTED_SYNTHETIC_SOURCE);
  if (fixtureProblems.length) {
    throw new Error(`Synthetic source fixture expectation is not finalized: ${fixtureProblems.join("; ")}`);
  }
  const request = async (type, payload) => {
    const response = await rawBridgeRequest(page, type, payload);
    if (response.type === "operation.failed" || response.payload?.error) {
      throw new Error(`${type} failed: ${JSON.stringify(response)}`);
    }
    return response.payload;
  };
  const history = async () => request("data.importHistory", {});
  const query = { filters: [], sorts: [], offset: 0, limit: 100 };
  // Verifies one full pass of local authority state for all three tables:
  // schema columns, code-indexed rows, duplicate-title integrity, the loop
  // and reciprocal relation graph.
  const verifyLocalAuthority = async () => {
    const tables = new Map();
    for (const table of EXPECTED_SYNTHETIC_SOURCE.tables) {
      const described = await request("schema.describe", {
        collection: tableTargets.get(table.id).tableId, requestGeneration: 0,
        accepts: ["vibetable.relation-capabilities.v1", "vibetable.lookup-query.v1"],
      });
      const columns = resolveFieldColumns(table, described.schema?.columns ?? []);
      const result = await request("query.page", {
        tableId: tableTargets.get(table.id).tableId, query,
      });
      const rows = [...result.rows].sort((left, right) => left.id.localeCompare(right.id));
      tables.set(table.id, { rows, columns, codeIndex: buildCodeIndex(table, rows, columns.get("code").name) });
    }
    return tables;
  };

  const statePath = path.join(runtime.controlsDir, SOURCE_IMPORT_STATE_CONTROL);
  const requestPath = path.join(runtime.controlsDir, SOURCE_IMPORT_REQUEST_CONTROL);
  const evidence = (name) => path.join(runtime.evidenceDir, name);
  const writeControl = async (mode) => {
    // Remove the previous terminal state so a stale file can never satisfy
    // the next wait; the runner owns the controls directory.
    await fs.rm(statePath, { force: true });
    await fs.writeFile(requestPath, sourceImportRequestText(mode), "utf8");
  };
  const waitForState = async (timeoutMs, scenario) => {
    const deadline = Date.now() + timeoutMs;
    let lastError = null;
    while (Date.now() < deadline) {
      try {
        const state = parseSourceImportState(await fs.readFile(statePath, "utf8"));
        if (state.scenario === scenario) return state;
        lastError = new Error(`stale state for scenario ${state.scenario}`);
      } catch (error) {
        lastError = error;
      }
      await page.waitForTimeout(200);
    }
    throw new Error(`Host source import state for "${scenario}" never became valid: ${lastError}`);
  };
  const openManagement = async () => {
    const panel = page.getByTestId("import-management");
    if (!(await panel.isVisible().catch(() => false))) {
      await page.getByTestId("import-management-open").click();
    }
    await panel.waitFor({ state: "visible", timeout: 60_000 });
  };

  // Entry runs on the already-active workspace session (see
  // awaitSourceImportEntryReady); the caller keeps its import-management
  // view open and the same workspace/session is retained for the migration.
  const entrySession = await awaitSourceImportEntryReady(page, rawBridgeRequest);

  // ---- Real success migration through the Host TestMode provider ----
  await writeControl("success");
  const completed = await waitForState(180_000, "success");
  recorder.check("the real Host completes the synthetic 3-table migration with state succeeded",
    completed.action === "source-import-completed" && completed.state === "succeeded",
    { completed });

  const afterSuccess = await history();
  const entry = (afterSuccess.migrations ?? []).find((item) => item.jobId === completed.taskId);
  const successSession = await page.evaluate(
    () => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
  const receiptProblems = verifyMigrationReceipt(entry, {
    ...EXPECTED_SYNTHETIC_SOURCE, sessionEpoch: successSession.sessionEpoch,
  });
  recorder.check("the durable Go receipt is a terminal success with every target and no secrets",
    receiptProblems.length === 0
      && successSession.workspaceId === entrySession.workspaceId
      && successSession.sessionEpoch === entrySession.sessionEpoch,
    { problems: receiptProblems, entry, entrySession, successSession });

  const tableTargets = buildTableTargets(entry, EXPECTED_SYNTHETIC_SOURCE);
  const tables = await verifyLocalAuthority();
  recorder.check("duplicate display values keep distinct code identities in every target",
    verifyRecordIdentityByCode({ expectation: EXPECTED_SYNTHETIC_SOURCE, tables }).length === 0,
    { tables: [...tables.entries()].map(([id, value]) => ({ id, rows: value.rows.length })) });
  const graphProblems = verifyRelationGraph({ expectation: EXPECTED_SYNTHETIC_SOURCE, tables });
  recorder.check("loop and one-to-one relations resolve by code identity with reciprocal backlinks",
    graphProblems.length === 0, { problems: graphProblems });

  const attachmentTable = tables.get(EXPECTED_SYNTHETIC_SOURCE.attachment.table);
  const attachmentRequest = {
    tableId: tableTargets.get(EXPECTED_SYNTHETIC_SOURCE.attachment.table).tableId,
    recordId: attachmentTable.codeIndex.get(EXPECTED_SYNTHETIC_SOURCE.attachment.code).id,
    fieldId: attachmentTable.columns.get(EXPECTED_SYNTHETIC_SOURCE.attachment.field).fieldId,
  };
  const fileListing = await request("file.list", attachmentRequest);
  const attachmentProblems = verifyAttachmentBinding({
    expectation: EXPECTED_SYNTHETIC_SOURCE, tables, fileListing });
  recorder.check("the migrated attachment is a real local file reference on the committed row",
    attachmentProblems.length === 0,
    { problems: attachmentProblems, attachment: safeAttachmentEvidence({ ...attachmentRequest, fileListing }) });

  // ---- Management page: receipt, targets, physical table open ----
  await openManagement();
  await page.getByTestId("import-history-refresh").click();
  const migrationRow = page.locator(
    `[data-testid="source-import-row"][data-job-id="${completed.taskId}"]`);
  await migrationRow.waitFor({ state: "visible", timeout: 60_000 });
  await migrationRow.locator('[data-testid="source-count-created"]').waitFor({ state: "visible" });
  recorder.check("the management page renders the migration as terminal success",
    (await migrationRow.getAttribute("data-state")) === "succeeded"
      && (await migrationRow.getAttribute("data-stage")) === "settled", { taskId: completed.taskId });
  // Keep the real migration outcome inside the scroll container viewport so
  // the screenshot shows the rendered source-import receipt row.
  await migrationRow.scrollIntoViewIfNeeded();
  await page.screenshot({ path: evidence("35-source-import-history.png"), fullPage: true });
  await page.getByTestId(`source-import-detail-${completed.taskId}`).click();
  const targetButtons = migrationRow.locator('[data-testid="source-target-open"]');
  await targetButtons.first().waitFor({ state: "visible", timeout: 60_000 });
  recorder.check("the receipt lists every committed target as openable",
    (await targetButtons.count()) === EXPECTED_SYNTHETIC_SOURCE.tables.length, {});
  // The expanded target list stays in view for the evidence capture.
  await migrationRow.scrollIntoViewIfNeeded();
  await page.screenshot({ path: evidence("35-source-import-targets.png"), fullPage: true });
  const firstTarget = EXPECTED_SYNTHETIC_SOURCE.tables[0];
  await page.locator(
    `[data-testid="source-target-open"][data-table-id="${tableTargets.get(firstTarget.id).tableId}"]`).click();
  await page.getByTestId("toolbar-table-title").waitFor({ state: "visible" });
  recorder.check("opening a committed target physically switches the grid to that table",
    (await page.getByTestId("toolbar-table-title").textContent()).includes(firstTarget.name), {});

  // ---- Workspace close/reopen: journal, rows, relations persist ----
  const originalSession = await page.evaluate(
    () => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
  await openWorkspaceCenterFromSwitcher(page);
  const closed = await replicaUiMethod(page, recorder, "workspace.close", () =>
    page.getByTestId("workspace-center").getByRole("button", {
      name: /关闭当前工作区|Close current workspace/,
    }).click());
  recorder.check("source import acceptance closes the actual workspace",
    closed.result?.state === "closed", { closed });
  await beginWritableWorkspaceBootstrapCapture(page, originalSession.sessionEpoch, "workspace.open");
  await page.getByTestId("workspace-center").getByRole("button", { name: /E2E Product Workspace/ }).click();
  const reopened = await waitForCapturedBridgeMessage(page, 60_000);
  recorder.check("the workspace reopens with a fresh session epoch",
    reopened.payload.session.workspaceId === originalSession.workspaceId
      && reopened.payload.session.sessionEpoch > originalSession.sessionEpoch, { reopened });

  const afterReopen = await history();
  const reopenedEntry = (afterReopen.migrations ?? [])
    .find((item) => item.jobId === completed.taskId);
  const reopenProblems = verifyMigrationReceipt(reopenedEntry, {
    ...EXPECTED_SYNTHETIC_SOURCE, sessionEpoch: successSession.sessionEpoch,
  });
  recorder.check("the migration receipt survives the workspace reopen unchanged",
    reopenProblems.length === 0, { problems: reopenProblems, reopenedEntry });

  const reopenedTables = await verifyLocalAuthority();
  const reopenedGraphProblems = [
    ...verifyRecordIdentityByCode({
      expectation: EXPECTED_SYNTHETIC_SOURCE, tables: reopenedTables }),
    ...verifyRelationGraph({ expectation: EXPECTED_SYNTHETIC_SOURCE, tables: reopenedTables }),
  ];
  recorder.check("after reopen rows and relations still map to the same local records",
    reopenedGraphProblems.length === 0, { problems: reopenedGraphProblems });

  await openManagement();
  const reopenedRow = page.locator(
    `[data-testid="source-import-row"][data-job-id="${completed.taskId}"]`);
  await reopenedRow.waitFor({ state: "visible", timeout: 60_000 });
  await reopenedRow.scrollIntoViewIfNeeded();
  await page.screenshot({ path: evidence("35-source-import-reopened.png"), fullPage: true });
  const baselineRowCount = await page.getByTestId("source-import-row").count();

  // ---- Pre-execute drift and cancel: durable terminal receipts, zero effects ----
  // AC7: after user confirmation, an observation drift or a pre-execute
  // cancellation must still produce an authoritative Go result. The Host
  // terminal reply and the durable journal share the SAME job id. Snapshots
  // keep the parsed receipt OBJECTS so later comparisons stay structural
  // (PocketBase re-serializes map keys non-deterministically between reads).
  const beforeNegative = await history();
  const negativeEpoch = reopened.payload.session.sessionEpoch;
  const totalFixtureRecords = EXPECTED_SYNTHETIC_SOURCE.tables
    .reduce((sum, table) => sum + table.records, 0);
  const negativeReceipts = new Map();
  for (const mode of ["drift", "cancel"]) {
    const expectation = EXPECTED_SYNTHETIC_SOURCE.negative[mode];
    await writeControl(mode);
    const terminal = await waitForState(180_000, mode);
    recorder.check(`the ${mode} control ends as a ${expectation.state} task`,
      terminal.action === "source-import-completed"
        && terminal.state === expectation.state
        && terminal.taskId != null, { terminal });
    const afterMode = await history();
    const negativeEntry = (afterMode.migrations ?? [])
      .find((item) => item.jobId === terminal.taskId);
    const problems = verifyPreSubmissionTerminalReceipt(negativeEntry, {
      ...expectation,
      provider: EXPECTED_SYNTHETIC_SOURCE.provider,
      totalRecords: totalFixtureRecords,
      sessionEpoch: negativeEpoch,
    });
    recorder.check(`${mode} persists a durable ${expectation.state} receipt with zero submitted work`,
      problems.length === 0, { problems });
    negativeReceipts.set(terminal.taskId, negativeEntry);
  }
  const afterNegatives = await history();
  const structureProblems = assertPreSubmissionTerminal(
    beforeNegative, afterNegatives, [...negativeReceipts.keys()]);
  recorder.check("only the two pre-submission terminal receipts join the durable journal",
    structureProblems.length === 0, { problems: structureProblems });

  // The committed business authority — rows, relations, fields and the real
  // attachment — is untouched by the negative runs (same public queries as
  // the success phase).
  const afterNegativeTables = await verifyLocalAuthority();
  const negativeAttachmentTable = afterNegativeTables.get(EXPECTED_SYNTHETIC_SOURCE.attachment.table);
  const negativeAttachmentRequest = {
    tableId: tableTargets.get(EXPECTED_SYNTHETIC_SOURCE.attachment.table).tableId,
    recordId: negativeAttachmentTable.codeIndex.get(EXPECTED_SYNTHETIC_SOURCE.attachment.code).id,
    fieldId: negativeAttachmentTable.columns.get(EXPECTED_SYNTHETIC_SOURCE.attachment.field).fieldId,
  };
  const afterNegativeFiles = await request("file.list", negativeAttachmentRequest);
  const authorityProblems = [
    ...verifyRecordIdentityByCode({
      expectation: EXPECTED_SYNTHETIC_SOURCE, tables: afterNegativeTables }),
    ...verifyRelationGraph({ expectation: EXPECTED_SYNTHETIC_SOURCE, tables: afterNegativeTables }),
    ...verifyAttachmentBinding({
      expectation: EXPECTED_SYNTHETIC_SOURCE, tables: afterNegativeTables,
      fileListing: afterNegativeFiles }),
  ];
  recorder.check("drift and cancel leave committed rows, relations and the attachment untouched",
    authorityProblems.length === 0,
    { problems: authorityProblems,
      attachment: safeAttachmentEvidence({ ...negativeAttachmentRequest, fileListing: afterNegativeFiles }) });

  // The management page renders both new terminal outcomes.
  await openManagement();
  await page.getByTestId("import-history-refresh").click();
  let lastNegativeRow = null;
  for (const [taskId, receipt] of negativeReceipts) {
    const row = page.locator(
      `[data-testid="source-import-row"][data-job-id="${taskId}"]`);
    await row.waitFor({ state: "visible", timeout: 60_000 });
    recorder.check(`the management page renders job ${taskId} as terminal ${receipt.state}`,
      (await row.getAttribute("data-state")) === receipt.state, { taskId });
    lastNegativeRow = row;
  }
  recorder.check("the migration list gained exactly the two negative terminal rows",
    (await page.getByTestId("source-import-row").count()) === baselineRowCount + 2,
    { baselineRowCount });
  // Keep the real migration outcomes inside the scroll container viewport so
  // the screenshot actually shows the rendered negative receipts.
  if (lastNegativeRow) await lastNegativeRow.scrollIntoViewIfNeeded();
  await page.screenshot({ path: evidence("35-source-import-negative.png"), fullPage: true });

  // ---- Final reopen: every receipt persists by job id with identical facts ----
  const negativeSession = await page.evaluate(
    () => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
  await openWorkspaceCenterFromSwitcher(page);
  const negativeClosed = await replicaUiMethod(page, recorder, "workspace.close", () =>
    page.getByTestId("workspace-center").getByRole("button", {
      name: /关闭当前工作区|Close current workspace/,
    }).click());
  recorder.check("the negative-control acceptance closes the actual workspace",
    negativeClosed.result?.state === "closed", { negativeClosed });
  await beginWritableWorkspaceBootstrapCapture(
    page, negativeSession.sessionEpoch, "workspace.open");
  await page.getByTestId("workspace-center").getByRole("button", { name: /E2E Product Workspace/ }).click();
  const finalReopened = await waitForCapturedBridgeMessage(page, 60_000);
  recorder.check("the final reopen advances the session epoch again",
    finalReopened.payload.session.workspaceId === negativeSession.workspaceId
      && finalReopened.payload.session.sessionEpoch > negativeSession.sessionEpoch,
    { finalReopened });

  const afterFinalReopen = await history();
  // Full-fact structural persistence: every pre-existing receipt (the success
  // job included) keeps identical facts across the reopen, exactly the two
  // negative jobs exist, and each captured negative receipt is structurally
  // equal to its post-run snapshot. Key order is not a fact; values, field
  // presence, null/missing, and array order all are.
  const persistenceProblems = [
    ...assertPreSubmissionTerminal(beforeNegative, afterFinalReopen, [...negativeReceipts.keys()]),
  ];
  for (const [taskId, snapshot] of negativeReceipts) {
    const persisted = (afterFinalReopen.migrations ?? [])
      .find((item) => item.jobId === taskId);
    if (!persisted || !isDeepStrictEqual(persisted, snapshot)) {
      persistenceProblems.push(`negative job ${taskId} did not persist with identical facts`);
    }
  }
  const finalSuccessEntry = (afterFinalReopen.migrations ?? [])
    .find((item) => item.jobId === completed.taskId);
  if (!finalSuccessEntry || verifyMigrationReceipt(finalSuccessEntry, {
    ...EXPECTED_SYNTHETIC_SOURCE, sessionEpoch: successSession.sessionEpoch,
  }).length !== 0) {
    persistenceProblems.push("the success receipt did not persist with its original facts");
  }
  recorder.check("after the final reopen every migration receipt persists by job id unchanged",
    persistenceProblems.length === 0, { problems: persistenceProblems });

  // Offline stability of the committed success data one last time — now in
  // the EXECUTING offline state: the final Go-authority verification and the
  // real Host file.downloadRequested/PNG byte comparison run with the
  // renderer offline (scenario05/26 pattern); the Host channel's own network
  // posture is asserted separately fail-closed by the runner's product
  // process-network observation.
  await withRendererOffline(page, async () => {
    recorder.check("the renderer network stays offline across the final authority and download stage",
      await page.evaluate(() => navigator.onLine === false), {});
    const finalTables = await verifyLocalAuthority();
    const finalAttachmentTable = finalTables.get(EXPECTED_SYNTHETIC_SOURCE.attachment.table);
    const finalAttachmentRequest = {
      tableId: tableTargets.get(EXPECTED_SYNTHETIC_SOURCE.attachment.table).tableId,
      recordId: finalAttachmentTable.codeIndex.get(EXPECTED_SYNTHETIC_SOURCE.attachment.code).id,
      fieldId: finalAttachmentTable.columns.get(EXPECTED_SYNTHETIC_SOURCE.attachment.field).fieldId,
    };
    const finalFiles = await request("file.list", finalAttachmentRequest);
    const finalAuthorityProblems = [
      ...verifyRecordIdentityByCode({
        expectation: EXPECTED_SYNTHETIC_SOURCE, tables: finalTables }),
      ...verifyRelationGraph({ expectation: EXPECTED_SYNTHETIC_SOURCE, tables: finalTables }),
      ...verifyAttachmentBinding({
        expectation: EXPECTED_SYNTHETIC_SOURCE, tables: finalTables, fileListing: finalFiles }),
    ];
    recorder.check("the committed success data stays offline-stable across the final reopen",
      finalAuthorityProblems.length === 0,
      { problems: finalAuthorityProblems,
        attachment: safeAttachmentEvidence({ ...finalAttachmentRequest, fileListing: finalFiles }) });

    // ---- AC6: materialize the migrated attachment bytes through the real Host ----
    // file.list proves metadata and capability only. The offline contract uses
    // the real native download channel: the correlated file.downloadRequested
    // wire request carries the committed identity (tableId/recordId/fieldId +
    // storedName/originalName), TestMode resolves the save target from the
    // attachment-target.txt control, and the gateway writes the stored bytes.
    // rawBridgeRequest is used directly so a failed download stays a verifiable
    // outcome instead of a thrown wrapper error.
    const downloadTarget = path.join(runtime.evidenceDir, "35-source-import-attachment-download.png");
    await fs.writeFile(path.join(runtime.controlsDir, "attachment-target.txt"), downloadTarget, "utf8");
    const downloadReply = await rawBridgeRequest(page, "file.downloadRequested", {
      tableId: tableTargets.get(EXPECTED_SYNTHETIC_SOURCE.attachment.table).tableId,
      recordId: finalAttachmentTable.codeIndex.get(EXPECTED_SYNTHETIC_SOURCE.attachment.code).id,
      fieldId: finalAttachmentTable.columns.get(EXPECTED_SYNTHETIC_SOURCE.attachment.field).fieldId,
      storedName: finalFiles.attachments[0].storedName,
      originalName: EXPECTED_SYNTHETIC_SOURCE.attachment.name,
    }, 60_000);
    const outcomeProblems = verifyDownloadOutcome(downloadReply);
    let byteProblems = ["saved attachment bytes were not read because the download outcome failed"];
    if (outcomeProblems.length === 0) {
      byteProblems = verifyOfflineAttachmentBytes({
        savedBytes: await fs.readFile(downloadTarget),
        expectedBase64: TESTMODE_ATTACHMENT_PNG_BASE64,
      });
    }
    recorder.check("the real Host downloads the committed attachment and the saved bytes equal the fixture PNG",
      outcomeProblems.length === 0 && byteProblems.length === 0,
      { problems: [...outcomeProblems, ...byteProblems],
        replyOutcome: downloadReply?.payload?.outcome ?? null,
        evidence: path.basename(downloadTarget) });
  });
}
