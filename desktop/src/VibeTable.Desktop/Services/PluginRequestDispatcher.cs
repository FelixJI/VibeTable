using System;
using System.Diagnostics;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Diagnostics;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Maps the closed WebView plugin message set to aggregate RPC use cases.
/// Surface messages terminate here and never become arbitrary Python calls.
/// </summary>
public sealed class PluginRequestDispatcher : IDisposable
{
    public const string HostManagedSource = "host-managed";

    private static readonly JsonSerializerOptions JsonOptions =
        new(JsonSerializerDefaults.Web);

    private readonly IWebReplySink _reply;
    private readonly PluginSurfaceSessionManager _surfaces;
    private readonly IPluginPackageSourcePicker _packagePicker;
    private readonly PluginWebViewResourceHost _resourceHost;
    private readonly IPluginFilePicker? _filePicker;
    private readonly IGitHubPluginPackageSource? _githubSource;
    private readonly Action<string>? _diagnosticTrace;
    private readonly Func<PluginProjectContext?> _projectContext;
    private readonly object _gatewayGate = new();
    private readonly HostInstallPlanLeaseRegistry _installLeases;
    private readonly ProductAuthorityEpoch _authority;
    private readonly bool _ownsAuthority;
    private IPluginRpcGateway? _gateway;
    private bool _disposed;

    public PluginRequestDispatcher(
        IWebReplySink reply,
        PluginSurfaceSessionManager surfaces,
        IPluginPackageSourcePicker packagePicker,
        PluginWebViewResourceHost resourceHost,
        IPluginFilePicker? filePicker = null,
        Func<PluginProjectContext?>? projectContext = null,
        ProductAuthorityEpoch? authority = null,
        TimeSpan? cleanupTimeout = null,
        TimeProvider? cleanupTimeProvider = null)
        : this(
            reply,
            surfaces,
            packagePicker,
            resourceHost,
            filePicker,
            null,
            null,
            projectContext,
            authority,
            cleanupTimeout,
            cleanupTimeProvider)
    {
    }

    internal PluginRequestDispatcher(
        IWebReplySink reply,
        PluginSurfaceSessionManager surfaces,
        IPluginPackageSourcePicker packagePicker,
        PluginWebViewResourceHost resourceHost,
        IPluginFilePicker? filePicker,
        IGitHubPluginPackageSource? githubSource,
        Action<string>? diagnosticTrace = null,
        Func<PluginProjectContext?>? projectContext = null,
        ProductAuthorityEpoch? authority = null,
        TimeSpan? cleanupTimeout = null,
        TimeProvider? cleanupTimeProvider = null)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _surfaces = surfaces ?? throw new ArgumentNullException(nameof(surfaces));
        _packagePicker = packagePicker ?? throw new ArgumentNullException(nameof(packagePicker));
        _resourceHost = resourceHost ?? throw new ArgumentNullException(nameof(resourceHost));
        _filePicker = filePicker;
        _githubSource = githubSource;
        _diagnosticTrace = diagnosticTrace;
        _projectContext = projectContext ?? (() => null);
        _authority = authority ?? new ProductAuthorityEpoch();
        _ownsAuthority = authority is null;
        _installLeases = new HostInstallPlanLeaseRegistry(
            _authority,
            cleanupTimeout,
            cleanupTimeProvider,
            cleanupTrace: TraceCleanupFailure);
    }

    public event Action<PluginSurfaceEvent>? SurfaceEventReceived;

    public static bool Handles(string requestType) =>
        requestType.StartsWith("plugin.", StringComparison.Ordinal);

    public bool HasGateway
    {
        get { lock (_gatewayGate) return _gateway is not null; }
    }

    public void SetGateway(IPluginRpcGateway gateway)
    {
        PluginProjectContext? context = _projectContext();
        _authority.Transition(context);
        SetGatewayAfterAuthorityTransition(gateway, context);
    }

    internal void SetGatewayAfterAuthorityTransition(
        IPluginRpcGateway gateway,
        PluginProjectContext? context)
    {
        ArgumentNullException.ThrowIfNull(gateway);
        DetachGateway();
        lock (_gatewayGate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            _gateway = gateway;
            gateway.CatalogChanged += OnCatalogChanged;
            gateway.TaskChanged += OnTaskChanged;
            gateway.InteractionRequested += OnInteractionRequested;
            gateway.FileRequested += OnFileRequested;
        }
        ReleaseLeases(_installLeases.SetGatewayAfterAuthorityTransition(gateway, context));
    }

    public void ClearGateway(IPluginRpcGateway gateway)
    {
        ArgumentNullException.ThrowIfNull(gateway);
        _authority.Transition(null);
        ClearGatewayAfterAuthorityTransition(gateway);
    }

    internal void ClearGatewayAfterAuthorityTransition(IPluginRpcGateway gateway)
    {
        ArgumentNullException.ThrowIfNull(gateway);
        DetachGateway(gateway);
    }

    public void SetProjectContext(PluginProjectContext? context)
    {
        _authority.Transition(context);
        SetProjectContextAfterAuthorityTransition(context);
    }

    internal void SetProjectContextAfterAuthorityTransition(PluginProjectContext? context) =>
        ReleaseLeases(_installLeases.SetContextAfterAuthorityTransition(context));

    public void InvalidateProjectContext() => SetProjectContext(null);

    public void Dispatch(RoutedWebRequest request)
        => _ = DispatchAsync(request);

    public async Task DispatchAsync(
        RoutedWebRequest request,
        CancellationToken token = default)
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        ArgumentNullException.ThrowIfNull(request);
        try
        {
            if (string.Equals(request.Type, "plugin.surface.event", StringComparison.Ordinal))
            {
                DispatchSurfaceEvent(request);
                return;
            }
            IPluginRpcGateway? gateway = CaptureGatewayOrNull();
            if (gateway is null)
            {
                _reply.PostOperationFailed(
                    request.RequestId,
                    "Plugin services are not available for the current project.",
                    "PLUGIN_NOT_READY");
                return;
            }
            if (string.Equals(request.Type, "plugin.install.commit", StringComparison.Ordinal))
            {
                await CommitInstallAsync(
                    request,
                    Read<PluginCommitInstallParams>(request.Payload),
                    token).ConfigureAwait(false);
                return;
            }
            if (string.Equals(request.Type, "plugin.lifecycle.upgrade", StringComparison.Ordinal))
            {
                await UpgradeAsync(
                    request,
                    Read<PluginUpgradeParams>(request.Payload),
                    token).ConfigureAwait(false);
                return;
            }

            object result = request.Type switch
            {
                "plugin.catalog.list" => await ListCatalogAsync(
                    Read<PluginCatalogListParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.audit.list" => await gateway.ListAuditAsync(
                    Read<PluginAuditListParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.cleanup.listPending" => await gateway.ListPendingCleanupAsync(
                    Read<PluginCatalogListParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.install.inspect" => await InspectInstallAsync(
                    Read<PluginInspectInstallParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.install.github.inspect" => await InspectGitHubInstallAsync(
                    Read<PluginGitHubInspectParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.install.cancel" => await CancelInstallAsync(
                    Read<PluginInstallCancelParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.lifecycle.setEnabled" => ProjectSnapshot(await gateway.SetEnabledAsync(
                    Read<PluginSetEnabledParams>(request.Payload), token).ConfigureAwait(false)),
                "plugin.lifecycle.rollback" => ProjectSnapshot(await gateway.RollbackAsync(
                    Read<PluginRollbackParams>(request.Payload), token).ConfigureAwait(false)),
                "plugin.lifecycle.uninstall" => await UninstallAsync(
                    Read<PluginUninstallParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.action.describe" => await gateway.DescribeActionAsync(
                    Read<PluginDescribeActionParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.action.start" => await gateway.StartActionAsync(
                    Read<PluginStartActionParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.interaction.resolve" => await gateway.ResolveInteractionAsync(
                    Read<PluginResolveInteractionParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.task.cancel" => await gateway.CancelTaskAsync(
                    Read<PluginTaskParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.task.get" => await gateway.GetTaskAsync(
                    Read<PluginTaskParams>(request.Payload), token).ConfigureAwait(false),
                _ => throw new PluginDispatchException(
                    "UNKNOWN_TYPE", $"Unhandled plugin request type '{request.Type}'."),
            };
            // The Web HostBridge resolves a pending request by requestId. The
            // response deliberately reuses the fixed use-case type.
            _reply.PostResponse(request.Type, request.RequestId, result);
        }
        catch (JsonException)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "Plugin request payload is invalid.",
                "BAD_PAYLOAD");
        }
        catch (PluginDispatchException ex)
        {
            _reply.PostOperationFailed(request.RequestId, ex.Message, ex.Code);
        }
        catch (GitHubPluginSourceException ex)
        {
            _reply.PostOperationFailed(request.RequestId, ex.Message, ex.Code);
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "Plugin request was cancelled.",
                "PLUGIN_REQUEST_CANCELLED");
        }
        catch (Exception ex)
        {
            SafeTrace(() => Trace.TraceError(DiagnosticEvent.Failure(
                    "VibeTable.Desktop.PluginRequestDispatcher",
                    request.Type,
                    ex.GetType().Name)));
            SafeDiagnosticTrace(
                $"Plugin request failed; type={request.Type}; " +
                $"exception={ex.GetType().Name}");
            _reply.PostOperationFailed(
                request.RequestId,
                "Plugin operation failed.",
                "PLUGIN_OPERATION_FAILED");
        }
    }

    public void Dispose()
    {
        lock (_gatewayGate)
        {
            if (_disposed) return;
            _disposed = true;
        }
        DetachGateway();
        _githubSource?.Dispose();
        _surfaces.CloseAll();
        if (_ownsAuthority) _authority.Dispose();
    }

    private void DispatchSurfaceEvent(RoutedWebRequest request)
    {
        var surfaceEvent = Read<PluginSurfaceEvent>(request.Payload);
        if (!_surfaces.TryAccept(surfaceEvent, out _))
        {
            throw new PluginDispatchException(
                "PLUGIN_SURFACE_TOKEN_INVALID",
                "Plugin surface token is invalid or expired.");
        }
        if (string.Equals(surfaceEvent.Event, PluginSurfaceEvents.Close, StringComparison.Ordinal))
        {
            _resourceHost.ForgetSurfaceToken(surfaceEvent.SurfaceToken);
        }
        SurfaceEventReceived?.Invoke(surfaceEvent);
        _reply.PostResponse(
            request.Type,
            request.RequestId,
            new PluginSurfaceAcceptance(true));
    }

    private static T Read<T>(JsonElement payload) where T : notnull
    {
        if (payload.ValueKind is JsonValueKind.Undefined or JsonValueKind.Null)
        {
            throw new JsonException("Payload is required.");
        }
        return payload.Deserialize<T>(JsonOptions)
            ?? throw new JsonException("Payload did not deserialize.");
    }

    private async Task<PluginRuntimeSnapshot[]> ListCatalogAsync(
        PluginCatalogListParams request,
        CancellationToken token)
    {
        var snapshots = await _gateway!.ListCatalogAsync(request, token).ConfigureAwait(false);
        return snapshots.Select(ProjectSnapshot).ToArray();
    }

    private async Task<PluginRuntimeInstallPlan> InspectInstallAsync(
        PluginInspectInstallParams request,
        CancellationToken token)
    {
        if (!PluginPackageSourceTokens.TryGetPickKind(request.SourceLocation, out var kind))
        {
            throw new PluginDispatchException(
                "PLUGIN_SOURCE_NOT_HOST_SELECTED",
                "Plugin sources must be selected by the native host.");
        }
        string? sourceLocation = await _packagePicker.PickAsync(kind, token).ConfigureAwait(false);
        if (string.IsNullOrWhiteSpace(sourceLocation))
        {
            throw new PluginDispatchException(
                "PLUGIN_SOURCE_SELECTION_CANCELLED",
                "Plugin source selection was cancelled.");
        }
        HostInstallPlanBinding binding = CapturePluginBinding();
        var plan = await binding.Gateway.InspectInstallAsync(
            request with
            {
                ProjectKey = binding.Context.ProjectKey,
                ProjectRevision = binding.Context.ProjectRevision,
                SourceLocation = sourceLocation,
            },
            token).ConfigureAwait(false);
        await AdmitInstallPlanAsync(binding, plan, null).ConfigureAwait(false);
        return plan with { SourceLocation = HostManagedSource };
    }

    private async Task<PluginRuntimeInstallPlan> InspectGitHubInstallAsync(
        PluginGitHubInspectParams request,
        CancellationToken token)
    {
        if (_githubSource is null)
        {
            throw new PluginDispatchException(
                "PLUGIN_GITHUB_SOURCE_UNAVAILABLE",
                "GitHub 插件来源在当前运行模式下不可用。");
        }
        DownloadedPluginPackage? download = await _githubSource.DownloadLatestAsync(
            request.Repository,
            token).ConfigureAwait(false);
        try
        {
            HostInstallPlanBinding binding = CapturePluginBinding();
            var plan = await binding.Gateway.InspectInstallAsync(
                new PluginInspectInstallParams(
                    binding.Context.ProjectKey,
                    binding.Context.ProjectRevision,
                    download.Path),
                token).ConfigureAwait(false);
            DownloadedPluginPackage admittedPackage = download;
            download = null;
            await AdmitInstallPlanAsync(binding, plan, admittedPackage).ConfigureAwait(false);
            return plan with { SourceLocation = HostManagedSource };
        }
        finally
        {
            download?.Dispose();
        }
    }

    private async Task CommitInstallAsync(
        RoutedWebRequest routed,
        PluginCommitInstallParams request,
        CancellationToken token)
    {
        await using HostInstallPlanOperation operation =
            BeginInstallPlanOperation(request.PlanId);
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(
            token,
            operation.Authority.Token);
        try
        {
            HostInstallPlanBinding binding = operation.Plan.Binding;
            if (!_authority.TryStart(
                    operation.Authority,
                    _ => binding.Gateway.CommitInstallAsync(
                        request with { ProjectRevision = binding.Context.ProjectRevision },
                        linked.Token),
                    out Task<PluginRuntimeSnapshot>? pending)
                || pending is null)
            {
                throw StaleInstallPlan();
            }
            PluginRuntimeSnapshot snapshot = await pending.WaitAsync(linked.Token)
                .ConfigureAwait(false);
            if (!_authority.TryFinish(operation.Authority, () =>
            {
                _reply.PostResponse(
                    routed.Type,
                    routed.RequestId,
                    ProjectSnapshot(snapshot));
            }))
            {
                throw StaleInstallPlan();
            }
            operation.Complete();
        }
        catch (OperationCanceledException) when (operation.Authority.Token.IsCancellationRequested)
        {
            throw StaleInstallPlan();
        }
    }

    private async Task UpgradeAsync(
        RoutedWebRequest routed,
        PluginUpgradeParams request,
        CancellationToken token)
    {
        await using HostInstallPlanOperation operation = BeginInstallPlanOperation(
            request.PlanId,
            request.PluginId);
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(
            token,
            operation.Authority.Token);
        try
        {
            HostInstallPlanBinding binding = operation.Plan.Binding;
            if (!_authority.TryStart(
                    operation.Authority,
                    _ => binding.Gateway.UpgradeAsync(
                        request with
                        {
                            ProjectKey = binding.Context.ProjectKey,
                            ProjectRevision = binding.Context.ProjectRevision,
                        },
                        linked.Token),
                    out Task<PluginRuntimeSnapshot>? pending)
                || pending is null)
            {
                throw StaleInstallPlan();
            }
            PluginRuntimeSnapshot snapshot = await pending.WaitAsync(linked.Token)
                .ConfigureAwait(false);
            if (!_authority.TryFinish(operation.Authority, () =>
            {
                _reply.PostResponse(
                    routed.Type,
                    routed.RequestId,
                    ProjectSnapshot(snapshot));
            }))
            {
                throw StaleInstallPlan();
            }
            operation.Complete();
        }
        catch (OperationCanceledException) when (operation.Authority.Token.IsCancellationRequested)
        {
            throw StaleInstallPlan();
        }
    }

    private async Task<PluginInstallCancelResult> CancelInstallAsync(
        PluginInstallCancelParams request,
        CancellationToken token)
    {
        bool owned = _installLeases.TryTake(request.PlanId, out HostInstallPlanLease? lease);
        IPluginRpcGateway gateway = lease?.Binding.Gateway ?? CaptureGateway();
        bool backendCancelled = lease is null
            ? await _installLeases.Cleanup.CancelRemoteAsync(
                gateway,
                request.PlanId,
                token).ConfigureAwait(false)
            : await _installLeases.Cleanup.ReleaseAsync(lease, token).ConfigureAwait(false);
        return new PluginInstallCancelResult(backendCancelled || owned);
    }

    private async Task<PluginRuntimeUninstallResult> UninstallAsync(
        PluginUninstallParams request,
        CancellationToken token)
    {
        var result = await _gateway!.UninstallAsync(request, token).ConfigureAwait(false);
        if (result.Uninstalled)
        {
            _resourceHost.UnregisterInstalled(request.ProjectKey, request.PluginId);
        }
        return result;
    }

    private PluginRuntimeSnapshot ProjectSnapshot(PluginRuntimeSnapshot snapshot)
    {
        bool registered = _resourceHost.TryRegisterInstalled(
            snapshot.ProjectKey,
            snapshot.PluginId,
            snapshot.SourceLocation,
            snapshot.PackageHash);
        PluginRuntimeManifest manifest = registered
            ? snapshot.Manifest with { Ui = ProjectCustomViews(snapshot) }
            : snapshot.Manifest;
        return snapshot with
        {
            SourceLocation = HostManagedSource,
            Manifest = manifest,
        };
    }

    private JsonElement ProjectCustomViews(PluginRuntimeSnapshot snapshot)
    {
        JsonObject? ui;
        try
        {
            ui = JsonNode.Parse(snapshot.Manifest.Ui.GetRawText()) as JsonObject;
        }
        catch (JsonException)
        {
            return snapshot.Manifest.Ui;
        }
        if (ui?["customViews"] is not JsonArray views)
        {
            return snapshot.Manifest.Ui;
        }

        var candidates = views.OfType<JsonObject>().ToArray();
        foreach (JsonObject view in candidates)
        {
            string? entry = ReadNodeString(view["entry"]);
            if (string.IsNullOrWhiteSpace(entry))
            {
                continue;
            }
            string? actionId = ReadNodeString(view["actionId"]);
            if (string.IsNullOrWhiteSpace(actionId))
            {
                string? viewId = ReadNodeString(view["viewId"]);
                actionId = snapshot.Manifest.Actions
                    .Select(action => action.ActionId)
                    .FirstOrDefault(candidate =>
                        string.Equals(candidate, viewId, StringComparison.Ordinal)
                        || string.Equals(candidate, $"open-{viewId}", StringComparison.Ordinal));
                if (actionId is null
                    && candidates.Length == 1
                    && snapshot.Manifest.Actions.Count == 1)
                {
                    actionId = snapshot.Manifest.Actions[0].ActionId;
                }
            }
            if (string.IsNullOrWhiteSpace(actionId))
            {
                continue;
            }
            var surface = _resourceHost.OpenSurface(
                snapshot.ProjectKey,
                snapshot.PluginId,
                entry);
            view["actionId"] = actionId;
            view["src"] = surface.DocumentUri.ToString();
            view["surfaceToken"] = surface.SurfaceToken;
            view["title"] ??= view["viewId"]?.DeepClone() ?? JsonValue.Create(actionId);
        }
        return JsonSerializer.SerializeToElement(ui, JsonOptions);
    }

    private static string? ReadNodeString(JsonNode? node)
    {
        try
        {
            return node?.GetValue<string>();
        }
        catch (InvalidOperationException)
        {
            return null;
        }
    }

    private void OnCatalogChanged(PluginEventEnvelope envelope)
    {
        try
        {
            var snapshot = envelope.Snapshot.Deserialize<PluginRuntimeSnapshot>(JsonOptions);
            if (snapshot is null)
            {
                return;
            }
            var projected = ProjectSnapshot(snapshot);
            _reply.PostNotification(
                "plugin.catalog.changed",
                envelope with { Snapshot = JsonSerializer.SerializeToElement(projected, JsonOptions) });
        }
        catch (Exception ex)
        {
            SafeTrace(() => Trace.TraceError($"Plugin catalog event was dropped: {ex}"));
        }
    }

    private void OnTaskChanged(PluginEventEnvelope envelope)
        => _reply.PostNotification("plugin.task.changed", envelope);

    private void OnInteractionRequested(PluginEventEnvelope envelope)
        => _reply.PostNotification("plugin.interaction.requested", envelope);

    private async void OnFileRequested(PluginEventEnvelope envelope)
    {
        if (_gateway is null)
        {
            return;
        }
        string? selectedPath = null;
        try
        {
            var request = envelope.Snapshot.Deserialize<PluginRuntimeFileRequest>(JsonOptions)
                ?? throw new JsonException("Plugin file request did not deserialize.");
            if (_filePicker is not null)
            {
                selectedPath = await _filePicker.PickAsync(request, CancellationToken.None);
            }
            await _gateway.ResolveFileAsync(
                new PluginResolveFileParams(request.RequestId, selectedPath),
                CancellationToken.None);
        }
        catch (Exception ex)
        {
            SafeTrace(() => Trace.TraceError($"Plugin file request failed: {ex}"));
        }
    }

    private void DetachGateway(IPluginRpcGateway? expected = null)
    {
        IPluginRpcGateway? gateway;
        lock (_gatewayGate)
        {
            if (expected is not null && !ReferenceEquals(_gateway, expected)) return;
            gateway = _gateway;
            _gateway = null;
        }
        if (gateway is not null)
        {
            ReleaseLeases(_installLeases.ClearGatewayAfterAuthorityTransition(gateway));
        }
        if (gateway is null)
        {
            return;
        }
        gateway.CatalogChanged -= OnCatalogChanged;
        gateway.TaskChanged -= OnTaskChanged;
        gateway.InteractionRequested -= OnInteractionRequested;
        gateway.FileRequested -= OnFileRequested;
    }

    private HostInstallPlanBinding CapturePluginBinding()
    {
        HostInstallPlanBinding? binding = _installLeases.Capture();
        if (binding is null
            || binding.GatewayGeneration == 0
            || binding.Context.SessionGeneration == 0
            || string.IsNullOrWhiteSpace(binding.Context.ProjectKey)
            || string.IsNullOrWhiteSpace(binding.Context.ProjectRevision))
        {
            throw new PluginDispatchException(
                "PLUGIN_NOT_READY",
                "Plugin services are not available for the current project.");
        }
        return binding;
    }

    private IPluginRpcGateway CaptureGateway()
    {
        lock (_gatewayGate)
        {
            return _gateway ?? throw new PluginDispatchException(
                "PLUGIN_NOT_READY",
                "Plugin services are not available for the current project.");
        }
    }

    private async Task AdmitInstallPlanAsync(
        HostInstallPlanBinding binding,
        PluginRuntimeInstallPlan plan,
        DownloadedPluginPackage? package)
    {
        if (!_installLeases.TryAdmit(binding, plan, package, out HostInstallPlanLease? replaced))
        {
            package?.Dispose();
            await _installLeases.Cleanup.CancelRemoteAsync(
                binding.Gateway,
                plan.PlanId).ConfigureAwait(false);
            throw new PluginDispatchException(
                "PLUGIN_INSTALL_PLAN_STALE",
                "Plugin install plan is stale for the current project session.");
        }
        ReleaseLease(replaced);
    }

    private HostInstallPlanOperation BeginInstallPlanOperation(
        string planId,
        string? expectedPluginId = null)
    {
        if (!_installLeases.TryBeginOperation(
                planId,
                expectedPluginId,
                out HostInstallPlanOperation? operation,
                out HostInstallPlanLease? rejected)
            || operation is null)
        {
            if (rejected is not null)
            {
                ReleaseLease(rejected);
            }
            else
            {
                IPluginRpcGateway? gateway = CaptureGatewayOrNull();
                if (gateway is not null)
                    _ = _installLeases.Cleanup.CancelRemoteAsync(gateway, planId);
            }
            throw StaleInstallPlan();
        }
        return operation;
    }

    private static PluginDispatchException StaleInstallPlan() => new(
        "PLUGIN_INSTALL_PLAN_STALE",
        "Plugin install plan is stale for the current project session.");

    private void TraceCleanupFailure(string code)
    {
        SafeTrace(() => Trace.TraceError(DiagnosticEvent.Failure(
                "VibeTable.Desktop.PluginRequestDispatcher",
                "plugin.install.cancel",
                code)));
        SafeDiagnosticTrace($"Plugin install cleanup failed; code={code}");
    }

    private void SafeDiagnosticTrace(string message)
        => SafeTrace(() => _diagnosticTrace?.Invoke(message));

    private static void SafeTrace(Action trace)
    {
        try
        {
            trace();
        }
        catch
        {
        }
    }

    private void ReleaseLeases(IEnumerable<HostInstallPlanLease> leases)
    {
        foreach (HostInstallPlanLease lease in leases)
        {
            ReleaseLease(lease);
        }
    }

    private void ReleaseLease(HostInstallPlanLease? lease)
    {
        if (lease is null) return;
        _ = _installLeases.Cleanup.ReleaseAsync(lease);
    }

    private IPluginRpcGateway? CaptureGatewayOrNull()
    {
        lock (_gatewayGate) return _gateway;
    }

    private sealed class PluginDispatchException : Exception
    {
        public PluginDispatchException(string code, string message) : base(message)
            => Code = code;

        public string Code { get; }
    }
}
