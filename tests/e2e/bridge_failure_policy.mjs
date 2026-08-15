export function isExpectedSidecarRecoveryFailure(response) {
  return response?.type === "operation.failed"
    && response.payload?.code === "BACKEND_UNAVAILABLE"
    && typeof response.requestId === "string"
    && response.requestId.length > 0;
}

export async function acknowledgeExpectedSidecarRecoveryFailure(
  response,
  acknowledge,
) {
  if (!isExpectedSidecarRecoveryFailure(response)) return false;
  await acknowledge(response);
  return true;
}
