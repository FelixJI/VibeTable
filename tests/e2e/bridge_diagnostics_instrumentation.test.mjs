import assert from "node:assert/strict";
import test from "node:test";

import { installBridgeDiagnosticsInPage } from "./bridge_diagnostics_instrumentation.mjs";

test("diagnostics observes workspace sequences without injecting or rewriting them", () => {
  const posted = [];
  const listeners = [];
  const webview = {
    postMessage(message) {
      posted.push(message);
    },
    addEventListener(type, listener) {
      if (type === "message") listeners.push(listener);
    },
  };
  globalThis.window = { chrome: { webview } };
  try {
    installBridgeDiagnosticsInPage();
    listeners[0]({
      data: {
        type: "workspace.v2.bootstrap",
        payload: {
          session: {
            workspaceId: "11111111-1111-4111-8111-111111111111",
            sessionEpoch: 7,
          },
        },
      },
    });
    webview.postMessage({
      type: "query.page",
      requestId: "e2e-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      payload: {},
    });
    assert.equal(posted[0].scope, undefined);
    assert.equal(window.__vibetableE2EBridgeDiagnostics.maxWorkspaceSequence, 0);
    const normal = {
      type: "events.reconcile",
      requestId: "hostbridge-reconcile",
      payload: {},
      scope: {
        scope: "workspace",
        workspaceId: "11111111-1111-4111-8111-111111111111",
        sessionEpoch: 7,
        operationId: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
        sequence: 2,
      },
    };

    webview.postMessage(normal);

    assert.equal(normal.scope.sequence, 2);
    assert.equal(
      window.__vibetableE2EBridgeDiagnostics.maxWorkspaceSequence,
      2,
    );
  } finally {
    delete globalThis.window;
  }
});

test("observes every workspace wire copy without normalizing the envelope", () => {
  const posted = [];
  const webview = {
    postMessage(message) {
      posted.push(message);
    },
    addEventListener() {},
  };
  globalThis.window = { chrome: { webview } };
  try {
    installBridgeDiagnosticsInPage();
    const outerWire = {
      scope: "workspace",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      sessionEpoch: 7,
      operationId: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
      sequence: 4,
    };
    const payloadWire = { ...outerWire, sequence: 9 };
    const envelope = {
      type: "workspace.v2.request",
      requestId: "e2e-cccccccc-cccc-4ccc-8ccc-cccccccccccc",
      wire: outerWire,
      payload: { method: "workspaceSearch.status", params: {}, wire: payloadWire },
    };

    webview.postMessage(envelope);

    assert.equal(posted.length, 1);
    assert.equal(outerWire.sequence, 4);
    assert.equal(payloadWire.sequence, 9);
    assert.equal(
      9,
      window.__vibetableE2EBridgeDiagnostics.maxWorkspaceSequence,
    );
  } finally {
    delete globalThis.window;
  }
});
