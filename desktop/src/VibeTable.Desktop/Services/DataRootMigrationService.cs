using System.IO;
using System.Text.Json;
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
internal static class DataRootMigrationService
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
                MigrateEntries(other, current);
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
                // Finished on an earlier attempt (its source copy would be
                // gone) or a real conflict; the drained-source check below
                // keeps real conflicts loud instead of dropping data.
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

    private static void CleanupIncompleteTransfers(string root)
    {
        if (!Directory.Exists(root))
        {
            return;
        }
        string[] stagingDirectories = [.. Directory.EnumerateDirectories(
            root,
            "*.migrating-*",
            SearchOption.AllDirectories)];
        foreach (string directory in stagingDirectories)
        {
            TryDeleteDirectory(directory);
        }
        foreach (string file in Directory.EnumerateFiles(
                     root,
                     "*.migrating-*",
                     SearchOption.AllDirectories))
        {
            TryDeleteFile(file);
        }
    }

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
