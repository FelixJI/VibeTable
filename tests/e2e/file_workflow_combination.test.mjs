import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import {
  COMBINATION_DOCX,
  COMBINATION_XLSX,
  FILE_WORKFLOW_FINAL_CALCULATION_ORACLE,
  fileWorkflowCombinationBeforeCorpus,
  fileWorkflowCombinationSources,
  fileWorkflowHasCurrentBinding,
} from "./file_workflow_combination.mjs";

test("S39 source corpus pins the S38 fixture plus its single visible edit", () => {
  const sources = fileWorkflowCombinationSources();
  assert.equal(sources.length, 201);
  assert.deepEqual(sources[0], { marker: "来源-000", contract: "合同甲", amount: 4 });
  assert.deepEqual(sources[199], { marker: "来源-199", contract: "合同乙", amount: 7 });
  assert.deepEqual(sources[200], { marker: "来源-200", contract: "其它合同", amount: 900 });
  assert.equal(new Set(sources.map(row => row.marker)).size, 201);
});

test("S39 final calculation oracle is an independent literal pin", () => {
  // Recompute from the frozen corpus with plain JavaScript, never through the
  // product oracle helper, so the pinned literals below stay independent.
  const sources = fileWorkflowCombinationSources();
  const recomputed = ["合同甲", "合同乙", "合同丙"].map(contract => {
    const matched = sources.filter(row => row.contract === contract);
    const sum = matched.reduce((total, row) => total + row.amount, 0);
    return { contract, matches: matched.length, sum, doubled: sum * 3, total: sum * 3 + 1 };
  });
  assert.deepEqual(recomputed, [
    { contract: "合同甲", matches: 199, sum: 994, doubled: 2982, total: 2983 },
    { contract: "合同乙", matches: 1, sum: 7, doubled: 21, total: 22 },
    { contract: "合同丙", matches: 0, sum: 0, doubled: 0, total: 1 },
  ]);
  assert.deepEqual(FILE_WORKFLOW_FINAL_CALCULATION_ORACLE, recomputed);
});

test("S39 rejects unknown corpus names and resolves the two static before fixtures", () => {
  assert.throws(() => fileWorkflowCombinationBeforeCorpus("document-sparse.xlsx"), /unknown S39/);
  assert.throws(() => fileWorkflowCombinationBeforeCorpus("document-format.xlsx"), /unknown S39/);
  const fixtures = [COMBINATION_DOCX, COMBINATION_XLSX].map(fileWorkflowCombinationBeforeCorpus);
  for (const fixture of fixtures) {
    assert.ok(fixture.endsWith("-before.docx") || fixture.endsWith("-before.xlsx"));
    assert.ok(fs.statSync(fixture).isFile(), `${fixture} must exist`);
    assert.ok(fs.statSync(fixture).size > 0);
  }
});

test("restored current search rejects stale bindings and a missing file index", () => {
  const restored = { kind: "file", canonicalId: "doc", sourceRevision: "restored" };
  const stale = { ...restored, sourceRevision: "old" };
  assert.equal(fileWorkflowHasCurrentBinding([restored], "doc", "restored"), true);
  assert.equal(fileWorkflowHasCurrentBinding([restored, stale], "doc", "restored"), false);
  assert.equal(fileWorkflowHasCurrentBinding([], "doc", "restored"), false);
});

function documentDiffBridge() {
  const listeners = new Set();
  const waiters = new Set();
  let waiting;
  const waitStarted = new Promise(resolve => { waiting = resolve; });
  const pump = () => {
    for (const waiter of [...waiters]) {
      if (waiter.predicate(waiter.argument)) {
        waiters.delete(waiter);
        waiter.resolve();
      }
    }
  };
  const webview = {
    postMessage() {},
    addEventListener(_type, listener) { listeners.add(listener); },
    removeEventListener(_type, listener) { listeners.delete(listener); },
  };
  const dispatch = message => {
    for (const listener of [...listeners]) listener({ data: JSON.stringify(message) });
    pump();
  };
  const page = {
    async evaluate(callback, argument) { return callback(argument); },
    async waitForFunction(predicate, argument, options) {
      assert.equal(options.timeout, 30_000);
      if (predicate(argument)) return;
      waiting();
      await new Promise(resolve => { waiters.add({ predicate, argument, resolve }); });
    },
    getByTestId(testId) {
      assert.equal(testId, "diff-close");
      return { async click() {
        webview.postMessage({ type: "document.diffCloseRequested", requestId: "close-current",
          payload: { sessionId: "retry-session" } });
        dispatch({ type: "document.diffCloseCompleted", requestId: "close-current",
          payload: { sessionId: "retry-session" } });
      } };
    },
  };
  return { webview, page, dispatch, waitStarted, waiters, listeners };
}

test("S14/S39 close waits for the retry session's actual page terminal", async () => {
  const { installDocumentDiffReadCaptureInPage, closeDocumentDiffThroughUi } =
    await import("./webview_product_scenarios.mjs");
  const bridge = documentDiffBridge();
  globalThis.window = { chrome: { webview: bridge.webview } };
  const previousPostMessage = bridge.webview.postMessage;
  try {
    installDocumentDiffReadCaptureInPage();
    installDocumentDiffReadCaptureInPage();
    assert.equal(bridge.listeners.size, 1);
    for (const [requestId, sessionId] of [["page-retry", "retry-session"], ["page-old", "old-session"]]) {
      bridge.webview.postMessage(JSON.stringify({ type: "document.diffPageRequested", requestId,
        payload: { sessionId, cursor: null, limit: 50 } }));
    }
    const closed = closeDocumentDiffThroughUi(bridge.page, "retry-session");
    const reached = await Promise.race([
      closed.then(() => "closed"), bridge.waitStarted.then(() => "waiting"),
    ]);
    assert.equal(reached, "waiting", "closeCompleted alone must not authorize the cleanup assertion");
    bridge.dispatch({ type: "document.diffPageCompleted", requestId: "page-old",
      payload: { outcome: "success" } });
    bridge.dispatch({ type: "document.diffPageCompleted", requestId: "unrelated-request",
      payload: { outcome: "failure", failure: "sessionExpired" } });
    bridge.dispatch({ type: "document.diffCompleted", requestId: "page-retry" });
    assert.equal(bridge.waiters.size, 1, "another session or terminal cannot release the active reader wait");
    bridge.dispatch({ type: "document.diffPageCompleted", requestId: "page-retry",
      payload: { outcome: "failure", failure: "sessionExpired" } });
    assert.equal((await closed).payload.sessionId, "retry-session");
    window.__vibetableE2EDocumentDiffReads.release();
    assert.equal(bridge.listeners.size, 0);
    assert.equal(bridge.webview.postMessage, previousPostMessage);
    assert.equal(window.__vibetableE2EDocumentDiffReads, undefined);
  } finally {
    window.__vibetableE2EDocumentDiffReads?.release();
    delete globalThis.window;
  }
});

test("S14/S39 close completes immediately when its page already terminated", async () => {
  const { installDocumentDiffReadCaptureInPage, closeDocumentDiffThroughUi } =
    await import("./webview_product_scenarios.mjs");
  const bridge = documentDiffBridge();
  globalThis.window = { chrome: { webview: bridge.webview } };
  try {
    installDocumentDiffReadCaptureInPage();
    bridge.webview.postMessage({ type: "document.diffPageRequested", requestId: "page-retry",
      payload: { sessionId: "retry-session" } });
    bridge.dispatch({ type: "document.diffPageCompleted", requestId: "page-retry" });
    bridge.webview.postMessage({ type: "document.diffPageRequested", requestId: "page-old",
      payload: { sessionId: "old-session" } });
    assert.equal((await closeDocumentDiffThroughUi(bridge.page, "retry-session")).payload.sessionId,
      "retry-session");
    assert.equal(bridge.waiters.size, 0);
    await assert.rejects(closeDocumentDiffThroughUi(bridge.page, "old-session"), /session identity/);
  } finally {
    window.__vibetableE2EDocumentDiffReads?.release();
    delete globalThis.window;
  }
});


test("S14/S39 close rejects an uncorrelated close and propagates a missing page terminal", async () => {
  const { installDocumentDiffReadCaptureInPage, closeDocumentDiffThroughUi } =
    await import("./webview_product_scenarios.mjs");
  const bridge = documentDiffBridge();
  globalThis.window = { chrome: { webview: bridge.webview } };
  const previousPostMessage = bridge.webview.postMessage;
  try {
    installDocumentDiffReadCaptureInPage();
    const actualClick = bridge.page.getByTestId;
    bridge.page.getByTestId = () => ({ async click() {
      bridge.dispatch({ type: "document.diffCloseCompleted", requestId: "unknown-close",
        payload: { sessionId: "retry-session" } });
    } });
    await assert.rejects(closeDocumentDiffThroughUi(bridge.page, "retry-session"), /session identity/);
    bridge.page.getByTestId = actualClick;
    bridge.webview.postMessage({ type: "document.diffPageRequested", requestId: "page-retry",
      payload: { sessionId: "retry-session" } });
    const timeout = Object.assign(new Error("page terminal timed out"), { name: "TimeoutError" });
    bridge.page.waitForFunction = async (predicate, argument, options) => {
      assert.equal(options.timeout, 30_000);
      if (!predicate(argument)) throw timeout;
    };
    await assert.rejects(closeDocumentDiffThroughUi(bridge.page, "retry-session"), error => error === timeout);
  } finally {
    window.__vibetableE2EDocumentDiffReads?.release();
    assert.equal(bridge.listeners.size, 0);
    assert.equal(bridge.webview.postMessage, previousPostMessage);
    delete globalThis.window;
  }
});
