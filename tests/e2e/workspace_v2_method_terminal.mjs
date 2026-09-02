export function installWorkspaceV2MethodTerminalCaptureInPage(expectedMethod) {
  if (typeof expectedMethod !== "string" || expectedMethod.length === 0) {
    throw new Error("workspace.v2 method terminal capture requires a method");
  }
  const webview = window.chrome?.webview;
  if (!webview || typeof webview.postMessage !== "function") {
    throw new Error("workspace.v2 method terminal capture requires WebView2");
  }

  const parseMessage = (candidate) => {
    if (typeof candidate !== "string") return candidate;
    try { return JSON.parse(candidate); } catch { return null; }
  };
  const validUuid = (candidate) => typeof candidate === "string"
    && /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
      .test(candidate);
  const sameWire = (left, right) => {
    if (!left || typeof left !== "object" || Array.isArray(left)
      || !right || typeof right !== "object" || Array.isArray(right)) return false;
    const leftKeys = Object.keys(left).sort();
    const rightKeys = Object.keys(right).sort();
    return leftKeys.length === rightKeys.length
      && leftKeys.every((key, index) => key === rightKeys[index] && left[key] === right[key]);
  };
  const hasExactKeys = (value, keys) => value
    && typeof value === "object"
    && !Array.isArray(value)
    && Object.keys(value).sort().join(",") === [...keys].sort().join(",");
  const validWire = (wire) => {
    const validCommon = validUuid(wire?.operationId)
      && Number.isSafeInteger(wire?.sequence)
      && wire.sequence >= 0;
    if (wire?.scope === "global") {
      return validCommon && hasExactKeys(wire, ["scope", "operationId", "sequence"]);
    }
    return wire?.scope === "workspace"
      && validCommon
      && hasExactKeys(
        wire,
        ["scope", "workspaceId", "sessionEpoch", "operationId", "sequence"],
      )
      && validUuid(wire.workspaceId)
      && Number.isSafeInteger(wire.sessionEpoch)
      && wire.sessionEpoch >= 1;
  };

  const previousCapture = window.__vibetableE2EBridgeCapture;
  if (previousCapture && typeof previousCapture.release === "function") {
    if (!previousCapture.message && !previousCapture.error) {
      previousCapture.error = {
        method: previousCapture.method,
        code: "CAPTURE_REPLACED",
        message: "workspace method terminal capture ownership changed",
      };
      previousCapture.release();
      throw new Error("workspace method terminal capture replaced an active owner");
    }
    previousCapture.release();
  }
  const previousPostMessage = webview.postMessage;
  let listenerInstalled = false;
  let wrapperInstalled = false;
  const capture = {
    types: ["workspace.v2.response", "operation.failed"],
    method: expectedMethod,
    message: null,
    error: null,
    owner: null,
    released: false,
    release: null,
  };

  const release = () => {
    if (capture.released) return;
    if (!capture.message && !capture.error) {
      capture.error = {
        method: expectedMethod,
        code: "CAPTURE_RELEASED",
        message: "workspace method terminal capture was released before completion",
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
    capture.error = { method: expectedMethod, code, message };
    release();
  };

  function onMessage(event) {
    if (capture.message || capture.error || !capture.owner) return;
    const message = parseMessage(event.data);
    if (message?.requestId !== capture.owner.requestId) return;
    if (message?.type === "workspace.v2.response") {
      if (
        message.payload?.method !== capture.owner.method
        || !validWire(message.wire)
        || !validWire(message.payload?.wire)
        || !sameWire(message.wire, message.payload.wire)
        || !sameWire(message.wire, capture.owner.wire)
      ) {
        fail(
          "CAPTURE_TERMINAL_IDENTITY_MISMATCH",
          "workspace terminal does not match its outbound request identity",
        );
        return;
      }
      capture.message = message;
      release();
      return;
    }
    if (message?.type !== "operation.failed") return;
    const operation = message.payload?.operation;
    const operationId = message.payload?.operationId;
    const outerWire = message.wire;
    const payloadWire = message.payload?.wire;
    const optionalWireMatches = (wire) => wire === undefined
      || (validWire(wire) && sameWire(wire, capture.owner.wire));
    if (
      (operation !== undefined && operation !== null && operation !== capture.owner.method)
      || (operationId !== undefined
        && operationId !== null
        && operationId !== capture.owner.operationId)
      || !optionalWireMatches(outerWire)
      || !optionalWireMatches(payloadWire)
    ) {
      fail(
        "CAPTURE_TERMINAL_IDENTITY_MISMATCH",
        "workspace failure does not match its outbound request identity",
      );
      return;
    }
    const rawCode = message.payload?.code ?? message.payload?.error?.code;
    capture.message = {
      type: "operation.failed",
      requestId: capture.owner.requestId,
      operation: capture.owner.method,
      code: typeof rawCode === "string" && /^[a-z0-9_.-]{1,80}$/i.test(rawCode)
        ? rawCode
        : null,
    };
    release();
  }

  function wrappedPostMessage(...args) {
    const message = parseMessage(args[0]);
    const isWorkspaceRequest = message?.type === "workspace.v2.request";
    if (
      isWorkspaceRequest
      && capture.owner
      && message.requestId === capture.owner.requestId
      && message.payload?.method !== capture.owner.method
    ) {
      fail(
        "CAPTURE_OUTBOUND_IDENTITY_MISMATCH",
        "workspace requestId changed method",
      );
    }
    const isTarget = isWorkspaceRequest && message?.payload?.method === expectedMethod;
    if (isTarget && !capture.owner && !capture.error) {
      if (
        typeof message.requestId !== "string"
        || message.requestId.trim().length === 0
        || !validWire(message.wire)
        || !validWire(message.payload?.wire)
        || !sameWire(message.wire, message.payload.wire)
      ) {
        fail(
          "CAPTURE_OUTBOUND_IDENTITY_MISMATCH",
          "workspace request has inconsistent wire identity",
        );
      } else {
        capture.owner = {
          requestId: message.requestId,
          method: expectedMethod,
          operationId: message.wire.operationId,
          wire: { ...message.wire },
        };
      }
    } else if (isTarget && capture.owner && message.requestId === capture.owner.requestId) {
      if (
        !validWire(message.wire)
        || !validWire(message.payload?.wire)
        || !sameWire(message.wire, message.payload.wire)
        || !sameWire(message.wire, capture.owner.wire)
      ) {
        fail(
          "CAPTURE_OUTBOUND_IDENTITY_MISMATCH",
          "workspace request wire identity changed",
        );
      }
    }
    try {
      return previousPostMessage.apply(webview, args);
    } catch (error) {
      if (isTarget && capture.owner?.requestId === message.requestId) {
        fail("CAPTURE_POST_FAILED", "workspace request could not be posted");
      }
      throw error;
    }
  }

  try {
    webview.addEventListener("message", onMessage);
    listenerInstalled = true;
    webview.postMessage = wrappedPostMessage;
    wrapperInstalled = true;
    window.__vibetableE2EBridgeCapture = capture;
  } catch (error) {
    release();
    throw error;
  }
}
