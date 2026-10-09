import assert from "node:assert/strict";
import test from "node:test";
import { testing } from "../../../../sdk/plugin/tests/load-sdk.mjs";
import { run } from "../dist/workers/overview.js";
const description = { contract: "vibetable.plugin-data.v2", collection: "articles", displayName: "Synthetic", schemaRevision: "schema-1", fields: [{ fieldId: "id", displayName: "ID", logicalType: "text", resultType: "text", readonly: true, nullable: false, display: null, options: [], constraints: null, filterOperators: ["eq", "in"], sortable: true }] };
function synthetic(count) {
  const requests = [];
  const host = testing.createOfflineHost({ pluginApi: "2.x", context: { collection: "articles" },
    permissions: { data: [{ collection: "$active", operations: ["read", "query"], fields: ["$configured"] }] },
    descriptions: { articles: description }, queryAdapter: async request => {
      requests.push(request); const offset = Number(request.cursor ?? 0);
      const items = Array.from({ length: Math.min(request.pageSize, count - offset) }, (_, i) => ({ id: String(offset + i + 1) }));
      return { contract: "vibetable.plugin-query-page.v2", items, nextCursor: offset + items.length < count ? String(offset + items.length) : null, filteredRows: count, totalRows: count, schemaRevision: "schema-1", dataRevision: 1, complete: true };
    } });
  return { host, requests };
}
for (const count of [0, 550, 6200]) test(`overview consumes every synthetic page for ${count} rows`, async () => {
  const { host, requests } = synthetic(count);
  const result = await testing.startOfflineAction(run, {}, host).result;
  assert.equal(result.status, "success"); assert.equal(result.table.data.count, count);
  assert.equal(requests.length, Math.max(1, Math.ceil(count / 200)));
  assert.equal(host.progressEvents.length, 1);
});
test("missing context and unavailable query adapters fail clearly", async () => {
  assert.equal((await testing.startOfflineAction(run, {}, testing.createOfflineHost()).result).status, "error");
  const { host } = synthetic(550); host.requestCancel();
  await assert.rejects(host.capabilities.data.query({ contract: "vibetable.plugin-query.v2", collection: "articles", fields: ["id"] }), error => error.code === "plugin_cancel_requested");
});
