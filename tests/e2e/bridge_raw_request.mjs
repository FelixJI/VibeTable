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

export async function requestWorkspaceV2InPage({ method, params }) {
  const wirePort = window.__vibetableE2EWorkspaceWirePort;
  if (!wirePort) {
    throw new Error(`workspace wire E2E port unavailable for ${method}`);
  }
  try {
    const result = await wirePort.request({ method, params });
    return { result };
  } catch (error) {
    const code = typeof error?.code === "string" ? error.code : "workspace.operation_failed";
    const detail = { code };
    if (typeof error?.message === "string" && error.message) {
      detail.message = error.message;
    }
    throw new Error(`${method} failed closed: ${JSON.stringify(detail)}`);
  }
}

// Degraded-state lifecycle RPCs (for example workspace.close right after the
// packaged backend exited) must not traverse the serialized UI adapter: the
// host owns the session authority during recovery and the reply envelope may
// carry a post-restart epoch that the UI adapter would reject as stale.
// Sequencing with product traffic is not a concern here because the product
// session is already down; the wire scope still comes from the formal
// allocator so the host-side sequence accounting stays closed.
export async function requestLifecycleWorkspaceV2InPage({ method, params, timeoutMs }) {
  const wirePort = window.__vibetableE2EWorkspaceWirePort;
  if (!wirePort) {
    throw new Error(`workspace wire E2E port unavailable for ${method}`);
  }
  const operationId = crypto.randomUUID();
  const requestId = `e2e-${operationId}`;
  const wire = wirePort.reserve(operationId);
  const envelope = {
    type: "workspace.v2.request",
    requestId,
    payload: { method, params, wire },
    wire,
  };
  const timeout = timeoutMs ?? 20_000;
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      window.chrome.webview.removeEventListener("message", handler);
      reject(new Error(`workspace v2 timeout for ${method}`));
    }, timeout);
    const handler = (event) => {
      let message = event.data;
      if (typeof message === "string") {
        try { message = JSON.parse(message); } catch { return; }
      }
      if (
        !message
        || message.requestId !== requestId
        || !["workspace.v2.response", "workspace.v2.reply", "operation.failed"].includes(message.type)
      ) {
        return;
      }
      clearTimeout(timer);
      window.chrome.webview.removeEventListener("message", handler);
      if (message.type === "operation.failed") {
        reject(new Error(`${method} failed: ${JSON.stringify(message.payload)}`));
        return;
      }
      const reply = message.payload;
      if (reply?.method !== method || reply?.wire?.operationId !== operationId) {
        reject(new Error(`${method} returned a mismatched reply: ${JSON.stringify(reply)}`));
        return;
      }
      if (reply.ok !== true) {
        reject(new Error(`${method} failed closed: ${JSON.stringify(reply.error)}`));
        return;
      }
      resolve(reply);
    };
    window.chrome.webview.addEventListener("message", handler);
    window.chrome.webview.postMessage(envelope);
  });
}
