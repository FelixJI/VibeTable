import test from "node:test";
import assert from "node:assert/strict";
import { fileDocumentNativeFixture, requireNativeDragEvidence, requireNativeDocumentUnchanged }
  from "./file_document_native_operations_journey.mjs";

test("native Copy requires received FileDrop and exact bytes; cancel cannot qualify Copy", () => {
  const fixture = fileDocumentNativeFixture("11111111-1111-4111-8111-111111111111");
  const source = `C:\\qa\\files\\${fixture.name}`;
  const sourceBytes = Buffer.from(fixture.text);
  const arm = { operationId: "copy-1", workspaceId: "workspace-1", mode: "copy" };
  const completion = { ...arm, source, effect: "Copy" };
  const drop = { operationId: arm.operationId, workspaceId: arm.workspaceId,
    source, received: [source], effect: "Copy" };
  const evidence = { arm, completion, drop, source, copied: sourceBytes, sourceBytes };
  requireNativeDragEvidence(evidence);
  assert.throws(() => requireNativeDragEvidence({ ...evidence, copied: Buffer.from("wrong") }));
  assert.throws(() => requireNativeDragEvidence({ ...evidence,
    completion: { ...completion, effect: "None" } }));
  assert.throws(() => requireNativeDragEvidence({ ...evidence,
    drop: { ...drop, received: ["C:\\user\\unrelated.txt"] } }));
  const cancelArm = { ...arm, operationId: "cancel-2", mode: "cancel" };
  const cancel = { ...evidence, arm: cancelArm,
    completion: { ...cancelArm, source, effect: "None" }, drop: null };
  requireNativeDragEvidence(cancel);
  assert.throws(() => requireNativeDragEvidence({ ...cancel, drop }));
  assert.throws(() => requireNativeDragEvidence({ ...cancel,
    completion: { ...cancel.completion, effect: "Copy" } }));
});

test("native actions must preserve literal source bytes and the authoritative document/tree", () => {
  const expectedBytes = Buffer.from("synthetic source\n");
  const before = { workspaceId: "workspace", document: { documentId: "document", relativePath: "file.txt",
    effectiveRevisionId: "revision", status: "active" },
    tree: { documentId: "document", effectiveRevisionId: "revision", revisions: [{ revisionId: "revision" }] },
    importBytes: expectedBytes, managedBytes: expectedBytes };
  requireNativeDocumentUnchanged(before, before, expectedBytes);
  for (const after of [
    { ...before, workspaceId: "other" },
    { ...before, document: { ...before.document, documentId: "other" } },
    { ...before, document: { ...before.document, relativePath: "other.txt" } },
    { ...before, document: { ...before.document, effectiveRevisionId: "newer" } },
    { ...before, tree: { ...before.tree, revisions: [] } },
    { ...before, importBytes: Buffer.from("changed import") },
    { ...before, managedBytes: Buffer.from("changed managed") },
  ]) assert.throws(() => requireNativeDocumentUnchanged(before, after, expectedBytes));
});
