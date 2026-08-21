import type { StructuredDialogFocusOutcome } from "./dialogFocus";

const DIALOG_FOCUS_OUTCOME_EVENT = "vibetable:e2e-dialog-focus-outcome";

function installedE2EDiagnostics(): boolean {
  const diagnostics = Reflect.get(window, "__vibetableE2EBridgeDiagnostics");
  return typeof diagnostics === "object"
    && diagnostics !== null
    && Reflect.get(diagnostics, "installed") === true;
}

/**
 * Exposes a closed, identity-free focus outcome only while the E2E diagnostics
 * observer is installed in the product page.
 */
export function reportStructuredDialogFocusE2EOutcome(
  outcome: StructuredDialogFocusOutcome,
): void {
  if (!installedE2EDiagnostics()) return;
  if (outcome.target !== "attachment" && outcome.target !== "json") return;
  const identity = { leaseId: outcome.leaseId, target: outcome.target };
  let detail: StructuredDialogFocusOutcome;
  if (outcome.state === "claimed" || outcome.state === "released") {
    detail = { ...identity, state: outcome.state };
  } else if (outcome.state === "restored") {
    detail = { ...identity, state: outcome.state, via: outcome.via };
  } else if (outcome.state === "pending") {
    detail = { ...identity, state: outcome.state, reason: outcome.reason };
  } else if (outcome.state === "cancelled") {
    detail = { ...identity, state: outcome.state, reason: outcome.reason };
  } else {
    return;
  }
  window.dispatchEvent(new CustomEvent(DIALOG_FOCUS_OUTCOME_EVENT, { detail }));
}
