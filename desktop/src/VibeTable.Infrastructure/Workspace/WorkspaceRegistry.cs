using System.Text;
using System.Text.Json;
using VibeTable.Contracts;

namespace VibeTable.Infrastructure.Workspace;

/// <summary>
/// Device-local projection of known workspaces. Workspace contents remain
/// authoritative in each root's <c>.vibetable/workspace.json</c>.
/// </summary>
public sealed class WorkspaceRegistry
{
    private const int CurrentFormatVersion = 2;
    private readonly object _gate = new();
    private readonly string _storePath;

    public WorkspaceRegistry(string? localAppData = null)
    {
        var root = localAppData
            ?? Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData);
        _storePath = Path.Combine(
            root,
            "VibeTable",
            "shell",
            "workspace-registry-v2.json");
    }

    public IReadOnlyList<WorkspaceRegistryEntryV2> List()
    {
        lock (_gate)
        {
            if (!File.Exists(_storePath))
                return [];
            RegistryFile file;
            try
            {
                var json = File.ReadAllText(_storePath, Encoding.UTF8);
                file = JsonSerializer.Deserialize<RegistryFile>(
                    json,
                    WorkspaceV2Json.StrictOptions)
                    ?? throw new WorkspaceRegistryException(
                        "workspace.registry_corrupt",
                        "Workspace registry is empty.");
            }
            catch (WorkspaceRegistryException)
            {
                throw;
            }
            catch (Exception exception) when (
                exception is JsonException or IOException or UnauthorizedAccessException)
            {
                throw new WorkspaceRegistryException(
                    "workspace.registry_corrupt",
                    "Workspace registry could not be read.",
                    exception);
            }
            if (file.FormatVersion != CurrentFormatVersion)
            {
                throw new WorkspaceRegistryException(
                    "workspace.registry_version_unsupported",
                    "Workspace registry version is not supported.");
            }
            var duplicate = file.Workspaces
                .GroupBy(entry => entry.WorkspaceId)
                .FirstOrDefault(group => group.Count() > 1);
            if (duplicate is not null)
            {
                throw new WorkspaceRegistryException(
                    "workspace.registry_corrupt",
                    $"Workspace registry contains duplicate UUID {duplicate.Key:D}.");
            }
            foreach (var entry in file.Workspaces)
                entry.Validate();
            return file.Workspaces
                .OrderByDescending(entry => entry.LastOpenedAt)
                .ThenBy(entry => entry.DisplayName, StringComparer.CurrentCulture)
                .ToArray();
        }
    }

    public WorkspaceRegistryEntryV2 Register(WorkspaceRegistryEntryV2 selection)
    {
        ArgumentNullException.ThrowIfNull(selection);
        selection.Validate();
        var manifest = WorkspaceLayout.ReadManifest(selection.SelectedRoot);
        if (manifest.WorkspaceId != selection.WorkspaceId)
        {
            throw new WorkspaceRegistryException(
                "workspace.identity_mismatch",
                "Selected path does not contain the workspace UUID being registered.");
        }
        lock (_gate)
        {
            var entries = List().ToList();
            var existingPath = entries.FirstOrDefault(entry =>
                entry.WorkspaceId != selection.WorkspaceId
                && string.Equals(
                    Normalize(entry.SelectedRoot),
                    Normalize(selection.SelectedRoot),
                    StringComparison.OrdinalIgnoreCase));
            if (existingPath is not null)
            {
                throw new WorkspaceRegistryException(
                    "workspace.path_registered",
                    "The selected path belongs to a different workspace UUID.");
            }
            entries.RemoveAll(entry => entry.WorkspaceId == selection.WorkspaceId);
            entries.Add(selection);
            Write(entries);
            return selection;
        }
    }

    public WorkspaceRegistryEntryV2 UpdateHealth(
        Guid workspaceId,
        WorkspaceHealthObservation observation)
    {
        ArgumentNullException.ThrowIfNull(observation);
        lock (_gate)
        {
            var entries = List().ToList();
            var index = entries.FindIndex(entry => entry.WorkspaceId == workspaceId);
            if (index < 0)
            {
                throw new WorkspaceRegistryException(
                    "workspace.not_registered",
                    "Workspace is not registered on this device.");
            }
            var current = entries[index];
            var updated = current with
            {
                LastKnownHealth = observation.Health,
                LastSnapshotAt = observation.LastSnapshotAt ?? current.LastSnapshotAt,
                LastSyncAt = observation.LastSyncAt ?? current.LastSyncAt,
                PendingSync = observation.PendingSync,
            };
            entries[index] = updated;
            Write(entries);
            return updated;
        }
    }

    public WorkspaceRegistryEntryV2 Relink(
        Guid workspaceId,
        string selectedRoot,
        string? activityRoot,
        WorkspaceStorageObservation storage)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(selectedRoot);
        ArgumentNullException.ThrowIfNull(storage);
        string selected = Normalize(selectedRoot);
        string? activity = NormalizeNullable(activityRoot);
        WorkspaceManifestV2 manifest = WorkspaceLayout.ReadManifest(selected);
        if (manifest.WorkspaceId != workspaceId)
        {
            throw new WorkspaceRegistryException(
                "workspace.identity_mismatch",
                "Selected path contains a different workspace UUID.");
        }
        lock (_gate)
        {
            var entries = List().ToList();
            int index = entries.FindIndex(
                entry => entry.WorkspaceId == workspaceId);
            if (index < 0)
            {
                throw new WorkspaceRegistryException(
                    "workspace.not_registered",
                    "Workspace is not registered on this device.");
            }
            if (entries.Any(entry =>
                    entry.WorkspaceId != workspaceId
                    && string.Equals(
                        Normalize(entry.SelectedRoot),
                        selected,
                        StringComparison.OrdinalIgnoreCase)))
            {
                throw new WorkspaceRegistryException(
                    "workspace.path_registered",
                    "The selected path belongs to a different workspace UUID.");
            }

            WorkspaceRegistryEntryV2 current = entries[index];
            WorkspaceRegistryEntryV2 updated = current with
            {
                SelectedRoot = selected,
                ActivityRoot = activity,
                StorageKind = storage.StorageKind,
                CoordinationStrength = storage.CoordinationStrength,
                LastKnownHealth = WorkspaceHealth.Healthy,
            };
            entries[index] = updated;
            Write(entries);
            return updated;
        }
    }

    public void Unregister(Guid workspaceId)
    {
        lock (_gate)
        {
            var entries = List().Where(entry => entry.WorkspaceId != workspaceId).ToList();
            Write(entries);
        }
    }

    public WorkspaceDeletePlan PlanPermanentDelete(Guid workspaceId)
    {
        lock (_gate)
        {
            var entry = List().SingleOrDefault(item => item.WorkspaceId == workspaceId)
                ?? throw new WorkspaceRegistryException(
                    "workspace.not_registered",
                    "Workspace is not registered on this device.");
            var manifest = WorkspaceLayout.ReadManifest(entry.SelectedRoot);
            if (manifest.WorkspaceId != workspaceId)
            {
                throw new WorkspaceRegistryException(
                    "workspace.identity_mismatch",
                    "Selected path no longer contains the registered workspace UUID.");
            }
            return new WorkspaceDeletePlan(
                Guid.NewGuid(),
                workspaceId,
                entry.DisplayName,
                entry.SelectedRoot,
                entry.ActivityRoot,
                manifest.FormatVersion,
                DateTimeOffset.UtcNow.AddMinutes(10));
        }
    }

    public void ApplyPermanentDelete(
        WorkspaceDeletePlan plan,
        string confirmation,
        DateTimeOffset? now = null)
    {
        ArgumentNullException.ThrowIfNull(plan);
        if (!string.Equals(plan.DisplayName, confirmation, StringComparison.Ordinal))
        {
            throw new WorkspaceRegistryException(
                "workspace.delete_confirmation_invalid",
                "Typed workspace name does not match.");
        }
        if ((now ?? DateTimeOffset.UtcNow) > plan.ExpiresAt)
        {
            throw new WorkspaceRegistryException(
                "workspace.delete_plan_expired",
                "Workspace delete plan has expired.");
        }
        lock (_gate)
        {
            var current = List().SingleOrDefault(
                entry => entry.WorkspaceId == plan.WorkspaceId)
                ?? throw new WorkspaceRegistryException(
                    "workspace.delete_plan_stale",
                    "Workspace registration changed after the plan was created.");
            if (!string.Equals(
                    Normalize(current.SelectedRoot),
                    Normalize(plan.SelectedRoot),
                    StringComparison.OrdinalIgnoreCase) ||
                !string.Equals(
                    NormalizeNullable(current.ActivityRoot),
                    NormalizeNullable(plan.ActivityRoot),
                    StringComparison.OrdinalIgnoreCase))
            {
                throw new WorkspaceRegistryException(
                    "workspace.delete_plan_stale",
                    "Workspace location changed after the plan was created.");
            }
            var manifest = WorkspaceLayout.ReadManifest(plan.SelectedRoot);
            if (manifest.WorkspaceId != plan.WorkspaceId ||
                manifest.FormatVersion != plan.ManifestFormatVersion)
            {
                throw new WorkspaceRegistryException(
                    "workspace.delete_plan_stale",
                    "Workspace identity changed after the plan was created.");
            }
            WorkspaceLayout.DeleteWorkspaceRoot(plan.SelectedRoot, plan.WorkspaceId);
            if (plan.ActivityRoot is not null &&
                !string.Equals(
                    Normalize(plan.ActivityRoot),
                    Normalize(plan.SelectedRoot),
                    StringComparison.OrdinalIgnoreCase))
            {
                WorkspaceLayout.DeleteWorkspaceRoot(plan.ActivityRoot, plan.WorkspaceId);
            }
            Write(List().Where(entry => entry.WorkspaceId != plan.WorkspaceId).ToList());
        }
    }

    private void Write(IReadOnlyList<WorkspaceRegistryEntryV2> entries)
    {
        var file = new RegistryFile(CurrentFormatVersion, entries.ToList());
        DurableJsonFile.Write(_storePath, file, WorkspaceV2Json.StrictOptions);
    }

    private static string Normalize(string path) =>
        Path.TrimEndingDirectorySeparator(Path.GetFullPath(path));

    private static string? NormalizeNullable(string? path) =>
        path is null ? null : Normalize(path);

    private sealed record RegistryFile(
        int FormatVersion,
        List<WorkspaceRegistryEntryV2> Workspaces);
}

public sealed record WorkspaceHealthObservation(
    WorkspaceHealth Health,
    bool PendingSync,
    DateTimeOffset? LastSnapshotAt = null,
    DateTimeOffset? LastSyncAt = null);

public sealed record WorkspaceDeletePlan(
    Guid PlanId,
    Guid WorkspaceId,
    string DisplayName,
    string SelectedRoot,
    string? ActivityRoot,
    ulong ManifestFormatVersion,
    DateTimeOffset ExpiresAt);

public sealed class WorkspaceRegistryException(
    string code,
    string message,
    Exception? innerException = null) : Exception(message, innerException)
{
    public string Code { get; } = code;
}

internal static class DurableJsonFile
{
    public static void Write<T>(
        string path,
        T value,
        JsonSerializerOptions options)
    {
        var directory = Path.GetDirectoryName(path)
            ?? throw new InvalidOperationException("JSON path has no parent directory.");
        Directory.CreateDirectory(directory);
        var temporary = Path.Combine(
            directory,
            $".{Path.GetFileName(path)}.{Guid.NewGuid():N}.tmp");
        try
        {
            var raw = JsonSerializer.SerializeToUtf8Bytes(value, options);
            using (var stream = new FileStream(
                       temporary,
                       FileMode.CreateNew,
                       FileAccess.Write,
                       FileShare.None,
                       bufferSize: 4096,
                       FileOptions.WriteThrough))
            {
                stream.Write(raw);
                stream.Flush(flushToDisk: true);
            }
            if (File.Exists(path))
                File.Replace(temporary, path, destinationBackupFileName: null);
            else
                File.Move(temporary, path);
        }
        finally
        {
            if (File.Exists(temporary))
                File.Delete(temporary);
        }
    }
}
