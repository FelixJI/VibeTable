import test from "node:test";
import assert from "node:assert/strict";
import {
  pendingEffects,
  distinguishableRequests,
  matchPendingWindowsToRequests,
  assertConcurrentOutcome,
  assertConcurrentAuthority,
  assertT01Generation,
} from "./plugin_integration.mjs";

// Both eight-occupancy plans write the same balance head's derived backlink
// update while their line/alloc inserts stay exclusive to each request's newIds.
const sharedHeadUpdate = "tbl_head:headshared001:update";
const firstPreview = {
  requestId: "req-first", newIds: { lines: { l: "linefirst001" }, allocations: { a: "allocfirst01" } },
  effects: [
    { tableId: "tbl_head", recordId: "headshared001", operation: "update" },
    { tableId: "tbl_line", recordId: "linefirst001", operation: "insert" },
    { tableId: "tbl_alloc", recordId: "allocfirst01", operation: "insert" },
  ],
};
const secondPreview = {
  requestId: "req-second", newIds: { lines: { l: "linesecond01" }, allocations: { a: "allocsecond01" } },
  effects: [
    { tableId: "tbl_head", recordId: "headshared001", operation: "update" },
    { tableId: "tbl_line", recordId: "linesecond01", operation: "insert" },
    { tableId: "tbl_alloc", recordId: "allocsecond01", operation: "insert" },
  ],
};
const firstWindow = { hwnd: 111, pid: 4242, effects: pendingEffects(firstPreview) };
const secondWindow = { hwnd: 222, pid: 4242, effects: pendingEffects(secondPreview) };

test("two plans sharing the derived head update stay distinguishable through their own newIds", () => {
  const distinction = distinguishableRequests(firstPreview, secondPreview);
  assert.deepEqual(distinction.shared, [sharedHeadUpdate]);
  assert.equal(distinction.firstOnly.length, 2);
  assert.equal(distinction.secondOnly.length, 2);
  assert.ok(distinction.firstOnly.every(effect => effect.includes("first")));
  assert.ok(distinction.secondOnly.every(effect => effect.includes("second")));
});

test("pending windows bind through complete effect-set equality regardless of observation order", () => {
  const mapping = matchPendingWindowsToRequests(
    [
      { ...secondWindow, effects: [...secondWindow.effects].reverse() },
      { ...firstWindow, effects: [...firstWindow.effects].reverse() },
    ],
    firstPreview,
    secondPreview,
  );
  assert.equal(mapping.first, firstWindow.hwnd);
  assert.equal(mapping.second, secondWindow.hwnd);
  assert.deepEqual(mapping.shared, [sharedHeadUpdate]);
});

test("identical complete effect sets are rejected as indistinguishable requests", () => {
  assert.throws(
    () => distinguishableRequests(firstPreview, { ...secondPreview, effects: firstPreview.effects }),
    /identical complete effect sets/,
  );
});

test("a window that matches neither complete set is a mismatch", () => {
  const foreign = { hwnd: 333, pid: 4242, effects: [
    "tbl_head:headshared001:update",
    "tbl_line:lineother0001:insert",
    "tbl_line:lineother0001:update",
    "tbl_alloc:allocother001:insert",
  ] };
  assert.throws(
    () => matchPendingWindowsToRequests([foreign, secondWindow], firstPreview, secondPreview),
    /does not exactly bind either prepared request/,
  );
});

test("a window missing the shared head effect is an incomplete set and never matches", () => {
  const incomplete = { hwnd: 444, pid: 4242, effects: pendingEffects(firstPreview).filter(effect => effect !== sharedHeadUpdate) };
  assert.throws(
    () => matchPendingWindowsToRequests([incomplete, secondWindow], firstPreview, secondPreview),
    /does not exactly bind either prepared request/,
  );
});

test("two windows binding the same request are rejected", () => {
  assert.throws(
    () => matchPendingWindowsToRequests([firstWindow, { ...firstWindow, hwnd: 555 }], firstPreview, secondPreview),
    /two windows bind the first request/,
  );
});

test("a window without effects or an unbound request fails closed", () => {
  assert.throws(() => matchPendingWindowsToRequests([{ hwnd: 1, effects: [] }, secondWindow], firstPreview, secondPreview), /no canonical effects/);
  assert.throws(() => matchPendingWindowsToRequests([firstWindow], firstPreview, secondPreview), /no bound window/);
});

function appliedPreview(preview) {
  return { ...preview, status: "applied", receipt: { status: "applied", changeSetId: "cs1", rows: preview.effects.map(effect => ({ ...effect })) } };
}

test("exactly one applied request with an explicit plan_stale loser reconciles the boundary", () => {
  const outcome = assertConcurrentOutcome(
    { first: appliedPreview(firstPreview), second: { code: "transaction.plan_stale", message: "stale" } },
    firstPreview,
    secondPreview,
  );
  assert.deepEqual(outcome, { winner: "first", losingCode: "transaction.plan_stale", requestId: "req-first" });
  const flipped = assertConcurrentOutcome(
    { first: { code: "transaction.balance_violation", message: "balance" }, second: appliedPreview(secondPreview) },
    firstPreview,
    secondPreview,
  );
  assert.equal(flipped.winner, "second");
  assert.equal(flipped.losingCode, "transaction.balance_violation");
});

test("two applied requests or a loser without an explicit definite code are rejected", () => {
  assert.throws(
    () => assertConcurrentOutcome({ first: appliedPreview(firstPreview), second: appliedPreview(secondPreview) }, firstPreview, secondPreview),
    /exactly one applied/,
  );
  assert.throws(
    () => assertConcurrentOutcome({ first: appliedPreview(firstPreview), second: null }, firstPreview, secondPreview),
    /not an explicit not-committed/,
  );
  assert.throws(
    () => assertConcurrentOutcome({ first: appliedPreview(firstPreview), second: { code: "plugin_timeout" } }, firstPreview, secondPreview),
    /not an explicit not-committed/,
  );
});

const model = { bindings: {
  heads: { tableId: "tbl_head", fields: {} },
  lines: { tableId: "tbl_line", fields: {} },
  allocations: { tableId: "tbl_alloc", fields: {} },
} };
const authority = {
  heads: [{ id: "headshared001", code: "SYNTHETIC-CONCURRENCY-01", budget: "10.00", lines: ["linefirst001"] }],
  lines: [{ id: "linefirst001", amount: "8.00", head: "headshared001", allocations: ["allocfirst01"] }],
  allocations: [{ id: "allocfirst01", amount: "8.00", line: "linefirst001" }],
};

test("the authority keeps occupancy 8 of budget 10 with no loser rows", () => {
  const proof = assertConcurrentAuthority(authority, model, { headId: "headshared001" }, {
    winner: "first",
    requests: { first: firstPreview, second: secondPreview },
  });
  assert.equal(proof.occupancy, "8.00");
  assert.equal(proof.remaining, "2.00");
});

test("a leftover loser row or a broken backlink fails the authority check", () => {
  const orphanLines = { ...authority, lines: [...authority.lines, { id: "linesecond01", amount: "8.00", head: "headshared001", allocations: [] }] };
  assert.throws(
    () => assertConcurrentAuthority(orphanLines, model, { headId: "headshared001" }, { winner: "first", requests: { first: firstPreview, second: secondPreview } }),
    /not exactly 1\/1\/1/,
  );
  const duplicatedBacklink = { ...authority, heads: [{ ...authority.heads[0], lines: ["linefirst001", "linefirst001"] }] };
  assert.throws(
    () => assertConcurrentAuthority(duplicatedBacklink, model, { headId: "headshared001" }, { winner: "first", requests: { first: firstPreview, second: secondPreview } }),
    /orphan or duplicate line/,
  );
});

const t01Model = { bindings: {
  heads: { tableId: "tbl_head", fields: {} },
  lines: { tableId: "tbl_line", fields: {} },
  allocations: { tableId: "tbl_alloc", fields: {} },
} };
const t01Opened = { datasetId: "ds-0001", versions: {
  tbl_head: { schemaRevision: "rev1", dataRevision: 4, clockPeriod: "2026-01-01", computed: null },
  tbl_line: { schemaRevision: "rev2", dataRevision: 5, clockPeriod: "2026-01-01", computed: null },
  tbl_alloc: { schemaRevision: "rev3", dataRevision: 6, clockPeriod: "2026-01-01", computed: null },
} };
const t01Authority = {
  heads: [{ id: "headrec0000001", budget: "10.00", lines: ["linerec0000001"] }],
  lines: [{ id: "linerec0000001", amount: "8.00", head: "headrec0000001", allocations: ["allocrec00001"] }],
  allocations: [{ id: "allocrec00001", amount: "8.00", line: "linerec0000001" }],
};

function t01Generated(rows) {
  return { outputId: "output-1", datasetId: "ds-0001", versions: t01Opened.versions, bytes: 2048,
    source: { templateVersion: "T01-synthetic-template-v1", dataVersion: "ds-0001" }, rows };
}

const t01Rows = [
  { entity: "heads", recordId: "headrec0000001", amount: "10.00", relation: "" },
  { entity: "lines", recordId: "linerec0000001", amount: "8.00", relation: "headrec0000001" },
  { entity: "allocations", recordId: "allocrec00001", amount: "8.00", relation: "linerec0000001" },
];

test("T01 generation reconciles against the observed public authority", () => {
  const byEntity = assertT01Generation(t01Generated(t01Rows), t01Opened, t01Model, t01Authority);
  assert.equal(byEntity.heads.recordId, "headrec0000001");
  assert.equal(byEntity.allocations.relation, "linerec0000001");
});

test("a swapped self-consistent id chain with identical amounts fails T01 generation", () => {
  const swapped = [
    { entity: "heads", recordId: "headimposter1", amount: "10.00", relation: "" },
    { entity: "lines", recordId: "lineimposter1", amount: "8.00", relation: "headimposter1" },
    { entity: "allocations", recordId: "allocimposter1", amount: "8.00", relation: "lineimposter1" },
  ];
  assert.throws(() => assertT01Generation(t01Generated(swapped), t01Opened, t01Model, t01Authority), /row ids differ/);
  const driftedAmount = [
    { ...t01Rows[0], amount: "9.00" },
    ...t01Rows.slice(1),
  ];
  assert.throws(() => assertT01Generation(t01Generated(driftedAmount), t01Opened, t01Model, t01Authority), /amounts differ/);
  const brokenRelation = [
    t01Rows[0],
    { ...t01Rows[1], relation: "headimposter1" },
    t01Rows[2],
  ];
  assert.throws(() => assertT01Generation(t01Generated(brokenRelation), t01Opened, t01Model, t01Authority), /relations differ/);
  assert.throws(
    () => assertT01Generation(t01Generated(t01Rows), { ...t01Opened, versions: { ...t01Opened.versions, tbl_alloc: { schemaRevision: "other", dataRevision: 9 } } }, t01Model, t01Authority),
    /versions differ/,
  );
});
