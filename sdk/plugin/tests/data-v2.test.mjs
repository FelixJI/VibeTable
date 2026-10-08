import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { testing } from "./load-sdk.mjs";
const corpus = JSON.parse(await readFile(new URL("../../../tests/contract/fixtures/plugin-capabilities-v2.json", import.meta.url), "utf8"));
function fixture(overrides = {}) {
  const requests = [];
  const host = testing.createOfflineHost({ pluginApi: "2.x", context: { collection: "articles" },
    permissions: { data: [{ collection: "$active", operations: ["read", "query"], fields: ["$configured"] }] },
    descriptions: { articles: corpus.description }, queryAdapter: async request => {
      requests.push(request); const index = request.cursor ? Number(request.cursor.split("-").at(-1)) - 1 : 0;
      return structuredClone(corpus.pages[index]);
    }, ...overrides });
  return { host, requests };
}
test("offlineHost consumes the real Go v2 producer corpus without a filter engine", async () => {
  const { host, requests } = fixture();
  assert.deepEqual(await host.capabilities.data.describe({ accepts: ["vibetable.plugin-data.v2"], collection: "articles" }), corpus.description);
  let cursor, rows = [];
  do { const page = await host.capabilities.data.query(cursor ? { ...corpus.request, cursor } : corpus.request); rows.push(...page.items); cursor = page.nextCursor; } while(cursor);
  assert.equal(rows.length, 550); assert.equal(new Set(rows.map(row => row.id)).size, 550);
  assert.ok(rows.some(row => row.fld_amount === 0)); assert.ok(rows.some(row => row.fld_amount === null));
  assert.ok(rows.some(row => row.fld_enabled === false)); assert.ok(rows.some(row => row.fld_date === null));
  assert.equal(requests.length, 3);
});
test("shared Go permission and contract counterexamples have identical offline codes", async () => {
  for (const item of corpus.rejectCases) {
    const { host, requests } = fixture();
    await assert.rejects(host.capabilities.data.query({ ...corpus.request, ...item.patch }), error => error.code === item.code, item.name);
    assert.equal(requests.length, 0, item.name);
  }
});
test("opaque cursors are single-use and reject changed queries, runs and revisions", async () => {
  let revision = "1"; const { host } = fixture({ revision: () => revision });
  const execution = host.createExecution(); const first = await execution.capabilities.data.query(corpus.request);
  await assert.rejects(host.createExecution().capabilities.data.query({ ...corpus.request, cursor: first.nextCursor }), error => error.code === "plugin_cursor_invalid");
  const second = await execution.capabilities.data.query({ ...corpus.request, cursor: first.nextCursor });
  await assert.rejects(execution.capabilities.data.query({ ...corpus.request, cursor: first.nextCursor }), error => error.code === "plugin_cursor_invalid");
  revision = "2";
  await assert.rejects(execution.capabilities.data.query({ ...corpus.request, cursor: second.nextCursor }), error => error.code === "plugin_cursor_stale");
  const fresh = await execution.capabilities.data.query(corpus.request);
  await assert.rejects(execution.capabilities.data.query({ ...corpus.request, sorts: [], cursor: fresh.nextCursor }), error => error.code === "plugin_cursor_invalid");
});
test("v1 rejects negotiation, missing adapters and cancellation fail closed", async () => {
  await assert.rejects(testing.createOfflineHost().capabilities.data.describe({ accepts: ["vibetable.plugin-data.v2"] }), error => error.code === "plugin_api_unsupported");
  await assert.rejects(fixture({ queryAdapter: undefined }).host.capabilities.data.query(corpus.request), error => error.code === "plugin_query_failed");
  const { host } = fixture(); host.requestCancel();
  await assert.rejects(host.capabilities.data.query(corpus.request), error => error.code === "plugin_cancel_requested");
});
