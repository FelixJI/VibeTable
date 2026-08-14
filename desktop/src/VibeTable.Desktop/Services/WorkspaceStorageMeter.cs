using System.IO;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

public readonly record struct WorkspaceStorageMeasurement(
    long LogicalSize,
    long PhysicalSize);

public interface IWorkspaceStorageMeter
{
    WorkspaceStorageMeasurement Measure(WorkspaceRegistryEntryV2 workspace);
}

/// <summary>
/// Measures one workspace without following reparse points or guessing
/// reclaimable storage. Provider disconnects leave health as the authority.
/// </summary>
public sealed class WorkspaceStorageMeter : IWorkspaceStorageMeter
{
    public WorkspaceStorageMeasurement Measure(WorkspaceRegistryEntryV2 workspace)
        => MeasureDirectory(workspace);

    internal static WorkspaceStorageMeasurement MeasureDirectory(
        WorkspaceRegistryEntryV2 workspace)
    {
        ArgumentNullException.ThrowIfNull(workspace);
        string activityRoot =
            ProductionWorkspaceRuntimeFactory.RuntimeRoot(workspace);
        WorkspacePaths activity = WorkspaceLayout.Paths(activityRoot);
        long logical = AddSaturating(
            MeasureDirectoryBytes(activity.Data),
            MeasureDirectoryBytes(activity.Files));

        string selectedRoot = Path.GetFullPath(workspace.SelectedRoot);
        long physical = 0;
        foreach (string root in new[] { selectedRoot, activityRoot }
                     .Distinct(StringComparer.OrdinalIgnoreCase))
        {
            physical = AddSaturating(
                physical,
                MeasureDirectoryBytes(root));
        }
        return new WorkspaceStorageMeasurement(logical, physical);
    }

    private static long MeasureDirectoryBytes(string root)
    {
        if (!Directory.Exists(root))
            return 0;
        var options = new EnumerationOptions
        {
            RecurseSubdirectories = true,
            IgnoreInaccessible = true,
            ReturnSpecialDirectories = false,
            AttributesToSkip = FileAttributes.ReparsePoint,
        };
        long total = 0;
        try
        {
            foreach (string path in Directory.EnumerateFiles(
                         root,
                         "*",
                         options))
            {
                try
                {
                    total = AddSaturating(total, new FileInfo(path).Length);
                }
                catch (Exception exception) when (
                    exception is IOException or UnauthorizedAccessException)
                {
                    // The live workspace may rotate files while metrics are
                    // sampled. Skip only that file and keep the projection.
                }
            }
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            // A provider may disconnect during bootstrap. Health remains the
            // authoritative signal; never follow a fallback path.
        }
        return total;
    }

    private static long AddSaturating(long left, long right)
        => left > long.MaxValue - right
            ? long.MaxValue
            : left + right;
}
