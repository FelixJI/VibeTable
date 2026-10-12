import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { waitForCapturedBridgeMessage } from "./bridge_capture_wait.mjs";

const RUNNER = new URL("./webview_product_scenarios.mjs", import.meta.url);
function compileAsyncHelper(source, name, dependencies) {
  const begin = source.indexOf(`async function ${name}(`);
  assert.ok(begin >= 0, `the product runner must still define ${name}`);
  const end = source.indexOf("\nasync function ", begin + 1);
  return new Function(...Object.keys(dependencies),
    `${source.slice(begin, end)}; return ${name};`)(...Object.values(dependencies));
}

function makeWebView() {
  const listeners = new Set();
  const webview = {
    addEventListener(type, listener) {
      assert.equal(type, "message");
      listeners.add(listener);
    },
    removeEventListener(type, listener) {
      listeners.delete(listener);
    },
  };
  return {
    webview,
    dispatch(data) {
      for (const listener of [...listeners]) listener({ data });
    },
  };
}

function makeSettlementPage() {
  return {
    async evaluate(callback, argument) {
      return callback(argument);
    },
    async waitForFunction(predicate, argument) {
      if (!predicate(argument)) {
        throw Object.assign(new Error("settlement capture did not complete"), {
          name: "TimeoutError",
        });
      }
    },
  };
}

function installSettlementWindow(bridge) {
  globalThis.window = {
    chrome: { webview: bridge.webview },
    // The real instrumentation only appends outbound renderer notifications;
    // an inbound dashboard.exportSettled never appears in this array.
    __vibetableE2EBridgeDiagnostics: { notifications: [] },
  };
}

async function compileSettlementHelpers() {
  const source = await readFile(RUNNER, "utf8");
  return {
    source,
    beginCapture: compileAsyncHelper(source, "beginBridgeMessageCapture", {}),
    waitForSettlement: compileAsyncHelper(source, "waitForDashboardExportSettlement",
      { waitForCapturedBridgeMessage }),
  };
}

test("S46 settlement is observed on the real inbound bridge, not outbound diagnostics", async () => {
  const { beginCapture, waitForSettlement } = await compileSettlementHelpers();
  const bridge = makeWebView();
  installSettlementWindow(bridge);
  try {
    await beginCapture(makeSettlementPage(), ["dashboard.exportSettled"]);
    bridge.dispatch({
      type: "dashboard.exportSettled",
      payload: { token: "token-csv", outcome: "saved" },
    });
    const settled = await waitForSettlement(makeSettlementPage(), 30_000, new Set());
    assert.equal(settled.token, "token-csv");
    assert.equal(settled.outcome, "saved");
    // The settlement arrived only as host-driven inbound traffic: the outbound
    // notification diagnostics that the previous wait polled stay empty.
    assert.deepEqual(window.__vibetableE2EBridgeDiagnostics.notifications, []);
  } finally {
    delete globalThis.window;
  }
});

test("a stale settlement token from an earlier export cannot satisfy the next wait", async () => {
  const { beginCapture, waitForSettlement } = await compileSettlementHelpers();
  const bridge = makeWebView();
  installSettlementWindow(bridge);
  try {
    const page = makeSettlementPage();
    const settledTokens = new Set();
    await beginCapture(page, ["dashboard.exportSettled"]);
    bridge.dispatch({
      type: "dashboard.exportSettled",
      payload: { token: "token-csv", outcome: "saved" },
    });
    assert.equal((await waitForSettlement(page, 30_000, settledTokens)).token, "token-csv");

    await beginCapture(page, ["dashboard.exportSettled"]);
    bridge.dispatch({
      type: "dashboard.exportSettled",
      payload: { token: "token-csv", outcome: "saved" },
    });
    await assert.rejects(
      () => waitForSettlement(page, 30_000, settledTokens),
      /dashboard export settlement was not a fresh saved terminal/,
    );
  } finally {
    delete globalThis.window;
  }
});

test("a failed settlement outcome is a terminal failure, never a pass", async () => {
  const { beginCapture, waitForSettlement } = await compileSettlementHelpers();
  const bridge = makeWebView();
  installSettlementWindow(bridge);
  try {
    const page = makeSettlementPage();
    await beginCapture(page, ["dashboard.exportSettled"]);
    bridge.dispatch({
      type: "dashboard.exportSettled",
      payload: { token: "token-failed", outcome: "failed", reason: "TEMPORARY_UNAVAILABLE" },
    });
    await assert.rejects(
      () => waitForSettlement(page, 30_000, new Set()),
      /dashboard export settlement was not a fresh saved terminal/,
    );
  } finally {
    delete globalThis.window;
  }
});

test("a missing settlement fails closed through the capture timeout", async () => {
  const { beginCapture, waitForSettlement } = await compileSettlementHelpers();
  const bridge = makeWebView();
  installSettlementWindow(bridge);
  try {
    await beginCapture(makeSettlementPage(), ["dashboard.exportSettled"]);
    await assert.rejects(
      () => waitForSettlement(makeSettlementPage(), 30_000, new Set()),
      /captured bridge response timed out/,
    );
  } finally {
    delete globalThis.window;
  }
});
