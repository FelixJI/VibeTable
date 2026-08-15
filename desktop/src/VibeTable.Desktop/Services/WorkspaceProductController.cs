using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

public interface IWorkspaceProductReplySink
{
    void PostNotification(string type, object? payload);

    void PostWorkspaceV2Response(
        string? requestId,
        object payload,
        JsonElement wire);

    void PostWorkspaceV2Event(object payload, JsonElement wire);
}

public interface IWorkspaceProductHost
{
    bool IsRendererReady { get; }

    bool IsClosing { get; }

    bool HasDocumentWorkspace { get; }

    void Schedule(Action action);

    void OpenProductWorkspaceWhenReady();

    void WriteError(string message);
}

/// <summary>
/// The session/runtime seam needed by the workspace product state machine.
/// It deliberately hides the much larger runtime factory, session manager,
/// and epoch filter interfaces from both the controller and its callers.
/// </summary>
public interface IWorkspaceProductSessionPort
{
    WorkspaceSessionV2 CurrentSession { get; }

    WorkspaceRegistryEntryV2? CurrentWorkspace { get; }

    WorkspaceV2HttpGateway? CurrentGateway { get; }

    WorkspaceV2SidecarCapabilities? CurrentCapabilities { get; }

    bool TryCapture(
        WorkspaceWireScope? scope,
        out WorkspaceRequestEpochLease? lease);

    bool TryAdmitLifecycleRequest(WorkspaceWireScope? scope);

    bool IsCurrent(WorkspaceRequestEpochLease? lease);

    ulong ReserveHostSequence(Guid workspaceId, ulong sessionEpoch);

    Task<WorkspaceSessionV2> OpenAsync(
        Guid workspaceId,
        WorkspaceOpenMode mode,
        bool switching,
        CancellationToken cancellationToken);

    Task<WorkspaceSessionV2> CloseAsync(
        string reason,
        CancellationToken cancellationToken);

    Task<WorkspaceSessionV2> RestartAfterRestoreAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken);

    Task<WorkspaceSessionV2> RestartAfterHostMaintenanceAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken);
}

public sealed class WorkspaceProductSessionPort(
    ProductionWorkspaceRuntimeFactory runtime,
    WorkspaceSessionManager sessions,
    WorkspaceSessionEnvelopeFilter sessionFilter) : IWorkspaceProductSessionPort
{
    private readonly ProductionWorkspaceRuntimeFactory _runtime = runtime
        ?? throw new ArgumentNullException(nameof(runtime));
    private readonly WorkspaceSessionManager _sessions = sessions
        ?? throw new ArgumentNullException(nameof(sessions));
    private readonly WorkspaceSessionEnvelopeFilter _sessionFilter = sessionFilter
        ?? throw new ArgumentNullException(nameof(sessionFilter));

    public WorkspaceSessionV2 CurrentSession => _sessions.Current;

    public WorkspaceRegistryEntryV2? CurrentWorkspace => _runtime.CurrentWorkspace;

    public WorkspaceV2HttpGateway? CurrentGateway => _runtime.CurrentV2Gateway;

    public WorkspaceV2SidecarCapabilities? CurrentCapabilities =>
        _runtime.CurrentCapabilities;

    public bool TryCapture(
        WorkspaceWireScope? scope,
        out WorkspaceRequestEpochLease? lease) =>
        _sessionFilter.TryCapture(scope, out lease);

    public bool TryAdmitLifecycleRequest(WorkspaceWireScope? scope) =>
        _sessionFilter.TryAdmitLifecycleRequest(scope);

    public bool IsCurrent(WorkspaceRequestEpochLease? lease) =>
        _sessionFilter.IsCurrent(lease);

    public ulong ReserveHostSequence(Guid workspaceId, ulong sessionEpoch) =>
        _sessionFilter.ReserveHostSequence(workspaceId, sessionEpoch);

    public Task<WorkspaceSessionV2> OpenAsync(
        Guid workspaceId,
        WorkspaceOpenMode mode,
        bool switching,
        CancellationToken cancellationToken) =>
        switching
            ? _sessions.SwitchAsync(workspaceId, mode, cancellationToken)
            : _sessions.OpenAsync(workspaceId, mode, cancellationToken);

    public Task<WorkspaceSessionV2> CloseAsync(
        string reason,
        CancellationToken cancellationToken) =>
        _sessions.CloseAsync(reason, cancellationToken);

    public Task<WorkspaceSessionV2> RestartAfterRestoreAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken) =>
        _sessions.RestartAfterRestoreAsync(
            workspaceId,
            sessionEpoch,
            cancellationToken);

    public Task<WorkspaceSessionV2> RestartAfterHostMaintenanceAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken) =>
        _sessions.RestartAfterHostMaintenanceAsync(
            workspaceId,
            sessionEpoch,
            cancellationToken);
}

/// <summary>
/// Coordinates the public workspace product seams. Registry/topology,
/// replica polling and bootstrap projection live behind dedicated modules.
/// </summary>
public sealed class WorkspaceProductController : IAsyncDisposable
{
    private readonly IWorkspaceProductReplySink _reply;
    private readonly IWorkspaceProductHost _host;
    private readonly IWorkspaceProductSessionPort _session;
    private readonly IWorkspaceRegistryTopologyController _registryTopology;
    private readonly IWorkspaceReplicaStatusController _replicaStatus;
    private readonly IWorkspaceBootstrapPublisher _bootstrap;
    private readonly WorkspacePathGrantStore _workspacePathGrants;
    private readonly SnapshotPackageBroker _snapshotPackages;
    private readonly WorkspaceStorageBroker _workspaceStorage;
    private readonly Func<CancellationToken> _sessionToken;

    public WorkspaceProductController(
        IWorkspaceProductReplySink reply,
        IWorkspaceProductHost host,
        IWorkspaceProductSessionPort session,
        IWorkspaceRegistryTopologyController registryTopology,
        IWorkspaceReplicaStatusController replicaStatus,
        IWorkspaceBootstrapPublisher bootstrap,
        WorkspacePathGrantStore workspacePathGrants,
        SnapshotPackageBroker snapshotPackages,
        WorkspaceStorageBroker workspaceStorage,
        Func<CancellationToken>? sessionToken = null)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _host = host ?? throw new ArgumentNullException(nameof(host));
        _session = session ?? throw new ArgumentNullException(nameof(session));
        _registryTopology = registryTopology
            ?? throw new ArgumentNullException(nameof(registryTopology));
        _replicaStatus = replicaStatus
            ?? throw new ArgumentNullException(nameof(replicaStatus));
        _bootstrap = bootstrap
            ?? throw new ArgumentNullException(nameof(bootstrap));
        _workspacePathGrants = workspacePathGrants
            ?? throw new ArgumentNullException(nameof(workspacePathGrants));
        _snapshotPackages = snapshotPackages
            ?? throw new ArgumentNullException(nameof(snapshotPackages));
        _workspaceStorage = workspaceStorage
            ?? throw new ArgumentNullException(nameof(workspaceStorage));
        _sessionToken = sessionToken ?? (() => CancellationToken.None);
    }

    public async Task DispatchAsync(RoutedWebRequest request)
    {
        WorkspaceRequestEpochLease? epochLease = null;
        try
        {
            bool lifecycleRequest = request.V2Method is
                "workspace.switch" or "workspace.close";
            bool admitted = request.Scope is null ||
                (lifecycleRequest
                    ? _session.TryAdmitLifecycleRequest(
                        request.Scope)
                    : _session.TryCapture(
                        request.Scope,
                        out epochLease));
            if (!admitted)
            {
                throw new WorkspaceRegistryException(
                    "workspace.session_stale",
                    "The workspace request does not belong to the active session.");
            }
            using CancellationTokenSource? requestCancellation =
                epochLease is null
                    ? null
                    : CancellationTokenSource.CreateLinkedTokenSource(
                        _sessionToken(),
                        epochLease.CancellationToken);
            CancellationToken requestToken =
                requestCancellation?.Token ?? _sessionToken();
            JsonElement parameters = request.Payload.TryGetProperty(
                "params", out JsonElement value)
                    ? value
                    : default;
            Guid operationId = ReadOperationId(request.Wire);
            object result;
            switch (request.V2Method)
            {
                case "workspace.list":
                case "workspace.create":
                case "workspace.register":
                case "workspace.relink":
                case "workspace.remove":
                case "workspace.planDelete":
                case "workspace.applyDelete":
                    WorkspaceRegistryDispatchResult registryResult =
                        await _registryTopology.DispatchAsync(
                            request.V2Method,
                            parameters,
                            operationId,
                            requestToken);
                    result = registryResult.Result;
                    if (registryResult.BootstrapChanged)
                        PostBootstrap();
                    break;
                case "workspace.open":
                    result = ToSessionResult(
                        await OpenAsync(
                            ReadRequiredGuid(parameters, "workspaceId"),
                            ReadOpenMode(parameters),
                            switching: false,
                            requestToken));
                    break;
                case "workspace.switch":
                    result = ToSessionResult(
                        await OpenAsync(
                            ReadRequiredGuid(parameters, "targetWorkspaceId"),
                            ReadOpenMode(parameters),
                            switching: true,
                            requestToken));
                    break;
                case "workspace.close":
                    result = ToSessionResult(
                        await _session.CloseAsync(
                            ReadString(parameters, "reason") ?? "user",
                            requestToken));
                    break;
                case "snapshot.inspectPackage":
                    {
                        JsonElement packageParameters =
                            _workspacePathGrants.MaterializeSentinels(
                                request.V2Method!,
                                operationId,
                                parameters);
                        WorkspaceSidecarPathGrant sourceGrant =
                            _workspacePathGrants.ConsumeForSidecar(
                                packageParameters,
                                request.V2Method!,
                                operationId)
                            ?? throw new WorkspacePathGrantException(
                                "workspace.path_grant_invalid",
                                "Snapshot package inspection requires a native source grant.");
                        result = await _snapshotPackages.InspectAsync(
                            request.RequestId ?? operationId.ToString("D"),
                            request.Wire,
                            packageParameters,
                            sourceGrant,
                            requestToken);
                        break;
                    }
                case "snapshot.import":
                    result = await _snapshotPackages.ImportAsync(
                        request.RequestId ?? operationId.ToString("D"),
                        request.Wire,
                        parameters,
                        requestToken);
                    break;
                case "snapshot.openAsNewWorkspace":
                    {
                        WorkspaceV2HttpGateway sourceGateway =
                            _session.CurrentGateway
                            ?? throw new WorkspaceRegistryException(
                                "workspace.session_required",
                                "Opening a snapshot as a new workspace requires an active source workspace.");
                        if (_session.CurrentCapabilities?.RpcMethods.Contains(
                                "snapshot.export",
                                StringComparer.Ordinal) != true)
                        {
                            throw new WorkspaceRegistryException(
                                "workspace.capability_unavailable",
                                "The source runtime cannot export this snapshot.");
                        }
                        SnapshotOpenAsNewPlan plan =
                            await _snapshotPackages.StageOpenAsNewAsync(
                                sourceGateway,
                                request.RequestId ?? operationId.ToString("D"),
                                request.Wire,
                                parameters,
                                requestToken);
                        // The package is now staged independently of the source
                        // epoch. Release this request before switching sessions so
                        // the source drain cannot wait on its own broker request.
                        epochLease?.Dispose();
                        epochLease = null;
                        result = ToSessionResult(
                            await _snapshotPackages.CompleteOpenAsNewAsync(
                                plan,
                                request.RequestId ?? operationId.ToString("D"),
                                _sessionToken()));
                        break;
                    }
                case "workspace.storage.preview":
                    {
                        string? action = ReadString(parameters, "action");
                        string? selectedRoot = null;
                        JsonElement storageParameters = parameters;
                        if (action is "relocate" or "convertTopology")
                        {
                            storageParameters =
                                _workspacePathGrants.MaterializeSentinels(
                                    request.V2Method!,
                                    operationId,
                                    parameters);
                            string grantId =
                                ReadString(
                                    storageParameters,
                                    "selectedRootGrant")
                                ?? throw new WorkspacePathGrantException(
                                    "workspace.path_grant_invalid",
                                    "Storage relocation requires a native target grant.");
                            selectedRoot = _workspacePathGrants.Consume(
                                grantId,
                                request.V2Method!,
                                operationId,
                                "workspace-root");
                        }
                        result = await _workspaceStorage.PreviewAsync(
                            storageParameters,
                            selectedRoot,
                            requestToken);
                        break;
                    }
                case "workspace.storage.apply":
                    result = await _workspaceStorage.ApplyAsync(
                        parameters,
                        requestToken);
                    _host.Schedule(PostBootstrap);
                    break;
                default:
                    if (IsWorkspaceMutation(request.V2Method!)
                        && !_session.CurrentSession.Writable)
                    {
                        throw new WorkspaceRegistryException(
                            "workspace.read_only",
                            "This workspace session is read-only.");
                    }
                    WorkspaceV2HttpGateway gateway =
                        _session.CurrentGateway
                        ?? throw new WorkspaceRegistryException(
                            "workspace.session_required",
                            "This operation requires an active workspace session.");
                    if (_session.CurrentCapabilities?.RpcMethods.Contains(
                            request.V2Method!,
                            StringComparer.Ordinal) != true)
                    {
                        throw new WorkspaceRegistryException(
                            "workspace.capability_unavailable",
                            "This workspace v2 capability is not connected in this build.");
                    }
                    JsonElement materialized =
                        _workspacePathGrants.MaterializeSentinels(
                            request.V2Method!,
                            operationId,
                            parameters);
                    WorkspaceSidecarPathGrant? sidecarPathGrant =
                        _workspacePathGrants.ConsumeForSidecar(
                            materialized,
                            request.V2Method!,
                            operationId);
                    WorkspaceV2ForwardResult forwarded =
                        await gateway.ForwardAsync(
                            request.RequestId ?? operationId.ToString("D"),
                            request.V2Method!,
                            request.Wire,
                            materialized,
                            sidecarPathGrant,
                            requestToken);
                    if (epochLease is not null &&
                        !_session.IsCurrent(epochLease))
                        return;
                    if (forwarded.Error is not null)
                    {
                        _reply.PostWorkspaceV2Response(
                            request.RequestId,
                            new
                            {
                                method = request.V2Method,
                                wire = forwarded.Wire,
                                ok = false,
                                result = (object?)null,
                                error = new
                                {
                                    code = forwarded.Error.Code,
                                    message = forwarded.Error.Message,
                                    retryable = forwarded.Error.Retryable,
                                },
                            },
                            forwarded.Wire);
                        return;
                    }
                    _reply.PostWorkspaceV2Response(
                        request.RequestId,
                        new
                        {
                            method = request.V2Method,
                            wire = forwarded.Wire,
                            ok = true,
                            result = forwarded.Result,
                            error = (object?)null,
                        },
                        forwarded.Wire);
                    if (request.V2Method == "replica.forceTakeover" &&
                        request.Scope is not null)
                    {
                        await _replicaStatus.RefreshNowAsync(
                            request.Scope.WorkspaceId,
                            request.Scope.SessionEpoch,
                            requestToken);
                    }
                    if (request.V2Method == "snapshot.applyRestore"
                        && IsResultState(forwarded.Result, "prepared")
                        && request.Scope is { } restoreScope)
                    {
                        // The prepared response must reach the old epoch
                        // before it is rotated. Go has suspended the workspace
                        // and requested shutdown; Desktop owns the explicit
                        // stop/reopen/verify/bootstrap sequence.
                        epochLease?.Dispose();
                        epochLease = null;
                        try
                        {
                            _ = await _session.RestartAfterRestoreAsync(
                                    restoreScope.WorkspaceId,
                                    restoreScope.SessionEpoch,
                                    _sessionToken());
                            PostBootstrap();
                        }
                        catch (Exception restartError)
                        {
                            _host.WriteError(
                                $"Restored workspace restart failed: {restartError.GetType().Name}");
                            PostBootstrap();
                        }
                    }
                    else if (request.V2Method
                                 == "repository.applyKeyRotation"
                             && IsResultState(
                                 forwarded.Result,
                                 "hostRestartRequired")
                             && request.Scope is { } maintenanceScope)
                    {
                        epochLease?.Dispose();
                        epochLease = null;
                        try
                        {
                            _ = await _session.RestartAfterHostMaintenanceAsync(
                                    maintenanceScope.WorkspaceId,
                                    maintenanceScope.SessionEpoch,
                                    _sessionToken());
                            PostBootstrap();
                        }
                        catch (Exception restartError)
                        {
                            _host.WriteError(
                                $"Repository key rotation restart failed: {restartError.GetType().Name}");
                            PostBootstrap();
                        }
                    }
                    return;
            }
            _reply.PostWorkspaceV2Response(
                request.RequestId,
                new
                {
                    method = request.V2Method,
                    wire = request.Wire,
                    ok = true,
                    result,
                    error = (object?)null,
                },
                request.Wire);
        }
        catch (OperationCanceledException)
            when (epochLease?.CancellationToken.IsCancellationRequested == true)
        {
            // Draining invalidated this epoch. Never post a late response into
            // a new workspace session.
        }
        catch (Exception exception)
        {
            string code = exception is WorkspaceRegistryException registry
                ? registry.Code
                : exception is WorkspacePathGrantException grant
                    ? grant.Code
                : "workspace.operation_failed";
            _reply.PostWorkspaceV2Response(
                request.RequestId,
                new
                {
                    method = request.V2Method,
                    wire = request.Wire,
                    ok = false,
                    result = (object?)null,
                    error = new
                    {
                        code,
                        message = exception.Message,
                        retryable = false,
                    },
                },
                request.Wire);
        }
        finally
        {
            epochLease?.Dispose();
        }
    }

    public static bool Handles(string requestType) =>
        string.Equals(requestType, "workspace.v2.request", StringComparison.Ordinal);

    public Task<WorkspaceSessionV2> OpenAsync(
        Guid workspaceId,
        WorkspaceOpenMode mode,
        bool switching,
        CancellationToken cancellationToken)
        => _registryTopology.OpenAsync(
            workspaceId,
            mode,
            switching,
            cancellationToken);

    private static Guid ReadOperationId(JsonElement wire)
    {
        string? raw = ReadString(wire, "operationId");
        return Guid.TryParse(raw, out Guid operationId)
            && operationId != Guid.Empty
                ? operationId
                : throw new WorkspaceRegistryException(
                    "workspace.request_invalid",
                    "Workspace operationId is invalid.");
    }

    private static object ToSessionResult(WorkspaceSessionV2 session) => new
    {
        workspaceId = session.WorkspaceId?.ToString("D"),
        sessionEpoch = session.SessionEpoch,
        state = WorkspaceProjection.SessionStateName(session.State),
    };

    public void OnSessionChanged(WorkspaceSessionChangedEventArgs args)
    {
        _replicaStatus.Bind(args.Session);
        if (_host.IsClosing || !_host.IsRendererReady)
            return;
        _host.Schedule(() =>
        {
            if (_host.IsClosing || !_host.IsRendererReady)
                return;
            _bootstrap.Post();
            _host.OpenProductWorkspaceWhenReady();
        });
    }

    public void PostBootstrap() => _bootstrap.Post();

    internal static bool IsWorkspaceMutation(string method)
        => method is
            "snapshot.request"
            or "snapshot.update"
            or "snapshot.applyRestore"
            or "snapshot.import"
            or "history.applyRestore"
            or "repository.applyKeyRotation"
            or "fileHistory.import"
            or "fileHistory.relink"
            or "fileHistory.unlink"
            or "fileHistory.restore"
            or "fileHistory.upgrade"
            or "fileHistory.activateLeaf"
            or "fileHistory.applyPendingChange"
            or "retention.update"
            or "retention.apply"
            or "replica.forceTakeover"
            or "conflict.apply";

    private static Guid ReadRequiredGuid(JsonElement value, string name)
    {
        string? raw = ReadString(value, name);
        return Guid.TryParse(raw, out Guid parsed) && parsed != Guid.Empty
            ? parsed
            : throw new WorkspaceRegistryException(
                "workspace.request_invalid",
                $"Missing or invalid '{name}'.");
    }

    private static WorkspaceOpenMode ReadOpenMode(JsonElement value)
    {
        string? raw = ReadString(value, "openMode");
        return raw switch
        {
            "writable" => WorkspaceOpenMode.Writable,
            "readOnly" => WorkspaceOpenMode.ReadOnly,
            "provisional" => WorkspaceOpenMode.Provisional,
            _ => throw new WorkspaceRegistryException(
                "workspace.request_invalid",
                "Missing or invalid 'openMode'."),
        };
    }
    private static string? ReadString(JsonElement value, string name)
        => value.ValueKind == JsonValueKind.Object
            && value.TryGetProperty(name, out JsonElement item)
            && item.ValueKind == JsonValueKind.String
                ? item.GetString()
                : null;

    private static bool IsResultState(JsonElement? result, string expected)
        => result is { ValueKind: JsonValueKind.Object } value
            && value.TryGetProperty("state", out JsonElement state)
            && state.ValueKind == JsonValueKind.String
            && string.Equals(
                state.GetString(),
                expected,
                StringComparison.Ordinal);

    public async ValueTask DisposeAsync()
    {
        try
        {
            await _replicaStatus.DisposeAsync().ConfigureAwait(false);
        }
        finally
        {
            await _snapshotPackages.DisposeAsync().ConfigureAwait(false);
        }
    }
}

internal sealed class WorkspaceProductReplySink(
    ProductWebViewBridge bridge) : IWorkspaceProductReplySink
{
    public void PostNotification(string type, object? payload)
        => bridge.PostNotification(type, payload);

    public void PostWorkspaceV2Response(
        string? requestId,
        object payload,
        JsonElement wire)
        => bridge.PostWorkspaceV2Response(requestId, payload, wire);

    public void PostWorkspaceV2Event(object payload, JsonElement wire)
        => bridge.PostWorkspaceV2Event(payload, wire);
}

internal sealed class WorkspaceProductHost(
    Func<bool> isRendererReady,
    Func<bool> isClosing,
    Func<bool> hasDocumentWorkspace,
    Action<Action> schedule,
    Action openProductWorkspaceWhenReady,
    Action<string> writeError) : IWorkspaceProductHost
{
    public bool IsRendererReady => isRendererReady();

    public bool IsClosing => isClosing();

    public bool HasDocumentWorkspace => hasDocumentWorkspace();

    public void Schedule(Action action) => schedule(action);

    public void OpenProductWorkspaceWhenReady()
        => openProductWorkspaceWhenReady();

    public void WriteError(string message) => writeError(message);
}
