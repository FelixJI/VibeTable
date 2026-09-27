import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";
import { JSDOM } from "../../desktop/web-grid/node_modules/jsdom/lib/api.js";

// The packaged runner has a CLI entry point. Exercise its small async seam
// without importing that entry point or starting a browser/Host.
const source = fs.readFileSync(new URL("./webview_product_scenarios.mjs", import.meta.url), "utf8");
const extract = (start, end) => source.slice(source.indexOf(start), source.indexOf(end));
const openNaturalAgingWorkspaceInPage = eval(`(${extract(
  "function openNaturalAgingWorkspaceInPage", "async function resumeNaturalRetentionAging",
)})`);
const activateRestartedWorkspaceSource = extract(
  "async function activateRestartedWorkspace", "async function main",
);

function restartedWorkspaceHarness({ start = "home", switches }) {
  const workspaceId = "11111111-1111-4111-8111-111111111111";
  const evidence = { switchCalls: 0, cardWaits: 0, cardClicks: 0, sessionWaits: 0 };
  let releaseSessionWait = null;
  const rawWorkspaceV2Request = async (_page, method, params) => {
    assert.equal(method, "workspace.switch");
    assert.deepEqual(params, { targetWorkspaceId: workspaceId, openMode: "writable" });
    const outcome = switches[Math.min(evidence.switchCalls, switches.length - 1)];
    evidence.switchCalls += 1;
    if (outcome instanceof Error) throw outcome;
    return outcome;
  };
  const hasActiveNaturalAgingWorkspaceSessionInPage = () => {
    evidence.sessionWaits += 1;
    return false;
  };
  const openNaturalAgingWorkspaceInPageForRestart = () => {
    evidence.cardClicks += 1;
    releaseSessionWait?.();
    return true;
  };
  const page = {
    getByTestId(id) {
      if (id === "workspace-center" || id === "home-view") {
        const visible = id === (start === "center" ? "workspace-center" : "home-view");
        return { waitFor: () => visible
          ? Promise.resolve()
          // A hidden element's visibility wait stays pending, never rejects:
          // the product race is won by whichever view is actually shown.
          : new Promise(() => {}) };
      }
      assert.equal(id, `workspace-delete-${workspaceId}`);
      return { waitFor: async () => {
        evidence.cardWaits += 1;
      } };
    },
    waitForFunction(callback, argument) {
      assert.equal(callback, hasActiveNaturalAgingWorkspaceSessionInPage);
      assert.equal(argument, undefined);
      return new Promise(resolve => {
        releaseSessionWait = resolve;
      });
    },
    async evaluate(callback, argument) {
      assert.equal(callback, openNaturalAgingWorkspaceInPage);
      assert.equal(argument, `workspace-delete-${workspaceId}`);
      return openNaturalAgingWorkspaceInPageForRestart();
    },
  };
  const activateRestartedWorkspace = eval(`(${activateRestartedWorkspaceSource})`);
  return {
    page,
    workspaceId,
    evidence,
    run: () => activateRestartedWorkspace(page, workspaceId),
  };
}

test("restarted workspace activation binds the UUID through the direct switch response", async () => {
  const switched = { result: { workspaceId: "11111111-1111-4111-8111-111111111111", sessionEpoch: 2, state: "openedWritable" } };
  const harness = restartedWorkspaceHarness({ switches: [switched] });
  const outcome = await harness.run();
  assert.equal(outcome.start, "home");
  assert.deepEqual(outcome.switched, switched);
  assert.equal(harness.evidence.switchCalls, 1);
  assert.equal(harness.evidence.sessionWaits, 0);
  assert.equal(harness.evidence.cardWaits, 0);
  assert.equal(harness.evidence.cardClicks, 0);
});

test("restarted workspace activation opens the UUID card once when no session exists yet", async () => {
  const noSession = new Error('workspace.switch failed closed: {"code":"workspace.session_required"}');
  const switched = { result: { workspaceId: "11111111-1111-4111-8111-111111111111", sessionEpoch: 1, state: "openedWritable" } };
  const harness = restartedWorkspaceHarness({ start: "center", switches: [noSession, switched] });
  const outcome = await harness.run();
  assert.equal(outcome.start, "center");
  assert.deepEqual(outcome.switched, switched);
  assert.equal(harness.evidence.switchCalls, 2);
  assert.equal(harness.evidence.cardWaits, 1);
  assert.equal(harness.evidence.cardClicks, 1);
});

test("restarted workspace activation propagates an unknown switch failure without retry", async () => {
  const failure = new Error('workspace.switch failed closed: {"code":"workspace.operation_failed"}');
  const harness = restartedWorkspaceHarness({ switches: [failure] });
  await assert.rejects(harness.run(), /workspace.operation_failed/);
  assert.equal(harness.evidence.switchCalls, 1);
  assert.equal(harness.evidence.cardWaits, 0);
  assert.equal(harness.evidence.cardClicks, 0);
});

async function resumeDom(mutate = () => {}) {
  const expected = {
    workspaceId: "workspace", tableId: "table", revision: "saved-revision",
    fields: { title: "title", status: "status", first: "first", second: "second" },
    state: {
      keyword: "preserved", density: "compact",
      columns: [
        { name: "title", width: 280, order: 0, frozen: true, visible: true },
        { name: "status", width: 100, order: 1, frozen: false, visible: false },
        { name: "second", width: 100, order: 2, frozen: false, visible: true },
        { name: "first", width: 100, order: 3, frozen: false, visible: true },
      ],
      sorts: [{ field: "title", direction: "asc" }],
      filters: [
        { field: "title", operator: "eq", value: "preserved" },
        { field: "status", operator: "eq", value: "", logic: "OR" },
      ],
    },
  };
  const dom = new JSDOM(`
    <button data-testid="nav-tables"></button>
    <div data-testid="view-keyword"><input value="preserved"></div>
    <div data-testid="view-query-controls" aria-busy="false"></div>
    <div data-testid="view-density">紧凑</div>
    <button data-testid="view-filter-trigger"></button>
    <div class="grid-wrapper density-compact" aria-busy="false">
      <div class="tabulator-header"><div class="tabulator-headers">
        <div class="tabulator-col tabulator-frozen" tabulator-field="title" aria-sort="ascending" data-width="280"><div class="tabulator-arrow"></div></div>
        <div class="tabulator-col" tabulator-field="status" style="display:none" data-width="100"></div>
        <div class="tabulator-col" tabulator-field="second" aria-sort="none" data-width="100"></div>
        <div class="tabulator-col" tabulator-field="first" aria-sort="none" data-width="100"></div>
      </div></div>
    </div>
    <div class="control-card--wide">
      <div class="filter-node"><span class="field-select">Title</span><span class="operator-select">等于</span><div class="value-input"><input value="preserved"></div></div>
      <div class="filter-node"><span class="joiner-select">或</span><span class="field-select">Status</span><span class="operator-select">等于</span><div class="value-input"><input value=""></div></div>
    </div>`);
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  globalThis.getComputedStyle = dom.window.getComputedStyle.bind(dom.window);
  // JSDOM supplies DOM semantics, not layout. Supply only browser geometry.
  dom.window.HTMLElement.prototype.getBoundingClientRect = function () {
    const hidden = this.style.display === "none";
    return { width: hidden ? 0 : Number(this.dataset.width ?? 100), height: hidden ? 0 : 30 };
  };
  mutate(document);
  class Locator {
    constructor(elements) { this.elements = elements; }
    first() { return new Locator(this.elements.slice(0, 1)); }
    locator(selector) { return new Locator(this.elements.flatMap(element => [...element.querySelectorAll(selector)])); }
    async inputValue() { return this.elements[0].value; }
    async innerText() { return this.elements[0].textContent; }
    async isVisible() { return (this.elements[0]?.getBoundingClientRect().width ?? 0) > 0; }
    async evaluateAll(callback) { return callback(this.elements); }
    async click() {}
  }
  const page = {
    locator: selector => new Locator([...document.querySelectorAll(selector.replaceAll(":visible", ""))]),
    getByTestId: id => new Locator([...document.querySelectorAll(`[data-testid="${id}"]`)]),
    evaluate: async (callback, argument) => callback(argument),
    waitForFunction: async (callback, argument) => assert.ok(callback(argument), "readiness condition"),
  };
  const recorder = { check(name, passed) { assert.ok(passed, name); } };
  const fs = { readFile: async () => JSON.stringify(expected) };
  const activateRestartedWorkspace = async () => ({
    start: "home",
    switched: {
      result: { workspaceId: expected.workspaceId, sessionEpoch: 2, state: "openedWritable" },
    },
  });
  const selectTable = async () => {};
  const rawBridgeRequest = async (_page, method) => {
    assert.equal(method, "gridState.get", "resume must not reapply Host state to repair the UI");
    return { type: method, payload: { revision: expected.revision, state: expected.state } };
  };
  const equivalentPresentationState = eval(`(${extract(
    "function equivalentPresentationState", "async function scenario13",
  )})`);
  // Command controls have their own UI/real-package assertions; this probe isolates presentation restoration.
  const resumeHostCommands = async () => {};
  const resumeHostPresentation = eval(`(${extract(
    "async function resumeHostPresentation", "function equivalentPresentationState",
  )})`);
  return resumeHostPresentation(page, recorder, "state.json");
}

test("Host restart accepts the restored DOM presentation", async () => {
  await resumeDom();
});

test("Host restart accepts subpixel width rounding", async () => {
  await resumeDom(document => {
    document.querySelector('[tabulator-field="title"]').dataset.width = "280.5";
  });
});

const brokenPresentations = {
  width: document => { document.querySelector('[tabulator-field="title"]').dataset.width = "210"; },
  "width beyond rounding": document => { document.querySelector('[tabulator-field="title"]').dataset.width = "282"; },
  frozen: document => { document.querySelector('[tabulator-field="title"]').classList.remove("tabulator-frozen"); },
  order: document => { document.querySelector(".tabulator-headers").append(document.querySelector('[tabulator-field="second"]')); },
  "frozen reordered column": document => { document.querySelector('[tabulator-field="second"]').classList.add("tabulator-frozen"); },
  sort: document => { document.querySelector('[tabulator-field="title"]').setAttribute("aria-sort", "none"); },
  density: document => { document.querySelector(".grid-wrapper").className = "grid-wrapper density-normal"; },
  operator: document => { document.querySelectorAll(".operator-select")[1].textContent = "为空"; },
  field: document => { document.querySelectorAll(".field-select")[1].textContent = "Title"; },
  hidden: document => { document.querySelector('[tabulator-field="status"]').style.display = "block"; },
  joiner: document => { document.querySelector(".joiner-select").textContent = "且"; },
  "empty equality": document => { document.querySelectorAll(".value-input input")[1].value = "changed"; },
};
for (const [name, mutate] of Object.entries(brokenPresentations)) {
  test(`Host restart rejects stale DOM ${name} even when Host state is correct`, async () => {
    await assert.rejects(resumeDom(mutate));
  });
}

// ---- S11 plugin host restart helpers ----

const verifiedProcessMembers = eval(`(${extract(
  "function verifiedProcessMembers", "function countProcessMembers",
)})`);
const countProcessMembers = eval(`(${extract(
  "function countProcessMembers", "async function waitForPackagedMemberExit",
)})`);
const waitForPackagedMemberExitSource = extract(
  "async function waitForPackagedMemberExit", "async function waitForBusyPluginMarker",
);

function memberSnapshot(pids, processName = "node.exe") {
  return { status: "completed", action: "observe-processes", members: pids.map(pid => ({
    pid, processName, identityVerified: true,
  })) };
}

test("waiting for a packaged member exit polls the exact pid until it disappears", async () => {
  const waitForPackagedMemberExit = eval(`(${waitForPackagedMemberExitSource})`);
  const snapshots = [
    memberSnapshot([4242, 9000]),
    memberSnapshot([9000]),
  ];
  const observations = [];
  const requestPackagedProcessKill = async (_runtime, action, reason) => {
    assert.equal(action, "observe-processes");
    assert.equal(reason, "verify member 4242 exited");
    observations.push(0);
    return snapshots[Math.min(observations.length - 1, snapshots.length - 1)];
  };
  const result = await waitForPackagedMemberExit({}, 4242, 2_000);
  assert.equal(result.pid, 4242);
  assert.equal(countProcessMembers(result.snapshot.members, "node.exe"), 1);
  assert.ok(!result.snapshot.members.some(member => member.pid === 4242));
  assert.equal(observations.length, 2);
});

test("waiting for a packaged member exit propagates an observation failure immediately", async () => {
  const waitForPackagedMemberExit = eval(`(${waitForPackagedMemberExitSource})`);
  let calls = 0;
  const requestPackagedProcessKill = async () => {
    calls += 1;
    throw new Error("Python orchestrator did not acknowledge the observe-processes fault request");
  };
  await assert.rejects(
    waitForPackagedMemberExit({}, 4242, 60_000),
    /acknowledge the observe-processes fault request/,
  );
  assert.equal(calls, 1);
});

test("waiting for a packaged member exit rejects a snapshot without verified identities", async () => {
  const waitForPackagedMemberExit = eval(`(${waitForPackagedMemberExitSource})`);
  let snapshot = null;
  const requestPackagedProcessKill = async () => snapshot;
  for (const invalid of [
    { status: "completed", action: "observe-processes" },
    {
      status: "completed",
      action: "observe-processes",
      members: [{ pid: 4242, processName: "node.exe", identityVerified: false }],
    },
  ]) {
    snapshot = invalid;
    await assert.rejects(
      waitForPackagedMemberExit({}, 4242, 60_000),
      /not a verified legal snapshot/,
    );
  }
});
