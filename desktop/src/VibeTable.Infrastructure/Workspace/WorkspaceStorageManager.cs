using System.Security.Cryptography;
using VibeTable.Contracts;

namespace VibeTable.Infrastructure.Workspace;

public enum WorkspaceStorageOperation
{
    Move,
    ReleaseActivityCache,
}

public sealed record WorkspaceStoragePlan(
    Guid PlanId,
    WorkspaceStorageOperation Operation,
    Guid WorkspaceId,
    string SourceRoot,
    string? TargetRoot,
    string SourceFingerprint,
    long BytesToCopy,
    DateTimeOffset ExpiresAt);

public sealed record ReleaseActivityCacheContext(
    bool SessionClosed,
    bool ReplicaComplete,
    bool HasPendingSync,
    bool ReplicaReopenVerified);

public sealed class WorkspaceStorageManager
{
    private static readonly HashSet<string> DeviceLocalLockFiles =
        new(StringComparer.OrdinalIgnoreCase)
        {
            ".vibetable/coordination/desktop-writer.lock",
            ".vibetable/coordination/storage-maintenance.lock",
        };

    private readonly TimeProvider _timeProvider;

    public WorkspaceStorageManager(TimeProvider? timeProvider = null)
    {
        _timeProvider = timeProvider ?? TimeProvider.System;
    }

    public WorkspaceStoragePlan PreviewMove(
        string sourceRoot,
        string targetRoot,
        TimeSpan? lifetime = null)
    {
        var source = Path.GetFullPath(sourceRoot);
        var target = Path.GetFullPath(targetRoot);
        EnsureDistinctTrees(source, target);
        EnsureNoReparseAncestors(target);
        var manifest = WorkspaceLayout.ReadManifest(source);
        RequireEmptyTarget(target);
        var inventory = Inventory(source);
        return new WorkspaceStoragePlan(
            Guid.NewGuid(),
            WorkspaceStorageOperation.Move,
            manifest.WorkspaceId,
            source,
            target,
            Fingerprint(inventory),
            inventory.Sum(item => item.Length),
            _timeProvider.GetUtcNow() + (lifetime ?? TimeSpan.FromMinutes(10)));
    }

    public void ApplyMove(WorkspaceStoragePlan plan)
    {
        RequireCurrentPlan(plan, WorkspaceStorageOperation.Move);
        var target = plan.TargetRoot ??
            throw new WorkspaceRegistryException(
                "workspace.storage_target_missing",
                "Move target is missing.");
        var sourceManifest = WorkspaceLayout.ReadManifest(plan.SourceRoot);
        if (sourceManifest.WorkspaceId != plan.WorkspaceId ||
            Fingerprint(Inventory(plan.SourceRoot)) != plan.SourceFingerprint)
            throw new WorkspaceRegistryException(
                "workspace.storage_plan_stale",
                "Workspace changed after the move preview.");
        EnsureDistinctTrees(plan.SourceRoot, target);
        EnsureNoReparseAncestors(target);
        if (Directory.Exists(target)
            && Directory.EnumerateFileSystemEntries(target).Any())
        {
            WorkspaceManifestV2 copiedManifest =
                WorkspaceLayout.ReadManifest(target);
            if (copiedManifest.WorkspaceId == plan.WorkspaceId
                && Fingerprint(Inventory(target))
                    == plan.SourceFingerprint)
            {
                // Resume safely after a crash between durable target publish
                // and the registry's atomic path update.
                return;
            }
            throw new WorkspaceRegistryException(
                "workspace.storage_target_not_empty",
                "Storage target changed after preview.");
        }

        var staging = target + $".vibetable-moving-{plan.PlanId:N}";
        if (Directory.Exists(staging))
            throw new WorkspaceRegistryException(
                "workspace.storage_staging_exists",
                "Move staging directory already exists.");
        try
        {
            CopyTree(plan.SourceRoot, staging);
            var targetManifest = WorkspaceLayout.ReadManifest(staging);
            if (targetManifest.WorkspaceId != plan.WorkspaceId ||
                Fingerprint(Inventory(staging)) != plan.SourceFingerprint)
                throw new WorkspaceRegistryException(
                    "workspace.storage_verify_failed",
                    "Moved workspace did not match the source.");
            if (Directory.Exists(target))
            {
                if (Directory.EnumerateFileSystemEntries(target).Any())
                    throw new WorkspaceRegistryException(
                        "workspace.storage_target_not_empty",
                        "Storage target changed while the copy was verified.");
                Directory.Delete(target);
            }
            Directory.Move(staging, target);
        }
        catch
        {
            if (Directory.Exists(staging))
                DeleteTreeWithoutFollowingReparsePoints(staging);
            throw;
        }
    }

    public WorkspaceStoragePlan PreviewReleaseActivityCache(
        string activityRoot,
        ReleaseActivityCacheContext context,
        TimeSpan? lifetime = null)
    {
        if (!context.SessionClosed || !context.ReplicaComplete ||
            context.HasPendingSync || !context.ReplicaReopenVerified)
            throw new WorkspaceRegistryException(
                "workspace.release_cache_unsafe",
                "Close the workspace and independently verify a complete replica before releasing cache.");
        var root = Path.GetFullPath(activityRoot);
        var manifest = WorkspaceLayout.ReadManifest(root);
        var inventory = Inventory(root);
        return new WorkspaceStoragePlan(
            Guid.NewGuid(),
            WorkspaceStorageOperation.ReleaseActivityCache,
            manifest.WorkspaceId,
            root,
            null,
            Fingerprint(inventory),
            inventory.Sum(item => item.Length),
            _timeProvider.GetUtcNow() + (lifetime ?? TimeSpan.FromMinutes(5)));
    }

    public void ApplyReleaseActivityCache(
        WorkspaceStoragePlan plan,
        ReleaseActivityCacheContext context)
    {
        RequireCurrentPlan(plan, WorkspaceStorageOperation.ReleaseActivityCache);
        if (!context.SessionClosed || !context.ReplicaComplete ||
            context.HasPendingSync || !context.ReplicaReopenVerified ||
            WorkspaceLayout.ReadManifest(plan.SourceRoot).WorkspaceId != plan.WorkspaceId ||
            Fingerprint(Inventory(plan.SourceRoot)) != plan.SourceFingerprint)
            throw new WorkspaceRegistryException(
                "workspace.release_cache_unsafe",
                "Release-cache conditions changed after preview.");
        WorkspaceLayout.DeleteWorkspaceRoot(plan.SourceRoot, plan.WorkspaceId);
    }

    private void RequireCurrentPlan(
        WorkspaceStoragePlan plan,
        WorkspaceStorageOperation expectedOperation)
    {
        if (plan.Operation != expectedOperation ||
            plan.PlanId == Guid.Empty ||
            plan.ExpiresAt <= _timeProvider.GetUtcNow())
            throw new WorkspaceRegistryException(
                "workspace.storage_plan_stale",
                "Storage plan is invalid or expired.");
    }

    private static IReadOnlyList<InventoryEntry> Inventory(string root)
    {
        string normalized = Path.GetFullPath(root);
        var inventory = new List<InventoryEntry>();
        InventoryDirectory(normalized, normalized, inventory);
        return inventory
            .OrderBy(item => item.RelativePath, StringComparer.Ordinal)
            .ToArray();
    }

    private static void InventoryDirectory(
        string root,
        string directory,
        ICollection<InventoryEntry> inventory)
    {
        var info = new DirectoryInfo(directory);
        if ((info.Attributes & FileAttributes.ReparsePoint) != 0)
            throw ReparsePointError();
        foreach (FileInfo file in info.EnumerateFiles())
        {
            if ((file.Attributes & FileAttributes.ReparsePoint) != 0)
                throw ReparsePointError();
            string relativePath = Path.GetRelativePath(root, file.FullName)
                .Replace('\\', '/');
            if (DeviceLocalLockFiles.Contains(relativePath))
                continue;
            using var stream = new FileStream(
                file.FullName,
                FileMode.Open,
                FileAccess.Read,
                FileShare.Read);
            long length = stream.Length;
            string sha256 = Convert.ToHexString(
                    SHA256.HashData(stream))
                .ToLowerInvariant();
            file.Refresh();
            if (stream.Length != length
                || file.Length != length)
            {
                throw new WorkspaceRegistryException(
                    "workspace.storage_source_changed",
                    "Workspace changed while it was inventoried.");
            }
            inventory.Add(new InventoryEntry(
                relativePath,
                length,
                sha256));
        }
        foreach (DirectoryInfo child in info.EnumerateDirectories())
            InventoryDirectory(root, child.FullName, inventory);
    }

    private static string Fingerprint(IEnumerable<InventoryEntry> inventory)
    {
        using var hash = IncrementalHash.CreateHash(HashAlgorithmName.SHA256);
        foreach (var item in inventory)
        {
            hash.AppendData(System.Text.Encoding.UTF8.GetBytes(
                $"{item.RelativePath}\0{item.Length}\0{item.Sha256}\0"));
        }
        return Convert.ToHexString(hash.GetHashAndReset()).ToLowerInvariant();
    }

    private static void CopyTree(string sourceRoot, string targetRoot)
    {
        Directory.CreateDirectory(targetRoot);
        CopyDirectory(sourceRoot, sourceRoot, targetRoot);
    }

    private static void CopyDirectory(
        string sourceRoot,
        string sourceDirectory,
        string targetRoot)
    {
        var directory = new DirectoryInfo(sourceDirectory);
        if ((directory.Attributes & FileAttributes.ReparsePoint) != 0)
            throw ReparsePointError();
        foreach (FileInfo source in directory.EnumerateFiles())
        {
            if ((source.Attributes & FileAttributes.ReparsePoint) != 0)
                throw ReparsePointError();
            string relativePath =
                Path.GetRelativePath(sourceRoot, source.FullName)
                    .Replace('\\', '/');
            if (DeviceLocalLockFiles.Contains(relativePath))
                continue;
            var target = Path.Combine(
                targetRoot,
                relativePath.Replace('/', Path.DirectorySeparatorChar));
            Directory.CreateDirectory(Path.GetDirectoryName(target)!);
            File.Copy(source.FullName, target, overwrite: false);
            using var stream = new FileStream(
                target,
                FileMode.Open,
                FileAccess.ReadWrite,
                FileShare.None,
                4096,
                FileOptions.WriteThrough);
            stream.Flush(flushToDisk: true);
        }
        foreach (DirectoryInfo child in directory.EnumerateDirectories())
        {
            string target = Path.Combine(
                targetRoot,
                Path.GetRelativePath(sourceRoot, child.FullName));
            Directory.CreateDirectory(target);
            CopyDirectory(sourceRoot, child.FullName, targetRoot);
        }
    }

    private static void RequireEmptyTarget(string target)
    {
        if (Directory.Exists(target))
        {
            if ((File.GetAttributes(target) & FileAttributes.ReparsePoint) != 0)
                throw ReparsePointError();
            if (Directory.EnumerateFileSystemEntries(target).Any())
                throw new WorkspaceRegistryException(
                    "workspace.storage_target_not_empty",
                    "Storage target must be new or empty.");
        }
    }

    private static void EnsureDistinctTrees(string source, string target)
    {
        string normalizedSource = Path.TrimEndingDirectorySeparator(
            Path.GetFullPath(source));
        string normalizedTarget = Path.TrimEndingDirectorySeparator(
            Path.GetFullPath(target));
        if (string.Equals(
                normalizedSource,
                normalizedTarget,
                StringComparison.OrdinalIgnoreCase)
            || IsDescendant(normalizedSource, normalizedTarget)
            || IsDescendant(normalizedTarget, normalizedSource))
        {
            throw new WorkspaceRegistryException(
                "workspace.storage_target_overlaps_source",
                "Storage source and target must be separate directory trees.");
        }
    }

    private static bool IsDescendant(string parent, string candidate)
        => candidate.StartsWith(
            parent + Path.DirectorySeparatorChar,
            StringComparison.OrdinalIgnoreCase);

    private static void EnsureNoReparseAncestors(string path)
    {
        DirectoryInfo? current = new DirectoryInfo(path);
        while (current is not null)
        {
            if (current.Exists
                && (current.Attributes & FileAttributes.ReparsePoint) != 0)
                throw ReparsePointError();
            current = current.Parent;
        }
    }

    private static void DeleteTreeWithoutFollowingReparsePoints(string root)
    {
        var directory = new DirectoryInfo(root);
        if ((directory.Attributes & FileAttributes.ReparsePoint) != 0)
            throw ReparsePointError();
        foreach (FileInfo file in directory.EnumerateFiles())
        {
            if ((file.Attributes & FileAttributes.ReparsePoint) != 0)
                throw ReparsePointError();
            file.Delete();
        }
        foreach (DirectoryInfo child in directory.EnumerateDirectories())
            DeleteTreeWithoutFollowingReparsePoints(child.FullName);
        directory.Delete();
    }

    private static WorkspaceRegistryException ReparsePointError()
        => new(
            "workspace.storage_reparse_point_unsupported",
            "Storage operations never follow reparse points.");

    private sealed record InventoryEntry(string RelativePath, long Length, string Sha256);
}
