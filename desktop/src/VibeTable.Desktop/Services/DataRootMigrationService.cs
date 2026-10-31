using System.IO;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Text.RegularExpressions;
using VibeTable.Infrastructure;

namespace VibeTable.Desktop.Services;

/// <summary>
/// One-shot data-root migration between standard and portable mode. Arming
/// writes a pending marker into the outgoing root; the next launch moves every
/// entry across before any runtime component opens a handle, and removes the
/// outgoing root only after it is fully drained. Interrupted migrations retry
/// safely: finished entries are gone from the source, half-copied transfers
/// are renamed into place or discarded, and conflicting leftovers keep the
/// source intact and fail loudly.
/// </summary>
internal static partial class DataRootMigrationService
{
    private static readonly JsonSerializerOptions PendingJsonOptions =
        new(JsonSerializerDefaults.Web);

    /// <summary>Durable intent recorded in the outgoing data root.</summary>
    public sealed record PendingModeSwitch(
        string TargetRoot,
        bool TargetPortable,
        DateTimeOffset RequestedAtUtc);

    public enum MigrationOutcome
    {
        None = 0,
        Migrated = 1,
    }

    /// <summary>Structured, bounded failure surfaced to the launch sequence.</summary>
    public sealed class DataRootMigrationException : Exception
    {
        public DataRootMigrationException(string errorCode, string message)
            : base(message)
        {
            ErrorCode = errorCode;
        }

        public string ErrorCode { get; }
    }

    /// <summary>
    /// Runs the pending mode switch (if any) for a normal launch. Test and
    /// development roots are explicitly redirected and never migrate.
    /// </summary>
    public static MigrationOutcome ApplyForLaunch(
        HostStartupOptions startup,
        string baseDirectory,
        string localAppData)
    {
        ArgumentNullException.ThrowIfNull(startup);
        if (startup.TestMode
            || !string.IsNullOrWhiteSpace(startup.DevelopmentDataRoot))
        {
            return MigrationOutcome.None;
        }
        string portableRoot = LaunchPaths.ResolvePortableDataRoot(baseDirectory);
        string standardRoot = LaunchPaths.ResolveStandardDataRoot(localAppData);
        bool portable = LaunchPaths.IsPortableMode(baseDirectory);
        return ApplyIfNeeded(
            portable ? portableRoot : standardRoot,
            portable ? standardRoot : portableRoot);
    }

    /// <summary>Records the switch intent inside the currently active root.</summary>
    public static void Arm(
        string currentDataRoot,
        string targetDataRoot,
        bool targetPortable)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(currentDataRoot);
        ArgumentException.ThrowIfNullOrWhiteSpace(targetDataRoot);
        string root = Path.GetFullPath(currentDataRoot);
        Directory.CreateDirectory(root);
        string json = JsonSerializer.Serialize(
            new PendingModeSwitch(
                Path.GetFullPath(targetDataRoot),
                targetPortable,
                DateTimeOffset.UtcNow),
            PendingJsonOptions);
        string pendingPath = PendingPath(root);
        string staging = pendingPath + ".tmp-" + Guid.NewGuid().ToString("N");
        try
        {
            File.WriteAllText(staging, json);
            File.Move(staging, pendingPath, overwrite: true);
        }
        finally
        {
            TryDeleteFile(staging);
        }
    }

    /// <summary>
    /// Applies the pending switch recorded in the outgoing root when it
    /// targets <paramref name="currentDataRoot"/>; stale markers (the user
    /// switched again before restarting) are discarded so the active mode
    /// always wins.
    /// </summary>
    public static MigrationOutcome ApplyIfNeeded(
        string currentDataRoot,
        string otherDataRoot)
    {
        string current = Path.GetFullPath(currentDataRoot);
        string other = Path.GetFullPath(otherDataRoot);
        if (string.Equals(current, other, StringComparison.OrdinalIgnoreCase))
        {
            return MigrationOutcome.None;
        }

        string otherPending = PendingPath(other);
        if (File.Exists(otherPending))
        {
            PendingModeSwitch? pending = TryReadPending(otherPending);
            if (pending is null)
            {
                throw new DataRootMigrationException(
                    "data.mode_switch_pending_invalid",
                    $"模式切换记录无法读取：{otherPending}");
            }
            if (string.Equals(
                    Path.GetFullPath(pending.TargetRoot),
                    current,
                    StringComparison.OrdinalIgnoreCase))
            {
                VerifyRegistryRelocation(other);
                MigrateEntries(other, current);
                RelocateRegistryPaths(current, other);
                CompleteMigration(other);
                return MigrationOutcome.Migrated;
            }
            // The user flipped the switch again before restarting; the mode
            // resolved this launch is the final intent.
            TryDeleteFile(otherPending);
        }

        // A marker inside the active root can only be stale.
        TryDeleteFile(PendingPath(current));
        return MigrationOutcome.None;
    }

    private static string PendingPath(string root) =>
        Path.Combine(root, LaunchPaths.ModeSwitchPendingFileName);

    /// <summary>
    /// Rewrites absolute workspace paths inside the migrated registry so the
    /// registered roots follow their data across the switch. Only paths under
    /// the outgoing root move; externally located workspaces keep their
    /// registration. Every relocated root is verified against its
    /// <c>.vibetable/workspace.json</c> UUID so a half-migrated tree fails
    /// loudly instead of silently rebinding a directory to the wrong id.
    /// </summary>
    private static void RelocateRegistryPaths(string dataRoot, string sourceRoot)
    {
        string registryPath = Path.Combine(
            dataRoot,
            "shell",
            "workspace-registry-v2.json");
        if (!File.Exists(registryPath))
        {
            return;
        }
        JsonObject registry;
        try
        {
            registry = JsonNode.Parse(File.ReadAllText(registryPath))
                as JsonObject
                ?? throw new JsonException();
        }
        catch (Exception exception) when (
            exception is IOException
                or JsonException
                or UnauthorizedAccessException)
        {
            throw new DataRootMigrationException(
                "data.migration_registry_invalid",
                $"迁移后的工作区注册表无法读取：{registryPath}");
        }
        if (registry["workspaces"] is not JsonArray workspaces)
        {
            throw new DataRootMigrationException(
                "data.migration_registry_invalid",
                $"迁移后的工作区注册表格式无效：{registryPath}");
        }
        string sourcePrefix = sourceRoot.TrimEnd(
            Path.DirectorySeparatorChar,
            Path.AltDirectorySeparatorChar)
            + Path.DirectorySeparatorChar;
        foreach (JsonNode? node in workspaces)
        {
            if (node is not JsonObject entry
                || entry["workspaceId"]?.GetValue<string>() is not { } workspaceId)
            {
                throw new DataRootMigrationException(
                    "data.migration_registry_invalid",
                    $"迁移后的工作区注册表条目无效：{registryPath}");
            }
            foreach (string key in new[] { SelectedRootKey, ActivityRootKey })
            {
                if (entry[key] is JsonValue value
                    && value.TryGetValue<string>(out string? path)
                    && !string.IsNullOrEmpty(path)
                    && path.StartsWith(sourcePrefix, StringComparison.OrdinalIgnoreCase))
                {
                    string relocated = Path.Combine(
                        dataRoot,
                        path[sourcePrefix.Length..]);
                    entry[key] = relocated;
                    if (key == SelectedRootKey)
                    {
                        VerifyRelocatedWorkspaceIdentity(
                            relocated,
                            workspaceId,
                            registryPath);
                    }
                }
            }
        }
        string staging = registryPath + ".tmp-" + Guid.NewGuid().ToString("N");
        try
        {
            File.WriteAllText(
                staging,
                registry.ToJsonString(JsonSerializerOptions.Web));
            File.Move(staging, registryPath, overwrite: true);
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            TryDeleteFile(staging);
            throw new DataRootMigrationException(
                "data.migration_registry_write_failed",
                $"迁移后的工作区注册表无法更新：{registryPath}");
        }
    }

    private const string SelectedRootKey = "selectedRoot";
    private const string ActivityRootKey = "activityRoot";

    /// <summary>
    /// Pre-flight identity check while the data still sits in the outgoing
    /// root: every registered workspace that will move must have a readable
    /// manifest whose UUID matches its registration. Failing here leaves both
    /// roots untouched instead of stranding moved data behind a bad registry.
    /// </summary>
    private static void VerifyRegistryRelocation(string sourceRoot)
    {
        string registryPath = Path.Combine(
            sourceRoot,
            "shell",
            "workspace-registry-v2.json");
        if (!File.Exists(registryPath))
        {
            return;
        }
        JsonArray workspaces;
        try
        {
            workspaces = (JsonNode.Parse(File.ReadAllText(registryPath))
                as JsonObject)?["workspaces"] as JsonArray
                ?? throw new JsonException();
        }
        catch (Exception exception) when (
            exception is IOException
                or JsonException
                or UnauthorizedAccessException)
        {
            throw new DataRootMigrationException(
                "data.migration_registry_invalid",
                $"待迁移的工作区注册表无法读取：{registryPath}");
        }
        string sourcePrefix = sourceRoot.TrimEnd(
            Path.DirectorySeparatorChar,
            Path.AltDirectorySeparatorChar)
            + Path.DirectorySeparatorChar;
        foreach (JsonNode? node in workspaces)
        {
            if (node is not JsonObject entry
                || entry[SelectedRootKey]?.GetValue<string>() is not { } selectedRoot
                || !selectedRoot.StartsWith(
                    sourcePrefix,
                    StringComparison.OrdinalIgnoreCase))
            {
                continue;
            }
            if (entry["workspaceId"]?.GetValue<string>() is not { } workspaceId)
            {
                throw new DataRootMigrationException(
                    "data.migration_registry_invalid",
                    $"待迁移的工作区注册表条目无效：{registryPath}");
            }
            VerifyRelocatedWorkspaceIdentity(
                selectedRoot,
                workspaceId,
                registryPath);
        }
    }

    private static void VerifyRelocatedWorkspaceIdentity(
        string workspaceRoot,
        string workspaceId,
        string registryPath)
    {
        string manifestPath = Path.Combine(
            workspaceRoot,
            ".vibetable",
            "workspace.json");
        try
        {
            var manifest = JsonSerializer.Deserialize<JsonElement>(
                File.ReadAllText(manifestPath));
            if (manifest.ValueKind == JsonValueKind.Object
                && manifest.TryGetProperty("workspaceId", out JsonElement id)
                && id.ValueKind == JsonValueKind.String
                && string.Equals(
                    id.GetString(),
                    workspaceId,
                    StringComparison.OrdinalIgnoreCase))
            {
                return;
            }
        }
        catch (Exception exception) when (
            exception is IOException or JsonException)
        {
            // A relocated root without a readable manifest fails below; the
            // migration moved the whole tree, so identity must be verifiable.
        }
        throw new DataRootMigrationException(
            "data.migration_workspace_identity_mismatch",
            $"迁移后的工作区身份与注册表不一致（{workspaceRoot}，期望 {workspaceId}）；"
                + $"注册表：{registryPath}");
    }

    private static PendingModeSwitch? TryReadPending(string path)
    {
        try
        {
            return JsonSerializer.Deserialize<PendingModeSwitch>(
                File.ReadAllText(path),
                PendingJsonOptions);
            // Deserialization failures mean the file is being written or is
            // corrupt; the next retry re-reads it after a clean shutdown.
        }
        catch (Exception exception) when (
            exception is IOException
                or JsonException
                or UnauthorizedAccessException)
        {
            return null;
        }
    }

    private static void MigrateEntries(string sourceRoot, string targetRoot)
    {
        CleanupIncompleteTransfers(targetRoot);
        if (!Directory.Exists(sourceRoot))
        {
            return;
        }
        Directory.CreateDirectory(targetRoot);
        foreach (string entry in Directory.EnumerateFileSystemEntries(sourceRoot))
        {
            string name = Path.GetFileName(entry);
            if (name == LaunchPaths.ModeSwitchPendingFileName)
            {
                continue;
            }
            string target = Path.Combine(targetRoot, name);
            if (File.Exists(target) || Directory.Exists(target))
            {
                // A previous cross-volume attempt may have committed the copy
                // but died before deleting the source; finish that cleanup so
                // the switch can complete on retry. Anything else is a real
                // conflict and stays loud through the drained-source check.
                if (IsCompletedTransfer(entry, target))
                {
                    DeleteSourceEntry(entry, target);
                }
                continue;
            }
            if (Directory.Exists(entry))
            {
                MoveDirectory(entry, target);
            }
            else
            {
                MoveFile(entry, target);
            }
        }
    }

    /// <summary>
    /// True when the target already holds a complete copy of the source entry
    /// (equal file sizes throughout), meaning only the source-side cleanup of
    /// an earlier cross-volume transfer remains.
    /// </summary>
    private static bool IsCompletedTransfer(string source, string target)
    {
        try
        {
            if (File.Exists(source))
            {
                return File.Exists(target)
                    && new FileInfo(target).Length == new FileInfo(source).Length;
            }
            if (Directory.Exists(source))
            {
                if (!Directory.Exists(target))
                {
                    return false;
                }
                ValidateTree(source, target);
                return true;
            }
            return false;
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            return false;
        }
    }

    private static void DeleteSourceEntry(string source, string target)
    {
        try
        {
            if (Directory.Exists(source))
            {
                Directory.Delete(source, recursive: true);
            }
            else
            {
                File.Delete(source);
            }
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            throw new DataRootMigrationException(
                "data.migration_entry_failed",
                $"数据迁移失败：无法清理已复制完成的源条目：{source} → {target}");
        }
    }

    private static void CompleteMigration(string sourceRoot)
    {
        if (!Directory.Exists(sourceRoot))
        {
            return;
        }
        bool drained = !Directory.EnumerateFileSystemEntries(sourceRoot)
            .Any(entry => Path.GetFileName(entry)
                != LaunchPaths.ModeSwitchPendingFileName);
        if (!drained)
        {
            throw new DataRootMigrationException(
                "data.migration_residual_entries",
                $"源数据目录仍有未迁移的内容，已保留现场：{sourceRoot}");
        }
        TryDeleteFile(PendingPath(sourceRoot));
        try
        {
            // Only the (now removed) pending marker is left, so a plain
            // delete cannot recurse into user data.
            Directory.Delete(sourceRoot, recursive: false);
        }
        catch (IOException)
        {
            // A locked empty directory is harmless; the next launch finds no
            // pending marker and continues normally.
        }
        catch (UnauthorizedAccessException)
        {
        }
    }

    private static void MoveFile(string source, string target)
    {
        try
        {
            File.Move(source, target);
            return;
        }
        catch (IOException)
        {
            // Cross-volume move (or a transient lock); copy across instead.
        }
        string staging = target + ".migrating-" + Guid.NewGuid().ToString("N");
        try
        {
            File.Copy(source, staging);
            ValidateSameLength(source, staging);
            File.Move(staging, target);
            // The copy path must also remove the source; a failure here keeps
            // the committed copy, and the next launch recognizes the finished
            // transfer through IsCompletedTransfer instead of treating it as
            // a conflict.
            File.Delete(source);
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            TryDeleteFile(staging);
            throw new DataRootMigrationException(
                "data.migration_entry_failed",
                $"数据迁移失败：{source} → {target}");
        }
    }

    private static void MoveDirectory(string source, string target)
    {
        try
        {
            Directory.Move(source, target);
            return;
        }
        catch (IOException)
        {
            // Cross-volume move (or a transient lock); copy across instead.
        }
        string staging = target + ".migrating-" + Guid.NewGuid().ToString("N");
        try
        {
            CopyDirectory(source, staging);
            ValidateTree(source, staging);
            Directory.Move(staging, target);
            Directory.Delete(source, recursive: true);
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            TryDeleteDirectory(staging);
            throw new DataRootMigrationException(
                "data.migration_entry_failed",
                $"数据迁移失败：{source} → {target}");
        }
    }

    private static void CopyDirectory(string source, string target)
    {
        Directory.CreateDirectory(target);
        foreach (string file in Directory.EnumerateFiles(source))
        {
            File.Copy(file, Path.Combine(target, Path.GetFileName(file)));
        }
        foreach (string directory in Directory.EnumerateDirectories(source))
        {
            CopyDirectory(
                directory,
                Path.Combine(target, Path.GetFileName(directory)));
        }
    }

    private static void ValidateSameLength(string source, string copy)
    {
        if (new FileInfo(copy).Length != new FileInfo(source).Length)
        {
            throw new IOException($"incomplete copy: {source}");
        }
    }

    private static void ValidateTree(string sourceRoot, string copyRoot)
    {
        foreach (string file in Directory.EnumerateFiles(
                     sourceRoot,
                     "*",
                     SearchOption.AllDirectories))
        {
            string relative = Path.GetRelativePath(sourceRoot, file);
            string copy = Path.Combine(copyRoot, relative);
            if (!File.Exists(copy))
            {
                throw new IOException($"incomplete copy: {relative}");
            }
            ValidateSameLength(file, copy);
        }
    }

    /// <summary>
    /// Removes leftovers of interrupted cross-volume transfers. Staging
    /// entries are only ever created as direct children of the target root,
    /// so the scan stays at that level and matches the exact
    /// <c>&lt;name&gt;.migrating-&lt;32-hex&gt;</c> shape — user files inside
    /// already-migrated workspace trees are never touched.
    /// </summary>
    private static void CleanupIncompleteTransfers(string root)
    {
        if (!Directory.Exists(root))
        {
            return;
        }
        foreach (string entry in Directory.EnumerateFileSystemEntries(root))
        {
            if (!StagingEntryName().IsMatch(Path.GetFileName(entry)))
            {
                continue;
            }
            if (Directory.Exists(entry))
            {
                TryDeleteDirectory(entry);
            }
            else if (File.Exists(entry))
            {
                TryDeleteFile(entry);
            }
        }
    }

    [GeneratedRegex(
        @"\.migrating-[0-9a-f]{32}$",
        RegexOptions.IgnoreCase | RegexOptions.CultureInvariant)]
    private static partial Regex StagingEntryName();

    private static void TryDeleteFile(string path)
    {
        try
        {
            if (File.Exists(path))
            {
                File.Delete(path);
            }
        }
        catch (IOException)
        {
        }
        catch (UnauthorizedAccessException)
        {
        }
    }

    private static void TryDeleteDirectory(string path)
    {
        try
        {
            if (Directory.Exists(path))
            {
                Directory.Delete(path, recursive: true);
            }
        }
        catch (IOException)
        {
        }
        catch (UnauthorizedAccessException)
        {
        }
    }
}
