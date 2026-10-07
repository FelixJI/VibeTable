import assert from "node:assert/strict";
import test from "node:test";
import { testing } from "../../../../sdk/plugin/tests/load-sdk.mjs";
import { run } from "../dist/workers/overview.js";

test("overview counts real synthetic rows across the 200-row boundary", async () => {
  const rows = Array.from({ length: 401 }, (_, index) => ({ id: String(index + 1) }));
  const host = testing.createOfflineHost({ context: { collection: "articles" },
    permissions: { data: [{ collection: "$active", operations: ["read"], fields: ["id"] }] },
    collections: { articles: rows } });
  const result = await testing.startOfflineAction(run, {}, host).result;
  assert.equal(result.status, "success");
  assert.deepEqual(result.table, { data: { count: 401 } });
  assert.equal(host.progressEvents.length, 1);
  assert.equal(host.progressEvents[0].current, 401);
});

test("a 6200-row table is counted within the Worker capability budget", async () => {
  // The real Worker budget is 64 capability calls; paging the whole table 200
  // rows at a time needs 65 here (1 context.read + 32 reads + 32 progress).
  // One authoritative totalRows read plus one progress receipt keeps 3 calls.
  const rows = Array.from({ length: 6200 }, (_, index) => ({ id: String(index + 1) }));
  const host = testing.createOfflineHost({ context: { collection: "articles" },
    permissions: { data: [{ collection: "$active", operations: ["read"], fields: ["id"] }] },
    collections: { articles: rows } });
  const result = await testing.startOfflineAction(run, {}, host).result;
  assert.equal(result.status, "success");
  assert.deepEqual(result.table, { data: { count: 6200 } });
  assert.equal(host.progressEvents.length, 1);
});

test("an empty table reports zero and a missing active table fails clearly", async () => {
  const host = testing.createOfflineHost({ context: { collection: "articles" }, fields: { articles: ["id"] },
    permissions: { data: [{ collection: "$active", operations: ["read"], fields: ["id"] }] } });
  assert.equal((await testing.startOfflineAction(run, {}, host).result).table.data.count, 0);
  const missing = await testing.startOfflineAction(run, {}, testing.createOfflineHost()).result;
  assert.equal(missing.status, "error");
});
