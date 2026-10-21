import fs from "node:fs/promises";
import path from "node:path";
import { isDeepStrictEqual } from "node:util";

import {
  BASE_FILE_BATCH_BUDGET, BASE_FILE_BULK_BATCHES, BASE_FILE_BULK_ROWS, BASE_FILE_NAME,
  BASE_FILE_PICKER_TITLE, EXPECTED_BASE_FILE_IMPORT, baseFileExpectedNoticeSubstrings,
  baseFilePolicyPlan, buildBaseFileFixture,
} from "./base_file_envelope.mjs";
import {
  assertPreSubmissionTerminal, awaitSourceImportEntryReady, buildCodeIndex,
  resolveFieldColumns, verifyMigrationReceipt, withRendererOffline,
} from "./source_import_journey.mjs";

// #436/#437 formal .base file import regression on the real WPF wizard.
//
// The whole chain runs through real user surfaces only: the Web import
// management entry sends sourceImport.open (wire provider feishuBase, read
// from the Owner's HostSourceImportRequestController), the Host opens the
// owned SourceImportWindow which auto-opens the system picker titled
// 导入飞书 .base 文件, the picker request is answered by the CI-only
// SendInput helper with one fixed synthetic file, and the wizard's
// table/strategy/notice/preview/start decisions are driven through observed
// UIA bounds with real SendInput. Nothing bypasses the picker and no
// production bridge/backend method is invoked to fake user input; the only
// bridge traffic is the public read surface (data.importHistory,
// schema.describe, query.page) that also backs the management UI.
//
// Envelope facts (schemaVersion 5/structVersion 1/gzipSnapshot, field shapes)
// mirror the frozen FeishuBaseFileSourceTests builder; no unverified field
// encoding is invented. Native relation recovery is explicitly NOT expected:
// the .base container carries no relation metadata, so no relation graph is
// asserted for a JSON snapshot.
export const SOURCE_IMPORT_NATIVE_REQUEST = "source-import-native-request.json";
export const SOURCE_IMPORT_NATIVE_RESULT = "source-import-native-result.json";
export const PICKER_REQUEST = "plugin-file-dialog-request.json";
export const PICKER_RESULT = "plugin-file-dialog-result.json";
// The landed web catalog id for the local .base source card (Owner-final);
// the retired cloud ids are deliberately absent so an old UI fails here.
export const BASE_FILE_ENTRY_TESTID = "import-source-feishuBase";
// Landed history selectors (SourceImportHistory.vue): the persisted
// 来源文件说明（含未选表） renders under source-notices/source-notice only
// in the expanded receipt detail; no fallback guessing is attempted.
const HISTORY_NOTICES_TESTID = "source-notices";
const HISTORY_NOTICE_ITEM_TESTID = "source-notice";

function nativeResultProblem(result) {
  if (result?.status !== "completed") {
    return `native source import helper failed: ${JSON.stringify(result)}`;
  }
  return null;
}

export async function requestNative(runtime, request, timeoutMs = 60_000) {
  const requestId = `base-file-${Date.now()}-${Math.random().toString(16).slice(2, 8)}`;
  const target = path.join(runtime.evidenceDir, SOURCE_IMPORT_NATIVE_REQUEST);
  const payload = {
    requestId,
    target: null,
    index: null,
    textKind: null,
    contains: null,
    shot: false,
    ...request,
  };
  await fs.writeFile(`${target}.tmp`, JSON.stringify(payload));
  await fs.rename(`${target}.tmp`, target);
  const until = Date.now() + timeoutMs;
  while (Date.now() < until) {
    const result = await fs.readFile(
      path.join(runtime.evidenceDir, SOURCE_IMPORT_NATIVE_RESULT), "utf8")
      .then((text) => JSON.parse(text))
      .catch((error) => { if (error.code === "ENOENT") return null; throw error; });
    if (result?.requestId === requestId) {
      const problem = nativeResultProblem(result);
      if (problem) throw new Error(problem);
      return result;
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`source import native request deadline: ${request.action}`);
}

export async function beginPicker(runtime, action) {
  const requestId = `base-file-picker-${Date.now()}-${Math.random().toString(16).slice(2, 8)}`;
  const target = path.join(runtime.evidenceDir, PICKER_REQUEST);
  await fs.writeFile(`${target}.tmp`, JSON.stringify({ requestId, kind: "base", action }));
  await fs.rename(`${target}.tmp`, target);
  return async () => {
    const until = Date.now() + 45_000;
    while (Date.now() < until) {
      const result = await fs.readFile(path.join(runtime.evidenceDir, PICKER_RESULT), "utf8")
        .then((text) => JSON.parse(text))
        .catch((error) => { if (error.code === "ENOENT") return null; throw error; });
      if (result?.requestId === requestId) {
        if (result.status !== "completed") {
          throw new Error(`real .base picker failed: ${JSON.stringify(result)}`);
        }
        return result;
      }
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    throw new Error("real .base picker deadline");
  };
}

async function openManagement(page) {
  const panel = page.getByTestId("import-management");
  if (!(await panel.isVisible().catch(() => false))) {
    await page.getByTestId("import-management-open").click();
  }
  await panel.waitFor({ state: "visible", timeout: 60_000 });
}

async function clickBaseFileEntry(page) {
  const card = page.getByTestId(BASE_FILE_ENTRY_TESTID);
  await card.waitFor({ state: "visible", timeout: 60_000 });
  await card.click();
  return BASE_FILE_ENTRY_TESTID;
}

export function controlOf(observation, id) {
  return (observation?.controls ?? []).find((item) => item.id === id) ?? null;
}

async function captureWizardEvidence(runtime, name) {
  await fs.copyFile(
    path.join(runtime.evidenceDir, "source-import-screenshot.png"),
    path.join(runtime.evidenceDir, name));
}

function verifyNotices(expectedSubstrings, noticeTexts) {
  return expectedSubstrings.filter((needle) =>
    !noticeTexts.some((text) => typeof text === "string" && text.includes(needle)));
}

// Exact committed-value comparison for the fidelity ACs. Numbers keep JSON
// fidelity; leading-zero texts stay strings; raw JSON snapshots (person,
// attachment refs, widget booleans, rich text) are compared structurally.
export function verifyRowValues(row, expectedByPhysicalName) {
  const problems = [];
  for (const [physicalName, value] of Object.entries(expectedByPhysicalName)) {
    const actual = row?.[physicalName];
    if (!isDeepStrictEqual(actual, value)) {
      problems.push(`column ${physicalName} is ${JSON.stringify(actual)}, expected ${JSON.stringify(value)}`);
    }
  }
  return problems;
}

export async function runFeishuBaseFileJourney(page, recorder, runtime, helpers) {
  const { rawBridgeRequest, openWorkspaceCenterFromSwitcher, replicaUiMethod,
    beginWritableWorkspaceBootstrapCapture, waitForCapturedBridgeMessage } = helpers;
  const request = async (type, payload) => {
    const response = await rawBridgeRequest(page, type, payload);
    if (response.type === "operation.failed" || response.payload?.error) {
      throw new Error(`${type} failed: ${JSON.stringify(response)}`);
    }
    return response.payload;
  };
  const history = async () => request("data.importHistory", {});
  const query = { filters: [], sorts: [], offset: 0, limit: 100 };
  const evidence = (name) => path.join(runtime.evidenceDir, name);
  const writeEvidence = async (name, value) =>
    fs.writeFile(evidence(name), JSON.stringify(value, null, 2), "utf8");
  const summary = { pickerTitle: BASE_FILE_PICKER_TITLE, entry: null, cancel: {}, success: {} };

  // ---- Synthetic envelope: fixed file, fixed controls-root path ----
  await fs.writeFile(path.join(runtime.controlsDir, BASE_FILE_NAME), buildBaseFileFixture());
  const entrySession = await awaitSourceImportEntryReady(page, rawBridgeRequest);
  const expectedNotices = baseFileExpectedNoticeSubstrings();

  // ---- Cancel first: the cancelled picker writes nothing ----
  // Actual SourceImportWindow.OpenLocalFileAsync behaviour: cancelling the
  // picker keeps the wizard open with a zero-write status; we then dismiss
  // it through the real cancel button.
  const beforeCancel = await history();
  await openManagement(page);
  const awaitCancelPicker = await beginPicker(runtime, "cancel");
  summary.entry = await clickBaseFileEntry(page);
  await awaitCancelPicker();
  const cancelled = await requestNative(
    runtime, { action: "await", textKind: "status", contains: "已取消" }, 60_000);
  const cancelledStatus = controlOf(cancelled.observation, "status-text");
  recorder.check("the cancelled .base picker keeps the wizard open with a zero-write status",
    typeof cancelledStatus?.value === "string" && cancelledStatus.value.includes("未导入任何内容"),
    { status: cancelledStatus?.value });
  const cancelledShot = await requestNative(runtime, { action: "observe", shot: true }, 30_000);
  recorder.check("the dismissed wizard can still reselect a file and stays interactive",
    cancelledShot.present
      && controlOf(cancelledShot.observation, "source-file-button")?.enabled === true,
    {});
  await captureWizardEvidence(runtime, "35-base-import-cancel.png");
  await requestNative(runtime, { action: "click", target: "cancel-button" }, 30_000);
  await requestNative(runtime, { action: "gone" }, 30_000);
  const afterCancel = await history();
  const cancelProblems = assertPreSubmissionTerminal(beforeCancel, afterCancel, []);
  recorder.check("cancelling the .base picker leaves the durable journal byte-for-byte untouched",
    cancelProblems.length === 0, { problems: cancelProblems });
  summary.cancel = { journalUntouched: cancelProblems.length === 0 };
  await writeEvidence("35-base-import-cancel.json", summary.cancel);

  // ---- Formal pass: pick the fixed file through the real system picker ----
  await openManagement(page);
  const awaitSelectPicker = await beginPicker(runtime, "select");
  await clickBaseFileEntry(page);
  await awaitSelectPicker();
  const catalog = await requestNative(
    runtime, { action: "await", textKind: "status", contains: EXPECTED_BASE_FILE_IMPORT.sourceName },
    60_000);
  const catalogControls = catalog.observation;
  const tableChecks = EXPECTED_BASE_FILE_IMPORT.tables.map((table) => ({
    table: table.id, checkbox: controlOf(catalogControls, `table-select-${table.id}`),
  }));
  recorder.check("the real wizard catalog lists every fixture table from the frozen snapshot",
    tableChecks.every((item) => item.checkbox != null && item.checkbox.enabled), { tableChecks });

  for (const item of tableChecks) {
    await requestNative(runtime, { action: "click", target: `table-select-${item.table}` }, 30_000);
  }
  // The field-policy sections start collapsed inside WPF Expanders; expand
  // them with the helper's real clicks before touching any combobox.
  await requestNative(runtime, { action: "expand" }, 60_000);
  for (const policy of baseFilePolicyPlan()) {
    await requestNative(
      runtime, { action: "select", target: policy.control, index: policy.index }, 30_000);
  }
  const policyObservation = await requestNative(runtime, { action: "observe" }, 30_000);
  const policyStates = Object.fromEntries(baseFilePolicyPlan().map((policy) => {
    const control = controlOf(policyObservation.observation, policy.control);
    return [policy.control, control?.value ?? null];
  }));
  recorder.check("every lossy field carries its confirmed snapshot/skip strategy in the real window",
    baseFilePolicyPlan().every((policy) => policyStates[policy.control] === policy.label),
    { policyStates });

  const noticeTexts = (policyObservation.observation.controls)
    .filter((item) => /^notice-\d+$/.test(item.id))
    .sort((left, right) => left.id.localeCompare(right.id, "en", { numeric: true }))
    .map((item) => item.value);
  const missingNotices = verifyNotices(expectedNotices, noticeTexts);
  recorder.check("the wizard renders every lossy disclosure before preflight, including unselected content",
    missingNotices.length === 0, { missingNotices, noticeCount: noticeTexts.length });
  summary.success.notices = noticeTexts;

  await requestNative(
    runtime, { action: "click", target: "notice-acknowledge-checkbox", shot: true }, 30_000);
  await captureWizardEvidence(runtime, "35-base-import-window.png");

  await requestNative(runtime, { action: "click", target: "preview-button" }, 30_000);
  const preview = await requestNative(
    runtime, { action: "await", textKind: "report", contains: "预检通过" }, 90_000);
  const reportText = controlOf(preview.observation, "report-text")?.value ?? "";
  recorder.check("the real preflight passes with the confirmed lossy strategies", reportText.includes("预检通过"),
    { report: reportText });
  const startEnabled = controlOf(preview.observation, "start-button");
  if (!startEnabled?.enabled) throw new Error(`start button stayed disabled: ${reportText}`);
  await requestNative(runtime, { action: "click", target: "start-button", shot: true }, 30_000);
  await captureWizardEvidence(runtime, "35-base-import-preview.png");
  await requestNative(runtime, { action: "gone" }, 30_000);

  // ---- Durable terminal receipt on the real Go authority ----
  const session = await page.evaluate(() => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
  let entry = null;
  const until = Date.now() + 180_000;
  while (Date.now() < until) {
    const current = await history();
    entry = (current.migrations ?? []).find((item) =>
      item.containerId === EXPECTED_BASE_FILE_IMPORT.containerId
      && item.sourceName === EXPECTED_BASE_FILE_IMPORT.sourceName);
    if (entry && ["succeeded", "failed", "cancelled"].includes(entry.state)) break;
    entry = null;
    await page.waitForTimeout(500);
  }
  if (entry == null) throw new Error("the .base migration never reached a terminal receipt");
  const receiptProblems = verifyMigrationReceipt(entry, {
    ...EXPECTED_BASE_FILE_IMPORT, sessionEpoch: session.sessionEpoch,
  });
  const recordBatches = (entry.batches ?? []).filter((batch) => batch.stage === "records");
  const createdSum = recordBatches.reduce((sum, batch) => sum + (batch.created ?? 0), 0);
  const crossBatch = {
    bulkRows: BASE_FILE_BULK_ROWS,
    batchBudget: BASE_FILE_BATCH_BUDGET,
    expectedBulkBatches: BASE_FILE_BULK_BATCHES,
    recordBatches: recordBatches.length,
    createdSum,
  };
  recorder.check("the durable receipt is a terminal success with every target and no secrets",
    receiptProblems.length === 0 && entry.state === "succeeded", { problems: receiptProblems, entry });
  recorder.check("the parser-to-Go chain crosses the 400-record batch boundary inside one real file pass",
    recordBatches.length >= BASE_FILE_BULK_BATCHES + 2 && createdSum === 2506, crossBatch);
  const noticeProblems = verifyNotices(expectedNotices, entry.notices ?? []);
  recorder.check("the acknowledged source disclosures persist with the durable receipt",
    noticeProblems.length === 0, { problems: noticeProblems, receiptNotices: entry.notices ?? [] });
  summary.success = {
    ...summary.success, jobId: entry.jobId, state: entry.state,
    total: entry.total, created: entry.created, recordBatches: crossBatch.recordBatches,
  };
  await writeEvidence("35-base-import.json", summary);

  const tableTargets = new Map((entry.targets ?? []).map((target) => [target.sourceTableId, target]));
  const verifyLocalAuthority = async () => {
    const problems = [];
    for (const table of EXPECTED_BASE_FILE_IMPORT.tables) {
      const tableId = tableTargets.get(table.id)?.tableId;
      if (!tableId) throw new Error(`receipt has no committed target for ${table.id}`);
      const described = await request("schema.describe", {
        collection: tableId, requestGeneration: 0,
        accepts: ["vibetable.relation-capabilities.v1", "vibetable.lookup-query.v1"],
      });
      const columns = described.schema?.columns ?? [];
      const resolved = resolveFieldColumns(table, columns);
      for (const title of table.absentFields ?? []) {
        if (columns.some((column) => column.title === title)) {
          problems.push(`${table.id} unexpectedly committed the skipped column ${title}`);
        }
      }
      const result = await request("query.page", { tableId, query });
      if (result.totalRows !== table.records) {
        problems.push(`${table.id} totalRows ${result.totalRows} !== ${table.records}`);
        continue;
      }
      const rows = result.rows;
      if (table.totalRowsOnly) {
        // Spot-check the bulk table without assuming page ordering: verify
        // the page size, the code identity shape, the 1.5-step number series
        // and distinct physical row identities.
        const codeColumn = resolved.get("code").name;
        const amountColumn = resolved.get("amount").name;
        const seenIds = new Set();
        for (const row of rows) {
          seenIds.add(row.id);
          const match = /^QA-BULK-(\d{6})$/.exec(String(row[codeColumn] ?? ""));
          if (!match || row[amountColumn] !== Number(match[1]) * 1.5) {
            problems.push(`${table.id} bulk row ${JSON.stringify(row[codeColumn])} breaks the series`);
            break;
          }
        }
        if (seenIds.size !== rows.length || rows.length === 0) {
          problems.push(`${table.id} returned ${rows.length} rows with ${seenIds.size} distinct ids`);
        }
        continue;
      }
      const codeIndex = buildCodeIndex(
        { ...table, codes: table.codes }, rows, resolved.get(table.identityField).name);
      for (const [code, expected] of Object.entries(EXPECTED_BASE_FILE_IMPORT.rows[table.id] ?? {})) {
        const physical = Object.fromEntries(Object.entries(expected).map(([title, value]) => {
          const fieldKey = Object.keys(table.fields).find((key) => table.fields[key] === title);
          return [resolved.get(fieldKey).name, value];
        }));
        problems.push(...verifyRowValues(codeIndex.get(code), physical));
      }
    }
    return problems;
  };
  const authorityProblems = await verifyLocalAuthority();
  recorder.check("leading zeros, zero/false booleans and raw JSON snapshots keep their exact values",
    authorityProblems.length === 0, { problems: authorityProblems });

  // ---- Management history: new receipt, expandable targets, old rows intact ----
  await openManagement(page);
  await page.getByTestId("import-history-refresh").click();
  const migrationRow = page.locator(
    `[data-testid="source-import-row"][data-job-id="${entry.jobId}"]`);
  await migrationRow.waitFor({ state: "visible", timeout: 60_000 });
  const previousJobIds = (beforeCancel.migrations ?? []).map((item) => item.jobId);
  const lostLegacy = [];
  for (const jobId of previousJobIds) {
    if (!(await page.locator(
      `[data-testid="source-import-row"][data-job-id="${jobId}"]`).isVisible().catch(() => false))) {
      lostLegacy.push(jobId);
    }
  }
  recorder.check("the history renders the new .base receipt and keeps every earlier migration row",
    (await migrationRow.getAttribute("data-state")) === "succeeded" && lostLegacy.length === 0,
    { lostLegacy });
  await migrationRow.scrollIntoViewIfNeeded();
  await page.screenshot({ path: evidence("35-base-import-history.png"), fullPage: true });
  await page.getByTestId(`source-import-detail-${entry.jobId}`).click();
  const targetButtons = migrationRow.locator('[data-testid="source-target-open"]');
  await targetButtons.first().waitFor({ state: "visible", timeout: 60_000 });
  recorder.check("the .base receipt lists every committed target as openable",
    (await targetButtons.count()) === EXPECTED_BASE_FILE_IMPORT.tables.length, {});

  // ---- Close/reopen: receipt, rows and disclosures persist offline ----
  const originalSession = await page.evaluate(
    () => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
  await openWorkspaceCenterFromSwitcher(page);
  const closed = await replicaUiMethod(page, recorder, "workspace.close", () =>
    page.getByTestId("workspace-center").getByRole("button", {
      name: /关闭当前工作区|Close current workspace/,
    }).click());
  recorder.check("the .base acceptance closes the actual workspace",
    closed.result?.state === "closed", { closed });
  await beginWritableWorkspaceBootstrapCapture(page, originalSession.sessionEpoch, "workspace.open");
  await page.getByTestId("workspace-center").getByRole("button", { name: /E2E Product Workspace/ }).click();
  const reopened = await waitForCapturedBridgeMessage(page, 60_000);
  const reopenedSession = await page.evaluate(
    () => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
  const afterReopen = await history();
  const reopenedEntry = (afterReopen.migrations ?? []).find((item) => item.jobId === entry.jobId);
  const reopenProblems = reopenedEntry == null ? ["the .base receipt vanished after reopen"]
    : assertPreSubmissionTerminal(
        { migrations: [entry] }, { migrations: [reopenedEntry] }, []);
  recorder.check("the .base receipt with its disclosures survives the workspace reopen unchanged",
    reopenedEntry != null
      && reopened.payload.session.sessionEpoch > originalSession.sessionEpoch
      && reopenProblems.length === 0
      && isDeepStrictEqual(reopenedEntry?.notices ?? [], entry.notices ?? []),
    { problems: reopenProblems, entryEpoch: entry.sessionEpoch });
  const reopenedAuthorityProblems = await verifyLocalAuthority();
  recorder.check("after reopen every committed table keeps its rows and exact values",
    reopenedAuthorityProblems.length === 0, { problems: reopenedAuthorityProblems });

  await withRendererOffline(page, async () => {
    const offlineHistory = await history();
    const offlineEntry = (offlineHistory.migrations ?? []).find((item) => item.jobId === entry.jobId);
    recorder.check("the persisted source disclosures stay readable with the renderer offline",
      offlineEntry != null && verifyNotices(expectedNotices, offlineEntry?.notices ?? []).length === 0,
        { notices: offlineEntry?.notices ?? [] });
    await openManagement(page);
    await page.getByTestId("import-history-refresh").click();
    const offlineRow = page.locator(
      `[data-testid="source-import-row"][data-job-id="${entry.jobId}"]`);
    await offlineRow.waitFor({ state: "visible", timeout: 60_000 });
    // Landed markup: the expanded receipt detail carries the persisted
    // 来源文件说明（含未选表） under source-notices/source-notice. An old UI
    // without the disclosure fails here — there is no fallback selector.
    await offlineRow.getByTestId(`source-import-detail-${entry.jobId}`).click();
    const noticesBlock = offlineRow.getByTestId(HISTORY_NOTICES_TESTID);
    await noticesBlock.waitFor({ state: "visible", timeout: 60_000 });
    const noticeTexts = await noticesBlock.getByTestId(HISTORY_NOTICE_ITEM_TESTID)
      .all().then((items) => Promise.all(items.map((item) => item.textContent())));
    recorder.check("the offline reopened history still shows the 来源文件说明 disclosures",
      verifyNotices(expectedNotices, noticeTexts).length === 0,
      { missing: verifyNotices(expectedNotices, noticeTexts), count: noticeTexts.length });
    await offlineRow.scrollIntoViewIfNeeded();
    await page.screenshot({ path: evidence("35-base-import-reopened.png"), fullPage: true });
  });
}
