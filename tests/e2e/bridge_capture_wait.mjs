function readCaptureInPage() {
  return {
    message: window.__vibetableE2EBridgeCapture?.message ?? null,
    error: window.__vibetableE2EBridgeCapture?.error ?? null,
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
      throw new Error("captured bridge response timed out", { cause: error });
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
