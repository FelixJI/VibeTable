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
    private readonly Func<string, JsonElement, CancellationToken, Task<JsonElement>>? _sharedRpc;
    private readonly Func<string, string?>? _packageCacheRoot;
    private readonly Func<CancellationToken, Task>? _ensureGateway;
    private readonly object _gatewayGate = new();
    // Orders local resource projection only; PocketBase remains the authority.
    private readonly object _catalogProjectionGate = new();
    private long _catalogProjectionGeneration;
    private readonly HostInstallPlanLeaseRegistry _installLeases;
    private readonly HostPluginTaskRegistry _taskRegistry;
    private readonly ProductAuthorityEpoch _authority;
    private readonly bool _ownsAuthority;
    private readonly Func<PluginProjectContext, Func<Func<bool>, bool>>? _sharedReadEmitGate;
    private IPluginRpcGateway? _gateway;
    private Action<PluginEventEnvelope>? _taskChangedHandler;
    private Action<PluginEventEnvelope>? _interactionRequestedHandler;
    private Action<PluginEventEnvelope>? _fileRequestedHandler;
    private Action? _gatewayTerminatedHandler;
    private bool _disposed;

    public PluginRequestDispatcher(
        IWebReplySink reply,
        PluginSurfaceSessionManager surfaces,
        IPluginPackageSourcePicker packagePicker,
        PluginWebViewResourceHost resourceHost,
        IPluginFilePicker? filePicker = null,
        Func<PluginProjectContext?>? projectContext = null,
        ProductAuthorityEpoch? authority = null,
        Func<string, JsonElement, CancellationToken, Task<JsonElement>>? sharedRpc = null,
        Func<string, string?>? packageCacheRoot = null,
        Func<CancellationToken, Task>? ensureGateway = null)
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
            sharedRpc,
            packageCacheRoot,
            ensureGateway)
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
        Func<string, JsonElement, CancellationToken, Task<JsonElement>>? sharedRpc = null,
        Func<string, string?>? packageCacheRoot = null,
        Func<CancellationToken, Task>? ensureGateway = null,
        Func<PluginProjectContext, Func<Func<bool>, bool>>? sharedReadEmitGate = null)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _surfaces = surfaces ?? throw new ArgumentNullException(nameof(surfaces));
        _packagePicker = packagePicker ?? throw new ArgumentNullException(nameof(packagePicker));
        _resourceHost = resourceHost ?? throw new ArgumentNullException(nameof(resourceHost));
        _filePicker = filePicker;
        _githubSource = githubSource;
        _diagnosticTrace = diagnosticTrace;
        _projectContext = projectContext ?? (() => null);
        _sharedRpc = sharedRpc;
        _packageCacheRoot = packageCacheRoot;
        _ensureGateway = ensureGateway;
        _authority = authority ?? new ProductAuthorityEpoch();
        _ownsAuthority = authority is null;
        _sharedReadEmitGate = sharedReadEmitGate;
        _installLeases = new HostInstallPlanLeaseRegistry(_authority);
        _taskRegistry = new HostPluginTaskRegistry(_authority);
    }

    public event Action<PluginSurfaceEvent>? SurfaceEventReceived;
    internal HostPluginTaskRegistry Tasks => _taskRegistry;

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
            _taskChangedHandler = envelope => OnTaskChanged(gateway, envelope);
            gateway.TaskChanged += _taskChangedHandler;
            _interactionRequestedHandler = envelope => OnInteractionRequested(gateway, envelope);
            gateway.InteractionRequested += _interactionRequestedHandler;
            _fileRequestedHandler = envelope => OnFileRequested(gateway, envelope);
            gateway.FileRequested += _fileRequestedHandler;
            _gatewayTerminatedHandler = () => OnGatewayTerminated(gateway);
            gateway.Terminated += _gatewayTerminatedHandler;
        }
        ReleaseLeases(_installLeases.SetGatewayAfterAuthorityTransition(gateway, context));
        PostSettlements(_taskRegistry.SetGateway(gateway, context));
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

    /// <summary>
    /// Updates the workspace-open identity used for task history visibility.
    /// The admission authority is deliberately unaffected: a Python client
    /// loss must keep settled terminal states queryable for the same
    /// workspace instead of hiding them as unknown tasks.
    /// </summary>
    public void SetWorkspaceContext(PluginProjectContext? context)
        => _taskRegistry.SetWorkspaceContext(context);

    internal void SetProjectContextAfterAuthorityTransition(PluginProjectContext? context)
    {
        AdvanceCatalogProjection();
        ReleaseLeases(_installLeases.SetContextAfterAuthorityTransition(context));
        PostSettlements(_taskRegistry.SetContext(context));
    }

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
            string? sharedMethod = request.Type switch
            {
                "plugin.catalog.list" => "plugin.listCatalog",
                "plugin.audit.list" => "plugin.listAudit",
                "plugin.cleanup.listPending" => "plugin.listPendingCleanup",
                _ => null,
            };
            if (sharedMethod is not null)
            {
                PluginProjectContext? context = _projectContext();
                if (context is null) throw new PluginDispatchException("PLUGIN_NOT_READY", "Plugin project context is unavailable.");
                Func<Func<bool>, bool>? emitGate = GateSharedReadTerminal(request.Type, context);
                if (sharedMethod == "plugin.listCatalog")
                {
                    if (Read<PluginCatalogListParams>(request.Payload).ProjectKey != context.ProjectKey) throw StaleTask();
                    bool PublishTaskHistory()
                    {
                        if (context != _projectContext()) return false;
                        foreach (var receipt in _taskRegistry.ProjectHistory(context))
                            _reply.TryPostResponse("plugin.task.changed", requestId: null, receipt, emitGate);
                        return true;
                    }
                    // The renderer establishes its workspace store before this
                    // bootstrap read. Replay before requiring either backend:
                    // recovery receipts remain useful when catalog loading fails.
                    if (!(emitGate?.Invoke(PublishTaskHistory) ?? PublishTaskHistory())) return;
                }
                Func<string, JsonElement, CancellationToken, Task<JsonElement>> sharedRpc = _sharedRpc
                    ?? throw new PluginDispatchException(
                        "PLUGIN_NOT_READY", "Plugin catalog is not available for the current project.");
                // The three shared reads settle through the workspace session
                // emission gate: the gate runs the actual reply commit under
                // the session publication lock, so a scope retired by a close
                // or epoch rotation can never emit a late terminal while the
                // renderer has already settled the request locally. Enable
                // transitions use the Host-only execution path below. Without a
                // production gate (standalone construction) the sink's
                // synchronous default commits exactly as before.
                long catalogGeneration = CaptureCatalogProjection();
                JsonElement shared;
                object projected = null!;
                try
                {
                    shared = await sharedRpc(sharedMethod, request.Payload, token).ConfigureAwait(false);
                    // Ungated paths (standalone/public construction) keep the
                    // original stale reply for a rotated context; gated reads
                    // rely on the session emission gate instead.
                    if (context != _projectContext() && emitGate is null) throw StaleTask();
                    PluginProjectContext? projectionContext = _projectContext();
                    PluginRuntimeSnapshot[]? snapshots = sharedMethod == "plugin.listCatalog"
                        ? shared.Deserialize<PluginRuntimeSnapshot[]>(JsonOptions) ?? throw new JsonException("Invalid plugin catalog.") : null;
                    string? cacheRoot = _packageCacheRoot?.Invoke(context.ProjectKey);
                    bool ProjectCurrentCatalog()
                    {
                        lock (_catalogProjectionGate)
                        {
                            bool current = catalogGeneration == _catalogProjectionGeneration && context == projectionContext;
                            if (snapshots is not null && !current)
                                throw new PluginDispatchException("PLUGIN_REQUEST_CANCELLED", "Plugin catalog read was superseded.");
                            if (snapshots is not null)
                            {
                                var retained = snapshots.Select(item => item.PluginId).ToHashSet(StringComparer.Ordinal);
                                _resourceHost.RetireAbsentInstallations(context.ProjectKey, retained);
                                foreach (var key in _nativePlugins.Keys.Where(key => key.Item1 == context.ProjectKey && !retained.Contains(key.Item2)))
                                    _nativePlugins.TryRemove(key, out _);
                            }
                            projected = snapshots is not null
                                ? snapshots.Select(item => ProjectSnapshot(item, projectionContext, cacheRoot)).ToArray()
                                : shared;
                            return true;
                        }
                    }
                    // The publication gate precedes the projection lock. No
                    // session/cache callback or network await runs in that lock.
                    if (snapshots is not null && emitGate is not null)
                    {
                        if (!emitGate(ProjectCurrentCatalog)) return;
                    }
                    else ProjectCurrentCatalog();
                }
                catch (Exception ex) when (emitGate is not null)
                {
                    // Every shared-read failure — RPC, cancellation, JSON and
                    // projection included — maps to its original code but only
                    // commits through the emission gate.
                    PostSharedReadFailure(request, ex, token, emitGate);
                    return;
                }
                _reply.TryPostResponse(request.Type, request.RequestId, projected, emitGate);
                return;
            }
            if (string.Equals(request.Type, "plugin.surface.event", StringComparison.Ordinal))
            {
                DispatchSurfaceEvent(request);
                return;
            }
            // Host-owned task lifecycle reads/writes never require a live
            // Python client: the registry answers from workspace-owned state
            // so a lost backend cannot leave a task permanently running.
            if (string.Equals(request.Type, "plugin.task.get", StringComparison.Ordinal)
                || string.Equals(request.Type, "plugin.task.cancel", StringComparison.Ordinal)
                || string.Equals(request.Type, "plugin.interaction.resolve", StringComparison.Ordinal)
                || string.Equals(request.Type, "plugin.install.cancel", StringComparison.Ordinal))
            {
                object ownedResult = request.Type switch
                {
                    "plugin.task.get" => GetTask(Read<PluginTaskParams>(request.Payload)),
                    "plugin.task.cancel" => await CancelTaskAsync(
                        Read<PluginTaskParams>(request.Payload), token).ConfigureAwait(false),
                    "plugin.interaction.resolve" => await ResolveInteractionAsync(
                        Read<PluginResolveInteractionParams>(request.Payload), token).ConfigureAwait(false),
                    _ => CancelInstall(
                        Read<PluginInstallCancelParams>(request.Payload)),
                };
                _reply.PostResponse(request.Type, request.RequestId, ownedResult);
                return;
            }
            if (request.Type is not ("plugin.install.inspect" or "plugin.install.github.inspect"
                or "plugin.install.commit" or "plugin.lifecycle.upgrade"
                or "plugin.lifecycle.setEnabled"
                or "plugin.lifecycle.rollback" or "plugin.lifecycle.uninstall"
                or "plugin.action.describe" or "plugin.action.start"))
                throw new PluginDispatchException("UNKNOWN_TYPE", $"Unhandled plugin request type '{request.Type}'.");
            if (request.Type == "plugin.action.describe")
            {
                var describe = Read<PluginDescribeActionParams>(request.Payload);
                bool native = _nativePlugins.TryGetValue((describe.ProjectKey, describe.PluginId), out var actions);
                if (native)
                {
                    var action = actions!.Declared.FirstOrDefault(action => action.ActionId == describe.ActionId);
                    if (action is null) throw new PluginDispatchException("PLUGIN_ACTION_NOT_FOUND", "Plugin action is not declared.");
                    if (_projectContext()?.ProjectKey != describe.ProjectKey || describe.Context.ProjectKey != describe.ProjectKey) throw StaleTask();
                    if (action.Invocation != "manual")
                    {
                        _reply.PostResponse(request.Type, request.RequestId, new PluginRuntimeActionAvailability(false, ["manual_invocation_required"]));
                        return;
                    }
                    if (!actions.Available.Contains(describe.ActionId))
                    {
                        _reply.PostResponse(request.Type, request.RequestId, new PluginRuntimeActionAvailability(false, ["plugin_surface_unavailable"]));
                        return;
                    }
                    string? selection = action.Requires.ValueKind == JsonValueKind.Object
                        && action.Requires.TryGetProperty("selection", out var required) && required.ValueKind == JsonValueKind.String ? required.GetString() : null;
                    string? reason = selection switch
                    {
                        "one-or-more" when describe.Context.SelectedKeys.Count == 0 => "selection_required",
                        "exactly-one" when describe.Context.SelectedKeys.Count != 1 => "single_selection_required",
                        _ => null,
                    };
                    if (reason is not null)
                    {
                        _reply.PostResponse(request.Type, request.RequestId, new PluginRuntimeActionAvailability(false, [reason]));
                        return;
                    }
                    _surfaces.BindActiveCollection(describe.ProjectKey, describe.PluginId, describe.ActionId, describe.Context);
                    _reply.PostResponse(request.Type, request.RequestId, new PluginRuntimeActionAvailability(true, []));
                    return;
                }
            }
            if (_ensureGateway is not null)
            {
                PluginProjectContext? context = _projectContext();
                if (context is null) throw new PluginDispatchException("PLUGIN_NOT_READY", "Plugin project context is unavailable.");
                await _ensureGateway(token).ConfigureAwait(false);
                if (context != _projectContext()) throw StaleTask();
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
                "plugin.lifecycle.setEnabled" => await SetEnabledExecutionAsync(
                    gateway, Read<PluginSetEnabledParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.install.inspect" => await InspectInstallAsync(
                    Read<PluginInspectInstallParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.install.github.inspect" => await InspectGitHubInstallAsync(
                    Read<PluginGitHubInspectParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.lifecycle.rollback" => await RollbackAsync(
                    gateway, Read<PluginRollbackParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.lifecycle.uninstall" => await UninstallAsync(
                    Read<PluginUninstallParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.action.describe" => await gateway.DescribeActionAsync(
                    Read<PluginDescribeActionParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.action.start" => await StartActionAsync(
                    Read<PluginStartActionParams>(request.Payload), token).ConfigureAwait(false),
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
        // The host owns the plan identity: the id is generated here, sent with
        // the private inspection payload and its echo is verified before the
        // plan is ever admitted to the lease registry.
        string planId = NewInstallPlanId();
        var plan = await binding.Gateway.InspectInstallAsync(
            new PluginInspectInstallExecutionParams(
                binding.Context.ProjectKey,
                binding.Context.ProjectRevision,
                sourceLocation,
                planId),
            token).ConfigureAwait(false);
        VerifyPlanEcho(plan, planId);
        plan = await BindPlanTargetAsync(binding, plan, token).ConfigureAwait(false);
        AdmitInstallPlan(binding, plan, null);
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
            string planId = NewInstallPlanId();
            var plan = await binding.Gateway.InspectInstallAsync(
                new PluginInspectInstallExecutionParams(
                    binding.Context.ProjectKey,
                    binding.Context.ProjectRevision,
                    download.Path,
                    planId),
                token).ConfigureAwait(false);
            VerifyPlanEcho(plan, planId);
            plan = await BindPlanTargetAsync(binding, plan, token).ConfigureAwait(false);
            DownloadedPluginPackage admittedPackage = download;
            download = null;
            AdmitInstallPlan(binding, plan, admittedPackage);
            return plan with { SourceLocation = HostManagedSource };
        }
        finally
        {
            download?.Dispose();
        }
    }

    private static string NewInstallPlanId()
        => $"plugin-plan-{Guid.NewGuid():N}"[..24];

    private async Task<PluginRuntimeInstallPlan> BindPlanTargetAsync(
        HostInstallPlanBinding binding, PluginRuntimeInstallPlan plan, CancellationToken token)
    {
        // Production reads the Go authority directly; the renderer never supplies this binding.
        // Standalone gateways may provide the same binding from their authority-backed inspection.
        if (_sharedRpc is null) return plan;
        JsonElement catalog = await _sharedRpc("plugin.listCatalog",
            JsonSerializer.SerializeToElement(new PluginCatalogListParams(binding.Context.ProjectKey), JsonOptions), token)
            .ConfigureAwait(false);
        var current = (catalog.Deserialize<PluginRuntimeSnapshot[]>(JsonOptions)
            ?? throw new JsonException("Invalid plugin catalog."))
            .SingleOrDefault(item => item.PluginId == plan.Manifest.PluginId);
        if (current is not null && string.IsNullOrWhiteSpace(current.InstallationId)) throw StaleInstallPlan();
        return plan with { TargetInstallationId = current?.InstallationId, TargetRevision = current?.Revision };
    }

    private static void VerifyPlanEcho(PluginRuntimeInstallPlan plan, string planId)
    {
        if (!string.Equals(plan.PlanId, planId, StringComparison.Ordinal))
        {
            throw new PluginDispatchException(
                "PLUGIN_INSTALL_PLAN_STALE",
                "Plugin install plan identity was not echoed by the backend.");
        }
    }

    private async Task CommitInstallAsync(
        RoutedWebRequest routed,
        PluginCommitInstallParams request,
        CancellationToken token)
    {
        await using HostInstallPlanOperation operation =
            BeginInstallPlanOperation(request.PlanId);
        if (operation.Plan.Plan.TargetInstallationId is not null || operation.Plan.Plan.TargetRevision is not null)
            throw StaleInstallPlan();
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(
            token,
            operation.Authority.Token);
        long catalogGeneration = AdvanceCatalogProjection();
        bool catalogCompleted = false;
        try
        {
            HostInstallPlanBinding binding = operation.Plan.Binding;
            // The full execution payload comes exclusively from the consumed
            // lease; the renderer's frozen commit DTO never carries it.
            if (!_installLeases.TryStartOperation(
                    operation,
                    _ => binding.Gateway.CommitInstallAsync(
                        new PluginCommitInstallExecutionParams(
                            binding.Context.ProjectKey,
                            operation.Plan.Plan,
                            binding.Context.ProjectRevision),
                        linked.Token),
                    out Task<PluginRuntimeSnapshot>? pending)
                || pending is null)
            {
                throw StaleInstallPlan();
            }
            PluginRuntimeSnapshot snapshot = await pending.WaitAsync(linked.Token)
                .ConfigureAwait(false);
            PluginProjectContext? projectionContext = _projectContext();
            string? cacheRoot = _packageCacheRoot?.Invoke(snapshot.ProjectKey);
            if (!_installLeases.TryFinishOperation(operation, () =>
            {
                lock (_catalogProjectionGate)
                {
                    _reply.PostResponse(routed.Type, routed.RequestId,
                        catalogGeneration == _catalogProjectionGeneration
                            ? ProjectSnapshot(snapshot, projectionContext, cacheRoot) : SanitizeSnapshot(snapshot));
                    ++_catalogProjectionGeneration;
                    catalogCompleted = true;
                }
            }))
            {
                throw StaleInstallPlan();
            }
        }
        catch (OperationCanceledException) when (operation.Authority.Token.IsCancellationRequested)
        {
            throw StaleInstallPlan();
        }
        finally { if (!catalogCompleted) AdvanceCatalogProjection(); }
    }

    private async Task UpgradeAsync(
        RoutedWebRequest routed,
        PluginUpgradeParams request,
        CancellationToken token)
    {
        await using HostInstallPlanOperation operation = BeginInstallPlanOperation(
            request.PlanId,
            request.PluginId);
        if (string.IsNullOrWhiteSpace(operation.Plan.Plan.TargetInstallationId)
            || operation.Plan.Plan.TargetRevision is null or < 1) throw StaleInstallPlan();
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(
            token,
            operation.Authority.Token);
        long catalogGeneration = AdvanceCatalogProjection();
        bool catalogCompleted = false;
        try
        {
            HostInstallPlanBinding binding = operation.Plan.Binding;
            if (!_installLeases.TryStartOperation(
                    operation,
                    _ => binding.Gateway.UpgradeAsync(
                        new PluginUpgradeExecutionParams(
                            binding.Context.ProjectKey,
                            request.PluginId,
                            operation.Plan.Plan,
                            binding.Context.ProjectRevision),
                        linked.Token),
                    out Task<PluginRuntimeSnapshot>? pending)
                || pending is null)
            {
                throw StaleInstallPlan();
            }
            PluginRuntimeSnapshot snapshot = await pending.WaitAsync(linked.Token)
                .ConfigureAwait(false);
            PluginProjectContext? projectionContext = _projectContext();
            string? cacheRoot = _packageCacheRoot?.Invoke(snapshot.ProjectKey);
            if (!_installLeases.TryFinishOperation(operation, () =>
            {
                lock (_catalogProjectionGate)
                {
                    _reply.PostResponse(routed.Type, routed.RequestId,
                        catalogGeneration == _catalogProjectionGeneration
                            ? ProjectSnapshot(snapshot, projectionContext, cacheRoot) : SanitizeSnapshot(snapshot));
                    ++_catalogProjectionGeneration;
                    catalogCompleted = true;
                }
            }))
            {
                throw StaleInstallPlan();
            }
        }
        catch (OperationCanceledException) when (operation.Authority.Token.IsCancellationRequested)
        {
            throw StaleInstallPlan();
        }
        finally { if (!catalogCompleted) AdvanceCatalogProjection(); }
    }

    private PluginInstallCancelResult CancelInstall(
        PluginInstallCancelParams request)
    {
        // Cancellation is host-owned and fully local: the plan lease is taken
        // and its download disposed without any backend round trip. Python
        // holds no acceptable-plan ledger, so there is nothing to cancel there.
        bool owned = _installLeases.TryTake(request.PlanId, out HostInstallPlanLease? lease);
        lease?.Package?.Dispose();
        return new PluginInstallCancelResult(owned);
    }

    private async Task<PluginRuntimeSnapshot> RollbackAsync(
        IPluginRpcGateway gateway, PluginRollbackParams request, CancellationToken token)
    {
        long generation = AdvanceCatalogProjection();
        bool catalogCompleted = false;
        try
        {
            var snapshot = await gateway.RollbackAsync(request, token).ConfigureAwait(false);
            PluginProjectContext? context = _projectContext();
            string? cacheRoot = _packageCacheRoot?.Invoke(snapshot.ProjectKey);
            lock (_catalogProjectionGate)
            {
                var projected = generation == _catalogProjectionGeneration
                    ? ProjectSnapshot(snapshot, context, cacheRoot) : SanitizeSnapshot(snapshot);
                ++_catalogProjectionGeneration;
                catalogCompleted = true;
                return projected;
            }
        }
        finally { if (!catalogCompleted) AdvanceCatalogProjection(); }
    }

    private async Task<PluginRuntimeSnapshot> SetEnabledExecutionAsync(
        IPluginRpcGateway gateway, PluginSetEnabledParams request, CancellationToken token)
    {
        PluginProjectContext? context = _projectContext();
        if (context is null || context.ProjectKey != request.ProjectKey) throw StaleTask();
        if (string.IsNullOrWhiteSpace(request.InstallationId) || request.ExpectedRevision < 1)
            throw new PluginDispatchException("BAD_PAYLOAD", "Installation identity and revision are required.");
        long generation = AdvanceCatalogProjection();
        bool completed = false;
        try
        {
            var snapshot = await gateway.SetEnabledExecutionAsync(new(request.ProjectKey, request.PluginId,
                request.Enabled, request.InstallationId, request.ExpectedRevision), token).ConfigureAwait(false);
            if (context != _projectContext() || !ReferenceEquals(gateway, CaptureGatewayOrNull())) throw StaleTask();
            string? root = _packageCacheRoot?.Invoke(snapshot.ProjectKey);
            lock (_catalogProjectionGate)
            {
                var projected = generation == _catalogProjectionGeneration
                    ? ProjectSnapshot(snapshot, context, root) : SanitizeSnapshot(snapshot);
                ++_catalogProjectionGeneration;
                completed = true;
                return projected;
            }
        }
        finally { if (!completed) AdvanceCatalogProjection(); }
    }

    private async Task<PluginRuntimeUninstallResult> UninstallAsync(
        PluginUninstallParams request,
        CancellationToken token)
    {
        if (string.IsNullOrWhiteSpace(request.InstallationId) || request.ExpectedRevision < 1)
            throw new PluginDispatchException("BAD_PAYLOAD", "Installation identity and revision are required.");
        AdvanceCatalogProjection();
        bool catalogCompleted = false;
        try
        {
            var result = await _gateway!.UninstallAsync(request, token).ConfigureAwait(false);
            lock (_catalogProjectionGate)
            {
                if (result.Uninstalled
                    && _resourceHost.UnregisterInstalledIfCurrent(request.ProjectKey, request.PluginId, request.InstallationId))
                {
                    _nativePlugins.TryRemove((request.ProjectKey, request.PluginId), out _);
                }
                ++_catalogProjectionGeneration;
                catalogCompleted = true;
            }
            return result;
        }
        finally { if (!catalogCompleted) AdvanceCatalogProjection(); }
    }

    /// <summary>
    /// Generates the task/run identity, registers the task in the host-owned
    /// registry BEFORE the executor is invoked, and then starts the closed
    /// host-only execution entry. The executor receives the identity and
    /// returns only descriptive metadata echoing it; it never owns the
    /// lifecycle. A start that fails or returns from a retired generation is
    /// settled (failed/aborted) so no task ever exists without a host owner.
    /// </summary>
    private async Task<PluginRuntimeTaskSnapshot> StartActionAsync(
        PluginStartActionParams request,
        CancellationToken token)
    {
        HostPluginTaskBinding binding = CaptureTaskBinding();
        if (request.ProjectKey != binding.Context.ProjectKey
            || request.Context.ProjectKey != binding.Context.ProjectKey) throw StaleTask();
        string taskId = $"plugin-task-{Guid.NewGuid():N}"[..24];
        string runId = $"plugin-run-{Guid.NewGuid():N}"[..23];
        PluginRuntimeTaskSnapshot queued = new(
            taskId,
            runId,
            request.PluginId,
            "unknown",
            request.ActionId,
            binding.Context.ProjectKey,
            request.Context.Collection,
            request.Context.SelectedKeys.Count,
            PluginRisk.Read,
            "queued",
            false,
            null,
            null,
            null);
        if (!_taskRegistry.AdmitTask(binding, queued)) throw StaleTask();
        PluginRuntimeTaskSnapshot reported;
        try
        {
            reported = await binding.Gateway!.StartActionAsync(
                request with { TaskId = taskId, RunId = runId }, token).ConfigureAwait(false);
        }
        catch (Exception)
        {
            PostSingleSettlement(_taskRegistry.AbortTask(taskId));
            throw;
        }
        if (!_taskRegistry.TryApplyStartResult(binding.Gateway!, reported))
        {
            SafeDiagnosticTrace(
                "Plugin action start was rejected as stale; " +
                $"taskId={taskId}");
            PostSingleSettlement(_taskRegistry.AbortTask(taskId));
            await ObserveCancelAsync(
                () => binding.Gateway!.CancelTaskAsync(
                    new PluginTaskParams(taskId),
                    CancellationToken.None)).ConfigureAwait(false);
            throw StaleTask();
        }
        // Answer with the registry's authoritative snapshot: a faster
        // execution report may legitimately have advanced or finished the
        // task before the start response arrived.
        return _taskRegistry.TryGetTask(taskId, out PluginRuntimeTaskSnapshot current)
            ? current
            : throw StaleTask();
    }

    /// <summary>
    /// Answers public task state from the host-owned registry. Python is never
    /// queried, so a lost client cannot leave a task permanently running.
    /// </summary>
    private PluginRuntimeTaskSnapshot GetTask(PluginTaskParams request)
    {
        if (_taskRegistry.TryGetTask(request.TaskId, out PluginRuntimeTaskSnapshot snapshot))
        {
            return snapshot;
        }
        throw UnknownTask();
    }

    /// <summary>
    /// Cancels through the host-owned registry. A terminal task is returned
    /// unchanged — a late cancel can never overwrite a recorded success — and
    /// the gateway call is only the execution-side handle trigger.
    /// </summary>
    private async Task<PluginRuntimeTaskSnapshot> CancelTaskAsync(
        PluginTaskParams request,
        CancellationToken token)
    {
        var outcome = _taskRegistry.RequestCancel(
            request.TaskId,
            out PluginRuntimeTaskSnapshot snapshot,
            out HostPluginTaskBinding binding);
        if (outcome == HostPluginTaskCancelOutcome.NotFound) throw UnknownTask();
        if (outcome == HostPluginTaskCancelOutcome.Active && binding.Gateway is not null)
        {
            _ = ObserveRevokeRunAsync(binding.Gateway, snapshot.RunId);
            await ObserveCancelAsync(
                () => binding.Gateway.CancelTaskAsync(request, token)).ConfigureAwait(false);
        }
        return snapshot;
    }

    /// <summary>
    /// Resolves a confirmation only while its run is pending on the current
    /// generation. Late confirmations are answered as expired without ever
    /// reaching an executor, so they cannot revive a retired run.
    /// #482: application/headless confirmations are NATIVE-ONLY — a resolve
    /// arriving from the web bridge is rejected before the one-shot registry
    /// decision is consumed; the native host window keeps the sole entry.
    /// </summary>
    private async Task<PluginRuntimeInteractionResolveResult> ResolveInteractionAsync(
        PluginResolveInteractionParams request,
        CancellationToken token,
        bool fromNativeHost = false)
    {
        PluginRuntimeInteractionResolveResult decision =
            _taskRegistry.BeginInteractionResolve(request, out IPluginRpcGateway? gateway, fromNativeHost);
        if (decision.Status == "native-required")
            throw new PluginDispatchException("PLUGIN_NATIVE_CONFIRMATION_REQUIRED",
                "无界面/全视口插件动作的确认只能由宿主原生窗口批准。");
        if (gateway is null) return decision;
        // Python only wakes the execution future. Host has already consumed
        // the public decision; transport failure cannot turn it into a retry.
        await gateway.ResolveInteractionAsync(request, token).ConfigureAwait(false);
        return decision;
    }

    private async Task ObserveCancelAsync(
        Func<Task<bool>> cancel)
    {
        try
        {
            await cancel().ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            // The cancel request itself is owned by the host registry; a
            // failing execution-side trigger is traced and settled by events
            // or by the generation settlement instead of surfacing an error.
            SafeTrace(() => Trace.TraceError(DiagnosticEvent.Failure(
                "VibeTable.Desktop.PluginRequestDispatcher",
                "plugin.task.cancel",
                ex.GetType().Name)));
        }
    }

    private static PluginDispatchException StaleTask() => new(
        "PLUGIN_TASK_STALE",
        "Plugin task belongs to a retired project session.");

    private Func<Func<bool>, bool>? GateSharedReadTerminal(
        string requestType,
        PluginProjectContext context)
    {
        if (_sharedReadEmitGate is null) return null;
        Func<Func<bool>, bool> gate = _sharedReadEmitGate(context);
        return post =>
        {
            if (gate(post)) return true;
            SafeDiagnosticTrace(
                $"Plugin shared read terminal was dropped as retired at emit; " +
                $"type={requestType}");
            return false;
        };
    }

    /// <summary>
    /// Captures the shared-read emission gate for one request admission.
    /// The host epoch lease is taken inside the publication gate; its token is
    /// copied and the observer disposed immediately so a close drain never
    /// waits for the queued terminal. The terminal commits only when,
    /// atomically under the publication gate, the copied token is still
    /// uncancelled AND the admitting (workspace, epoch) is the current opened
    /// Idle session: a close drain permanently cancels the old token and a
    /// protection-failure rollback resumes the same UUID/epoch with a fresh
    /// token, so an old terminal can never revive. A failed capture rejects
    /// the gate outright — never a null fallback.
    /// </summary>
    internal static Func<Func<bool>, bool> CaptureSharedReadEmitGate(
        WorkspaceSessionManager sessions,
        WorkspaceSessionEnvelopeFilter filter,
        PluginProjectContext context)
    {
        string key = context.ProjectKey;
        if (key.Length <= "local:".Length
            || !key.StartsWith("local:", StringComparison.Ordinal)
            || !Guid.TryParseExact(key.AsSpan("local:".Length), "N", out Guid workspaceId))
        {
            return _ => false;
        }
        ulong epoch = context.SessionGeneration;
        CancellationToken capturedToken = default;
        bool admitted = sessions.TryUseCurrentSession(
            workspaceId,
            epoch,
            () =>
            {
                if (!filter.TryCaptureHost(
                        workspaceId,
                        epoch,
                        Guid.NewGuid(),
                        out WorkspaceRequestEpochLease? lease)
                    || lease is null)
                {
                    return false;
                }
                // Copy the epoch token, then release the observer immediately:
                // only the copied token's cancellation state is read later,
                // never the disposed lease.
                capturedToken = lease.CancellationToken;
                lease.Dispose();
                return true;
            });
        if (!admitted)
        {
            return _ => false;
        }
        // The token check lives INSIDE the publication action: checking it
        // outside the lock would let a paused caller observe an uncancelled
        // token, then drain-cancel, then a same-UUID/epoch rollback resume,
        // and only then acquire and emit a revived late terminal.
        return post => sessions.TryUseCurrentSession(
            workspaceId,
            epoch,
            () => !capturedToken.IsCancellationRequested && post());
    }

    /// <summary>
    /// Maps a shared-read failure exactly like the unguarded dispatch catches
    /// and commits it only through the session emission gate, so the original
    /// code stays visible for a current scope while a retired scope emits
    /// nothing.
    /// </summary>
    private void PostSharedReadFailure(
        RoutedWebRequest request,
        Exception exception,
        CancellationToken token,
        Func<Func<bool>, bool> emitGate)
    {
        string message;
        string? code;
        switch (exception)
        {
            case JsonException:
                message = "Plugin request payload is invalid.";
                code = "BAD_PAYLOAD";
                break;
            case PluginDispatchException dispatch:
                message = dispatch.Message;
                code = dispatch.Code;
                break;
            case GitHubPluginSourceException github:
                message = github.Message;
                code = github.Code;
                break;
            case OperationCanceledException when token.IsCancellationRequested:
                message = "Plugin request was cancelled.";
                code = "PLUGIN_REQUEST_CANCELLED";
                break;
            default:
                message = "Plugin operation failed.";
                code = "PLUGIN_OPERATION_FAILED";
                SafeTrace(() => Trace.TraceError(DiagnosticEvent.Failure(
                    "VibeTable.Desktop.PluginRequestDispatcher",
                    request.Type,
                    exception.GetType().Name)));
                SafeDiagnosticTrace(
                    $"Plugin request failed; type={request.Type}; " +
                    $"exception={exception.GetType().Name}");
                break;
        }
        _reply.TryPostOperationFailed(request.RequestId, message, code, emitGate: emitGate);
    }

    private static PluginDispatchException UnknownTask() => new(
        "PLUGIN_TASK_NOT_FOUND",
        "Plugin task was not found for the current project session.");

    private sealed record NativeActions(IReadOnlyList<PluginRuntimeAction> Declared, string[] Available);
    private readonly System.Collections.Concurrent.ConcurrentDictionary<(string, string), NativeActions> _nativePlugins = new();

    /// <summary>
    /// Native confirmation entry: the same one-shot registry decision the web
    /// footer used for legacy surfaces — the host window is not a second
    /// approval state, and it is the ONLY entry for application/headless.
    /// </summary>
    internal Task<PluginRuntimeInteractionResolveResult> ResolveInteractionForHostAsync(
        PluginResolveInteractionParams request)
        => ResolveInteractionAsync(request, CancellationToken.None, fromNativeHost: true);

    private long CaptureCatalogProjection()
    {
        lock (_catalogProjectionGate) return _catalogProjectionGeneration;
    }

    private long AdvanceCatalogProjection()
    {
        lock (_catalogProjectionGate) return ++_catalogProjectionGeneration;
    }

    private PluginRuntimeSnapshot ProjectSnapshot(
        PluginRuntimeSnapshot snapshot, PluginProjectContext? context, string? cacheRoot)
    {
        string? localPackage = PluginRetainedPackage.PathFor(cacheRoot, snapshot.PackageHash);
        bool registered = snapshot.Status == "enabled" && localPackage is not null && _resourceHost.TryRegisterInstalled(
            snapshot.ProjectKey,
            snapshot.PluginId,
            localPackage,
            snapshot.PackageHash, snapshot.InstallationId, snapshot.Revision);
        if (!registered) _resourceHost.UnregisterInstalled(snapshot.ProjectKey, snapshot.PluginId);
        PluginRuntimeManifest manifest = snapshot.Manifest with
        {
            Ui = ProjectCustomViews(snapshot, registered, context),
            Actions = ProjectActionSurfaces(snapshot, registered, context),
        };
        if (snapshot.Manifest.Compatibility.TryGetProperty("pluginApi", out var api) && api.GetString() == "3.x" && snapshot.Status == "enabled")
        {
            List<string> availableActions =
            [
                .. (manifest.Ui.TryGetProperty("customViews", out var views) && views.ValueKind == JsonValueKind.Array
                    ? views.EnumerateArray().Where(view => view.TryGetProperty("surfaceToken", out var token) && token.ValueKind == JsonValueKind.String
                        && _surfaces.TryGet(token.GetString()!, out _) && view.TryGetProperty("actionId", out _))
                        .Select(view => view.GetProperty("actionId").GetString()!) : []),
                .. manifest.Actions
                    .Where(action => (action.EffectivePresentation == "application" || action.EffectivePresentation == "headless")
                        && action.SurfaceToken is not null
                        && _surfaces.TryGet(action.SurfaceToken, out _))
                    .Select(action => action.ActionId),
            ];
            _nativePlugins[(snapshot.ProjectKey, snapshot.PluginId)] = new(manifest.Actions, [.. availableActions]);
        }
        else _nativePlugins.TryRemove((snapshot.ProjectKey, snapshot.PluginId), out _);
        return snapshot with
        {
            SourceLocation = HostManagedSource,
            Manifest = manifest,
        };
    }

    /// <summary>
    /// Derives host-owned surfaces for application/headless actions. The
    /// declared entry stays on the action; src/token are projection-only
    /// fields that never persist in the stored manifest.
    /// </summary>
    private IReadOnlyList<PluginRuntimeAction> ProjectActionSurfaces(PluginRuntimeSnapshot snapshot, bool registered, PluginProjectContext? context)
    {
        var projected = new List<PluginRuntimeAction>(snapshot.Manifest.Actions.Count);
        foreach (PluginRuntimeAction action in snapshot.Manifest.Actions)
        {
            if (action.EffectivePresentation is not ("application" or "headless"))
            {
                projected.Add(action);
                continue;
            }
            if (!registered || action.Invocation != "manual" || string.IsNullOrWhiteSpace(action.Entry)
                || !PluginResourceHost.SupportsPresentationEntry(action.EffectivePresentation, action.Entry))
            {
                projected.Add(action with { SurfaceSrc = null, SurfaceToken = null, SurfaceTitle = null });
                continue;
            }
            var surface = _resourceHost.OpenSurface(
                snapshot.ProjectKey,
                snapshot.PluginId,
                action.Entry,
                action.ActionId,
                action.EffectivePresentation);
            if (context is not null)
                _surfaces.BindIdentity(surface.SurfaceToken, context, snapshot.Revision,
                    snapshot.Manifest.Compatibility.TryGetProperty("pluginApi", out var version) ? version.GetString() ?? "" : "", snapshot.Version, action.ActionId,
                    action.Risk, action.Entry, action.EffectivePresentation, snapshot.InstallationId);
            projected.Add(action with
            {
                SurfaceSrc = surface.DocumentUri.ToString(),
                SurfaceToken = surface.SurfaceToken,
                SurfaceTitle = LocalizeTitle(action.DisplayName, action.ActionId),
            });
        }
        return projected;
    }

    private static string LocalizeTitle(IReadOnlyDictionary<string, string> displayName, string fallback)
    {
        foreach (string locale in new[] { "zh-CN", "en-US" })
        {
            if (displayName.TryGetValue(locale, out string? value) && !string.IsNullOrWhiteSpace(value))
            {
                return value;
            }
        }
        return displayName.Values.FirstOrDefault() ?? fallback;
    }

    private JsonElement ProjectCustomViews(PluginRuntimeSnapshot snapshot, bool registered, PluginProjectContext? context)
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
        bool native = snapshot.Manifest.Compatibility.TryGetProperty("pluginApi", out var api) && api.GetString() == "3.x";
        foreach (JsonObject view in candidates)
        {
            view.Remove("src");
            view.Remove("surfaceToken");
            if (!registered) continue;
            string? entry = ReadNodeString(view["entry"]);
            if (string.IsNullOrWhiteSpace(entry)
                || new[] { "entry", "html", "script", "style" }.Any(key =>
                    ReadNodeString(view[key]) is string resource && PluginResourceHost.IsHostReservedPath(resource)))
            {
                continue;
            }
            string? actionId = ReadNodeString(view["actionId"]);
            if (native && (string.IsNullOrWhiteSpace(actionId)
                || !snapshot.Manifest.Actions.Any(action => action.ActionId == actionId))) continue;
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
                entry, actionId);
            if (context is not null)
                _surfaces.BindIdentity(surface.SurfaceToken, context, snapshot.Revision,
                    snapshot.Manifest.Compatibility.TryGetProperty("pluginApi", out var version) ? version.GetString() ?? "" : "", snapshot.Version, actionId,
                    snapshot.Manifest.Actions.FirstOrDefault(action => action.ActionId == actionId)?.Risk ?? "", entry, installationId: snapshot.InstallationId);
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

    internal PluginEventEnvelope ProjectCatalogEvent(JsonElement payload)
    {
        PluginEventEnvelope envelope = payload.Deserialize<PluginEventEnvelope>(JsonOptions)
            ?? throw new JsonException("Invalid plugin catalog event.");
        PluginRuntimeSnapshot snapshot = envelope.Snapshot.Deserialize<PluginRuntimeSnapshot>(JsonOptions)
            ?? throw new JsonException("Invalid plugin catalog snapshot.");
        if (envelope.Contract != PluginContractVersions.Event || envelope.EventType != "plugin.catalog.changed"
            || envelope.ProjectKey != snapshot.ProjectKey || envelope.EntityId != snapshot.PluginId
            || envelope.Revision != snapshot.Revision || envelope.Revision < 1
            || _projectContext()?.ProjectKey != envelope.ProjectKey)
            throw new JsonException("Plugin catalog event identity is invalid.");
        AdvanceCatalogProjection();
        snapshot = SanitizeSnapshot(snapshot);
        return envelope with { Snapshot = JsonSerializer.SerializeToElement(snapshot, JsonOptions) };
    }

    private static PluginRuntimeSnapshot SanitizeSnapshot(PluginRuntimeSnapshot snapshot)
    {
        // Recoverable SSE carries history. It invalidates the renderer catalog;
        // only an authority list/lifecycle result may bind or retire surfaces.
        var ui = JsonNode.Parse(snapshot.Manifest.Ui.GetRawText())!.AsObject();
        if (ui["customViews"] is JsonArray views)
            foreach (JsonObject view in views.OfType<JsonObject>())
            {
                view.Remove("src");
                view.Remove("surfaceToken");
            }
        snapshot = snapshot with
        {
            SourceLocation = HostManagedSource,
            Manifest = snapshot.Manifest with
            {
                Ui = JsonSerializer.SerializeToElement(ui, JsonOptions),
                Actions = snapshot.Manifest.Actions.Select(action => action with
                    { SurfaceSrc = null, SurfaceToken = null, SurfaceTitle = null }).ToArray(),
            },
        };
        return snapshot;
    }

    private void OnTaskChanged(IPluginRpcGateway gateway, PluginEventEnvelope envelope)
    {
        PluginEventEnvelope? projected = _taskRegistry.ApplyExecutionReport(
            gateway, envelope);
        if (projected is null)
        {
            SafeDiagnosticTrace(
                "Plugin task report was rejected; " +
                $"taskId={envelope.EntityId}");
            return;
        }
        if (projected.Snapshot.Deserialize<PluginRuntimeTaskSnapshot>(JsonOptions) is { } task
            && task.State is ("succeeded" or "failed" or "cancelled" or "aborted"))
            _ = ObserveRevokeRunAsync(gateway, task.RunId);
        _reply.PostNotification("plugin.task.changed", projected);
    }

    private void OnInteractionRequested(
        IPluginRpcGateway gateway,
        PluginEventEnvelope envelope)
    {
        if (!_taskRegistry.RecordInteraction(gateway, envelope))
        {
            SafeDiagnosticTrace(
                "Plugin interaction was rejected; " +
                $"runId={envelope.EntityId}");
            return;
        }
        _reply.PostNotification("plugin.interaction.requested", envelope);
    }

    private void OnGatewayTerminated(IPluginRpcGateway gateway)
    {
        // A dead Python client retires its plan bindings immediately; the
        // shared Go epoch stays alive and later bindings start a new client.
        ReleaseLeases(_installLeases.RetireGateway(gateway));
        PostSettlements(_taskRegistry.SettleGateway(gateway));
    }

    private void PostSettlements(IReadOnlyList<HostPluginTaskSettlement> settlements)
    {
        foreach (HostPluginTaskSettlement settlement in settlements)
        {
            _ = ObserveRevokeRunAsync(
                _taskRegistry.GatewayForTask(settlement.Snapshot.TaskId), settlement.Snapshot.RunId);
            _reply.PostNotification("plugin.task.changed", settlement.Envelope);
        }
    }

    private void PostSingleSettlement(HostPluginTaskSettlement? settlement)
    {
        if (settlement is not null)
        {
            _ = ObserveRevokeRunAsync(
                _taskRegistry.GatewayForTask(settlement.Snapshot.TaskId), settlement.Snapshot.RunId);
            _reply.PostNotification("plugin.task.changed", settlement.Envelope);
        }
    }

    private async Task ObserveRevokeRunAsync(IPluginRpcGateway? gateway, string runId)
    {
        if (gateway is null) return;
        try { await gateway.RevokeRunFileGrantsAsync(runId).ConfigureAwait(false); }
        catch (Exception ex)
        {
            SafeTrace(() => Trace.TraceError(
                $"Plugin run file grant cleanup failed: {ex}"));
        }
    }
    private async void OnFileRequested(IPluginRpcGateway gateway, PluginEventEnvelope envelope)
    {
        string? selectedPath = null;
        string? requestId = null;
        try
        {
            var request = envelope.Snapshot.Deserialize<PluginRuntimeFileRequest>(
                HostPluginTaskRegistry.PluginTaskJson.Options)
                ?? throw new JsonException("Plugin file request did not deserialize.");
            requestId = request.RequestId;
            if (!_taskRegistry.RecordFileRequest(gateway, envelope, request))
            {
                SafeDiagnosticTrace(
                    "Plugin file request was rejected; " +
                    $"requestId={request.RequestId}");
                return;
            }
            if (_filePicker is not null)
            {
                selectedPath = await _filePicker.PickAsync(request, CancellationToken.None);
            }
            if (_taskRegistry.TryBeginFileResolve(request, out CancellationToken runToken) is not
                IPluginRpcGateway current)
            {
                SafeDiagnosticTrace(
                    "Plugin file selection was dropped as stale; " +
                    $"requestId={request.RequestId}");
                return;
            }
            await current.ResolveFileAsync(
                request, selectedPath,
                runToken);
        }
        catch (Exception ex)
        {
            SafeTrace(() => Trace.TraceError($"Plugin file request failed: {ex}"));
        }
        finally
        {
            if (requestId is not null) _taskRegistry.ForgetFileRequest(requestId);
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
        if (_taskChangedHandler is not null) gateway.TaskChanged -= _taskChangedHandler;
        if (_interactionRequestedHandler is not null)
            gateway.InteractionRequested -= _interactionRequestedHandler;
        if (_fileRequestedHandler is not null) gateway.FileRequested -= _fileRequestedHandler;
        if (_gatewayTerminatedHandler is not null) gateway.Terminated -= _gatewayTerminatedHandler;
        _taskChangedHandler = null;
        _interactionRequestedHandler = null;
        _fileRequestedHandler = null;
        _gatewayTerminatedHandler = null;
        PostSettlements(_taskRegistry.ClearGateway(gateway));
    }

    private HostPluginTaskBinding CaptureTaskBinding()
    {
        HostPluginTaskBinding? binding = _taskRegistry.Capture();
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

    private void AdmitInstallPlan(
        HostInstallPlanBinding binding,
        PluginRuntimeInstallPlan plan,
        DownloadedPluginPackage? package)
    {
        if (!_installLeases.TryAdmit(binding, plan, package, out HostInstallPlanLease? replaced))
        {
            // Nothing was admitted and Python keeps no plan, so the orphaned
            // download is the only resource to release.
            package?.Dispose();
            ReleaseLease(replaced);
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
            rejected?.Package?.Dispose();
            throw StaleInstallPlan();
        }
        return operation;
    }

    private static PluginDispatchException StaleInstallPlan() => new(
        "PLUGIN_INSTALL_PLAN_STALE",
        "Plugin install plan is stale for the current project session.");

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
            lease.Package?.Dispose();
        }
    }

    private void ReleaseLease(HostInstallPlanLease? lease)
        => lease?.Package?.Dispose();

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
