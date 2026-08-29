export function installWritableWorkspaceBootstrapCaptureInPage({
  minimumEpoch,
  expectedWorkspaceId,
  expectedFailureMethods,
}) {
  if (
    expectedWorkspaceId !== null
    && (typeof expectedWorkspaceId !== "string" || !expectedWorkspaceId.trim())
  ) {
    throw new Error("expectedWorkspaceId must be a non-empty string when provided.");
  }
  window.__vibetableE2EBridgeCapture = {
    types: ["workspace.v2.bootstrap"],
    message: null,
    error: null,
    unexpectedBootstraps: [],
  };
  window.chrome.webview.addEventListener("message", function handler(event) {
    let message = event.data;
    if (typeof message === "string") {
      try { message = JSON.parse(message); } catch { return; }
    }
    if (
      expectedFailureMethods.includes(message?.payload?.method)
      && message?.type === "workspace.v2.response"
      && message.payload?.ok === false
    ) {
      window.__vibetableE2EBridgeCapture.error = {
        method: message.payload.method,
        code: message.payload?.error?.code ?? "unknown",
        message: message.payload?.error?.message ?? "workspace operation failed",
      };
      window.chrome.webview.removeEventListener("message", handler);
      return;
    }
    const session = message?.payload?.session;
    if (
      message?.type !== "workspace.v2.bootstrap"
      || session?.state !== "openedWritable"
      || session?.writable !== true
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
    window.__vibetableE2EBridgeCapture.message = message;
    window.chrome.webview.removeEventListener("message", handler);
  });
}
