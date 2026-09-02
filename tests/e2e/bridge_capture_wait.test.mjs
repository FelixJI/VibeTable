import assert from "node:assert/strict";
import test from "node:test";

import {
  beginWorkspaceActivationCapture,
  captureCompletedInPage,
  waitForCapturedBridgeMessage,
} from "./bridge_capture_wait.mjs";
import { installWorkspaceV2MethodTerminalCaptureInPage } from "./workspace_v2_method_terminal.mjs";

const WORKSPACE_ID = "11111111-1111-4111-8111-111111111111";
const OPERATION_ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";

function makeWebView() {
  const listeners = new Set();
  const outbound = [];
  const webview = {
    postMessage(message) {
      outbound.push(message);
    },
    addEventListener(type, listener) {
      assert.equal(type, "message");
      listeners.add(listener);
    },
    removeEventListener(type, listener) {
      assert.equal(type, "message");
      listeners.delete(listener);
    },
  };
  return {
    webview,
    outbound,
    dispatch(data) {
      for (const listener of [...listeners]) listener({ data });
    },
    listenerCount() {
      return listeners.size;
    },
  };
}

function makeInPageBrowser(webview) {
  return {
    async evaluate(callback, argument) {
      return callback(argument);
    },
    async waitForFunction(predicate, argument) {
      if (!predicate(argument)) {
        throw Object.assign(new Error("capture did not complete"), { name: "TimeoutError" });
      }
    },
  };
}

function ownerRequest(wire = { scope: "global", operationId: OPERATION_ID, sequence: 1 }) {
  return {
    type: "workspace.v2.request",
    requestId: "request-a",
    wire,
    payload: { method: "workspace.open", params: {}, wire },
  };
}

function ownerTerminal(wire = { scope: "global", operationId: OPERATION_ID, sequence: 1 }) {
  return {
    type: "workspace.v2.response",
    requestId: "request-a",
    wire,
    payload: {
      method: "workspace.open",
      wire,
      ok: true,
      result: { workspaceId: WORKSPACE_ID, sessionEpoch: 7, state: "openedWritable" },
      error: null,
    },
  };
}

function writableBootstrap() {
  return {
    type: "workspace.v2.bootstrap",
    payload: {
      session: {
        contractVersion: "2.0",
        workspaceId: WORKSPACE_ID,
        sessionEpoch: 7,
        state: "openedWritable",
        openMode: "writable",
        writable: true,
        provisional: false,
        phase: "idle",
        errorCode: null,
      },
    },
  };
}

function databaseOpened() {
  return {
    type: "database.opened",
    payload: {
      projectKey: "local:11111111111141118111111111111111",
      projectRevision: "11111111111141118111111111111111:7",
    },
  };
}

async function runExactCapture(observations, outbound = ownerRequest()) {
  const bridge = makeWebView();
  globalThis.window = { chrome: { webview: bridge.webview } };
  try {
    const capture = await beginWorkspaceActivationCapture(
      makeInPageBrowser(bridge.webview),
      { method: "workspace.open" },
    );
    bridge.webview.postMessage(outbound);
    for (const observation of observations) bridge.dispatch(observation);
    return await capture.wait();
  } finally {
    delete globalThis.window;
  }
}

function makePage(captured, overrides = {}) {
  const calls = [];
  return {
    calls,
    page: {
      async waitForFunction(predicate, argument, options) {
        calls.push(["waitForFunction", predicate, argument, options]);
      },
      async evaluate(reader) {
        calls.push(["evaluate", reader]);
        return captured;
      },
      ...overrides,
    },
  };
}

test("waits once in the renderer and reads the captured message once", async () => {
  const message = { type: "workspace.v2.response", payload: { ok: true } };
  const { page, calls } = makePage({ message, error: null });

  const result = await waitForCapturedBridgeMessage(page, 60_000);

  assert.equal(result, message);
  assert.equal(calls.length, 2);
  assert.equal(calls[0][0], "waitForFunction");
  assert.equal(calls[0][1], captureCompletedInPage);
  assert.equal(calls[0][2], undefined);
  assert.deepEqual(calls[0][3], { polling: 50, timeout: 60_000 });
  assert.equal(calls[1][0], "evaluate");
});

test("renderer wait completes for either a message or an error", () => {
  globalThis.window = { __vibetableE2EBridgeCapture: { message: null, error: null } };
  try {
    assert.equal(captureCompletedInPage(), false);
    window.__vibetableE2EBridgeCapture.message = { type: "database.opened" };
    assert.equal(captureCompletedInPage(), true);
    window.__vibetableE2EBridgeCapture = {
      message: null,
      error: { method: "workspace.switch", code: "busy", message: "busy" },
    };
    assert.equal(captureCompletedInPage(), true);
  } finally {
    delete globalThis.window;
  }
});

test("surfaces a captured workspace failure", async () => {
  const { page } = makePage({
    message: null,
    error: {
      method: "workspace.switch",
      code: "workspace_busy",
      message: "workspace is busy",
    },
  });

  await assert.rejects(
    waitForCapturedBridgeMessage(page),
    /captured workspace\.switch failure: workspace_busy: workspace is busy/,
  );
});

test("keeps the stable timeout error without polling the page", async () => {
  const timeout = Object.assign(new Error("playwright details"), { name: "TimeoutError" });
  let releases = 0;
  const { page, calls } = makePage(null, {
    async waitForFunction() {
      calls.push(["waitForFunction"]);
      throw timeout;
    },
    async evaluate(callback, argument) {
      calls.push(["evaluate", argument]);
      return callback(argument);
    },
  });

  globalThis.window = {
    __vibetableE2EBridgeCapture: {
      id: undefined,
      release() { releases += 1; },
    },
  };
  try {
    await assert.rejects(
      waitForCapturedBridgeMessage(page, 123),
      /captured bridge response timed out/,
    );
    assert.deepEqual(calls, [["waitForFunction"], ["evaluate", undefined]]);
    assert.equal(releases, 1);
  } finally {
    delete globalThis.window;
  }
});

test("does not rewrite non-timeout Playwright failures", async () => {
  const closed = new Error("page closed");
  const { page } = makePage(null, {
    async waitForFunction() {
      throw closed;
    },
  });

  await assert.rejects(waitForCapturedBridgeMessage(page), (error) => error === closed);
});

test("activation refuses to replace an active method capture without an id", async () => {
  const bridge = makeWebView();
  const originalPostMessage = bridge.webview.postMessage;
  globalThis.window = { chrome: { webview: bridge.webview } };
  try {
    installWorkspaceV2MethodTerminalCaptureInPage("snapshot.export");
    const methodCapture = window.__vibetableE2EBridgeCapture;

    await assert.rejects(
      beginWorkspaceActivationCapture(
        makeInPageBrowser(bridge.webview),
        { method: "workspace.open" },
      ),
      /replaced an unaddressable active owner/u,
    );

    assert.equal(window.__vibetableE2EBridgeCapture, methodCapture);
    assert.deepEqual(methodCapture.error, {
      method: "snapshot.export",
      code: "CAPTURE_REPLACED",
      message: "workspace activation capture ownership changed",
    });
    assert.equal(methodCapture.released, true);
    assert.equal(bridge.listenerCount(), 0);
    assert.equal(bridge.webview.postMessage, originalPostMessage);
  } finally {
    delete globalThis.window;
  }
});

test("captures one activation owner and correlates readiness in any inbound order", async () => {
  const bridge = makeWebView();
  const originalPostMessage = bridge.webview.postMessage;
  globalThis.window = { chrome: { webview: bridge.webview } };
  try {
    const capture = await beginWorkspaceActivationCapture(
      makeInPageBrowser(bridge.webview),
      { method: "workspace.open" },
    );

    bridge.webview.postMessage(ownerRequest());
    bridge.dispatch(JSON.stringify(databaseOpened()));
    bridge.dispatch(writableBootstrap());
    bridge.dispatch(ownerTerminal());

    assert.deepEqual(await capture.wait(123), databaseOpened());
    assert.equal(bridge.webview.postMessage, originalPostMessage);
    assert.equal(bridge.listenerCount(), 0);
  } finally {
    delete globalThis.window;
  }
});

test("fails closed when the outbound outer and payload wire copies disagree", async () => {
  const bridge = makeWebView();
  globalThis.window = { chrome: { webview: bridge.webview } };
  try {
    const capture = await beginWorkspaceActivationCapture(
      makeInPageBrowser(bridge.webview),
      { method: "workspace.open" },
    );
    const request = ownerRequest();
    request.payload.wire = { ...request.wire, sequence: 2 };

    bridge.webview.postMessage(request);

    await assert.rejects(
      capture.wait(),
      /CAPTURE_OUTBOUND_IDENTITY_MISMATCH: workspace activation request has inconsistent wire identity/,
    );
    assert.equal(bridge.listenerCount(), 0);
  } finally {
    delete globalThis.window;
  }
});

test("seals the first owner terminal instead of accepting a conflicting duplicate", async () => {
  const bridge = makeWebView();
  globalThis.window = { chrome: { webview: bridge.webview } };
  try {
    const capture = await beginWorkspaceActivationCapture(
      makeInPageBrowser(bridge.webview),
      { method: "workspace.open" },
    );
    bridge.webview.postMessage(ownerRequest());
    bridge.dispatch(ownerTerminal());
    const duplicate = ownerTerminal();
    duplicate.payload.result.sessionEpoch = 8;
    bridge.dispatch(duplicate);

    await assert.rejects(
      capture.wait(),
      /CAPTURE_TERMINAL_DUPLICATE: workspace activation owner returned conflicting terminals/,
    );
  } finally {
    delete globalThis.window;
  }
});

test("fails as soon as the owner terminal and bootstrap identify different sessions", async () => {
  const bridge = makeWebView();
  globalThis.window = { chrome: { webview: bridge.webview } };
  try {
    const capture = await beginWorkspaceActivationCapture(
      makeInPageBrowser(bridge.webview),
      { method: "workspace.open" },
    );
    bridge.webview.postMessage(ownerRequest());
    bridge.dispatch(ownerTerminal());
    const bootstrap = writableBootstrap();
    bootstrap.payload.session.sessionEpoch = 8;
    bridge.dispatch(bootstrap);

    await assert.rejects(
      capture.wait(),
      /CAPTURE_SESSION_IDENTITY_MISMATCH: workspace terminal and bootstrap session identity differ/,
    );
  } finally {
    delete globalThis.window;
  }
});

test("correlates every readiness ordering without accepting another same-method request", async (t) => {
  const competitor = ownerTerminal({
    scope: "global",
    operationId: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
    sequence: 2,
  });
  competitor.requestId = "request-b";
  const observations = [ownerTerminal(), writableBootstrap(), databaseOpened()];
  const orderings = [
    [0, 1, 2], [0, 2, 1], [1, 0, 2], [1, 2, 0], [2, 0, 1], [2, 1, 0],
  ];
  for (const ordering of orderings) {
    await t.test(ordering.join("-"), async () => {
      const ordered = ordering.map(index => observations[index]);
      ordered.splice(1, 0, competitor);
      assert.deepEqual(
        await runExactCapture(ordered, JSON.stringify(ownerRequest())),
        databaseOpened(),
      );
    });
  }
});

test("accepts the exact provisional readiness quartet", async () => {
  const terminal = ownerTerminal();
  terminal.payload.result.state = "openedProvisional";
  const bootstrap = writableBootstrap();
  Object.assign(bootstrap.payload.session, {
    state: "openedProvisional",
    openMode: "provisional",
    writable: false,
    provisional: true,
  });

  assert.deepEqual(
    await runExactCapture([bootstrap, databaseOpened(), terminal]),
    databaseOpened(),
  );
});

test("ignores a transitional bootstrap before the ready session", async () => {
  const transitional = writableBootstrap();
  Object.assign(transitional.payload.session, {
    state: "opening",
    writable: false,
    phase: "binding",
  });

  assert.deepEqual(
    await runExactCapture([
      transitional,
      ownerTerminal(),
      writableBootstrap(),
      databaseOpened(),
    ]),
    databaseOpened(),
  );
});

test("fails closed for owner operation, mode, and project identity contradictions", async (t) => {
  const wrongOperation = ownerTerminal({
    scope: "global",
    operationId: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
    sequence: 1,
  });
  const wrongMode = writableBootstrap();
  wrongMode.payload.session.provisional = true;
  const wrongWorkspace = writableBootstrap();
  wrongWorkspace.payload.session.workspaceId = "22222222-2222-4222-8222-222222222222";
  const wrongProject = databaseOpened();
  wrongProject.payload.projectRevision = "11111111111141118111111111111111:8";
  const cases = [
    ["operation", [wrongOperation], /CAPTURE_TERMINAL_IDENTITY_MISMATCH/],
    ["mode", [ownerTerminal(), wrongMode], /CAPTURE_SESSION_MODE_MISMATCH/],
    ["workspace", [ownerTerminal(), wrongWorkspace], /CAPTURE_SESSION_IDENTITY_MISMATCH/],
    [
      "project",
      [ownerTerminal(), writableBootstrap(), wrongProject],
      /CAPTURE_PROJECT_IDENTITY_MISMATCH/,
    ],
  ];
  for (const [name, messages, expected] of cases) {
    await t.test(name, async () => assert.rejects(runExactCapture(messages), expected));
  }
});

test("releases the exact wrapper and listener when readiness times out", async () => {
  const bridge = makeWebView();
  const originalPostMessage = bridge.webview.postMessage;
  globalThis.window = { chrome: { webview: bridge.webview } };
  try {
    const capture = await beginWorkspaceActivationCapture(
      makeInPageBrowser(bridge.webview),
      { method: "workspace.open" },
    );
    bridge.webview.postMessage(ownerRequest());

    await assert.rejects(capture.wait(1), /captured bridge response timed out/);
    assert.equal(bridge.webview.postMessage, originalPostMessage);
    assert.equal(bridge.listenerCount(), 0);
  } finally {
    delete globalThis.window;
  }
});

test("external release wakes an incomplete readiness wait with a stable failure", async () => {
  const bridge = makeWebView();
  globalThis.window = { chrome: { webview: bridge.webview } };
  try {
    const capture = await beginWorkspaceActivationCapture(
      makeInPageBrowser(bridge.webview),
      { method: "workspace.open" },
    );
    bridge.webview.postMessage(ownerRequest());

    await capture.release();
    await assert.rejects(
      capture.wait(),
      /CAPTURE_RELEASED: workspace activation capture was released before readiness/,
    );
    assert.equal(bridge.listenerCount(), 0);
  } finally {
    delete globalThis.window;
  }
});

test("seals the first database readiness identity instead of accepting a replacement", async () => {
  const wrongDatabase = databaseOpened();
  wrongDatabase.payload.projectKey = "local:22222222222242228222222222222222";

  await assert.rejects(
    runExactCapture([
      wrongDatabase,
      databaseOpened(),
      ownerTerminal(),
      writableBootstrap(),
    ]),
    /CAPTURE_PROJECT_DUPLICATE: database readiness identity changed during activation/,
  );
});

test("replacement releases the previous capture across both handle release orders", async (t) => {
  for (const releaseNewFirst of [false, true]) {
    await t.test(releaseNewFirst ? "new-then-old" : "old-then-new", async () => {
      const bridge = makeWebView();
      const originalPostMessage = bridge.webview.postMessage;
      globalThis.window = { chrome: { webview: bridge.webview } };
      try {
        const page = makeInPageBrowser(bridge.webview);
        const first = await beginWorkspaceActivationCapture(page, { method: "workspace.open" });
        const second = await beginWorkspaceActivationCapture(page, { method: "workspace.open" });
        if (releaseNewFirst) await second.release();
        await assert.rejects(first.wait(), /CAPTURE_REPLACED/);
        if (!releaseNewFirst) await second.release();
        assert.equal(bridge.listenerCount(), 0);
        assert.equal(bridge.webview.postMessage, originalPostMessage);

        const third = await beginWorkspaceActivationCapture(page, { method: "workspace.open" });
        bridge.webview.postMessage(ownerRequest());
        bridge.dispatch(ownerTerminal());
        bridge.dispatch(writableBootstrap());
        bridge.dispatch(databaseOpened());
        assert.deepEqual(await third.wait(), databaseOpened());
        assert.equal(bridge.listenerCount(), 0);
        assert.equal(bridge.webview.postMessage, originalPostMessage);
      } finally {
        delete globalThis.window;
      }
    });
  }
});

test("fails closed when a later outbound reuses the owner requestId for another method", async () => {
  const bridge = makeWebView();
  globalThis.window = { chrome: { webview: bridge.webview } };
  try {
    const capture = await beginWorkspaceActivationCapture(
      makeInPageBrowser(bridge.webview),
      { method: "workspace.open" },
    );
    bridge.webview.postMessage(ownerRequest());
    const collision = ownerRequest();
    collision.payload.method = "workspace.switch";
    bridge.webview.postMessage(collision);

    await assert.rejects(
      capture.wait(),
      /CAPTURE_OUTBOUND_IDENTITY_MISMATCH: workspace activation requestId changed method/,
    );
    assert.equal(bridge.listenerCount(), 0);
  } finally {
    delete globalThis.window;
  }
});
