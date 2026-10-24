import assert from "node:assert/strict";
import fs from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

// Contract test for the table-delete catalog observation channel used by the
// 02 scenario. The in-page helpers are not exported; extract them with the
// same source-slicing pattern as scenario08_stale_conflict.test.mjs.
const source = await fs.readFile(
  new URL("./webview_product_scenarios.mjs", import.meta.url),
  "utf8",
);
const helperSource = source.slice(
  source.indexOf("function installTableDeleteCatalogLogInPage("),
  source.indexOf("async function beginWritableWorkspaceBootstrapCapture("),
);

function createSandbox() {
  const listeners = new Set();
  const posted = [];
  const webview = {
    addEventListener: (_type, listener) => listeners.add(listener),
    removeEventListener: (_type, listener) => listeners.delete(listener),
    postMessage: (message) => {
      posted.push(message);
      return "posted";
    },
  };
  const window = { chrome: { webview } };
  const sandbox = { window, structuredClone, console };
  vm.createContext(sandbox);
  sandbox.deliver = (message) => {
    for (const listener of [...listeners]) listener({ data: message });
  };
  sandbox.bridge = { webview, posted, listeners };
  vm.runInContext(helperSource, sandbox);
  return sandbox;
}

const install = (sandbox, inboundTypes, outboundTypes = []) =>
  vm.runInContext("installTableDeleteCatalogLogInPage", sandbox)({
    inboundTypes, outboundTypes,
  });
const read = (sandbox) => vm.runInContext("readTableDeleteCatalogLogInPage", sandbox)();
const release = (sandbox) => vm.runInContext("releaseTableDeleteCatalogLogInPage", sandbox)();

test("the channel records inbound catalog messages in both envelope forms", () => {
  const sandbox = createSandbox();
  install(sandbox, ["database.collectionsChanged", "database.opened"]);
  sandbox.deliver({ type: "database.collectionsChanged", requestId: null,
    payload: { tables: ["tbl_a"] } });
  sandbox.deliver(JSON.stringify({ type: "database.opened", payload: { tables: [] } }));
  sandbox.deliver({ type: "table.editCommitted", payload: {} });
  sandbox.deliver("not-json");
  const observed = read(sandbox);
  assert.equal(observed.inbound.length, 2);
  assert.equal(observed.inbound[0].payload.tables[0], "tbl_a");
  assert.equal(observed.inbound[0].requestId, null);
  assert.equal(observed.inbound[1].type, "database.opened");
  release(sandbox);
});

test("the outbound wrapper passes every message through unchanged and clones records", () => {
  const sandbox = createSandbox();
  install(sandbox, [], ["tableAdmin.deleteRequested"]);
  const request = { type: "tableAdmin.deleteRequested", requestId: "delete-1",
    payload: { collection: "tbl_x" } };
  assert.equal(sandbox.bridge.webview.postMessage(request), "posted");
  sandbox.bridge.webview.postMessage({ type: "table.selected", payload: { table: "tbl_x" } });
  assert.equal(sandbox.bridge.posted[0], request, "the original message reference passes through");
  assert.deepEqual(sandbox.bridge.posted[1], { type: "table.selected", payload: { table: "tbl_x" } });
  const observed = read(sandbox);
  assert.equal(observed.outbound.length, 1);
  assert.equal(observed.outbound[0].requestId, "delete-1");
  request.payload.collection = "mutated-after-post";
  assert.equal(observed.outbound[0].payload.collection, "tbl_x",
    "the record must be a clone, not the live message");
  release(sandbox);
});

test("reinstall replaces the previous channel and release restores the pristine bridge", () => {
  const sandbox = createSandbox();
  const original = sandbox.bridge.webview.postMessage;
  install(sandbox, ["database.collectionsChanged"]);
  const firstWrapper = sandbox.bridge.webview.postMessage;
  install(sandbox, ["database.opened"]);
  assert.notEqual(sandbox.bridge.webview.postMessage, firstWrapper,
    "the new install must replace the previous wrapper");
  sandbox.deliver({ type: "database.collectionsChanged", payload: {} });
  assert.equal(read(sandbox).inbound.length, 0,
    "the replaced channel must no longer record");
  sandbox.deliver({ type: "database.opened", payload: {} });
  assert.equal(read(sandbox).inbound.length, 1);
  release(sandbox);
  assert.equal(sandbox.bridge.webview.postMessage, original,
    "release must restore the original postMessage");
  sandbox.deliver({ type: "database.opened", payload: {} });
  const afterRelease = read(sandbox);
  assert.equal(afterRelease.inbound.length, 0,
    "a released channel records nothing and reads empty");
  assert.equal(afterRelease.outbound.length, 0);
  release(sandbox);
});

test("a scenario failure inside try still restores the bridge in finally", () => {
  const sandbox = createSandbox();
  const original = sandbox.bridge.webview.postMessage;
  assert.throws(() => {
    try {
      install(sandbox, ["database.collectionsChanged"]);
      throw new Error("scenario step failed");
    } finally {
      release(sandbox);
    }
  }, /scenario step failed/);
  assert.equal(sandbox.bridge.webview.postMessage, original);
  assert.equal(sandbox.bridge.listeners.size, 0);
});

test("deleted-table reads require the real query.page not-found error and a readable survivor", () => {
  const start = source.indexOf('  recorder.check("02 the deleted table is no longer queryable');
  const end = source.indexOf('\n  // 真实重开', start);
  assert.ok(start >= 0 && end > start, "the product probe assertion must exist");
  const probeAssertion = source.slice(start, end);
  const check = (deletedProbe, survivorProbe = { type: "query.page", payload: { rows: [] } }) =>
    vm.runInNewContext(probeAssertion, {
      deletedProbe, survivorProbe,
      recorder: { check: (name, passed) => assert.equal(passed, true, name) },
    });
  const notFound = {
    type: "query.page",
    payload: { error: { code: "query.table.not_found", path: "table", retryable: false } },
  };
  check(notFound);
  assert.throws(() => check({ type: "operation.failed", payload: {} }));
  assert.throws(() => check({ type: "query.page", payload: { rows: [] } }));
  assert.throws(() => check({ type: "query.page", payload: { error: { code: "query.timeout" } } }));
  assert.throws(() => check(notFound, { type: "query.page", payload: { error: { code: "query.table.not_found" } } }));
});
