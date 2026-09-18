import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

const source = fs.readFileSync(new URL("./webview_product_scenarios.mjs", import.meta.url), "utf8");
const extract = (start, end) => source.slice(source.indexOf(start), source.indexOf(end));

let bridgeRequests = [];
let workspaceRequests = [];
let requestFailures = new Map();

// The extracted helper closes over these module bindings at call time, so the
// fakes stand in for the scenario runner's raw request helpers.
async function rawBridgeRequest(page, type, payload) {
  bridgeRequests.push({ page, type, payload });
  const failure = requestFailures.get(type);
  if (failure) throw failure;
  return { type, payload: { recorded: type, params: payload } };
}

async function rawWorkspaceV2Request(page, method, params) {
  workspaceRequests.push({ page, method, params });
  const failure = requestFailures.get(method);
  if (failure) throw failure;
  return { ok: true, result: { recorded: method, params } };
}

const collectRestoredSearchDiagnostics = eval(`(${extract(
  "async function collectRestoredSearchDiagnostics", "async function scenario12",
)})`);

test("restored-search diagnostics issue one read-only request per boundary", async () => {
  bridgeRequests = [];
  workspaceRequests = [];
  requestFailures = new Map();
  const page = { id: "page" };
  const attachmentParams = { tableId: "tbl", recordId: "rec", fieldId: "fld" };

  const diagnostics = await collectRestoredSearchDiagnostics(page, attachmentParams, "tbl");

  assert.deepEqual(
    bridgeRequests.map((item) => [item.type, item.payload, item.page]),
    [
      ["file.list", attachmentParams, page],
      ["query.page", {
        tableId: "tbl",
        query: { filters: [], sorts: [], offset: 0, limit: 100 },
      }, page],
    ],
  );
  assert.deepEqual(
    workspaceRequests.map((item) => [item.method, item.params, item.page]),
    [["workspaceSearch.status", {}, page]],
  );
  assert.deepEqual(diagnostics.gaps, []);
  assert.equal(diagnostics.attachmentList.payload.params, attachmentParams);
  assert.equal(diagnostics.targetRecord.payload.params.tableId, "tbl");
  assert.deepEqual(diagnostics.searchIndex.result.params, {});
});

test("a failing diagnostic records a gap instead of hiding the original failure", async () => {
  bridgeRequests = [];
  workspaceRequests = [];
  requestFailures = new Map([
    ["file.list", new Error("bridge timeout for file.list")],
  ]);

  const diagnostics = await collectRestoredSearchDiagnostics(
    { id: "page" },
    { tableId: "tbl", recordId: "rec", fieldId: "fld" },
    "tbl",
  );

  assert.equal(diagnostics.attachmentList, null);
  assert.deepEqual(diagnostics.gaps, ["attachmentList: bridge timeout for file.list"]);
  assert.ok(diagnostics.targetRecord.payload.params);
  assert.ok(diagnostics.searchIndex.result.params);
  // The bounded snapshot stops after one attempt per boundary.
  assert.equal(bridgeRequests.filter((item) => item.type === "file.list").length, 1);
});
