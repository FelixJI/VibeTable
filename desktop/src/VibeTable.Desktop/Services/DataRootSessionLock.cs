using System.IO;
using VibeTable.Infrastructure;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Data-root ownership for one running session. The host holds the lock file
/// open with an exclusive share until the process exits, so a second instance
/// on the same data root fails to start and a mode-switch migration refuses
/// to move a root that another instance still uses. The OS releases the
/// handle on process death, so crashes never leave the root wedged.
/// </summary>
internal sealed class DataRootSessionLock : IDisposable
{
    private FileStream? _stream;

    private DataRootSessionLock(FileStream stream) => _stream = stream;

    /// <summary>
    /// Acquires the session lock for <paramref name="dataRoot"/>; returns
    /// null when another process already holds it (or the root is not
    /// writable).
    /// </summary>
    public static DataRootSessionLock? Acquire(string dataRoot)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(dataRoot);
        string path = Path.Combine(
            Path.GetFullPath(dataRoot),
            LaunchPaths.SessionLockFileName);
        try
        {
            Directory.CreateDirectory(Path.GetDirectoryName(path)!);
            return new DataRootSessionLock(new FileStream(
                path,
                FileMode.OpenOrCreate,
                FileAccess.ReadWrite,
                FileShare.None,
                64,
                FileOptions.None));
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            return null;
        }
    }

    /// <summary>True when another process currently holds the lock.</summary>
    public static bool IsHeldByAnotherProcess(string dataRoot)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(dataRoot);
        string path = Path.Combine(
            Path.GetFullPath(dataRoot),
            LaunchPaths.SessionLockFileName);
        try
        {
            using var probe = new FileStream(
                path,
                FileMode.OpenOrCreate,
                FileAccess.ReadWrite,
                FileShare.None,
                64,
                FileOptions.DeleteOnClose);
            return false;
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            return true;
        }
    }

    public void Dispose()
    {
        _stream?.Dispose();
        _stream = null;
    }
}
