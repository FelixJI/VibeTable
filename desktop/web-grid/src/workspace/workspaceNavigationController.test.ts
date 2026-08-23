import { describe, expect, it, vi } from "vitest";
import { createWorkspaceNavigationController } from "./workspaceNavigationController";

function harness(dashboardDirty: boolean, surfaceDirty: boolean) {
  const dashboard = vi.fn(() => true);
  const surface = vi.fn(() => true);
  const stop = vi.fn();
  const reset = vi.fn();
  const action = vi.fn();
  const controller = createWorkspaceNavigationController(
    { dashboardDirty: () => dashboardDirty, surfaceDirty: () => surfaceDirty },
    {
      confirmDashboardDiscard: dashboard,
      confirmSurfaceDiscard: surface,
      stopDashboardDraft: stop,
      resetSurfaceDraft: reset,
    },
  );
  return { controller, dashboard, surface, stop, reset, action };
}

describe("WorkspaceNavigationController", () => {
  it("authorizes deferred departure without cleanup until its lease commits", () => {
    const h = harness(true, true);

    const departure = h.controller.authorizeDeparture();

    expect(departure).not.toBeNull();
    expect(h.dashboard).toHaveBeenCalledOnce();
    expect(h.surface).toHaveBeenCalledOnce();
    expect(h.stop).not.toHaveBeenCalled();
    expect(h.reset).not.toHaveBeenCalled();

    departure!.commit();
    departure!.commit();
    expect(h.stop).toHaveBeenCalledOnce();
    expect(h.reset).toHaveBeenCalledOnce();
  });

  it("does not issue a departure lease when either dirty aggregate rejects", () => {
    const h = harness(true, true);
    h.surface.mockReturnValue(false);

    expect(h.controller.authorizeDeparture()).toBeNull();
    expect(h.stop).not.toHaveBeenCalled();
    expect(h.reset).not.toHaveBeenCalled();
  });

  it("runs a clean navigation through the full aggregate cleanup", () => {
    const h = harness(false, false);

    expect(h.controller.hasUnsavedChanges()).toBe(false);
    expect(h.controller.attempt(h.action)).toBe(true);
    expect(h.dashboard).not.toHaveBeenCalled();
    expect(h.surface).not.toHaveBeenCalled();
    expect(h.stop).toHaveBeenCalledOnce();
    expect(h.reset).toHaveBeenCalledOnce();
    expect(h.action).toHaveBeenCalledOnce();
  });

  it("stops at the first rejected dirty aggregate without cleanup or navigation", () => {
    const h = harness(true, true);
    h.dashboard.mockReturnValue(false);

    expect(h.controller.hasUnsavedChanges()).toBe(true);
    expect(h.controller.attempt(h.action)).toBe(false);
    expect(h.dashboard).toHaveBeenCalledOnce();
    expect(h.surface).not.toHaveBeenCalled();
    expect(h.stop).not.toHaveBeenCalled();
    expect(h.reset).not.toHaveBeenCalled();
    expect(h.action).not.toHaveBeenCalled();
  });

  it("checks Surface after Dashboard and preserves both drafts when Surface rejects", () => {
    const h = harness(true, true);
    h.surface.mockReturnValue(false);

    expect(h.controller.attempt(h.action)).toBe(false);
    expect(h.dashboard).toHaveBeenCalledOnce();
    expect(h.surface).toHaveBeenCalledOnce();
    expect(h.stop).not.toHaveBeenCalled();
    expect(h.reset).not.toHaveBeenCalled();
    expect(h.action).not.toHaveBeenCalled();
  });

  it("cleans both aggregates exactly once after every required confirmation", () => {
    const h = harness(true, true);

    expect(h.controller.attempt(h.action)).toBe(true);
    expect(h.dashboard).toHaveBeenCalledOnce();
    expect(h.surface).toHaveBeenCalledOnce();
    expect(h.stop).toHaveBeenCalledOnce();
    expect(h.reset).toHaveBeenCalledOnce();
    expect(h.action).toHaveBeenCalledOnce();
  });
});
