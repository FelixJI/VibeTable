/**
 * pasteFlow — B2 multi-row paste state machine.
 *
 * This module owns the renderer-side bookkeeping for the transparent preview +
 * atomic batch write flow. It is deliberately PURE w.r.t. the DOM: it takes
 * parsed clipboard cells, outbound bridge actions and inbound results, and
 * produces a state object + callbacks. main.ts binds those callbacks to the
 * real DOM + HostBridge.
 *
 * Flow (B2 brief):
 *   1. The user pastes (Ctrl+V) over a selection. The grid parses the clipboard
 *      and calls {@link requestPreview} with the parsed cells + the anchor.
 *   2. The host returns a {@link PastePlan}; the state machine surfaces the
 *      preview for confirmation. A plan with any error blocks submission;
 *      warnings require explicit acknowledgement.
 *   3. The user confirms (or cancels). {@link requestApply} posts the token +
 *      a fresh idempotency key. Duplicate submissions are disabled while a
 *      request is in flight; the user may cancel before the request leaves.
 *   4. The host returns an {@link ApplyPasteResult}. ``committed`` shows the
 *      server-confirmed counts; ``conflict`` shows the current values and asks
 *      for a re-preview; ``pending`` shows "result pending, checking…" so the
 *      UI never fabricates a success under network uncertainty.
 *
 * Overflow: when the parsed clipboard exceeds the 10k cell cap the state
 * machine enters the ``overflow`` state and surfaces the C1 file-import path
 * instead of submitting.
 */

import type {
  ApplyPasteResult,
  PasteCellDiagnostic,
  PastePlan,
  PasteSummary,
} from "./contracts";

/** Hard cap on parsed clipboard cells. Mirrors the backend constant. */
export const PASTE_CELL_LIMIT = 10_000;

/** The lifecycle phase of a paste operation. */
export type PastePhase =
  | "idle"
  | "previewing"
  | "preview"
  | "overflow"
  | "applying"
  | "committed"
  | "conflict"
  | "pending"
  | "error";

/** The accumulated state of one paste operation. */
export interface PasteFlowState {
  readonly phase: PastePhase;
  /** The collection the paste targets (null when idle). */
  readonly collection: string | null;
  /** The parsed clipboard cell count (for the overflow decision). */
  readonly cellCount: number;
  /** The preview plan shown for confirmation (null until preview arrives). */
  readonly plan: PastePlan | null;
  /** True when the user has acknowledged warnings and may submit. */
  readonly warningsAcknowledged: boolean;
  /** The last confirmed apply result (null until apply resolves). */
  readonly result: ApplyPasteResult | null;
  /** The last error message (cleared on success). */
  readonly error: string | null;
  /** A counter that increments whenever the state changes (for re-render). */
  readonly revision: number;
}

export const initialPasteFlowState: PasteFlowState = {
  phase: "idle",
  collection: null,
  cellCount: 0,
  plan: null,
  warningsAcknowledged: false,
  result: null,
  error: null,
  revision: 0,
};

/**
 * Callbacks the state machine emits to bind to the DOM + bridge. Each method
 * receives the updated state so the binder can refresh the UI.
 */
export interface PasteFlowCallbacks {
  onStateChange: (state: PasteFlowState) => void;
}

/** Bridge action the state machine asks the host to perform. */
export type PasteBridgeAction =
  | {
      readonly kind: "preview";
      readonly collection: string;
      readonly schemaRevision: string;
      readonly selection: unknown;
      readonly startCell: { rowKey: string | number | null; column: string };
      readonly cells: unknown;
    }
  | {
      readonly kind: "apply";
      readonly collection: string;
      readonly token: string;
      readonly idempotencyKey: string;
    };

/**
 * Create a paste-flow controller. The controller owns the mutable state and
 * exposes typed event handlers that main.ts subscribes to.
 */
export function createPasteFlow(callbacks: PasteFlowCallbacks) {
  let state: PasteFlowState = { ...initialPasteFlowState };

  function setState(next: Partial<PasteFlowState>): void {
    state = { ...state, ...next, revision: state.revision + 1 };
    callbacks.onStateChange(state);
  }

  function reset(collection: string | null = null): void {
    state = { ...initialPasteFlowState, collection, revision: state.revision + 1 };
    callbacks.onStateChange(state);
  }

  /** Whether the confirm button may submit the current plan. */
  function canSubmit(): boolean {
    return (
      state.phase === "preview" &&
      state.plan !== null &&
      state.plan.summary.errorCount === 0 &&
      (state.plan.summary.warningCount === 0 || state.warningsAcknowledged)
    );
  }

  /**
   * The user pasted a clipboard. Resolve the overflow first; if under the cap,
   * ask the host for a preview plan.
   */
  function requestPreview(
    action: (a: PasteBridgeAction) => void,
    params: {
      readonly collection: string;
      readonly schemaRevision: string;
      readonly selection: unknown;
      readonly startCell: { rowKey: string | number | null; column: string };
      readonly cells: unknown;
      readonly cellCount: number;
    },
  ): void {
    reset(params.collection);
    if (params.cellCount > PASTE_CELL_LIMIT) {
      setState({
        phase: "overflow",
        collection: params.collection,
        cellCount: params.cellCount,
      });
      return;
    }
    setState({
      phase: "previewing",
      collection: params.collection,
      cellCount: params.cellCount,
    });
    action({
      kind: "preview",
      collection: params.collection,
      schemaRevision: params.schemaRevision,
      selection: params.selection,
      startCell: params.startCell,
      cells: params.cells,
    });
  }

  /** Host replied with a preview plan. */
  function onPreviewReady(plan: PastePlan): void {
    setState({ phase: "preview", plan, warningsAcknowledged: false });
  }

  /** Host rejected the preview (schema changed, anchor invalid, etc.). */
  function onPreviewFailed(message: string): void {
    setState({ phase: "error", error: message });
  }

  /** The user toggled acknowledgement of the plan's warnings. */
  function acknowledgeWarnings(acknowledged: boolean): void {
    setState({ warningsAcknowledged: acknowledged });
  }

  /** The user confirmed the plan; submit the apply. */
  function requestApply(
    action: (a: PasteBridgeAction) => void,
    idempotencyKey: string,
  ): void {
    if (state.plan === null || !canSubmit()) {
      return;
    }
    setState({ phase: "applying" });
    action({
      kind: "apply",
      collection: state.plan.collection,
      token: state.plan.token.token,
      idempotencyKey,
    });
  }

  /** Host replied with the confirmed apply result. */
  function onApplyResult(result: ApplyPasteResult): void {
    setState({ phase: result.outcome, result, error: null });
  }

  /** Host rejected the apply (token expired/consumed, network error, etc.). */
  function onApplyFailed(message: string): void {
    setState({ phase: "error", error: message });
  }

  /** The user dismissed the paste panel; return to idle. */
  function cancel(): void {
    reset(null);
  }

  return {
    getState: () => state,
    canSubmit,
    requestPreview,
    onPreviewReady,
    onPreviewFailed,
    acknowledgeWarnings,
    requestApply,
    onApplyResult,
    onApplyFailed,
    cancel,
  };
}

export type PasteFlowController = ReturnType<typeof createPasteFlow>;

// ---------------------------------------------------------------------------
// Pure presentation helpers (used by the confirm panel; easy to unit test).
// ---------------------------------------------------------------------------

/** Summarize the plan's localized errors by row for the confirm panel. */
export function errorsByRow(
  plan: PastePlan,
): ReadonlyArray<{ readonly rowIndex: number; readonly diagnostics: readonly PasteCellDiagnostic[] }> {
  const grouped = new Map<number, PasteCellDiagnostic[]>();
  for (const row of plan.rows) {
    for (const diagnostic of row.diagnostics) {
      if (diagnostic.severity !== "error") {
        continue;
      }
      const list = grouped.get(diagnostic.rowIndex) ?? [];
      list.push(diagnostic);
      grouped.set(diagnostic.rowIndex, list);
    }
  }
  for (const diagnostic of plan.diagnostics) {
    if (diagnostic.severity !== "error") {
      continue;
    }
    const list = grouped.get(diagnostic.rowIndex) ?? [];
    list.push(diagnostic);
    grouped.set(diagnostic.rowIndex, list);
  }
  return [...grouped.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([rowIndex, diagnostics]) => ({ rowIndex, diagnostics }));
}

/** Human-readable summary line for the confirm panel header. */
export function summaryLine(summary: PasteSummary): string {
  const parts: string[] = [];
  parts.push(`${summary.updateRows} to update`);
  if (summary.insertRows > 0) {
    parts.push(`${summary.insertRows} to add`);
  }
  if (summary.skipRows > 0) {
    parts.push(`${summary.skipRows} skipped`);
  }
  if (summary.warningCount > 0) {
    parts.push(`${summary.warningCount} warning${summary.warningCount === 1 ? "" : "s"}`);
  }
  if (summary.errorCount > 0) {
    parts.push(`${summary.errorCount} error${summary.errorCount === 1 ? "" : "s"}`);
  }
  return parts.join(", ");
}

/** Human-readable outcome line for the result panel. */
export function outcomeLine(result: ApplyPasteResult): string {
  switch (result.outcome) {
    case "committed":
      return `Committed: ${result.createdRowKeys.length} added, ${result.updatedRowKeys.length} updated.`;
    case "conflict":
      return `Conflict: ${result.conflicts.length} row${result.conflicts.length === 1 ? "" : "s"} changed since preview.`;
    case "pending":
      return "Result pending — the request timed out. Checking by idempotency key…";
  }
}
