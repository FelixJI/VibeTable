export interface StructuredDialogFocusTarget {
  element: HTMLElement | null;
  rowKey: string | number;
  field: string;
  target?: StructuredDialogFocusDiagnosticTarget;
}

export type StructuredDialogFocusDiagnosticTarget = "attachment" | "json";
export type StructuredDialogFocusPendingReason = "grid" | "row" | "cell" | "focus-rejected";
export type StructuredDialogFocusCancellationReason =
  | "scope"
  | "window"
  | "external"
  | "disposed"
  | "stale";

export type StructuredDialogFocusOutcome = {
  readonly leaseId: number;
  readonly target?: StructuredDialogFocusDiagnosticTarget;
} & (
  | { readonly state: "claimed" | "released" }
  | { readonly state: "restored"; readonly via: "captured" | "reprojected" }
  | { readonly state: "pending"; readonly reason: StructuredDialogFocusPendingReason }
  | { readonly state: "cancelled"; readonly reason: StructuredDialogFocusCancellationReason }
);

interface StructuredCellLike {
  getElement?: () => HTMLElement;
}

interface StructuredRowLike {
  getCell?: (field: string) => StructuredCellLike | null | undefined;
  getIndex?: () => string | number;
}

export interface StructuredGridLike {
  getRows?: () => readonly StructuredRowLike[] | null | undefined;
  on?: (event: string, handler: () => void) => void;
  off?: (event: string, handler: () => void) => void;
}

export interface StructuredGridFocusScope {
  readonly workspaceId: string | null;
  readonly sessionEpoch: number;
  readonly tableId: string | null;
}

export interface StructuredDialogFocusLease {
  restore(): void;
  cancel(): void;
}

export interface StructuredDialogFocus {
  capture(target: StructuredDialogFocusTarget): StructuredDialogFocusLease;
  dispose(): void;
}

export interface StructuredDialogFocusDependencies {
  readonly getGrid: () => StructuredGridLike | null;
  readonly getScope: () => StructuredGridFocusScope;
  readonly subscribeScope: (listener: () => void) => () => void;
  readonly reportOutcome?: (outcome: StructuredDialogFocusOutcome) => void;
}

type StructuredDialogFocusOutcomeBody =
  | { readonly state: "claimed" | "released" }
  | { readonly state: "restored"; readonly via: "captured" | "reprojected" }
  | { readonly state: "pending"; readonly reason: StructuredDialogFocusPendingReason }
  | { readonly state: "cancelled"; readonly reason: StructuredDialogFocusCancellationReason };

type StructuredDialogFocusAttempt = Extract<
  StructuredDialogFocusOutcomeBody,
  { state: "restored" | "pending" }
>;

type StructuredDialogFocusResolution =
  | { readonly status: "grid" | "row" }
  | { readonly status: "resolved"; readonly element: HTMLElement | null };

interface InternalStructuredDialogFocusLease extends StructuredDialogFocusLease {
  cancelFor(reason: StructuredDialogFocusCancellationReason): void;
}

// The E2E ledger survives WorkspaceView/service recreation within one page.
// Keep lease identities monotonic for the same lifetime so a bounded visible
// window never aliases a new lease to an older producer instance.
let nextStructuredDialogFocusLeaseId = 0;

const KEYBOARD_FOCUS_NAVIGATION_KEYS = new Set([
  "Tab",
  "ArrowUp",
  "ArrowDown",
  "ArrowLeft",
  "ArrowRight",
  "Home",
  "End",
  "PageUp",
  "PageDown",
  "F6",
]);

function sameScope(left: StructuredGridFocusScope, right: StructuredGridFocusScope): boolean {
  return left.workspaceId === right.workspaceId
    && left.sessionEpoch === right.sessionEpoch
    && left.tableId === right.tableId;
}

export function createStructuredDialogFocus(
  dependencies: StructuredDialogFocusDependencies,
): StructuredDialogFocus {
  let current: InternalStructuredDialogFocusLease | null = null;
  let disposed = false;
  const stopScope = dependencies.subscribeScope(() => current?.cancelFor("scope"));

  function capture(target: StructuredDialogFocusTarget): StructuredDialogFocusLease {
    current?.cancelFor("stale");
    const scope = dependencies.getScope();
    const leaseId = ++nextStructuredDialogFocusLeaseId;
    let cancelled = false;
    let restoreRequested = false;
    let boundGrid: StructuredGridLike | null = null;
    let observingDocumentFocus = false;
    let observingDocumentIntent = false;
    let observingWindowFocus = false;
    let focusTargetObserver: MutationObserver | null = null;
    let restoringFocus = false;
    let terminalReported = false;
    const captured = resolveStructuredDialogFocusTarget(dependencies.getGrid(), target);
    // Only an exact, connected current cell can authorize later row/grid gaps.
    // A stale trigger or initially absent identity remains fail-closed.
    const captureVerified = target.element?.isConnected === true
      && captured.status === "resolved"
      && captured.element === target.element;
    const capturedGridRoot = captureVerified
      ? target.element?.closest<HTMLElement>(".tabulator") ?? null
      : null;

    const report = (outcome: StructuredDialogFocusOutcomeBody): void => {
      if (terminalReported) return;
      if (outcome.state === "restored" || outcome.state === "cancelled") {
        terminalReported = true;
      }
      dependencies.reportOutcome?.({
        leaseId,
        ...(target.target ? { target: target.target } : {}),
        ...outcome,
      } as StructuredDialogFocusOutcome);
    };

    const attemptRestore = (grid: StructuredGridLike | null): void => {
      restoringFocus = true;
      try {
        const attempt = attemptStructuredDialogFocus(grid, target);
        report(attempt);
      } finally {
        restoringFocus = false;
      }
    };

    const reattemptRestore = () => {
      if (cancelled || disposed || current !== lease) return;
      if (!sameScope(scope, dependencies.getScope())) {
        lease.cancelFor("scope");
        return;
      }
      attemptRestore(dependencies.getGrid());
    };

    const isCapturedGridInfrastructureFocus = (candidate: unknown): boolean =>
      capturedGridRoot !== null
      && candidate instanceof HTMLElement
      && (
        candidate.classList.contains("tabulator-tableholder")
        || candidate.classList.contains("tabulator-cell")
      )
      && candidate.closest(".tabulator") === capturedGridRoot;

    const onDocumentFocusIn = (event: FocusEvent) => {
      if (restoringFocus) return;
      if (event.target === document.body || event.target === document.documentElement) {
        reattemptRestore();
        return;
      }
      // Tabulator's range module can focus its tableholder or another cell as an
      // infrastructure sink when editing settles. Other controls inside the grid
      // retain external ownership; pointer/keyboard intent cancels before focusin.
      if (isCapturedGridInfrastructureFocus(event.target)) {
        reattemptRestore();
        return;
      }
      lease.cancelFor("external");
    };

    const onDocumentPointerFocusIntent = () => lease.cancelFor("external");

    const onDocumentKeyboardFocusIntent = (event: KeyboardEvent) => {
      if (KEYBOARD_FOCUS_NAVIGATION_KEYS.has(event.key)) {
        lease.cancelFor("external");
      }
    };

    const onRenderComplete = () => reattemptRestore();

    const onFocusTargetMutation = () => {
      if (target.element?.isConnected === true) return;
      if (
        document.activeElement !== document.body
        && document.activeElement !== document.documentElement
        && !isCapturedGridInfrastructureFocus(document.activeElement)
      ) return;
      reattemptRestore();
    };

    const onWindowBlur = () => lease.cancelFor("window");

    const lease: InternalStructuredDialogFocusLease = {
      restore(): void {
        if (cancelled || disposed || restoreRequested || current !== lease) return;
        restoreRequested = true;
        report({ state: "released" });
        if (!sameScope(scope, dependencies.getScope())) {
          lease.cancelFor("scope");
          return;
        }
        if (!captureVerified) {
          lease.cancelFor("stale");
          return;
        }
        boundGrid = dependencies.getGrid();
        boundGrid?.on?.("renderComplete", onRenderComplete);
        if (capturedGridRoot) {
          focusTargetObserver = new MutationObserver(onFocusTargetMutation);
          focusTargetObserver.observe(capturedGridRoot, { childList: true, subtree: true });
        }
        attemptRestore(boundGrid);
        if (cancelled) return;
        document.addEventListener("focusin", onDocumentFocusIn);
        observingDocumentFocus = true;
        document.addEventListener("pointerdown", onDocumentPointerFocusIntent, true);
        document.addEventListener("keydown", onDocumentKeyboardFocusIntent, true);
        observingDocumentIntent = true;
      },
      cancel(): void {
        lease.cancelFor("stale");
      },
      cancelFor(reason): void {
        if (cancelled) return;
        cancelled = true;
        report({ state: "cancelled", reason });
        boundGrid?.off?.("renderComplete", onRenderComplete);
        if (observingDocumentFocus) {
          document.removeEventListener("focusin", onDocumentFocusIn);
        }
        if (observingDocumentIntent) {
          document.removeEventListener("pointerdown", onDocumentPointerFocusIntent, true);
          document.removeEventListener("keydown", onDocumentKeyboardFocusIntent, true);
        }
        if (observingWindowFocus) {
          window.removeEventListener("blur", onWindowBlur);
        }
        focusTargetObserver?.disconnect();
        focusTargetObserver = null;
        boundGrid = null;
        if (current === lease) current = null;
      },
    };
    current = lease;
    window.addEventListener("blur", onWindowBlur);
    observingWindowFocus = true;
    report({ state: "claimed" });
    return lease;
  }

  function dispose(): void {
    if (disposed) return;
    disposed = true;
    current?.cancelFor("disposed");
    stopScope();
  }

  return { capture, dispose };
}

/**
 * Restore focus to the structured cell that opened a dialog.
 *
 * Tabulator can replace a cell DOM node while its range module settles. The
 * row/field identity is authoritative on every attempt, while the original
 * trigger only identifies whether the current node was captured or reprojected.
 * The row is resolved from one enumerated
 * `getRows` snapshot matched by `getIndex` instead of Tabulator's `getRow`,
 * whose miss path emits the "Find Error - No matching row found" console
 * warning that product E2E treats as a renderer contract violation.
 */
export function restoreStructuredDialogFocus(
  grid: StructuredGridLike | null,
  target: StructuredDialogFocusTarget | null,
): boolean {
  return attemptStructuredDialogFocus(grid, target).state === "restored";
}

function attemptStructuredDialogFocus(
  grid: StructuredGridLike | null,
  target: StructuredDialogFocusTarget | null,
): StructuredDialogFocusAttempt {
  if (!target) return { state: "pending", reason: "row" };
  const resolved = resolveStructuredDialogFocusTarget(grid, target);
  if (resolved.status !== "resolved") {
    return { state: "pending", reason: resolved.status };
  }
  const element = resolved.element;
  if (!element?.isConnected) return { state: "pending", reason: "cell" };
  element.focus({ preventScroll: true });
  if (document.activeElement !== element) {
    return { state: "pending", reason: "focus-rejected" };
  }
  const current = resolveStructuredDialogFocusTarget(grid, target);
  if (current.status !== "resolved") {
    return { state: "pending", reason: current.status };
  }
  if (current.element !== element) {
    return { state: "pending", reason: "cell" };
  }
  return {
    state: "restored",
    via: element === target.element ? "captured" : "reprojected",
  };
}

function resolveStructuredDialogFocusTarget(
  grid: StructuredGridLike | null,
  target: StructuredDialogFocusTarget,
): StructuredDialogFocusResolution {
  if (!grid?.getRows) return { status: "grid" };
  const rows = grid.getRows();
  if (!rows) return { status: "grid" };
  const row = rows.find((candidate) => String(candidate.getIndex?.()) === String(target.rowKey));
  if (!row) return { status: "row" };
  return {
    status: "resolved",
    element: row.getCell?.(target.field)?.getElement?.() ?? null,
  };
}
