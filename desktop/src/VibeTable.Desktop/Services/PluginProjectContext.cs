using System;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

public sealed record PluginProjectContext(
    string ProjectKey,
    string ProjectRevision,
    ulong SessionGeneration)
{
    public static PluginProjectContext? FromSession(WorkspaceSessionV2 session)
    {
        if (session.WorkspaceId is not Guid workspaceId
            || workspaceId == Guid.Empty
            || session.SessionEpoch == 0
            || !ProductWorkspaceOpenPolicy.CanProject(session)) return null;
        string identity = workspaceId.ToString("N");
        return new PluginProjectContext(
            $"local:{identity}",
            $"{identity}:{session.SessionEpoch}",
            session.SessionEpoch);
    }
}
