import assert from "node:assert/strict";
import test from "node:test";

import {
  DashboardIdempotencyCache,
  DashboardPanelSnapshotLimitError,
  DASHBOARD_DRAFT_CONTRACT,
  assertDashboardPanelSnapshotWithinLimit,
  computeDashboardConfigHash,
  computeDashboardDraftFingerprint,
  computeDashboardRevision,
  dashboardClientPanelIds,
  dashboardDeletionTargets,
  isDashboardUuid,
  matchesCommittedDashboardDraft,
  matchesDeterministicDashboardCreation,
  projectedDashboardPanelCount,
  rewriteDashboardConfigPanelIds,
  stableDashboardUuid,
  validateDashboardDraftRequest,
  type DashboardDraftRequest,
} from "../dashboard-draft-helpers.ts";

const DASHBOARD_ID = "11111111-1111-4111-8111-111111111111";
const PANEL_ID = "22222222-2222-4222-8222-222222222222";

function request() {
  return {
    contract: DASHBOARD_DRAFT_CONTRACT,
    idempotencyKey: "33333333-3333-4333-8333-333333333333",
    dashboardId: DASHBOARD_ID,
    expectedRevision: null,
    dashboard: { name: "运营概览", note: null },
    panels: [{
      clientId: "panel-1",
      panelId: PANEL_ID,
      name: "收入",
      type: "bar-chart",
      showHeader: false,
      position: { x: 0, y: 0, width: 6, height: 4 },
      options: { collection: "orders" },
    }],
    deletedPanelIds: [],
    config: { refresh: { mode: "manual" }, filters: [] },
  };
}

test("validates a bounded 12-column dashboard draft", () => {
  const result = validateDashboardDraftRequest(request());
  assert.equal(result.ok, true);
});

test("rejects duplicate panels and positions outside the grid", () => {
  const duplicate = request();
  duplicate.panels.push({ ...duplicate.panels[0]! });
  const duplicateResult = validateDashboardDraftRequest(duplicate);
  assert.equal(duplicateResult.ok, false);
  if (!duplicateResult.ok) assert.match(duplicateResult.error, /unique/);

  const outside = request();
  outside.panels[0]!.position.x = 10;
  outside.panels[0]!.position.width = 4;
  const outsideResult = validateDashboardDraftRequest(outside);
  assert.equal(outsideResult.ok, false);
  if (!outsideResult.ok) assert.match(outsideResult.error, /12-column/);
});

test("rejects more than one hundred panels", () => {
  const oversized = request();
  oversized.panels = Array.from({ length: 101 }, (_, index) => ({
    ...oversized.panels[0]!,
    clientId: `panel-${index}`,
    panelId: `${String(index).padStart(8, "0")}-1111-4111-8111-111111111111`,
  }));
  const result = validateDashboardDraftRequest(oversized);
  assert.equal(result.ok, false);
  if (!result.ok) assert.match(result.error, /100/);
});

test("projects the final panel count against existing preserved panels", () => {
  const existing = new Set(Array.from({ length: 100 }, (_, index) => `p-${index}`));
  const fresh = { ...request().panels[0]!, panelId: null, clientId: "new" };
  assert.equal(projectedDashboardPanelCount(existing, [], [fresh]), 101);
  assert.equal(projectedDashboardPanelCount(existing, ["p-1"], [fresh]), 100);
  assert.equal(projectedDashboardPanelCount(existing, ["foreign"], [fresh]), 101);
});

test("revision and config hashes are stable across object key order", () => {
  const left = computeDashboardRevision({
    dashboard: { id: DASHBOARD_ID, name: "A" },
    panels: [{ id: PANEL_ID, options: { b: 2, a: 1 } }],
    config: { z: true, a: [2, 1] },
  });
  const right = computeDashboardRevision({
    config: { a: [2, 1], z: true },
    panels: [{ options: { a: 1, b: 2 }, id: PANEL_ID }],
    dashboard: { name: "A", id: DASHBOARD_ID },
  });
  assert.equal(left, right);
  assert.equal(
    computeDashboardConfigHash({ b: 2, a: 1 }),
    computeDashboardConfigHash({ a: 1, b: 2 }),
  );
});

test("revision changes when a panel option changes", () => {
  const original = request();
  const changed = request();
  changed.panels[0]!.options = { collection: "invoices" };
  const snapshot = (value: ReturnType<typeof request>) => ({
    dashboard: value.dashboard,
    panels: value.panels,
    config: value.config,
  });
  assert.notEqual(
    computeDashboardRevision(snapshot(original)),
    computeDashboardRevision(snapshot(changed)),
  );
});

function validatedRequest(): DashboardDraftRequest {
  const value = request();
  const result = validateDashboardDraftRequest(value);
  assert.equal(result.ok, true);
  return result.request;
}

test("rewrites only VibeTable-owned panel reference paths without mutating input", () => {
  const config = {
    globalFilters: [{
      key: "region",
      targetPanels: ["client-a", "already-real", 7],
      fieldBindings: { "client-a": "region", "already-real": "country" },
      sourcePanelId: "client-a",
      extension: { keep: true },
    }],
    interactions: [{
      sourcePanelId: "client-a",
      targetPanelIds: ["client-b", "already-real"],
      targetPanels: ["client-b"],
      extension: "kept",
    }],
    plugin: {
      sourcePanelId: "client-a",
      targetPanelIds: ["client-b"],
      nested: {
        globalFilters: [{ targetPanels: ["client-b"], future: 1 }],
      },
    },
  };
  const rewritten = rewriteDashboardConfigPanelIds(config, {
    "client-a": "panel-a",
    "client-b": "panel-b",
  });

  assert.deepEqual(rewritten.globalFilters, [{
    key: "region",
    targetPanels: ["panel-a", "already-real", 7],
    fieldBindings: { "panel-a": "region", "already-real": "country" },
    sourcePanelId: "client-a",
    extension: { keep: true },
  }]);
  assert.deepEqual(rewritten.interactions, [{
    sourcePanelId: "panel-a",
    targetPanelIds: ["panel-b", "already-real"],
    targetPanels: ["client-b"],
    extension: "kept",
  }]);
  assert.deepEqual((rewritten.plugin as Record<string, unknown>).targetPanelIds, ["client-b"]);
  assert.deepEqual(
    ((rewritten.plugin as any).nested.globalFilters[0]).targetPanels,
    ["client-b"],
  );
  assert.equal(config.interactions[0]!.sourcePanelId, "client-a");
});

test("fingerprints are canonical and cache rejects same key with another request", () => {
  const original = validatedRequest();
  const reordered = {
    ...original,
    config: { filters: [], refresh: { mode: "manual" } },
  };
  const changed = { ...original, dashboard: { ...original.dashboard, name: "Changed" } };
  const fingerprint = computeDashboardDraftFingerprint(original);
  assert.equal(fingerprint, computeDashboardDraftFingerprint(reordered));
  assert.notEqual(fingerprint, computeDashboardDraftFingerprint(changed));

  const cache = new DashboardIdempotencyCache(1);
  cache.set("user:key", fingerprint, { status: 200, body: { data: 1 } });
  assert.deepEqual(cache.lookup("user:key", fingerprint), {
    kind: "hit",
    result: { status: 200, body: { data: 1 } },
  });
  assert.deepEqual(cache.lookup("user:key", computeDashboardDraftFingerprint(changed)), {
    kind: "conflict",
  });
  cache.set("another", fingerprint, { status: 200, body: null });
  assert.deepEqual(cache.lookup("user:key", fingerprint), { kind: "miss" });
});

test("snapshot reads explicitly reject the one-hundred-and-first panel", () => {
  assert.doesNotThrow(() => assertDashboardPanelSnapshotWithinLimit(new Array(100)));
  assert.throws(
    () => assertDashboardPanelSnapshotWithinLimit(new Array(101)),
    DashboardPanelSnapshotLimitError,
  );
});

test("builds bounded, validated deletion targets for the route transaction", () => {
  assert.deepEqual(dashboardDeletionTargets({
    dashboard: { id: DASHBOARD_ID },
    panels: [{ id: PANEL_ID }],
    config: { id: DASHBOARD_ID },
  }, DASHBOARD_ID), {
    dashboardId: DASHBOARD_ID,
    panelIds: [PANEL_ID],
    configId: DASHBOARD_ID,
  });
  assert.throws(() => dashboardDeletionTargets({
    dashboard: { id: DASHBOARD_ID },
    panels: [{ id: "foreign" }],
    config: null,
  }, DASHBOARD_ID), /invalid panel id/);
  assert.throws(() => dashboardDeletionTargets({
    dashboard: { id: "44444444-4444-4444-8444-444444444444" },
    panels: [],
    config: null,
  }, DASHBOARD_ID), /does not match/);
});

test("recognizes only an exact deterministic creation after a process restart", () => {
  const draft = validatedRequest();
  draft.dashboardId = null;
  draft.panels[0]!.panelId = null;
  const dashboardId = stableDashboardUuid(draft.idempotencyKey);
  const ids = dashboardClientPanelIds(draft);
  const rewrittenConfig = rewriteDashboardConfigPanelIds(draft.config, ids);
  const snapshot = {
    dashboard: {
      id: dashboardId,
      name: draft.dashboard.name.trim(),
      note: null,
      icon: "dashboard",
      color: null,
    },
    panels: [{
      id: ids["panel-1"],
      dashboard: dashboardId,
      name: draft.panels[0]!.name,
      note: null,
      icon: null,
      color: null,
      type: draft.panels[0]!.type,
      show_header: false,
      position_x: 0,
      position_y: 0,
      width: 6,
      height: 4,
      options: { collection: "orders" },
    }],
    config: {
      id: dashboardId,
      status: "active",
      dashboard: dashboardId,
      config_version: 1,
      config: rewrittenConfig,
      content_hash: computeDashboardConfigHash(rewrittenConfig),
    },
  };
  assert.equal(matchesDeterministicDashboardCreation(snapshot, draft), true);
  assert.equal(isDashboardUuid(dashboardId), true);

  const changedSnapshot = structuredClone(snapshot);
  changedSnapshot.panels[0]!.name = "concurrent change";
  assert.equal(matchesDeterministicDashboardCreation(changedSnapshot, draft), false);

  const edit = { ...draft, dashboardId };
  assert.equal(matchesDeterministicDashboardCreation(snapshot, edit), false);
  assert.equal(isDashboardUuid("not-a-dashboard-id"), false);
});

test("recognizes an exact committed edit after a process restart without masking divergence", () => {
  const draft = validatedRequest();
  draft.expectedRevision = "a".repeat(64);
  const ids = dashboardClientPanelIds(draft);
  const rewrittenConfig = rewriteDashboardConfigPanelIds(draft.config, ids);
  const snapshot = {
    dashboard: {
      id: DASHBOARD_ID,
      name: draft.dashboard.name.trim(),
      note: null,
      icon: "dashboard",
      color: null,
    },
    panels: [{
      id: PANEL_ID,
      dashboard: DASHBOARD_ID,
      name: draft.panels[0]!.name,
      note: null,
      icon: null,
      color: null,
      type: draft.panels[0]!.type,
      show_header: false,
      position_x: 0,
      position_y: 0,
      width: 6,
      height: 4,
      options: { collection: "orders" },
    }, {
      id: "55555555-5555-4555-8555-555555555555",
      dashboard: DASHBOARD_ID,
      name: "preserved concurrent panel",
    }],
    config: {
      id: DASHBOARD_ID,
      status: "active",
      dashboard: DASHBOARD_ID,
      config_version: 8,
      config: rewrittenConfig,
      content_hash: computeDashboardConfigHash(rewrittenConfig),
    },
  };

  assert.equal(matchesCommittedDashboardDraft(snapshot, draft), true);
  const divergent = structuredClone(snapshot);
  divergent.config.config = { refresh: { mode: "interval" } };
  assert.equal(matchesCommittedDashboardDraft(divergent, draft), false);
  const archived = structuredClone(snapshot);
  archived.config.status = "archived";
  assert.equal(matchesCommittedDashboardDraft(archived, draft), false);

  const deletion = structuredClone(draft);
  deletion.deletedPanelIds = ["55555555-5555-4555-8555-555555555555"];
  assert.equal(matchesCommittedDashboardDraft(snapshot, deletion), false);
  assert.equal(matchesCommittedDashboardDraft({ ...snapshot, panels: snapshot.panels.slice(0, 1) }, deletion), true);
});
