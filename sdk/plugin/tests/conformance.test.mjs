import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { sdk, testing } from "./load-sdk.mjs";

const corpus = JSON.parse(await readFile(new URL("../../../tests/contract/fixtures/plugin-capabilities-v1.json", import.meta.url), "utf8"));
const permissions = { data: [{ collection: "$active", operations: ["read", "update"], fields: ["$configured"] }], privateStorage: true };
const options = { context: { collection: "articles" }, permissions, fields: { articles: corpus.fields } };

test("offline reads follow the shared real Worker corpus", async () => {
  for (const item of corpus.readCases) {
    const rows = Array.from({ length: item.rowCount ?? 1 }, (_, index) => ({ ...corpus.rows[0], id: String(index + 1) }));
    const host = testing.createOfflineHost({ ...options, collections: { articles: rows } });
    if (item.code) await assert.rejects(host.capabilities.data.read(item.request), error => error.code === item.code, item.name);
    else {
      const page = await host.capabilities.data.read(item.request);
      assert.equal(page.items.length, item.count, item.name);
      assert.equal(page.nextCursor, item.nextCursor, item.name);
      if (item.first) assert.deepEqual(page.items[0], item.first, item.name);
    }
  }
});

test("deprecated interfaces never succeed and storage preserves original JSON values", async () => {
  const host = testing.createOfflineHost(options);
  await assert.rejects(host.capabilities.data.mutate(corpus.returns[1]), error => error.code === "plugin_direct_mutation_unsupported");
  await assert.rejects(host.capabilities.ui.emitResult(corpus.returns[0]), error => error.code === "plugin_emit_result_unsupported");
  for (const value of [0, false, null, [0, false, null]]) {
    await host.capabilities.storage.set("value", value);
    assert.deepEqual(await host.capabilities.storage.get("value"), value);
  }
  host.requestCancel();
  assert.deepEqual(await host.capabilities.ui.reportProgress({ current: 1, total: 1 }), { cancelRequested: true });
});

test("read result and write plan follow separate confirmation/commit paths", async () => {
  let commits = 0;
  const host = testing.createOfflineHost({ ...options, approveMutation: true,
    applyMutation: async () => { commits++; return corpus.returns[0]; } });
  assert.equal((await testing.startOfflineAction(async () => corpus.returns[0], {}, host).result).status, "success");
  assert.equal(commits, 0);
  assert.equal((await testing.startOfflineAction(async () => corpus.returns[1], {}, host).result).status, "success");
  assert.equal(commits, 1);
  const denied = testing.createOfflineHost({ ...options, approveMutation: false,
    applyMutation: async () => { commits++; return corpus.returns[0]; } });
  assert.equal((await testing.startOfflineAction(async () => corpus.returns[1], {}, denied).result).table.code, "plugin_mutation_rejected");
  assert.equal(commits, 1);
});

test("cancel and timeout settle even when the action never cooperates", async () => {
  const host = testing.createOfflineHost(options);
  const run = testing.startOfflineAction(async () => new Promise(() => {}), {}, host);
  run.cancel();
  assert.deepEqual((await run.result).warnings, ["cancel_requested"]);
  const timeout = testing.startOfflineAction(async () => new Promise(() => {}), {}, testing.createOfflineHost(options), { timeoutMs: 1 });
  assert.equal((await timeout.result).table.code, "plugin_timeout");
});

test("conflict and unknown commit are explicit, with no replay", async () => {
  let commits = 0;
  for (const code of ["mutation.digest_conflict", "plugin_commit_unknown"]) {
    const host = testing.createOfflineHost({ ...options, approveMutation: true, applyMutation: async () => {
      commits++; throw new sdk.PluginCapabilityError(code, "synthetic outcome");
    } });
    assert.equal((await testing.startOfflineAction(async () => corpus.returns[1], {}, host).result).table.code, code);
  }
  assert.equal(commits, 2);
});

test("invalid guard values never reach confirmation or commit", async () => {
  for (const guard of corpus.invalidGuards) {
    const plan = structuredClone(corpus.returns[1]);
    Object.assign(plan.operations[0], guard);
    const host = testing.createOfflineHost({ ...options, approveMutation: true,
      applyMutation: async () => { assert.fail("invalid plan reached commit"); } });
    assert.equal((await testing.startOfflineAction(async () => plan, {}, host).result).table.code, "plugin_worker_failed");
    assert.equal(host.mutationPlans.length, 0);
  }
});

test("invalid wire returns match real Worker/Host errors before confirmation or commit", async t => {
  for (const item of corpus.invalidReturns) {
    await t.test(item.name, async () => {
      let confirmations = 0;
      let commits = 0;
      const host = testing.createOfflineHost({ ...options,
        approveMutation: () => { confirmations++; return true; },
        applyMutation: async () => { commits++; return corpus.returns[0]; } });
      const result = await testing.startOfflineAction(async () => item.value, {}, host).result;
      assert.equal(result.status, "error");
      assert.equal(result.table.code, item.code);
      assert.equal(confirmations, 0);
      assert.equal(commits, 0);
      assert.equal(host.mutationPlans.length, 0);
    });
  }
});

test("valid wire members, model defaults and legacy aliases remain accepted", async t => {
  for (const item of corpus.validReturns) {
    await t.test(item.name, async () => {
      let commits = 0;
      const host = testing.createOfflineHost({ ...options, approveMutation: true,
        applyMutation: async () => { commits++; return corpus.returns[0]; } });
      const result = await testing.startOfflineAction(async () => item.value, {}, host).result;
      assert.equal(result.status, "success");
      assert.equal(commits, item.risk === "write" ? 1 : 0);
      if (item.risk === "read" && item.value.metrics) {
        assert.deepEqual(result.metrics.map(metric => metric.value), [1, 1, "text"]);
      }
      if (item.risk === "write") assert.equal(host.mutationPlans[0].preview.affectedCount,
        item.value.operations.length);
    });
  }
});
