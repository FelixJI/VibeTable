using System.IO;
using System.Text.Json;

namespace VibeTable.Infrastructure.Workspace;

/// <summary>
/// Per-machine mount registry for workspace roots.
///
/// Stored at <c>%LOCALAPPDATA%/VibeTable/workspace-mounts.json</c>. Maps
/// <c>workspaceId → localRoot</c>. Does NOT enter the data service or the Web layer;
/// each machine maps the same workspace UUID to its own local path.
/// </summary>
public sealed class WorkspaceMountStore
{
    private readonly string _storePath;
    private static readonly JsonSerializerOptions JsonOptions = new(JsonSerializerDefaults.Web);

    /// <summary>
    /// One mount entry.
    /// </summary>
    public sealed record MountEntry(
        string WorkspaceId,
        string LocalRoot,
        string DisplayName,
        string MountedAt,
        string? PartitionKey = null);

    /// <summary>
    /// The on-disk JSON shape.
    /// </summary>
    public sealed record MountFile(int FormatVersion, List<MountEntry> Mounts)
    {
        public const int CurrentFormatVersion = 1;
    }

    public WorkspaceMountStore(string? localAppData = null)
    {
        var baseDir = localAppData ?? Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData);
        var vibeTableDir = Path.Combine(baseDir, "VibeTable");
        Directory.CreateDirectory(vibeTableDir);
        _storePath = Path.Combine(vibeTableDir, "workspace-mounts.json");
    }

    /// <summary>
    /// Read all mounts. Returns an empty list if the file does not exist.
    /// </summary>
    public List<MountEntry> ReadAll()
    {
        if (!File.Exists(_storePath))
            return [];

        var json = File.ReadAllText(_storePath);
        var file = JsonSerializer.Deserialize<MountFile>(json, JsonOptions);
        return file?.Mounts ?? [];
    }

    /// <summary>
    /// Resolve a workspace UUID to its local root. Returns null if not mounted.
    /// </summary>
    public string? ResolveRoot(string workspaceId, string? partitionKey = null)
    {
        return ReadAll()
            .FirstOrDefault(m =>
                m.WorkspaceId == workspaceId
                && (partitionKey is null
                    || string.Equals(
                        m.PartitionKey,
                        partitionKey,
                        StringComparison.Ordinal)))?
            .LocalRoot;
    }

    /// <summary>
    /// Add or update a mount.
    /// </summary>
    public void Mount(
        string workspaceId,
        string localRoot,
        string displayName,
        string? partitionKey = null)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(workspaceId);
        ArgumentException.ThrowIfNullOrWhiteSpace(localRoot);
        ArgumentException.ThrowIfNullOrWhiteSpace(displayName);

        var normalizedRoot = Path.GetFullPath(localRoot);
        if (!Directory.Exists(normalizedRoot))
            throw new DirectoryNotFoundException(
                $"workspace root does not exist: {normalizedRoot}");
        if (File.GetAttributes(normalizedRoot).HasFlag(FileAttributes.ReparsePoint))
            throw new InvalidOperationException(
                $"workspace root may not be a reparse point: {normalizedRoot}");

        var mounts = ReadAll();
        mounts = mounts.Where(m => m.WorkspaceId != workspaceId).ToList();
        mounts.Add(new MountEntry(
            workspaceId,
            normalizedRoot,
            displayName,
            DateTime.UtcNow.ToString("o"),
            string.IsNullOrWhiteSpace(partitionKey) ? null : partitionKey));
        Write(mounts);
    }

    /// <summary>
    /// Remove a mount.
    /// </summary>
    public void Unmount(string workspaceId)
    {
        var mounts = ReadAll().Where(m => m.WorkspaceId != workspaceId).ToList();
        Write(mounts);
    }

    private void Write(List<MountEntry> mounts)
    {
        var file = new MountFile(MountFile.CurrentFormatVersion, mounts);
        var json = JsonSerializer.Serialize(file, JsonOptions);
        var tempPath = _storePath + ".tmp";
        File.WriteAllText(tempPath, json);

        if (File.Exists(_storePath))
            File.Replace(tempPath, _storePath, destinationBackupFileName: null);
        else
            File.Move(tempPath, _storePath);
    }
}
