using System.IO;
using VibeTable.Infrastructure;

namespace VibeTable.Desktop.Services;

internal interface IPortableModeStore
{
    bool IsEnabled();

    /// <summary>Arms the switch and flips the marker; takes effect after the
    /// next launch applies the pending data-root migration.</summary>
    void SetEnabled(bool enabled);
}

/// <summary>
/// Thrown when the portable marker cannot be written or removed (typically a
/// read-only program directory). Nothing was changed when this surfaces.
/// </summary>
internal sealed class PortableModeWriteException : Exception
{
    public PortableModeWriteException(string message)
        : base(message)
    {
    }
}

/// <summary>
/// Marker-file authority for portable mode. Arming the migration before the
/// marker exists keeps every intermediate state safe: a pending switch that
/// never applies (marker write failed, or the user flipped back) is discarded
/// on next launch, while a marker without a pending migration would strand
/// user data in the old root.
/// </summary>
internal sealed class MarkerPortableModeStore : IPortableModeStore
{
    private readonly string _baseDirectory;
    private readonly string _localAppData;

    public MarkerPortableModeStore(string baseDirectory, string localAppData)
    {
        _baseDirectory = baseDirectory;
        _localAppData = localAppData;
    }

    public bool IsEnabled() =>
        LaunchPaths.IsPortableMode(_baseDirectory);

    public void SetEnabled(bool enabled)
    {
        if (enabled == IsEnabled())
        {
            return;
        }
        string portableRoot = LaunchPaths.ResolvePortableDataRoot(_baseDirectory);
        string standardRoot = LaunchPaths.ResolveStandardDataRoot(_localAppData);
        string marker = Path.Combine(
            portableRoot,
            LaunchPaths.PortableModeMarkerFileName);
        try
        {
            if (enabled)
            {
                DataRootMigrationService.Arm(standardRoot, portableRoot, true);
                Directory.CreateDirectory(portableRoot);
                File.WriteAllText(marker, string.Empty);
            }
            else
            {
                DataRootMigrationService.Arm(portableRoot, standardRoot, false);
                File.Delete(marker);
            }
        }
        catch (Exception exception) when (
            exception is IOException
                or UnauthorizedAccessException
                or System.Security.SecurityException)
        {
            throw new PortableModeWriteException(
                "无法更新便携模式标记文件："
                    + marker
                    + "（"
                    + exception.GetType().Name
                    + "）");
        }
    }
}

/// <summary>
/// In-memory store for redirected test/development launches: the switch is
/// visible in the UI for the session but never touches the real marker or
/// schedules a migration.
/// </summary>
internal sealed class InMemoryPortableModeStore : IPortableModeStore
{
    private bool _enabled;

    public bool IsEnabled() => _enabled;

    public void SetEnabled(bool enabled) => _enabled = enabled;
}
