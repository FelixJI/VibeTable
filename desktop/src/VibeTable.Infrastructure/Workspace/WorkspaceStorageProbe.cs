using System.ComponentModel;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;
using VibeTable.Contracts;

namespace VibeTable.Infrastructure.Workspace;

public sealed class WorkspaceStorageProbe
{
    private static readonly TimeSpan ProbeCleanupTimeout = TimeSpan.FromSeconds(2);
    private static readonly TimeSpan ProbeCleanupPollInterval = TimeSpan.FromMilliseconds(10);

    private readonly Func<string, bool> _isRegisteredCloudPath;
    private readonly Func<SafeFileHandle, WorkspaceRemoteProtocol>
        _remoteProtocolProbe;

    public WorkspaceStorageProbe()
        : this(
            WindowsCloudFilesPathProbe.IsUnderRegisteredSyncRoot,
            WindowsRemoteProtocolProbe.Detect)
    {
    }

    internal WorkspaceStorageProbe(
        Func<string, bool> isRegisteredCloudPath,
        Func<SafeFileHandle, WorkspaceRemoteProtocol>? remoteProtocolProbe = null)
    {
        _isRegisteredCloudPath = isRegisteredCloudPath
            ?? throw new ArgumentNullException(nameof(isRegisteredCloudPath));
        _remoteProtocolProbe = remoteProtocolProbe
            ?? WindowsRemoteProtocolProbe.Detect;
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
        WorkspaceRemoteProtocol? remoteProtocol = ProbeDurableRename(
            root,
            kind == WorkspaceStorageKind.Network);
        return new WorkspaceStorageObservation(
            kind,
            coordination,
            drive.AvailableFreeSpace,
            File.GetAttributes(root).HasFlag(FileAttributes.ReparsePoint),
            DateTimeOffset.UtcNow,
            cloudRoot,
            remoteProtocol);
    }

    private static bool IsWithin(string path, string candidateRoot)
    {
        var relative = Path.GetRelativePath(candidateRoot, path);
        return relative == "." ||
               (!Path.IsPathRooted(relative) &&
                relative != ".." &&
                !relative.StartsWith($"..{Path.DirectorySeparatorChar}", StringComparison.Ordinal));
    }

    private WorkspaceRemoteProtocol? ProbeDurableRename(
        string root,
        bool detectRemoteProtocol)
    {
        var source = Path.Combine(root, $".vibetable-probe-{Guid.NewGuid():N}.tmp");
        var destination = source + ".renamed";
        WorkspaceRemoteProtocol? remoteProtocol = null;
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
                if (detectRemoteProtocol)
                    remoteProtocol = _remoteProtocolProbe(stream.SafeFileHandle);
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
            WaitForProbeCleanup(root, source, destination);
        }
        return remoteProtocol;
    }

    private static void WaitForProbeCleanup(
        string root,
        string source,
        string destination)
    {
        string sourceName = Path.GetFileName(source);
        string destinationName = Path.GetFileName(destination);
        long deadline = Environment.TickCount64 + (long)ProbeCleanupTimeout.TotalMilliseconds;
        while (ProbeEntryIsVisible(root, sourceName, destinationName))
        {
            if (Environment.TickCount64 >= deadline)
            {
                throw new WorkspaceRegistryException(
                    "workspace.storage_probe_cleanup_failed",
                    "Storage probe cleanup did not become visible before workspace creation.");
            }
            Thread.Sleep(ProbeCleanupPollInterval);
        }
    }

    private static bool ProbeEntryIsVisible(
        string root,
        string sourceName,
        string destinationName)
        => Directory.EnumerateFileSystemEntries(root).Any(path =>
            string.Equals(
                Path.GetFileName(path),
                sourceName,
                StringComparison.OrdinalIgnoreCase)
            || string.Equals(
                Path.GetFileName(path),
                destinationName,
                StringComparison.OrdinalIgnoreCase));
}

public enum WorkspaceRemoteProtocol
{
    Smb,
    Other,
}

public sealed record WorkspaceStorageObservation(
    WorkspaceStorageKind StorageKind,
    WorkspaceCoordinationStrength CoordinationStrength,
    long AvailableBytes,
    bool IsReparsePoint,
    DateTimeOffset ObservedAt,
    string? RegisteredCloudRoot = null,
    WorkspaceRemoteProtocol? RemoteProtocol = null);

internal static class WindowsRemoteProtocolProbe
{
    private const int FileRemoteProtocolInfo = 13;
    private const uint WnncNetSmb = 0x00020000;

    public static WorkspaceRemoteProtocol Detect(SafeFileHandle fileHandle)
    {
        ArgumentNullException.ThrowIfNull(fileHandle);
        if (fileHandle.IsInvalid || fileHandle.IsClosed)
            throw new ArgumentException(
                "A valid file handle is required.",
                nameof(fileHandle));
        if (!GetFileInformationByHandleEx(
                fileHandle,
                FileRemoteProtocolInfo,
                out FileRemoteProtocolInformation information,
                (uint)Marshal.SizeOf<FileRemoteProtocolInformation>()))
        {
            throw new WorkspaceRegistryException(
                "workspace.storage_protocol_probe_failed",
                "Windows could not identify the remote storage protocol.",
                new Win32Exception(Marshal.GetLastWin32Error()));
        }
        return Classify(information.Protocol);
    }

    internal static WorkspaceRemoteProtocol Classify(uint protocol)
        => protocol == WnncNetSmb
            ? WorkspaceRemoteProtocol.Smb
            : WorkspaceRemoteProtocol.Other;

    [DllImport(
        "Kernel32.dll",
        ExactSpelling = true,
        SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool GetFileInformationByHandleEx(
        SafeFileHandle fileHandle,
        int fileInformationClass,
        out FileRemoteProtocolInformation fileInformation,
        uint bufferSize);

    [StructLayout(LayoutKind.Explicit, Size = 180)]
    private struct FileRemoteProtocolInformation
    {
        [FieldOffset(4)]
        public uint Protocol;
    }
}

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
