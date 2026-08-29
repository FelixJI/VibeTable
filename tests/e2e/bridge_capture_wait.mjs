function readCaptureInPage() {
  return {
    message: window.__vibetableE2EBridgeCapture?.message ?? null,
    error: window.__vibetableE2EBridgeCapture?.error ?? null,
  };
}

export function readCaptureTimeoutEvidenceInPage() {
  const capture = window.__vibetableE2EBridgeCapture;
  const diagnostics = window.__vibetableE2EBridgeDiagnostics;
  if (!capture) return null;
  const session = capture.bootstrap?.payload?.session;
  const baselineRequestIds = new Set(capture.baselineRequestIds ?? []);
  const expectedMethods = new Set(capture.expectedLifecycleMethods ?? []);
  return {
    expectedWorkspaceId: capture.expectedWorkspaceId ?? null,
    minimumEpoch: Number.isInteger(capture.minimumEpoch) ? capture.minimumEpoch : null,
    expectedLifecycleMethods: [...expectedMethods],
    bootstrapSession: session ? {
      workspaceId: session.workspaceId ?? null,
      sessionEpoch: session.sessionEpoch ?? null,
      state: session.state ?? null,
      writable: session.writable === true,
    } : null,
    lifecycleSuccess: capture.lifecycleSuccess ? {
      requestId: capture.lifecycleSuccess.requestId ?? null,
      method: capture.lifecycleSuccess.method ?? null,
      result: {
        workspaceId: capture.lifecycleSuccess.result?.workspaceId ?? null,
        sessionEpoch: capture.lifecycleSuccess.result?.sessionEpoch ?? null,
        state: capture.lifecycleSuccess.result?.state ?? null,
      },
    } : null,
    unexpectedBootstraps: Array.isArray(capture.unexpectedBootstraps)
      ? capture.unexpectedBootstraps.slice(-5)
      : [],
    observedWorkspaceSession: diagnostics?.workspaceSession ?? null,
    lifecycleRequests: Array.isArray(diagnostics?.requests)
      ? diagnostics.requests
        .filter((request) =>
          !baselineRequestIds.has(request?.requestId)
          && expectedMethods.has(request?.requestType))
        .slice(0, 5)
        .map((request) => ({
          requestId: request.requestId ?? null,
          requestType: request.requestType ?? null,
        }))
      : [],
  };
}

export function captureCompletedInPage() {
  const capture = window.__vibetableE2EBridgeCapture;
  return Boolean(capture?.message || capture?.error);
}

export async function waitForCapturedBridgeMessage(page, timeoutMs = 20_000) {
  try {
    await page.waitForFunction(captureCompletedInPage, undefined, {
      polling: 50,
      timeout: timeoutMs,
    });
  } catch (error) {
    if (error?.name === "TimeoutError") {
      let evidence = null;
      try {
        evidence = await page.evaluate(readCaptureTimeoutEvidenceInPage);
      } catch {
        // Preserve the stable timeout if the renderer closed while collecting evidence.
      }
      const suffix = evidence === null ? "" : `: ${JSON.stringify(evidence)}`;
      throw new Error(`captured bridge response timed out${suffix}`, { cause: error });
    }
    throw error;
  }

  const captured = await page.evaluate(readCaptureInPage);
  if (captured.error) {
    throw new Error(
      `captured ${captured.error.method} failure: `
      + `${captured.error.code}: ${captured.error.message}`,
    );
  }
  return captured.message;
}
