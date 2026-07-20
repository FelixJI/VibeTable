using System.IO;
using VibeTable.Infrastructure.Workspace;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Storage;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Resolves the most recently mounted valid workspace or creates a visible,
/// ordinary Windows folder for the current user. It never adopts a non-empty
/// unrecognized directory.
/// </summary>
public sealed class ManagedWorkspaceProvisioner
{
    private const string DefaultWorkspaceName = "VibeTable 工作区";
    private readonly WorkspaceMountStore _mounts;
    private readonly string _partitionKey;
    private readonly string _documentsRoot;

    public ManagedWorkspaceProvisioner(
        WorkspaceMountStore mounts,
        string partitionKey,
        string? documentsRoot = null)
    {
        _mounts = mounts ?? throw new ArgumentNullException(nameof(mounts));
        ArgumentException.ThrowIfNullOrWhiteSpace(partitionKey);
        _partitionKey = partitionKey;
        _documentsRoot = documentsRoot
            ?? Environment.GetFolderPath(Environment.SpecialFolder.MyDocuments);
    }

    public ManagedWorkspaceSelection EnsurePreferred()
    {
        foreach (var mount in _mounts.ReadAll().AsEnumerable().Reverse())
        {
            if (string.Equals(
                    mount.PartitionKey,
                    _partitionKey,
                    StringComparison.Ordinal)
                && TryReadWorkspace(mount.LocalRoot, out var mountedManifest)
                && string.Equals(
                    mountedManifest!.WorkspaceId, mount.WorkspaceId, StringComparison.Ordinal))
            {
                return new ManagedWorkspaceSelection(
                    mountedManifest.WorkspaceId,
                    mount.LocalRoot,
                    mountedManifest.Name);
            }
        }

        if (string.IsNullOrWhiteSpace(_documentsRoot))
            throw new DocumentFileOperationException(
                "无法确定当前用户的文档目录。",
                "WORKSPACE_ROOT_UNAVAILABLE");
        Directory.CreateDirectory(_documentsRoot);
        string root = FindUnusedRoot(_documentsRoot);
        Directory.CreateDirectory(root);
        RejectReparsePoint(root);

        string workspaceId = Guid.NewGuid().ToString("D");
        string backup = Path.Combine(root, ".backup");
        foreach (string directory in new[]
        {
            backup,
            Path.Combine(backup, ".staging"),
            Path.Combine(backup, "objects"),
            Path.Combine(backup, "revisions"),
            Path.Combine(backup, "refs"),
            Path.Combine(backup, "documents"),
            Path.Combine(backup, "folders"),
        })
        {
            Directory.CreateDirectory(directory);
        }

        var manifest = new WorkspaceManifest(
            WorkspaceManifest.CurrentFormatVersion,
            workspaceId,
            DefaultWorkspaceName,
            DateTimeOffset.UtcNow.ToString("O"));
        new AtomicJsonStore().Write(Path.Combine(backup, "workspace.json"), manifest);
        _mounts.Mount(workspaceId, root, DefaultWorkspaceName, _partitionKey);
        return new ManagedWorkspaceSelection(workspaceId, root, DefaultWorkspaceName);
    }

    private static bool TryReadWorkspace(string root, out WorkspaceManifest? manifest)
    {
        manifest = null;
        try
        {
            if (!Directory.Exists(root)) return false;
            RejectReparsePoint(root);
            string manifestPath = WorkspacePathGuard.ResolveAndCheck(
                root,
                ".backup/workspace.json");
            manifest = new AtomicJsonStore().Read<WorkspaceManifest>(manifestPath);
            return manifest is { FormatVersion: WorkspaceManifest.CurrentFormatVersion };
        }
        catch
        {
            return false;
        }
    }

    private static string FindUnusedRoot(string documentsRoot)
    {
        for (int suffix = 1; suffix <= 10_000; suffix++)
        {
            string name = suffix == 1
                ? DefaultWorkspaceName
                : $"{DefaultWorkspaceName} ({suffix})";
            string candidate = Path.GetFullPath(Path.Combine(documentsRoot, name));
            if (!Directory.Exists(candidate)) return candidate;
            RejectReparsePoint(candidate);
            if (!Directory.EnumerateFileSystemEntries(candidate).Any()) return candidate;
        }
        throw new DocumentFileOperationException(
            "无法创建默认工作区文件夹。",
            "WORKSPACE_ROOT_UNAVAILABLE");
    }

    private static void RejectReparsePoint(string root)
    {
        if (File.GetAttributes(root).HasFlag(FileAttributes.ReparsePoint))
            throw new DocumentFileOperationException(
                "工作区根目录不能是符号链接或重解析点。",
                "WORKSPACE_ROOT_INVALID");
    }
}

public sealed record ManagedWorkspaceSelection(
    string WorkspaceId,
    string Root,
    string DisplayName);
