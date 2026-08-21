import assert from "node:assert/strict";
import test from "node:test";

import { installWorkspaceV2MethodTerminalCaptureInPage } from "./workspace_v2_method_terminal.mjs";

function installCapture(method = "snapshot.export", diagnostics = null) {
  let listener = null;
  const webview = {
    addEventListener(type, next) {
      if (type === "message") listener = next;
    },
    removeEventListener(type, current) {
      if (type === "message" && listener === current) listener = null;
    },
  };
  globalThis.window = { chrome: { webview } };
  if (diagnostics !== null) {
    window.__vibetableE2EBridgeDiagnostics = diagnostics;
  }
  installWorkspaceV2MethodTerminalCaptureInPage(method);
  return {
    dispatch(message) {
      listener?.({ data: message });
    },
    captured() {
      return window.__vibetableE2EBridgeCapture.message;
    },
    listening() {
      return listener !== null;
    },
  };
}

test("captures only the exact correlated workspace.v2 method success", () => {
  const capture = installCapture();
  try {
    capture.dispatch({
      type: "workspace.v2.response",
      requestId: "competing",
      payload: { method: "snapshot.inspect", ok: true },
    });
    capture.dispatch({
      type: "workspace.v2.response",
      requestId: "",
      payload: { method: "snapshot.export", ok: true },
    });
    capture.dispatch({
      type: "operation.failed",
      requestId: "competing-failure",
      payload: { operation: "snapshot.inspect", code: "WORKSPACE_ERROR" },
    });
    assert.equal(capture.captured(), null);

    capture.dispatch({
      type: "workspace.v2.response",
      requestId: "export-1",
      payload: {
        method: "snapshot.export",
        ok: true,
        result: { privatePath: "C:\\Users\\secret\\snapshot.vtsnapshot" },
      },
    });
    assert.deepEqual(capture.captured(), {
      type: "workspace.v2.response",
      requestId: "export-1",
      payload: {
        method: "snapshot.export",
        ok: true,
        result: { privatePath: "C:\\Users\\secret\\snapshot.vtsnapshot" },
      },
    });
    assert.equal(capture.listening(), false);
  } finally {
    delete globalThis.window;
  }
});

test("captures the exact correlated operation failure without its message", () => {
  const capture = installCapture();
  try {
    capture.dispatch({
      type: "operation.failed",
      requestId: null,
      payload: { operation: "snapshot.export", code: "WORKSPACE_ERROR" },
    });
    assert.equal(capture.captured(), null);

    capture.dispatch(JSON.stringify({
      type: "operation.failed",
      requestId: "export-2",
      payload: {
        operation: "snapshot.export",
        code: "WORKSPACE_ERROR",
        message: "failed at C:\\Users\\secret\\snapshot.vtsnapshot",
      },
    }));
    assert.deepEqual(capture.captured(), {
      type: "operation.failed",
      requestId: "export-2",
      operation: "snapshot.export",
      code: "WORKSPACE_ERROR",
    });
    assert.equal(JSON.stringify(capture.captured()).includes("secret"), false);
    assert.equal(capture.listening(), false);
  } finally {
    delete globalThis.window;
  }
});

test("owns a router failure without operation through the outbound request ledger", () => {
  const capture = installCapture("snapshot.export", {
    requests: [
      { requestId: "export-router", requestType: "snapshot.export" },
      { requestId: "competing-router", requestType: "snapshot.inspect" },
    ],
    pending: {},
    recentCompleted: [],
  });
  try {
    capture.dispatch({
      type: "operation.failed",
      requestId: "competing-router",
      payload: {
        code: "WORKSPACE_ERROR",
        operation: null,
        message: "competing request failed",
      },
    });
    assert.equal(capture.captured(), null);

    capture.dispatch({
      type: "operation.failed",
      requestId: "export-router",
      payload: {
        code: "WORKSPACE_ERROR",
        operation: null,
        message: "failed at C:\\Users\\secret\\snapshot.vtsnapshot",
      },
    });
    assert.deepEqual(capture.captured(), {
      type: "operation.failed",
      requestId: "export-router",
      operation: "snapshot.export",
      code: "WORKSPACE_ERROR",
    });
    assert.equal(JSON.stringify(capture.captured()).includes("secret"), false);
    assert.equal(capture.listening(), false);
  } finally {
    delete globalThis.window;
  }
});

test("captures an exact negative response so the caller can fail closed immediately", () => {
  const capture = installCapture();
  try {
    capture.dispatch({
      type: "workspace.v2.response",
      requestId: "export-3",
      payload: { method: "snapshot.export", ok: false },
    });
    assert.deepEqual(capture.captured(), {
      type: "workspace.v2.response",
      requestId: "export-3",
      payload: { method: "snapshot.export", ok: false },
    });
    assert.equal(capture.listening(), false);
  } finally {
    delete globalThis.window;
  }
});
