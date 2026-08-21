import assert from "node:assert/strict";
import test from "node:test";

import {
  Scenario18RecoveryBoundaryError,
  runScenario18RecoveryBoundary,
} from "./scenario18_recovery_boundary.mjs";

const CONTENT_REQUEST_TYPES = [
  "schema.getTable",
  "contentProfile.load",
  "recordDocumentLink.list",
];

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((accept, decline) => {
    resolve = accept;
    reject = decline;
  });
  return { promise, resolve, reject };
}

async function flushMicrotasks() {
  for (let index = 0; index < 12; index += 1) await Promise.resolve();
}

function withWindow(windowValue, callback) {
  const previous = globalThis.window;
  globalThis.window = windowValue;
  try {
    return callback();
  } finally {
    if (previous === undefined) delete globalThis.window;
    else globalThis.window = previous;
  }
}

class FakeJsHandle {
  constructor(value, jsonError = null) {
    this.value = value;
    this.jsonError = jsonError;
    this.disposed = false;
  }

  async jsonValue() {
    if (this.jsonError) throw this.jsonError;
    return structuredClone(this.value);
  }

  async dispose() {
    this.disposed = true;
  }
}

class FakeWebView {
  constructor(onChange) {
    this.listeners = new Set();
    this.onChange = onChange;
    this.nextAddError = null;
  }

  addEventListener(type, listener) {
    assert.equal(type, "message");
    if (this.nextAddError) {
      const error = this.nextAddError;
      this.nextAddError = null;
      throw error;
    }
    this.listeners.add(listener);
  }

  removeEventListener(type, listener) {
    assert.equal(type, "message");
    this.listeners.delete(listener);
  }

  postMessage() {
    this.onChange();
  }

  failNextPostMessageAssignment(error) {
    let current = this.postMessage;
    let shouldFail = true;
    Object.defineProperty(this, "postMessage", {
      configurable: true,
      get: () => current,
      set: (value) => {
        current = value;
        if (!shouldFail) return;
        shouldFail = false;
        throw error;
      },
    });
  }

  emit(message) {
    for (const listener of [...this.listeners]) {
      listener({ data: structuredClone(message) });
    }
    this.onChange();
  }
}

class FakePage {
  constructor() {
    this.currentProjection = deferred();
    this.summaryWaitOptions = null;
    this.waiters = new Set();
    this.handles = [];
    this.nextWaitError = null;
    this.nextJsonError = null;
    this.window = {
      chrome: {
        webview: new FakeWebView(() => this.notifyChange()),
      },
      __vibetableE2EBridgeDiagnostics: {
        requests: [],
        roundTrips: [],
      },
    };
  }

  get webview() {
    return this.window.chrome.webview;
  }

  get diagnostics() {
    return this.window.__vibetableE2EBridgeDiagnostics;
  }

  getByTestId(testId) {
    assert.equal(testId, "table-summary");
    return {
      waitFor: (options) => {
        this.summaryWaitOptions = options;
        return this.currentProjection.promise;
      },
    };
  }

  async evaluate(pageFunction, argument) {
    return withWindow(this.window, () => pageFunction(argument));
  }

  waitForFunction(pageFunction, argument, options) {
    if (this.nextWaitError) {
      const error = this.nextWaitError;
      this.nextWaitError = null;
      return Promise.reject(error);
    }
    return new Promise((resolve, reject) => {
      const waiter = {
        check: () => {
          try {
            const value = withWindow(this.window, () => pageFunction(argument));
            if (!value) return;
            this.waiters.delete(waiter);
            const handle = new FakeJsHandle(value, this.nextJsonError);
            this.nextJsonError = null;
            this.handles.push(handle);
            resolve(handle);
          } catch (error) {
            this.waiters.delete(waiter);
            reject(error);
          }
        },
        options,
        reject,
      };
      this.waiters.add(waiter);
      waiter.check();
    });
  }

  notifyChange() {
    for (const waiter of [...this.waiters]) waiter.check();
  }

  rejectOldestWaiter(error) {
    const waiter = this.waiters.values().next().value;
    assert.ok(waiter);
    this.waiters.delete(waiter);
    waiter.reject(error);
  }

  addRequest(requestType, requestId) {
    this.diagnostics.requests.push({ requestType, requestId, privatePath: "C:\\secret" });
    if (this.diagnostics.requests.length > 200) this.diagnostics.requests.shift();
    this.notifyChange();
  }

  addTerminal(requestId, responseType, extras = {}) {
    this.diagnostics.roundTrips.push({
      requestId,
      responseType,
      operation: extras.operation ?? null,
      code: extras.code ?? null,
      rawMessage: extras.rawMessage ?? null,
    });
    if (this.diagnostics.roundTrips.length > 200) this.diagnostics.roundTrips.shift();
    this.notifyChange();
  }

  addContentSuccess(prefix = "fresh") {
    CONTENT_REQUEST_TYPES.forEach((requestType, index) => {
      const requestId = `${prefix}-${index}`;
      this.addRequest(requestType, requestId);
      this.addTerminal(requestId, requestType);
    });
  }
}

function datasetReady(table = "tbl-search") {
  return {
    type: "table.datasetReady",
    payload: { table, rows: [{ privatePath: "C:\\secret" }] },
  };
}

function armTableSelection(page) {
  page.webview.postMessage({
    type: "table.selected",
    payload: { table: "private-table-name" },
  });
}

function scenario(page, overrides = {}) {
  return {
    page,
    tableId: "tbl-search",
    injectFault: async () => ({ pid: 42 }),
    awaitBackendRecovery: async () => {},
    prepareFreshTable: async () => {},
    triggerFreshTable: async () => {
      armTableSelection(page);
      page.webview.emit(datasetReady());
    },
    prepareFreshContent: async () => {},
    triggerFreshContent: async () => page.addContentSuccess(),
    readFreshContent: async () => "fresh content",
    ...overrides,
  };
}

test("serializes every recovery stage behind the current table authority terminal", async () => {
  const page = new FakePage();
  const fault = deferred();
  const backend = deferred();
  const table = deferred();
  const content = deferred();
  const calls = [];
  const run = runScenario18RecoveryBoundary(scenario(page, {
    injectFault: async () => {
      calls.push("fault");
      await fault.promise;
      return { pid: 42 };
    },
    awaitBackendRecovery: async () => {
      calls.push("backend");
      await backend.promise;
    },
    prepareFreshTable: async () => {
      calls.push("prepare-table");
    },
    triggerFreshTable: async () => {
      calls.push("trigger-table");
      armTableSelection(page);
      await table.promise;
      page.webview.emit(datasetReady());
    },
    prepareFreshContent: async () => {
      calls.push("prepare-content");
    },
    triggerFreshContent: async () => {
      calls.push("trigger-content");
      await content.promise;
      page.addContentSuccess();
    },
    readFreshContent: async () => {
      calls.push("read");
      return "fresh content";
    },
  }));

  await flushMicrotasks();
  assert.deepEqual(calls, []);
  assert.deepEqual(page.summaryWaitOptions, { state: "attached", timeout: 30_000 });

  page.currentProjection.resolve();
  await flushMicrotasks();
  assert.deepEqual(calls, ["fault"]);

  fault.resolve();
  await flushMicrotasks();
  assert.deepEqual(calls, ["fault", "backend"]);

  backend.resolve();
  await flushMicrotasks();
  assert.deepEqual(calls, ["fault", "backend", "prepare-table", "trigger-table"]);

  table.resolve();
  await flushMicrotasks();
  assert.deepEqual(calls, [
    "fault",
    "backend",
    "prepare-table",
    "trigger-table",
    "prepare-content",
    "trigger-content",
  ]);

  content.resolve();
  assert.deepEqual(await run, { fault: { pid: 42 }, content: "fresh content" });
  assert.deepEqual(calls, [
    "fault",
    "backend",
    "prepare-table",
    "trigger-table",
    "prepare-content",
    "trigger-content",
    "read",
  ]);
  assert.equal(page.webview.listeners.size, 0);
  assert.ok(page.handles.every((handle) => handle.disposed));
});

test("does not own same-table readiness before the outbound fresh table selection", async () => {
  const page = new FakePage();
  page.currentProjection.resolve();
  const triggered = deferred();
  let contentPrepared = false;
  const originalPostMessage = page.webview.postMessage;
  const run = runScenario18RecoveryBoundary(scenario(page, {
    prepareFreshTable: async () => {
      assert.equal(page.webview.listeners.size, 0);
      page.webview.emit(datasetReady());
    },
    triggerFreshTable: async () => {
      assert.equal(page.webview.listeners.size, 1);
      page.webview.emit(datasetReady());
      armTableSelection(page);
      triggered.resolve();
    },
    prepareFreshContent: async () => {
      contentPrepared = true;
    },
  }));

  await triggered.promise;
  await flushMicrotasks();
  assert.equal(contentPrepared, false);
  assert.equal(page.webview.listeners.size, 1);

  page.webview.emit(datasetReady());
  assert.equal((await run).content, "fresh content");
  assert.equal(contentPrepared, true);
  assert.equal(page.webview.listeners.size, 0);
  assert.equal(page.webview.postMessage, originalPostMessage);
});

test("ignores a wrong-table terminal and releases the listener after the owned table succeeds", async () => {
  const page = new FakePage();
  page.currentProjection.resolve();
  const run = runScenario18RecoveryBoundary(scenario(page, {
    triggerFreshTable: async () => {
      armTableSelection(page);
      page.webview.emit(datasetReady("tbl-other"));
      assert.equal(page.webview.listeners.size, 1);
      queueMicrotask(() => page.webview.emit(datasetReady()));
    },
  }));

  assert.equal((await run).content, "fresh content");
  assert.equal(page.webview.listeners.size, 0);
});

for (const operation of ["table.selected", "query"]) {
  test(`closes on an anonymous ${operation} authority failure with sanitized evidence`, async () => {
    const page = new FakePage();
    page.currentProjection.resolve();
    const run = runScenario18RecoveryBoundary(scenario(page, {
      triggerFreshTable: async () => {
        armTableSelection(page);
        page.webview.emit({
          type: "operation.failed",
          requestId: null,
          payload: {
            operation,
            code: "BACKEND_UNAVAILABLE",
            message: "secret C:\\workspace\\data.db",
          },
        });
      },
    }));

    await assert.rejects(run, (error) => {
      assert.ok(error instanceof Scenario18RecoveryBoundaryError);
      assert.deepEqual(error.evidence, {
        type: "operation.failed",
        operation,
        code: "BACKEND_UNAVAILABLE",
        requestId: null,
      });
      assert.doesNotMatch(JSON.stringify(error), /secret|workspace|data\.db/u);
      return true;
    });
    assert.equal(page.webview.listeners.size, 0);
  });
}

test("does not consume unrelated or correlated operation failures", async () => {
  const page = new FakePage();
  page.currentProjection.resolve();
  const run = runScenario18RecoveryBoundary(scenario(page, {
    triggerFreshTable: async () => {
      armTableSelection(page);
      page.webview.emit({
        type: "operation.failed",
        requestId: null,
        payload: { operation: "contentProfile.load", code: "BACKEND_UNAVAILABLE" },
      });
      page.webview.emit({
        type: "operation.failed",
        requestId: "other-request",
        payload: { operation: "table.selected", code: "BACKEND_UNAVAILABLE" },
      });
      assert.equal(page.webview.listeners.size, 1);
      page.webview.emit(datasetReady());
    },
  }));

  assert.equal((await run).content, "fresh content");
  assert.equal(page.webview.listeners.size, 0);
});

test("releases the table listener when select action throws", async () => {
  const page = new FakePage();
  page.currentProjection.resolve();
  const originalPostMessage = page.webview.postMessage;
  const run = runScenario18RecoveryBoundary(scenario(page, {
    triggerFreshTable: async () => { throw new Error("select failed"); },
  }));

  await assert.rejects(run, /select failed/u);
  assert.equal(page.webview.listeners.size, 0);
  assert.equal(page.webview.postMessage, originalPostMessage);
});

test("releases the table listener when the authority wait times out", async () => {
  const page = new FakePage();
  page.currentProjection.resolve();
  const originalPostMessage = page.webview.postMessage;
  const timeout = new Error("playwright path C:\\secret");
  timeout.name = "TimeoutError";
  page.nextWaitError = timeout;
  const run = runScenario18RecoveryBoundary(scenario(page, {
    triggerFreshTable: async () => armTableSelection(page),
  }));

  await assert.rejects(run, (error) => {
    assert.ok(error instanceof Scenario18RecoveryBoundaryError);
    assert.doesNotMatch(JSON.stringify(error), /playwright|secret/u);
    return true;
  });
  assert.equal(page.webview.listeners.size, 0);
  assert.equal(page.webview.postMessage, originalPostMessage);
});

test("rolls back table capture when listener installation throws", async () => {
  const page = new FakePage();
  page.currentProjection.resolve();
  const originalPostMessage = page.webview.postMessage;
  page.webview.nextAddError = new Error("listener installation failed");

  await assert.rejects(
    runScenario18RecoveryBoundary(scenario(page)),
    /listener installation failed/u,
  );
  assert.equal(page.webview.listeners.size, 0);
  assert.equal(page.webview.postMessage, originalPostMessage);
  assert.equal(page.window.__vibetableE2EScenario18TableProjection, undefined);
});

test("rolls back an installed listener when postMessage replacement throws", async () => {
  const page = new FakePage();
  page.currentProjection.resolve();
  const originalPostMessage = page.webview.postMessage;
  page.webview.failNextPostMessageAssignment(new Error("postMessage replacement failed"));

  await assert.rejects(
    runScenario18RecoveryBoundary(scenario(page)),
    /postMessage replacement failed/u,
  );
  assert.equal(page.webview.listeners.size, 0);
  assert.equal(page.webview.postMessage, originalPostMessage);
  assert.equal(page.window.__vibetableE2EScenario18TableProjection, undefined);
});

test("a stale capture release cannot tear down the newer single-flight owner", async () => {
  const page = new FakePage();
  page.currentProjection.resolve();
  const originalPostMessage = page.webview.postMessage;
  const firstTriggered = deferred();
  const secondTriggered = deferred();
  const first = runScenario18RecoveryBoundary(scenario(page, {
    triggerFreshTable: async () => {
      armTableSelection(page);
      firstTriggered.resolve();
    },
  }));

  await firstTriggered.promise;
  await flushMicrotasks();
  const second = runScenario18RecoveryBoundary(scenario(page, {
    triggerFreshTable: async () => {
      armTableSelection(page);
      secondTriggered.resolve();
    },
  }));
  await secondTriggered.promise;
  await flushMicrotasks();
  assert.equal(page.webview.listeners.size, 1);

  const staleTimeout = new Error("stale capture timeout");
  staleTimeout.name = "TimeoutError";
  page.rejectOldestWaiter(staleTimeout);
  await assert.rejects(first, Scenario18RecoveryBoundaryError);
  assert.equal(page.webview.listeners.size, 1);
  assert.notEqual(page.webview.postMessage, originalPostMessage);

  page.webview.emit(datasetReady());
  assert.equal((await second).content, "fresh content");
  assert.equal(page.webview.listeners.size, 0);
  assert.equal(page.webview.postMessage, originalPostMessage);
});

test("disposes the table JSHandle even when reading its value throws", async () => {
  const page = new FakePage();
  page.currentProjection.resolve();
  page.nextJsonError = new Error("json failed");
  const run = runScenario18RecoveryBoundary(scenario(page));

  await assert.rejects(run, Scenario18RecoveryBoundaryError);
  assert.equal(page.webview.listeners.size, 0);
  assert.equal(page.handles.length, 1);
  assert.equal(page.handles[0].disposed, true);
});

test("owns one successful terminal for each fresh content request identity", async () => {
  const page = new FakePage();
  page.currentProjection.resolve();

  const result = await runScenario18RecoveryBoundary(scenario(page));

  assert.equal(result.content, "fresh content");
  assert.equal(page.handles.length, 2);
  assert.ok(page.handles.every((handle) => handle.disposed));
});

test("fails immediately on an owned content failure before panel visibility or reading", async () => {
  const page = new FakePage();
  page.currentProjection.resolve();
  let read = false;
  const run = runScenario18RecoveryBoundary(scenario(page, {
    triggerFreshContent: async () => {
      CONTENT_REQUEST_TYPES.slice(0, 2).forEach((requestType, index) => {
        const requestId = `request-${index}`;
        page.addRequest(requestType, requestId);
        page.addTerminal(
          requestId,
          index === 1 ? "operation.failed" : requestType,
          index === 1
            ? { operation: requestType, code: "BACKEND_UNAVAILABLE", rawMessage: "C:\\secret" }
            : {},
        );
      });
    },
    readFreshContent: async () => {
      read = true;
      throw new Error("panel never became visible");
    },
  }));

  await assert.rejects(run, (error) => {
    assert.ok(error instanceof Scenario18RecoveryBoundaryError);
    assert.equal(error.evidence.operation, "contentProfile.load");
    assert.doesNotMatch(JSON.stringify(error), /secret/u);
    return true;
  });
  assert.equal(read, false);
  assert.ok(page.handles.every((handle) => handle.disposed));
});

test("sanitizes malformed content terminal operation and code values", async () => {
  const page = new FakePage();
  page.currentProjection.resolve();
  const run = runScenario18RecoveryBoundary(scenario(page, {
    triggerFreshContent: async () => {
      page.addRequest("schema.getTable", "malformed-terminal");
      page.addTerminal("malformed-terminal", "operation.failed", {
        operation: "secret C:\\workspace\\data.db",
        code: "secret backend path",
      });
    },
  }));

  await assert.rejects(run, (error) => {
    assert.deepEqual(error.evidence, {
      requestId: "malformed-terminal",
      responseType: "operation.failed",
      operation: null,
      code: null,
    });
    assert.doesNotMatch(JSON.stringify(error), /secret|workspace|data\.db/u);
    return true;
  });
});

test("disposes the content JSHandle even when reading its value throws", async () => {
  const page = new FakePage();
  page.currentProjection.resolve();
  const run = runScenario18RecoveryBoundary(scenario(page, {
    triggerFreshContent: async () => {
      page.addContentSuccess();
      page.nextJsonError = new Error("content json failed");
    },
  }));

  await assert.rejects(run, Scenario18RecoveryBoundaryError);
  assert.equal(page.handles.length, 2);
  assert.ok(page.handles.every((handle) => handle.disposed));
});

for (const duplicate of ["request", "terminal"]) {
  test(`fails closed on duplicate content ${duplicate} ownership`, async () => {
    const page = new FakePage();
    page.currentProjection.resolve();
    const run = runScenario18RecoveryBoundary(scenario(page, {
      triggerFreshContent: async () => {
        page.addContentSuccess();
        if (duplicate === "request") {
          page.addRequest("schema.getTable", "duplicate-schema");
          page.addTerminal("duplicate-schema", "schema.getTable");
        } else {
          page.addTerminal("fresh-0", "schema.getTable");
        }
      },
    }));

    await assert.rejects(run, (error) => {
      assert.ok(error instanceof Scenario18RecoveryBoundaryError);
      assert.equal(error.evidence.reason, `duplicate_${duplicate}`);
      return true;
    });
    assert.ok(page.handles.every((handle) => handle.disposed));
  });
}

test("recognizes new request identities after a bounded 200-entry ledger shift", async () => {
  const page = new FakePage();
  page.currentProjection.resolve();
  for (let index = 0; index < 200; index += 1) {
    const requestType = CONTENT_REQUEST_TYPES[index % CONTENT_REQUEST_TYPES.length];
    const requestId = `old-${index}`;
    page.addRequest(requestType, requestId);
    page.addTerminal(requestId, requestType);
  }

  const result = await runScenario18RecoveryBoundary(scenario(page, {
    triggerFreshContent: async () => page.addContentSuccess("new"),
  }));

  assert.equal(result.content, "fresh content");
  assert.equal(page.diagnostics.requests.length, 200);
});

test("does not reinterpret old successful requests as the fresh content projection", async () => {
  const page = new FakePage();
  page.currentProjection.resolve();
  page.addContentSuccess("old");
  const opened = deferred();
  let read = false;
  const run = runScenario18RecoveryBoundary(scenario(page, {
    triggerFreshContent: async () => opened.resolve(),
    readFreshContent: async () => {
      read = true;
      return "fresh content";
    },
  }));

  await opened.promise;
  await flushMicrotasks();
  assert.equal(read, false);

  page.addContentSuccess("new");
  assert.equal((await run).content, "fresh content");
  assert.equal(read, true);
});
