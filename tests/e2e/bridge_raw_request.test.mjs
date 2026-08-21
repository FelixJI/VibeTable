import assert from "node:assert/strict";
import test from "node:test";

import {
  beginRawBridgeRequestInPage,
  isAppliedMutationResponse,
  postRawBridgeNotificationInPage,
  readRawBridgeRequestTerminalInPage,
  releaseRawBridgeRequestInPage,
  requestLifecycleWorkspaceV2InPage,
  requestWorkspaceV2InPage,
} from "./bridge_raw_request.mjs";

test("applied mutation response requires the authoritative response type", () => {
  assert.equal(isAppliedMutationResponse({
    type: "mutation.apply",
    payload: { status: "applied" },
  }), true);
  assert.equal(isAppliedMutationResponse({
    type: "unexpected.mutation.reply",
    payload: { status: "applied" },
  }), false);
});

test("raw bridge requests seal the first correlated inbound before a later success", () => {
  const posted = [];
  const listeners = [];
  const removedListeners = [];
  const reserved = [];
  const webview = {
    postMessage(message) {
      posted.push(message);
    },
    addEventListener(type, listener) {
      if (type === "message") listeners.push(listener);
    },
    removeEventListener(type, listener) {
      if (type === "message") removedListeners.push(listener);
    },
  };
  globalThis.window = {
    chrome: { webview },
    __vibetableE2EWorkspaceWirePort: {
      reserve(operationId) {
        reserved.push(operationId);
        return {
          scope: "workspace",
          workspaceId: "11111111-1111-4111-8111-111111111111",
          sessionEpoch: 7,
          operationId,
          sequence: 123,
        };
      },
    },
  };
  try {
    const requestId = beginRawBridgeRequestInPage({
      requestType: "query.page",
      requestPayload: { tableId: "tbl_records" },
    });

    assert.equal(reserved.length, 1);
    assert.equal(requestId, `e2e-${reserved[0]}`);
    assert.equal(posted[0].scope.sequence, 123);
    assert.equal(posted[0].scope.operationId, reserved[0]);

    const unknownTerminal = {
      type: "unexpected.query.reply",
      requestId,
      payload: { status: "unknown" },
    };
    listeners[0]({ data: unknownTerminal });
    listeners[0]({
      data: { type: "query.page", requestId, payload: { rows: [] } },
    });
    assert.deepEqual(
      readRawBridgeRequestTerminalInPage({ requestId }),
      unknownTerminal,
    );
    assert.deepEqual(removedListeners, [listeners[0]]);
    assert.equal(releaseRawBridgeRequestInPage({ requestId }), true);
    assert.equal(window.__vibetableE2ERawRequests, undefined);
  } finally {
    delete globalThis.window;
  }
});

test("raw UI notifications keep formal scope without inventing a correlated request", () => {
  const posted = [];
  const reserved = [];
  globalThis.window = {
    chrome: {
      webview: {
        postMessage(message) {
          posted.push(message);
        },
      },
    },
    __vibetableE2EWorkspaceWirePort: {
      reserve(operationId) {
        reserved.push(operationId);
        return {
          scope: "workspace",
          workspaceId: "11111111-1111-4111-8111-111111111111",
          sessionEpoch: 7,
          operationId,
          sequence: 124,
        };
      },
    },
  };
  try {
    postRawBridgeNotificationInPage({
      requestType: "table.updateCellRequested",
      requestPayload: { table: "tbl_orders" },
    });

    assert.equal(reserved.length, 1);
    assert.equal(posted.length, 1);
    assert.equal(posted[0].type, "table.updateCellRequested");
    assert.equal(posted[0].scope.operationId, reserved[0]);
    assert.equal(posted[0].requestId, undefined);
    assert.equal(window.__vibetableE2ERawRequests, undefined);
  } finally {
    delete globalThis.window;
  }
});

test("raw bridge request release removes a live listener and its registry entry", () => {
  const listeners = [];
  const removedListeners = [];
  globalThis.window = {
    chrome: {
      webview: {
        postMessage() {},
        addEventListener(type, listener) {
          if (type === "message") listeners.push(listener);
        },
        removeEventListener(type, listener) {
          if (type === "message") removedListeners.push(listener);
        },
      },
    },
    __vibetableE2EWorkspaceWirePort: {
      reserve(operationId) {
        return {
          scope: "workspace",
          workspaceId: "11111111-1111-4111-8111-111111111111",
          sessionEpoch: 7,
          operationId,
          sequence: 125,
        };
      },
    },
  };
  try {
    const requestId = beginRawBridgeRequestInPage({
      requestType: "query.page",
      requestPayload: { tableId: "tbl_records" },
    });
    assert.equal(readRawBridgeRequestTerminalInPage({ requestId }), null);

    assert.equal(releaseRawBridgeRequestInPage({ requestId }), true);
    assert.equal(releaseRawBridgeRequestInPage({ requestId }), false);
    assert.deepEqual(removedListeners, [listeners[0]]);
    assert.equal(window.__vibetableE2ERawRequests, undefined);

    listeners[0]({
      data: { type: "query.page", requestId, payload: { rows: [] } },
    });
    assert.equal(window.__vibetableE2ERawRequests, undefined);
  } finally {
    delete globalThis.window;
  }
});

test("raw bridge request setup releases ownership when postMessage throws", () => {
  const listeners = [];
  const removedListeners = [];
  globalThis.window = {
    chrome: {
      webview: {
        postMessage() {
          throw new Error("host bridge unavailable");
        },
        addEventListener(type, listener) {
          if (type === "message") listeners.push(listener);
        },
        removeEventListener(type, listener) {
          if (type === "message") removedListeners.push(listener);
        },
      },
    },
    __vibetableE2EWorkspaceWirePort: {
      reserve(operationId) {
        return {
          scope: "workspace",
          workspaceId: "11111111-1111-4111-8111-111111111111",
          sessionEpoch: 7,
          operationId,
          sequence: 126,
        };
      },
    },
  };
  try {
    assert.throws(
      () => beginRawBridgeRequestInPage({
        requestType: "query.page",
        requestPayload: { tableId: "tbl_records" },
      }),
      /host bridge unavailable/,
    );
    assert.deepEqual(removedListeners, [listeners[0]]);
    assert.equal(window.__vibetableE2ERawRequests, undefined);
  } finally {
    delete globalThis.window;
  }
});

test("workspace v2 probes use the formal serialized UI port", async () => {
  const requested = [];
  globalThis.window = {
    __vibetableE2EWorkspaceWirePort: {
      async request(action) {
        requested.push(action);
        return { policyRevision: 7 };
      },
    },
  };
  try {
    const reply = await requestWorkspaceV2InPage({
      method: "retention.get",
      params: {},
    });

    assert.deepEqual(requested, [{ method: "retention.get", params: {} }]);
    assert.deepEqual(reply, { result: { policyRevision: 7 } });
  } finally {
    delete globalThis.window;
  }
});

test("degraded lifecycle requests reserve a formal scope and accept the host reply", async () => {
  const posted = [];
  const listeners = [];
  const reserved = [];
  globalThis.window = {
    chrome: {
      webview: {
        postMessage(message) {
          posted.push(message);
        },
        addEventListener(type, listener) {
          if (type === "message") listeners.push(listener);
        },
        removeEventListener() {},
      },
    },
    __vibetableE2EWorkspaceWirePort: {
      reserve(operationId) {
        reserved.push(operationId);
        return {
          scope: "workspace",
          workspaceId: "11111111-1111-4111-8111-111111111111",
          sessionEpoch: 7,
          operationId,
          sequence: 321,
        };
      },
    },
  };
  try {
    const pending = requestLifecycleWorkspaceV2InPage({
      method: "workspace.close",
      params: { reason: "user" },
      timeoutMs: 5_000,
    });
    await new Promise((resolve) => setImmediate(resolve));

    assert.equal(reserved.length, 1);
    assert.equal(posted[0].type, "workspace.v2.request");
    assert.equal(posted[0].payload.method, "workspace.close");
    assert.equal(posted[0].payload.wire.sequence, 321);

    listeners[0]({
      data: {
        type: "workspace.v2.response",
        requestId: posted[0].requestId,
        payload: {
          method: "workspace.close",
          wire: posted[0].wire,
          ok: true,
          result: { state: "closed", workspaceId: null },
        },
      },
    });
    const reply = await pending;
    assert.equal(reply.result.state, "closed");
  } finally {
    delete globalThis.window;
  }
});
