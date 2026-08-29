export function installWorkspaceBootstrapCaptureInPage({
  minimumEpoch,
  expectedWorkspaceId,
  expectedLifecycleMethods,
  expectedSessionState = "openedWritable",
}) {
  if (
    expectedWorkspaceId !== null
    && (typeof expectedWorkspaceId !== "string" || !expectedWorkspaceId.trim())
  ) {
    throw new Error("expectedWorkspaceId must be a non-empty string when provided.");
  }
  if (!Array.isArray(expectedLifecycleMethods)) {
    throw new Error("expectedLifecycleMethods must be an array.");
  }
  if (!["openedWritable", "openedProvisional"].includes(expectedSessionState)) {
    throw new Error("expectedSessionState must be openedWritable or openedProvisional.");
  }
  const diagnostics = window.__vibetableE2EBridgeDiagnostics;
  const baselineRequestIds = new Set([
    ...(Array.isArray(diagnostics?.requests) ? diagnostics.requests : []),
    ...(Array.isArray(diagnostics?.recentCompleted) ? diagnostics.recentCompleted : []),
    ...Object.values(diagnostics?.pending ?? {}),
  ].map((request) => request?.requestId).filter(Boolean));
  window.__vibetableE2EBridgeCapture = {
    types: ["workspace.v2.bootstrap"],
    message: null,
    error: null,
    expectedWorkspaceId,
    minimumEpoch,
    expectedLifecycleMethods: [...expectedLifecycleMethods],
    expectedSessionState,
    baselineRequestIds: [...baselineRequestIds],
    bootstrap: null,
    lifecycleSuccess: null,
    unexpectedBootstraps: [],
  };
  const currentLifecycleRequest = (requestId) => {
    if (typeof requestId !== "string" || baselineRequestIds.has(requestId)) return null;
    const requests = window.__vibetableE2EBridgeDiagnostics?.requests;
    if (!Array.isArray(requests)) return null;
    const current = requests.find((request) =>
      !baselineRequestIds.has(request?.requestId)
      && expectedLifecycleMethods.includes(request?.requestType));
    return current?.requestId === requestId ? current : null;
  };
  const matchingLifecycleResult = (bootstrap, success) => {
    const result = success?.result;
    const requiresSessionResult = new Set([
      "workspace.open",
      "workspace.switch",
      "snapshot.openAsNewWorkspace",
    ]).has(success?.method);
    if (
      typeof result?.workspaceId !== "string"
      || !Number.isInteger(result?.sessionEpoch)
      || typeof result?.state !== "string"
    ) {
      return !requiresSessionResult;
    }
    const session = bootstrap?.payload?.session;
    return result.state === expectedSessionState
      && result.workspaceId === session?.workspaceId
      && result.sessionEpoch === session?.sessionEpoch;
  };
  const tryComplete = (handler) => {
    const capture = window.__vibetableE2EBridgeCapture;
    if (!capture?.bootstrap) return;
    const session = capture.bootstrap.payload?.session;
    const observed = window.__vibetableE2EBridgeDiagnostics?.workspaceSession;
    if (
      observed?.workspaceId !== session?.workspaceId
      || observed?.sessionEpoch !== session?.sessionEpoch
    ) {
      return;
    }
    if (expectedLifecycleMethods.length > 0) {
      if (!capture.lifecycleSuccess) return;
      if (!matchingLifecycleResult(capture.bootstrap, capture.lifecycleSuccess)) return;
    }
    capture.message = capture.bootstrap;
    window.chrome.webview.removeEventListener("message", handler);
  };
  window.chrome.webview.addEventListener("message", function handler(event) {
    let message = event.data;
    if (typeof message === "string") {
      try { message = JSON.parse(message); } catch { return; }
    }
    const lifecycleRequest = currentLifecycleRequest(message?.requestId);
    const responseMethod = message?.payload?.method;
    const isOwnedResponse = lifecycleRequest !== null
      && message?.type === "workspace.v2.response"
      && responseMethod === lifecycleRequest.requestType;
    const isOwnedFailure = lifecycleRequest !== null
      && message?.type === "operation.failed";
    if ((isOwnedResponse && message.payload?.ok === false) || isOwnedFailure) {
      window.__vibetableE2EBridgeCapture.error = {
        method: lifecycleRequest.requestType,
        code: message.payload?.error?.code ?? message.payload?.code ?? "unknown",
        message: message.payload?.error?.message
          ?? message.payload?.message
          ?? "workspace operation failed",
      };
      window.chrome.webview.removeEventListener("message", handler);
      return;
    }
    if (isOwnedResponse && message.payload?.ok === true) {
      window.__vibetableE2EBridgeCapture.lifecycleSuccess = {
        requestId: message.requestId,
        method: responseMethod,
        result: message.payload?.result ?? null,
      };
      tryComplete(handler);
      return;
    }
    const session = message?.payload?.session;
    const expectedSessionMode = expectedSessionState === "openedWritable"
      ? session?.openMode === "writable"
        && session?.writable === true
        && session?.provisional === false
      : session?.openMode === "provisional"
        && session?.writable === false
        && session?.provisional === true;
    if (
      message?.type !== "workspace.v2.bootstrap"
      || session?.state !== expectedSessionState
      || !expectedSessionMode
      || !Number.isInteger(session?.sessionEpoch)
      || session.sessionEpoch <= minimumEpoch
    ) {
      return;
    }
    if (expectedWorkspaceId !== null && session.workspaceId !== expectedWorkspaceId) {
      window.__vibetableE2EBridgeCapture.unexpectedBootstraps.push({
        workspaceId: session.workspaceId ?? null,
        sessionEpoch: session.sessionEpoch,
      });
      return;
    }
    window.__vibetableE2EBridgeCapture.bootstrap = message;
    tryComplete(handler);
  });
}
