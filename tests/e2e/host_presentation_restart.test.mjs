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
const activateHostPresentationWorkspace = eval(`(${extract(
  "async function activateHostPresentationWorkspace", "function equivalentPresentationState",
)})`);

function activationPage(mode) {
  const workspaceId = "11111111-1111-4111-8111-111111111111";
  const evidence = { clicks: 0, attempts: 0, sessionReads: 0, targetWaits: 0 };
  const waiters = [];
  globalThis.window = { __vibetableE2EBridgeDiagnostics: { workspaceSession: null } };
  const activate = (id = workspaceId) => {
    window.__vibetableE2EBridgeDiagnostics.workspaceSession = { workspaceId: id, sessionEpoch: 1 };
    waiters.forEach(waiter => waiter());
  };
  class Button {
    disabled = false;
    click() { evidence.clicks += 1; activate(); }
  }
  globalThis.HTMLButtonElement = Button;
  const button = new Button();
  const card = { querySelector(selector) {
    assert.equal(selector, "button[aria-label]");
    return button;
  } };
  globalThis.document = { querySelector(selector) {
    assert.equal(selector, `[data-testid="workspace-delete-${workspaceId}"]`);
    return { closest(selector) { assert.equal(selector, ".workspace-card"); return card; } };
  } };
  const page = {
    getByTestId(id) {
      assert.equal(id, `workspace-delete-${workspaceId}`);
      return { waitFor: async () => undefined };
    },
    waitForFunction(callback, argument) {
      if (argument === workspaceId) evidence.targetWaits += 1;
      if (callback(argument)) return Promise.resolve();
      return new Promise(resolve => waiters.push(() => {
        if (callback(argument)) resolve();
      }));
    },
    async evaluate(callback, argument) {
      if (callback === openNaturalAgingWorkspaceInPage) {
        evidence.attempts += 1;
        // The card won the race and the previous session read returned null.
        // Auto-open completes before the in-page callback gets to the button.
        if (mode === "interleaved") activate();
        if (mode === "wrong-interleaved") activate("other-workspace");
        if (mode === "pending") button.disabled = true;
        const result = callback(argument);
        return result;
      }
      evidence.sessionReads += 1;
      const result = callback(argument);
      if (mode === "pending" && evidence.sessionReads === 2) queueMicrotask(activate);
      return result;
    },
  };
  if (mode === "already-active") activate();
  const recorder = { check(name, passed) { assert.ok(passed, name); } };
  return { page, recorder, workspaceId, evidence };
}

for (const mode of ["interleaved", "pending", "click", "already-active"]) {
  test(`Host restart activates the exact workspace: ${mode}`, async () => {
    const { page, recorder, workspaceId, evidence } = activationPage(mode);
    const session = await activateHostPresentationWorkspace(page, recorder, workspaceId);
    assert.equal(session.workspaceId, workspaceId);
    assert.equal(evidence.clicks, mode === "click" ? 1 : 0);
    assert.equal(evidence.attempts, mode === "already-active" ? 0 : 1);
    assert.equal(evidence.targetWaits, 1);
  });
}

test("Host restart rejects another UUID completing between read and open", async () => {
  const { page, recorder, workspaceId, evidence } = activationPage("wrong-interleaved");
  await assert.rejects(activateHostPresentationWorkspace(page, recorder, workspaceId));
  assert.equal(evidence.clicks, 0);
  assert.equal(evidence.attempts, 1);
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
  const activateHostPresentationWorkspace = async () => ({ workspaceId: expected.workspaceId });
  const selectTable = async () => {};
  const rawBridgeRequest = async (_page, method) => {
    assert.equal(method, "gridState.get", "resume must not reapply Host state to repair the UI");
    return { type: method, payload: { revision: expected.revision, state: expected.state } };
  };
  const equivalentPresentationState = eval(`(${extract(
    "function equivalentPresentationState", "async function scenario13",
  )})`);
  const resumeHostPresentation = eval(`(${extract(
    "async function resumeHostPresentation", "async function activateHostPresentationWorkspace",
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
