using System.IO;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Production protection boundary. A writable close/switch must complete a
/// Sidecar-owned foreground snapshot before ingress and coordinator drain.
/// </summary>
public sealed class SidecarWorkspaceProtectionHook :
    IWorkspaceProtectionReceiptHook
{
    private readonly ProductionWorkspaceRuntimeFactory _runtime;
    private readonly Func<Guid, ulong, ulong> _reserveSequence;

    public SidecarWorkspaceProtectionHook(
        ProductionWorkspaceRuntimeFactory runtime,
        Func<Guid, ulong, ulong> reserveSequence)
    {
        _runtime = runtime ?? throw new ArgumentNullException(nameof(runtime));
        _reserveSequence = reserveSequence
            ?? throw new ArgumentNullException(nameof(reserveSequence));
    }

    public async Task ProtectAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        string reason,
        CancellationToken cancellationToken)
        => _ = await CaptureProtectionAsync(
            workspaceId,
            sessionEpoch,
            reason,
        cancellationToken).ConfigureAwait(false);

    public Task<ProtectionSnapshotReceipt> CaptureAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        string reason,
        CancellationToken cancellationToken)
        => CaptureProtectionAsync(
            workspaceId,
            sessionEpoch,
            reason,
            cancellationToken);

    public async Task<ulong> ProtectAndSynchronizeAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        string reason,
        CancellationToken cancellationToken)
    {
        ProtectionSnapshotReceipt protection =
            await CaptureProtectionAsync(
                workspaceId,
                sessionEpoch,
                reason,
                cancellationToken).ConfigureAwait(false);
        WorkspaceV2SidecarCapabilities capabilities =
            RequiredCapabilities(workspaceId, sessionEpoch);
        if (!capabilities.RpcMethods.Contains(
                "replica.synchronize",
                StringComparer.Ordinal) ||
            !capabilities.RpcMethods.Contains(
                "replica.status",
                StringComparer.Ordinal))
            throw new WorkspaceRegistryException(
                "workspace.replica_sync_unavailable",
                "The active Sidecar cannot synchronize the mirrored replica.");

        Guid synchronizeOperation = Guid.NewGuid();
        JsonElement synchronize = await ForwardAsync(
            workspaceId,
            sessionEpoch,
            synchronizeOperation,
            "replica.synchronize",
            JsonSerializer.SerializeToElement(new { }),
            cancellationToken).ConfigureAwait(false);
        EnsureOperationState(
            synchronize,
            synchronizeOperation,
            "queued",
            "workspace.replica_sync_response_invalid");

        while (true)
        {
            cancellationToken.ThrowIfCancellationRequested();
            Guid statusOperation = Guid.NewGuid();
            JsonElement status = await ForwardAsync(
                workspaceId,
                sessionEpoch,
                statusOperation,
                "replica.status",
                JsonSerializer.SerializeToElement(new { }),
                cancellationToken).ConfigureAwait(false);
            if (status.ValueKind != JsonValueKind.Object ||
                status.EnumerateObject().Count() != 3 ||
                !status.TryGetProperty(
                    "coordinationStrength",
                    out JsonElement strength) ||
                strength.ValueKind != JsonValueKind.String ||
                strength.GetString() is not ("strong" or "advisory") ||
                !status.TryGetProperty(
                    "syncState",
                    out JsonElement state) ||
                state.ValueKind != JsonValueKind.String ||
                !status.TryGetProperty(
                    "pendingSync",
                    out JsonElement pending) ||
                pending.ValueKind is not (
                    JsonValueKind.True or JsonValueKind.False))
                throw new WorkspaceRegistryException(
                    "workspace.replica_status_response_invalid",
                    "The Sidecar returned an invalid replica status.");
            if (!pending.GetBoolean())
            {
                if (state.GetString() != "replicated")
                    throw new WorkspaceRegistryException(
                        "workspace.replica_sync_failed",
                        "The replica did not reach a verified replicated state.");
                return protection.MutationRevision;
            }
            await Task.Delay(
                TimeSpan.FromMilliseconds(250),
                cancellationToken).ConfigureAwait(false);
        }
    }

    private async Task<ProtectionSnapshotReceipt> CaptureProtectionAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        string reason,
        CancellationToken cancellationToken)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(reason);
        WorkspaceV2HttpGateway gateway =
            _runtime.CurrentV2Gateway
            ?? throw new WorkspaceRegistryException(
                "workspace.protection_unavailable",
                "The active Sidecar cannot create a protection snapshot.");
        WorkspaceV2SidecarCapabilities capabilities =
            RequiredCapabilities(workspaceId, sessionEpoch);
        if (_runtime.CurrentWorkspace?.WorkspaceId != workspaceId ||
            !capabilities.RpcMethods.Contains(
                "snapshot.request",
                StringComparer.Ordinal))
            throw new WorkspaceRegistryException(
                "workspace.protection_identity_mismatch",
                "Protection snapshot identity does not match the active session.");

        Guid operationId = Guid.NewGuid();
        ulong sequence = _reserveSequence(workspaceId, sessionEpoch);
        JsonElement wire = JsonSerializer.SerializeToElement(new
        {
            scope = "workspace",
            operationId = operationId.ToString("D"),
            sequence,
            workspaceId = workspaceId.ToString("D"),
            sessionEpoch,
        });
        JsonElement parameters = JsonSerializer.SerializeToElement(new
        {
            trigger = "protection",
            urgency = "foreground",
        });
        WorkspaceV2ForwardResult response = await gateway.ForwardAsync(
            $"desktop-protection-{operationId:N}",
            "snapshot.request",
            wire,
            parameters,
            pathGrant: null,
            cancellationToken).ConfigureAwait(false);
        if (response.Error is not null)
            throw new WorkspaceRegistryException(
                response.Error.Code,
                "The Sidecar rejected the protection snapshot.");
        if (response.Result is not JsonElement result)
            throw InvalidProtectionResponse();
        return EnsureProtectionCompleted(result, operationId);
    }

    internal static ProtectionSnapshotReceipt EnsureProtectionCompleted(
        JsonElement result,
        Guid operationId)
    {
        if (
            result.ValueKind != JsonValueKind.Object ||
            !result.TryGetProperty(
                "operationId",
                out JsonElement resultOperation) ||
            resultOperation.ValueKind != JsonValueKind.String ||
            resultOperation.GetString() != operationId.ToString("D") ||
            !result.TryGetProperty("state", out JsonElement state) ||
            state.ValueKind != JsonValueKind.String ||
            state.GetString() != "ready" ||
            !result.TryGetProperty("snapshotId", out JsonElement snapshot) ||
            snapshot.ValueKind != JsonValueKind.String ||
            !Guid.TryParse(snapshot.GetString(), out Guid snapshotId) ||
            snapshotId == Guid.Empty ||
            !result.TryGetProperty(
                "mutationRevision",
                out JsonElement revision) ||
            !revision.TryGetUInt64(out ulong mutationRevision) ||
            result.EnumerateObject().Count() != 4)
            throw InvalidProtectionResponse();
        return new ProtectionSnapshotReceipt(
            snapshotId,
            mutationRevision);
    }

    private WorkspaceV2SidecarCapabilities RequiredCapabilities(
        Guid workspaceId,
        ulong sessionEpoch)
    {
        WorkspaceV2SidecarCapabilities capabilities =
            _runtime.CurrentCapabilities
            ?? throw new WorkspaceRegistryException(
                "workspace.protection_unavailable",
                "The active Sidecar capabilities are unavailable.");
        if (capabilities.WorkspaceId !=
                workspaceId.ToString("D").ToLowerInvariant() ||
            capabilities.SessionEpoch != sessionEpoch)
            throw new WorkspaceRegistryException(
                "workspace.protection_identity_mismatch",
                "Protection snapshot identity does not match the active session.");
        return capabilities;
    }

    private async Task<JsonElement> ForwardAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        Guid operationId,
        string method,
        JsonElement parameters,
        CancellationToken cancellationToken)
    {
        WorkspaceV2HttpGateway gateway =
            _runtime.CurrentV2Gateway
            ?? throw new WorkspaceRegistryException(
                "workspace.protection_unavailable",
                "The active Sidecar gateway is unavailable.");
        JsonElement wire = JsonSerializer.SerializeToElement(new
        {
            scope = "workspace",
            operationId = operationId.ToString("D"),
            sequence = _reserveSequence(workspaceId, sessionEpoch),
            workspaceId = workspaceId.ToString("D"),
            sessionEpoch,
        });
        WorkspaceV2ForwardResult response = await gateway.ForwardAsync(
            $"desktop-{method.Replace('.', '-')}-{operationId:N}",
            method,
            wire,
            parameters,
            pathGrant: null,
            cancellationToken).ConfigureAwait(false);
        if (response.Error is not null)
            throw new WorkspaceRegistryException(
                response.Error.Code,
                $"The Sidecar rejected {method}.");
        return response.Result
            ?? throw new WorkspaceRegistryException(
                "workspace.replica_response_invalid",
                $"The Sidecar returned no result for {method}.");
    }

    private static void EnsureOperationState(
        JsonElement result,
        Guid operationId,
        string expectedState,
        string errorCode)
    {
        if (result.ValueKind != JsonValueKind.Object ||
            result.EnumerateObject().Count() != 2 ||
            !result.TryGetProperty(
                "operationId",
                out JsonElement responseOperation) ||
            responseOperation.ValueKind != JsonValueKind.String ||
            responseOperation.GetString() != operationId.ToString("D") ||
            !result.TryGetProperty("state", out JsonElement state) ||
            state.ValueKind != JsonValueKind.String ||
            state.GetString() != expectedState)
            throw new WorkspaceRegistryException(
                errorCode,
                "The Sidecar returned an invalid operation receipt.");
    }

    private static WorkspaceRegistryException InvalidProtectionResponse()
        => new(
            "workspace.protection_response_invalid",
            "The Sidecar returned an invalid protection snapshot response.");
}

public sealed record ProtectionSnapshotReceipt(
    Guid SnapshotId,
    ulong MutationRevision);

/// <summary>
/// Production strong/advisory lease policy. Writable strong workspaces and
/// mirrored activity roots hold an exclusive local coordination file handle
/// for the complete session lifetime, including across Desktop processes.
/// Advisory remote semantics are still explicitly downgraded to provisional
/// and are never presented as strongly coordinated.
/// </summary>
public sealed class WorkspaceCoordinationLeaseHook :
    IWorkspaceLeaseHook,
    IDisposable
{
    private const string LeaseFileName = "desktop-writer.lock";
    private readonly object _gate = new();
    private readonly Dictionary<Guid, FileStream> _held = [];
    private bool _disposed;

    public Task<WorkspaceOpenMode> AcquireAsync(
        WorkspaceRegistryEntryV2 workspace,
        WorkspaceOpenMode requestedMode,
        CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(workspace);
        cancellationToken.ThrowIfCancellationRequested();
        if (requestedMode != WorkspaceOpenMode.Writable)
            return Task.FromResult(requestedMode);
        // A separate activity root is the device-local marker for mirrored
        // storage. Its remote replica remains advisory, but the local activity
        // database must still have one writer so maintenance can fence every
        // process before independently verifying and deleting the cache.
        bool mirrored = !string.IsNullOrWhiteSpace(workspace.ActivityRoot);
        bool provisional = mirrored ||
            workspace.CoordinationStrength ==
                WorkspaceCoordinationStrength.Advisory;
        if (provisional && !mirrored)
            return Task.FromResult(WorkspaceOpenMode.Provisional);

        string runtimeRoot =
            ProductionWorkspaceRuntimeFactory.RuntimeRoot(workspace);
        WorkspaceStorageMaintenanceLease.EnsureNoIntent(runtimeRoot);
        string coordination = WorkspaceLayout.Paths(runtimeRoot).Coordination;
        string leasePath = Path.Combine(coordination, LeaseFileName);
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            if (_held.ContainsKey(workspace.WorkspaceId))
                throw new WorkspaceRegistryException(
                    "workspace.lease_conflict",
                    "This process already holds a lease for the workspace.");
            try
            {
                Directory.CreateDirectory(coordination);
                FileStream acquired = new(
                        leasePath,
                        FileMode.OpenOrCreate,
                        FileAccess.ReadWrite,
                        FileShare.None,
                        bufferSize: 1,
                        FileOptions.WriteThrough);
                try
                {
                    // A maintenance intent may have been published after the
                    // first check but before this process obtained the writer
                    // lock. Never let that race create a writer during copy.
                    WorkspaceStorageMaintenanceLease.EnsureNoIntent(
                        runtimeRoot);
                    _held.Add(workspace.WorkspaceId, acquired);
                }
                catch
                {
                    acquired.Dispose();
                    throw;
                }
            }
            catch (IOException)
            {
                throw new WorkspaceRegistryException(
                    "workspace.lease_conflict",
                    "Another Desktop process already holds the workspace writer lease.");
            }
            catch (UnauthorizedAccessException exception)
            {
                throw new WorkspaceRegistryException(
                    "workspace.lease_unavailable",
                    "The workspace coordination lease cannot be opened.",
                    exception);
            }
        }
        return Task.FromResult(
            provisional
                ? WorkspaceOpenMode.Provisional
                : WorkspaceOpenMode.Writable);
    }

    public Task ReleaseAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken)
    {
        FileStream? lease = null;
        lock (_gate)
        {
            if (_held.Remove(workspaceId, out FileStream? held))
                lease = held;
        }
        lease?.Dispose();
        return Task.CompletedTask;
    }

    public void Dispose()
    {
        FileStream[] leases;
        lock (_gate)
        {
            if (_disposed)
                return;
            _disposed = true;
            leases = [.. _held.Values];
            _held.Clear();
        }
        foreach (FileStream lease in leases)
            lease.Dispose();
    }
}
