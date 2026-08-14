export interface WorkspaceNavigationState {
  readonly dashboardDirty: () => boolean;
  readonly surfaceDirty: () => boolean;
}

export interface WorkspaceNavigationCommands {
  readonly confirmDashboardDiscard: () => boolean;
  readonly confirmSurfaceDiscard: () => boolean;
  readonly stopDashboardDraft: () => void;
  readonly resetSurfaceDraft: () => void;
}

export interface WorkspaceNavigationController {
  readonly hasUnsavedChanges: () => boolean;
  readonly attempt: (action: () => void) => boolean;
}

/**
 * Coordinates leaving the current workspace surface. Callers provide only the
 * target action; confirmation order and aggregate cleanup remain local here.
 */
export function createWorkspaceNavigationController(
  state: WorkspaceNavigationState,
  commands: WorkspaceNavigationCommands,
): WorkspaceNavigationController {
  function hasUnsavedChanges(): boolean {
    return state.dashboardDirty() || state.surfaceDirty();
  }

  function attempt(action: () => void): boolean {
    if (state.dashboardDirty() && !commands.confirmDashboardDiscard()) return false;
    if (state.surfaceDirty() && !commands.confirmSurfaceDiscard()) return false;
    commands.stopDashboardDraft();
    commands.resetSurfaceDraft();
    action();
    return true;
  }

  return { hasUnsavedChanges, attempt };
}
