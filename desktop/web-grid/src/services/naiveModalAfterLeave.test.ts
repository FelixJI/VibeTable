import { describe, expect, it, vi } from "vitest";

import { createNaiveModalAfterLeaveAdapter } from "./naiveModalAfterLeave";

describe("Naive modal after-leave adapter", () => {
  it("reports a synchronous close release failure", async () => {
    const failure = new Error("sync release failed");
    const reportError = vi.fn();
    const adapter = createNaiveModalAfterLeaveAdapter({
      claimRelease: () => ({
        release: () => { throw failure; },
      }),
      reportError,
    });

    adapter.beforeLeave();
    await expect(adapter.afterLeave()).resolves.toBeUndefined();

    expect(reportError).toHaveBeenCalledWith(failure);
  });

  it("reports an asynchronous close release failure", async () => {
    const failure = new Error("async release failed");
    const reportError = vi.fn();
    const adapter = createNaiveModalAfterLeaveAdapter({
      claimRelease: () => ({
        release: async () => { throw failure; },
      }),
      reportError,
    });

    adapter.beforeLeave();
    await expect(adapter.afterLeave()).resolves.toBeUndefined();

    expect(reportError).toHaveBeenCalledWith(failure);
  });
});
