import { afterEach, describe, expect, it, vi } from "vitest";
import type { SearchStatus } from "@/contracts/generated/workbench";
import { observeWorkspaceSearchTerminal } from "./workspaceSearchTerminalObserver";

const searchStatus = (state: SearchStatus["state"], generation: number): SearchStatus => ({
  state,
  generation,
  checkpoint: null,
  processed: 0,
  total: state === "building" ? 2 : 0,
  errorCode: null,
});

function deferred<T>(): {
  promise: Promise<T>;
  resolve: (value: T) => void;
} {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

describe("observeWorkspaceSearchTerminal", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("fails at the lifecycle deadline when a status request never settles", async () => {
    vi.useFakeTimers({ now: 0 });
    const pendingStatus = deferred<SearchStatus>();
    const publishObservation = vi.fn();
    const observation = observeWorkspaceSearchTerminal({
      acceptedGeneration: 8,
      initial: searchStatus("building", 8),
      budgetMs: 1_000,
      timeoutCode: "workspace_search.test_terminal_timeout",
      ownsLifecycle: () => true,
      readStatus: () => pendingStatus.promise,
      publishObservation,
    });

    const rejection = expect(observation).rejects.toThrow(
      "workspace_search.test_terminal_timeout",
    );
    await vi.advanceTimersByTimeAsync(1_000);
    await rejection;
    expect(publishObservation).toHaveBeenCalledTimes(1);

    pendingStatus.resolve(searchStatus("degraded", 8));
    await vi.runAllTimersAsync();
    expect(publishObservation).toHaveBeenCalledTimes(1);
  });
});
