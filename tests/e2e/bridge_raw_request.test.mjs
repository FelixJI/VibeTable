import assert from "node:assert/strict";
import test from "node:test";

import { beginRawBridgeRequestInPage } from "./bridge_raw_request.mjs";

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
