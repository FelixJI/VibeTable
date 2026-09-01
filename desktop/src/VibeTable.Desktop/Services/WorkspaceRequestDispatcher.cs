using System.Diagnostics;
using VibeTable.Infrastructure.Diagnostics;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Selects one closed renderer request module and schedules its transport
/// lifecycle. Business payload interpretation belongs to the selected module.
/// </summary>
public sealed class WorkspaceRequestDispatcher :
    IDisposable,
    IProductSidecarForwarderBinding
{
    private readonly IWebReplySink _reply;
    private readonly DatabaseOpenTerminalPublisher _terminals;
    private readonly WorkspaceTableRequestController _tableController;
    private readonly ProductDataRequestController _productController;
    private readonly GridRequestController _gridController;
    private readonly DashboardRequestController _dashboardController;
    private readonly SurfaceRequestController _surfaceController;
    private readonly DocumentBrowseRequestController _documentController;
    private readonly RequestRoute[] _routes;
    private readonly PluginProjectContextBindingRegistry _pluginBindings;
    private readonly ProductAuthorityEpoch _authority;
    private readonly bool _ownsAuthority;
    private readonly bool _ownsPluginBindings;
    private readonly object _productSidecarBindingGate = new();
    private CancellationToken _workspaceSessionToken;
    private IProductSidecarRpcForwarder? _productSidecarForwarder;

    public WorkspaceRequestDispatcher(
        TableWorkspaceService workspace,
        IDatabasePicker picker,
        IWebReplySink reply,
        GridStateCoordinator coordinator,
        TimeSpan? dashboardRequestTimeout = null,
        TimeSpan? readRecoveryTimeout = null,
        WorkspaceSessionEnvelopeFilter? sessionEnvelopeFilter = null,
        TimeSpan? schemaLifecycleTimeout = null,
        TimeProvider? timeProvider = null,
        Func<PluginProjectContext?>? pluginContext = null,
        ProductAuthorityEpoch? authority = null,
        PluginProjectContextBindingRegistry? databaseOpens = null)
        : this(
            workspace,
            picker,
            reply,
            coordinator ?? throw new ArgumentNullException(nameof(coordinator)),
            true,
            dashboardRequestTimeout,
            readRecoveryTimeout,
            sessionEnvelopeFilter,
            schemaLifecycleTimeout,
            timeProvider,
            pluginContext,
            authority,
            databaseOpens)
    {
    }

    public WorkspaceRequestDispatcher(
        TableWorkspaceService workspace,
        IDatabasePicker picker,
        IWebReplySink reply,
        NoDatabaseOpenRoute noDatabaseOpenRoute,
        TimeSpan? dashboardRequestTimeout = null,
        TimeSpan? readRecoveryTimeout = null,
        WorkspaceSessionEnvelopeFilter? sessionEnvelopeFilter = null,
        TimeSpan? schemaLifecycleTimeout = null,
        TimeProvider? timeProvider = null,
        Func<PluginProjectContext?>? pluginContext = null,
        ProductAuthorityEpoch? authority = null,
        PluginProjectContextBindingRegistry? databaseOpens = null)
        : this(
            workspace,
            picker,
            reply,
            null,
            false,
            dashboardRequestTimeout,
            readRecoveryTimeout,
            sessionEnvelopeFilter,
            schemaLifecycleTimeout,
            timeProvider,
            pluginContext,
            authority,
            databaseOpens)
    {
        ArgumentNullException.ThrowIfNull(noDatabaseOpenRoute);
    }

    private WorkspaceRequestDispatcher(
        TableWorkspaceService workspace,
        IDatabasePicker picker,
        IWebReplySink reply,
        GridStateCoordinator? coordinator,
        bool databaseOpenEnabled,
        TimeSpan? dashboardRequestTimeout,
        TimeSpan? readRecoveryTimeout,
        WorkspaceSessionEnvelopeFilter? sessionEnvelopeFilter,
        TimeSpan? schemaLifecycleTimeout,
        TimeProvider? timeProvider,
        Func<PluginProjectContext?>? pluginContext,
        ProductAuthorityEpoch? authority,
        PluginProjectContextBindingRegistry? databaseOpens)
    {
        ArgumentNullException.ThrowIfNull(workspace);
        ArgumentNullException.ThrowIfNull(picker);
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _terminals = new DatabaseOpenTerminalPublisher(
            _reply,
            message => Trace.TraceError(message));
        _authority = authority ?? new ProductAuthorityEpoch();
        _ownsAuthority = authority is null;
        _pluginBindings = databaseOpens
            ?? new PluginProjectContextBindingRegistry(_authority);
        _ownsPluginBindings = databaseOpens is null;
        PluginProjectContext? initialContext = pluginContext?.Invoke();
        if (_ownsAuthority) _authority.Transition(initialContext);
        if (_ownsPluginBindings)
            _pluginBindings.SetAfterAuthorityTransition(initialContext);
        TimeSpan correlatedRequestTimeout =
            dashboardRequestTimeout ?? TimeSpan.FromSeconds(60);
        _productController = new ProductDataRequestController(
            _reply,
            readRecoveryTimeout,
            sessionEnvelopeFilter);
        _tableController = databaseOpenEnabled
            ? new WorkspaceTableRequestController(
                workspace,
                picker,
                _reply,
                () => _productController.CurrentGateway,
                coordinator!,
                readRecoveryTimeout,
                ResolveSchemaLifecycleTimeout(schemaLifecycleTimeout),
                () => _workspaceSessionToken,
                timeProvider,
                _pluginBindings)
            : new WorkspaceTableRequestController(
                workspace,
                picker,
                _reply,
                () => _productController.CurrentGateway,
                NoDatabaseOpenRoute.Instance,
                readRecoveryTimeout,
                ResolveSchemaLifecycleTimeout(schemaLifecycleTimeout),
                () => _workspaceSessionToken,
                timeProvider,
                _pluginBindings);
        _gridController = new GridRequestController(coordinator, _reply);
        _dashboardController = new DashboardRequestController(
            _reply,
            correlatedRequestTimeout,
            () => _workspaceSessionToken);
        _surfaceController = new SurfaceRequestController(
            _reply,
            correlatedRequestTimeout,
            () => _workspaceSessionToken);
        _documentController = new DocumentBrowseRequestController(
            _reply,
            readRecoveryTimeout);
        _routes =
        [
            new(_tableController.HandlesRequest, _tableController.DispatchAsync),
            new(ProductDataRequestController.Handles, _productController.DispatchAsync),
            new(GridRequestController.Handles, _gridController.DispatchAsync),
            new(DashboardRequestController.Handles, _dashboardController.DispatchAsync),
            new(SurfaceRequestController.Handles, _surfaceController.DispatchAsync),
            new(DocumentBrowseRequestController.Handles, _documentController.DispatchAsync),
        ];
    }

    public void SetProductDataGateway(IProductDataRpcGateway gateway)
        => _productController.SetGateway(gateway);

    public bool ClearProductDataGateway(IProductDataRpcGateway expected)
        => _productController.ClearGateway(expected);

    internal void SetProductSidecarForwarder(
        IProductSidecarRpcForwarder forwarder)
    {
        ArgumentNullException.ThrowIfNull(forwarder);
        lock (_productSidecarBindingGate)
        {
            _productController.SetProductSidecarForwarder(forwarder);
            _productSidecarForwarder = forwarder;
        }
    }

    internal bool ClearProductSidecarForwarder(
        IProductSidecarRpcForwarder expected)
    {
        ArgumentNullException.ThrowIfNull(expected);
        lock (_productSidecarBindingGate)
        {
            if (!ReferenceEquals(_productSidecarForwarder, expected))
                return false;
            if (!_productController.ClearProductSidecarForwarder(expected))
                return false;
            _productSidecarForwarder = null;
            return true;
        }
    }

    bool IProductSidecarForwarderBinding.TryReplace(
        IProductSidecarRpcForwarder? expected,
        IProductSidecarRpcForwarder replacement)
    {
        ArgumentNullException.ThrowIfNull(replacement);
        lock (_productSidecarBindingGate)
        {
            if (!ReferenceEquals(_productSidecarForwarder, expected))
                return false;
            _productController.SetProductSidecarForwarder(replacement);
            _productSidecarForwarder = replacement;
            return true;
        }
    }

    bool IProductSidecarForwarderBinding.Clear(
        IProductSidecarRpcForwarder expected)
        => ClearProductSidecarForwarder(expected);

    public void SetPluginProjectContext(
        PluginProjectContext? context,
        CancellationToken sessionToken = default)
    {
        _authority.Transition(context, sessionToken);
        PostRetiredDatabaseOpenCancellations(
            RetireDatabaseOpensAfterAuthorityTransition(context, sessionToken));
    }

    internal IReadOnlyList<string> RetireDatabaseOpensAfterAuthorityTransition(
        PluginProjectContext? context,
        CancellationToken sessionToken = default)
        => _pluginBindings.SetAfterAuthorityTransition(context, sessionToken);

    internal void PostRetiredDatabaseOpenCancellations(
        IReadOnlyList<string> openIds) =>
        _terminals.PostRetiredCancellations(openIds, "project-context-changed");

    public void Dispose()
    {
        if (_ownsPluginBindings) _pluginBindings.Dispose();
        if (_ownsAuthority) _authority.Dispose();
    }

    public void SetDashboardGateway(
        IDashboardRpcGateway gateway,
        CancellationToken sessionToken = default)
    {
        _workspaceSessionToken = sessionToken;
        _dashboardController.SetGateway(gateway, sessionToken);
    }

    public void SetSurfaceGateway(ISurfaceRpcGateway gateway)
        => _surfaceController.SetGateway(gateway);

    public void SetDocumentWorkspace(WorkspaceDocumentOsAdapter documents)
        => _documentController.SetWorkspace(documents);

    public void RotateDocumentCapabilityEpoch()
        => _documentController.RotateCapabilityEpoch();

    public void Dispatch(RoutedWebRequest request)
        => RunInBackground(request, () => DispatchAsync(request));

    internal Task DispatchAsyncForTesting(RoutedWebRequest request)
        => DispatchAsync(request);

    public bool Handles(string requestType) =>
        _routes.Any(route => route.Handles(requestType));

    internal static TimeSpan ResolveSchemaLifecycleTimeout(TimeSpan? configured)
        => configured ?? SchemaLifecycleBudget.DefaultTimeout;

    private async Task DispatchAsync(RoutedWebRequest request)
    {
        foreach (RequestRoute route in _routes)
        {
            if (!route.Handles(request.Type)) continue;
            await route.DispatchAsync(request).ConfigureAwait(false);
            return;
        }
        if (!string.Equals(request.Type, "app.ready", StringComparison.Ordinal))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                $"Unhandled request type '{request.Type}'.",
                "UNKNOWN_TYPE");
        }
    }

    private void RunInBackground(
        RoutedWebRequest request,
        Func<Task> operation)
    {
        try
        {
            // Starting the async module synchronously lets correlated modules
            // register operation identities before a following cancel message.
            // The first real I/O still yields immediately to the renderer.
            _ = ObserveAsync(request, operation());
        }
        catch (Exception)
        {
            PostUnhandledFailure(request);
        }
    }

    private async Task ObserveAsync(RoutedWebRequest request, Task operation)
    {
        try
        {
            await operation.ConfigureAwait(false);
        }
        catch (Exception)
        {
            PostUnhandledFailure(request);
        }
    }

    private void PostUnhandledFailure(RoutedWebRequest request)
    {
        Trace.TraceError(DiagnosticEvent.Failure(
            "VibeTable.Desktop.WorkspaceRequestDispatcher",
            request.Type,
            "WORKSPACE_OPERATION_FAILED"));
        _reply.PostOperationFailed(
            request.RequestId,
            "Workspace operation failed.",
            "WORKSPACE_ERROR",
            request.Type);
    }

    private sealed record RequestRoute(
        Func<string, bool> Handles,
        Func<RoutedWebRequest, Task> DispatchAsync);
}
