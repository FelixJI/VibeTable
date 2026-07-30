using System.Runtime.InteropServices;
using VibeTable.Contracts;

namespace VibeTable.Infrastructure.Workspace;

public sealed class WorkspaceStorageProbe
{
    private readonly Func<string, bool> _isRegisteredCloudPath;

    public WorkspaceStorageProbe()
        : this(WindowsCloudFilesPathProbe.IsUnderRegisteredSyncRoot)
    {
    }

    internal WorkspaceStorageProbe(Func<string, bool> isRegisteredCloudPath)
    {
        _isRegisteredCloudPath = isRegisteredCloudPath
            ?? throw new ArgumentNullException(nameof(isRegisteredCloudPath));
    }

    public WorkspaceStorageObservation Probe(
        string selectedRoot,
        bool userMarkedSync = false,
        IEnumerable<string>? registeredCloudRoots = null)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(selectedRoot);
        var root = Path.GetFullPath(selectedRoot);
        if (!Directory.Exists(root))
            throw new DirectoryNotFoundException(root);
        var drive = new DriveInfo(Path.GetPathRoot(root)!);
        var cloudRoot = registeredCloudRoots?
            .Select(Path.GetFullPath)
            .OrderByDescending(candidate => candidate.Length)
            .FirstOrDefault(candidate => IsWithin(root, candidate));
        bool registeredCloud = cloudRoot is not null
            || _isRegisteredCloudPath(root);
        var kind = registeredCloud
            ? WorkspaceStorageKind.RegisteredCloud
            : userMarkedSync
                ? WorkspaceStorageKind.UserMarkedSync
                : drive.DriveType switch
            {
                DriveType.Fixed => WorkspaceStorageKind.Fixed,
                DriveType.Network => WorkspaceStorageKind.Network,
                DriveType.Removable => WorkspaceStorageKind.Removable,
                _ => throw new WorkspaceRegistryException(
                    "workspace.storage_unsupported",
                    $"Storage type {drive.DriveType} is not supported."),
            };
        var coordination = kind == WorkspaceStorageKind.Fixed
            ? WorkspaceCoordinationStrength.Strong
            : WorkspaceCoordinationStrength.Advisory;
        ProbeDurableRename(root);
        return new WorkspaceStorageObservation(
            kind,
            coordination,
            drive.AvailableFreeSpace,
            File.GetAttributes(root).HasFlag(FileAttributes.ReparsePoint),
            DateTimeOffset.UtcNow,
            cloudRoot);
    }

    private static bool IsWithin(string path, string candidateRoot)
    {
        var relative = Path.GetRelativePath(candidateRoot, path);
        return relative == "." ||
               (!Path.IsPathRooted(relative) &&
                relative != ".." &&
                !relative.StartsWith($"..{Path.DirectorySeparatorChar}", StringComparison.Ordinal));
    }

    private static void ProbeDurableRename(string root)
    {
        var source = Path.Combine(root, $".vibetable-probe-{Guid.NewGuid():N}.tmp");
        var destination = source + ".renamed";
        try
        {
            using (var stream = new FileStream(
                       source,
                       FileMode.CreateNew,
                       FileAccess.Write,
                       FileShare.None,
                       4096,
                       FileOptions.WriteThrough))
            {
                stream.WriteByte(0x56);
                stream.Flush(flushToDisk: true);
            }
            File.Move(source, destination);
            using var read = new FileStream(
                destination,
                FileMode.Open,
                FileAccess.Read,
                FileShare.Read);
            if (read.ReadByte() != 0x56)
                throw new IOException("Storage probe readback failed.");
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            throw new WorkspaceRegistryException(
                "workspace.storage_probe_failed",
                "Storage does not satisfy durable write and rename requirements.",
                exception);
        }
        finally
        {
            if (File.Exists(source))
                File.Delete(source);
            if (File.Exists(destination))
                File.Delete(destination);
        }
    }
}

public sealed record WorkspaceStorageObservation(
    WorkspaceStorageKind StorageKind,
    WorkspaceCoordinationStrength CoordinationStrength,
    long AvailableBytes,
    bool IsReparsePoint,
    DateTimeOffset ObservedAt,
    string? RegisteredCloudRoot = null);

internal static class WindowsCloudFilesPathProbe
{
    private const int BasicInfoClass = 0;

    public static bool IsUnderRegisteredSyncRoot(string path)
    {
        if (!OperatingSystem.IsWindowsVersionAtLeast(10, 0, 16299))
            return false;
        try
        {
            int result = CfGetSyncRootInfoByPath(
                Path.GetFullPath(path),
                BasicInfoClass,
                out _,
                (uint)Marshal.SizeOf<CfSyncRootBasicInfo>(),
                out _);
            return result == 0;
        }
        catch (Exception exception) when (
            exception is DllNotFoundException
                or EntryPointNotFoundException
                or BadImageFormatException)
        {
            return false;
        }
    }

    [DllImport(
        "CldApi.dll",
        CharSet = CharSet.Unicode,
        ExactSpelling = true)]
    private static extern int CfGetSyncRootInfoByPath(
        string filePath,
        int infoClass,
        out CfSyncRootBasicInfo infoBuffer,
        uint infoBufferLength,
        out uint returnedLength);

    [StructLayout(LayoutKind.Sequential)]
    private readonly struct CfSyncRootBasicInfo
    {
        public readonly long SyncRootFileId;
    }
}
