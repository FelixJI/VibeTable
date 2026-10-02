import assert from "node:assert/strict";
import test from "node:test";
import {
  documentOrderOracle, documentRefreshReadyInPage, installDocumentPageCaptureInPage, requireDocumentPages,
  requireAppendedRevision, requireActivatedLeaf,
} from "./file_document_operations_journey.mjs";

function pagesFixture() {
  const fixtures = Array.from({ length: 101 }, (_, index) => ({
    relativePath: `file-${index}.txt`, text: `independent text ${index}\n`,
  }));
  const documents = fixtures.map((item, index) => ({
    documentId: `id-${String(index).padStart(3, "0")}`,
    relativePath: item.relativePath, sizeBytes: Buffer.byteLength(item.text),
    effectiveRevisionId: `revision-${index}`, status: "active",
    effectiveRevisionCreatedAt: `2026-10-02T01:02:03.${String(index).padStart(9, "0")}Z`,
  }));
  const order = documentOrderOracle(documents);
  const entries = order.map(id => ({ ...documents.find(item => item.documentId === id), entryHandle: id }));
  const query = { cursor: null, filters: [], limit: 100,
    sort: [{ field: "effectiveRevisionCreatedAt", direction: "desc" }] };
  const pages = [
    { query, payload: { entries: entries.slice(0, 100), nextCursor: "next", topologyRevision: 10 } },
    { query: { ...query, cursor: "next" }, payload: { entries: entries.slice(100), nextCursor: null,
      topologyRevision: 10 } },
  ];
  return { fixtures, documents, pages };
}

test("raw pages require independent nanosecond ordering and complete identity/content metadata", () => {
  const { pages, documents, fixtures } = pagesFixture();
  assert.equal(requireDocumentPages(pages, documents, fixtures).length, 101);
  // All these instants collapse to the same Date millisecond.
  assert.equal(new Set(documents.map(item => new Date(item.effectiveRevisionCreatedAt).getTime())).size, 1);
  assert.equal(documentOrderOracle(documents)[0], "id-100");
  assert.deepEqual(documentOrderOracle([
    { documentId: "z", effectiveRevisionCreatedAt: "2026-10-02T01:02:03Z" },
    { documentId: "a", effectiveRevisionCreatedAt: "2026-10-02T01:02:03.000000000Z" },
  ]), ["a", "z"]);
});

test("duplicate, omitted, reordered and drifted Host pages cannot be hidden by store deduplication", () => {
  for (const corrupt of [
    value => { value.pages[1].payload.entries[0] = value.pages[0].payload.entries[0]; },
    value => { value.pages[1].payload.entries = []; },
    value => { value.pages[0].payload.entries.reverse(); },
    value => { value.pages[1].query.cursor = "wrong"; },
    value => { value.pages[1].payload.topologyRevision++; },
    value => { value.pages[1].payload.nextCursor = "extra"; },
    value => { value.documents[0].sizeBytes++; },
    value => { value.documents[0].relativePath = "unknown.txt"; },
    value => { value.pages[0].payload.entries[0].effectiveRevisionId = "wrong"; },
  ]) {
    const value = pagesFixture();
    corrupt(value);
    assert.throws(() => requireDocumentPages(value.pages, value.documents, value.fixtures));
  }
});

test("page observation correlates requestId and preserves the original raw duplicate response", t => {
  const listeners = new Set();
  const sent = [];
  const webview = {
    postMessage(message) { sent.push(message); },
    addEventListener(type, listener) { assert.equal(type, "message"); listeners.add(listener); },
    removeEventListener(type, listener) { assert.equal(type, "message"); listeners.delete(listener); },
  };
  const original = webview.postMessage;
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  Object.defineProperty(globalThis, "window", { configurable: true, value: { chrome: { webview } } });
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else delete globalThis.window;
  });
  installDocumentPageCaptureInPage();
  const request = { type: "document.listRequested", requestId: "actual-ui", payload: { query: { cursor: null } } };
  webview.postMessage(request);
  const respond = value => { for (const listener of listeners) listener({ data: JSON.stringify(value) }); };
  respond({ type: "document.listLoaded", requestId: "other", payload: { entries: [] } });
  assert.equal(window.__s41DocumentPages.pages.length, 0);
  const duplicate = { entries: [{ documentId: "same" }, { documentId: "same" }] };
  respond({ type: "document.listLoaded", requestId: "actual-ui", payload: duplicate });
  assert.deepEqual(window.__s41DocumentPages.pages[0].payload, duplicate);
  assert.deepEqual(sent, [request]);
  window.__s41DocumentPages.release();
  assert.equal(webview.postMessage, original);
  assert.equal(listeners.size, 0);
});

function revision(id, parent, ordinal, kind, formal, content = id) {
  return { revisionId: id, documentId: "document", parentRevisionId: parent,
    revisionOrdinal: ordinal, formalVersion: formal, kind, restoredFromRevisionId: null,
    objectId: content, contentHash: `existing-contract-${content}`, size: content.length };
}

function tree(revisions, effective) {
  return { documentId: "document", revisions, effectiveRevisionId: effective };
}

test("autosaves, formal siblings, leaf restore and activation preserve distinct contracts", () => {
  const seed = revision("seed", null, 1, "formal", 1);
  const first = revision("first", "seed", 2, "autosave", null);
  const second = revision("second", "first", 3, "autosave", null);
  requireAppendedRevision(tree([seed], "seed"), tree([seed, first], "first"),
    { id: "first", parent: "seed", kind: "autosave" });
  requireAppendedRevision(tree([seed, first], "first"), tree([seed, first, second], "second"),
    { id: "second", parent: "first", kind: "autosave" });
  const branch = revision("branch", "first", 4, "formal", 2);
  const before = tree([seed, first, second], "second");
  const branched = tree([...before.revisions, branch], "branch");
  requireAppendedRevision(before, branched, { id: "branch", parent: "first", kind: "formal" });
  const restored = { ...revision("restore", "branch", 5, "restore", 3, "second"),
    restoredFromRevisionId: "second" };
  const after = tree([...branched.revisions, restored], "restore");
  requireAppendedRevision(branched, after,
    { id: "restore", parent: "branch", kind: "restore", source: "second" });
  requireActivatedLeaf(after, tree(after.revisions, "second"), "second");
  assert.throws(() => requireActivatedLeaf(after, tree(after.revisions, "first"), "first"));
});

test("wrong restore source/parent/number/content, history rewrite or activation append fail", () => {
  const seed = revision("seed", null, 1, "formal", 1);
  const leaf = revision("leaf", "seed", 2, "autosave", null);
  const branch = revision("branch", "seed", 3, "formal", 2);
  const before = tree([seed, leaf, branch], "branch");
  const restored = { ...revision("restored", "branch", 4, "restore", 3, "leaf"),
    restoredFromRevisionId: "leaf" };
  for (const corrupt of [
    after => { after.revisions[3].parentRevisionId = "leaf"; },
    after => { after.revisions[3].restoredFromRevisionId = "seed"; },
    after => { after.revisions[3].formalVersion = 8; },
    after => { after.revisions[3].revisionOrdinal = 8; },
    after => { after.revisions[3].objectId = "wrong-content"; },
    after => { after.revisions[0].kind = "restore"; },
    after => { after.effectiveRevisionId = "leaf"; },
  ]) {
    const after = structuredClone(tree([...before.revisions, restored], "restored"));
    corrupt(after);
    assert.throws(() => requireAppendedRevision(before, after,
      { id: "restored", parent: "branch", kind: "restore", source: "leaf" }));
  }
  assert.throws(() => requireActivatedLeaf(before, tree([...before.revisions, restored], "leaf"), "leaf"));
});

test("refresh requires a new successful requestId, settled list and fresh path-bound handle", t => {
  const saved = ["window", "document", "HTMLElement"].map(name => [name,
    Object.getOwnPropertyDescriptor(globalThis, name)]);
  t.after(() => {
    for (const [name, descriptor] of saved) {
      if (descriptor) Object.defineProperty(globalThis, name, descriptor);
      else delete globalThis[name];
    }
  });
  let handle = "new";
  let rowPath = "file.txt · Workspace";
  class Element {
    getAttribute() { return handle; }
    querySelector() { return { textContent: rowPath }; }
  }
  const roundTrip = { requestId: "old", requestType: "document.listRequested",
    responseType: "document.listLoaded", code: null, startedAt: "same-millisecond" };
  const diagnostics = { roundTrips: [roundTrip], pending: {} };
  Object.defineProperty(globalThis, "window", { configurable: true,
    value: { __vibetableE2EBridgeDiagnostics: diagnostics } });
  Object.defineProperty(globalThis, "document", { configurable: true,
    value: { querySelectorAll: () => [new Element()] } });
  Object.defineProperty(globalThis, "HTMLElement", { configurable: true, value: Element });
  const ready = () => documentRefreshReadyInPage({ priorRequestIds: ["old"],
    relativePath: "file.txt", previousHandle: "old-handle" });
  assert.equal(ready(), false);
  diagnostics.roundTrips.push({ ...roundTrip, requestId: "new" });
  assert.equal(ready(), true, "a new terminal qualifies even with identical wall-clock timestamps");
  diagnostics.pending.list = { requestType: "document.listRequested" };
  assert.equal(ready(), false);
  delete diagnostics.pending.list;
  handle = "old-handle";
  assert.equal(ready(), false);
  handle = "new";
  rowPath = "other-file.txt · Workspace";
  assert.equal(ready(), false);
  rowPath = "file.txt · Workspace";
  diagnostics.roundTrips[1].code = "DOCUMENT_LIST_FAILED";
  assert.equal(ready(), false);
});
