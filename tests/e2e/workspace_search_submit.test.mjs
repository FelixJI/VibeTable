import assert from "node:assert/strict";
import test from "node:test";
import vm from "node:vm";

import { submitWorkspaceSearch } from "./workspace_search_submit.mjs";
import { waitForWorkspaceSearchRebuildTerminal } from "./workspace_search_terminal.mjs";

function searchPage({ delayedAutomaticQuery = true, automaticCompletesBeforeSubmit = true } = {}) {
  const listeners = new Set();
  const posted = [];
  let query = "backup-mutated";
  let automatic;
  let submitted;
  const webview = {
    addEventListener(_type, listener) { listeners.add(listener); },
    removeEventListener(_type, listener) { listeners.delete(listener); },
    postMessage(message) { posted.push(message); },
  };
  const originalPostMessage = webview.postMessage;
  const context = vm.createContext({ window: { chrome: { webview } } });
  const evaluate = async (callback, input) => {
    context.input = input;
    return vm.runInContext(`(${callback.toString()})(input)`, context);
  };
  function request(requestId, value, sequence) {
    const wire = {
      scope: "workspace", workspaceId: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
      sessionEpoch: 7,
      operationId: sequence === 2 ? "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
        : "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
      sequence,
    };
    return { type: "workspace.v2.request", requestId, wire,
      payload: {
        method: "workspaceSearch.query", wire,
        params: { contractVersion: "1.0", query: value, logic: "and", filters: [],
          sorts: [{ field: "score", direction: "desc" }], scope: "current", cursor: null, limit: 50 },
      } };
  }
  function dispatch(owner) {
    const hits = owner.payload.params.query === "backup-original"
      ? [{ kind: "attachment", hitId: "original" }] : [];
    const message = { type: "workspace.v2.response", requestId: owner.requestId,
      wire: owner.wire, payload: { method: owner.payload.method, wire: owner.wire,
        ok: true, result: { generation: 17, hits } } };
    for (const listener of [...listeners]) listener({ data: message });
  }
  function startAutomatic() {
    // The host adapter queues actions with their existing params. Its old query
    // can reach postMessage after rebuild ready is visible and capture installed.
    if (delayedAutomaticQuery && !posted.includes(automatic)) {
      webview.postMessage(automatic);
      if (automaticCompletesBeforeSubmit) dispatch(automatic);
    }
  }
  async function submit() {
    startAutomatic();
    // Like the store's searching guard, Enter cannot submit during auto-search.
    if (delayedAutomaticQuery && !automaticCompletesBeforeSubmit) return;
    submitted = request("explicit-submit", query.trim(), 3);
    webview.postMessage(submitted);
    dispatch(submitted);
  }
  const input = {
    async fill(value) { query = value; },
    async inputValue() { return query; },
    async focus() { startAutomatic(); },
    async press(key) { assert.equal(key, "Enter"); await submit(); },
  };
  const index = {
    getAttribute(name) { return name === "data-state" ? "ready" : "17"; },
  };
  context.document = { querySelector: () => index };
  const page = {
    evaluate,
    async waitForFunction(predicate, value) {
      if (delayedAutomaticQuery && !automaticCompletesBeforeSubmit) dispatch(automatic);
      if (!await evaluate(predicate, value)) {
        const error = new Error("page.waitForFunction: Timeout");
        error.name = "TimeoutError";
        throw error;
      }
    },
    getByTestId(id) {
      if (id === "workspace-search-view") {
        return { locator: () => ({ evaluate: async (callback) => callback(index) }) };
      }
      if (id === "workspace-search-input") return { locator: () => input };
      assert.equal(id, "workspace-search-submit");
      return { waitFor: async () => {}, click: submit };
    },
  };
  return {
    page,
    async rebuildToReady() {
      automatic = request("rebuild-automatic", query, 2);
      return waitForWorkspaceSearchRebuildTerminal(page, { state: "building", generation: 16 });
    },
    assertReleased() {
      assert.equal(listeners.size, 0);
      assert.equal(webview.postMessage, originalPostMessage);
    },
    capturedRequestId() { return context.window.__vibetableE2EBridgeCapture.owner?.requestId; },
    posted,
  };
}

for (const keyboard of [false, true]) {
  test(`submission owns its query after rebuild; keyboard=${keyboard}`, async () => {
    const harness = searchPage();
    const terminal = await harness.rebuildToReady();
    assert.equal(terminal.generation, 17);
    await harness.page.getByTestId("workspace-search-input").locator("input").fill(" backup-original ");
    const result = await submitWorkspaceSearch(harness.page, { keyboard });
    assert.deepEqual(Array.from(result.hits, (hit) => hit.kind), ["attachment"]);
    assert.equal(harness.capturedRequestId(), "explicit-submit");
    assert.deepEqual(harness.posted.map((request) => request.requestId), ["rebuild-automatic", "explicit-submit"]);
    harness.assertReleased();
  });
}

test("Enter suppressed by an active automatic search cannot return that query's terminal", async () => {
  const harness = searchPage({ automaticCompletesBeforeSubmit: false });
  await harness.rebuildToReady();
  await harness.page.getByTestId("workspace-search-input").locator("input").fill("backup-original");
  await assert.rejects(submitWorkspaceSearch(harness.page, { keyboard: true }), /timed out/);
  assert.deepEqual(harness.posted.map((request) => request.requestId), ["rebuild-automatic"]);
  harness.assertReleased();
});

test("ordinary explicit search still captures its terminal and releases the observer", async () => {
  const harness = searchPage({ delayedAutomaticQuery: false });
  await harness.rebuildToReady();
  await harness.page.getByTestId("workspace-search-input").locator("input").fill("backup-original");
  const result = await submitWorkspaceSearch(harness.page);
  assert.equal(result.hits[0].kind, "attachment");
  harness.assertReleased();
});

test("an empty explicit-query result remains empty instead of borrowing another query's hits", async () => {
  const harness = searchPage();
  await harness.rebuildToReady();
  await harness.page.getByTestId("workspace-search-input").locator("input").fill("missing");
  const result = await submitWorkspaceSearch(harness.page);
  assert.equal(result.hits.length, 0);
  assert.equal(harness.capturedRequestId(), "explicit-submit");
  harness.assertReleased();
});