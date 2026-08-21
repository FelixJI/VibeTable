import { afterEach, describe, expect, it, vi } from "vitest";

import { reportStructuredDialogFocusE2EOutcome } from "./dialogFocusDiagnostics";

describe("structured dialog focus E2E diagnostics", () => {
  afterEach(() => {
    Reflect.deleteProperty(window, "__vibetableE2EBridgeDiagnostics");
    vi.restoreAllMocks();
  });

  it("does not expose focus outcomes without installed E2E diagnostics", () => {
    const dispatch = vi.spyOn(window, "dispatchEvent");

    reportStructuredDialogFocusE2EOutcome({
      leaseId: 1,
      state: "claimed",
      target: "json",
    });

    expect(dispatch).not.toHaveBeenCalled();
  });

  it("exposes only the typed focus outcome when E2E diagnostics is installed", () => {
    Reflect.set(window, "__vibetableE2EBridgeDiagnostics", { installed: true });
    const dispatch = vi.spyOn(window, "dispatchEvent");
    const outcome = {
      leaseId: 2,
      state: "restored" as const,
      target: "attachment" as const,
      via: "reprojected" as const,
      rowKey: "private-row",
      field: "private-field",
    };

    reportStructuredDialogFocusE2EOutcome(outcome);

    const [event] = dispatch.mock.calls[0] ?? [];
    expect(event).toBeInstanceOf(CustomEvent);
    expect((event as CustomEvent).detail).toEqual({
      leaseId: 2,
      state: "restored",
      target: "attachment",
      via: "reprojected",
    });
  });

  it("does not expose an outcome without a closed diagnostic target", () => {
    Reflect.set(window, "__vibetableE2EBridgeDiagnostics", { installed: true });
    const dispatch = vi.spyOn(window, "dispatchEvent");

    reportStructuredDialogFocusE2EOutcome({
      leaseId: 3,
      state: "cancelled",
      reason: "stale",
    });

    expect(dispatch).not.toHaveBeenCalled();
  });
});
