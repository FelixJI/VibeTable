using System.Text.Json;
using System.Text.Json.Serialization;

namespace VibeTable.Contracts;

public interface IWorkspaceV2Contract
{
    void Validate();
}

public static class WorkspaceV2Json
{
    public const string ContractVersion = "2.0";
    public const ulong WorkspaceFormatVersion = 2;

    public static JsonSerializerOptions StrictOptions { get; } = CreateOptions();

    public static T DeserializeStrict<T>(string json)
        where T : IWorkspaceV2Contract
    {
        var value = JsonSerializer.Deserialize<T>(json, StrictOptions)
            ?? throw new JsonException($"The {typeof(T).Name} payload is null.");
        value.Validate();
        return value;
    }

    private static JsonSerializerOptions CreateOptions()
    {
        var options = new JsonSerializerOptions(JsonSerializerDefaults.Web)
        {
            UnmappedMemberHandling = JsonUnmappedMemberHandling.Disallow,
            PropertyNameCaseInsensitive = false,
        };
        options.Converters.Add(new JsonStringEnumConverter(JsonNamingPolicy.CamelCase));
        return options;
    }

    internal static void RequireVersion(string version)
    {
        if (!string.Equals(version, ContractVersion, StringComparison.Ordinal))
            throw new JsonException("contractVersion must be 2.0.");
    }
}

public enum WorkspaceStorageMode { Direct, Mirrored }
public enum WorkspaceEncryptionMode { None, Convenient, Protected }
public enum WorkspaceStorageKind { Fixed, Network, Removable, RegisteredCloud, UserMarkedSync }
public enum WorkspaceCoordinationStrength { Strong, Advisory }
public enum WorkspaceHealth { Healthy, Offline, Degraded, Corrupt, Unknown }
public enum WorkspaceOpenMode { ReadOnly, Writable, Provisional }
public enum WorkspaceSessionState
{
    Closed,
    Opening,
    OpenedReadOnly,
    OpenedWritable,
    OpenedProvisional,
    Switching,
    Failed,
}
public enum WorkspaceSessionPhase
{
    Idle,
    Protecting,
    Draining,
    Stopping,
    Starting,
    Binding,
    Verifying,
    RollingBack,
}
public enum FileDocumentStatus { Active, Deleted }
public enum FileRevisionKind { Autosave, Formal, Restore }
public enum SnapshotTrigger { Automatic, Manual, Protection, Import, Restore }
public enum SnapshotState
{
    Queued,
    Barrier,
    Captured,
    Chunking,
    Verifying,
    Published,
    Syncing,
    Ready,
    Failed,
    Corrupt,
    Repairing,
}
public enum SnapshotIntegrity { Pending, Verified, Corrupt, Repairing }
public enum SnapshotSyncState { LocalOnly, Pending, Syncing, Replicated, Failed }
public enum LeaseMode { Writable, Provisional }

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record GlobalWireScope : IWorkspaceV2Contract
{
    [JsonRequired] public required string Scope { get; init; }
    [JsonRequired] public required Guid OperationId { get; init; }
    [JsonRequired] public required ulong Sequence { get; init; }

    public void Validate()
    {
        if (Scope != "global" || OperationId == Guid.Empty)
            throw new JsonException("Global wire scope is invalid.");
    }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record WorkspaceWireScope : IWorkspaceV2Contract
{
    [JsonRequired] public required string Scope { get; init; }
    [JsonRequired] public required Guid WorkspaceId { get; init; }
    [JsonRequired] public required ulong SessionEpoch { get; init; }
    [JsonRequired] public required Guid OperationId { get; init; }
    [JsonRequired] public required ulong Sequence { get; init; }

    public void Validate()
    {
        if (Scope != "workspace" || WorkspaceId == Guid.Empty ||
            OperationId == Guid.Empty || SessionEpoch == 0)
            throw new JsonException("Workspace wire scope is invalid.");
    }

    public void EnsureCurrent(Guid workspaceId, ulong sessionEpoch, ulong minimumSequence = 0)
    {
        if (WorkspaceId != workspaceId)
            throw new InvalidOperationException("workspace.workspace_mismatch");
        if (SessionEpoch != sessionEpoch)
            throw new InvalidOperationException("workspace.session_epoch_stale");
        if (Sequence < minimumSequence)
            throw new InvalidOperationException("workspace.sequence_stale");
    }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record WorkspaceManifestV2 : IWorkspaceV2Contract
{
    [JsonRequired] public required string ContractVersion { get; init; }
    [JsonRequired] public required ulong FormatVersion { get; init; }
    [JsonRequired] public required Guid WorkspaceId { get; init; }
    [JsonRequired] public required string DisplayName { get; init; }
    [JsonRequired] public required DateTimeOffset CreatedAt { get; init; }
    [JsonRequired] public required WorkspaceStorageMode StorageMode { get; init; }
    [JsonRequired] public required WorkspaceEncryptionMode EncryptionMode { get; init; }
    [JsonRequired] public required string RepositoryFormat { get; init; }
    [JsonRequired] public required ulong TopologySchemaVersion { get; init; }
    [JsonRequired] public required ulong BusinessSchemaVersion { get; init; }
    [JsonRequired] public required Guid? ImportedFromWorkspaceId { get; init; }
    [JsonRequired] public required Guid? SourceSnapshotId { get; init; }

    public void Validate()
    {
        WorkspaceV2Json.RequireVersion(ContractVersion);
        if (FormatVersion != WorkspaceV2Json.WorkspaceFormatVersion)
            throw new JsonException("workspace.format_unsupported");
        if (WorkspaceId == Guid.Empty ||
            string.IsNullOrWhiteSpace(DisplayName) ||
            string.IsNullOrWhiteSpace(RepositoryFormat) ||
            TopologySchemaVersion == 0 || BusinessSchemaVersion == 0)
            throw new JsonException("Workspace manifest is invalid.");
    }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record WorkspaceRegistryEntryV2 : IWorkspaceV2Contract
{
    [JsonRequired] public required string ContractVersion { get; init; }
    [JsonRequired] public required Guid WorkspaceId { get; init; }
    [JsonRequired] public required string DisplayName { get; init; }
    [JsonRequired] public required string SelectedRoot { get; init; }
    [JsonRequired] public required string? ActivityRoot { get; init; }
    [JsonRequired] public required WorkspaceStorageKind StorageKind { get; init; }
    [JsonRequired] public required WorkspaceCoordinationStrength CoordinationStrength { get; init; }
    [JsonRequired] public required DateTimeOffset? LastOpenedAt { get; init; }
    [JsonRequired] public required WorkspaceHealth LastKnownHealth { get; init; }
    [JsonRequired] public required DateTimeOffset? LastSnapshotAt { get; init; }
    [JsonRequired] public required DateTimeOffset? LastSyncAt { get; init; }
    [JsonRequired] public required bool PendingSync { get; init; }

    public void Validate()
    {
        WorkspaceV2Json.RequireVersion(ContractVersion);
        if (WorkspaceId == Guid.Empty || string.IsNullOrWhiteSpace(DisplayName) ||
            string.IsNullOrWhiteSpace(SelectedRoot))
            throw new JsonException("Workspace registry entry is invalid.");
    }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record WorkspaceSessionV2 : IWorkspaceV2Contract
{
    [JsonRequired] public required string ContractVersion { get; init; }
    [JsonRequired] public required Guid? WorkspaceId { get; init; }
    [JsonRequired] public required ulong SessionEpoch { get; init; }
    [JsonRequired] public required WorkspaceSessionState State { get; init; }
    [JsonRequired] public required WorkspaceOpenMode OpenMode { get; init; }
    [JsonRequired] public required bool Writable { get; init; }
    [JsonRequired] public required bool Provisional { get; init; }
    [JsonRequired] public required WorkspaceSessionPhase Phase { get; init; }
    [JsonRequired] public required string? ErrorCode { get; init; }

    public void Validate()
    {
        WorkspaceV2Json.RequireVersion(ContractVersion);
        if (State == WorkspaceSessionState.Closed)
        {
            if (WorkspaceId is not null || Writable || Provisional)
                throw new JsonException("Closed session cannot own a workspace.");
            return;
        }
        if (WorkspaceId is null || WorkspaceId == Guid.Empty || SessionEpoch == 0)
            throw new JsonException("Open session requires workspace identity.");
        if (State == WorkspaceSessionState.OpenedWritable && !Writable)
            throw new JsonException("OpenedWritable session must be writable.");
        if (State == WorkspaceSessionState.OpenedProvisional && !Provisional)
            throw new JsonException("OpenedProvisional session must be provisional.");
    }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FileDocumentV2 : IWorkspaceV2Contract
{
    [JsonRequired] public required string ContractVersion { get; init; }
    [JsonRequired] public required Guid DocumentId { get; init; }
    [JsonRequired] public required Guid WorkspaceId { get; init; }
    [JsonRequired] public required string RelativePath { get; init; }
    [JsonRequired] public required FileDocumentStatus Status { get; init; }
    [JsonRequired] public required Guid? EffectiveRevisionId { get; init; }
    [JsonRequired] public required ulong NextRevisionOrdinal { get; init; }
    [JsonRequired] public required ulong NextFormalVersion { get; init; }

    public void Validate()
    {
        WorkspaceV2Json.RequireVersion(ContractVersion);
        var parts = RelativePath.Replace('\\', '/').Split('/');
        if (DocumentId == Guid.Empty || WorkspaceId == Guid.Empty ||
            string.IsNullOrWhiteSpace(RelativePath) || Path.IsPathRooted(RelativePath) ||
            parts.Any(part => part is "" or "..") ||
            NextRevisionOrdinal == 0 || NextFormalVersion == 0)
            throw new JsonException("File document is invalid.");
    }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FileDocumentSummaryV2 : IWorkspaceV2Contract
{
    [JsonRequired] public required string ContractVersion { get; init; }
    [JsonRequired] public required Guid DocumentId { get; init; }
    [JsonRequired] public required string RelativePath { get; init; }
    [JsonRequired] public required string DisplayName { get; init; }
    [JsonRequired] public required string Extension { get; init; }
    [JsonRequired] public required string MimeType { get; init; }
    [JsonRequired] public required ulong SizeBytes { get; init; }
    [JsonRequired] public required Guid EffectiveRevisionId { get; init; }
    [JsonRequired] public required DateTimeOffset EffectiveRevisionCreatedAt { get; init; }
    [JsonRequired] public required ulong? FormalVersion { get; init; }
    [JsonRequired] public required FileDocumentStatus Status { get; init; }

    public void Validate()
    {
        WorkspaceV2Json.RequireVersion(ContractVersion);
        var parts = RelativePath.Replace('\\', '/').Split('/');
        if (DocumentId == Guid.Empty || EffectiveRevisionId == Guid.Empty ||
            string.IsNullOrWhiteSpace(RelativePath) || Path.IsPathRooted(RelativePath) ||
            parts.Any(part => part is "" or "..") ||
            string.IsNullOrWhiteSpace(DisplayName) ||
            string.IsNullOrWhiteSpace(MimeType) ||
            Extension.StartsWith(".", StringComparison.Ordinal))
            throw new JsonException("File document summary is invalid.");
    }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FileRevisionV2 : IWorkspaceV2Contract
{
    [JsonRequired] public required string ContractVersion { get; init; }
    [JsonRequired] public required Guid RevisionId { get; init; }
    [JsonRequired] public required Guid DocumentId { get; init; }
    [JsonRequired] public required Guid? ParentRevisionId { get; init; }
    [JsonRequired] public required ulong RevisionOrdinal { get; init; }
    [JsonRequired] public required ulong? LocalSequence { get; init; }
    [JsonRequired] public required ulong? FormalVersion { get; init; }
    [JsonRequired] public required FileRevisionKind Kind { get; init; }
    [JsonRequired] public required string ObjectId { get; init; }
    [JsonRequired] public required string ContentHash { get; init; }
    [JsonRequired] public required ulong Size { get; init; }
    [JsonRequired] public required string MimeType { get; init; }
    [JsonRequired] public required DateTimeOffset CreatedAt { get; init; }
    [JsonRequired] public required string CreatedBy { get; init; }
    [JsonRequired] public required Guid DeviceId { get; init; }
    [JsonRequired] public required string? Comment { get; init; }
    [JsonRequired] public required Guid? RestoredFromRevisionId { get; init; }

    public void Validate()
    {
        WorkspaceV2Json.RequireVersion(ContractVersion);
        if (RevisionId == Guid.Empty || DocumentId == Guid.Empty ||
            DeviceId == Guid.Empty ||
            !ObjectId.StartsWith("obj_", StringComparison.Ordinal) ||
            !ContentHash.StartsWith("sha256:", StringComparison.Ordinal) ||
            string.IsNullOrWhiteSpace(MimeType) || string.IsNullOrWhiteSpace(CreatedBy))
            throw new JsonException("File revision is invalid.");
        bool provisional = RevisionOrdinal == 0;
        if (provisional && LocalSequence is null or 0)
            throw new JsonException("Provisional revision requires localSequence.");
        if (LocalSequence == 0)
            throw new JsonException("localSequence must be positive.");
        if (provisional && FormalVersion is not null)
            throw new JsonException("Provisional revision cannot consume a formal version.");
        if (Kind == FileRevisionKind.Autosave && FormalVersion is not null)
            throw new JsonException("Autosave cannot consume a formal version.");
        if (!provisional &&
            Kind != FileRevisionKind.Autosave &&
            FormalVersion is null or 0)
            throw new JsonException("Formal revision requires a formal version.");
        if (Kind == FileRevisionKind.Restore && RestoredFromRevisionId is null)
            throw new JsonException("Restore requires restoredFromRevisionId.");
        if (Kind != FileRevisionKind.Restore && RestoredFromRevisionId is not null)
            throw new JsonException("Only restore may reference restored content.");
    }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record AuditAnchorV2 : IWorkspaceV2Contract
{
    [JsonRequired] public required ulong Epoch { get; init; }
    [JsonRequired] public required ulong Sequence { get; init; }
    [JsonRequired] public required string ChainHash { get; init; }

    public void Validate()
    {
        if (Epoch == 0 || !ChainHash.StartsWith("sha256:", StringComparison.Ordinal))
            throw new JsonException("Audit anchor is invalid.");
    }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SnapshotManifestV2 : IWorkspaceV2Contract
{
    [JsonRequired] public required string ContractVersion { get; init; }
    [JsonRequired] public required Guid SnapshotId { get; init; }
    [JsonRequired] public required Guid WorkspaceId { get; init; }
    [JsonRequired] public required ulong FenceEpoch { get; init; }
    [JsonRequired] public required Guid ClaimId { get; init; }
    [JsonRequired] public required ulong MutationRevision { get; init; }
    [JsonRequired] public required ulong SnapshotSequence { get; init; }
    [JsonRequired] public required SnapshotTrigger Trigger { get; init; }
    [JsonRequired] public required DateTimeOffset CreatedAt { get; init; }
    [JsonRequired] public required Guid CreatedByDevice { get; init; }
    [JsonRequired] public required string BusinessDatabaseObjectId { get; init; }
    [JsonRequired] public required string TopologyRootObjectId { get; init; }
    [JsonRequired] public required string FileStateRootObjectId { get; init; }
    [JsonRequired] public required string WorkspaceSettingsObjectId { get; init; }
    [JsonRequired] public required AuditAnchorV2 AuditAnchor { get; init; }
    [JsonRequired] public required string AuditPrefixObjectId { get; init; }
    [JsonRequired] public required Guid? SourceSnapshotId { get; init; }
    [JsonRequired] public required ulong FormatVersion { get; init; }
    [JsonRequired] public required string MinimumAppVersion { get; init; }

    public void Validate()
    {
        WorkspaceV2Json.RequireVersion(ContractVersion);
        AuditAnchor.Validate();
        var objectIds = new[]
        {
            BusinessDatabaseObjectId,
            TopologyRootObjectId,
            FileStateRootObjectId,
            WorkspaceSettingsObjectId,
            AuditPrefixObjectId,
        };
        if (SnapshotId == Guid.Empty || WorkspaceId == Guid.Empty ||
            ClaimId == Guid.Empty || CreatedByDevice == Guid.Empty ||
            FenceEpoch == 0 || SnapshotSequence == 0 || FormatVersion == 0 ||
            objectIds.Any(value => !value.StartsWith("obj_", StringComparison.Ordinal)))
            throw new JsonException("Snapshot manifest is invalid.");
    }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SnapshotSealV2 : IWorkspaceV2Contract
{
    [JsonRequired] public required string ContractVersion { get; init; }
    [JsonRequired] public required Guid SnapshotId { get; init; }
    [JsonRequired] public required string ManifestHash { get; init; }
    [JsonRequired] public required string DatabaseHash { get; init; }
    [JsonRequired] public required string FileStateRootHash { get; init; }
    [JsonRequired] public required string AuditAnchorHash { get; init; }
    [JsonRequired] public required string RepositoryFormat { get; init; }
    [JsonRequired] public required ulong FenceEpoch { get; init; }
    [JsonRequired] public required Guid ClaimId { get; init; }
    [JsonRequired] public required ulong MutationRevision { get; init; }
    [JsonRequired] public required ulong SnapshotSequence { get; init; }
    [JsonRequired] public required bool Verified { get; init; }

    public void Validate()
    {
        WorkspaceV2Json.RequireVersion(ContractVersion);
        if (SnapshotId == Guid.Empty || ClaimId == Guid.Empty ||
            FenceEpoch == 0 || SnapshotSequence == 0 || !Verified ||
            string.IsNullOrWhiteSpace(RepositoryFormat) ||
            new[] { ManifestHash, DatabaseHash, FileStateRootHash, AuditAnchorHash }
                .Any(value => !value.StartsWith("sha256:", StringComparison.Ordinal)))
            throw new JsonException("Snapshot seal is invalid.");
    }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SnapshotCatalogEntryV2 : IWorkspaceV2Contract
{
    [JsonRequired] public required string ContractVersion { get; init; }
    [JsonRequired] public required Guid SnapshotId { get; init; }
    [JsonRequired] public required SnapshotState State { get; init; }
    [JsonRequired] public required bool Pinned { get; init; }
    [JsonRequired] public required IReadOnlyList<string> RetentionReasons { get; init; }
    [JsonRequired] public required SnapshotIntegrity Integrity { get; init; }
    [JsonRequired] public required SnapshotSyncState SyncState { get; init; }
    [JsonRequired] public required ulong LogicalSize { get; init; }
    [JsonRequired] public required ulong PhysicalSize { get; init; }
    [JsonRequired] public required string? Note { get; init; }
    [JsonRequired] public required ulong CatalogRevision { get; init; }

    public void Validate()
    {
        WorkspaceV2Json.RequireVersion(ContractVersion);
        if (SnapshotId == Guid.Empty || CatalogRevision == 0 ||
            RetentionReasons.Any(string.IsNullOrWhiteSpace))
            throw new JsonException("Snapshot catalog entry is invalid.");
    }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record LeaseClaimV2 : IWorkspaceV2Contract
{
    [JsonRequired] public required string ContractVersion { get; init; }
    [JsonRequired] public required Guid WorkspaceId { get; init; }
    [JsonRequired] public required ulong FenceEpoch { get; init; }
    [JsonRequired] public required Guid ClaimId { get; init; }
    [JsonRequired] public required Guid DeviceId { get; init; }
    [JsonRequired] public required DateTimeOffset IssuedAt { get; init; }
    [JsonRequired] public required DateTimeOffset HeartbeatAt { get; init; }
    [JsonRequired] public required DateTimeOffset ExpiresAt { get; init; }
    [JsonRequired] public required LeaseMode Mode { get; init; }
    [JsonRequired] public required Guid? PreviousClaimId { get; init; }
    [JsonRequired] public required WorkspaceCoordinationStrength CoordinationStrength { get; init; }

    public void Validate()
    {
        WorkspaceV2Json.RequireVersion(ContractVersion);
        if (WorkspaceId == Guid.Empty || ClaimId == Guid.Empty || DeviceId == Guid.Empty ||
            FenceEpoch == 0 || HeartbeatAt < IssuedAt || ExpiresAt <= HeartbeatAt)
            throw new JsonException("Lease claim is invalid.");
    }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record RetentionPolicyV2 : IWorkspaceV2Contract
{
    [JsonRequired] public required string ContractVersion { get; init; }
    [JsonRequired] public required ulong PolicyRevision { get; init; }
    [JsonRequired] public required ulong SnapshotDays { get; init; }
    [JsonRequired] public required ulong SnapshotCount { get; init; }
    [JsonRequired] public required IReadOnlyList<string> SnapshotBuckets { get; init; }
    [JsonRequired] public required ulong FileRevisionDays { get; init; }
    [JsonRequired] public required ulong FileRevisionCount { get; init; }
    [JsonRequired] public required IReadOnlyList<string> FileRevisionBuckets { get; init; }
    [JsonRequired] public required ulong TrashMonths { get; init; }
    [JsonRequired] public required ulong? RepositoryLimitBytes { get; init; }

    public void Validate()
    {
        WorkspaceV2Json.RequireVersion(ContractVersion);
        var validBuckets = new HashSet<string>(["hourly", "daily", "weekly", "monthly"]);
        if (PolicyRevision == 0 || SnapshotDays == 0 || SnapshotCount == 0 ||
            FileRevisionDays == 0 || FileRevisionCount == 0 || TrashMonths != 3 ||
            RepositoryLimitBytes == 0 ||
            SnapshotBuckets.Distinct().Count() != SnapshotBuckets.Count ||
            FileRevisionBuckets.Distinct().Count() != FileRevisionBuckets.Count ||
            SnapshotBuckets.Any(bucket => !validBuckets.Contains(bucket)) ||
            FileRevisionBuckets.Any(bucket => !validBuckets.Contains(bucket)))
            throw new JsonException("Retention policy is invalid.");
    }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record WorkspaceEventV2 : IWorkspaceV2Contract
{
    [JsonRequired] public required string ContractVersion { get; init; }
    [JsonRequired] public required string Topic { get; init; }
    [JsonRequired] public required WorkspaceWireScope Wire { get; init; }
    [JsonRequired] public required string PayloadModel { get; init; }
    [JsonRequired] public required JsonElement PayloadSchema { get; init; }
    [JsonRequired] public required JsonElement Payload { get; init; }

    public void Validate()
    {
        WorkspaceV2Json.RequireVersion(ContractVersion);
        Wire.Validate();
        var expected = Topic switch
        {
            "workspace.session.changed" => ("WorkspaceSessionChangedEvent", new[] { "state", "phase" }),
            "snapshot.changed" => ("SnapshotChangedEvent", new[] { "snapshotId", "state", "integrity" }),
            "replica.changed" => ("ReplicaChangedEvent", new[] { "syncState", "pendingSync" }),
            "lease.changed" => ("LeaseChangedEvent", new[] { "mode", "coordinationStrength" }),
            "conflict.changed" => ("ConflictChangedEvent", new[] { "conflictId", "state" }),
            _ => throw new JsonException("Workspace event topic is invalid."),
        };
        if (PayloadModel != expected.Item1 ||
            Payload.ValueKind != JsonValueKind.Object ||
            !HasExactProperties(Payload, expected.Item2) ||
            PayloadSchema.ValueKind != JsonValueKind.Object ||
            !PayloadSchema.TryGetProperty("type", out var type) ||
            type.GetString() != "object" ||
            !PayloadSchema.TryGetProperty("additionalProperties", out var additional) ||
            additional.ValueKind != JsonValueKind.False ||
            !PayloadSchema.TryGetProperty("required", out var required) ||
            required.ValueKind != JsonValueKind.Array ||
            !required.EnumerateArray().Select(item => item.GetString()).ToHashSet()
                .SetEquals(expected.Item2))
            throw new JsonException("Workspace event payload schema is invalid.");
    }

    private static bool HasExactProperties(JsonElement value, IReadOnlyCollection<string> expected)
    {
        var actual = value.EnumerateObject().Select(property => property.Name).ToArray();
        return actual.Length == expected.Count &&
               actual.ToHashSet(StringComparer.Ordinal).SetEquals(expected);
    }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record RpcGoldenCaseV2 : IWorkspaceV2Contract
{
    [JsonRequired] public required string Method { get; init; }
    [JsonRequired] public required string Scope { get; init; }
    [JsonRequired] public required string ParamsModel { get; init; }
    [JsonRequired] public required string ResultModel { get; init; }
    [JsonRequired] public required JsonElement ParamsSchema { get; init; }
    [JsonRequired] public required JsonElement ResultSchema { get; init; }
    [JsonRequired] public required JsonElement Request { get; init; }
    [JsonRequired] public required JsonElement Success { get; init; }
    [JsonRequired] public required JsonElement Error { get; init; }

    public void Validate()
    {
        if (string.IsNullOrWhiteSpace(Method) ||
            string.IsNullOrWhiteSpace(ParamsModel) ||
            string.IsNullOrWhiteSpace(ResultModel) ||
            Scope is not ("global" or "workspace"))
            throw new JsonException("RPC catalog case identity is invalid.");
        RequireClosedSchema(ParamsSchema);
        RequireClosedSchema(ResultSchema);
        RequireExact(Request, ["jsonrpc", "id", "method", "wire", "params"]);
        RequireExact(Success, ["jsonrpc", "id", "wire", "result"]);
        RequireExact(Error, ["jsonrpc", "id", "wire", "error"]);
        if (Request.GetProperty("jsonrpc").GetString() != "2.0" ||
            Success.GetProperty("jsonrpc").GetString() != "2.0" ||
            Error.GetProperty("jsonrpc").GetString() != "2.0" ||
            Request.GetProperty("method").GetString() != Method ||
            Request.GetProperty("id").GetString() != Success.GetProperty("id").GetString() ||
            Request.GetProperty("id").GetString() != Error.GetProperty("id").GetString())
            throw new JsonException("RPC fixture envelopes are inconsistent.");
        var wire = Request.GetProperty("wire").GetRawText();
        if (Scope == "global")
            WorkspaceV2Json.DeserializeStrict<GlobalWireScope>(wire);
        else
            WorkspaceV2Json.DeserializeStrict<WorkspaceWireScope>(wire);
        if (Success.GetProperty("wire").GetRawText() != wire ||
            Error.GetProperty("wire").GetRawText() != wire)
            throw new JsonException("RPC fixture wires differ.");
    }

    private static void RequireClosedSchema(JsonElement schema)
    {
        if (schema.ValueKind != JsonValueKind.Object ||
            !schema.TryGetProperty("type", out var type) ||
            type.GetString() != "object" ||
            !schema.TryGetProperty("additionalProperties", out var additional) ||
            additional.ValueKind != JsonValueKind.False ||
            !ValidateSchemaNode(schema))
            throw new JsonException("RPC params/result schema must be a closed object.");
    }

    private static bool ValidateSchemaNode(
        JsonElement schema,
        bool conditional = false)
    {
        if (schema.ValueKind == JsonValueKind.True)
            return true;
        if (schema.ValueKind != JsonValueKind.Object ||
            !HasOnlySchemaKeys(
                schema,
                "type",
                "enum",
                "const",
                "minimum",
                "maximum",
                "minLength",
                "maxItems",
                "pattern",
                "additionalProperties",
                "required",
                "properties",
                "items",
                "allOf",
                "oneOf"))
            return false;
        bool hasType = schema.TryGetProperty(
            "type",
            out JsonElement type);
        if (hasType && type.ValueKind == JsonValueKind.Array)
        {
            string[] allowed =
                ["string", "integer", "number", "boolean", "null"];
            JsonElement[] values = type.EnumerateArray().ToArray();
            if (values.Length == 0 ||
                !values.All(item =>
                    item.ValueKind == JsonValueKind.String &&
                    allowed.Contains(
                        item.GetString(),
                        StringComparer.Ordinal)))
                return false;
        }
        else if (hasType &&
            (type.ValueKind != JsonValueKind.String ||
             type.GetString() is not (
                 "object" or
                 "array" or
                 "string" or
                 "integer" or
                 "number" or
                 "boolean" or
                 "null")))
            return false;
        if (schema.TryGetProperty(
                "enum",
                out JsonElement enumValues) &&
            enumValues.ValueKind != JsonValueKind.Array)
            return false;
        if (schema.TryGetProperty(
                "minimum",
                out JsonElement minimum) &&
            minimum.ValueKind != JsonValueKind.Number)
            return false;
        if (schema.TryGetProperty(
                "maximum",
                out JsonElement maximum) &&
            maximum.ValueKind != JsonValueKind.Number)
            return false;
        if (schema.TryGetProperty(
                "minLength",
                out JsonElement minLength) &&
            (minLength.ValueKind != JsonValueKind.Number ||
             !minLength.TryGetInt32(out int minLengthValue) ||
             minLengthValue < 0))
            return false;
        if (schema.TryGetProperty(
                "maxItems",
                out JsonElement maxItems) &&
            (maxItems.ValueKind != JsonValueKind.Number ||
             !maxItems.TryGetInt32(out int maxItemsValue) ||
             maxItemsValue < 0))
            return false;
        if (schema.TryGetProperty(
                "pattern",
                out JsonElement pattern) &&
            pattern.ValueKind != JsonValueKind.String)
            return false;
        foreach (string keyword in new[] { "allOf", "oneOf" })
        {
            if (!schema.TryGetProperty(
                    keyword,
                    out JsonElement alternatives))
                continue;
            JsonElement[] branches =
                alternatives.ValueKind == JsonValueKind.Array
                    ? alternatives.EnumerateArray().ToArray()
                    : [];
            if (branches.Length == 0 ||
                !branches.All(branch =>
                    ValidateSchemaNode(branch, conditional: true)))
                return false;
        }
        JsonElement properties = default;
        bool hasProperties = schema.TryGetProperty(
            "properties",
            out properties);
        if (hasProperties &&
            (properties.ValueKind != JsonValueKind.Object ||
             !properties.EnumerateObject().All(property =>
                 ValidateSchemaNode(
                     property.Value,
                     conditional))))
            return false;
        JsonElement required = default;
        bool hasRequired = schema.TryGetProperty(
            "required",
            out required);
        string[] propertyNames = hasProperties
            ? properties.EnumerateObject()
                .Select(property => property.Name)
                .ToArray()
            : [];
        string?[] requiredNames = hasRequired &&
            required.ValueKind == JsonValueKind.Array
                ? required.EnumerateArray()
                    .Select(item =>
                        item.ValueKind == JsonValueKind.String
                            ? item.GetString()
                            : null)
                    .ToArray()
                : [];
        if (hasRequired &&
            (required.ValueKind != JsonValueKind.Array ||
             requiredNames.Any(name => name is null) ||
             requiredNames.Distinct(StringComparer.Ordinal).Count() !=
                 requiredNames.Length ||
             !requiredNames!
                 .Cast<string>()
                 .ToHashSet(StringComparer.Ordinal)
                 .IsSubsetOf(propertyNames)))
            return false;
        bool hasAdditional = schema.TryGetProperty(
            "additionalProperties",
            out JsonElement additional);
        bool typedMap = hasAdditional &&
            additional.ValueKind == JsonValueKind.Object &&
            ValidateSchemaNode(additional, conditional: true);
        if (hasAdditional &&
            additional.ValueKind is not (
                JsonValueKind.True or JsonValueKind.False) &&
            !typedMap)
            return false;
        if (hasType &&
            type.ValueKind == JsonValueKind.String &&
            type.GetString() == "object" &&
            !conditional &&
            !(
                hasAdditional &&
                additional.ValueKind == JsonValueKind.False &&
                hasProperties &&
                hasRequired &&
                requiredNames.Length == propertyNames.Length &&
                requiredNames!
                    .Cast<string>()
                    .ToHashSet(StringComparer.Ordinal)
                    .SetEquals(propertyNames) ||
                typedMap &&
                !hasProperties &&
                !hasRequired
            ))
            return false;
        bool hasItems = schema.TryGetProperty(
            "items",
            out JsonElement items);
        if (hasType &&
            type.ValueKind == JsonValueKind.String &&
            type.GetString() == "array")
            return hasItems && ValidateSchemaNode(items);
        if (hasItems)
            return false;
        return hasType ||
            schema.TryGetProperty("enum", out _) ||
            schema.TryGetProperty("const", out _) ||
            hasProperties ||
            schema.TryGetProperty("allOf", out _) ||
            schema.TryGetProperty("oneOf", out _);
    }

    private static bool HasOnlySchemaKeys(
        JsonElement schema,
        params string[] allowed)
    {
        HashSet<string> names = allowed.ToHashSet(StringComparer.Ordinal);
        return schema.EnumerateObject().All(
            property => names.Contains(property.Name));
    }

    private static void RequireExact(JsonElement value, IReadOnlyCollection<string> expected)
    {
        if (value.ValueKind != JsonValueKind.Object)
            throw new JsonException("RPC envelope must be an object.");
        var actual = value.EnumerateObject().Select(property => property.Name).ToArray();
        if (actual.Length != expected.Count ||
            !actual.ToHashSet(StringComparer.Ordinal).SetEquals(expected))
            throw new JsonException("RPC envelope contains missing or unknown fields.");
    }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record RpcContractCatalogV2 : IWorkspaceV2Contract
{
    [JsonRequired] public required string ContractVersion { get; init; }
    [JsonRequired] public required IReadOnlyList<string> RpcMethods { get; init; }
    [JsonRequired] public required IReadOnlyList<string> EventTopics { get; init; }
    [JsonRequired] public required IReadOnlyList<RpcGoldenCaseV2> RpcCases { get; init; }
    [JsonRequired] public required IReadOnlyList<WorkspaceEventV2> EventCases { get; init; }

    public void Validate()
    {
        WorkspaceV2Json.RequireVersion(ContractVersion);
        foreach (var item in RpcCases)
            item.Validate();
        foreach (var item in EventCases)
            item.Validate();
        if (RpcMethods.Distinct(StringComparer.Ordinal).Count() != RpcMethods.Count ||
            EventTopics.Distinct(StringComparer.Ordinal).Count() != EventTopics.Count ||
            !RpcMethods.SequenceEqual(RpcCases.Select(item => item.Method)) ||
            !EventTopics.SequenceEqual(EventCases.Select(item => item.Topic)))
            throw new JsonException("RPC catalog registry is missing, duplicated, or stale.");
    }
}
