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

test("records fire-and-forget notifications and uncorrelated host failures", () => {
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
    webview.postMessage({
      type: "tableAdmin.createRequested",
      payload: { displayName: "Orders" },
    });
    listeners[0]({
      data: {
        type: "operation.failed",
        requestId: null,
        payload: {
          code: "WORKSPACE_ERROR",
          message: "Workspace operation failed at C:\\Users\\secret\\Orders.db.",
          operation: "table.selected",
        },
      },
    });

    assert.equal(posted.length, 1);
    assert.equal(window.__vibetableE2EBridgeDiagnostics.notifications.length, 1);
    assert.equal(
      window.__vibetableE2EBridgeDiagnostics.notifications[0].requestType,
      "tableAdmin.createRequested",
    );
    assert.deepEqual(
      window.__vibetableE2EBridgeDiagnostics.failures.map((failure) => ({
        requestId: failure.requestId,
        responseType: failure.responseType,
        code: failure.code,
        messageLength: failure.messageLength,
        operation: failure.operation,
      })),
      [{
        requestId: null,
        responseType: "operation.failed",
        code: "WORKSPACE_ERROR",
        messageLength: 56,
        operation: "table.selected",
      }],
    );
    const artifact = JSON.stringify(window.__vibetableE2EBridgeDiagnostics);
    assert.equal(artifact.includes("Orders"), false);
    assert.equal(artifact.includes("C:\\Users\\secret"), false);
  } finally {
    delete globalThis.window;
  }
});

test("keeps bridge diagnostic history bounded", () => {
  const listeners = [];
  const webview = {
    postMessage() {},
    addEventListener(type, listener) {
      if (type === "message") listeners.push(listener);
    },
  };
  globalThis.window = { chrome: { webview } };
  try {
    installBridgeDiagnosticsInPage();
    for (let index = 0; index < 250; index += 1) {
      listeners[0]({
        data: {
          type: "operation.failed",
          requestId: null,
          payload: {
            code: "WORKSPACE_ERROR",
            message: `private-${index}`,
            operation: "table.selected",
          },
        },
      });
    }

    const diagnostics = window.__vibetableE2EBridgeDiagnostics;
    assert.equal(diagnostics.failures.length, 200);
    assert.equal(JSON.stringify(diagnostics).includes("private-249"), false);
  } finally {
    delete globalThis.window;
  }
});

test("drops code and operation names outside the protocol diagnostic catalog", () => {
  const listeners = [];
  const webview = {
    postMessage() {},
    addEventListener(type, listener) {
      if (type === "message") listeners.push(listener);
    },
  };
  globalThis.window = { chrome: { webview } };
  try {
    installBridgeDiagnosticsInPage();
    listeners[0]({
      data: {
        type: "operation.failed",
        requestId: null,
        payload: {
          code: "CUSTOMER_SECRET",
          message: "hidden",
          operation: "Orders",
        },
      },
    });

    const [failure] = window.__vibetableE2EBridgeDiagnostics.failures;
    assert.equal(failure.code, null);
    assert.equal(failure.operation, null);
    assert.equal(JSON.stringify(failure).includes("CUSTOMER_SECRET"), false);
    assert.equal(JSON.stringify(failure).includes("Orders"), false);
  } finally {
    delete globalThis.window;
  }
});
