using System.Text;
using System.Text.Json;
using VibeTable.Contracts;

namespace VibeTable.Infrastructure.Workspace;

public static class WorkspaceLayout
{
    public const string MetadataDirectoryName = ".vibetable";
    public const string ManifestFileName = "workspace.json";

    private static readonly string[] DirectMetadataDirectories =
    [
        "data",
        "topology",
        "objects",
        "audit",
        "snapshots",
        "coordination",
        "quarantine",
        "temp",
    ];

    private static readonly string[] ReplicaMetadataDirectories =
    [
        "objects",
        "snapshots",
        "audit",
        "coordination",
    ];

    public static WorkspaceLayoutResult Create(
        string selectedRoot,
        string displayName,
        WorkspaceStorageMode storageMode,
        WorkspaceEncryptionMode encryptionMode,
        string? activityRoot = null,
        Guid? workspaceId = null,
        DateTimeOffset? createdAt = null)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(selectedRoot);
        ArgumentException.ThrowIfNullOrWhiteSpace(displayName);
        var selected = Path.GetFullPath(selectedRoot);
        EnsureCreateTarget(selected);
        if (storageMode == WorkspaceStorageMode.Mirrored &&
            string.IsNullOrWhiteSpace(activityRoot))
            throw new WorkspaceRegistryException(
                "workspace.activity_root_required",
                "Mirrored workspaces require a local activity root.");

        var activity = storageMode == WorkspaceStorageMode.Direct
            ? selected
            : Path.GetFullPath(activityRoot!);
        if (storageMode == WorkspaceStorageMode.Mirrored)
            EnsureCreateTarget(activity);

        var identity = workspaceId ?? Guid.NewGuid();
        var manifest = new WorkspaceManifestV2
        {
            ContractVersion = WorkspaceV2Json.ContractVersion,
            FormatVersion = WorkspaceV2Json.WorkspaceFormatVersion,
            WorkspaceId = identity,
            DisplayName = displayName.Trim(),
            CreatedAt = createdAt ?? DateTimeOffset.UtcNow,
            StorageMode = storageMode,
            EncryptionMode = encryptionMode,
            RepositoryFormat = "kopia-v3",
            TopologySchemaVersion = 1,
            BusinessSchemaVersion = 1,
            ImportedFromWorkspaceId = null,
            SourceSnapshotId = null,
        };
        manifest.Validate();

        try
        {
            CreateRoot(selected, manifest, storageMode == WorkspaceStorageMode.Direct
                ? DirectMetadataDirectories
                : ReplicaMetadataDirectories);
            if (storageMode == WorkspaceStorageMode.Mirrored)
                CreateRoot(activity, manifest, DirectMetadataDirectories);
        }
        catch
        {
            TryDeleteFreshRoot(selected, identity);
            if (storageMode == WorkspaceStorageMode.Mirrored)
                TryDeleteFreshRoot(activity, identity);
            throw;
        }
        return new WorkspaceLayoutResult(manifest, selected, activity);
    }

    public static WorkspaceManifestV2 ReadManifest(string root)
    {
        var normalized = Path.GetFullPath(root);
        var metadata = Path.Combine(normalized, MetadataDirectoryName);
        var manifestPath = Path.Combine(metadata, ManifestFileName);
        if (!File.Exists(manifestPath))
        {
            if (Directory.Exists(Path.Combine(normalized, ".backup")))
            {
                throw new WorkspaceRegistryException(
                    "workspace.legacy_layout_unsupported",
                    "Legacy .backup workspaces must be rebuilt.");
            }
            throw new WorkspaceRegistryException(
                "workspace.manifest_missing",
                "Workspace manifest was not found.");
        }
        try
        {
            var json = File.ReadAllText(manifestPath, Encoding.UTF8);
            return WorkspaceV2Json.DeserializeStrict<WorkspaceManifestV2>(json);
        }
        catch (WorkspaceRegistryException)
        {
            throw;
        }
        catch (JsonException exception) when (
            exception.Message.Contains("workspace.format_unsupported", StringComparison.Ordinal))
        {
            throw new WorkspaceRegistryException(
                "workspace.format_unsupported",
                "This workspace belongs to an incompatible development format.",
                exception);
        }
        catch (Exception exception) when (
            exception is JsonException or IOException or UnauthorizedAccessException)
        {
            throw new WorkspaceRegistryException(
                "workspace.manifest_corrupt",
                "Workspace manifest could not be read.",
                exception);
        }
    }

    public static WorkspaceManifestV2 SetImportProvenance(
        string root,
        Guid sourceWorkspaceId,
        Guid sourceSnapshotId)
    {
        if (sourceWorkspaceId == Guid.Empty ||
            sourceSnapshotId == Guid.Empty)
            throw new WorkspaceRegistryException(
                "workspace.import_provenance_invalid",
                "Import provenance requires source workspace and snapshot IDs.");

        string normalized = Path.GetFullPath(root);
        WorkspaceManifestV2 current = ReadManifest(normalized);
        if (current.WorkspaceId == sourceWorkspaceId)
            throw new WorkspaceRegistryException(
                "workspace.import_identity_conflict",
                "An imported workspace must have a new UUID.");
        if (current.ImportedFromWorkspaceId is not null ||
            current.SourceSnapshotId is not null)
        {
            if (current.ImportedFromWorkspaceId == sourceWorkspaceId &&
                current.SourceSnapshotId == sourceSnapshotId)
                return current;
            throw new WorkspaceRegistryException(
                "workspace.import_provenance_conflict",
                "Workspace import provenance is already bound.");
        }

        WorkspaceManifestV2 updated = current with
        {
            ImportedFromWorkspaceId = sourceWorkspaceId,
            SourceSnapshotId = sourceSnapshotId,
        };
        updated.Validate();
        DurableJsonFile.Write(
            Path.Combine(
                normalized,
                MetadataDirectoryName,
                ManifestFileName),
            updated,
            WorkspaceV2Json.StrictOptions);
        return updated;
    }

    internal static WorkspaceManifestV2 RewriteStorageMode(
        string root,
        Guid expectedWorkspaceId,
        WorkspaceStorageMode storageMode)
    {
        string normalized = Path.GetFullPath(root);
        WorkspaceManifestV2 current = ReadManifest(normalized);
        if (current.WorkspaceId != expectedWorkspaceId)
            throw new WorkspaceRegistryException(
                "workspace.identity_mismatch",
                "Workspace storage target contains a different UUID.");
        WorkspaceManifestV2 updated = current with { StorageMode = storageMode };
        updated.Validate();
        DurableJsonFile.Write(
            Path.Combine(
                normalized,
                MetadataDirectoryName,
                ManifestFileName),
            updated,
            WorkspaceV2Json.StrictOptions);
        return updated;
    }

    internal static WorkspaceLayoutResult CreateReplicaRoot(
        string selectedRoot,
        string activityRoot,
        Guid expectedWorkspaceId)
    {
        string selected = Path.GetFullPath(selectedRoot);
        string activity = Path.GetFullPath(activityRoot);
        EnsureCreateTarget(selected);
        WorkspaceManifestV2 current = ReadManifest(activity);
        if (current.WorkspaceId != expectedWorkspaceId)
            throw new WorkspaceRegistryException(
                "workspace.identity_mismatch",
                "Workspace activity root contains a different UUID.");
        WorkspaceManifestV2 mirrored = current with
        {
            StorageMode = WorkspaceStorageMode.Mirrored,
        };
        mirrored.Validate();
        try
        {
            CreateRoot(selected, mirrored, ReplicaMetadataDirectories);
        }
        catch
        {
            TryDeleteFreshRoot(selected, expectedWorkspaceId);
            throw;
        }
        return new WorkspaceLayoutResult(mirrored, selected, activity);
    }

    /// <summary>
    /// Creates the device-local direct-layout activity root for an existing
    /// mirrored workspace replica. The selected replica is read-only during
    /// this operation and its UUID is copied into the fresh activity root.
    /// </summary>
    public static WorkspaceLayoutResult CreateActivityRoot(
        string selectedRoot,
        string activityRoot)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(selectedRoot);
        ArgumentException.ThrowIfNullOrWhiteSpace(activityRoot);
        string selected = Path.GetFullPath(selectedRoot);
        string activity = Path.GetFullPath(activityRoot);
        WorkspaceManifestV2 manifest = ReadManifest(selected);
        if (manifest.StorageMode != WorkspaceStorageMode.Mirrored)
            throw new WorkspaceRegistryException(
                "workspace.activity_root_not_required",
                "Direct workspaces do not use a separate activity root.");
        EnsureCreateTarget(activity);
        try
        {
            CreateRoot(activity, manifest, DirectMetadataDirectories);
        }
        catch
        {
            TryDeleteFreshRoot(activity, manifest.WorkspaceId);
            throw;
        }
        return new WorkspaceLayoutResult(manifest, selected, activity);
    }

    public static WorkspacePaths Paths(string root)
    {
        var normalized = Path.GetFullPath(root);
        var metadata = Path.Combine(normalized, MetadataDirectoryName);
        return new WorkspacePaths(
            normalized,
            Path.Combine(normalized, "files"),
            metadata,
            Path.Combine(metadata, "data"),
            Path.Combine(metadata, "topology"),
            Path.Combine(metadata, "objects"),
            Path.Combine(metadata, "audit"),
            Path.Combine(metadata, "snapshots"),
            Path.Combine(metadata, "coordination"),
            Path.Combine(metadata, "quarantine"),
            Path.Combine(metadata, "temp"));
    }

    internal static void DeleteWorkspaceRoot(string root, Guid expectedWorkspaceId)
    {
        var normalized = Path.GetFullPath(root);
        if (Path.GetPathRoot(normalized) == normalized)
            throw new WorkspaceRegistryException(
                "workspace.delete_target_invalid",
                "A volume root cannot be deleted as a workspace.");
        var manifest = ReadManifest(normalized);
        if (manifest.WorkspaceId != expectedWorkspaceId)
            throw new WorkspaceRegistryException(
                "workspace.identity_mismatch",
                "Workspace delete target has a different UUID.");
        Directory.Delete(normalized, recursive: true);
    }

    private static void EnsureCreateTarget(string root)
    {
        if (Directory.Exists(root) &&
            Directory.EnumerateFileSystemEntries(root).Any())
        {
            throw new WorkspaceRegistryException(
                "workspace.create_target_not_empty",
                "Workspace root must be new or empty.");
        }
    }

    private static void CreateRoot(
        string root,
        WorkspaceManifestV2 manifest,
        IEnumerable<string> metadataDirectories)
    {
        Directory.CreateDirectory(root);
        Directory.CreateDirectory(Path.Combine(root, "files"));
        var metadata = Path.Combine(root, MetadataDirectoryName);
        Directory.CreateDirectory(metadata);
        foreach (var directory in metadataDirectories)
            Directory.CreateDirectory(Path.Combine(metadata, directory));
        DurableJsonFile.Write(
            Path.Combine(metadata, ManifestFileName),
            manifest,
            WorkspaceV2Json.StrictOptions);
    }

    private static void TryDeleteFreshRoot(string root, Guid workspaceId)
    {
        try
        {
            if (!Directory.Exists(root))
                return;
            var manifest = ReadManifest(root);
            if (manifest.WorkspaceId == workspaceId)
                Directory.Delete(root, recursive: true);
        }
        catch
        {
            // Preserve unexpected or partially-created state for diagnosis.
        }
    }
}

public sealed record WorkspaceLayoutResult(
    WorkspaceManifestV2 Manifest,
    string SelectedRoot,
    string ActivityRoot);

public sealed record WorkspacePaths(
    string Root,
    string Files,
    string Metadata,
    string Data,
    string Topology,
    string Objects,
    string Audit,
    string Snapshots,
    string Coordination,
    string Quarantine,
    string Temp);
