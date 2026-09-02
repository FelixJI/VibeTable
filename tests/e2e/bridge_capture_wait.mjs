function readCaptureInPage(expectedCaptureId) {
  const capture = window.__vibetableE2EBridgeCapture;
  if (expectedCaptureId !== undefined && capture?.id !== expectedCaptureId) {
    return {
      message: null,
      error: {
        method: "workspace.activation",
        code: "CAPTURE_REPLACED",
        message: "workspace activation capture ownership changed",
      },
    };
  }
  return {
    message: capture?.message ?? null,
    error: capture?.error ?? null,
  };
}

export function captureCompletedInPage(expectedCaptureId) {
  const capture = window.__vibetableE2EBridgeCapture;
  if (expectedCaptureId !== undefined && capture?.id !== expectedCaptureId) return true;
  return Boolean(capture?.message || capture?.error);
}

function releaseCaptureInPage(expectedCaptureId) {
  const capture = window.__vibetableE2EBridgeCapture;
  if (capture?.id === expectedCaptureId && typeof capture.release === "function") {
    capture.release();
  }
}

function installWorkspaceActivationCaptureInPage(configuration) {
  const webview = window.chrome?.webview;
  if (!webview || typeof webview.postMessage !== "function") {
    throw new Error("WebView bridge is unavailable for workspace activation capture");
  }

  const parseMessage = (candidate) => {
    if (typeof candidate !== "string") return candidate;
    try { return JSON.parse(candidate); } catch { return null; }
  };
  const normalizeWorkspaceId = (candidate) => {
    if (typeof candidate !== "string") return null;
    const normalized = candidate.toLowerCase();
    return /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
      .test(normalized)
      ? normalized
      : null;
  };
  const validOperationId = (candidate) => typeof candidate === "string"
    && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
      .test(candidate);
  const sameWire = (left, right) => {
    if (!left || typeof left !== "object" || Array.isArray(left)
      || !right || typeof right !== "object" || Array.isArray(right)) return false;
    const leftKeys = Object.keys(left).sort();
    const rightKeys = Object.keys(right).sort();
    return leftKeys.length === rightKeys.length
      && leftKeys.every((key, index) => key === rightKeys[index] && left[key] === right[key]);
  };
  const nextCaptureId = Number.isSafeInteger(window.__vibetableE2EBridgeCaptureSequence)
    ? window.__vibetableE2EBridgeCaptureSequence + 1
    : 1;
  window.__vibetableE2EBridgeCaptureSequence = nextCaptureId;
  const previousCapture = window.__vibetableE2EBridgeCapture;
  if (previousCapture && typeof previousCapture.release === "function") {
    const previousWasActive = !previousCapture.message && !previousCapture.error;
    if (previousWasActive) {
      previousCapture.error = {
        method: previousCapture.method,
        code: "CAPTURE_REPLACED",
        message: "workspace activation capture ownership changed",
      };
    }
    previousCapture.release();
    if (previousWasActive && previousCapture.id === undefined) {
      throw new Error("workspace activation capture replaced an unaddressable active owner");
    }
  }

  const previousPostMessage = webview.postMessage;
  let listenerInstalled = false;
  let wrapperInstalled = false;
  const capture = {
    id: nextCaptureId,
    method: configuration.method,
    message: null,
    error: null,
    owner: null,
    terminal: null,
    session: null,
    databaseOpened: null,
    released: false,
    release: null,
  };

  const release = () => {
    if (capture.released) return;
    if (!capture.message && !capture.error) {
      capture.error = {
        method: configuration.method,
        code: "CAPTURE_RELEASED",
        message: "workspace activation capture was released before readiness",
      };
    }
    capture.released = true;
    if (listenerInstalled) {
      webview.removeEventListener("message", onMessage);
      listenerInstalled = false;
    }
    if (wrapperInstalled && webview.postMessage === wrappedPostMessage) {
      webview.postMessage = previousPostMessage;
    }
    wrapperInstalled = false;
  };
  capture.release = release;

  const fail = (code, message) => {
    if (capture.message || capture.error) return;
    capture.error = { method: configuration.method, code, message };
    release();
  };

  const tryComplete = () => {
    if (capture.message || capture.error || !capture.terminal || !capture.session) return;
    const terminal = capture.terminal;
    const session = capture.session;
    if (
      terminal.workspaceId !== session.workspaceId
      || terminal.sessionEpoch !== session.sessionEpoch
    ) {
      fail(
        "CAPTURE_SESSION_IDENTITY_MISMATCH",
        "workspace terminal and bootstrap session identity differ",
      );
      return;
    }
    if (terminal.state !== session.state) {
      fail("CAPTURE_SESSION_MODE_MISMATCH", "workspace terminal and bootstrap state differ");
      return;
    }
    if (!capture.databaseOpened) return;
    const identity = session.workspaceId.replaceAll("-", "");
    const expectedProjectKey = `local:${identity}`;
    const expectedProjectRevision = `${identity}:${session.sessionEpoch}`;
    if (
      capture.databaseOpened.payload?.projectKey !== expectedProjectKey
      || capture.databaseOpened.payload?.projectRevision !== expectedProjectRevision
    ) {
      fail(
        "CAPTURE_PROJECT_IDENTITY_MISMATCH",
        "database readiness does not belong to the activated workspace session",
      );
      return;
    }
    capture.message = capture.databaseOpened;
    release();
  };

  const recordTerminal = (message) => {
    if (!capture.owner || message?.requestId !== capture.owner.requestId) return;
    const payload = message?.payload;
    const outerOperationId = message?.wire?.operationId;
    const payloadOperationId = payload?.wire?.operationId;
    if (
      payload?.method !== capture.owner.method
      || !sameWire(message?.wire, payload?.wire)
      || !sameWire(message?.wire, capture.owner.wire)
      || outerOperationId !== capture.owner.operationId
      || payloadOperationId !== capture.owner.operationId
    ) {
      fail(
        "CAPTURE_TERMINAL_IDENTITY_MISMATCH",
        "workspace terminal does not match its outbound request identity",
      );
      return;
    }
    if (payload.ok === false) {
      const rawCode = payload.error?.code;
      const code = typeof rawCode === "string" && /^[a-z0-9_.-]{1,80}$/i.test(rawCode)
        ? rawCode
        : "workspace.operation_failed";
      fail(code, "workspace activation owner returned a failure terminal");
      return;
    }
    const workspaceId = normalizeWorkspaceId(payload.result?.workspaceId);
    const sessionEpoch = payload.result?.sessionEpoch;
    const state = payload.result?.state;
    if (
      payload.ok !== true
      || workspaceId === null
      || !Number.isSafeInteger(sessionEpoch)
      || sessionEpoch < 1
      || !["openedWritable", "openedProvisional"].includes(state)
    ) {
      fail("CAPTURE_TERMINAL_INVALID", "workspace activation owner returned an invalid terminal");
      return;
    }
    const observed = { workspaceId, sessionEpoch, state };
    if (capture.terminal) {
      if (JSON.stringify(capture.terminal) !== JSON.stringify(observed)) {
        fail(
          "CAPTURE_TERMINAL_DUPLICATE",
          "workspace activation owner returned conflicting terminals",
        );
      }
      return;
    }
    capture.terminal = observed;
    tryComplete();
  };

  const recordBootstrap = (message) => {
    if (!capture.owner) return;
    const session = message?.payload?.session;
    if (!["openedWritable", "openedProvisional"].includes(session?.state)) return;
    const workspaceId = normalizeWorkspaceId(session?.workspaceId);
    const sessionEpoch = session?.sessionEpoch;
    const writable = session?.state === "openedWritable"
      && session?.openMode === "writable"
      && session?.writable === true
      && session?.provisional === false;
    const provisional = session?.state === "openedProvisional"
      && session?.openMode === "provisional"
      && session?.writable === false
      && session?.provisional === true;
    if (
      workspaceId === null
      || !Number.isSafeInteger(sessionEpoch)
      || sessionEpoch < 1
      || typeof session?.contractVersion !== "string"
      || !session.contractVersion
      || typeof session?.phase !== "string"
      || session.phase !== "idle"
      || session.errorCode !== null
      || (!writable && !provisional)
    ) {
      fail("CAPTURE_SESSION_MODE_MISMATCH", "workspace bootstrap session is not ready");
      return;
    }
    const observed = {
      workspaceId,
      sessionEpoch,
      state: session.state,
      openMode: session.openMode,
      writable: session.writable,
      provisional: session.provisional,
    };
    if (capture.session && JSON.stringify(capture.session) !== JSON.stringify(observed)) {
      fail("CAPTURE_SESSION_IDENTITY_MISMATCH", "workspace bootstrap identity changed");
      return;
    }
    capture.session = observed;
    tryComplete();
  };

  const recordDatabaseOpened = (message) => {
    if (!capture.owner) return;
    const projectKey = message?.payload?.projectKey;
    const projectRevision = message?.payload?.projectRevision;
    if (
      typeof projectKey !== "string"
      || !projectKey
      || typeof projectRevision !== "string"
      || !projectRevision
    ) {
      fail("CAPTURE_PROJECT_IDENTITY_MISMATCH", "database readiness identity is invalid");
      return;
    }
    if (capture.databaseOpened) {
      if (
        capture.databaseOpened.payload.projectKey !== projectKey
        || capture.databaseOpened.payload.projectRevision !== projectRevision
      ) {
        fail(
          "CAPTURE_PROJECT_DUPLICATE",
          "database readiness identity changed during activation",
        );
      }
      return;
    }
    capture.databaseOpened = message;
    tryComplete();
  };

  function onMessage(event) {
    if (capture.message || capture.error) return;
    const message = parseMessage(event.data);
    if (message?.type === "workspace.v2.response") recordTerminal(message);
    else if (message?.type === "workspace.v2.bootstrap") recordBootstrap(message);
    else if (message?.type === "database.opened") recordDatabaseOpened(message);
  }

  function wrappedPostMessage(...args) {
    const message = parseMessage(args[0]);
    const isWorkspaceRequest = message?.type === "workspace.v2.request";
    if (
      isWorkspaceRequest
      && capture.owner
      && message?.requestId === capture.owner.requestId
      && message?.payload?.method !== capture.owner.method
    ) {
      fail(
        "CAPTURE_OUTBOUND_IDENTITY_MISMATCH",
        "workspace activation requestId changed method",
      );
    }
    const isTarget = isWorkspaceRequest && message?.payload?.method === configuration.method;
    if (isTarget && !capture.owner && !capture.error) {
      const requestId = message?.requestId;
      const outerOperationId = message?.wire?.operationId;
      const payloadOperationId = message?.payload?.wire?.operationId;
      if (
        typeof requestId !== "string"
        || !requestId
        || !validOperationId(outerOperationId)
        || outerOperationId !== payloadOperationId
        || !sameWire(message?.wire, message?.payload?.wire)
      ) {
        fail(
          "CAPTURE_OUTBOUND_IDENTITY_MISMATCH",
          "workspace activation request has inconsistent wire identity",
        );
      } else {
        capture.owner = {
          requestId,
          operationId: outerOperationId,
          method: configuration.method,
          wire: { ...message.wire },
        };
      }
    } else if (isTarget && capture.owner && message.requestId === capture.owner.requestId) {
      if (
        !sameWire(message?.wire, message?.payload?.wire)
        || !sameWire(message?.wire, capture.owner.wire)
      ) {
        fail(
          "CAPTURE_OUTBOUND_IDENTITY_MISMATCH",
          "workspace activation owner wire identity changed",
        );
      }
    }
    try {
      return previousPostMessage.apply(webview, args);
    } catch (error) {
      if (isTarget) fail("CAPTURE_POST_FAILED", "workspace activation request could not be posted");
      throw error;
    }
  }

  try {
    webview.addEventListener("message", onMessage);
    listenerInstalled = true;
    webview.postMessage = wrappedPostMessage;
    wrapperInstalled = true;
    window.__vibetableE2EBridgeCapture = capture;
    return nextCaptureId;
  } catch (error) {
    release();
    throw error;
  }
}

export async function beginWorkspaceActivationCapture(page, { method }) {
  if (typeof method !== "string" || !method) {
    throw new Error("workspace activation capture requires a method");
  }
  const captureId = await page.evaluate(installWorkspaceActivationCaptureInPage, { method });
  let releasePromise = null;
  const release = () => {
    releasePromise ??= page.evaluate(releaseCaptureInPage, captureId);
    return releasePromise;
  };
  return {
    async wait(timeoutMs = 20_000) {
      try {
        return await waitForCapturedBridgeMessage(page, timeoutMs, captureId);
      } finally {
        await release();
      }
    },
    release,
  };
}

export async function waitForCapturedBridgeMessage(
  page,
  timeoutMs = 20_000,
  expectedCaptureId = undefined,
) {
  try {
    await page.waitForFunction(captureCompletedInPage, expectedCaptureId, {
      polling: 50,
      timeout: timeoutMs,
    });
  } catch (error) {
    if (error?.name === "TimeoutError") {
      await page.evaluate(releaseCaptureInPage, expectedCaptureId);
      throw new Error("captured bridge response timed out", { cause: error });
    }
    throw error;
  }

  const captured = await page.evaluate(readCaptureInPage, expectedCaptureId);
  if (captured.error) {
    throw new Error(
      `captured ${captured.error.method} failure: `
      + `${captured.error.code}: ${captured.error.message}`,
    );
  }
  return captured.message;
}
