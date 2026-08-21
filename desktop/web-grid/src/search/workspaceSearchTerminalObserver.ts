import type { SearchStatus } from "@/contracts/generated/workbench";
import { classifyWorkspaceSearchObservation } from "./workspaceSearchLifecycle.mjs";

// The release qualification contract permits a full-corpus rebuild to take four minutes.
// Match the existing long-running task reconciliation window so bridge reads can still
// publish the authoritative terminal state without allowing an unbounded UI lifecycle.
export const WORKSPACE_SEARCH_REBUILD_TERMINAL_BUDGET_MS = 310_000;
// Cancellation is a bridge lifecycle, so it must settle within the bridge's request budget.
export const WORKSPACE_SEARCH_CANCEL_TERMINAL_BUDGET_MS = 30_000;
const WORKSPACE_SEARCH_STATUS_INTERVAL_MS = 250;

interface WorkspaceSearchTerminalObserverOptions {
  acceptedGeneration: number;
  initial: SearchStatus;
  budgetMs: number;
  timeoutCode: string;
  ownsLifecycle: () => boolean;
  readStatus: () => Promise<SearchStatus>;
  publishObservation: (status: SearchStatus) => void;
}

function lifecycleError(code: string): Error {
  return new Error(code);
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, milliseconds));
}

function settleBeforeDeadline<T>(
  pending: Promise<T>,
  deadline: number,
  timeoutCode: string,
): Promise<T> {
  const remaining = deadline - Date.now();
  if (remaining <= 0) return Promise.reject(lifecycleError(timeoutCode));
  return new Promise<T>((resolve, reject) => {
    let settled = false;
    const timeout = window.setTimeout(() => {
      if (settled) return;
      settled = true;
      reject(lifecycleError(timeoutCode));
    }, remaining);
    pending.then(
      (value) => {
        if (settled) return;
        settled = true;
        window.clearTimeout(timeout);
        resolve(value);
      },
      (error: unknown) => {
        if (settled) return;
        settled = true;
        window.clearTimeout(timeout);
        reject(error);
      },
    );
  });
}

export async function observeWorkspaceSearchTerminal({
  acceptedGeneration,
  initial,
  budgetMs,
  timeoutCode,
  ownsLifecycle,
  readStatus,
  publishObservation,
}: WorkspaceSearchTerminalObserverOptions): Promise<SearchStatus | null> {
  if (!Number.isInteger(acceptedGeneration)) {
    throw lifecycleError("workspace_search.generation_mismatch");
  }
  const deadline = Date.now() + budgetMs;
  let observed = initial;

  while (ownsLifecycle()) {
    const relation = classifyWorkspaceSearchObservation({
      acceptedGeneration,
      state: observed.state,
      generation: observed.generation,
    });
    if (relation === "invalid") throw lifecycleError("workspace_search.generation_mismatch");
    publishObservation(observed);
    if (relation === "terminal") return observed;

    const remaining = deadline - Date.now();
    if (remaining <= 0) throw lifecycleError(timeoutCode);
    await delay(Math.min(WORKSPACE_SEARCH_STATUS_INTERVAL_MS, remaining));
    if (!ownsLifecycle()) return null;
    observed = await settleBeforeDeadline(readStatus(), deadline, timeoutCode);
  }
  return null;
}
