import assert from "node:assert/strict";
import test from "node:test";

import { installWritableWorkspaceBootstrapCaptureInPage } from "./workspace_bootstrap_capture.mjs";

function install(options, baselineRequests = []) {
  let handler = null;
  const diagnostics = {
    requests: [...baselineRequests],
    pending: {},
    recentCompleted: [],
    workspaceSession: null,
  };
  globalThis.window = {
    __vibetableE2EBridgeDiagnostics: diagnostics,
    chrome: {
      webview: {
        addEventListener(_type, value) {
          handler = value;
        },
        removeEventListener(_type, value) {
          if (handler === value) handler = null;
        },
      },
    },
  };
  installWritableWorkspaceBootstrapCaptureInPage(options);
  return {
    request(requestId, requestType) {
      diagnostics.requests.push({ requestId, requestType });
    },
    emit(message, { observeBootstrap = true } = {}) {
      const session = message?.payload?.session;
      if (message?.type === "workspace.v2.bootstrap" && observeBootstrap) {
        diagnostics.workspaceSession = {
          workspaceId: session?.workspaceId ?? null,
          sessionEpoch: session?.sessionEpoch ?? null,
        };
      }
      handler?.({ data: message });
    },
  };
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
      expectedLifecycleMethods: ["workspace.switch"],
    }),
    /expectedWorkspaceId must be a non-empty string/,
  );
});

test("captures only the expected workspace bootstrap", () => {
  const capture = install({
    minimumEpoch: 3,
    expectedWorkspaceId: "replica-workspace",
    expectedLifecycleMethods: [],
  });

  capture.emit(bootstrap("replica-workspace", 4));

  assert.equal(
    window.__vibetableE2EBridgeCapture.message.payload.session.workspaceId,
    "replica-workspace",
  );
  assert.equal(window.__vibetableE2EBridgeCapture.error, null);
});

test("requires lifecycle success after the matching bootstrap", () => {
  const capture = install({
    minimumEpoch: 3,
    expectedWorkspaceId: "replica-workspace",
    expectedLifecycleMethods: ["workspace.switch"],
  });
  capture.request("switch-1", "workspace.switch");
  const expectedBootstrap = bootstrap("replica-workspace", 4);

  capture.emit(expectedBootstrap);
  assert.equal(window.__vibetableE2EBridgeCapture.message, null);

  capture.emit({
    type: "workspace.v2.response",
    requestId: "switch-1",
    payload: {
      method: "workspace.switch",
      ok: true,
      result: {
        workspaceId: "replica-workspace",
        sessionEpoch: 4,
        state: "openedWritable",
      },
    },
  });

  assert.deepEqual(window.__vibetableE2EBridgeCapture.message, expectedBootstrap);
});

test("requires the matching bootstrap after lifecycle success", () => {
  const capture = install({
    minimumEpoch: 3,
    expectedWorkspaceId: "replica-workspace",
    expectedLifecycleMethods: ["workspace.switch"],
  });
  capture.request("switch-2", "workspace.switch");

  capture.emit({
    type: "workspace.v2.response",
    requestId: "switch-2",
    payload: {
      method: "workspace.switch",
      ok: true,
      result: {
        workspaceId: "replica-workspace",
        sessionEpoch: 4,
        state: "openedWritable",
      },
    },
  });

  assert.equal(window.__vibetableE2EBridgeCapture.message, null);
  const expectedBootstrap = bootstrap("replica-workspace", 4);
  capture.emit(expectedBootstrap);
  assert.deepEqual(window.__vibetableE2EBridgeCapture.message, expectedBootstrap);
});

test("does not complete on bootstrap before a correlated lifecycle failure", () => {
  const capture = install({
    minimumEpoch: 3,
    expectedWorkspaceId: "replica-workspace",
    expectedLifecycleMethods: ["workspace.switch"],
  });
  capture.request("switch-failed", "workspace.switch");

  capture.emit(bootstrap("replica-workspace", 4));
  assert.equal(window.__vibetableE2EBridgeCapture.message, null);

  capture.emit({
    type: "workspace.v2.response",
    requestId: "switch-failed",
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
  assert.equal(
    window.__vibetableE2EBridgeCapture.error.code,
    "workspace.activation_failed",
  );
});

test("ignores a rollback bootstrap and then surfaces the switch failure", () => {
  const capture = install({
    minimumEpoch: 3,
    expectedWorkspaceId: "replica-workspace",
    expectedLifecycleMethods: ["workspace.switch"],
  });
  capture.request("switch-rollback", "workspace.switch");

  capture.emit(bootstrap("source-workspace", 5));

  assert.equal(window.__vibetableE2EBridgeCapture.message, null);
  assert.equal(window.__vibetableE2EBridgeCapture.error, null);
  assert.deepEqual(window.__vibetableE2EBridgeCapture.unexpectedBootstraps, [{
    workspaceId: "source-workspace",
    sessionEpoch: 5,
  }]);

  capture.emit({
    type: "workspace.v2.response",
    requestId: "switch-rollback",
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

test("does not complete when diagnostics did not observe the captured bootstrap", () => {
  const capture = install({
    minimumEpoch: 3,
    expectedWorkspaceId: "replica-workspace",
    expectedLifecycleMethods: ["workspace.switch"],
  });
  capture.request("switch-unobserved", "workspace.switch");
  capture.emit(bootstrap("replica-workspace", 4), { observeBootstrap: false });
  capture.emit({
    type: "workspace.v2.response",
    requestId: "switch-unobserved",
    payload: {
      method: "workspace.switch",
      ok: true,
      result: {
        workspaceId: "replica-workspace",
        sessionEpoch: 4,
        state: "openedWritable",
      },
    },
  });

  assert.equal(window.__vibetableE2EBridgeCapture.message, null);
});

test("does not complete after diagnostics observed a rollback bootstrap", () => {
  const capture = install({
    minimumEpoch: 3,
    expectedWorkspaceId: "replica-workspace",
    expectedLifecycleMethods: ["workspace.switch"],
  });
  capture.request("switch-rolled-back", "workspace.switch");
  capture.emit(bootstrap("replica-workspace", 4));
  capture.emit(bootstrap("source-workspace", 5));
  capture.emit({
    type: "workspace.v2.response",
    requestId: "switch-rolled-back",
    payload: {
      method: "workspace.switch",
      ok: true,
      result: {
        workspaceId: "replica-workspace",
        sessionEpoch: 4,
        state: "openedWritable",
      },
    },
  });

  assert.equal(window.__vibetableE2EBridgeCapture.message, null);
  assert.deepEqual(window.__vibetableE2EBridgeCapture.unexpectedBootstraps, [{
    workspaceId: "source-workspace",
    sessionEpoch: 5,
  }]);
});

test("rejects an incomplete lifecycle session result", () => {
  const capture = install({
    minimumEpoch: 3,
    expectedWorkspaceId: "replica-workspace",
    expectedLifecycleMethods: ["workspace.switch"],
  });
  capture.request("switch-incomplete", "workspace.switch");
  capture.emit(bootstrap("replica-workspace", 4));
  capture.emit({
    type: "workspace.v2.response",
    requestId: "switch-incomplete",
    payload: {
      method: "workspace.switch",
      ok: true,
      result: { workspaceId: "replica-workspace" },
    },
  });

  assert.equal(window.__vibetableE2EBridgeCapture.message, null);
});

test("pairs snapshot import's non-session result with the observed bootstrap", () => {
  const capture = install({
    minimumEpoch: 3,
    expectedWorkspaceId: null,
    expectedLifecycleMethods: ["snapshot.import"],
  });
  capture.request("snapshot-import", "snapshot.import");
  capture.emit({
    type: "workspace.v2.response",
    requestId: "snapshot-import",
    payload: {
      method: "snapshot.import",
      ok: true,
      result: {
        operationId: "import-operation",
        snapshotId: "snapshot-id",
        sourceWorkspaceId: "source-workspace",
        sourceSnapshotId: "source-snapshot",
        state: "restoreRequired",
      },
    },
  });
  assert.equal(window.__vibetableE2EBridgeCapture.message, null);

  capture.emit(bootstrap("imported-workspace", 4));
  assert.equal(
    window.__vibetableE2EBridgeCapture.message.payload.session.workspaceId,
    "imported-workspace",
  );
});

test("ignores baseline and later competing responses with the same method", () => {
  const capture = install({
    minimumEpoch: 3,
    expectedWorkspaceId: "replica-workspace",
    expectedLifecycleMethods: ["workspace.switch"],
  }, [{ requestId: "old-switch", requestType: "workspace.switch" }]);
  capture.request("switch-owner", "workspace.switch");
  capture.request("switch-competing", "workspace.switch");
  capture.emit(bootstrap("replica-workspace", 4));

  for (const requestId of ["old-switch", "switch-competing"]) {
    capture.emit({
      type: "workspace.v2.response",
      requestId,
      payload: {
        method: "workspace.switch",
        ok: true,
        result: {
          workspaceId: "replica-workspace",
          sessionEpoch: 4,
          state: "openedWritable",
        },
      },
    });
  }
  assert.equal(window.__vibetableE2EBridgeCapture.message, null);

  capture.emit({
    type: "workspace.v2.response",
    requestId: "switch-owner",
    payload: {
      method: "workspace.switch",
      ok: true,
      result: {
        workspaceId: "replica-workspace",
        sessionEpoch: 4,
        state: "openedWritable",
      },
    },
  });
  assert.equal(
    window.__vibetableE2EBridgeCapture.message.payload.session.workspaceId,
    "replica-workspace",
  );
});
