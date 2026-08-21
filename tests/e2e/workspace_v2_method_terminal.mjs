export function installWorkspaceV2MethodTerminalCaptureInPage(expectedMethod) {
  if (typeof expectedMethod !== "string" || expectedMethod.length === 0) {
    throw new Error("workspace.v2 method terminal capture requires a method");
  }
  const webview = window.chrome?.webview;
  if (!webview) throw new Error("workspace.v2 method terminal capture requires WebView2");

  window.__vibetableE2EBridgeCapture = {
    types: ["workspace.v2.response", "operation.failed"],
    message: null,
    error: null,
  };
  webview.addEventListener("message", function handler(event) {
    let message = event.data;
    if (typeof message === "string") {
      try { message = JSON.parse(message); } catch { return; }
    }
    const requestId = typeof message?.requestId === "string"
      && message.requestId.trim().length > 0
      ? message.requestId
      : null;
    if (requestId === null) return;

    const diagnostics = window.__vibetableE2EBridgeDiagnostics;
    const pending = diagnostics?.pending && typeof diagnostics.pending === "object"
      ? Object.values(diagnostics.pending)
      : [];
    const outboundOwnsFailure = [
      ...(Array.isArray(diagnostics?.requests) ? diagnostics.requests : []),
      ...pending,
      ...(Array.isArray(diagnostics?.recentCompleted) ? diagnostics.recentCompleted : []),
    ].some((request) => request?.requestId === requestId
      && request.requestType === expectedMethod);

    let terminal = null;
    if (message.type === "workspace.v2.response"
      && message.payload?.method === expectedMethod) {
      terminal = message;
    } else if (message.type === "operation.failed"
      && (message.payload?.operation === expectedMethod || outboundOwnsFailure)) {
      const rawCode = message.payload?.code ?? message.payload?.error?.code;
      terminal = {
        type: "operation.failed",
        requestId,
        operation: expectedMethod,
        code: typeof rawCode === "string" ? rawCode : null,
      };
    }
    if (terminal === null) return;

    window.__vibetableE2EBridgeCapture.message = terminal;
    webview.removeEventListener("message", handler);
  });
}
