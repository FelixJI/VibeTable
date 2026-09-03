import assert from "node:assert/strict";
import test from "node:test";
import vm from "node:vm";

import {
  ImportFaultOutcomeContractError,
  beginImportFaultOutcomeCapture,
  waitForFailedImportUi,
} from "./import_fault_outcome.mjs";

const WORKSPACE_ID = "11111111-1111-4111-8111-111111111111";

test("failed import UI reads each menu body's disabled state", async (t) => {
  for (const [importDisabled, cancelDisabled] of [[false, true], [true, true], [false, false]]) {
    await t.test(`import disabled=${importDisabled}, cancel disabled=${cancelDisabled}`, async () => {
      const element = {
        isVisible: async () => true,
        innerText: async () => "Import failed",
        isDisabled: async () => false,
        click: async () => {},
        waitFor: async () => {},
      };
      const page = {
        getByTestId: () => element,
        keyboard: { press: async () => {} },
        locator: selector => {
          assert.equal(selector, ".n-dropdown-option-body");
          return { filter: ({ hasText }) => ({ last: () => {
            const disabled = hasText.test("导入数据") ? importDisabled : cancelDisabled;
            return {
              waitFor: async () => {},
              getAttribute: async () => `n-dropdown-option-body${disabled ? " n-dropdown-option-body--disabled" : ""}`,
              // Naive UI's parent does not carry the disabled modifier.
              locator: () => ({ getAttribute: async () => "n-dropdown-option" }),
            };
          } }) };
        },
      };
      const state = await waitForFailedImportUi(page, Date.now() + 1000);
      assert.equal(state.newImportAvailable, !importDisabled);
      assert.equal(state.cancelTaskAvailable, !cancelDisabled);
    });
  }
});

function scope(overrides = {}) {
  return {
    scope: "workspace",
    workspaceId: WORKSPACE_ID,
    sessionEpoch: 7,
    operationId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
    sequence: 1,
    ...overrides,
  };
}

function createRequest(overrides = {}) {
  return {
    type: "task.create",
    requestId: "import-create",
    scope: scope(),
    payload: {
      kind: "data.import",
      params: { grantId: "private-grant", token: "private-token", path: "C:\\private.csv" },
    },
    ...overrides,
  };
}

function createTerminal(taskId = "task-import") {
  return {
    type: "task.create",
    requestId: "import-create",
    payload: { taskId, kind: "data.import", state: "running" },
  };
}

function statusRequest(taskId = "task-import", overrides = {}) {
  return {
    type: "task.status",
    requestId: "import-status",
    scope: scope({ sequence: 2, operationId: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" }),
    payload: { taskId },
    ...overrides,
  };
}

function exactFault(requestId = "import-status", overrides = {}) {
  return {
    type: "operation.failed",
    requestId,
    payload: {
      operation: null,
      code: "BACKEND_UNAVAILABLE",
      message: "private failure at C:\\secret",
    },
    ...overrides,
  };
}

function verifiedFault() {
  return {
    fault: { status: "completed", processName: "vibetable-pb.exe", pid: 42 },
    barrier: { point: "after_record", pid: 42 },
  };
}

class FakeWebView {
  constructor() {
    this.listeners = new Set();
    this.posted = [];
    this.postMessage = (message) => this.posted.push(message);
  }

  addEventListener(type, listener) {
    if (type === "message") this.listeners.add(listener);
  }

  removeEventListener(type, listener) {
    if (type === "message") this.listeners.delete(listener);
  }

  emit(message) {
    for (const listener of [...this.listeners]) listener({ data: structuredClone(message) });
  }
}

class FakeHandle {
  constructor(value) { this.value = value; this.disposed = false; }
  async jsonValue() { return structuredClone(this.value); }
  async dispose() { this.disposed = true; }
}

class FakePage {
  constructor() {
    this.webview = new FakeWebView();
    this.window = { chrome: { webview: this.webview } };
    this.waiters = new Set();
    this.serializedEvaluations = 0;
  }

  async evaluate(fn, argument) {
    this.serializedEvaluations += 1;
    const realm = vm.createContext({ window: this.window, argument });
    return structuredClone(vm.runInContext(`(${fn.toString()})(argument)`, realm));
  }

  waitForFunction(fn, argument) {
    return new Promise((resolve, reject) => {
      const waiter = { check: () => {
        this.evaluate(fn, argument).then(value => {
          if (!value) return;
          this.waiters.delete(waiter);
          resolve(new FakeHandle(value));
        }, reject);
      }, reject };
      this.waiters.add(waiter);
      waiter.check();
    });
  }

  change() {
    for (const waiter of [...this.waiters]) waiter.check();
  }

  post(message) {
    this.webview.postMessage(message);
    this.change();
  }

  emit(message) {
    this.webview.emit(message);
    this.change();
  }
}

async function readyCapture(page) {
  const capture = await beginImportFaultOutcomeCapture(page);
  page.post(createRequest());
  page.emit(createTerminal());
  const task = await capture.waitForCreatedTask(1_000);
  return { capture, task };
}

test("correlates one exact fault after the barrier and leaves acknowledgement to its caller", async () => {
  const page = new FakePage();
  const { capture, task } = await readyCapture(page);
  try {
    assert.deepEqual(task, {
      requestId: "import-create", taskId: "task-import", workspaceId: WORKSPACE_ID, sessionEpoch: 7,
    });
    await capture.openFaultWindow({ deadlineAt: Date.now() + 1_000 });
    page.post(statusRequest());
    page.emit(exactFault());

    assert.deepEqual(await capture.settle({ deadlineAt: Date.now() + 1_000, ...verifiedFault() }), {
      kind: "expected-bridge-failure",
      failure: { requestId: "import-status" },
    });
    assert.equal(JSON.stringify(await capture.readEvidence()).includes("private"), false);
  } finally {
    await capture.release();
  }
});

test("accepts omitted or explicit task.status on an already correlated failure", async () => {
  for (const operation of [undefined, "task.status"]) {
    const page = new FakePage();
    const { capture } = await readyCapture(page);
    try {
      await capture.openFaultWindow({ deadlineAt: Date.now() + 1_000 });
      page.post(statusRequest());
      const terminal = exactFault();
      if (operation === undefined) delete terminal.payload.operation;
      else terminal.payload.operation = operation;
      page.emit(terminal);
      const result = await capture.settle({ deadlineAt: Date.now() + 1_000, ...verifiedFault() });
      assert.equal(result.kind, "expected-bridge-failure");
    } finally {
      await capture.release();
    }
  }
});

test("runs every injected capture function in an isolated serialized realm", async () => {
  const page = new FakePage();
  const { capture } = await readyCapture(page);
  try {
    await capture.openFaultWindow({ deadlineAt: Date.now() + 1_000 });
    page.post(statusRequest());
    page.emit(exactFault());
    await capture.settle({ deadlineAt: Date.now() + 1_000, ...verifiedFault() });
    assert.ok(page.serializedEvaluations >= 4);
  } finally {
    await capture.release();
  }
});

test("accepts the existing succeeded-with-failedRows import representation without retaining rows", async () => {
  const page = new FakePage();
  const { capture } = await readyCapture(page);
  try {
    await capture.openFaultWindow({ deadlineAt: Date.now() + 1_000 });
    page.post(statusRequest());
    page.emit({
      type: "task.status",
      requestId: "import-status",
      payload: {
        taskId: "task-import", kind: "data.import", state: "succeeded",
        result: { failedRows: ["C:\\private-row"] },
      },
    });
    assert.deepEqual(await capture.settle({ deadlineAt: Date.now() + 1_000, ...verifiedFault() }), {
      kind: "normal-task-failure", state: "succeeded", failedRowCount: 1,
    });
    assert.equal(JSON.stringify(await capture.readEvidence()).includes("private-row"), false);
  } finally {
    await capture.release();
  }
});

test("keeps an in-flight task status eligible only when its terminal arrives after arm", async (t) => {
  await t.test("pending at arm may expose the exact post-arm fault", async () => {
    const page = new FakePage();
    const { capture } = await readyCapture(page);
    try {
      page.post(statusRequest());
      await capture.openFaultWindow({ deadlineAt: Date.now() + 1_000 });
      page.emit(exactFault());
      assert.deepEqual(await capture.settle({ deadlineAt: Date.now() + 1_000, ...verifiedFault() }), {
        kind: "expected-bridge-failure", failure: { requestId: "import-status" },
      });
    } finally {
      await capture.release();
    }
  });
  await t.test("a completed pre-arm status cannot become an expected fault", async () => {
    const page = new FakePage();
    const { capture } = await readyCapture(page);
    try {
      page.post(statusRequest());
      page.emit({
        type: "task.status", requestId: "import-status",
        payload: { taskId: "task-import", kind: "data.import", state: "failed", result: null },
      });
      await capture.openFaultWindow({ deadlineAt: Date.now() + 1_000 });
      await assert.rejects(
        capture.settle({ deadlineAt: Date.now() + 1_000, ...verifiedFault() }),
        ImportFaultOutcomeContractError,
      );
    } finally {
      await capture.release();
    }
  });
});

test("fails closed for mismatched scope, code, duplicate candidate, and unverified kill", async (t) => {
  const cases = [
    { name: "wrong explicit operation", request: statusRequest(), terminal: exactFault("import-status", { payload: { operation: "task.cancel", code: "BACKEND_UNAVAILABLE" } }) },
    { name: "scope", request: statusRequest("task-import", { scope: scope({ sessionEpoch: 8 }) }), terminal: exactFault() },
    { name: "code", request: statusRequest(), terminal: exactFault("import-status", { payload: { operation: "task.status", code: "PRODUCT_DATA_FAILED" } }) },
    { name: "duplicate", request: statusRequest(), terminal: [exactFault(), exactFault()] },
    { name: "kill", request: statusRequest(), terminal: exactFault(), evidence: { fault: { status: "completed", processName: "vibetable-pb.exe", pid: 42 }, barrier: { point: "after_record", pid: 9 } } },
  ];
  for (const item of cases) await t.test(item.name, async () => {
    const page = new FakePage();
    const { capture } = await readyCapture(page);
    try {
      await capture.openFaultWindow({ deadlineAt: Date.now() + 1_000 });
      page.post(item.request);
      for (const terminal of Array.isArray(item.terminal) ? item.terminal : [item.terminal]) page.emit(terminal);
      await assert.rejects(
        capture.settle({ deadlineAt: Date.now() + 1_000, ...(item.evidence ?? verifiedFault()) }),
        ImportFaultOutcomeContractError,
      );
    } finally {
      await capture.release();
    }
  });
});

test("release removes only its listener and never overwrites a successor wrapper", async () => {
  const page = new FakePage();
  const capture = await beginImportFaultOutcomeCapture(page);
  const successor = () => "successor";
  page.webview.postMessage = successor;

  await capture.release();

  assert.equal(page.webview.postMessage, successor);
  assert.equal(page.webview.listeners.size, 0);
});

test("release restores its prior wrapper and listener after a post failure", async () => {
  const page = new FakePage();
  page.webview.postMessage = () => { throw new Error("bridge unavailable"); };
  const original = page.webview.postMessage;
  const capture = await beginImportFaultOutcomeCapture(page);
  try {
    assert.throws(() => page.post(createRequest()), /bridge unavailable/);
  } finally {
    await capture.release();
  }
  assert.equal(page.webview.postMessage, original);
  assert.equal(page.webview.listeners.size, 0);
});
