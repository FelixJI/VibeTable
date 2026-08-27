export function installBridgeDiagnosticsInPage() {
  if (window.__vibetableE2EBridgeDiagnostics?.installed) return;

  const diagnostics = {
    installed: true,
    installedAt: new Date().toISOString(),
    requests: [],
    notifications: [],
    roundTrips: [],
    recentCompleted: [],
    failures: [],
    diagnosticCursor: 0,
    pending: {},
    workspaceSession: null,
    maxWorkspaceSequence: 0,
    dialogFocus: {
      cursor: 0,
      events: [],
    },
  };
  const webview = window.chrome.webview;
  const maxEntries = 200;
  const pushBounded = (items, value, onEvicted) => {
    items.push(value);
    if (items.length <= maxEntries) return;
    const evicted = items.shift();
    onEvicted?.(evicted);
  };
  const pushDiagnostic = (items, value) => {
    diagnostics.diagnosticCursor += 1;
    pushBounded(items, {
      cursor: diagnostics.diagnosticCursor,
      ...value,
    });
  };
  const dialogFocusTargets = new Set(["attachment", "json"]);
  const dialogFocusPendingReasons = new Set(["grid", "row", "cell", "focus-rejected"]);
  const dialogFocusCancellationReasons = new Set([
    "scope",
    "window",
    "external",
    "disposed",
    "stale",
  ]);
  const dialogFocusRestoreTargets = new Set(["captured", "reprojected"]);
  const hasExactKeys = (value, expected) => {
    if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
    const keys = Object.keys(value);
    return keys.length === expected.length && expected.every((key) => keys.includes(key));
  };
  window.addEventListener?.("vibetable:e2e-dialog-focus-outcome", (event) => {
    const detail = event?.detail;
    if (!Number.isSafeInteger(detail?.leaseId) || detail.leaseId <= 0) return;
    if (!dialogFocusTargets.has(detail.target)) return;
    let outcome = null;
    if ((detail.state === "claimed" || detail.state === "released")
      && hasExactKeys(detail, ["leaseId", "state", "target"])) {
      outcome = { state: detail.state };
    } else if (detail.state === "restored"
      && hasExactKeys(detail, ["leaseId", "state", "target", "via"])
      && dialogFocusRestoreTargets.has(detail.via)) {
      outcome = { state: detail.state, via: detail.via };
    } else if (detail.state === "pending"
      && hasExactKeys(detail, ["leaseId", "state", "target", "reason"])
      && dialogFocusPendingReasons.has(detail.reason)) {
      outcome = { state: detail.state, reason: detail.reason };
    } else if (detail.state === "cancelled"
      && hasExactKeys(detail, ["leaseId", "state", "target", "reason"])
      && dialogFocusCancellationReasons.has(detail.reason)) {
      outcome = { state: detail.state, reason: detail.reason };
    }
    if (outcome === null) return;
    diagnostics.dialogFocus.cursor += 1;
    pushBounded(diagnostics.dialogFocus.events, {
      cursor: diagnostics.dialogFocus.cursor,
      leaseId: detail.leaseId,
      target: detail.target,
      ...outcome,
    });
  });
  const messageLength = (message) => typeof message === "string"
    ? message.length
    : null;
  const diagnosticCodes = new Set([
    "BACKEND_UNAVAILABLE",
    "BAD_PAYLOAD",
    "CANCELLED",
    "CAPABILITY_NOT_PUBLIC",
    "DASHBOARD_CANCELLED",
    "PRODUCT_DATA_FAILED",
    "SCHEMA_LIFECYCLE_CANCELLED",
    "SCHEMA_LIFECYCLE_TIMEOUT",
    "UNKNOWN_TYPE",
    "UNKNOWN_V2_METHOD",
    "WORKSPACE_ERROR",
    "dashboard_edit_conflict",
    "history.field_not_found",
    "preset_edit_conflict",
    "retention.policy_revision_stale",
    "snapshot.package_invalid",
    "workspace.operation_failed",
    "workspace.session_stale",
  ]);
  const stableCode = (value) => diagnosticCodes.has(value) ? value : null;
  // The outbound bridge is the authority for request type names. Reuse the
  // bounded, sanitized observation ledger as a dynamic closed catalog so new
  // protocol operations remain diagnosable without accepting arbitrary host
  // strings into artifacts.
  const stableOperation = (value) => typeof value === "string"
    && (
      diagnostics.requests.some(request => request.requestType === value)
      || diagnostics.notifications.some(notification => notification.requestType === value)
    )
    ? value
    : null;
  const describePayload = (requestPayload) => requestPayload
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
      const payloadShape = describePayload(message.payload);
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
      pushBounded(diagnostics.requests, request, (evicted) => {
        if (diagnostics.pending[evicted.requestId] === evicted) {
          delete diagnostics.pending[evicted.requestId];
        }
      });
      diagnostics.pending[message.requestId] = request;
    } else if (message?.type) {
      const recoveryWindow = window.__vibetableE2ESidecarRecoveryFailureWindow;
      pushDiagnostic(diagnostics.notifications, {
        requestType: message.type,
        payloadShape: describePayload(message.payload),
        startedAt: new Date().toISOString(),
        recoveryOwnerToken: message.type === "table.selected"
          && message.payload?.table === recoveryWindow?.tableId
          ? recoveryWindow.ownerToken
          : null,
      });
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
    const rawRequestId = typeof message?.requestId === "string"
      ? message.requestId
      : null;
    const request = rawRequestId
      ? diagnostics.pending[rawRequestId]
      : null;
    const isFailure = message?.type === "operation.failed"
      || message?.ok === false
      || message?.payload?.ok === false;
    if (!request) {
      if (isFailure) {
        let completedRequest = null;
        for (let index = diagnostics.recentCompleted.length - 1; index >= 0; index -= 1) {
          if (diagnostics.recentCompleted[index].requestId === rawRequestId) {
            completedRequest = diagnostics.recentCompleted[index];
            break;
          }
        }
        const rawMessage = message?.payload?.message
          ?? message?.payload?.error?.message
          ?? message?.error?.message
          ?? null;
        pushDiagnostic(diagnostics.failures, {
          requestId: rawRequestId,
          requestType: completedRequest?.requestType ?? null,
          payloadShape: completedRequest?.payloadShape ?? null,
          responseType: message?.type ?? null,
          code: stableCode(message?.payload?.code
            ?? message?.payload?.error?.code
            ?? message?.error?.code
            ?? null),
          messageLength: messageLength(rawMessage),
          operation: stableOperation(message?.payload?.operation),
          startedAt: completedRequest?.startedAt ?? null,
          finishedAt: new Date().toISOString(),
          durationMs: completedRequest === null
            ? null
            : Math.round((performance.now() - completedRequest.startedMonotonicMs) * 100) / 100,
        });
      }
      return;
    }
    const roundTrip = {
      requestId: request.requestId,
      requestType: request.requestType,
      payloadShape: request.payloadShape,
      responseType: message.type ?? null,
      code: stableCode(message.payload?.code
        ?? message.payload?.error?.code
        ?? message.error?.code
        ?? null),
      messageLength: messageLength(
        message.payload?.message
          ?? message.payload?.error?.message
          ?? message.error?.message
          ?? null,
      ),
      operation: stableOperation(message.payload?.operation),
      startedAt: request.startedAt,
      finishedAt: new Date().toISOString(),
      durationMs: Math.round((performance.now() - request.startedMonotonicMs) * 100) / 100,
    };
    pushBounded(diagnostics.roundTrips, roundTrip);
    pushBounded(diagnostics.recentCompleted, request);
    if (isFailure) {
      pushDiagnostic(diagnostics.failures, roundTrip);
    }
    delete diagnostics.pending[message.requestId];
  });
  window.__vibetableE2EBridgeDiagnostics = diagnostics;
}

export function readBridgeDiagnosticsInPage() {
  const diagnostics = window.__vibetableE2EBridgeDiagnostics;
  if (!diagnostics) return null;
  const now = performance.now();
  return {
    installedAt: diagnostics.installedAt,
    diagnosticCursor: diagnostics.diagnosticCursor,
    requests: diagnostics.requests.map((request) => ({
      requestId: request.requestId,
      requestType: request.requestType,
      payloadShape: request.payloadShape,
      startedAt: request.startedAt,
    })),
    notifications: diagnostics.notifications.map((notification) => ({
      requestType: notification.requestType,
      payloadShape: notification.payloadShape,
      startedAt: notification.startedAt,
    })),
    roundTrips: diagnostics.roundTrips,
    failures: diagnostics.failures,
    acknowledgedFailures: diagnostics.acknowledgedFailures ?? [],
    pending: Object.values(diagnostics.pending).map((request) => ({
      requestId: request.requestId,
      requestType: request.requestType,
      payloadShape: request.payloadShape,
      startedAt: request.startedAt,
      pendingMs: Math.round((now - request.startedMonotonicMs) * 100) / 100,
    })),
    dialogFocus: {
      cursor: diagnostics.dialogFocus.cursor,
      events: diagnostics.dialogFocus.events,
    },
  };
}
