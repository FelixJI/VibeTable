export function beginRawBridgeRequestInPage({
  requestType,
  requestPayload,
  responseTypes,
}) {
  const operationId = crypto.randomUUID();
  const requestId = `e2e-${operationId}`;
  const wirePort = window.__vibetableE2EWorkspaceWirePort;
  if (!wirePort) {
    throw new Error(`workspace wire E2E port unavailable for ${requestType}`);
  }
  const scope = wirePort.reserve(operationId);
  window.__vibetableE2ERawRequests ??= {};
  window.__vibetableE2ERawRequests[requestId] = {
    responseTypes,
    message: null,
  };
  window.chrome.webview.addEventListener("message", function handler(event) {
    let message = event.data;
    if (typeof message === "string") {
      try { message = JSON.parse(message); } catch { return; }
    }
    if (!message || message.requestId !== requestId) return;
    if (!responseTypes.includes(message.type) && message.type !== "operation.failed") return;
    window.__vibetableE2ERawRequests[requestId].message = message;
    window.chrome.webview.removeEventListener("message", handler);
  });
  window.chrome.webview.postMessage({
    type: requestType,
    requestId,
    payload: requestPayload,
    scope,
  });
  return requestId;
}
