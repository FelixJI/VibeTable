import test from "node:test";
import assert from "node:assert/strict";
import {
  calculationChainDisplayText, calculationChainUiFixture, calculationChainUiOracle,
  drainCorrelatedQueryReloads, waitForApplyBackfillFreshness,
} from "./calculation_chain_journey.mjs";

test("S38 independent display oracle pins the default grouped DOM number text", () => {
  assert.equal(calculationChainDisplayText(1982), "1,982");
  assert.equal(calculationChainDisplayText(1983), "1,983");
  assert.equal(calculationChainDisplayText(2982), "2,982");
  assert.equal(calculationChainDisplayText(2983), "2,983");
  assert.equal(calculationChainDisplayText(991), "991");
  assert.equal(calculationChainDisplayText(7), "7");
  assert.equal(calculationChainDisplayText(0), "0");
  const raw = [["合同甲", 991, 1982], ["合同丙", 0, 0]];
  const displayed = raw.map(row => row.map(calculationChainDisplayText));
  assert.deepEqual(displayed, [["合同甲", "991", "1,982"], ["合同丙", "0", "0"]]);
  assert.deepEqual(raw, [["合同甲", 991, 1982], ["合同丙", 0, 0]]);
});

test("S38 independent oracle pins 199/1/0 sources and both UI edits", () => {
  const sources = calculationChainUiFixture();
  assert.equal(sources.length, 201);
  assert.equal(new Set(sources.map(row => row.marker)).size, 201);
  assert.deepEqual(calculationChainUiOracle(sources, 2), [
    { contract: "合同甲", matches: 199, sum: 991, doubled: 1982, total: 1983 },
    { contract: "合同乙", matches: 1, sum: 7, doubled: 14, total: 15 },
    { contract: "合同丙", matches: 0, sum: 0, doubled: 0, total: 1 },
  ]);
  sources[0].amount += 3;
  assert.deepEqual(calculationChainUiOracle(sources, 3), [
    { contract: "合同甲", matches: 199, sum: 994, doubled: 2982, total: 2983 },
    { contract: "合同乙", matches: 1, sum: 7, doubled: 21, total: 22 },
    { contract: "合同丙", matches: 0, sum: 0, doubled: 0, total: 1 },
  ]);
});

const chainQuery = { filters: [], sorts: [], offset: 0, limit: 500 };
const appliedReceipt = {
  schemaRevision: "schema-2",
  definition: { identity: { physicalName: "f_total" } },
};
const completedPayload = (values = [1983, 15, 0]) => ({
  snapshot: { table: "tbl_main", schemaRevision: "schema-2", dataRevision: 3 },
  rows: values.map((value, index) => ({ id: `r${index}`, f_total: value })),
});
const updating = { state: "updating", value: null, diagnostic: null };

test("apply freshness blocks the next describe/open until every row finishes backfill", async () => {
  let releaseBackfill;
  const backfillCompletes = new Promise(resolve => { releaseBackfill = resolve; });
  const events = [];
  const waitForQueryPage = async (_page, payload, predicate) => {
    assert.deepEqual(payload, { tableId: "tbl_main", query: chainQuery });
    assert.equal(predicate(completedPayload([1983, updating, 0])), false);
    events.push("waiting");
    await backfillCompletes;
    const ready = completedPayload();
    assert.equal(predicate(ready), true);
    events.push("ready");
    return { type: "query.page", payload: ready };
  };
  const continuation = waitForApplyBackfillFreshness({}, waitForQueryPage,
    "tbl_main", chainQuery, appliedReceipt).then(() => events.push("describe", "open"));
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(events, ["waiting"]);
  releaseBackfill();
  await continuation;
  assert.deepEqual(events, ["waiting", "ready", "describe", "open"]);
});

test("ready numeric zero passes immediately without another query", async () => {
  let queries = 0;
  await waitForApplyBackfillFreshness({}, async (_page, _payload, predicate) => {
    queries += 1;
    assert.equal(predicate(completedPayload([0, 0, 0])), true);
  }, "tbl_main", chainQuery, appliedReceipt);
  assert.equal(queries, 1);
});

test("stale schema, incomplete rows and unfinished computed values cannot advance", async () => {
  const stale = completedPayload();
  stale.snapshot.schemaRevision = "schema-1";
  const partial = completedPayload([1983, 15]);
  const duplicate = completedPayload();
  duplicate.rows[2].id = duplicate.rows[1].id;
  const failure = new Error("query.page did not reach the expected state");
  for (const payload of [stale, partial, duplicate, completedPayload([1983, updating, 0]),
    completedPayload([1983, { state: "failed", value: null }, 0]), completedPayload([1983, null, 0])]) {
    await assert.rejects(waitForApplyBackfillFreshness({}, async (_page, _payload, predicate) => {
      assert.equal(predicate(payload), false);
      throw failure;
    }, "tbl_main", chainQuery, appliedReceipt), error => error === failure);
  }
});

// Minimal deterministic waitForFunction double: evaluates the in-page predicate
// against the shimmed window, re-polling only while it stays falsy.
const createDrainPage = () => {
  const state = { evaluations: 0, options: null };
  const page = {
    waitForFunction(predicate, arg, options) {
      state.options = options;
      return new Promise((resolve, reject) => {
        let settled = false;
        const evaluate = () => {
          state.evaluations += 1;
          let satisfied = false;
          try {
            satisfied = predicate(arg);
          } catch (error) {
            settled = true;
            reject(error);
            return;
          }
          if (satisfied) {
            settled = true;
            resolve(true);
          }
        };
        evaluate();
        const timer = setInterval(() => {
          if (settled) clearInterval(timer);
          else evaluate();
        }, 1);
      });
    },
  };
  return { page, state };
};

test("query-reload drain holds the next table switch until the correlated reload terminal", async () => {
  const previousWindow = globalThis.window;
  const holdProbe = new Promise(resolve => setTimeout(resolve, 20));
  try {
    // The DOM oracle can already be satisfied while the journey's refresh
    // reload is still in flight: a correlated table.queryRequested stays
    // pending, so the drain must hold; only its terminal releases the wait.
    const pending = {
      "r-468": { requestType: "table.queryRequested" },
      "r-476": { requestType: "lookup.query" },
    };
    globalThis.window = { __vibetableE2EBridgeDiagnostics: { pending } };
    const held = createDrainPage();
    const draining = drainCorrelatedQueryReloads(held.page);
    assert.equal(await Promise.race([draining.then(() => true), holdProbe.then(() => false)]), false);
    delete pending["r-468"];
    await draining;
    assert.deepEqual(held.state.options, { timeout: 60_000 });

    // Notify-path refreshes post no correlated request: unrelated pending work
    // (or none) passes on the first evaluation, so the journey adds no delay.
    for (const notifyPending of [{ "r-477": { requestType: "lookup.query" } }, {}]) {
      globalThis.window = { __vibetableE2EBridgeDiagnostics: { pending: notifyPending } };
      const immediate = createDrainPage();
      await drainCorrelatedQueryReloads(immediate.page);
      assert.equal(immediate.state.evaluations, 1);
      assert.deepEqual(immediate.state.options, { timeout: 60_000 });
    }

    // A missing diagnostics ledger must fail closed, not masquerade as drained.
    globalThis.window = {};
    await assert.rejects(drainCorrelatedQueryReloads(createDrainPage().page), /diagnostics pending ledger/u);
    globalThis.window = { __vibetableE2EBridgeDiagnostics: {} };
    await assert.rejects(drainCorrelatedQueryReloads(createDrainPage().page), /diagnostics pending ledger/u);
  } finally {
    globalThis.window = previousWindow;
  }
});
