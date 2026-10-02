import assert from "node:assert/strict";
import { test } from "node:test";
import { pauseRestoreInPage, requireRestorePublication, requireRecoveredRestore, requireRestoreCrashCheckpoint } from "./file_restore_crash_journey.mjs";

test("only the exact pending Restore has an intentional crash exception", () => {
  const request = { requestId: "restore" };
  const diagnostics = { failures: [], pending: [{ requestId: "restore", requestType: "fileHistory.restore" }], roundTrips: [] };
  requireRestoreCrashCheckpoint(diagnostics, request);
  assert.throws(() => requireRestoreCrashCheckpoint({ ...diagnostics, failures: [{ requestId: "other" }] }, request));
  assert.throws(() => requireRestoreCrashCheckpoint({ ...diagnostics, pending: [...diagnostics.pending, { requestId: "other" }] }, request));
  assert.throws(() => requireRestoreCrashCheckpoint({ ...diagnostics, roundTrips: [{ requestId: "restore" }] }, request));
});

test("the UI seam releases the identical Restore message exactly once", () => {
  const sent = [];
  const listeners = new Set();
  const webview = { postMessage: raw => sent.push(raw), addEventListener: (_, listener) => listeners.add(listener),
    removeEventListener: (_, listener) => listeners.delete(listener) };
  globalThis.window = { chrome: { webview } };
  const params = { documentId: "doc", historicalRevisionId: "old", expectedEffectiveRevisionId: "current" };
  try {
    pauseRestoreInPage(params);
    for (const listener of listeners) listener({ data: { type: "workspace.changed" } });
    assert.equal(window.__s44Restore.terminal, null);
    const raw = JSON.stringify({ type: "workspace.v2.request", requestId: "actual", payload: {
      method: "fileHistory.restore", params, wire: { operationId: "actual-operation", sequence: 8 } } });
    webview.postMessage(raw);
    assert.deepEqual(sent, []);
    assert.equal(window.__s44Restore.message.payload.wire.operationId, "actual-operation");
    window.__s44Restore.release();
    assert.equal(sent[0], raw);
    assert.throws(() => window.__s44Restore.release(), /cannot be released/);
    window.__s44Restore.dispose();
    assert.equal(listeners.size, 0);
    pauseRestoreInPage(params);
    assert.throws(() => webview.postMessage({ type: "workspace.v2.request", payload: {
      method: "fileHistory.restore", params: { ...params, historicalRevisionId: "wrong" } } }), /different immutable source/);
  } finally { window.__s44Restore.dispose(); delete globalThis.window; }
});

function fixture() {
  const old = { revisionId: "old", documentId: "doc", revisionOrdinal: 1, formalVersion: 1,
    objectId: "original-object", contentHash: "existing-contract", size: 4, mimeType: "text/plain" };
  const current = { ...old, revisionId: "current", revisionOrdinal: 2, formalVersion: 2 };
  const before = { documentId: "doc", effectiveRevisionId: "current", revisions: [old, current] };
  const result = { revisionId: "restore", revisionOrdinal: 3, formalVersion: 3 };
  const head = { mutationRevision: 4, sessionEpoch: 7, fenceEpoch: 3, claimId: "claim" };
  const receipt = { method: "fileHistory.restore", result };
  const proof = { head, intent: { ...head, state: "prepared" }, committedMutationRevision: 3, receipt, cachedReceipt: null,
    journal: { ...head, workspaceId: "workspace", state: "applied", operations: [{ path: "restore-crash-44.txt", desired: true, objectId: old.objectId }] },
    bytes: "S44 historical formal revision, restored after real sidecar death\n" };
  const ready = { ...head, workspaceId: "workspace", point: "before-finish-committed-mutation", result };
  const after = { documentId: "doc", effectiveRevisionId: "restore", revisions: [...before.revisions, {
    ...old, ...result, kind: "restore", parentRevisionId: "current", restoredFromRevisionId: "old" }] };
  return { before, after, ready, proof, result };
}

test("the publication oracle rejects wrong lineage, unfinished recovery and repeated Restore", () => {
  const { before, after, ready, proof, result } = fixture();
  requireRestorePublication(before, "old", ready, proof);
  assert.throws(() => requireRestorePublication(before, "old", ready, { ...proof, cachedReceipt: proof.receipt }));
  assert.throws(() => requireRestorePublication(before, "old", ready, { ...proof, journal: { ...proof.journal, state: "prepared" } }));
  const recovered = { ...proof, intent: { ...proof.head, state: "committed" },
    restoreIntent: { ...proof.head, state: "committed" }, committedMutationRevision: 4, journal: null };
  requireRecoveredRestore(before, after, "old", result, recovered, proof.head);
  assert.throws(() => requireRecoveredRestore(before, after, "old", result, proof, proof.head));
  assert.throws(() => requireRecoveredRestore(before, { ...after, revisions: [...after.revisions, after.revisions[2]] }, "old", result, recovered, proof.head));
  assert.throws(() => requireRecoveredRestore(before, { ...after, revisions: [{ ...before.revisions[0], size: 9 }, ...after.revisions.slice(1)] }, "old", result, recovered, proof.head));
  assert.throws(() => requireRecoveredRestore(before, { ...after, revisions: [...before.revisions, { ...after.revisions[2], restoredFromRevisionId: "current" }] }, "old", result, recovered, proof.head));
});

test("recovery binds the seed Restore intent even when startup publishes a later head", () => {
  const { before, after, proof, result } = fixture();
  const laterHead = { mutationRevision: 5, sessionEpoch: 8, fenceEpoch: 4, claimId: "later-claim" };
  const recovered = { ...proof, head: laterHead, intent: { ...laterHead, state: "committed" },
    restoreIntent: { ...proof.head, state: "committed" }, committedMutationRevision: 5, journal: null };
  requireRecoveredRestore(before, after, "old", result, recovered, proof.head);
  assert.throws(() => requireRecoveredRestore(before, after, "old", result, {
    ...recovered, restoreIntent: { ...proof.head, state: "prepared" },
  }, proof.head));
  for (const field of ["mutationRevision", "sessionEpoch", "fenceEpoch", "claimId"]) {
    const wrong = { ...proof.head, [field]: typeof proof.head[field] === "number" ? proof.head[field] + 1 : "wrong-claim" };
    assert.throws(() => requireRecoveredRestore(before, after, "old", result, recovered, wrong));
  }
});
