using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Text.Json;
using System.Text.RegularExpressions;
using VibeTable.Contracts;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

public sealed record WorkspaceReplicaReceipt(
    string Operation,
    Guid WorkspaceId,
    Guid ReplicaId,
    Guid SnapshotId,
    ulong CatalogRevision,
    string CheckpointId,
    string ReceiptHash,
    DateTimeOffset VerifiedAt,
    string? ActivityRoot,
    ulong MutationRevision = 0,
    ulong RequiredMutationRevision = 0);

public sealed record WorkspaceRepositoryRecoveryAuthority(
    Guid WorkspaceId,
    ulong FenceEpoch,
    Guid ClaimId);

public interface IWorkspaceReplicaRecoveryService
{
    Task<WorkspaceReplicaReceipt> InitializeAsync(
        WorkspaceRegistryEntryV2 workspace,
        CancellationToken cancellationToken);

    Task<WorkspaceReplicaReceipt> VerifyAsync(
        WorkspaceRegistryEntryV2 workspace,
        CancellationToken cancellationToken);

    Task<WorkspaceReplicaReceipt> RecoverAndPublishAsync(
        WorkspaceRegistryEntryV2 workspace,
        CancellationToken cancellationToken);

    bool RequiresRecovery(WorkspaceRegistryEntryV2 workspace);
}

/// <summary>
/// Trusted Desktop boundary for mirrored-replica initialization, recovery and
/// independent verification. Paths are environment-only and the bundled
/// Sidecar publishes a single strict receipt on stdout.
/// </summary>
public sealed class WorkspaceReplicaRecoveryService :
    IWorkspaceReplicaRecoveryService
{
    private const int MaxTrustedOutputCharacters = 16 * 1024;
    internal static readonly TimeSpan DefaultReplicaOperationTimeout =
        TimeSpan.FromHours(4);
    private static readonly Regex Sha256 = new(
        "^sha256:[0-9a-f]{64}$",
        RegexOptions.CultureInvariant | RegexOptions.NonBacktracking);

    private readonly Func<PocketBaseLaunchOptions> _optionsFactory;
    private readonly Func<WorkspaceRegistryEntryV2, WorkspaceRepositoryAuthority>
        _authorityFactory;
    private readonly Func<WorkspaceRegistryEntryV2, WorkspaceRepositoryRecoveryAuthority>
        _recoveryAuthorityFactory;
    private readonly Action<WorkspaceRegistryEntryV2, string,
        WorkspaceRepositoryRecoveryAuthority>
        _recoveryAuthorityPublisher;
    private readonly ITrustedSidecarProcessRunner _runner;
    private readonly TimeSpan _replicaOperationTimeout;

    public WorkspaceReplicaRecoveryService(
        Func<PocketBaseLaunchOptions> optionsFactory,
        Func<WorkspaceRegistryEntryV2, WorkspaceRepositoryAuthority>
            authorityFactory,
        Func<WorkspaceRegistryEntryV2, WorkspaceRepositoryRecoveryAuthority>
            recoveryAuthorityFactory,
        Action<WorkspaceRegistryEntryV2, string,
            WorkspaceRepositoryRecoveryAuthority> recoveryAuthorityPublisher,
        ITrustedSidecarProcessRunner? runner = null,
        TimeSpan? replicaOperationTimeout = null)
    {
        _optionsFactory = optionsFactory
            ?? throw new ArgumentNullException(nameof(optionsFactory));
        _authorityFactory = authorityFactory
            ?? throw new ArgumentNullException(nameof(authorityFactory));
        _recoveryAuthorityFactory = recoveryAuthorityFactory
            ?? throw new ArgumentNullException(nameof(recoveryAuthorityFactory));
        _recoveryAuthorityPublisher = recoveryAuthorityPublisher
            ?? throw new ArgumentNullException(nameof(recoveryAuthorityPublisher));
        _runner = runner ?? new TrustedSidecarProcessRunner();
        _replicaOperationTimeout =
            replicaOperationTimeout ?? DefaultReplicaOperationTimeout;
        if (_replicaOperationTimeout <= TimeSpan.Zero)
            throw new ArgumentOutOfRangeException(
                nameof(replicaOperationTimeout),
                "Replica operation timeout must be positive.");
    }

    public Task<WorkspaceReplicaReceipt> InitializeAsync(
        WorkspaceRegistryEntryV2 workspace,
        CancellationToken cancellationToken)
        => RunAsync(
            workspace,
            "--initialize-workspace-replica",
            "initialize",
            activityRoot: null,
            cancellationToken);

    public Task<WorkspaceReplicaReceipt> VerifyAsync(
        WorkspaceRegistryEntryV2 workspace,
        CancellationToken cancellationToken)
        => RunAsync(
            workspace,
            "--verify-workspace-replica",
            "verify",
            activityRoot: null,
            cancellationToken);

    public async Task<WorkspaceReplicaReceipt> RecoverAndPublishAsync(
        WorkspaceRegistryEntryV2 workspace,
        CancellationToken cancellationToken)
    {
        RequireMirrored(workspace);
        string finalRoot = Path.GetFullPath(
            workspace.ActivityRoot
            ?? throw new WorkspaceRegistryException(
                "workspace.activity_root_required",
                "Mirrored workspaces require a local activity root."));
        EnsureEmptyOrMissing(finalRoot);
        string? parent = Path.GetDirectoryName(finalRoot);
        if (string.IsNullOrWhiteSpace(parent))
            throw RecoveryTargetInvalid();
        Directory.CreateDirectory(parent);
        string staging = Path.Combine(
            parent,
            $".{Path.GetFileName(finalRoot)}.vibetable-recovering-" +
            Guid.NewGuid().ToString("N"));
        if (Directory.Exists(staging) || File.Exists(staging))
            throw RecoveryTargetInvalid();
        WorkspaceRepositoryRecoveryAuthority authority =
            _recoveryAuthorityFactory(workspace);
        if (authority.WorkspaceId != workspace.WorkspaceId
            || authority.FenceEpoch == 0
            || authority.ClaimId == Guid.Empty)
            throw new WorkspaceRegistryException(
                "workspace.authority_invalid",
                "Prepared workspace recovery authority is invalid.");
        // A recovery authority is intentionally detached from the missing
        // final activity root. Persisting it there would make the target
        // non-empty before the verified staging tree can be atomically moved.
        EnsureEmptyOrMissing(finalRoot);

        try
        {
            WorkspaceReplicaReceipt receipt = await RunAsync(
                workspace,
                "--recover-workspace-replica",
                "recover",
                staging,
                cancellationToken,
                new WorkspaceRepositoryAuthority(
                    authority.FenceEpoch,
                    authority.ClaimId)).ConfigureAwait(false);
            if (!string.Equals(
                    Path.GetFullPath(receipt.ActivityRoot
                        ?? throw InvalidOutput()),
                    staging,
                    StringComparison.OrdinalIgnoreCase))
                throw InvalidOutput();
            WorkspaceManifestV2 manifest =
                WorkspaceLayout.ReadManifest(staging);
            if (manifest.WorkspaceId != workspace.WorkspaceId ||
                manifest.StorageMode != WorkspaceStorageMode.Mirrored)
                throw new WorkspaceRegistryException(
                    "workspace.identity_mismatch",
                    "Recovered activity root does not match the mirrored workspace.");
            EnsureRecoveredLayout(staging);
            _recoveryAuthorityPublisher(
                workspace,
                staging,
                authority);

            if (Directory.Exists(finalRoot))
            {
                if (Directory.EnumerateFileSystemEntries(finalRoot).Any())
                    throw RecoveryTargetInvalid();
                Directory.Delete(finalRoot);
            }
            Directory.Move(staging, finalRoot);
            return receipt with { ActivityRoot = finalRoot };
        }
        catch
        {
            TryDeleteOwnedRecoveryStaging(
                staging,
                workspace.WorkspaceId);
            throw;
        }
    }

    public bool RequiresRecovery(WorkspaceRegistryEntryV2 workspace)
    {
        ArgumentNullException.ThrowIfNull(workspace);
        string activity = Path.GetFullPath(
            workspace.ActivityRoot
            ?? throw new WorkspaceRegistryException(
                "workspace.activity_root_required",
                "Mirrored workspaces require a local activity root."));
        if (!Directory.Exists(activity))
            return true;
        try
        {
            WorkspaceManifestV2 manifest = WorkspaceLayout.ReadManifest(activity);
            return manifest.WorkspaceId != workspace.WorkspaceId ||
                manifest.StorageMode != WorkspaceStorageMode.Mirrored ||
                !HasRecoveredLayout(activity);
        }
        catch (WorkspaceRegistryException)
        {
            return true;
        }
    }

    private async Task<WorkspaceReplicaReceipt> RunAsync(
        WorkspaceRegistryEntryV2 workspace,
        string flag,
        string operation,
        string? activityRoot,
        CancellationToken cancellationToken,
        WorkspaceRepositoryAuthority? preparedAuthority = null)
    {
        RequireMirrored(workspace);
        PocketBaseLaunchOptions options = _optionsFactory();
        WorkspaceRepositoryAuthority authority =
            preparedAuthority ?? _authorityFactory(workspace);
        ProcessStartInfo start =
            WorkspaceRepositoryOnboardingService.CreateStartInfo(
                options,
                workspace,
                flag,
                authority);
        start.Environment["VIBETABLE_REPLICA_ROOT"] =
            Path.GetFullPath(workspace.SelectedRoot);
        if (activityRoot is not null)
        {
            start.Environment["VIBETABLE_ACTIVITY_ROOT"] =
                Path.GetFullPath(activityRoot);
            start.Environment["VIBETABLE_SIDECAR_DATA_DIR"] =
                WorkspaceLayout.Paths(activityRoot).Data;
        }
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(
            cancellationToken);
        // Replica initialization, authenticated verification and full recovery
        // are data-volume operations. PocketBase StartupTimeout only bounds a
        // service handshake and must not truncate a multi-hour copy. The
        // linked caller token still cancels immediately.
        timeout.CancelAfter(_replicaOperationTimeout);
        TrustedSidecarProcessResult result = await _runner.RunAsync(
            start,
            standardInput: null,
            timeout.Token).ConfigureAwait(false);
        if (result.ExitCode != 0)
            throw new WorkspaceRegistryException(
                result.ExitCode == 2
                    ? "workspace.replica_request_invalid"
                    : "workspace.replica_unavailable",
                "The bundled Sidecar could not verify the mirrored replica.");
        return ParseReceipt(
            result.StandardOutput,
            workspace.WorkspaceId,
            operation,
            activityRoot);
    }

    internal static WorkspaceReplicaReceipt ParseReceipt(
        string output,
        Guid expectedWorkspaceId,
        string expectedOperation,
        string? expectedActivityRoot)
    {
        if (string.IsNullOrWhiteSpace(output) ||
            output.Length > MaxTrustedOutputCharacters)
            throw InvalidOutput();
        try
        {
            using JsonDocument document = JsonDocument.Parse(output);
            JsonElement root = document.RootElement;
            string stateProperty = expectedOperation == "recover"
                ? "restored"
                : "healthy";
            string[] expected =
            [
                "activityRoot",
                "catalogRevision",
                "checkpointId",
                "contractVersion",
                stateProperty,
                "operation",
                "receiptHash",
                "replicaId",
                "requiredMutationRevision",
                "snapshotId",
                "mutationRevision",
                "verifiedAt",
                "workspaceId",
            ];
            string[] actual = root.EnumerateObject()
                .Select(property => property.Name)
                .Order(StringComparer.Ordinal)
                .ToArray();
            Array.Sort(expected, StringComparer.Ordinal);
            if (!actual.SequenceEqual(expected, StringComparer.Ordinal) ||
                RequiredString(root, "contractVersion") !=
                    WorkspaceV2Json.ContractVersion ||
                RequiredString(root, "operation") != expectedOperation ||
                !root.TryGetProperty(
                    stateProperty,
                    out JsonElement state) ||
                state.ValueKind != JsonValueKind.True ||
                !Guid.TryParse(
                    RequiredString(root, "workspaceId"),
                    out Guid workspaceId) ||
                workspaceId != expectedWorkspaceId ||
                !Guid.TryParse(
                    RequiredString(root, "replicaId"),
                    out Guid replicaId) ||
                replicaId == Guid.Empty ||
                !Guid.TryParse(
                    RequiredString(root, "snapshotId"),
                    out Guid snapshotId) ||
                snapshotId == Guid.Empty ||
                !root.TryGetProperty(
                    "catalogRevision",
                    out JsonElement revision) ||
                !revision.TryGetUInt64(out ulong catalogRevision) ||
                catalogRevision == 0 ||
                !root.TryGetProperty(
                    "mutationRevision",
                    out JsonElement mutation) ||
                !mutation.TryGetUInt64(out ulong mutationRevision) ||
                !root.TryGetProperty(
                    "requiredMutationRevision",
                    out JsonElement requiredMutation) ||
                !requiredMutation.TryGetUInt64(
                    out ulong requiredMutationRevision) ||
                mutationRevision < requiredMutationRevision)
                throw InvalidOutput();
            string checkpointId = RequiredString(root, "checkpointId");
            string receiptHash = RequiredString(root, "receiptHash");
            if (!Sha256.IsMatch(checkpointId) ||
                !Sha256.IsMatch(receiptHash) ||
                !DateTimeOffset.TryParse(
                    RequiredString(root, "verifiedAt"),
                    CultureInfo.InvariantCulture,
                    DateTimeStyles.RoundtripKind,
                    out DateTimeOffset verifiedAt))
                throw InvalidOutput();
            string? activity = root.GetProperty("activityRoot").ValueKind switch
            {
                JsonValueKind.Null => null,
                JsonValueKind.String => root.GetProperty("activityRoot").GetString(),
                _ => throw InvalidOutput(),
            };
            if ((expectedActivityRoot is null && activity is not null) ||
                (expectedActivityRoot is not null &&
                 (string.IsNullOrWhiteSpace(activity) ||
                  !string.Equals(
                      Path.GetFullPath(activity),
                      Path.GetFullPath(expectedActivityRoot),
                      StringComparison.OrdinalIgnoreCase))))
                throw InvalidOutput();
            return new WorkspaceReplicaReceipt(
                expectedOperation,
                workspaceId,
                replicaId,
                snapshotId,
                catalogRevision,
                checkpointId,
                receiptHash,
                verifiedAt,
                activity,
                mutationRevision,
                requiredMutationRevision);
        }
        catch (WorkspaceRegistryException)
        {
            throw;
        }
        catch (Exception exception) when (
            exception is JsonException
                or FormatException
                or ArgumentException
                or InvalidOperationException)
        {
            throw InvalidOutput();
        }
    }

    private static void RequireMirrored(WorkspaceRegistryEntryV2 workspace)
    {
        ArgumentNullException.ThrowIfNull(workspace);
        if (string.IsNullOrWhiteSpace(workspace.ActivityRoot))
            throw new WorkspaceRegistryException(
                "workspace.activity_root_required",
                "Mirrored workspaces require a local activity root.");
        WorkspaceManifestV2 selected =
            WorkspaceLayout.ReadManifest(workspace.SelectedRoot);
        if (selected.WorkspaceId != workspace.WorkspaceId ||
            selected.StorageMode != WorkspaceStorageMode.Mirrored)
            throw new WorkspaceRegistryException(
                "workspace.identity_mismatch",
                "Selected path is not the expected mirrored workspace.");
    }

    private static bool HasRecoveredLayout(string root)
    {
        WorkspacePaths paths = WorkspaceLayout.Paths(root);
        // Workspace settings are applied into workspace-v2.db. The
        // settings.json object is recovery input, not a durable live-layout
        // file, so a freshly initialized healthy replica does not contain it.
        return Directory.Exists(paths.Data) &&
            Directory.Exists(paths.Topology) &&
            Directory.Exists(paths.Objects) &&
            Directory.Exists(paths.Audit) &&
            Directory.Exists(paths.Snapshots) &&
            Directory.Exists(paths.Coordination) &&
            Directory.Exists(paths.Files) &&
            File.Exists(Path.Combine(paths.Data, "data.db")) &&
            File.Exists(Path.Combine(
                paths.Coordination,
                "write-coordinator.db"));
    }

    private static void EnsureRecoveredLayout(string root)
    {
        if (!HasRecoveredLayout(root))
            throw new WorkspaceRegistryException(
                "replica.recovery_install_failed",
                "Recovered activity root is incomplete.");
    }

    private static void EnsureEmptyOrMissing(string root)
    {
        if (Directory.Exists(root) &&
            Directory.EnumerateFileSystemEntries(root).Any())
            throw RecoveryTargetInvalid();
        if (File.Exists(root))
            throw RecoveryTargetInvalid();
    }

    private static void TryDeleteOwnedRecoveryStaging(
        string staging,
        Guid workspaceId)
    {
        if (!Directory.Exists(staging))
            return;
        try
        {
            WorkspaceManifestV2 manifest =
                WorkspaceLayout.ReadManifest(staging);
            if (manifest.WorkspaceId != workspaceId ||
                manifest.StorageMode != WorkspaceStorageMode.Mirrored)
                return;
            WorkspaceLayout.DeleteWorkspaceRoot(staging, workspaceId);
        }
        catch (Exception exception) when (
            exception is IOException
                or UnauthorizedAccessException
                or WorkspaceRegistryException)
        {
            // A staging directory without a valid, same-workspace mirrored
            // manifest is not proven to be ours. Preserve it for diagnosis
            // instead of risking an over-broad recursive delete.
        }
    }

    private static string RequiredString(JsonElement root, string name)
        => root.TryGetProperty(name, out JsonElement value)
            && value.ValueKind == JsonValueKind.String
            && !string.IsNullOrWhiteSpace(value.GetString())
                ? value.GetString()!
                : throw InvalidOutput();

    private static WorkspaceRegistryException InvalidOutput()
        => new(
            "workspace.replica_response_invalid",
            "The bundled Sidecar returned an invalid replica receipt.");

    private static WorkspaceRegistryException RecoveryTargetInvalid()
        => new(
            "replica.recovery_target_invalid",
            "The local activity recovery target must be new or empty.");
}

public sealed class WorkspaceReplicaPreOpenHook(
    WorkspaceReplicaRecoveryService replicas,
    WorkspaceRepositoryOnboardingService onboarding,
    IWorkspaceRepositoryRecoveryUi recoveryUi) : IWorkspacePreOpenHook
{
    private readonly WorkspaceReplicaRecoveryService _replicas =
        replicas ?? throw new ArgumentNullException(nameof(replicas));
    private readonly WorkspaceKeyRotationPreOpenHook _keys =
        new(onboarding, recoveryUi);

    public async Task PrepareAsync(
        WorkspaceRegistryEntryV2 workspace,
        CancellationToken cancellationToken)
    {
        if (!string.IsNullOrWhiteSpace(workspace.ActivityRoot) &&
            _replicas.RequiresRecovery(workspace))
            _ = await _replicas.RecoverAndPublishAsync(
                workspace,
                cancellationToken).ConfigureAwait(false);
        await _keys.PrepareAsync(workspace, cancellationToken)
            .ConfigureAwait(false);
    }
}
