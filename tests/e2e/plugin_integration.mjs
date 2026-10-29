import fs from "node:fs/promises";
import path from "node:path";
import { installApplication, openApplication, nativePluginInput } from "./plugin_applications.mjs";
import { nativeModel } from "./plugin_models.mjs";
import { picker } from "./plugin_capability_services.mjs";

const PLUGIN = "com.vibetable.examples.transaction-conformance";

/** Canonical top-level effect triples of a prepared transaction preview. */
export function pendingEffects(preview) {
  return [...(preview?.effects ?? [])]
    .map(effect => `${effect.tableId}:${effect.recordId}:${effect.operation}`)
    .sort();
}

function sameEffectSet(left, right) {
  return JSON.stringify([...left].sort()) === JSON.stringify([...right].sort());
}

/**
 * Both eight-occupancy plans reference the same balance head, so their
 * canonical effect sets share that head's derived backlink update while the
 * line/alloc inserts stay exclusive to each
 * request's own newIds. Requests are distinguishable exactly when both
 * complete sets are non-empty, unequal, and each keeps at least one exclusive
 * effect; two identical complete sets can never be told apart.
 */
export function distinguishableRequests(first, second) {
  const firstEffects = pendingEffects(first);
  const secondEffects = pendingEffects(second);
  if (!firstEffects.length || !secondEffects.length) throw new Error("prepared requests carry no effects");
  if (sameEffectSet(firstEffects, secondEffects)) throw new Error("prepared requests have identical complete effect sets");
  const secondSet = new Set(secondEffects);
  const firstSet = new Set(firstEffects);
  const shared = firstEffects.filter(effect => secondSet.has(effect));
  const firstOnly = firstEffects.filter(effect => !secondSet.has(effect));
  const secondOnly = secondEffects.filter(effect => !firstSet.has(effect));
  if (!firstOnly.length || !secondOnly.length) throw new Error("a prepared request has no exclusive effect over its own newIds");
  return { firstEffects, secondEffects, shared, firstOnly, secondOnly };
}

/**
 * Binds every observed native confirmation window to exactly one prepared
 * request through complete canonical effect-set equality. Shared derived
 * effects (the one head backlink update both requests write) are allowed; the
 * requests stay separated by their exclusive newIds effects, and every window
 * must match exactly one of the two complete sets.
 */
export function matchPendingWindowsToRequests(windows, first, second) {
  const distinction = distinguishableRequests(first, second);
  const mapping = { first: null, second: null };
  for (const window of windows) {
    const effects = [...(window.effects ?? [])];
    if (!effects.length) throw new Error("a pending window exposes no canonical effects");
    if (sameEffectSet(effects, distinction.firstEffects)) {
      if (mapping.first !== null) throw new Error("two windows bind the first request");
      mapping.first = window.hwnd;
    } else if (sameEffectSet(effects, distinction.secondEffects)) {
      if (mapping.second !== null) throw new Error("two windows bind the second request");
      mapping.second = window.hwnd;
    } else {
      throw new Error("a pending window does not exactly bind either prepared request");
    }
  }
  if (mapping.first === null || mapping.second === null) throw new Error("a prepared request has no bound window");
  if (mapping.first === mapping.second) throw new Error("one window cannot bind two requests");
  return { ...mapping, shared: distinction.shared, firstOnly: distinction.firstOnly, secondOnly: distinction.secondOnly };
}

/**
 * Exactly one request applied with its own receipt; the loser carries an
 * explicit definite failure code and no applied payload. The real losing code
 * is recorded verbatim - a plan_stale outcome is never rewritten into a
 * balance_violation claim.
 */
export function assertConcurrentOutcome(outcome, first, second) {
  const entries = [
    { key: "first", preview: first, value: outcome?.first },
    { key: "second", preview: second, value: outcome?.second },
  ];
  const applied = entries.filter(entry => entry.value?.status === "applied");
  if (applied.length !== 1) throw new Error(`expected exactly one applied request, saw ${applied.length}`);
  const loser = entries.find(entry => entry.value?.status !== "applied");
  if (!loser.value || typeof loser.value.code !== "string" || !/^transaction\.(plan_stale|balance_violation)$/.test(loser.value.code)) {
    throw new Error(`loser is not an explicit not-committed/stale result: ${JSON.stringify(loser.value)}`);
  }
  const winner = applied[0];
  if (winner.value.requestId !== winner.preview.requestId
    || JSON.stringify(winner.value.newIds) !== JSON.stringify(winner.preview.newIds)
    || winner.value.receipt?.status !== "applied" || !winner.value.receipt.changeSetId
    || winner.value.receipt.rows.length !== winner.preview.effects.length) {
    throw new Error("winner does not prove the exact prepared request boundary");
  }
  for (const effect of winner.preview.effects) {
    if (!winner.value.receipt.rows.some(row =>
      row.tableId === effect.tableId && row.recordId === effect.recordId && row.operation === effect.operation)) {
      throw new Error("winner receipt lacks a planned canonical effect");
    }
  }
  return { winner: winner.key, losingCode: loser.value.code, requestId: winner.value.requestId };
}

/** One real 1/1/1 authority: occupancy 8, remaining 2, no orphan or duplicate rows. */
export function assertConcurrentAuthority(authority, model, seed, outcome) {
  const bindings = model.bindings;
  const head = authority.heads[0];
  const line = authority.lines[0];
  const allocation = authority.allocations[0];
  if (authority.heads.length !== 1 || authority.lines.length !== 1 || authority.allocations.length !== 1) {
    throw new Error(`authority rows are not exactly 1/1/1: ${authority.heads.length}/${authority.lines.length}/${authority.allocations.length}`);
  }
  const winner = outcome.winner === "first" ? outcome.requests.first : outcome.requests.second;
  const loser = outcome.winner === "first" ? outcome.requests.second : outcome.requests.first;
  if (head.id !== seed.headId || head.code !== "SYNTHETIC-CONCURRENCY-01" || head.budget !== "10.00") throw new Error("authoritative balance head changed");
  if (line.id !== winner.newIds.lines.l || line.amount !== "8.00" || line.head !== head.id) throw new Error("the winning line is not the exact planned row");
  if (allocation.id !== winner.newIds.allocations.a || allocation.amount !== "8.00" || allocation.line !== line.id) throw new Error("the winning allocation is not the exact planned row");
  if (JSON.stringify([...head.lines].sort()) !== JSON.stringify([line.id])) throw new Error("head backlink has an orphan or duplicate line");
  if (JSON.stringify([...line.allocations].sort()) !== JSON.stringify([allocation.id])) throw new Error("line backlink has an orphan or duplicate allocation");
  const loserIds = [...Object.values(loser.newIds.lines ?? {}), ...Object.values(loser.newIds.allocations ?? {})];
  const authorityIds = [head.id, line.id, allocation.id];
  if (loserIds.some(id => authorityIds.includes(id))) throw new Error("the losing request left a row behind");
  const occupancyCents = Number(line.amount.replace(".", ""));
  const budgetCents = Number(head.budget.replace(".", ""));
  if (occupancyCents !== 800 || budgetCents - occupancyCents !== 200) throw new Error("occupancy is not 8 with 2 remaining");
  return { occupancy: line.amount, remaining: ((budgetCents - occupancyCents) / 100).toFixed(2), tableIds: Object.fromEntries(Object.entries(bindings).map(([entity, binding]) => [entity, binding.tableId])) };
}

/** The T01 artifact must carry the full dataset versions and the real rows. */
export function assertT01Generation(generated, opened, model, authority) {
  if (generated.outputId === opened.datasetId) throw new Error("output id must not alias the dataset id");
  if (generated.datasetId !== opened.datasetId) throw new Error("T01 source does not bind the displayed dataset");
  if (JSON.stringify(generated.versions) !== JSON.stringify(opened.versions)) throw new Error("T01 versions differ from the dataset versions");
  for (const entity of ["heads", "lines", "allocations"]) {
    const version = generated.versions[model.bindings[entity].tableId];
    if (!version || typeof version.schemaRevision !== "string" || !Number.isFinite(version.dataRevision)) {
      throw new Error(`T01 versions lack the full ${entity} version`);
    }
  }
  const byEntity = Object.fromEntries(generated.rows.map(row => [row.entity, row]));
  if (generated.rows.length !== 3 || !byEntity.heads || !byEntity.lines || !byEntity.allocations) throw new Error("T01 rows are not the three canonical entities");
  if (!/^-?\d+\.\d{2}$/.test(byEntity.heads.amount) || !/^-?\d+\.\d{2}$/.test(byEntity.lines.amount) || !/^-?\d+\.\d{2}$/.test(byEntity.allocations.amount)) throw new Error("T01 amounts are not exact fixed-scale decimals");
  // The public-query authority is the independent oracle: the artifact must
  // carry exactly the observed winning rows, not any three self-consistent ids.
  const authorityHead = authority.heads[0];
  const authorityLine = authority.lines[0];
  const authorityAllocation = authority.allocations[0];
  if (byEntity.heads.recordId !== authorityHead.id
    || byEntity.lines.recordId !== authorityLine.id
    || byEntity.allocations.recordId !== authorityAllocation.id) throw new Error("T01 row ids differ from the public authority rows");
  if (byEntity.heads.amount !== authorityHead.budget
    || byEntity.lines.amount !== authorityLine.amount
    || byEntity.allocations.amount !== authorityAllocation.amount) throw new Error("T01 amounts differ from the public authority rows");
  if (byEntity.lines.relation !== authorityLine.head || byEntity.lines.relation !== authorityHead.id
    || byEntity.allocations.relation !== authorityAllocation.line || byEntity.allocations.relation !== authorityLine.id) {
    throw new Error("T01 relations differ from the public authority rows");
  }
  if (byEntity.heads.amount !== "10.00" || byEntity.lines.amount !== "8.00" || byEntity.allocations.amount !== "8.00") throw new Error("T01 amounts are not the expected authoritative occupancy");
  if (generated.source.templateVersion !== "T01-synthetic-template-v1" || generated.source.dataVersion !== opened.datasetId) throw new Error("T01 source does not bind template and data versions");
  return byEntity;
}

async function nativeRequest(runtime, payload, timeoutMs) {
  const requestId = `native-integration-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  const target = path.join(runtime.evidenceDir, "plugin-native-request.json");
  await fs.writeFile(`${target}.tmp`, JSON.stringify({ requestId, ...payload }));
  await fs.rename(`${target}.tmp`, target);
  const until = Date.now() + timeoutMs;
  while (Date.now() < until) {
    const result = await fs.readFile(path.join(runtime.evidenceDir, "plugin-native-result.json"), "utf8")
      .then(JSON.parse).catch(error => { if (error.code === "ENOENT") return null; throw error; });
    if (result?.requestId === requestId) {
      if (result.status !== "completed") throw new Error(`native integration input failed: ${JSON.stringify(result)}`);
      return result;
    }
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  throw new Error("native integration input deadline");
}

export function observePendingConfirmations(runtime, expectCount) {
  return nativeRequest(runtime, { action: "confirm-pending", buttonId: "plugin-confirm-approve", stage: "pending", expectCount }, 25000);
}

export function approveExactConfirmation(runtime, hwnd, expectEffects, stage) {
  return nativeRequest(runtime, { action: "confirm-exact", buttonId: "plugin-confirm-approve", stage, hwnd, expectEffects }, 20000);
}

/**
 * One public query -> canonical logical-field projection for the three model
 * tables. The same projection is used before and after the output chain so a
 * before/after comparison can never mix physical columns with logical ones.
 */
export function canonicalAuthorityReader(rawBridgeRequest, page, model) {
  return async () => Object.fromEntries(await Promise.all(Object.entries(model.bindings).map(async ([entity, binding]) => {
    const schema = await rawBridgeRequest(page, "schema.getTable", { tableId: binding.tableId });
    const names = Object.fromEntries(schema.payload.fields.map(field => [field.identity.fieldId, field.identity.physicalName]));
    const reply = await rawBridgeRequest(page, "query.page", { tableId: binding.tableId, query: { filters: [], sorts: [], offset: 0, limit: 20 } });
    if (!Array.isArray(reply.payload?.rows)) throw new Error(`integration authority query failed: ${JSON.stringify(reply)}`);
    return [entity, reply.payload.rows.map(row => ({ id: row.id, ...Object.fromEntries(Object.entries(binding.fields).map(([key, field]) => [key, row[names[field]]])) }))];
  })));
}

/** Runs against the real WPF/WebView2 Host with synthetic, newly constructed data. */
export async function runPluginIntegration(page, recorder, runtime, helpers) {
  const { waitForShell, createSimpleTable, createV2Field, applyProductMutation, rawBridgeRequest } = helpers;
  await waitForShell(page, recorder, { requireDatabaseOpened: true });
  await page.getByTestId("nav-tables").click();
  const table = await createSimpleTable(page, "Synthetic Integration Output", "Unchanged integration value");
  const textField = table.field.physicalName;
  const fileField = await createV2Field(page, table.tableId, "Issued proof", "file", draft => ({ ...draft,
    file: { ...draft.file, maxFiles: 4, maxBytesPerFile: 33554432, allowedMimeTypes: [], thumbs: [] } }));
  await applyProductMutation(page, table.tableId, [{ kind: "insert", recordId: null, values: { [textField]: "SYNTHETIC-INTEGRATION-UNCHANGED" } }], "integration-output-seed");
  const attachmentRow = async () => {
    const response = await rawBridgeRequest(page, "query.page", { tableId: table.tableId, query: { filters: [], sorts: [], offset: 0, limit: 10 } });
    return response.payload.rows.map(row => ({ id: row.id, text: row[textField], files: row[fileField.physicalName] ?? null }));
  };
  await installApplication(page, runtime, "transaction-conformance", PLUGIN, "open-transactions");
  let frame = await openApplication(page, PLUGIN, "open-transactions");
  async function finish(expected = "succeeded") {
    await frame.locator("#status").filter({ hasText: /^(succeeded|failed)$/ }).waitFor({ timeout: 120000 });
    const value = JSON.parse(await frame.locator("#result").innerText());
    if (await frame.locator("#status").innerText() !== expected) throw new Error(`integration action failed: ${JSON.stringify(value)}`);
    return value;
  }
  async function action(id, expected = "succeeded") { await frame.locator(`#${id}`).click(); return finish(expected); }
  async function confirmOnce(id, stage, expected = "succeeded") {
    await frame.locator(`#${id}`).click();
    await page.locator(".host-confirmation").waitFor({ timeout: 30000 });
    const native = await nativePluginInput(runtime, "click", "plugin-confirm-approve", "approved");
    if (native.status !== "completed") throw new Error(`native confirmation failed: ${JSON.stringify(native)}`);
    await fs.copyFile(path.join(runtime.evidenceDir, "48-native-approved.png"), path.join(runtime.evidenceDir, `58-${stage}.png`));
    return finish(expected);
  }
  await frame.locator("#model").click();
  await nativeModel(runtime, "click", "plugin-model-confirm", "create");
  const model = await finish();
  recorder.check("integration model has exactly three canonical entities",
    model.status === "completed" && Object.keys(model.bindings).length === 3, { model });
  const configured = await confirmOnce("configure", "rules");
  recorder.check("native rule registration is acknowledged", configured?.status === "configured", { configured });

  // AC1: one authoritative balance of 10, two independent eight-occupancy requests.
  const seedPreview = await action("concurrent-seed");
  recorder.check("balance-10 head plan is prepared without any business write",
    seedPreview.status === "prepared" && Object.keys(seedPreview.newIds).length === 1
    && Object.keys(seedPreview.newIds.heads).length === 1, { seedPreview });
  const seedApplied = await confirmOnce("submit", "seed-commit");
  recorder.check("native confirmation commits exactly the one balance-10 head",
    seedApplied.status === "applied" && seedApplied.requestId === seedPreview.requestId
    && seedApplied.receipt.rows.length === 1
    && seedApplied.receipt.rows[0].tableId === model.bindings.heads.tableId
    && seedApplied.receipt.rows[0].recordId === seedApplied.newIds.heads.h, { seedApplied });
  const seed = { headId: seedApplied.newIds.heads.h, requestId: seedApplied.requestId };
  const prepared = await action("concurrent-prepare");
  let distinction;
  try {
    distinction = distinguishableRequests(prepared.first, prepared.second);
    recorder.check("two independent eight-occupancy requests are both prepared before any commit",
      prepared.first.status === "prepared" && prepared.second.status === "prepared"
      && prepared.first.requestId !== prepared.second.requestId
      && prepared.first.requestId !== seed.requestId && prepared.second.requestId !== seed.requestId, { prepared, distinction });
    recorder.check("the two plans share only derived effects and stay separated by their own newIds",
      distinction.shared.length > 0 && distinction.firstOnly.length > 0 && distinction.secondOnly.length > 0, distinction);
  } catch (error) {
    recorder.check("two independent eight-occupancy requests are both prepared before any commit", false, { failure: String(error), prepared });
    throw error;
  }

  const submitting = frame.locator("#concurrent-submit").click();
  await page.waitForFunction(() => document.querySelectorAll(".host-confirmation").length === 2, null, { timeout: 30000 });
  const pending = await observePendingConfirmations(runtime, 2);
  for (let index = 0; index < 2; index++) {
    await fs.copyFile(path.join(runtime.evidenceDir, `48-native-pending-${index}.png`), path.join(runtime.evidenceDir, `58-native-pending-${index}.png`));
  }
  const ownerRoots = new Set(pending.windows.map(window => window.win32OwnerRoot));
  recorder.check("two real native Host confirmation windows are simultaneously pending",
    pending.windows.length === 2 && new Set(pending.windows.map(window => window.hwnd)).size === 2
    && pending.windows.every(window => window.pid === pending.windows[0].pid)
    && ownerRoots.size === 1 && pending.windows.every(window => window.approveButton.enabled), { pending });
  let mapping;
  try {
    mapping = matchPendingWindowsToRequests(pending.windows, prepared.first, prepared.second);
    recorder.check("each pending window uniquely binds one prepared request's complete canonical effects",
      mapping.first !== mapping.second && mapping.shared.length > 0, { mapping });
  } catch (error) {
    recorder.check("each pending window uniquely binds one prepared request's complete canonical effects", false,
      { failure: String(error), firstEffects: pendingEffects(prepared.first), secondEffects: pendingEffects(prepared.second), pending });
    throw error;
  }

  const firstApproval = await approveExactConfirmation(runtime, mapping.first, pendingEffects(prepared.first), "concurrent");
  await fs.copyFile(path.join(runtime.evidenceDir, "48-native-concurrent.png"), path.join(runtime.evidenceDir, "58-native-concurrent.png"));
  recorder.check("first native approval clicks the exact bound Host window",
    firstApproval.hwnd === mapping.first && firstApproval.input === "SendInput"
    && JSON.stringify([...firstApproval.boundEffects].sort()) === JSON.stringify(pendingEffects(prepared.first)), { firstApproval });
  const secondApproval = await approveExactConfirmation(runtime, mapping.second, pendingEffects(prepared.second), "concurrent-second");
  await fs.copyFile(path.join(runtime.evidenceDir, "48-native-concurrent-second.png"), path.join(runtime.evidenceDir, "58-native-concurrent-second.png"));
  recorder.check("second native approval clicks the other exact bound Host window",
    secondApproval.hwnd === mapping.second && secondApproval.input === "SendInput"
    && JSON.stringify([...secondApproval.boundEffects].sort()) === JSON.stringify(pendingEffects(prepared.second)), { secondApproval });
  await submitting;
  const outcomeRaw = await finish();
  let outcome;
  try {
    outcome = { ...assertConcurrentOutcome(outcomeRaw, prepared.first, prepared.second), requests: outcomeRaw.requests };
    recorder.check("exactly one concurrent request applied and the loser is explicitly not committed",
      true, { winner: outcome.winner, losingCode: outcome.losingCode, first: outcomeRaw.first, second: outcomeRaw.second });
  } catch (error) {
    recorder.check("exactly one concurrent request applied and the loser is explicitly not committed", false, { failure: String(error), outcomeRaw });
    throw error;
  }

  const readAuthority = canonicalAuthorityReader(rawBridgeRequest, page, model);
  const authority = await readAuthority();
  let authorityProof;
  try {
    authorityProof = assertConcurrentAuthority(authority, model, seed, { ...outcome, requests: outcomeRaw.requests });
    recorder.check("public authority shows occupancy 8, remaining 2 and no orphan or duplicate rows", true, authorityProof);
  } catch (error) {
    recorder.check("public authority shows occupancy 8, remaining 2 and no orphan or duplicate rows", false, { failure: String(error), authority });
    throw error;
  }

  // AC2: same desktop path -> dataset -> T01 artifact -> native save/archive/reopen/reprint.
  const opened = await action("dataset");
  recorder.check("the three-table dataset opens on the same authoritative rows",
    opened.sources.length === 3 && opened.sources.every(source => source.filteredRows === 1), { opened });
  const generated = await action("t01-generate");
  let t01Rows;
  try {
    t01Rows = assertT01Generation(generated, opened, model, authority);
    recorder.check("T01 artifact is generated from the real dataset rows and full versions", true, { generated, t01Rows });
  } catch (error) {
    recorder.check("T01 artifact is generated from the real dataset rows and full versions", false, { failure: String(error), generated, authority });
    throw error;
  }
  await fs.writeFile(path.join(runtime.controlsDir, "attachment-target.txt"), "native-dialog\n");
  const cancelling = frame.locator("#t01-save").click();
  const cancellation = await picker(runtime, "t01", "cancel");
  await cancelling;
  const cancelled = await finish();
  recorder.check("real native T01 save cancellation is definite and writes no file",
    cancellation.status === "completed" && cancelled.state === "cancelled" && cancelled.committed === false
    && await fs.stat(path.join(runtime.controlsDir, "58-t01.xlsx")).then(() => false, error => { if (error.code === "ENOENT") return true; throw error; }), { cancellation, cancelled });
  const saving = frame.locator("#t01-save").click();
  const picked = await picker(runtime, "t01", "select");
  await saving;
  const saved = await finish();
  recorder.check("native T01 save commits the generated bytes once",
    picked.status === "completed" && saved.state === "succeeded" && saved.committed === true && saved.bytes === generated.bytes, { saved });
  await frame.locator("#t01-archive").click();
  await page.locator(".host-confirmation").waitFor({ timeout: 30000 });
  const archiveApproval = await nativePluginInput(runtime, "click", "plugin-confirm-approve", "approved");
  await fs.copyFile(path.join(runtime.evidenceDir, "48-native-approved.png"), path.join(runtime.evidenceDir, "58-archive-confirmation.png"));
  const archived = await finish();
  recorder.check("T01 archive commits through the ordinary file field with Host confirmation",
    archiveApproval.status === "completed" && archived.saved.status === "succeeded"
    && archived.artifactId === generated.outputId, { archived });

  await page.getByTestId("plugin-app-return").click();
  frame = await openApplication(page, PLUGIN, "open-transactions");
  await frame.locator("#artifact").fill(archived.artifactId);
  const reopened = await action("t01-reopen");
  recorder.check("close/reopen recovers the immutable archived artifact source",
    reopened.artifactId === archived.artifactId
    && reopened.source.templateVersion === "T01-synthetic-template-v1"
    && reopened.source.dataVersion === opened.datasetId, { reopened });
  const reprinting = frame.locator("#t01-save").click();
  await picker(runtime, "t01-reprint", "select");
  await reprinting;
  const reprinted = await finish();
  recorder.check("reprint of the reopened artifact succeeds with identical declared bytes",
    reprinted.state === "succeeded" && reprinted.bytes === generated.bytes, { reprinted });

  // Changing current business data must not alter the issued artifact on reprint.
  const header = model.bindings.heads;
  const schema = await rawBridgeRequest(page, "schema.getTable", { tableId: header.tableId });
  const code = schema.payload.fields.find(field => field.identity.fieldId === header.fields.code).identity.physicalName;
  await applyProductMutation(page, header.tableId, [{ kind: "update", recordId: seed.headId, values: { [code]: "SYNTHETIC-INTEGRATION-CHANGED" } }], "integration-concurrent-update");
  const reopenedAfterChange = await action("t01-reopen");
  const reprintingAgain = frame.locator("#t01-save").click();
  await picker(runtime, "t01-changed", "select");
  await reprintingAgain;
  const reprintedAfterChange = await finish();
  recorder.check("post-change reprint keeps the issued artifact's original version",
    reopenedAfterChange.source.dataVersion === opened.datasetId && reprintedAfterChange.state === "succeeded"
    && reprintedAfterChange.bytes === generated.bytes, { reopenedAfterChange, reprintedAfterChange });

  // The same canonical projection proves only the intended head code changed.
  const rowsAfter = await readAuthority();
  const businessUnchangedExceptCode = JSON.stringify(rowsAfter.heads.map(row => row.id === seed.headId ? { ...row, code: authority.heads[0].code } : row)) === JSON.stringify(authority.heads)
    && JSON.stringify(rowsAfter.lines) === JSON.stringify(authority.lines)
    && JSON.stringify(rowsAfter.allocations) === JSON.stringify(authority.allocations);
  recorder.check("current data change is visible and the output chain changed nothing else",
    rowsAfter.heads[0].code === "SYNTHETIC-INTEGRATION-CHANGED" && businessUnchangedExceptCode, { rowsAfter });
  const finalAttachmentRow = await attachmentRow();
  recorder.check("the archive lands in the ordinary file field without touching business text",
    finalAttachmentRow.length === 1 && finalAttachmentRow[0].text === "SYNTHETIC-INTEGRATION-UNCHANGED", { finalAttachmentRow });

  await fs.writeFile(path.join(runtime.controlsDir, "58-t01-proof.json"), JSON.stringify({
    model, seed, prepared, pending, mapping, approvals: { firstApproval, secondApproval },
    outcome: { ...outcome, raw: outcomeRaw }, authority, opened, generated, saved, archived, reopened, reprinted, reopenedAfterChange, reprintedAfterChange,
  }, null, 2));
  await fs.writeFile(path.join(runtime.evidenceDir, "58-integration.json"), JSON.stringify({
    model, seed, prepared, pending, mapping, approvals: { firstApproval, secondApproval }, outcome: { ...outcome, raw: outcomeRaw }, authority, generated, opened,
  }, null, 2));
  await page.screenshot({ path: path.join(runtime.evidenceDir, "58-integration.png"), fullPage: true });
}
