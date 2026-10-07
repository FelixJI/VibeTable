import assert from "node:assert/strict";
import test from "node:test";
import { sdk, testing } from "../../../../sdk/plugin/tests/load-sdk.mjs";
import { normalizeText, run } from "../dist/workers/normalize.js";

const digest = `sha256:${"a".repeat(64)}`;
const options = {
  context: { collection: "articles", selectedKeys: ["1", "2", "3"] },
  permissions: { data: [{ collection: "$active", operations: ["read", "update"], fields: ["$configured"] }] },
  collections: { articles: [{ id: "1", title: "  Vibe   Table  " }, { id: "2", title: 0 }, { id: "3", title: "clean" }, { id: "4", title: " keep " }] },
  writableFields: { articles: { create: [], update: ["title"] } },
  rowGuards: { articles: { "1": digest } },
};

test("normalization previews only selected changed text and carries the Go guard", async () => {
  const host = testing.createOfflineHost(options);
  const plan = await run({ field: "title", strategy: "collapse-whitespace" }, host.capabilities);
  assert.deepEqual(plan.operations, [{ kind: "update", primaryKey: "1", expectedDigest: digest, values: { title: "Vibe Table" } }]);
  assert.deepEqual(plan.preview.sampleRows, [{ id: "1", before: "  Vibe   Table  ", after: "Vibe Table" }]);
  assert.equal(normalizeText("  Vibe  ", "trim"), "Vibe");
  assert.equal(normalizeText("Vibe", "lowercase"), "vibe");
  assert.equal(normalizeText("Vibe", "uppercase"), "VIBE");
});

test("approval commits once; rejection and cancel commit zero times", async () => {
  let commits = 0;
  const applyMutation = async () => { commits++; return sdk.ok({ updated: 1 }); };
  for (const approveMutation of [false, true]) {
    const host = testing.createOfflineHost({ ...options, approveMutation, applyMutation });
    const result = await testing.startOfflineAction(run, { field: "title", strategy: "trim" }, host, { risk: "write" }).result;
    assert.equal(result.status, approveMutation ? "success" : "error");
  }
  assert.equal(commits, 1);
  const host = testing.createOfflineHost({ ...options, approveMutation: true, applyMutation });
  const action = testing.startOfflineAction(run, { field: "title", strategy: "trim" }, host, { risk: "write" });
  action.cancel();
  assert.equal((await action.result).status, "error");
  assert.equal(commits, 1);
});
