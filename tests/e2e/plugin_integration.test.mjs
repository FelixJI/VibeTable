import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
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


const integrationSource = await fs.readFile(new URL("./plugin_integration.mjs", import.meta.url), "utf8");
// Exercise the actual complete scenario with controlled external I/O, following
// plugin_application_commit_unknown.test.mjs; no production statement is skipped.
const integrationBody = integrationSource.replace(/^import .*;\r?\n/gmu, "").replace(/^export /gmu, "");

for (const foregroundRequest of ["first", "second"]) {
  test(`complete integration persists both prepared-request approvals when ${foregroundRequest} is foreground`, async () => {
    const previews = {
      first: { ...firstPreview, status: "prepared" },
      second: { ...secondPreview, status: "prepared" },
    };
    const winner = previews[foregroundRequest];
    const winningLine = winner.newIds.lines.l;
    const winningAllocation = winner.newIds.allocations.a;
    const rows = {
      heads: [{ id: "headshared001", code: "SYNTHETIC-CONCURRENCY-01", budget: "10.00", lines: [winningLine] }],
      lines: [{ id: winningLine, amount: "8.00", head: "headshared001", allocations: [winningAllocation] }],
      allocations: [{ id: winningAllocation, amount: "8.00", line: winningLine }],
    };
    const fixtureModel = { status: "completed", bindings: Object.fromEntries(Object.entries(rows).map(([entity, values]) => [entity, {
      tableId: model.bindings[entity].tableId, fields: Object.fromEntries(Object.keys(values[0]).filter(key => key !== "id").map(key => [key, key])),
    }])) };
    const opened = { ...t01Opened, sources: Object.keys(rows).map(() => ({ filteredRows: 1 })) };
    const generated = t01Generated([
      { entity: "heads", recordId: rows.heads[0].id, amount: "10.00", relation: "" },
      { entity: "lines", recordId: winningLine, amount: "8.00", relation: "headshared001" },
      { entity: "allocations", recordId: winningAllocation, amount: "8.00", relation: winningLine },
    ]);
    const pending = { status: "completed", input: null, foreground: null,
      foregroundHwnd: foregroundRequest === "first" ? firstWindow.hwnd : secondWindow.hwnd,
      windows: [firstWindow, secondWindow].map(window => ({ ...window, win32OwnerRoot: 42, approveButton: { enabled: true } })),
    };
    const runtime = { controlsDir: "controls", evidenceDir: "evidence" };
    const written = new Map();
    const approvals = new Map();
    const approvalOrder = [];
    let currentAction, saveCount = 0, changed = false, captured = false;
    const seedPreview = { status: "prepared", requestId: "seed", newIds: { heads: { h: "headshared001" } } };
    const reopened = { artifactId: generated.outputId, source: generated.source };
    const saved = { state: "succeeded", committed: true, bytes: generated.bytes };
    const result = () => ({
      model: fixtureModel,
      configure: { status: "configured" },
      "concurrent-seed": seedPreview,
      submit: { ...seedPreview, status: "applied", receipt: { rows: [{ tableId: "tbl_head", recordId: "headshared001" }] } },
      "concurrent-prepare": previews,
      "concurrent-submit": { requests: previews,
        first: foregroundRequest === "first" ? appliedPreview(previews.first) : { code: "transaction.plan_stale" },
        second: foregroundRequest === "second" ? appliedPreview(previews.second) : { code: "transaction.plan_stale" },
      },
      dataset: opened,
      "t01-generate": generated,
      "t01-save": saveCount === 1 ? { state: "cancelled", committed: false } : saved,
      "t01-archive": { artifactId: generated.outputId, saved: { status: "succeeded" } },
      "t01-reopen": reopened,
    })[currentAction];
    const frame = { locator(selector) {
      const element = {
        async click() { currentAction = selector.slice(1); if (currentAction === "t01-save") saveCount++; },
        async fill() {}, async waitFor() {}, filter() { return element; },
        async innerText() { return selector === "#status" ? "succeeded" : JSON.stringify(result()); },
      };
      return element;
    } };
    const page = {
      getByTestId() { return { async click() {} }; },
      locator() { return { async waitFor() {} }; },
      async waitForFunction() {},
      async screenshot({ path: target }) { assert.equal(target, path.join(runtime.evidenceDir, "58-integration.png")); captured = true; },
    };
    const fakeFs = {
      async writeFile(target, text) { written.set(target, text); },
      async readFile(target) { assert.ok(written.has(target), target); return written.get(target); },
      async copyFile() {},
      async stat() { throw Object.assign(new Error("cancelled save has no file"), { code: "ENOENT" }); },
      async rename(source, target) {
        const request = JSON.parse(written.get(source));
        assert.equal(target, path.join(runtime.evidenceDir, "plugin-native-request.json"));
        let response = { ...pending, requestId: request.requestId };
        if (request.action === "confirm-exact") {
          const key = request.hwnd === firstWindow.hwnd ? "first" : "second";
          assert.equal(request.hwnd, key === "first" ? firstWindow.hwnd : secondWindow.hwnd);
          assert.deepEqual(request.expectEffects, pendingEffects(previews[key]));
          response = { requestId: request.requestId, status: "completed", action: request.action,
            hwnd: request.hwnd, win32OwnerRoot: 42, boundEffects: request.expectEffects,
            observation: { texts: [{ value: `complete observed approval ${key}` }] },
            input: "SendInput", foreground: { afterHwnd: request.hwnd },
          };
          approvals.set(key, response); approvalOrder.push(key);
        } else assert.equal(request.action, "confirm-pending");
        written.set(path.join(runtime.evidenceDir, "plugin-native-result.json"), JSON.stringify(response));
      },
    };
    const helpers = {
      async waitForShell() {},
      async createSimpleTable() { return { tableId: "attachments", field: { physicalName: "text" } }; },
      async createV2Field() { return { physicalName: "files" }; },
      async applyProductMutation(_page, tableId) { if (tableId === "tbl_head") changed = true; },
      async rawBridgeRequest(_page, type, payload) {
        if (payload.tableId === "attachments") return { payload: { rows: [{ id: "attachment", text: "SYNTHETIC-INTEGRATION-UNCHANGED" }] } };
        const entity = Object.keys(fixtureModel.bindings).find(key => fixtureModel.bindings[key].tableId === payload.tableId);
        if (type === "schema.getTable") return { payload: { fields: Object.keys(fixtureModel.bindings[entity].fields).map(key => ({ identity: { fieldId: key, physicalName: key } })) } };
        assert.equal(type, "query.page");
        return { payload: { rows: rows[entity].map(row => changed && entity === "heads" ? { ...row, code: "SYNTHETIC-INTEGRATION-CHANGED" } : row) } };
      },
    };
    const run = new Function("fs", "path", "installApplication", "openApplication", "nativePluginInput", "nativeModel", "picker",
      `${integrationBody}\nreturn runPluginIntegration;`)(fakeFs, path, async () => {}, async () => frame,
      async () => ({ status: "completed" }), async () => {}, async () => ({ status: "completed" }));
    await run(page, { check(name, passed, details) { assert.ok(passed, `${name}: ${JSON.stringify(details)}`); } }, runtime, helpers);
    assert.deepEqual(approvalOrder, [foregroundRequest, foregroundRequest === "first" ? "second" : "first"]);
    for (const target of [path.join(runtime.controlsDir, "58-t01-proof.json"), path.join(runtime.evidenceDir, "58-integration.json")]) {
      assert.ok(written.has(target), `missing final evidence ${target}`);
      const proof = JSON.parse(written.get(target));
      assert.deepEqual(proof.approvals, { firstApproval: approvals.get("first"), secondApproval: approvals.get("second") });
      assert.equal(proof.approvals.firstApproval.hwnd, firstWindow.hwnd);
      assert.equal(proof.approvals.secondApproval.hwnd, secondWindow.hwnd);
      assert.equal(proof.outcome.winner, foregroundRequest);
      assert.deepEqual(proof.pending.windows, pending.windows);
    }
    assert.equal(captured, true);
  });
}
