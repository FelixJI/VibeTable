import fs from "node:fs/promises";
import path from "node:path";
import assert from "node:assert/strict";
import { nativePluginInput, openApplication } from "./plugin_applications.mjs";
import { picker } from "./plugin_capability_services.mjs";
import { requestTransactionNativeInput, runPluginTransactions } from "./plugin_transactions.mjs";

export function exactSyntheticCents(rows, field) {
  return rows.reduce((sum, row) => {
    const value = row[field];
    if (typeof value !== "string" || !/^-?\d+\.\d{2}$/.test(value)) throw new Error("synthetic money is not fixed-scale decimal text");
    return sum + BigInt(value.replace(".", ""));
  }, 0n).toString();
}

const effectKeys = rows => rows.map(row => JSON.stringify([row.tableId, row.recordId, row.operation])).sort();

export function assertDatasetSeedPreview(preview, bindings, headId) {
  assert.equal(preview.status, "prepared", "seed preview must be prepared");
  assert.equal(preview.plan.operations.length, 400, "seed has exactly 400 explicit creates");
  assert.deepEqual(Object.keys(preview.newIds).sort(), ["allocations", "lines"]);
  const expected = [{ tableId: bindings.heads.tableId, recordId: headId, operation: "update" }];
  for (const entity of ["lines", "allocations"]) {
    assert.equal(Object.keys(preview.newIds[entity]).length, 200, `seed creates exactly 200 ${entity}`);
    assert.equal(new Set(Object.values(preview.newIds[entity])).size, 200, `seed ${entity} IDs are unique`);
  }
  for (let i = 0; i < 200; i++) {
    assert.deepEqual(preview.plan.operations[2 * i], { entity: "lines", kind: "create", tempId: `dl${i}`, values: { amount: "0.01", head: headId } });
    assert.deepEqual(preview.plan.operations[2 * i + 1], { entity: "allocations", kind: "create", tempId: `da${i}`, values: { amount: "0.01", line: { entity: "lines", tempId: `dl${i}` } } });
    for (const [entity, tempId] of [["lines", `dl${i}`], ["allocations", `da${i}`]]) {
      const recordId = preview.newIds[entity][tempId];
      assert.ok(typeof recordId === "string" && recordId.length > 0, `seed is missing ${entity}.${tempId}`);
      expected.push({ tableId: bindings[entity].tableId, recordId, operation: "insert" });
    }
  }
  assert.deepEqual(effectKeys(preview.effects), effectKeys(expected), "canonical effects include only the 400 inserts and original head backlink update");
  return expected;
}

export function assertDatasetSeedReceipt(applied, preview, expected) {
  assert.equal(applied.status, "applied");
  assert.equal(applied.receipt.status, "applied");
  assert.ok(applied.receipt.changeSetId);
  for (const key of ["requestId", "workspaceId", "installationId"]) assert.equal(applied[key], preview[key], key);
  assert.deepEqual(applied.newIds, preview.newIds, "receipt preserves all prepared IDs");
  assert.deepEqual(effectKeys(applied.receipt.rows), effectKeys(expected), "receipt matches the entire canonical preview boundary");
}

export function assertDatasetSeedAuthority(rows, preview, original) {
  const headId = original.newIds.heads.h;
  const lineIds = [original.newIds.lines.l, ...Object.values(preview.newIds.lines)].sort();
  const allocationIds = [original.newIds.allocations.a, ...Object.values(preview.newIds.allocations)].sort();
  assert.deepEqual(rows.heads.map(row => row.id), [headId], "no additional business heads");
  assert.deepEqual(rows.lines.map(row => row.id).sort(), lineIds, "exactly the original line and 200 new lines");
  assert.deepEqual(rows.allocations.map(row => row.id).sort(), allocationIds, "exactly the original allocation and 200 new allocations");
  assert.equal(rows.heads[0].code, "SYNTHETIC-01");
  assert.equal(rows.heads[0].budget, "10.00");
  assert.deepEqual([...rows.heads[0].lines].sort(), lineIds, "head backlink includes every line exactly once");
  const lineAllocations = new Map([[original.newIds.lines.l, original.newIds.allocations.a]]);
  for (let i = 0; i < 200; i++) lineAllocations.set(preview.newIds.lines[`dl${i}`], preview.newIds.allocations[`da${i}`]);
  const allocationLines = new Map([...lineAllocations].map(([line, allocation]) => [allocation, line]));
  for (const row of rows.lines) {
    assert.equal(row.head, headId, "each line points to the original head");
    assert.deepEqual(row.allocations, [lineAllocations.get(row.id)], "line backlink matches its one allocation");
    assert.equal(row.amount, row.id === original.newIds.lines.l ? "8.00" : "0.01");
  }
  for (const row of rows.allocations) {
    assert.equal(row.line, allocationLines.get(row.id), "each allocation points to its planned line");
    assert.equal(row.amount, row.id === original.newIds.allocations.a ? "8.00" : "0.01");
  }
}

function transactionBoundaryDetails(value, rows) {
  const groups = new Map();
  for (const row of rows ?? []) {
    const key = JSON.stringify([row.tableId, row.operation]);
    const group = groups.get(key) ?? { tableId: row.tableId, operation: row.operation, count: 0 };
    group.count++; groups.set(key, group);
  }
  return { status: value?.status ?? null, requestId: value?.requestId ?? null, operations: value?.plan?.operations?.length ?? null,
    effects: rows?.length ?? 0, effectsByTableAndOperation: [...groups.values()] };
}

/** Real SDK model -> protected transaction -> shared dataset -> native atomic CSV. */
export async function runPluginDatasets(page, recorder, runtime, helpers) {
  await runPluginTransactions(page, recorder, runtime, helpers);
  const prior = JSON.parse(await fs.readFile(path.join(runtime.evidenceDir, "56-transactions.json"), "utf8"));
  let frame = page.frameLocator(".surface-shell iframe");
  async function finish(expected = "succeeded") {
    await frame.locator("#status").filter({ hasText: /^(succeeded|failed)$/ }).waitFor({ timeout: 120000 });
    const value = JSON.parse(await frame.locator("#result").innerText());
    if (await frame.locator("#status").innerText() !== expected) throw new Error(`dataset action failed: ${JSON.stringify(value)}`);
    return value;
  }
  async function action(id, expected = "succeeded") { await frame.locator(`#${id}`).click(); return finish(expected); }
  function verify(name, check, details) {
    let failure;
    try { check(); } catch (error) { failure = String(error); }
    recorder.check(name, failure === undefined, { ...details, ...(failure === undefined ? {} : { failure }) });
  }
  const bindings = prior.model.bindings;
  const headId = prior.applied.newIds.heads.h;
  const seed = await action("dataset-seed");
  let expectedEffects;
  verify("multi-page fixture has 400 creates and the canonical original head backlink update",
    () => { expectedEffects = assertDatasetSeedPreview(seed, bindings, headId); }, transactionBoundaryDetails(seed, seed?.effects));
  const approval = await requestTransactionNativeInput(page, frame.locator("#submit"),
    () => nativePluginInput(runtime, "click", "plugin-confirm-approve", "approved"));
  recorder.check("multi-page transaction uses the owned native confirmation", approval.status === "completed", { approval });
  const applied = await finish();
  verify("receipt commits exactly the 200 lines, 200 allocations and original head backlink from preview",
    () => assertDatasetSeedReceipt(applied, seed, expectedEffects), transactionBoundaryDetails(applied, applied?.receipt?.rows));
  const authority = {};
  for (const [entity, binding] of Object.entries(bindings)) {
    const schema = await helpers.rawBridgeRequest(page, "schema.getTable", { tableId: binding.tableId });
    const names = Object.fromEntries(schema.payload.fields.map(field => [field.identity.fieldId, field.identity.physicalName]));
    const rows = [];
    for (let offset = 0; offset <= (entity === "heads" ? 1 : 201); offset += 200) {
      const reply = await helpers.rawBridgeRequest(page, "query.page", { tableId: binding.tableId, query: { filters: [], sorts: [], offset, limit: 200 } });
      if (!Array.isArray(reply.payload?.rows)) throw new Error(`dataset seed authority query failed: ${JSON.stringify(reply)}`);
      rows.push(...reply.payload.rows);
      if (reply.payload.rows.length < 200) break;
    }
    authority[entity] = rows.map(row => ({ id: row.id, ...Object.fromEntries(Object.entries(binding.fields).map(([key, field]) => [key, row[names[field]]])) }));
  }
  verify("public authority has exactly 1/201/201 rows with all forward and reciprocal links and exact amounts",
    () => assertDatasetSeedAuthority(authority, seed, prior.applied), { rows: Object.fromEntries(Object.entries(authority).map(([entity, rows]) => [entity, rows.length])) });
  const opened = await action("dataset");
  const lines = prior.model.bindings.lines, allocations = prior.model.bindings.allocations;
  const lineSummary = opened.summaries.find(item => item.collection === lines.tableId);
  const allocationSummary = opened.summaries.find(item => item.collection === allocations.tableId);
  recorder.check("same three-table dataset has >one page in both detail sources", opened.sources.length === 3
    && opened.sources.find(item => item.collection === lines.tableId).filteredRows === 201
    && opened.sources.find(item => item.collection === allocations.tableId).filteredRows === 201, { opened });
  recorder.check("line and allocation exact totals both equal authoritative 10.00", lineSummary.summary.sums[0].value === "10.00"
    && allocationSummary.summary.sums[0].value === "10.00" && lineSummary.summary.complete && allocationSummary.summary.complete, { lineSummary, allocationSummary });
  const detail = await action("dataset-pages");
  recorder.check("both pages match the displayed dataset and sum exactly to 1000 cents", detail.datasetId === opened.datasetId
    && JSON.stringify(detail.versions) === JSON.stringify(opened.versions) && detail.pages === 2 && detail.rows.length === 201
    && new Set(detail.rows.map(row => row.id)).size === 201 && exactSyntheticCents(detail.rows, lines.fields.amount) === "1000", { pages: detail.pages });
  async function save(decision) {
    const clicking = frame.locator("#dataset-export").click();
    const native = await picker(runtime, "dataset", decision); await clicking;
    recorder.check(`dataset ${decision} uses owned native file selection`, native.status === "completed" && native.input === "SendInput", { native });
    return finish();
  }
  await fs.writeFile(path.join(runtime.controlsDir, "attachment-target.txt"), "native-dialog\n");
  const cancelled = await save("cancel");
  recorder.check("cancelled dataset save writes no file", cancelled === null
    && await fs.stat(path.join(runtime.controlsDir, "57-dataset.csv")).then(() => false, error => { if (error.code === "ENOENT") return true; throw error; }));
  const saved = await save("select");
  recorder.check("CSV save acknowledges the same displayed dataset and versions", saved.committed && saved.datasetId === opened.datasetId
    && saved.rowsWritten === detail.rows.length && JSON.stringify(saved.versions) === JSON.stringify(opened.versions), { saved });
  await fs.writeFile(path.join(runtime.controlsDir, "57-dataset-proof.json"), JSON.stringify({ fields: ["id", lines.fields.amount, lines.fields.head], amountField: lines.fields.amount, rows: detail.rows, datasetId: opened.datasetId, versions: opened.versions, saved }, null, 2));
  const header = prior.model.bindings.heads;
  const schema = await helpers.rawBridgeRequest(page, "schema.getTable", { tableId: header.tableId });
  const code = schema.payload.fields.find(field => field.identity.fieldId === header.fields.code).identity.physicalName;
  await helpers.applyProductMutation(page, header.tableId, [{ kind: "update", recordId: prior.applied.newIds.heads.h, values: { [code]: "SYNTHETIC-CHANGED" } }], "dataset-concurrent-update");
  const stale = await action("dataset-pages", "failed");
  recorder.check("a concurrent write to another source invalidates all remaining dataset pages", stale.code === "plugin_dataset_stale", { stale });
  const staleExport = await action("dataset-export", "failed");
  recorder.check("stale export never silently reopens or displays a newer snapshot", staleExport.code === "plugin_dataset_stale", { staleExport });
  const refreshed = await action("dataset");
  recorder.check("explicit refresh gets a new dataset and source revision", refreshed.datasetId !== opened.datasetId
    && refreshed.versions[header.tableId].dataRevision > opened.versions[header.tableId].dataRevision, { refreshed });
  await action("dataset-release");
  for (let i = 0; i < 3; i++) {
    await page.getByTestId("plugin-app-return").click();
    frame = await openApplication(page, "com.vibetable.examples.transaction-conformance", "open-transactions");
    await action("dataset");
  }
  recorder.check("repeated remounts can reopen a current dataset", await frame.locator("#dataset-state").innerText() === "current");
  await fs.writeFile(path.join(runtime.evidenceDir, "57-datasets.json"), JSON.stringify({ model: prior.model, seed, transaction: applied, authority, opened, detail, saved, stale, staleExport, refreshed }, null, 2));
  await page.screenshot({ path: path.join(runtime.evidenceDir, "57-datasets.png"), fullPage: true });
}
