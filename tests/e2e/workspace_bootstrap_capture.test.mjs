import assert from "node:assert/strict";
import test from "node:test";

import { installWritableWorkspaceBootstrapCaptureInPage } from "./workspace_bootstrap_capture.mjs";

function install(options) {
  let handler = null;
  globalThis.window = {
    chrome: {
      webview: {
        addEventListener(_type, value) {
          handler = value;
        },
        removeEventListener() {},
      },
    },
  };
  installWritableWorkspaceBootstrapCaptureInPage(options);
  return (message) => handler({ data: message });
}

function bootstrap(workspaceId, sessionEpoch) {
  return {
    type: "workspace.v2.bootstrap",
    payload: {
      session: {
        workspaceId,
        sessionEpoch,
        state: "openedWritable",
        writable: true,
      },
    },
  };
}

test.afterEach(() => {
  delete globalThis.window;
});

test("rejects an empty expected workspace identity", () => {
  assert.throws(
    () => install({
      minimumEpoch: 3,
      expectedWorkspaceId: "",
      expectedFailureMethods: ["workspace.switch"],
    }),
    /expectedWorkspaceId must be a non-empty string/,
  );
});

test("captures only the expected workspace bootstrap", () => {
  const emit = install({
    minimumEpoch: 3,
    expectedWorkspaceId: "replica-workspace",
    expectedFailureMethods: ["workspace.switch"],
  });

  emit(bootstrap("replica-workspace", 4));

  assert.equal(
    window.__vibetableE2EBridgeCapture.message.payload.session.workspaceId,
    "replica-workspace",
  );
  assert.equal(window.__vibetableE2EBridgeCapture.error, null);
});

test("ignores a rollback bootstrap and then surfaces the switch failure", () => {
  const emit = install({
    minimumEpoch: 3,
    expectedWorkspaceId: "replica-workspace",
    expectedFailureMethods: ["workspace.switch"],
  });

  emit(bootstrap("source-workspace", 5));

  assert.equal(window.__vibetableE2EBridgeCapture.message, null);
  assert.equal(window.__vibetableE2EBridgeCapture.error, null);
  assert.deepEqual(window.__vibetableE2EBridgeCapture.unexpectedBootstraps, [{
    workspaceId: "source-workspace",
    sessionEpoch: 5,
  }]);

  emit({
    type: "workspace.v2.response",
    payload: {
      method: "workspace.switch",
      ok: false,
      error: {
        code: "workspace.activation_failed",
        message: "Replica activation failed.",
      },
    },
  });

  assert.equal(window.__vibetableE2EBridgeCapture.message, null);
  assert.deepEqual(window.__vibetableE2EBridgeCapture.error, {
    method: "workspace.switch",
    code: "workspace.activation_failed",
    message: "Replica activation failed.",
  });
});
