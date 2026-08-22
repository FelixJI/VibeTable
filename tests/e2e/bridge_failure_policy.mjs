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

export function beginSidecarRecoveryNotificationFailureWindowInPage({ ownerToken, tableId }) {
  const pageGlobal = globalThis.window ?? globalThis;
  const diagnostics = pageGlobal.__vibetableE2EBridgeDiagnostics;
  if (
    typeof ownerToken !== "string"
    || ownerToken.length === 0
    || typeof tableId !== "string"
    || tableId.length === 0
    || !Number.isSafeInteger(diagnostics?.diagnosticCursor)
  ) {
    throw new Error("sidecar recovery notification failure window is unavailable");
  }
  pageGlobal.__vibetableE2ESidecarRecoveryFailureWindow = {
    ownerToken,
    tableId,
    startCursor: diagnostics.diagnosticCursor,
  };
  return { state: "owned", startCursor: diagnostics.diagnosticCursor };
}

export function settleSidecarRecoveryNotificationFailureWindowInPage({
  ownerToken,
  deadlineAt,
}) {
  const pageGlobal = globalThis.window ?? globalThis;
  const lease = pageGlobal.__vibetableE2ESidecarRecoveryFailureWindow;
  if (lease?.ownerToken !== ownerToken) return { state: "stale" };
  if (!Number.isFinite(deadlineAt) || Date.now() >= deadlineAt) {
    return { state: "expired" };
  }
  const diagnostics = pageGlobal.__vibetableE2EBridgeDiagnostics;
  if (!Number.isSafeInteger(diagnostics?.diagnosticCursor)) {
    throw new Error("sidecar recovery notification diagnostics are unavailable");
  }

  const endCursor = diagnostics.diagnosticCursor;
  const notificationEvents = diagnostics.notifications
    .filter(notification => (
      notification.requestType === "table.selected"
      && Number.isSafeInteger(notification.cursor)
      && notification.cursor > lease.startCursor
      && notification.cursor <= endCursor
    ))
    .map(notification => ({
      kind: "notification",
      cursor: notification.cursor,
      ownerToken: notification.recoveryOwnerToken,
    }));
  const failureEvents = diagnostics.failures
    .filter(failure => (
      failure.requestId === null
      && failure.operation === "table.selected"
      && Number.isSafeInteger(failure.cursor)
      && failure.cursor > lease.startCursor
      && failure.cursor <= endCursor
    ))
    .map(failure => ({ kind: "failure", cursor: failure.cursor, failure }));
  let currentNotification = null;
  const acknowledged = new Set();
  for (const event of [...notificationEvents, ...failureEvents]
    .sort((left, right) => left.cursor - right.cursor)) {
    if (event.kind === "notification") {
      currentNotification = event;
      continue;
    }
    const notification = currentNotification;
    currentNotification = null;
    if (
      notification?.ownerToken === ownerToken
      && event.failure.responseType === "operation.failed"
      && event.failure.code === "BACKEND_UNAVAILABLE"
    ) {
      acknowledged.add(event.failure);
    }
  }
  const remaining = diagnostics.failures.filter(failure => !acknowledged.has(failure));
  diagnostics.failures = remaining;
  diagnostics.acknowledgedFailures ??= [];
  diagnostics.acknowledgedFailures.push(...acknowledged);
  delete pageGlobal.__vibetableE2ESidecarRecoveryFailureWindow;
  return {
    state: "settled",
    startCursor: lease.startCursor,
    endCursor,
    acknowledgedCount: acknowledged.size,
  };
}

export function releaseSidecarRecoveryNotificationFailureWindowInPage({ ownerToken }) {
  const pageGlobal = globalThis.window ?? globalThis;
  const lease = pageGlobal.__vibetableE2ESidecarRecoveryFailureWindow;
  if (lease?.ownerToken !== ownerToken) return { state: "stale" };
  delete pageGlobal.__vibetableE2ESidecarRecoveryFailureWindow;
  return { state: "released" };
}

const recoveryObservationMs = 5_000;
const recoveryRequestType = "query.page";

export class SidecarRecoveryContractError extends Error {
  constructor(message, options) {
    super(message, options);
    this.name = "SidecarRecoveryContractError";
  }
}

/**
 * Owns every correlated query.page probe issued during one deliberate sidecar
 * recovery window. The five-second observation is not a terminal timeout:
 * requests remain owned until settle() observes their real terminal within the
 * caller's absolute recovery deadline.
 */
export class SidecarRecoveryReadWindow {
  #acknowledge;
  #activeObservations = new Set();
  #closePromise = null;
  #closedError = new SidecarRecoveryContractError("sidecar recovery window is closed");
  #closedPromise = null;
  #closeSignal;
  #closed = false;
  #deadlineAt;
  #now;
  #observeTerminal;
  #owned = new Map();
  #releaseRequest;
  #settlePromise = null;
  #signalClose;

  constructor({
    deadlineAt,
    now = Date.now,
    observeTerminal,
    releaseRequest,
    acknowledge,
  }) {
    this.#deadlineAt = deadlineAt;
    this.#now = now;
    this.#observeTerminal = observeTerminal;
    this.#releaseRequest = releaseRequest;
    this.#acknowledge = acknowledge;
    this.#closeSignal = new Promise(resolve => {
      this.#signalClose = resolve;
    });
  }

  own(requestId) {
    if (this.#closed) throw this.#closedError;
    if (this.#settlePromise !== null) {
      throw new SidecarRecoveryContractError(
        "sidecar recovery window no longer accepts requests",
      );
    }
    if (typeof requestId !== "string" || requestId.length === 0) {
      throw new SidecarRecoveryContractError(
        "sidecar recovery requests require a correlated requestId",
      );
    }
    if (this.#owned.has(requestId)) {
      throw new SidecarRecoveryContractError(
        `sidecar recovery request is already owned: ${requestId}`,
      );
    }
    this.#owned.set(requestId, {
      requestId,
      requestType: recoveryRequestType,
      terminal: null,
      released: false,
      releasePromise: null,
    });
  }

  observe(requestId) {
    if (this.#closed) return this.#closedResult();
    const observation = this.#observeOnce(requestId);
    this.#activeObservations.add(observation);
    void observation.then(
      () => this.#activeObservations.delete(observation),
      () => this.#activeObservations.delete(observation),
    );
    return observation;
  }

  async #observeOnce(requestId) {
    this.#assertOpen();
    this.#remainingMs();
    const owned = this.#owned.get(requestId);
    if (!owned) {
      throw new SidecarRecoveryContractError(
        `recovery request is not owned: ${requestId}`,
      );
    }
    if (owned.terminal !== null) {
      await this.#releaseOwned(owned);
      this.#assertOpen();
      this.#remainingMs();
      return owned.terminal;
    }
    return this.#observeOwned(
      owned,
      Math.min(recoveryObservationMs, this.#remainingMs()),
    );
  }

  settle() {
    if (this.#closed) return this.#closedResult();
    if (this.#settlePromise === null) {
      this.#settlePromise = this.#settleOnce();
    }
    return this.#settlePromise;
  }

  close() {
    if (this.#closePromise === null) {
      this.#closed = true;
      this.#signalClose();
      this.#closePromise = this.#closeOnce();
    }
    return this.#closePromise;
  }

  async #settleOnce() {
    this.#assertOpen();
    this.#remainingMs();
    for (const owned of this.#owned.values()) {
      this.#assertOpen();
      this.#remainingMs();
      let terminal = owned.terminal;
      if (terminal === null) {
        terminal = await this.#observeOwned(owned, this.#remainingMs());
        if (terminal === null) {
          throw new SidecarRecoveryContractError(
            `recovery request did not settle: ${owned.requestId}`,
          );
        }
      } else {
        await this.#releaseOwned(owned);
        this.#assertOpen();
        this.#remainingMs();
      }
      this.#assertOpen();
      this.#remainingMs();
      if (terminal.type === owned.requestType) continue;
      try {
        this.#assertOpen();
        this.#remainingMs();
        await this.#acknowledge(terminal);
        this.#assertOpen();
        this.#remainingMs();
      } catch (error) {
        if (error instanceof SidecarRecoveryContractError) throw error;
        throw new SidecarRecoveryContractError(
          `recovery failure was not acknowledged: ${owned.requestId}`,
          { cause: error },
        );
      }
    }
  }

  async #observeOwned(owned, timeoutMs) {
    let terminal;
    try {
      this.#assertOpen();
      const observation = this.#observeTerminal(owned.requestId, timeoutMs);
      terminal = await Promise.race([
        observation,
        this.#closeSignal.then(() => {
          throw this.#closedError;
        }),
      ]);
      this.#assertOpen();
    } catch (error) {
      if (error instanceof SidecarRecoveryContractError) throw error;
      throw new SidecarRecoveryContractError(
        `recovery request observation failed: ${owned.requestId}`,
        { cause: error },
      );
    }
    let deadlineError = null;
    try {
      this.#remainingMs();
    } catch (error) {
      deadlineError = error;
    }
    if (terminal === null) {
      if (deadlineError !== null) throw deadlineError;
      return null;
    }
    if (deadlineError !== null) {
      try {
        await this.#releaseOwned(owned);
      } catch (releaseError) {
        deadlineError.cause = releaseError;
      }
      throw deadlineError;
    }

    let contractError = null;
    try {
      this.#accept(owned, terminal);
    } catch (error) {
      contractError = error;
    }
    try {
      await this.#releaseOwned(owned);
      this.#assertOpen();
      this.#remainingMs();
    } catch (error) {
      const releaseError = error instanceof SidecarRecoveryContractError
        ? error
        : new SidecarRecoveryContractError(
          `recovery request release failed: ${owned.requestId}`,
          { cause: error },
        );
      if (contractError !== null) {
        contractError.cause = releaseError;
      } else {
        contractError = releaseError;
      }
    }
    if (contractError !== null) throw contractError;
    return terminal;
  }

  #accept(owned, terminal) {
    if (terminal?.requestId !== owned.requestId) {
      throw new SidecarRecoveryContractError(
        `recovery request identity mismatch: ${owned.requestId}`,
      );
    }
    const succeeded = terminal.type === owned.requestType;
    const expectedFailure = owned.requestType === recoveryRequestType
      && isExpectedSidecarRecoveryFailure(terminal);
    if (!succeeded && !expectedFailure) {
      throw new SidecarRecoveryContractError(
        `unexpected recovery terminal: ${owned.requestId}`,
      );
    }
    owned.terminal = terminal;
  }

  async #releaseOwned(owned) {
    if (owned.released) return;
    if (owned.releasePromise === null) {
      owned.releasePromise = (async () => {
        await this.#releaseRequest(owned.requestId);
        owned.released = true;
      })();
    }
    await owned.releasePromise;
  }

  async #closeOnce() {
    const inFlight = new Set(this.#activeObservations);
    if (this.#settlePromise !== null) inFlight.add(this.#settlePromise);
    await Promise.allSettled(inFlight);

    const errors = [];
    for (const owned of this.#owned.values()) {
      try {
        await this.#releaseOwned(owned);
      } catch (error) {
        errors.push(error);
      }
    }
    if (errors.length > 0) {
      throw new SidecarRecoveryContractError(
        "sidecar recovery window could not release every request",
        { cause: new AggregateError(errors) },
      );
    }
  }

  #remainingMs() {
    const remaining = this.#deadlineAt - this.#now();
    if (remaining <= 0) {
      throw new SidecarRecoveryContractError("sidecar recovery deadline expired");
    }
    return remaining;
  }

  #assertOpen() {
    if (this.#closed) throw this.#closedError;
  }

  #closedResult() {
    this.#closedPromise ??= Promise.reject(this.#closedError);
    return this.#closedPromise;
  }
}
