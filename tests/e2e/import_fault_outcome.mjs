let ownerSequence = 0;

export async function waitForFailedImportUi(page, deadlineAt) {
  const panel = page.getByTestId("import-preview-panel");
  const error = page.getByTestId("import-apply-error");
  let errorLength = 0;
  while (Date.now() < deadlineAt) {
    if (await error.isVisible().catch(() => false)) {
      errorLength = (await error.innerText()).trim().length;
      const confirmEnabled = !await page.getByTestId("import-confirm").isDisabled();
      const cancelEnabled = !await page.getByTestId("import-cancel").isDisabled();
      if (errorLength > 0 && confirmEnabled && cancelEnabled) {
        await page.getByTestId("import-cancel").click();
        await panel.waitFor({ state: "hidden", timeout: Math.max(1, deadlineAt - Date.now()) });
        await page.getByTestId("toolbar-more").click();
        const importOption = page.locator(".n-dropdown-option-body")
          .filter({ hasText: /导入数据|Import data/i }).last();
        await importOption.waitFor({ timeout: Math.max(1, deadlineAt - Date.now()) });
        const optionClass = await importOption.getAttribute("class");
        const cancelOption = page.locator(".n-dropdown-option-body")
          .filter({ hasText: /取消数据任务|Cancel data task/i }).last();
        await cancelOption.waitFor({ timeout: Math.max(1, deadlineAt - Date.now()) });
        const cancelClass = await cancelOption.getAttribute("class");
        await page.keyboard.press("Escape");
        return {
          errorLength,
          confirmEnabled,
          cancelEnabled,
          newImportAvailable: !optionClass?.includes("--disabled"),
          cancelTaskAvailable: !cancelClass?.includes("--disabled"),
        };
      }
    }
    await page.waitForTimeout(50);
  }
  throw new Error("faulted import did not reach an explicit non-busy UI failure state");
}

export class ImportFaultOutcomeContractError extends Error {
  constructor(message) {
    super(message);
    this.name = "ImportFaultOutcomeContractError";
  }
}

function installImportFaultOutcomeCaptureInPage({ ownerToken }) {
  const captureKey = "__vibetableE2EImportFaultOutcome";
  const webview = window.chrome?.webview;
  const contractError = message => {
    const error = new Error(message);
    error.name = "ImportFaultOutcomeContractError";
    return error;
  };
  if (!webview || typeof webview.postMessage !== "function") {
    throw contractError("import fault capture requires WebView2");
  }
  const prior = window[captureKey];
  if (prior?.release) prior.release();
  const parse = value => {
    if (typeof value !== "string") return value;
    try { return JSON.parse(value); } catch { return null; }
  };
  const readScope = value => value?.scope === "workspace"
    && typeof value.workspaceId === "string" && value.workspaceId.length > 0
    && Number.isSafeInteger(value.sessionEpoch) && value.sessionEpoch > 0
    ? { workspaceId: value.workspaceId, sessionEpoch: value.sessionEpoch }
    : null;
  const capture = {
    ownerToken,
    create: null,
    error: null,
    event: 0,
    window: null,
    statuses: new Map(),
    listener: null,
    previousPostMessage: webview.postMessage,
    wrapper: null,
    released: false,
    release: null,
  };
  const fail = code => { if (!capture.error) capture.error = code; };
  capture.release = () => {
    if (capture.released) return;
    capture.released = true;
    webview.removeEventListener("message", capture.listener);
    if (webview.postMessage === capture.wrapper) webview.postMessage = capture.previousPostMessage;
    if (window[captureKey] === capture) delete window[captureKey];
  };
  capture.listener = event => {
    if (capture.released) return;
    const message = parse(event.data);
    capture.event += 1;
    if (!message || capture.error) return;
    if (message.requestId === capture.create?.requestId && message.type === "task.create") {
      const payload = message.payload;
      if (
        typeof payload?.taskId !== "string" || !payload.taskId
        || payload.kind !== "data.import"
      ) fail("CREATE_TERMINAL_INVALID");
      else capture.create = { ...capture.create, taskId: payload.taskId };
      return;
    }
    const status = capture.statuses.get(message?.requestId);
    if (!status) return;
    if (status.terminal !== null) {
      fail("STATUS_TERMINAL_DUPLICATE");
      return;
    }
    if (message.type === "operation.failed") {
      const payload = message.payload;
      status.terminal = payload?.operation === "task.status"
        && payload?.code === "BACKEND_UNAVAILABLE"
        ? { kind: "expected", requestId: status.requestId, at: capture.event }
        : { kind: "unexpected", at: capture.event };
      return;
    }
    if (message.type !== "task.status" || message.payload?.taskId !== capture.create.taskId) {
      status.terminal = { kind: "unexpected", at: capture.event };
      return;
    }
    const payload = message.payload;
    const failedRowCount = Array.isArray(payload?.result?.failedRows)
      ? payload.result.failedRows.length
      : 0;
    status.terminal = {
      kind: "status",
      state: payload?.state,
      failedRowCount,
      at: capture.event,
    };
  };
  capture.wrapper = (...args) => {
    if (capture.released) return capture.previousPostMessage.apply(webview, args);
    const message = parse(args[0]);
    capture.event += 1;
    if (message?.type === "task.create" && message.payload?.kind === "data.import") {
      const messageScope = readScope(message.scope);
      if (capture.create) fail("IMPORT_CREATE_NOT_UNIQUE");
      else if (typeof message.requestId !== "string" || !message.requestId || !messageScope) {
        fail("CREATE_IDENTITY_INVALID");
      } else {
        capture.create = { requestId: message.requestId, ...messageScope, taskId: null };
      }
    }
    if (message?.type === "task.status" && capture.create?.taskId
      && message.payload?.taskId === capture.create.taskId) {
      const messageScope = readScope(message.scope);
      if (!messageScope || messageScope.workspaceId !== capture.create.workspaceId
        || messageScope.sessionEpoch !== capture.create.sessionEpoch) {
        fail("STATUS_SCOPE_MISMATCH");
      } else if (typeof message.requestId !== "string" || !message.requestId) {
        fail("STATUS_IDENTITY_INVALID");
      } else if (capture.statuses.has(message.requestId)) {
        fail("STATUS_REQUEST_DUPLICATE");
      } else {
        capture.statuses.set(message.requestId, { requestId: message.requestId, sentAt: capture.event, terminal: null });
      }
    }
    try {
      return capture.previousPostMessage.apply(webview, args);
    } catch (error) {
      fail("POST_FAILED");
      throw error;
    }
  };
  webview.addEventListener("message", capture.listener);
  webview.postMessage = capture.wrapper;
  window[captureKey] = capture;
}

function readCaptureInPage({ ownerToken }) {
  const capture = window.__vibetableE2EImportFaultOutcome;
  if (capture?.ownerToken !== ownerToken) return { error: "CAPTURE_STALE" };
  if (capture.error) return { error: capture.error };
  if (!capture.create?.taskId) return null;
  const { requestId, taskId, workspaceId, sessionEpoch } = capture.create;
  return { task: { requestId, taskId, workspaceId, sessionEpoch } };
}

function openFaultWindowInPage({ ownerToken, deadlineAt }) {
  const capture = window.__vibetableE2EImportFaultOutcome;
  const contractError = message => {
    const error = new Error(message);
    error.name = "ImportFaultOutcomeContractError";
    return error;
  };
  if (capture?.ownerToken !== ownerToken || capture.error || !capture.create?.taskId) {
    throw contractError("import fault capture is not ready");
  }
  if (!Number.isFinite(deadlineAt) || Date.now() >= deadlineAt || capture.window) {
    throw contractError("import fault window is invalid");
  }
  capture.window = {
    startEvent: capture.event,
    pendingAtArm: [...capture.statuses.values()]
      .filter(status => status.terminal === null)
      .map(status => status.requestId),
  };
}

function settleFaultWindowInPage({ ownerToken, deadlineAt, fault, barrier }) {
  const capture = window.__vibetableE2EImportFaultOutcome;
  const contractError = message => {
    const error = new Error(message);
    error.name = "ImportFaultOutcomeContractError";
    return error;
  };
  if (capture?.ownerToken !== ownerToken || capture.error || !capture.window) {
    throw contractError("import fault capture cannot settle");
  }
  if (!Number.isFinite(deadlineAt) || Date.now() >= deadlineAt
    || fault?.status !== "completed" || fault?.processName !== "vibetable-pb.exe"
    || !Number.isSafeInteger(fault?.pid) || fault.pid <= 0
    || barrier?.point !== "after_record" || barrier?.pid !== fault.pid) {
    throw contractError("import fault receipt is not verified");
  }
  const pendingAtArm = new Set(capture.window.pendingAtArm);
  const statuses = [...capture.statuses.values()]
    .filter(status => pendingAtArm.has(status.requestId)
      || status.sentAt > capture.window.startEvent);
  if (statuses.some(status => status.terminal === null
    || status.terminal.at <= capture.window.startEvent
    || status.terminal.kind === "unexpected")) {
    throw contractError("import task status terminal is not eligible");
  }
  const expected = statuses.filter(status => status.terminal.kind === "expected");
  if (expected.length > 1) throw contractError("import fault candidate is duplicate");
  if (expected.length === 1) return {
    kind: "expected-bridge-failure", failure: { requestId: expected[0].requestId },
  };
  const normal = statuses.find(status => status.terminal.kind === "status" && (
    ["failed", "cancelled", "aborted"].includes(status.terminal.state)
    || (status.terminal.state === "succeeded" && status.terminal.failedRowCount > 0)
  ));
  if (!normal) throw contractError("import task did not expose a normal failure");
  return {
    kind: "normal-task-failure",
    state: normal.terminal.state,
    ...(normal.terminal.failedRowCount > 0 ? { failedRowCount: normal.terminal.failedRowCount } : {}),
  };
}

function readEvidenceInPage({ ownerToken }) {
  const capture = window.__vibetableE2EImportFaultOutcome;
  if (capture?.ownerToken !== ownerToken) return null;
  return {
    task: capture.create?.taskId ? {
      requestId: capture.create.requestId,
      taskId: capture.create.taskId,
      workspaceId: capture.create.workspaceId,
      sessionEpoch: capture.create.sessionEpoch,
    } : null,
    statuses: [...capture.statuses.values()].map(status => ({
      requestId: status.requestId,
      terminal: status.terminal?.kind ?? null,
    })),
  };
}

function releaseCaptureInPage({ ownerToken }) {
  const capture = window.__vibetableE2EImportFaultOutcome;
  if (capture?.ownerToken !== ownerToken) return false;
  capture.release();
  return true;
}

export async function beginImportFaultOutcomeCapture(page) {
  const ownerToken = `import-fault-${++ownerSequence}`;
  const evaluate = async (fn, argument) => {
    try {
      return await page.evaluate(fn, argument);
    } catch (error) {
      if (error?.name === "ImportFaultOutcomeContractError") {
        throw new ImportFaultOutcomeContractError(error.message);
      }
      throw error;
    }
  };
  await evaluate(installImportFaultOutcomeCaptureInPage, { ownerToken });
  let releasePromise = null;
  const release = () => {
    releasePromise ??= evaluate(releaseCaptureInPage, { ownerToken });
    return releasePromise;
  };
  return {
    async waitForCreatedTask(timeoutMs = 20_000) {
      let handle;
      try {
        handle = await page.waitForFunction(readCaptureInPage, { ownerToken }, { timeout: timeoutMs });
        const result = await handle.jsonValue();
        if (result?.error) throw new ImportFaultOutcomeContractError(result.error);
        return result.task;
      } finally {
        await handle?.dispose();
      }
    },
    openFaultWindow: options => evaluate(openFaultWindowInPage, { ownerToken, ...options }),
    settle: options => evaluate(settleFaultWindowInPage, { ownerToken, ...options }),
    readEvidence: () => evaluate(readEvidenceInPage, { ownerToken }),
    release,
  };
}
