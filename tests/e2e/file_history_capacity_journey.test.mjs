import assert from "node:assert/strict";
import fs from "node:fs/promises";
import test from "node:test";
import { runFileHistoryCapacityJourney } from "./file_history_capacity_journey.mjs";

// Exercise the real journey with a ready DOM and delayed CDP attribute evidence.
function capacityJourney(t, { readyMs = 28000, proofMs = 2000, current = true } = {}) {
  let now = 0;
  const assertions = [];
  const timings = [];
  const attributes = [];
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
  function revision(selector) {
    const effective = selector.includes('data-revision-id="effective"');
    return {
      async waitFor(options) {
        assert.deepEqual(options, { state: "visible", timeout: 30000 });
        now = readyMs;
        if (effective && selector.includes('[aria-current="true"]') && !current) {
          throw new Error("effective revision not current");
        }
      },
      async getAttribute(name) {
        attributes.push(name);
        now += proofMs;
        return effective ? (current ? "true" : "false") : "first";
      },
    };
  }
  const control = {
    async waitFor() {}, async click() {}, async fill() {}, async count() { return 1; },
    async getAttribute() { return "old-handle"; },
    first() { return control; }, nth() { return control; }, filter() { return control; },
    getByRole() { return control; },
    locator(selector) {
      return selector.includes("data-revision-id=") ? revision(selector) : control;
    },
  };
  const page = {
    getByTestId() { return control; }, locator() { return control; },
    async waitForFunction() {}, async screenshot() {},
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
  return { assertions, timings, attributes,
    run: () => runFileHistoryCapacityJourney(page, recorder, runtime, helpers) };
}

test("S40 seals the ready first screen before delayed attribute evidence", async (t) => {
  const journey = capacityJourney(t);
  await journey.run();
  assert.deepEqual(journey.attributes, ["data-revision-id", "aria-current"]);
  const budget = journey.assertions.find(item => item.name.startsWith("full legal tree first screen"));
  assert.deepEqual(budget.details, { elapsedMs: 28000, harnessElapsedMs: 32000 });
  const timing = journey.timings.find(item => item.name === "file-history-capacity-tree-first-screen");
  assert.equal(timing.durationMs, 28000);
  assert.equal(timing.details.harnessElapsedMs, 32000);
});

test("S40 still rejects a first screen that takes 31 seconds", async (t) => {
  const journey = capacityJourney(t, { readyMs: 31000, proofMs: 0 });
  await assert.rejects(journey.run(), /full legal tree first screen/);
});

test("S40 still rejects a visible revision without current authority", async (t) => {
  const journey = capacityJourney(t, { current: false });
  await assert.rejects(journey.run(), /effective revision not current|legal all-formal chain/);
});
