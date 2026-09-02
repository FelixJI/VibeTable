import assert from "node:assert/strict";
import test from "node:test";

import { installWorkspaceV2MethodTerminalCaptureInPage } from "./workspace_v2_method_terminal.mjs";

const OPERATION_ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const WORKSPACE_ID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";

function globalWire(overrides = {}) {
  return {
    scope: "global",
    operationId: OPERATION_ID,
    sequence: 1,
    ...overrides,
  };
}

function workspaceWire(overrides = {}) {
  return {
    scope: "workspace",
    workspaceId: WORKSPACE_ID,
    sessionEpoch: 7,
    operationId: OPERATION_ID,
    sequence: 2,
    ...overrides,
  };
}

function request({
  requestId = "owner-1",
  method = "snapshot.export",
  wire = globalWire(),
  outerWire = wire,
} = {}) {
  return {
    type: "workspace.v2.request",
    requestId,
    wire: outerWire,
    payload: { method, params: {}, wire },
  };
}

function success(owner, overrides = {}) {
  const wire = owner.payload.wire;
  return {
    type: "workspace.v2.response",
    requestId: owner.requestId,
    wire,
    payload: {
      method: owner.payload.method,
      wire,
      ok: true,
      result: { status: "exported" },
      error: null,
    },
    ...overrides,
  };
}

function createWebview() {
  const listeners = new Set();
  const posted = [];
  function originalPostMessage(message) {
    posted.push(message);
    return `posted:${posted.length}`;
  }
  const webview = {
    addEventListener(type, listener) {
      if (type === "message") listeners.add(listener);
    },
    removeEventListener(type, listener) {
      if (type === "message") listeners.delete(listener);
    },
    postMessage: originalPostMessage,
  };
  return {
    dispatch(message) {
      for (const listener of [...listeners]) listener({ data: message });
    },
    listenerCount() {
      return listeners.size;
    },
    originalPostMessage,
    posted,
    webview,
  };
}

function installCapture(method = "snapshot.export", harness = createWebview()) {
  globalThis.window = { chrome: { webview: harness.webview } };
  installWorkspaceV2MethodTerminalCaptureInPage(method);
  return harness;
}

function capture() {
  return window.__vibetableE2EBridgeCapture;
}

test.afterEach(() => {
  delete globalThis.window;
});

test("captures a global success only after freezing its exact outbound owner", () => {
  const harness = installCapture();
  const owner = request();
  const wrapped = harness.webview.postMessage;

  assert.notEqual(wrapped, harness.originalPostMessage);
  assert.equal(harness.webview.postMessage(owner), "posted:1");
  assert.equal(capture().message, null);

  const terminal = success(owner);
  harness.dispatch(terminal);

  assert.deepEqual(capture().message, terminal);
  assert.equal(capture().error, null);
  assert.equal(harness.listenerCount(), 0);
  assert.equal(harness.webview.postMessage, harness.originalPostMessage);
});

test("captures a workspace success with its exact session-scoped wire", () => {
  const harness = installCapture("replica.releaseCache");
  const owner = request({
    requestId: "release-1",
    method: "replica.releaseCache",
    wire: workspaceWire(),
  });

  harness.webview.postMessage(owner);
  const terminal = success(owner);
  harness.dispatch(terminal);

  assert.deepEqual(capture().message, terminal);
  assert.equal(capture().error, null);
  assert.equal(harness.listenerCount(), 0);
  assert.equal(harness.webview.postMessage, harness.originalPostMessage);
});

test("ignores a competing request and terminal for the same method", () => {
  const harness = installCapture();
  const owner = request();
  const competing = request({
    requestId: "competing-1",
    wire: globalWire({
      operationId: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
      sequence: 3,
    }),
  });

  harness.webview.postMessage(owner);
  harness.webview.postMessage(competing);
  harness.dispatch(success(competing));
  assert.equal(capture().message, null);
  assert.equal(capture().error, null);

  const ownerTerminal = success(owner);
  harness.dispatch(ownerTerminal);
  assert.deepEqual(capture().message, ownerTerminal);
});

test("fails closed when the frozen requestId is reused for a different method", () => {
  const harness = installCapture();
  const owner = request();
  harness.webview.postMessage(owner);

  harness.webview.postMessage(request({
    requestId: owner.requestId,
    method: "snapshot.inspect",
    wire: owner.wire,
  }));

  assert.deepEqual(capture().error, {
    method: "snapshot.export",
    code: "CAPTURE_OUTBOUND_IDENTITY_MISMATCH",
    message: "workspace requestId changed method",
  });
  assert.equal(harness.listenerCount(), 0);
  assert.equal(harness.webview.postMessage, harness.originalPostMessage);
});

test("rejects non-exact or invalid outbound wire identities", () => {
  const cases = [
    request({ wire: globalWire({ extra: true }) }),
    request({ wire: globalWire({ operationId: "not-a-uuid" }) }),
    request({
      wire: globalWire({ operationId: "aaaaaaaa-aaaa-0aaa-8aaa-aaaaaaaaaaaa" }),
    }),
    request({
      wire: globalWire({ operationId: "aaaaaaaa-aaaa-4aaa-7aaa-aaaaaaaaaaaa" }),
    }),
    request({ wire: globalWire({ sequence: 1.5 }) }),
    request({ wire: workspaceWire({ extra: true }) }),
    request({ wire: workspaceWire({ workspaceId: "not-a-uuid" }) }),
    request({ wire: workspaceWire({ sessionEpoch: 0 }) }),
    request({ outerWire: globalWire({ sequence: 2 }) }),
  ];

  for (const invalid of cases) {
    const harness = installCapture();
    harness.webview.postMessage(invalid);
    assert.equal(capture().error?.code, "CAPTURE_OUTBOUND_IDENTITY_MISMATCH");
    assert.equal(capture().message, null);
    assert.equal(harness.listenerCount(), 0);
    assert.equal(harness.webview.postMessage, harness.originalPostMessage);
  }
});

test("fails closed when the frozen request changes its wire", () => {
  const harness = installCapture();
  const owner = request();
  harness.webview.postMessage(owner);

  harness.webview.postMessage(request({
    requestId: owner.requestId,
    wire: globalWire({ sequence: 4 }),
  }));

  assert.equal(capture().error?.code, "CAPTURE_OUTBOUND_IDENTITY_MISMATCH");
  assert.equal(capture().message, null);
  assert.equal(harness.listenerCount(), 0);
  assert.equal(harness.webview.postMessage, harness.originalPostMessage);
});

test("fails closed when the owner terminal changes its method or exact wire", () => {
  const owner = request();
  const missingOuter = success(owner);
  delete missingOuter.wire;
  const cases = [
    missingOuter,
    success(owner, { wire: globalWire({ sequence: 9 }) }),
    {
      ...success(owner),
      payload: {
        ...success(owner).payload,
        wire: globalWire({ extra: true }),
      },
    },
    {
      ...success(owner),
      payload: { ...success(owner).payload, method: "snapshot.inspect" },
    },
  ];

  for (const invalid of cases) {
    const harness = installCapture();
    harness.webview.postMessage(owner);
    harness.dispatch(invalid);
    assert.equal(capture().error?.code, "CAPTURE_TERMINAL_IDENTITY_MISMATCH");
    assert.equal(capture().message, null);
    assert.equal(harness.listenerCount(), 0);
    assert.equal(harness.webview.postMessage, harness.originalPostMessage);
  }
});

test("rejects uppercase UUIDs that the workspace v2 parser does not accept", () => {
  const harness = installCapture("replica.releaseCache");
  const owner = request({
    method: "replica.releaseCache",
    wire: workspaceWire({
      workspaceId: WORKSPACE_ID.toUpperCase(),
      operationId: OPERATION_ID.toUpperCase(),
    }),
  });
  harness.webview.postMessage(owner);
  assert.equal(capture().error?.code, "CAPTURE_OUTBOUND_IDENTITY_MISMATCH");
  assert.equal(capture().message, null);
});

test("captures an owner operation failure without leaking its message", () => {
  const harness = installCapture();
  const owner = request();
  harness.webview.postMessage(owner);

  harness.dispatch(JSON.stringify({
    type: "operation.failed",
    requestId: owner.requestId,
    payload: {
      code: "WORKSPACE_ERROR",
      message: "failed at C:\\Users\\secret\\snapshot.vtsnapshot",
    },
  }));

  assert.deepEqual(capture().message, {
    type: "operation.failed",
    requestId: owner.requestId,
    operation: owner.payload.method,
    code: "WORKSPACE_ERROR",
  });
  assert.equal(JSON.stringify(capture().message).includes("secret"), false);
  assert.equal(harness.listenerCount(), 0);
  assert.equal(harness.webview.postMessage, harness.originalPostMessage);
});

test("accepts exact optional failure identity and rejects changed identity", () => {
  const exactHarness = installCapture();
  const exactOwner = request();
  exactHarness.webview.postMessage(exactOwner);
  exactHarness.dispatch({
    type: "operation.failed",
    requestId: exactOwner.requestId,
    wire: exactOwner.wire,
    payload: {
      operation: exactOwner.payload.method,
      operationId: OPERATION_ID,
      wire: exactOwner.wire,
      code: "WORKSPACE_ERROR",
      message: "private detail",
    },
  });
  assert.equal(capture().message?.code, "WORKSPACE_ERROR");

  const invalidFailures = [
    { operation: "snapshot.inspect" },
    { operationId: "cccccccc-cccc-4ccc-8ccc-cccccccccccc" },
    { wire: globalWire({ sequence: 8 }) },
  ];
  for (const payloadOverride of invalidFailures) {
    const harness = installCapture();
    const owner = request();
    harness.webview.postMessage(owner);
    harness.dispatch({
      type: "operation.failed",
      requestId: owner.requestId,
      payload: {
        code: "WORKSPACE_ERROR",
        message: "private detail",
        ...payloadOverride,
      },
    });
    assert.equal(capture().error?.code, "CAPTURE_TERMINAL_IDENTITY_MISMATCH");
    assert.equal(capture().message, null);
    assert.equal(harness.listenerCount(), 0);
  }
});

test("ignores competing failures and terminals before or after owner selection", () => {
  const harness = installCapture();
  harness.dispatch({
    type: "operation.failed",
    requestId: "owner-1",
    payload: { operation: "snapshot.export", code: "EARLY", message: "early" },
  });
  const owner = request();
  harness.webview.postMessage(owner);
  harness.dispatch({
    type: "operation.failed",
    requestId: "competing-1",
    payload: { operation: "snapshot.export", code: "OTHER", message: "other" },
  });
  assert.equal(capture().message, null);
  assert.equal(capture().error, null);
  harness.dispatch(success(owner));
  assert.equal(capture().message?.requestId, owner.requestId);
});

test("replacement fails the old capture and the new installation closed", () => {
  const harness = createWebview();
  installCapture("snapshot.export", harness);
  const first = capture();

  assert.throws(
    () => installWorkspaceV2MethodTerminalCaptureInPage("snapshot.inspect"),
    /replaced an active owner/u,
  );

  assert.deepEqual(first.error, {
    method: "snapshot.export",
    code: "CAPTURE_REPLACED",
    message: "workspace method terminal capture ownership changed",
  });
  assert.equal(first.released, true);
  assert.equal(capture(), first);
  assert.equal(harness.listenerCount(), 0);
  assert.equal(harness.webview.postMessage, harness.originalPostMessage);
});

test("a competing request post failure does not terminate the frozen owner", () => {
  const harness = createWebview();
  const posted = harness.webview.postMessage;
  harness.webview.postMessage = (message) => {
    if (message.requestId === "competing-1") throw new Error("competing post failed");
    return posted(message);
  };
  const installedPostMessage = harness.webview.postMessage;
  installCapture("snapshot.export", harness);
  const owner = request();
  harness.webview.postMessage(owner);

  assert.throws(
    () => harness.webview.postMessage(request({ requestId: "competing-1" })),
    /competing post failed/u,
  );
  assert.equal(capture().error, null);
  const terminal = success(owner);
  harness.dispatch(terminal);
  assert.deepEqual(capture().message, terminal);
  assert.equal(harness.webview.postMessage, installedPostMessage);
});

test("explicit release reports an error and preserves a wrapper it no longer owns", () => {
  const harness = installCapture();
  const current = capture();
  function externalWrapper() {}
  harness.webview.postMessage = externalWrapper;

  current.release();

  assert.deepEqual(current.error, {
    method: "snapshot.export",
    code: "CAPTURE_RELEASED",
    message: "workspace method terminal capture was released before completion",
  });
  assert.equal(current.released, true);
  assert.equal(harness.listenerCount(), 0);
  assert.equal(harness.webview.postMessage, externalWrapper);
});
