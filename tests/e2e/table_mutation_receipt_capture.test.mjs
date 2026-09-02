import assert from "node:assert/strict";
import test from "node:test";
import { waitForCapturedBridgeMessage } from "./bridge_capture_wait.mjs";
import { installTableMutationReceiptCaptureInPage } from "./table_mutation_receipt_capture.mjs";
import { installWorkspaceV2MethodTerminalCaptureInPage } from "./workspace_v2_method_terminal.mjs";
const WORKSPACE_ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const OPERATION_ID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
function scope(overrides = {}) {
  return { scope: "workspace", workspaceId: WORKSPACE_ID, sessionEpoch: 7,
    operationId: OPERATION_ID, sequence: 3, ...overrides };
}
function revision(overrides = {}) {
  return { databaseSessionId: "pocketbase", schemaRevision: "schema-1",
    dataRevision: 4, ...overrides };
}
function insertRequest(overrides = {}) {
  return { type: "table.insertRowRequested", scope: scope(),
    payload: { table: "records", values: { name: "Ada" }, schemaRevision: "schema-1" },
    ...overrides };
}
function editRequest(overrides = {}) {
  return {
    type: "table.updateCellRequested",
    scope: scope(),
    payload: {
      table: "records",
      rowKey: "row-1",
      column: "name",
      oldValue: "before",
      newValue: "after",
      expectedDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      schemaRevision: "schema-1",
    },
    ...overrides,
  };
}
function insertSuccess(overrides = {}) {
  return { type: "table.rowsInserted", requestId: null,
    payload: { rowKey: "row-1", row: { id: "row-1", name: "Ada" }, revision: revision() },
    ...overrides };
}
function editSuccess(overrides = {}) {
  return {
    type: "table.editCommitted",
    requestId: null,
    payload: {
      rowKey: "row-1",
      column: "name",
      storedValue: "after",
      currentRow: { id: "row-1", name: "after" },
      revision: revision(),
    },
    ...overrides,
  };
}
function rejection(overrides = {}) {
  return {
    type: "table.editRejected",
    requestId: null,
    payload: {
      kind: "edit_conflict",
      operation: "updateCell",
      code: "ROW_DIGEST_MISMATCH",
      message: "private C:\\Users\\secret\\database.db",
      currentRow: { id: "row-1", name: "other" },
      conflictingRowKeys: ["row-1"],
      fieldErrors: null,
      ...overrides,
    },
  };
}
function createHarness({ throwOnPost = false } = {}) {
  const listeners = new Set();
  const posted = [];
  function originalPostMessage(message) {
    posted.push(message);
    if (throwOnPost) throw new Error("native post failed at C:\\private");
    return posted.length;
  }
  const webview = {
    addEventListener(type, listener) {
      if (type === "message") listeners.add(listener);
    },
    removeEventListener(type, listener) {
      if (type === "message") listeners.delete(listener);
    },
    postMessage: originalPostMessage,
  };
  return {
    dispatch(message) {
      for (const listener of [...listeners]) listener({ data: message });
    },
    listeners,
    originalPostMessage,
    posted,
    webview,
  };
}
function install(requestType, harness = createHarness()) {
  globalThis.window = { chrome: { webview: harness.webview } };
  const id = installTableMutationReceiptCaptureInPage({ requestType });
  return { harness, id };
}
function capture() { return window.__vibetableE2EBridgeCapture; }
test.afterEach(() => { delete globalThis.window; });
test("captures one insert owner and returns a wait-compatible receipt with sanitized metadata", async () => {
  const { harness, id } = install("table.insertRowRequested");
  harness.dispatch(insertSuccess());
  harness.webview.postMessage(editRequest());
  assert.equal(capture().message, null);
  harness.webview.postMessage(insertRequest());
  harness.dispatch(editSuccess());
  harness.dispatch(insertSuccess());
  const page = {
    async waitForFunction(predicate, expectedId) {
      assert.equal(predicate(expectedId), true);
    },
    async evaluate(fn, expectedId) {
      return fn(expectedId);
    },
  };
  const result = await waitForCapturedBridgeMessage(page, 100, id);
  assert.equal(result.type, "table.rowsInserted");
  assert.deepEqual(result.owner, {
    requestType: "table.insertRowRequested",
    table: "records",
    schemaRevision: "schema-1",
    valueKeys: ["name"],
    workspaceId: WORKSPACE_ID,
    sessionEpoch: 7,
    operationId: OPERATION_ID,
    sequence: 3,
  });
  assert.equal(JSON.stringify(result.owner).includes("Ada"), false);
  assert.equal(harness.listeners.size, 0);
  assert.equal(harness.webview.postMessage, harness.originalPostMessage);
});
test("captures an edit only when every receipt identity field matches its owner", () => {
  const { harness } = install("table.updateCellRequested");
  harness.webview.postMessage(editRequest());
  harness.dispatch(editSuccess());
  assert.equal(capture().message?.payload.storedValue, "after");
  assert.deepEqual(capture().message?.owner, {
    requestType: "table.updateCellRequested",
    table: "records",
    rowKey: "row-1",
    column: "name",
    schemaRevision: "schema-1",
    workspaceId: WORKSPACE_ID,
    sessionEpoch: 7,
    operationId: OPERATION_ID,
    sequence: 3,
  });
  assert.equal(JSON.stringify(capture().message.owner).includes("after"), false);
});
test("rejects invalid workspace scopes, requestIds, payloads, and repeated owners", () => {
  const invalid = [
    insertRequest({ extra: true }),
    insertRequest({ scope: scope({ extra: true }) }),
    insertRequest({ scope: scope({ workspaceId: WORKSPACE_ID.toUpperCase() }) }),
    insertRequest({ scope: scope({ operationId: "not-a-uuid" }) }),
    insertRequest({ scope: scope({ sessionEpoch: 0 }) }),
    insertRequest({ scope: scope({ sequence: -1 }) }),
    insertRequest({ requestId: "not-a-notify" }),
    insertRequest({ payload: { ...insertRequest().payload, extra: true } }),
  ];
  for (const request of invalid) {
    const { harness } = install("table.insertRowRequested");
    harness.webview.postMessage(request);
    assert.equal(capture().error?.code, "CAPTURE_OUTBOUND_IDENTITY_MISMATCH");
    assert.equal(harness.webview.postMessage, harness.originalPostMessage);
  }
  const editHarness = install("table.updateCellRequested").harness;
  editHarness.webview.postMessage(editRequest({ payload: {
    ...editRequest().payload, expectedDigest: "digest",
  } }));
  assert.equal(capture().error?.code, "CAPTURE_OUTBOUND_IDENTITY_MISMATCH");
  const unsafe = install("table.insertRowRequested").harness;
  assert.equal(unsafe.webview.postMessage(insertRequest({ payload: {
    ...insertRequest().payload, values: { name: undefined },
  } })), 1);
  assert.deepEqual(
    [capture().error?.code, unsafe.listeners.size, unsafe.webview.postMessage],
    [
      "CAPTURE_OUTBOUND_IDENTITY_MISMATCH",
      0,
      unsafe.originalPostMessage,
    ],
  );
  for (const duplicate of [insertRequest(), insertRequest({ payload: {
    ...insertRequest().payload,
    values: { name: "Grace" },
  } })]) {
    const { harness } = install("table.insertRowRequested");
    harness.webview.postMessage(insertRequest());
    harness.webview.postMessage(duplicate);
    assert.equal(capture().error?.code, "CAPTURE_OUTBOUND_IDENTITY_MISMATCH");
  }
});
test("fails closed for insert and edit success identity drift", () => {
  const invalidInsert = insertSuccess({
    payload: { ...insertSuccess().payload, rowKey: "other" },
  });
  let installed = install("table.insertRowRequested");
  installed.harness.webview.postMessage(insertRequest());
  installed.harness.dispatch(invalidInsert);
  assert.equal(capture().error?.code, "CAPTURE_TERMINAL_IDENTITY_MISMATCH");
  installed = install("table.insertRowRequested");
  installed.harness.webview.postMessage(insertRequest());
  installed.harness.dispatch(insertSuccess({ extra: true }));
  assert.equal(capture().error?.code, "CAPTURE_TERMINAL_IDENTITY_MISMATCH");
  const invalidEdit = editSuccess({
    payload: { ...editSuccess().payload, storedValue: "drift" },
  });
  installed = install("table.updateCellRequested");
  installed.harness.webview.postMessage(editRequest());
  installed.harness.dispatch(invalidEdit);
  assert.equal(capture().error?.code, "CAPTURE_TERMINAL_IDENTITY_MISMATCH");
  installed = install("table.updateCellRequested");
  installed.harness.webview.postMessage(editRequest());
  installed.harness.dispatch(editSuccess({ payload: {
    ...editSuccess().payload, currentRow: { id: "other", name: "after" },
  } }));
  assert.equal(capture().error?.code, "CAPTURE_TERMINAL_IDENTITY_MISMATCH");
});
test("turns a closed owner rejection into a stable sanitized error", () => {
  const { harness } = install("table.updateCellRequested");
  harness.webview.postMessage(editRequest());
  harness.dispatch(rejection());
  assert.deepEqual(capture().error, {
    method: "table.updateCellRequested",
    code: "TABLE_MUTATION_REJECTED",
    message: "updateCell rejected: edit_conflict (ROW_DIGEST_MISMATCH)",
  });
  assert.equal(JSON.stringify(capture().error).includes("secret"), false);
  assert.equal(capture().message, null);
  assert.equal(harness.listeners.size, 0);
  assert.equal(harness.webview.postMessage, harness.originalPostMessage);
});
test("ignores competing rejection but fails closed for malformed owner rejection", () => {
  const { harness } = install("table.insertRowRequested");
  harness.webview.postMessage(insertRequest());
  harness.dispatch(rejection({ operation: "updateCell" }));
  assert.equal(capture().error, null);
  const malformed = rejection({ operation: "insertRow" });
  delete malformed.payload.fieldErrors;
  harness.dispatch(malformed);
  assert.equal(capture().error?.code, "CAPTURE_TERMINAL_IDENTITY_MISMATCH");
  const rootHarness = install("table.insertRowRequested").harness;
  rootHarness.webview.postMessage(insertRequest());
  rootHarness.dispatch({ ...rejection({ operation: "insertRow" }), extra: true });
  assert.equal(capture().error?.code, "CAPTURE_TERMINAL_IDENTITY_MISMATCH");
});
test("reports operation.failed only as a sanitized post-owner environment failure", () => {
  const { harness } = install("table.insertRowRequested");
  harness.dispatch({
    type: "operation.failed",
    requestId: null,
    payload: { operation: "table.insertRowRequested", code: "HOST_DOWN", message: "early" },
  });
  assert.equal(capture().error, null);
  harness.webview.postMessage(insertRequest());
  harness.dispatch({ type: "operation.failed", requestId: null, payload: { code: "NONE" } });
  harness.dispatch({
    type: "operation.failed", requestId: null,
    payload: { operation: null, code: "NULL" },
  });
  harness.dispatch({
    type: "operation.failed",
    requestId: null,
    payload: { operation: "other.request", code: "OTHER", message: "competing" },
  });
  assert.equal(capture().error, null);
  harness.dispatch({
    type: "operation.failed",
    requestId: null,
    payload: {
      operation: "table.insertRowRequested",
      code: "HOST_DOWN",
      message: "C:\\Users\\secret\\database.db",
      path: "C:\\Users\\secret",
    },
  });
  assert.deepEqual(capture().error, {
    method: "table.insertRowRequested",
    code: "MUTATION_ENVIRONMENT_FAILURE",
    message: "table.insertRowRequested failed in the host environment (HOST_DOWN)",
  });
  assert.equal(JSON.stringify(capture().error).includes("secret"), false);
  assert.equal(capture().message, null);
  assert.equal(harness.listeners.size, 0);
  assert.equal(harness.webview.postMessage, harness.originalPostMessage);
});
test("replacement and either release order restore only the wrapper each capture owns", () => {
  for (const order of ["old-first", "new-first"]) {
    const harness = createHarness();
    install("table.insertRowRequested", harness);
    const old = capture();
    const oldWrapper = harness.webview.postMessage;
    const nextId = installTableMutationReceiptCaptureInPage({
      requestType: "table.updateCellRequested",
    });
    const current = capture();
    assert.equal(nextId, old.id + 1);
    assert.equal(old.error?.code, "CAPTURE_REPLACED");
    assert.notEqual(harness.webview.postMessage, oldWrapper);
    if (order === "old-first") {
      old.release();
      current.release();
    } else {
      current.release();
      old.release();
    }
    assert.equal(harness.listeners.size, 0);
    assert.equal(harness.webview.postMessage, harness.originalPostMessage);
  }
});
test("refuses to cross-replace active id-less captures without changing their waiter", () => {
  let harness = createHarness();
  globalThis.window = { chrome: { webview: harness.webview } };
  installWorkspaceV2MethodTerminalCaptureInPage("snapshot.export");
  const owned = capture();
  assert.throws(
    () => installTableMutationReceiptCaptureInPage({ requestType: "table.insertRowRequested" }),
    /active id-less capture/,
  );
  assert.equal(capture(), owned);
  assert.equal(owned.error?.code, "CAPTURE_REPLACED");
  assert.equal(owned.released, true);
  assert.equal(harness.listeners.size, 0);
  assert.equal(harness.webview.postMessage, harness.originalPostMessage);
  harness = createHarness();
  globalThis.window = { chrome: { webview: harness.webview } };
  const unowned = { types: ["database.opened"], message: null, error: null };
  window.__vibetableE2EBridgeCapture = unowned;
  assert.throws(
    () => installTableMutationReceiptCaptureInPage({ requestType: "table.insertRowRequested" }),
    /active id-less capture/,
  );
  assert.equal(capture(), unowned);
  assert.equal(unowned.error, null);
  assert.equal(harness.listeners.size, 0);
  assert.equal(harness.webview.postMessage, harness.originalPostMessage);
});
test("timeout release and post failure clean up idempotently without leaking native errors", async () => {
  let installed = install("table.insertRowRequested");
  const timeout = new Error("timeout");
  timeout.name = "TimeoutError";
  const page = {
    async waitForFunction() { throw timeout; },
    async evaluate(fn, expectedId) { return fn(expectedId); },
  };
  await assert.rejects(waitForCapturedBridgeMessage(page, 1, installed.id), /timed out/);
  capture().release();
  assert.equal(installed.harness.listeners.size, 0);
  assert.equal(installed.harness.webview.postMessage, installed.harness.originalPostMessage);
  installed = install("table.insertRowRequested", createHarness({ throwOnPost: true }));
  assert.throws(() => installed.harness.webview.postMessage(insertRequest()), /native post failed/);
  assert.deepEqual(capture().error, {
    method: "table.insertRowRequested",
    code: "CAPTURE_POST_FAILED",
    message: "table mutation owner could not be posted",
  });
  assert.equal(JSON.stringify(capture().error).includes("private"), false);
  assert.equal(installed.harness.listeners.size, 0);
  assert.equal(installed.harness.webview.postMessage, installed.harness.originalPostMessage);
});
