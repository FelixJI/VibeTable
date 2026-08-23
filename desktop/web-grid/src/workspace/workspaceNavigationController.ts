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
  readonly authorizeDeparture: () => WorkspaceDepartureLease | null;
  readonly attempt: (action: () => void) => boolean;
}

export interface WorkspaceDepartureLease {
  commit(): void;
}

/**
 * Coordinates authorization and one-shot cleanup when leaving a workspace
 * surface. Synchronous navigation commits immediately; asynchronous callers
 * retain a lease until their target operation reaches a successful terminal.
 */
export function createWorkspaceNavigationController(
  state: WorkspaceNavigationState,
  commands: WorkspaceNavigationCommands,
): WorkspaceNavigationController {
  function hasUnsavedChanges(): boolean {
    return state.dashboardDirty() || state.surfaceDirty();
  }

  function authorizeDeparture(): WorkspaceDepartureLease | null {
    if (state.dashboardDirty() && !commands.confirmDashboardDiscard()) return null;
    if (state.surfaceDirty() && !commands.confirmSurfaceDiscard()) return null;
    let committed = false;
    return {
      commit(): void {
        if (committed) return;
        committed = true;
        commands.stopDashboardDraft();
        commands.resetSurfaceDraft();
      },
    };
  }

  function attempt(action: () => void): boolean {
    const departure = authorizeDeparture();
    if (!departure) return false;
    departure.commit();
    action();
    return true;
  }

  return { hasUnsavedChanges, authorizeDeparture, attempt };
}
