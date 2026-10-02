import assert from "node:assert/strict";
import fs from "node:fs/promises";
import test from "node:test";
import { runFileHistoryCapacityJourney, hasVisibleCapacityRevisionsInPage } from "./file_history_capacity_journey.mjs";

// Exercise the real journey while preserving the total 30s readiness budget.
function capacityJourney(t, { readyMs = 28000, current = true } = {}) {
  let now = 0;
  const assertions = [];
  const timings = [];
  const fixture = {
    name: "depth-4096", documents: 1, revisions: 4096,
    workspaceRoot: "workspace", manifestPath: "manifest.json",
    identity: { workspaceId: "workspace-1", sessionEpoch: 1 },
    selectedMultiDocumentId: "document-1", selectedMultiRelativePath: "chain.txt",
    selectedMultiRevisionCount: 4096,
    selectedMultiFirstRevisionId: "first", selectedMultiEffectiveRevisionId: "effective",
  };
  t.mock.method(performance, "now", () => now);
  t.mock.method(fs, "readFile", async (file) => JSON.stringify(file === "fixture.json"
    ? { fixtures: [fixture] } : { workspaceId: "workspace-1" }));
  t.mock.method(fs, "writeFile", async () => {});
  const control = {
    async waitFor() {}, async click() {}, async fill() {}, async count() { return 1; },
    async getAttribute() { return "old-handle"; },
    first() { return control; }, nth() { return control; }, filter() { return control; },
    getByRole() { return control; },
    locator() { return control; },
  };
  const page = {
    getByTestId() { return control; }, locator() { return control; },
    async waitForFunction(predicate, args, options) {
      if (predicate !== hasVisibleCapacityRevisionsInPage) return;
      assert.deepEqual(args, { firstId: "first", effectiveId: "effective" });
      assert.deepEqual(options, { timeout: 30000 });
      now = readyMs;
      if (!current) throw new Error("effective revision not current");
    },
    async screenshot() {},
  };
  const recorder = {
    check(name, passed, details = {}) {
      assertions.push({ name, passed, details });
      if (!passed) throw new Error(`assertion failed: ${name}`);
    },
  };
  const runtime = {
    controlsDir: "controls", evidenceDir: "evidence",
    recordUiTiming(name, durationMs, details) { timings.push({ name, durationMs, details }); },
  };
  const helpers = {
    metadataPath: "fixture.json",
    async replicaUiMethod(_page, _recorder, _method, activate) {
      await activate();
      return { result: { workspaceId: "workspace-1", status: "registered" } };
    },
    async activateWorkspaceThroughUi(_page, { activate }) {
      await activate();
      return { session: { workspaceId: "workspace-1", sessionEpoch: 2, writable: true } };
    },
    async rawWorkspaceV2Request(_page, method) {
      return { elapsedMs: 1, result: method === "fileHistory.readTree"
        ? { revisions: Array(4096).fill({}), effectiveRevisionId: "effective" }
        : { documents: [{ documentId: "document-1", relativePath: "chain.txt" }] } };
    },
  };
  return { assertions, timings,
    run: () => runFileHistoryCapacityJourney(page, recorder, runtime, helpers) };
}

test("S40 checks both exact revisions once within its original budget", async (t) => {
  const journey = capacityJourney(t);
  await journey.run();
  const budget = journey.assertions.find(item => item.name.startsWith("full legal tree first screen"));
  assert.deepEqual(budget.details, { elapsedMs: 28000, harnessElapsedMs: 28000 });
  const timing = journey.timings.find(item => item.name === "file-history-capacity-tree-first-screen");
  assert.equal(timing.durationMs, 28000);
  assert.equal(timing.details.harnessElapsedMs, 28000);
});

test("S40 still rejects a first screen that takes 31 seconds", async (t) => {
  const journey = capacityJourney(t, { readyMs: 31000 });
  await assert.rejects(journey.run(), /full legal tree first screen/);
});

test("S40 still rejects a visible revision without current authority", async (t) => {
  const journey = capacityJourney(t, { current: false });
  await assert.rejects(journey.run(), /effective revision not current|legal all-formal chain/);
});

test("capacity readiness requires both exact visible revisions and the effective marker", () => {
  const nodes = new Map();
  const first = '.tree-row[data-revision-id="first"]';
  const effective = '.tree-row[data-revision-id="last"][aria-current="true"]';
  const node = (width = 100, visibility = "visible") => ({
    getBoundingClientRect: () => ({ width, height: 62 }), visibility,
  });
  const originalDocument = globalThis.document;
  const originalStyle = globalThis.getComputedStyle;
  globalThis.document = { querySelectorAll: selector => {
    assert.equal(selector, '[data-testid="file-revision-tree"]');
    return [{ querySelectorAll: selector => nodes.has(selector)
      ? [nodes.get(selector)].flat() : [] }];
  } };
  globalThis.getComputedStyle = element => ({ visibility: element.visibility });
  const ready = () => hasVisibleCapacityRevisionsInPage({ firstId: "first", effectiveId: "last" });
  try {
    assert.equal(ready(), false);
    nodes.set(first, node());
    nodes.set('.tree-row[data-revision-id="last"]', node());
    assert.equal(ready(), false);
    nodes.set(effective, node());
    assert.equal(ready(), true);
    for (const selector of [first, effective]) {
      for (const hidden of [node(0), node(100, "hidden"), node(100, "collapse")]) {
        nodes.set(selector, hidden);
        assert.equal(ready(), false);
      }
      nodes.set(selector, [node(), node()]);
      assert.equal(ready(), false);
      nodes.set(selector, node());
    }
  } finally {
    if (originalDocument === undefined) delete globalThis.document;
    else globalThis.document = originalDocument;
    if (originalStyle === undefined) delete globalThis.getComputedStyle;
    else globalThis.getComputedStyle = originalStyle;
  }
});
