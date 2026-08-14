using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

public sealed record WorkspaceReplicaStatusEnvelope(
    WorkspaceReplicaStatus Status,
    JsonElement Wire);

public interface IWorkspaceReplicaStatusQuery
{
    Task<WorkspaceReplicaStatusEnvelope> ReadAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken);
}

public interface IWorkspaceReplicaStatusController : IAsyncDisposable
{
    void Bind(WorkspaceSessionV2 session);

    Task RefreshNowAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken);
}

/// <summary>
/// Production adapter that binds a replica.status read to one exact session
/// epoch and rejects responses from retired runtimes.
/// </summary>
public sealed class WorkspaceReplicaStatusQuery(
    IWorkspaceProductSessionPort session) : IWorkspaceReplicaStatusQuery
{
    private readonly IWorkspaceProductSessionPort _session = session
        ?? throw new ArgumentNullException(nameof(session));

    public async Task<WorkspaceReplicaStatusEnvelope> ReadAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken)
    {
        WorkspaceSessionV2 current = _session.CurrentSession;
        WorkspaceRegistryEntryV2 workspace = _session.CurrentWorkspace
            ?? throw new InvalidOperationException(
                "Replica status requires an active workspace runtime.");
        WorkspaceV2HttpGateway gateway = _session.CurrentGateway
            ?? throw new InvalidOperationException(
                "Replica status requires an active Sidecar gateway.");
        EnsureCurrent(current, workspace, workspaceId, sessionEpoch);

        Guid operationId = Guid.NewGuid();
        ulong sequence = _session.ReserveHostSequence(
            workspaceId,
            sessionEpoch);
        JsonElement wire = JsonSerializer.SerializeToElement(new
        {
            scope = "workspace",
            workspaceId = workspaceId.ToString("D"),
            sessionEpoch,
            operationId = operationId.ToString("D"),
            sequence,
        });
        WorkspaceV2ForwardResult forwarded = await gateway.ForwardAsync(
            "desktop-replica-status-" + operationId.ToString("N"),
            "replica.status",
            wire,
            JsonSerializer.SerializeToElement(new { }),
            pathGrant: null,
            cancellationToken).ConfigureAwait(false);
        if (forwarded.Error is not null)
            throw new WorkspaceRegistryException(
                forwarded.Error.Code,
                "The Sidecar could not read durable replica status.");
        WorkspaceReplicaStatus status = WorkspaceReplicaStatusMonitor.Parse(
            forwarded.Result
            ?? throw new InvalidOperationException(
                "Sidecar replica.status returned no result."));
        current = _session.CurrentSession;
        workspace = _session.CurrentWorkspace
            ?? throw new InvalidOperationException(
                "Replica status response belongs to a retired session.");
        EnsureCurrent(current, workspace, workspaceId, sessionEpoch);
        return new WorkspaceReplicaStatusEnvelope(status, forwarded.Wire);
    }

    private void EnsureCurrent(
        WorkspaceSessionV2 session,
        WorkspaceRegistryEntryV2 workspace,
        Guid workspaceId,
        ulong sessionEpoch)
    {
        if (session.WorkspaceId != workspaceId ||
            session.SessionEpoch != sessionEpoch ||
            session.State is not (
                WorkspaceSessionState.OpenedReadOnly or
                WorkspaceSessionState.OpenedWritable or
                WorkspaceSessionState.OpenedProvisional) ||
            workspace.WorkspaceId != workspaceId ||
            string.IsNullOrWhiteSpace(workspace.ActivityRoot) ||
            _session.CurrentCapabilities?.RpcMethods.Contains(
                "replica.status",
                StringComparer.Ordinal) != true)
            throw new InvalidOperationException(
                "Replica status session is no longer current.");
    }
}

/// <summary>
/// Owns session-bound replica polling, durable registry health projection and
/// renderer events behind Bind/RefreshNow.
/// </summary>
public sealed class WorkspaceReplicaStatusController :
    IWorkspaceReplicaStatusController
{
    private readonly IWorkspaceProductReplySink _reply;
    private readonly IWorkspaceProductHost _host;
    private readonly IWorkspaceProductSessionPort _session;
    private readonly WorkspaceRegistry _workspaceRegistry;
    private readonly IWorkspaceReplicaStatusQuery _query;
    private readonly IWorkspaceBootstrapPublisher _bootstrap;
    private readonly WorkspaceReplicaStatusMonitor _monitor;

    public WorkspaceReplicaStatusController(
        IWorkspaceProductReplySink reply,
        IWorkspaceProductHost host,
        IWorkspaceProductSessionPort session,
        WorkspaceRegistry workspaceRegistry,
        IWorkspaceReplicaStatusQuery query,
        IWorkspaceBootstrapPublisher bootstrap)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _host = host ?? throw new ArgumentNullException(nameof(host));
        _session = session ?? throw new ArgumentNullException(nameof(session));
        _workspaceRegistry = workspaceRegistry
            ?? throw new ArgumentNullException(nameof(workspaceRegistry));
        _query = query ?? throw new ArgumentNullException(nameof(query));
        _bootstrap = bootstrap ?? throw new ArgumentNullException(nameof(bootstrap));
        _monitor = new WorkspaceReplicaStatusMonitor(RefreshAsync);
    }

    public void Bind(WorkspaceSessionV2 session)
    {
        bool opened = session.State is
            WorkspaceSessionState.OpenedReadOnly or
            WorkspaceSessionState.OpenedWritable or
            WorkspaceSessionState.OpenedProvisional;
        WorkspaceRegistryEntryV2? workspace = _session.CurrentWorkspace;
        bool enabled =
            !_host.IsClosing &&
            opened &&
            session.WorkspaceId is Guid workspaceId &&
            workspaceId != Guid.Empty &&
            session.SessionEpoch > 0 &&
            workspace?.WorkspaceId == workspaceId &&
            !string.IsNullOrWhiteSpace(workspace.ActivityRoot) &&
            _session.CurrentCapabilities?.RpcMethods.Contains(
                "replica.status",
                StringComparer.Ordinal) == true;
        _monitor.Bind(
            enabled ? session.WorkspaceId!.Value : Guid.Empty,
            enabled ? session.SessionEpoch : 0,
            enabled);
    }

    public Task RefreshNowAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken) =>
        _monitor.RefreshNowAsync(
            workspaceId,
            sessionEpoch,
            cancellationToken);

    private async Task<WorkspaceReplicaStatus> RefreshAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken)
    {
        WorkspaceReplicaStatusEnvelope envelope = await _query.ReadAsync(
            workspaceId,
            sessionEpoch,
            cancellationToken).ConfigureAwait(false);
        WorkspaceRegistryEntryV2 current = _workspaceRegistry.List()
            .SingleOrDefault(entry => entry.WorkspaceId == workspaceId)
            ?? throw new WorkspaceRegistryException(
                "workspace.not_registered",
                "Workspace is not registered on this device.");
        WorkspaceHealthObservation observation =
            WorkspaceReplicaStatusMonitor.ProjectHealth(
                current,
                envelope.Status,
                DateTimeOffset.UtcNow);
        bool registryChanged =
            current.LastKnownHealth != observation.Health ||
            current.PendingSync != observation.PendingSync ||
            (observation.LastSyncAt is not null &&
             current.LastSyncAt != observation.LastSyncAt);
        if (registryChanged)
            _ = _workspaceRegistry.UpdateHealth(workspaceId, observation);

        _reply.PostWorkspaceV2Event(
            ReplicaChangedEvent(envelope.Status, envelope.Wire),
            envelope.Wire);
        if (registryChanged && !_host.IsClosing && _host.IsRendererReady)
            _host.Schedule(_bootstrap.Post);
        return envelope.Status;
    }

    private static object ReplicaChangedEvent(
        WorkspaceReplicaStatus status,
        JsonElement wire) => new
        {
            contractVersion = WorkspaceV2Json.ContractVersion,
            topic = "replica.changed",
            wire,
            payloadModel = "ReplicaChangedEvent",
            payloadSchema = new
            {
                type = "object",
                additionalProperties = false,
                required = new[] { "syncState", "pendingSync" },
                properties = new
                {
                    syncState = new { type = "string" },
                    pendingSync = new { type = "boolean" },
                },
            },
            payload = new
            {
                syncState = status.SyncState,
                pendingSync = status.PendingSync,
            },
        };

    public ValueTask DisposeAsync() => _monitor.DisposeAsync();
}
