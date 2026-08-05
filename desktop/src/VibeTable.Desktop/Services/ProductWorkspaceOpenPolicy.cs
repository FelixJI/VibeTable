using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Prevents the legacy product projection from racing the authoritative
/// workspace-session transition.
/// </summary>
public static class ProductWorkspaceOpenPolicy
{
    public static bool CanProject(WorkspaceSessionV2 session) =>
        session.Phase == WorkspaceSessionPhase.Idle &&
        session.State is
            WorkspaceSessionState.OpenedReadOnly or
            WorkspaceSessionState.OpenedWritable or
            WorkspaceSessionState.OpenedProvisional;
}
