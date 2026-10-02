import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
import { readBridgeDiagnosticsInPage } from "./bridge_diagnostics_instrumentation.mjs";
import { historyUi } from "./file_document_operations_journey.mjs";

const name = "restore-crash-44.txt";
const originalText = "S44 historical formal revision, restored after real sidecar death\n";
const changedText = "S44 newer formal revision before Restore\n";

export function requireRestoreCrashCheckpoint(diagnostics, request) {
  assert.ok(diagnostics);
  assert.equal(diagnostics.failures.length, 0);
  assert.equal(diagnostics.pending.length, 1);
  assert.equal(diagnostics.pending[0].requestId, request.requestId);
  assert.equal(diagnostics.pending[0].requestType, "fileHistory.restore");
  assert.ok(!diagnostics.roundTrips.some(item => item.requestId === request.requestId));
}

// Pause one real product message without changing its payload, wire identity,
// or expected-effective revision. Only the external Node driver writes the arm.
export function pauseRestoreInPage({ documentId, historicalRevisionId, expectedEffectiveRevisionId }) {
  const webview = window.chrome.webview;
  const original = webview.postMessage;
  const capture = { message: null, raw: null, terminal: null, released: false };
  const parse = value => typeof value === "string" ? JSON.parse(value) : value;
  const wrapper = function (raw) {
    const message = parse(raw);
    if (message?.type === "workspace.v2.request" && message.payload?.method === "fileHistory.restore") {
      assertMatching(message.payload.params);
      if (capture.message !== null) throw new Error("more than one UI Restore request reached the seam");
      capture.raw = raw;
      capture.message = structuredClone(message);
      return;
    }
    return original.call(this, raw);
  };
  function assertMatching(params) {
    if (params?.documentId !== documentId || params.historicalRevisionId !== historicalRevisionId
      || params.expectedEffectiveRevisionId !== expectedEffectiveRevisionId) {
      throw new Error("UI Restore selected a different immutable source or effective revision");
    }
  }
  const listener = event => {
    const message = parse(event.data);
    if (capture.message !== null && message?.requestId === capture.message.requestId
      && ["workspace.v2.response", "workspace.v2.reply", "operation.failed"].includes(message.type)) {
      capture.terminal = structuredClone(message);
    }
  };
  capture.release = () => {
    if (capture.released || !capture.message) throw new Error("Restore outbound lease cannot be released");
    capture.released = true;
    webview.postMessage = original;
    return original.call(webview, capture.raw);
  };
  capture.dispose = () => {
    if (webview.postMessage === wrapper) webview.postMessage = original;
    webview.removeEventListener("message", listener);
  };
  webview.addEventListener("message", listener);
  webview.postMessage = wrapper;
  window.__s44Restore = capture;
}

export function requireRestorePublication(before, targetId, ready, proof) {
  const target = before.revisions.find(item => item.revisionId === targetId);
  assert.ok(target);
  assert.equal(ready.point, "before-finish-committed-mutation");
  assert.equal(proof.receipt.method, "fileHistory.restore");
  assert.deepEqual(proof.receipt.result, ready.result);
  assert.deepEqual(proof.intent, { ...proof.head, state: "prepared" });
  assert.ok(proof.committedMutationRevision < proof.head.mutationRevision);
  assert.equal(proof.cachedReceipt, null);
  assert.equal(proof.journal.state, "applied");
  for (const [key, value] of Object.entries(proof.head)) {
    assert.equal(proof.journal[key], value);
    assert.equal(ready[key], value);
  }
  assert.equal(proof.journal.workspaceId, ready.workspaceId);
  assert.ok(proof.journal.operations.some(item => item.path === name && item.desired && item.objectId === target.objectId));
  assert.equal(proof.bytes, originalText);
  assert.equal(ready.result.revisionOrdinal, Math.max(...before.revisions.map(item => item.revisionOrdinal)) + 1);
  assert.equal(ready.result.formalVersion, Math.max(...before.revisions.map(item => item.formalVersion ?? 0)) + 1);
  assert.ok(!before.revisions.some(item => item.revisionId === ready.result.revisionId));
}

export function requireRecoveredRestore(before, after, targetId, result, proof, restoreIntent) {
  assert.equal(after.documentId, before.documentId);
  assert.equal(after.revisions.length, before.revisions.length + 1);
  assert.equal(after.effectiveRevisionId, result.revisionId);
  for (const old of before.revisions) assert.deepEqual(after.revisions.find(item => item.revisionId === old.revisionId), old);
  const target = before.revisions.find(item => item.revisionId === targetId);
  const restored = after.revisions.find(item => item.revisionId === result.revisionId);
  assert.ok(target && restored);
  assert.equal(restored.kind, "restore");
  assert.equal(restored.parentRevisionId, before.effectiveRevisionId);
  assert.equal(restored.restoredFromRevisionId, targetId);
  assert.equal(restored.revisionOrdinal, result.revisionOrdinal);
  assert.equal(restored.formalVersion, result.formalVersion);
  for (const key of ["objectId", "contentHash", "size", "mimeType"]) assert.equal(restored[key], target[key]);
  assert.deepEqual(proof.restoreIntent, { ...restoreIntent, state: "committed" });
  assert.ok(proof.committedMutationRevision >= restoreIntent.mutationRevision);
  assert.equal(proof.journal, null);
  assert.equal(proof.receipt.method, "fileHistory.restore");
  assert.deepEqual(proof.receipt.result, result);
  assert.equal(proof.bytes, originalText);
}

async function control(runtime, action, state) {
  const requestId = crypto.randomUUID();
  await fs.writeFile(path.join(runtime.evidenceDir, "fault-request.json"), JSON.stringify({
    requestId, action, workspaceId: state.workspaceId, operationId: state.operationId,
    ...(action === "observe-restore-storage" ? { restoreIntent: state.restoreIntent } : {}),
  }), "utf8");
  const deadline = Date.now() + 30000;
  while (Date.now() < deadline) {
    try {
      const result = JSON.parse(await fs.readFile(path.join(runtime.evidenceDir, "fault-result.json"), "utf8"));
      if (result.requestId === requestId) { assert.equal(result.status, "completed", JSON.stringify(result)); return result; }
    } catch (error) { if (error.code !== "ENOENT" && !(error instanceof SyntaxError)) throw error; }
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  throw new Error(`Restore control ${action} did not complete`);
}

async function tree(page, helpers, documentId) {
  return (await helpers.rawWorkspaceV2Request(page, "fileHistory.readTree", { documentId })).result;
}

// Root integrates these two functions into the existing persistent-phase
// dispatch. This file itself neither registers nor launches an E2E scenario.
export async function seedFileRestoreCrash(page, recorder, runtime, helpers) {
  await helpers.waitForShell(page, recorder, { requireDatabaseOpened: true });
  const session = await page.evaluate(() => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
  const root = path.resolve(runtime.dataRoot, "workspaces", session.workspaceId);
  const manifest = JSON.parse(await fs.readFile(path.join(root, ".vibetable", "workspace.json"), "utf8"));
  assert.equal(manifest.workspaceId, session.workspaceId);
  const picker = path.join(runtime.controlsDir, "document-source.txt");
  const previousPicker = await fs.readFile(picker, "utf8");
  let crashAttempted = false;
  try {
    const source = path.join(runtime.controlsDir, name);
    await fs.writeFile(source, originalText, "utf8");
    await fs.writeFile(picker, `${source}\n`, "utf8");
    await page.getByTestId("nav-files").click();
    await helpers.beginBridgeMessageCapture(page, ["document.workspaceChanged", "document.operationFailed"]);
    await page.getByTestId("document-import").click();
    const imported = await helpers.waitForCapturedBridgeMessage(page, 30000);
    assert.equal(imported.payload?.reason, "import");
    const query = await helpers.rawWorkspaceV2Request(page, "fileHistory.queryDocuments", {
      logic: "and", filters: [{ field: "relativePath", operator: "eq", value: name }],
      sort: [{ field: "relativePath", direction: "asc" }], limit: 100, cursor: null,
    });
    assert.equal(query.result.documents.length, 1);
    const documentId = query.result.documents[0].documentId;
    const initial = await tree(page, helpers, documentId);
    assert.equal(initial.revisions.length, 1);
    const targetId = initial.effectiveRevisionId;
    const upgradeSource = path.join(runtime.controlsDir, "44-formal-upgrade.txt");
    await fs.writeFile(upgradeSource, changedText, "utf8");
    await fs.writeFile(path.join(runtime.controlsDir, "file-upgrade-source.txt"), `${upgradeSource}\n`, "utf8");
    // Seed through the existing picker contract; a sole current leaf has no
    // ancestor-upgrade UI. S43 separately exercises that real branch action.
    const upgraded = await helpers.rawWorkspaceV2Request(page, "fileHistory.upgrade", {
      documentId, revisionId: targetId, pathGrant: "host-picker://file-upgrade",
    });
    assert.ok(upgraded.result.revisionId && upgraded.result.revisionId !== targetId);
    const before = await tree(page, helpers, documentId);
    assert.equal(before.revisions.length, 2);
    assert.equal(await fs.readFile(path.join(root, "files", name), "utf8"), changedText);
    const revisionUi = await historyUi(page, name, before.effectiveRevisionId);
    await page.evaluate(pauseRestoreInPage, { documentId, historicalRevisionId: targetId, expectedEffectiveRevisionId: before.effectiveRevisionId });
    await revisionUi.locator(`[data-revision-id="${targetId}"]`).getByRole("button", { name: /^(恢复为新版本|Restore as new version)$/ }).click();
    await page.waitForFunction(() => window.__s44Restore?.message !== null, null, { timeout: 30000 });
    const request = await page.evaluate(() => window.__s44Restore.message);
    const operationId = request.payload.wire.operationId;
    const arm = { workspaceId: session.workspaceId, operationId, documentId,
      historicalRevisionId: targetId, expectedEffectiveRevisionId: before.effectiveRevisionId,
      relativePath: name, objectId: initial.revisions[0].objectId };
    // Exclusive creation is a one-shot lease. No write comes from the Web view.
    await fs.writeFile(path.join(runtime.controlsDir, "file-restore-barrier.arm.json"), JSON.stringify(arm), { encoding: "utf8", flag: "wx" });
    await page.evaluate(() => window.__s44Restore.release());
    const deadline = Date.now() + 60000;
    let ready;
    while (Date.now() < deadline) {
      try { ready = JSON.parse(await fs.readFile(path.join(runtime.controlsDir, "file-restore-barrier.ready.json"), "utf8")); break; }
      catch (error) { if (error.code !== "ENOENT" && !(error instanceof SyntaxError)) throw error; }
      await new Promise(resolve => setTimeout(resolve, 25));
    }
    assert.ok(ready, "Restore never reached its exact committed-publication point");
    for (const [key, value] of Object.entries(arm)) assert.equal(ready[key], value);
    let diagnostics;
    const checkpointDeadline = Date.now() + 10000;
    do {
      diagnostics = await page.evaluate(readBridgeDiagnosticsInPage);
      if (diagnostics.failures.length || diagnostics.pending.length === 1) break;
      await page.waitForTimeout(50);
    } while (Date.now() < checkpointDeadline);
    requireRestoreCrashCheckpoint(diagnostics, request);
    // The existing driver owns renderer/network arrays and tracing. Its
    // callback runs the normal clean checks, saves a live screenshot and stops
    // the seed trace before this precise, sole outstanding request is killed.
    const checkpoint = await helpers.captureRestoreCrashCheckpoint(page, recorder, request, runtime);
    requireRestoreCrashCheckpoint(checkpoint.bridgeDiagnostics, request);
    assert.equal(checkpoint.rendererDiagnosticsClean, true);
    assert.ok(checkpoint.screenshot && checkpoint.trace);
    const restoreCrash = { before, targetId, request, result: ready.result, operationId,
      ready, checkpoint };
    await fs.writeFile(path.join(runtime.evidenceDir, "44-restore-crash-checkpoint.json"),
      JSON.stringify({ workspaceId: session.workspaceId, restoreCrash }), { encoding: "utf8", flag: "wx" });
    crashAttempted = true;
    const killed = await control(runtime, "kill-restore-sidecar", arm);
    assert.equal(killed.pid, ready.pid);
    requireRestorePublication(before, targetId, ready, killed.proof);
    assert.deepEqual(killed.afterCrashProof, killed.proof);
    recorder.check("actual UI Restore publishes its head/receipt and applied bytes before the exact verified sidecar dies", true, { ready, killed });
    return { workspaceId: session.workspaceId, restoreCrash: { ...restoreCrash,
      publication: killed.proof, killedPid: killed.pid }, intentionalRestoreCrash: killed };
  } finally {
    await fs.writeFile(picker, previousPicker, "utf8");
    // A successful intentional crash leaves no live page to inspect or dispose.
    // A failed setup still restores the outbound lease on the living page.
    if (!crashAttempted && !page.isClosed()) {
      await page.evaluate(() => window.__s44Restore?.dispose()).catch(error => {
        if (!page.isClosed()) throw error;
      });
    }
  }
}

export async function resumeFileRestoreCrash(page, recorder, statePath, runtime, helpers) {
  const seed = JSON.parse(await fs.readFile(statePath, "utf8"));
  const { start, switched } = await helpers.activateRestartedWorkspace(page, seed.workspaceId);
  assert.equal(switched.result?.workspaceId, seed.workspaceId);
  assert.equal(switched.result?.state, "openedWritable");
  assert.ok(Number.isSafeInteger(switched.result.sessionEpoch) && switched.result.sessionEpoch > 0);
  assert.ok(switched.result.sessionEpoch > seed.restoreCrash.request.payload.wire.sessionEpoch);
  recorder.check("second normal Host activates the exact seeded UUID in a newer writable session", true,
    { start, switched: switched.result });
  // A same-UUID switch is idempotent; startup bootstrap may predate diagnostics.
  // Reserve once through the formal renderer session, never poll the allocator.
  const session = await page.evaluate(() =>
    window.__vibetableE2EWorkspaceWirePort.reserve(crypto.randomUUID()));
  assert.equal(session.workspaceId, seed.workspaceId);
  assert.equal(session.sessionEpoch, switched.result.sessionEpoch);
  const state = seed.restoreCrash;
  const binding = { workspaceId: seed.workspaceId, operationId: state.operationId,
    restoreIntent: state.publication.head };
  const restored = await tree(page, helpers, state.before.documentId);
  const first = await control(runtime, "observe-restore-storage", binding);
  requireRecoveredRestore(state.before, restored, state.targetId, state.result, first.proof, binding.restoreIntent);
  assert.deepEqual(first.proof.receipt, state.publication.receipt);
  recorder.check("a second normal Host reopens the same UUID and recovers exactly one Restore without changing old revisions", true, { restored, proof: first.proof });
  const rejection = await page.evaluate(request => new Promise((resolve, reject) => {
    const webview = window.chrome.webview;
    const timer = setTimeout(() => { webview.removeEventListener("message", listener); reject(new Error("old Restore envelope has no rejection terminal")); }, 20000);
    const listener = event => {
      const message = typeof event.data === "string" ? JSON.parse(event.data) : event.data;
      if (message?.requestId !== request.requestId) return;
      clearTimeout(timer); webview.removeEventListener("message", listener); resolve(message);
    };
    webview.addEventListener("message", listener);
    webview.postMessage(request);
  }), state.request);
  const code = rejection.payload?.error?.code ?? rejection.payload?.code;
  assert.ok(["workspace.session_stale", "workspace.session_epoch_stale", "workspace.sequence_stale"].includes(code), JSON.stringify(rejection));
  await helpers.acknowledgeExpectedBridgeFailure(page, rejection);
  assert.deepEqual(await tree(page, helpers, state.before.documentId), restored);
  const second = await control(runtime, "observe-restore-storage", binding);
  assert.deepEqual(second.proof, first.proof);
  recorder.check("the identical old Restore request is rejected and cannot append a second revision or receipt", true, { rejection });
  await page.getByTestId("nav-files").click();
  await historyUi(page, name, restored.effectiveRevisionId);
  await page.screenshot({ path: path.join(runtime.evidenceDir, "44-file-restore-crash.png") });
  return { workspaceId: seed.workspaceId, restoreCrash: state };
}
