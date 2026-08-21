// One fixed key intentionally permits only one live table capture per page.
// Owner tokens make terminal reads and cleanup compare-and-act operations, so
// a superseded flight cannot consume or release the capture that replaced it.
const TABLE_CAPTURE_KEY = "__vibetableE2EScenario18TableProjection";
const CONTENT_REQUEST_TYPES = Object.freeze([
  "schema.getTable",
  "contentProfile.load",
  "recordDocumentLink.list",
]);
const DEFAULT_TIMEOUT_MS = 30_000;
let tableCaptureOwnerSequence = 0;

export class Scenario18RecoveryBoundaryError extends Error {
  constructor(message, evidence = null) {
    super(message);
    this.name = "Scenario18RecoveryBoundaryError";
    this.evidence = evidence;
  }
}

function installTableProjectionCaptureInPage({ captureKey, expectedTable, ownerToken }) {
  const webview = window.chrome?.webview;
  if (!webview) throw new Error("scenario 18 table capture requires WebView2");

  const previous = window[captureKey];
  if (typeof previous?.release === "function") previous.release();
  else if (previous?.handler) webview.removeEventListener("message", previous.handler);
  const capture = {
    ownerToken,
    armed: false,
    terminal: null,
    handler: null,
    originalPostMessage: webview.postMessage,
    postMessage: null,
    release: null,
  };
  capture.release = () => {
    if (capture.handler) {
      try { webview.removeEventListener("message", capture.handler); } catch { /* best effort */ }
      capture.handler = null;
    }
    if (capture.postMessage && webview.postMessage === capture.postMessage) {
      try { webview.postMessage = capture.originalPostMessage; } catch { /* best effort */ }
    }
    capture.postMessage = null;
    capture.originalPostMessage = null;
  };
  const stableCode = (value) => typeof value === "string"
    && /^[A-Za-z0-9_.-]{1,80}$/u.test(value)
    ? value
    : null;
  capture.handler = (event) => {
    if (!capture.armed) return;
    let message = event.data;
    if (typeof message === "string") {
      try { message = JSON.parse(message); } catch { return; }
    }
    if (message?.type === "table.datasetReady" && message.payload?.table === expectedTable) {
      capture.terminal = {
        state: "ready",
        evidence: {
          type: "table.datasetReady",
          operation: null,
          code: null,
          requestId: null,
        },
      };
      capture.release();
      return;
    }
    const requestId = typeof message?.requestId === "string" ? message.requestId : null;
    const operation = message?.payload?.operation;
    if (
      message?.type !== "operation.failed"
      || requestId !== null
      || (operation !== "table.selected" && operation !== "query")
    ) return;
    capture.terminal = {
      state: "failed",
      evidence: {
        type: "operation.failed",
        operation,
        code: stableCode(message.payload?.code ?? message.payload?.error?.code),
        requestId: null,
      },
    };
    capture.release();
  };
  capture.postMessage = (...args) => {
    let message = args[0];
    if (typeof message === "string") {
      try { message = JSON.parse(message); } catch { message = null; }
    }
    if (message?.type === "table.selected") capture.armed = true;
    return capture.originalPostMessage.apply(webview, args);
  };
  window[captureKey] = capture;
  try {
    webview.addEventListener("message", capture.handler);
    webview.postMessage = capture.postMessage;
  } catch (error) {
    capture.release();
    if (window[captureKey] === capture) delete window[captureKey];
    throw error;
  }
}

function readTableProjectionTerminalInPage({ captureKey, ownerToken }) {
  const capture = window[captureKey];
  if (capture?.ownerToken !== ownerToken) return false;
  return capture.terminal ?? false;
}

function releaseTableProjectionCaptureInPage({ captureKey, ownerToken }) {
  const capture = window[captureKey];
  if (capture?.ownerToken !== ownerToken) return;
  if (typeof capture.release === "function") capture.release();
  if (window[captureKey] === capture) delete window[captureKey];
}

function captureRequestIdentityBaselineInPage() {
  const requests = window.__vibetableE2EBridgeDiagnostics?.requests;
  if (!Array.isArray(requests)) {
    throw new Error("scenario 18 content capture requires bridge diagnostics");
  }
  return requests
    .map((request) => request?.requestId)
    .filter((requestId) => typeof requestId === "string");
}

function readContentProjectionTerminalInPage({ knownRequestIds, expectedTypes }) {
  const diagnostics = window.__vibetableE2EBridgeDiagnostics;
  if (!diagnostics || !Array.isArray(diagnostics.requests)
    || !Array.isArray(diagnostics.roundTrips)) return false;
  const known = new Set(knownRequestIds);
  const expected = new Set(expectedTypes);
  const requests = diagnostics.requests.filter((request) =>
    typeof request?.requestId === "string"
    && !known.has(request.requestId)
    && expected.has(request.requestType));
  const requestSummary = (request) => ({
    requestId: request.requestId,
    requestType: request.requestType,
  });
  const stableToken = (value) => typeof value === "string"
    && /^[A-Za-z0-9_.-]{1,80}$/u.test(value)
    ? value
    : null;
  const terminalSummary = (terminal) => ({
    requestId: typeof terminal?.requestId === "string" ? terminal.requestId : null,
    responseType: stableToken(terminal?.responseType),
    operation: stableToken(terminal?.operation),
    code: stableToken(terminal?.code),
  });

  const requestIds = new Set();
  for (const request of requests) {
    if (requestIds.has(request.requestId)) {
      return { state: "failed", evidence: { reason: "duplicate_request" } };
    }
    requestIds.add(request.requestId);
  }
  for (const requestType of expectedTypes) {
    const owned = requests.filter((request) => request.requestType === requestType);
    if (owned.length > 1) {
      return {
        state: "failed",
        evidence: { reason: "duplicate_request", requestType },
      };
    }
  }

  const terminals = diagnostics.roundTrips.filter((terminal) =>
    requestIds.has(terminal?.requestId));
  for (const request of requests) {
    const owned = terminals.filter((terminal) => terminal.requestId === request.requestId);
    if (owned.length > 1) {
      return {
        state: "failed",
        evidence: { reason: "duplicate_terminal", ...requestSummary(request) },
      };
    }
    if (owned.length === 0) continue;
    const terminal = owned[0];
    if (terminal.responseType === "operation.failed") {
      return { state: "failed", evidence: terminalSummary(terminal) };
    }
    if (terminal.responseType !== request.requestType) {
      return {
        state: "failed",
        evidence: {
          reason: "unexpected_terminal",
          ...terminalSummary(terminal),
        },
      };
    }
  }
  if (expectedTypes.some((requestType) =>
    !requests.some((request) => request.requestType === requestType))) return false;
  if (requests.some((request) =>
    !terminals.some((terminal) => terminal.requestId === request.requestId))) return false;
  return {
    state: "ready",
    evidence: {
      requests: requests.map(requestSummary),
      terminals: terminals.map(terminalSummary),
    },
  };
}

function sanitizedWaitError(message, evidence) {
  return new Scenario18RecoveryBoundaryError(message, evidence);
}

async function awaitFreshTableProjection(page, tableId, triggerFreshTable, timeoutMs) {
  const ownerToken = `scenario18-table-${++tableCaptureOwnerSequence}`;
  await page.evaluate(installTableProjectionCaptureInPage, {
    captureKey: TABLE_CAPTURE_KEY,
    expectedTable: tableId,
    ownerToken,
  });
  let handle = null;
  try {
    await triggerFreshTable();
    try {
      handle = await page.waitForFunction(
        readTableProjectionTerminalInPage,
        { captureKey: TABLE_CAPTURE_KEY, ownerToken },
        { timeout: timeoutMs },
      );
    } catch (cause) {
      const reason = cause?.name === "TimeoutError" ? "timeout" : "wait_failed";
      throw sanitizedWaitError("fresh table projection did not reach a terminal", { reason });
    }
    let terminal;
    try {
      terminal = await handle.jsonValue();
    } catch {
      throw sanitizedWaitError("fresh table projection terminal was unreadable", {
        reason: "terminal_unreadable",
      });
    }
    if (terminal?.state !== "ready") {
      throw sanitizedWaitError("fresh table projection failed", terminal?.evidence ?? null);
    }
    return terminal.evidence;
  } finally {
    try {
      if (handle) await handle.dispose();
    } finally {
      await page.evaluate(releaseTableProjectionCaptureInPage, {
        captureKey: TABLE_CAPTURE_KEY,
        ownerToken,
      });
    }
  }
}

async function awaitFreshContentProjection(page, triggerFreshContent, timeoutMs) {
  const knownRequestIds = await page.evaluate(captureRequestIdentityBaselineInPage);
  await triggerFreshContent();
  let handle = null;
  try {
    try {
      handle = await page.waitForFunction(
        readContentProjectionTerminalInPage,
        { knownRequestIds, expectedTypes: CONTENT_REQUEST_TYPES },
        { timeout: timeoutMs },
      );
    } catch (cause) {
      const reason = cause?.name === "TimeoutError" ? "timeout" : "wait_failed";
      throw sanitizedWaitError("fresh content projection did not reach a terminal", { reason });
    }
    let terminal;
    try {
      terminal = await handle.jsonValue();
    } catch {
      throw sanitizedWaitError("fresh content projection terminal was unreadable", {
        reason: "terminal_unreadable",
      });
    }
    if (terminal?.state !== "ready") {
      throw sanitizedWaitError("fresh content projection failed", terminal?.evidence ?? null);
    }
    return terminal.evidence;
  } finally {
    if (handle) await handle.dispose();
  }
}

/**
 * Own scenario 18's authority boundaries. Callers supply only product actions;
 * this module owns projection terminals, correlation, sanitation, and cleanup.
 */
export async function runScenario18RecoveryBoundary({
  page,
  tableId,
  injectFault,
  awaitBackendRecovery,
  prepareFreshTable,
  triggerFreshTable,
  prepareFreshContent,
  triggerFreshContent,
  readFreshContent,
  timeoutMs = DEFAULT_TIMEOUT_MS,
}) {
  await page.getByTestId("table-summary").waitFor({
    state: "attached",
    timeout: timeoutMs,
  });
  const fault = await injectFault();
  await awaitBackendRecovery();
  await prepareFreshTable();
  await awaitFreshTableProjection(page, tableId, triggerFreshTable, timeoutMs);
  await prepareFreshContent();
  await awaitFreshContentProjection(page, triggerFreshContent, timeoutMs);
  const content = await readFreshContent();
  return { fault, content };
}
