import assert from "node:assert/strict";
import test from "node:test";

import {
  beginRawBridgeRequestInPage,
  requestLifecycleWorkspaceV2InPage,
  requestWorkspaceV2InPage,
} from "./bridge_raw_request.mjs";

test("async raw bridge requests reserve a formal workspace scope", () => {
  const posted = [];
  const listeners = [];
  const reserved = [];
  const webview = {
    postMessage(message) {
      posted.push(message);
    },
    addEventListener(type, listener) {
      if (type === "message") listeners.push(listener);
    },
    removeEventListener() {},
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
      requestType: "mutation.apply",
      requestPayload: { operations: [] },
      responseTypes: ["mutation.apply"],
    });

    assert.equal(reserved.length, 1);
    assert.equal(requestId, `e2e-${reserved[0]}`);
    assert.equal(posted[0].scope.sequence, 123);
    assert.equal(posted[0].scope.operationId, reserved[0]);

    listeners[0]({
      data: { type: "mutation.apply", requestId, payload: { status: "applied" } },
    });
    assert.equal(
      window.__vibetableE2ERawRequests[requestId].message.payload.status,
      "applied",
    );
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
