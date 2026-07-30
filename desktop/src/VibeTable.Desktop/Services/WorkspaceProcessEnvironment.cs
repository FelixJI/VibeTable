using System.Collections.Generic;
using System.IO;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Configures child processes for one already-resolved workspace data root.
/// It owns no global root preference and never selects or migrates storage.
/// </summary>
internal static class WorkspaceProcessEnvironment
{
    public static void Configure(
        IDictionary<string, string> environment,
        string workspaceDataRoot)
    {
        string dataRoot = Path.GetFullPath(workspaceDataRoot);
        environment["LOCALAPPDATA"] = dataRoot;
        environment["VIBETABLE_STATE_DIR"] = Path.Combine(dataRoot, "state");
    }
}
