using System.Diagnostics;
using VibeTable.Infrastructure.Diagnostics;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Selects one closed renderer request module and schedules its transport
/// lifecycle. Business payload interpretation belongs to the selected module.
/// </summary>
public sealed class WorkspaceRequestDispatcher
{
    private readonly IWebReplySink _reply;
    private readonly WorkspaceTableRequestController _tableController;
    private readonly ProductDataRequestController _productController;
    private readonly GridRequestController _gridController;
    private readonly DashboardRequestController _dashboardController;
    private readonly SurfaceRequestController _surfaceController;
    private readonly DocumentBrowseRequestController _documentController;
    private readonly RequestRoute[] _routes;
    private CancellationToken _workspaceSessionToken;

    public WorkspaceRequestDispatcher(
        TableWorkspaceService workspace,
        IDatabasePicker picker,
        IWebReplySink reply,
        GridStateCoordinator? coordinator = null,
        TimeSpan? dashboardRequestTimeout = null,
        TimeSpan? readRecoveryTimeout = null,
        WorkspaceSessionEnvelopeFilter? sessionEnvelopeFilter = null,
        TimeSpan? schemaLifecycleTimeout = null,
        TimeProvider? timeProvider = null)
    {
        ArgumentNullException.ThrowIfNull(workspace);
        ArgumentNullException.ThrowIfNull(picker);
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        TimeSpan correlatedRequestTimeout =
            dashboardRequestTimeout ?? TimeSpan.FromSeconds(60);
        _productController = new ProductDataRequestController(
            _reply,
            readRecoveryTimeout,
            sessionEnvelopeFilter);
        _tableController = new WorkspaceTableRequestController(
            workspace,
            picker,
            _reply,
            () => _productController.CurrentGateway,
            readRecoveryTimeout,
            ResolveSchemaLifecycleTimeout(schemaLifecycleTimeout),
            () => _workspaceSessionToken,
            timeProvider);
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
            new(WorkspaceTableRequestController.Handles, _tableController.DispatchAsync),
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
