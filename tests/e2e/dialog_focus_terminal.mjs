/**
 * One self-contained evaluator is deliberately exported under every
 * operation name. Playwright serializes the function body into the product
 * page, so this keeps capture, wait, and evidence reads on one validator.
 */
export function captureDialogFocusLeaseInPage(request) {
  const fail = (code, detail = {}) => {
    throw new Error(JSON.stringify({ code, ...detail }));
  };
  const validTarget = (target) => target === "json" || target === "attachment";
  const pendingReasons = new Set(["grid", "row", "cell", "focus-rejected"]);
  const cancellationReasons = new Set(["scope", "window", "external", "disposed", "stale"]);
  const restoreTargets = new Set(["captured", "reprojected"]);
  const dialogFocus = window.__vibetableE2EBridgeDiagnostics?.dialogFocus;
  const events = Array.isArray(dialogFocus?.events) ? dialogFocus.events : [];

  let previousCursor = 0;
  for (const event of events) {
    if (!Number.isSafeInteger(event?.cursor)
      || event.cursor <= previousCursor
      || !Number.isSafeInteger(event?.leaseId)
      || event.leaseId <= 0) {
      fail("DIALOG_FOCUS_LEASE_WINDOW_INVALID");
    }
    previousCursor = event.cursor;
  }

  const canonicalCapture = (candidate) => {
    if (!Number.isSafeInteger(candidate?.cursor)
      || candidate.cursor <= 0
      || !Number.isSafeInteger(candidate?.leaseId)
      || candidate.leaseId <= 0
      || !validTarget(candidate.target)) {
      fail("DIALOG_FOCUS_CAPTURE_INVALID");
    }
    return {
      cursor: candidate.cursor,
      leaseId: candidate.leaseId,
      target: candidate.target,
    };
  };

  const validateLease = (candidate) => {
    const capture = canonicalCapture(candidate);
    const captureIndex = events.findIndex((event) => event.cursor === capture.cursor);
    const claim = events[captureIndex];
    if (captureIndex < 0
      || claim.leaseId !== capture.leaseId
      || claim.target !== capture.target
      || claim.state !== "claimed") {
      fail("DIALOG_FOCUS_LEASE_WINDOW_INCOMPLETE", {
        leaseId: capture.leaseId,
        target: capture.target,
        cursor: capture.cursor,
      });
    }
    if (events.slice(0, captureIndex).some((event) => event.leaseId === capture.leaseId)) {
      fail("DIALOG_FOCUS_LEASE_ID_REUSED", {
        leaseId: capture.leaseId,
        target: capture.target,
        cursor: capture.cursor,
      });
    }

    let released = false;
    let terminal = null;
    const owned = [{
      cursor: claim.cursor,
      leaseId: claim.leaseId,
      state: "claimed",
      target: claim.target,
    }];
    const invalidSequence = (reason, event) => fail("DIALOG_FOCUS_LEASE_SEQUENCE_INVALID", {
      leaseId: capture.leaseId,
      target: capture.target,
      cursor: event.cursor,
      reason,
    });

    for (let index = captureIndex + 1; index < events.length; index += 1) {
      const event = events[index];
      if (event.leaseId !== capture.leaseId) continue;
      if (event.target !== capture.target) invalidSequence("TARGET_CHANGED", event);
      if (event.state === "claimed") invalidSequence("CLAIM_DUPLICATE", event);
      if (event.state === "released") {
        if (terminal !== null) invalidSequence("EVENT_AFTER_TERMINAL", event);
        if (released) invalidSequence("RELEASE_DUPLICATE", event);
        released = true;
        owned.push({
          cursor: event.cursor,
          leaseId: event.leaseId,
          state: "released",
          target: event.target,
        });
        continue;
      }
      if (event.state === "pending") {
        if (!released) invalidSequence("PENDING_BEFORE_RELEASE", event);
        if (terminal !== null) invalidSequence("EVENT_AFTER_TERMINAL", event);
        if (!pendingReasons.has(event.reason)) invalidSequence("PENDING_INVALID", event);
        owned.push({
          cursor: event.cursor,
          leaseId: event.leaseId,
          state: "pending",
          target: event.target,
          reason: event.reason,
        });
        continue;
      }
      if (event.state === "restored" || event.state === "cancelled") {
        if (terminal !== null) invalidSequence("TERMINAL_DUPLICATE", event);
        if (event.state === "restored" && !released) {
          invalidSequence("TERMINAL_BEFORE_RELEASE", event);
        }
        if (event.state === "restored" && !restoreTargets.has(event.via)) {
          invalidSequence("RESTORE_INVALID", event);
        }
        if (event.state === "cancelled" && !cancellationReasons.has(event.reason)) {
          invalidSequence("CANCELLATION_INVALID", event);
        }
        terminal = event.state === "restored"
          ? {
              cursor: event.cursor,
              leaseId: event.leaseId,
              state: "restored",
              target: event.target,
              via: event.via,
            }
          : {
              cursor: event.cursor,
              leaseId: event.leaseId,
              state: "cancelled",
              target: event.target,
              reason: event.reason,
            };
        owned.push(terminal);
        continue;
      }
      invalidSequence("STATE_INVALID", event);
    }
    return { capture, events: owned, released, terminal };
  };

  if (request?.operation === "capture") {
    if (!validTarget(request.target)) {
      fail("DIALOG_FOCUS_CAPTURE_INVALID_TARGET", { target: null });
    }
    let latest = null;
    for (const event of events) {
      if (event.state === "claimed" && event.target === request.target) latest = event;
    }
    if (latest === null) {
      fail("DIALOG_FOCUS_LEASE_NOT_CAPTURED", { target: request.target });
    }
    const capture = { cursor: latest.cursor, leaseId: latest.leaseId, target: latest.target };
    const validated = validateLease(capture);
    if (validated.events.length !== 1 || validated.released || validated.terminal !== null) {
      fail("DIALOG_FOCUS_LEASE_NOT_OPEN", {
        leaseId: capture.leaseId,
        target: capture.target,
        cursor: capture.cursor,
      });
    }
    return capture;
  }

  if (
    request?.operation === "has-terminal"
    || request?.operation === "has-restored-focus"
    || request?.operation === "read-evidence"
  ) {
    const validated = validateLease(request.capture);
    const capture = validated.capture;
    if (validated.terminal?.state === "cancelled") {
      fail("DIALOG_FOCUS_LEASE_CANCELLED", {
        leaseId: capture.leaseId,
        target: capture.target,
        reason: validated.terminal.reason,
        cursor: validated.terminal.cursor,
      });
    }
    if (request.operation === "has-terminal") return validated.terminal !== null;
    if (request.operation === "has-restored-focus") {
      if (
        typeof request.field !== "string"
        || request.field.length === 0
        || request.field.length > 200
        || !Number.isSafeInteger(request.occurrence)
        || request.occurrence < 0
        || request.occurrence > 10_000
      ) {
        fail("DIALOG_FOCUS_TARGET_INVALID");
      }
      if (validated.terminal?.state !== "restored") return false;
      const gridRoots = Array.from(
        document.querySelectorAll(".grid-host > .tabulator-mount.tabulator"),
      ).filter((candidate) => candidate.isConnected === true);
      if (gridRoots.length !== 1) return false;
      const matchingCells = Array.from(
        gridRoots[0].querySelectorAll(".tabulator-cell[tabulator-field]"),
      ).filter((candidate) => candidate.getAttribute("tabulator-field") === request.field);
      const element = matchingCells[request.occurrence] ?? null;
      if (!document.hasFocus()
        || element?.isConnected !== true
        || document.activeElement !== element) {
        return false;
      }
      const activeElement = document.activeElement;
      return {
        restored: true,
        documentHasFocus: true,
        activeTag: typeof activeElement?.tagName === "string" ? activeElement.tagName : null,
        activeClass: typeof activeElement?.className === "string" ? activeElement.className : null,
        activeField: typeof activeElement?.getAttribute === "function"
          ? activeElement.getAttribute("tabulator-field")
          : null,
        activeRole: typeof activeElement?.getAttribute === "function"
          ? activeElement.getAttribute("role")
          : null,
        activeTestId: typeof activeElement?.getAttribute === "function"
          ? activeElement.getAttribute("data-testid")
          : null,
      };
    }
    return { capture, events: validated.events, terminal: validated.terminal };
  }

  fail("DIALOG_FOCUS_OPERATION_INVALID");
}

export const hasDialogFocusLeaseTerminalInPage = captureDialogFocusLeaseInPage;
export const hasDialogFocusLeaseRestoredFocusInPage = captureDialogFocusLeaseInPage;
export const readDialogFocusLeaseEvidenceInPage = captureDialogFocusLeaseInPage;
