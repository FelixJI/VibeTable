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
    IWorkspaceProtectionHook
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
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(reason);
        WorkspaceV2HttpGateway gateway =
            _runtime.CurrentV2Gateway
            ?? throw new WorkspaceRegistryException(
                "workspace.protection_unavailable",
                "The active Sidecar cannot create a protection snapshot.");
        WorkspaceV2SidecarCapabilities capabilities =
            _runtime.CurrentCapabilities
            ?? throw new WorkspaceRegistryException(
                "workspace.protection_unavailable",
                "The active Sidecar capabilities are unavailable.");
        if (_runtime.CurrentWorkspace?.WorkspaceId != workspaceId ||
            capabilities.WorkspaceId !=
                workspaceId.ToString("D").ToLowerInvariant() ||
            capabilities.SessionEpoch != sessionEpoch ||
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
        EnsureProtectionCompleted(result, operationId);
    }

    internal static void EnsureProtectionCompleted(
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
            result.EnumerateObject().Count() != 2)
            throw InvalidProtectionResponse();
    }

    private static WorkspaceRegistryException InvalidProtectionResponse()
        => new(
            "workspace.protection_response_invalid",
            "The Sidecar returned an invalid protection snapshot response.");
}

/// <summary>
/// Production strong/advisory lease policy. Writable strong workspaces hold
/// an exclusive coordination file handle for the complete session lifetime,
/// including across Desktop processes. Advisory roots are explicitly
/// downgraded to provisional and never presented as exclusively writable.
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
        if (workspace.CoordinationStrength ==
            WorkspaceCoordinationStrength.Advisory)
            return Task.FromResult(WorkspaceOpenMode.Provisional);

        string runtimeRoot =
            ProductionWorkspaceRuntimeFactory.RuntimeRoot(workspace);
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
                _held.Add(
                    workspace.WorkspaceId,
                    new FileStream(
                        leasePath,
                        FileMode.OpenOrCreate,
                        FileAccess.ReadWrite,
                        FileShare.None,
                        bufferSize: 1,
                        FileOptions.WriteThrough));
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
        return Task.FromResult(WorkspaceOpenMode.Writable);
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
