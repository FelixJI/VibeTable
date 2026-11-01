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
        DateTimeOffset RequestedAtUtc,
        Guid MigrationId);

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
        string current = portable ? portableRoot : standardRoot;
        string other = portable ? standardRoot : portableRoot;
        // Nothing may touch the outgoing root while an older instance (for
        // example one parked in the tray) still runs on it: a live sidecar
        // holds workspace databases open, and copying files under concurrent
        // writes would both corrupt the copy and delete files the old
        // instance needs. Keep the pending marker and fail with guidance.
        if (File.Exists(PendingPath(other))
            && DataRootSessionLock.IsHeldByAnotherProcess(other))
        {
            throw new DataRootMigrationException(
                "data.migration_source_in_use",
                "旧数据目录仍被另一个 VibeTable 实例使用，本次未做任何改动。"
                    + "请先退出旧实例后重新启动完成切换。");
        }
        return ApplyIfNeeded(current, other);
    }

    private const string PortableLocationFileName =
        "data-root-location.json";

    /// <summary>
    /// Keeps a portable install usable after its folder moves (USB drive
    /// letter changes, relocation, or a copied tree): the registry inside the
    /// data root still holds absolute paths from the previous location, so on
    /// every portable launch the recorded location is compared against the
    /// current root and registered workspace paths under the old root are
    /// rewritten to the new one (with identity verification) before anything
    /// consumes them. Externally rooted workspaces never move.
    /// </summary>
    public static void RelocatePortableRegistry(string portableRoot)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(portableRoot);
        string root = Path.GetFullPath(portableRoot);
        string recordPath = Path.Combine(root, PortableLocationFileName);
        string? recorded = TryReadRecordedPortableRoot(recordPath);
        if (recorded is not null
            && !string.Equals(recorded, root, StringComparison.OrdinalIgnoreCase))
        {
            // Fail closed: a registry that cannot be verified against the
            // moved tree blocks the launch instead of rebinding paths.
            RelocateRegistryPaths(root, recorded);
        }
        TryRememberPortableRoot(recordPath, root);
    }

    private static string? TryReadRecordedPortableRoot(string recordPath)
    {
        if (!File.Exists(recordPath))
        {
            return null;
        }
        try
        {
            using JsonDocument document = JsonDocument.Parse(
                File.ReadAllText(recordPath));
            return document.RootElement.ValueKind == JsonValueKind.Object
                && document.RootElement.TryGetProperty(
                    "dataRoot",
                    out JsonElement value)
                && value.ValueKind == JsonValueKind.String
                && !string.IsNullOrWhiteSpace(value.GetString())
                ? value.GetString()
                : null;
        }
        catch (Exception exception) when (
            exception is IOException
                or JsonException
                or UnauthorizedAccessException)
        {
            // An unreadable record means "location unknown"; relocation is
            // skipped and the record is rewritten below.
            return null;
        }
    }

    private static void TryRememberPortableRoot(
        string recordPath,
        string portableRoot)
    {
        try
        {
            string staging = recordPath + ".tmp-" + Guid.NewGuid().ToString("N");
            File.WriteAllText(
                staging,
                JsonSerializer.Serialize(
                    new { dataRoot = portableRoot },
                    PendingJsonOptions));
            File.Move(staging, recordPath, overwrite: true);
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            // Remembering is best-effort; the writability probe already
            // guarantees the root is usable this session.
        }
    }

    private const string WebView2ProfileDirectoryName = "webview2-user-data";
    private const string LegacyWebView2ProfileDirectoryName = "webview2-udd";

    /// <summary>
    /// Upgrades from the previous release keep the renderer's stored settings
    /// (language, startup policy, density, recents) by carrying the legacy
    /// WebView2 profile directory over to its new name/location before the
    /// first cold profile is created. Runs after the session lock, so no
    /// other instance can hold profile files open. Best-effort: a locked or
    /// partially copied tree is retried on the next launch, and a fresh
    /// profile is simply created when nothing can be moved.
    /// </summary>
    public static void MigrateLegacyWebView2Profile(
        string dataRoot,
        string standardRoot)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(dataRoot);
        ArgumentException.ThrowIfNullOrWhiteSpace(standardRoot);
        string target = Path.Combine(
            Path.GetFullPath(dataRoot),
            WebView2ProfileDirectoryName);
        if (Directory.Exists(target))
        {
            return;
        }
        foreach (string legacy in new[]
        {
            Path.Combine(dataRoot, LegacyWebView2ProfileDirectoryName),
            Path.Combine(standardRoot, LegacyWebView2ProfileDirectoryName),
        })
        {
            if (string.Equals(
                    Path.GetFullPath(legacy),
                    Path.GetFullPath(target),
                    StringComparison.OrdinalIgnoreCase)
                || !Directory.Exists(legacy))
            {
                continue;
            }
            try
            {
                Directory.Move(legacy, target);
                return;
            }
            catch (IOException)
            {
                // Cross-volume move; fall through to a full copy.
            }
            catch (UnauthorizedAccessException)
            {
                return;
            }
            string staging = target + ".migrating-" + Guid.NewGuid().ToString("N");
            try
            {
                CopyDirectory(legacy, staging);
                Directory.Move(staging, target);
                Directory.Delete(legacy, recursive: true);
                return;
            }
            catch (Exception exception) when (
                exception is IOException or UnauthorizedAccessException)
            {
                TryDeleteDirectory(staging);
                // Leave the legacy profile in place; the next launch retries.
                return;
            }
        }
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
                DateTimeOffset.UtcNow,
                Guid.NewGuid()),
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
        try
        {
            return ApplyIfNeededCore(currentDataRoot, otherDataRoot);
        }
        catch (DataRootMigrationException)
        {
            throw;
        }
        catch (Exception exception) when (
            exception is IOException
                or UnauthorizedAccessException
                or System.Security.SecurityException)
        {
            // The migration runs before the writability probe and the log
            // listener, so raw filesystem failures (denied directory creates,
            // locked enumerations, denied moves) must still surface as the
            // structured migration failure with recovery guidance.
            throw new DataRootMigrationException(
                "data.migration_unexpected_io",
                $"数据迁移失败（{exception.GetType().Name}）："
                    + Path.GetFullPath(otherDataRoot));
        }
    }

    private static MigrationOutcome ApplyIfNeededCore(
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
                VerifyRegistryRelocation(other, current, pending.MigrationId);
                MigrateEntries(other, current, pending.MigrationId);
                RelocateRegistryPaths(current, other);
                CompleteMigration(other, current);
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
        bool changed = false;
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
                    && RelocatePath(dataRoot, path, sourceRoot) is { } relocated
                    && !string.Equals(relocated, path, StringComparison.OrdinalIgnoreCase))
                {
                    entry[key] = relocated;
                    changed = true;
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
        if (!changed)
        {
            return;
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

    /// <summary>Rebases a path under the outgoing root onto the new root.</summary>
    private static string RelocatePath(
        string dataRoot,
        string path,
        string sourceRoot)
    {
        string prefix = sourceRoot.TrimEnd(
            Path.DirectorySeparatorChar,
            Path.AltDirectorySeparatorChar)
            + Path.DirectorySeparatorChar;
        return path.StartsWith(prefix, StringComparison.OrdinalIgnoreCase)
            ? Path.Combine(dataRoot, path[prefix.Length..])
            : path;
    }

    /// <summary>
    /// Pre-flight identity check while the data still sits in the outgoing
    /// root: every registered workspace that will move must have a readable
    /// manifest whose UUID matches its registration. A retry whose source
    /// tree was already partially deleted after the copy committed verifies
    /// against the ledger-backed TARGET copy instead. Failing here leaves
    /// both roots untouched instead of stranding moved data behind a bad
    /// registry.
    /// </summary>
    private static void VerifyRegistryRelocation(
        string sourceRoot,
        string targetRoot,
        Guid migrationId)
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
            if (HasReadableIdentityManifest(selectedRoot, workspaceId))
            {
                continue;
            }
            // The source tree may already be partially deleted by an earlier
            // attempt whose cross-volume copy committed; only a ledger-backed
            // committed shell entry may fall back to verifying the target.
            if (TryReadLedger(targetRoot, migrationId, sourceRoot)?
                    .Committed.Contains("shell") == true
                && HasReadableIdentityManifest(
                    RelocatePath(targetRoot, selectedRoot, sourceRoot),
                    workspaceId))
            {
                continue;
            }
            VerifyRelocatedWorkspaceIdentity(
                selectedRoot,
                workspaceId,
                registryPath);
        }
    }

    private static bool HasReadableIdentityManifest(
        string workspaceRoot,
        string workspaceId)
    {
        string manifestPath = Path.Combine(
            workspaceRoot,
            ".vibetable",
            "workspace.json");
        try
        {
            var manifest = JsonSerializer.Deserialize<JsonElement>(
                File.ReadAllText(manifestPath));
            return manifest.ValueKind == JsonValueKind.Object
                && manifest.TryGetProperty("workspaceId", out JsonElement id)
                && id.ValueKind == JsonValueKind.String
                && string.Equals(
                    id.GetString(),
                    workspaceId,
                    StringComparison.OrdinalIgnoreCase);
        }
        catch (Exception exception) when (
            exception is IOException or JsonException)
        {
            return false;
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

    /// <summary>
    /// Durable ownership record for entries this migration committed to the
    /// target root. Only ledger-backed entries may have their source deleted
    /// on retry — an equally sized pre-existing file is never proof that the
    /// target copy came from this migration.
    /// </summary>
    private sealed record MigrationLedger(
        Guid MigrationId,
        string SourceRoot,
        List<string> Committed);

    private const string LedgerFileName = "data-migration-ledger.json";

    private static string LedgerPath(string targetRoot) =>
        Path.Combine(targetRoot, LedgerFileName);

    private static MigrationLedger? TryReadLedger(
        string targetRoot,
        Guid migrationId,
        string sourceRoot)
    {
        string path = LedgerPath(targetRoot);
        if (!File.Exists(path))
        {
            return null;
        }
        try
        {
            MigrationLedger? ledger = JsonSerializer.Deserialize<MigrationLedger>(
                File.ReadAllText(path),
                PendingJsonOptions);
            if (ledger is null
                || ledger.MigrationId != migrationId
                || !string.Equals(
                    ledger.SourceRoot,
                    sourceRoot,
                    StringComparison.OrdinalIgnoreCase))
            {
                return null;
            }
            return ledger;
        }
        catch (Exception exception) when (
            exception is IOException
                or JsonException
                or UnauthorizedAccessException)
        {
            // An unreadable ledger proves nothing; retries fall back to the
            // loud conflict path instead of deleting sources.
            return null;
        }
    }

    /// <summary>
    /// Records a committed entry. Best-effort by design: a lost record can
    /// only make a later retry report a conflict (sources preserved), never
    /// delete data it does not own.
    /// </summary>
    private static void RecordCommit(
        string targetRoot,
        Guid migrationId,
        string sourceRoot,
        string name)
    {
        try
        {
            string path = LedgerPath(targetRoot);
            MigrationLedger ledger =
                TryReadLedger(targetRoot, migrationId, sourceRoot)
                ?? new MigrationLedger(migrationId, sourceRoot, []);
            if (!ledger.Committed.Contains(name))
            {
                ledger.Committed.Add(name);
            }
            string staging = path + ".tmp-" + Guid.NewGuid().ToString("N");
            File.WriteAllText(
                staging,
                JsonSerializer.Serialize(ledger, PendingJsonOptions));
            File.Move(staging, path, overwrite: true);
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
        }
    }

    private static void MigrateEntries(
        string sourceRoot,
        string targetRoot,
        Guid migrationId)
    {
        CleanupIncompleteTransfers(targetRoot);
        if (!Directory.Exists(sourceRoot))
        {
            return;
        }
        Directory.CreateDirectory(targetRoot);
        MigrationLedger? ledger = TryReadLedger(
            targetRoot,
            migrationId,
            sourceRoot);
        foreach (string entry in Directory.EnumerateFileSystemEntries(sourceRoot))
        {
            string name = Path.GetFileName(entry);
            if (name == LaunchPaths.ModeSwitchPendingFileName)
            {
                continue;
            }
            if (name == LaunchPaths.SessionLockFileName)
            {
                // The previous owner has exited (its lock was verified free),
                // so this zero-byte leftover is plain bookkeeping.
                TryDeleteFile(entry);
                continue;
            }
            string target = Path.Combine(targetRoot, name);
            if (File.Exists(target) || Directory.Exists(target))
            {
                // A previous attempt of THIS migration may have committed the
                // copy but died before deleting the source; the ledger entry
                // plus a content-equal check proves ownership, so finish that
                // cleanup. Anything else is a pre-existing target and stays a
                // loud conflict through the drained-source check.
                if (ledger?.Committed.Contains(name) == true
                    && IsCompletedTransfer(entry, target))
                {
                    DeleteSourceEntry(entry, target);
                }
                continue;
            }
            if (Directory.Exists(entry))
            {
                MoveDirectory(
                    entry,
                    target,
                    () => RecordCommit(targetRoot, migrationId, sourceRoot, name));
            }
            else
            {
                MoveFile(
                    entry,
                    target,
                    () => RecordCommit(targetRoot, migrationId, sourceRoot, name));
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

    private static void CompleteMigration(string sourceRoot, string targetRoot)
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
        // The ledger proved ownership until the very end; a finished switch
        // no longer needs it.
        TryDeleteFile(LedgerPath(targetRoot));
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

    private static void MoveFile(
        string source,
        string target,
        Action onCommitted)
    {
        try
        {
            File.Move(source, target);
            onCommitted();
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
            // Record ownership before removing the source: if the delete
            // fails, the ledger-backed retry recognizes the committed copy
            // and finishes the cleanup instead of reporting a conflict.
            onCommitted();
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

    private static void MoveDirectory(
        string source,
        string target,
        Action onCommitted)
    {
        try
        {
            Directory.Move(source, target);
            onCommitted();
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
            // Record ownership before removing the source: a partial source
            // delete followed by a crash is recovered through the ledger on
            // the next launch (see VerifyRegistryRelocation).
            onCommitted();
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
