export function installBridgeDiagnosticsInPage() {
  if (window.__vibetableE2EBridgeDiagnostics?.installed) return;

  const diagnostics = {
    installed: true,
    installedAt: new Date().toISOString(),
    requests: [],
    roundTrips: [],
    failures: [],
    pending: {},
    workspaceSession: null,
    maxWorkspaceSequence: 0,
  };
  const webview = window.chrome.webview;
  const originalPostMessage = webview.postMessage.bind(webview);
  webview.postMessage = (...args) => {
    const candidate = args[0];
    let message = candidate;
    if (typeof candidate === "string") {
      try { message = JSON.parse(candidate); } catch { message = null; }
    }
    const workspaceWires = [
      message?.scope,
      message?.wire,
      message?.payload?.wire,
    ].filter((wire, index, values) =>
      wire?.scope === "workspace"
      && Number.isSafeInteger(wire.sequence)
      && values.indexOf(wire) === index,
    );
    if (workspaceWires.length > 0) {
      diagnostics.maxWorkspaceSequence = Math.max(
        diagnostics.maxWorkspaceSequence,
        ...workspaceWires.map(wire => wire.sequence),
      );
    }
    if (message?.requestId && message?.type) {
      const requestPayload = message.payload;
      const payloadShape = requestPayload
        && typeof requestPayload === "object"
        && !Array.isArray(requestPayload)
        ? Object.fromEntries(Object.entries(requestPayload).map(([key, value]) => [
          key,
          typeof value === "string"
            ? { kind: "string", length: value.length }
            : Array.isArray(value)
              ? { kind: "array", length: value.length }
              : { kind: value === null ? "null" : typeof value },
        ]))
        : null;
      const request = {
        requestId: message.requestId,
        requestType: message.type === "workspace.v2.request"
          && typeof message.payload?.method === "string"
          ? message.payload.method
          : message.type,
        payloadShape,
        startedAt: new Date().toISOString(),
        startedMonotonicMs: performance.now(),
      };
      diagnostics.requests.push(request);
      diagnostics.pending[message.requestId] = request;
    }
    return originalPostMessage(...args);
  };
  webview.addEventListener("message", (event) => {
    let message = event.data;
    if (typeof message === "string") {
      try { message = JSON.parse(message); } catch { return; }
    }
    const inboundWire = message?.wire ?? message?.payload?.wire;
    if (
      inboundWire?.scope === "workspace"
      && Number.isSafeInteger(inboundWire.sequence)
    ) {
      diagnostics.maxWorkspaceSequence = Math.max(
        diagnostics.maxWorkspaceSequence,
        inboundWire.sequence,
      );
    }
    if (message?.type === "workspace.v2.bootstrap") {
      const session = message.payload?.session;
      diagnostics.workspaceSession =
        typeof session?.workspaceId === "string"
        && Number.isSafeInteger(session?.sessionEpoch)
        && session.sessionEpoch > 0
          ? {
              workspaceId: session.workspaceId,
              sessionEpoch: session.sessionEpoch,
            }
          : null;
    }
    const request = message?.requestId
      ? diagnostics.pending[message.requestId]
      : null;
    if (!request) return;
    const roundTrip = {
      requestId: request.requestId,
      requestType: request.requestType,
      payloadShape: request.payloadShape,
      responseType: message.type ?? null,
      code: message.payload?.code
        ?? message.payload?.error?.code
        ?? message.error?.code
        ?? null,
      message: message.payload?.message
        ?? message.payload?.error?.message
        ?? message.error?.message
        ?? null,
      startedAt: request.startedAt,
      finishedAt: new Date().toISOString(),
      durationMs: Math.round((performance.now() - request.startedMonotonicMs) * 100) / 100,
    };
    diagnostics.roundTrips.push(roundTrip);
    if (
      message.type === "operation.failed"
      || message.ok === false
      || message.payload?.ok === false
    ) {
      diagnostics.failures.push(roundTrip);
    }
    delete diagnostics.pending[message.requestId];
  });
  window.__vibetableE2EBridgeDiagnostics = diagnostics;
}
