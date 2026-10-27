namespace VibeTable.Desktop.Services;

/// <summary>Owns the table request subgraph; shared runtime and session owners are borrowed.</summary>
internal sealed class ProductTableComposition : IDisposable
{
    private GridStateCoordinator? _coordinator;
    private int _sidecarDisposed;
    private int _requestsDisposed;
    private int _workspaceDisposed;

    public ProductTableComposition(
        ProductionWorkspaceRuntimeFactory runtime,
        WorkspaceSessionManager sessions,
        WorkspaceSessionEnvelopeFilter sessionEnvelopeFilter,
        IWebReplySink reply,
        IDatabasePicker picker,
        ProductAuthorityEpoch authority,
        PluginProjectContextBindingRegistry databaseOpens,
        Func<bool> isRendererReady,
        Func<bool> isClosing,
        Func<bool> hasProductGateway,
        Action<string> trace,
        string hostVersion,
        Func<CancellationToken, Task> bindProductGateways)
    {
        ArgumentNullException.ThrowIfNull(runtime);
        ArgumentNullException.ThrowIfNull(sessions);
        ArgumentNullException.ThrowIfNull(sessionEnvelopeFilter);
        ArgumentNullException.ThrowIfNull(reply);
        ArgumentNullException.ThrowIfNull(picker);
        ArgumentNullException.ThrowIfNull(authority);
        ArgumentNullException.ThrowIfNull(databaseOpens);
        ArgumentNullException.ThrowIfNull(isRendererReady);
        ArgumentNullException.ThrowIfNull(isClosing);
        ArgumentNullException.ThrowIfNull(hasProductGateway);
        ArgumentNullException.ThrowIfNull(trace);
        ArgumentNullException.ThrowIfNull(bindProductGateways);
        try
        {
            TableGateway = new LazyProductTableGateway(sessionEnvelopeFilter);
            Workspace = new TableWorkspaceService(TableGateway);
            ProductWorkspace = new ProductWorkspaceController(
                reply, runtime, sessions, picker, Workspace,
                isRendererReady, isClosing, hasProductGateway, trace, hostVersion,
                authority: authority, databaseOpens: databaseOpens,
                sessionEnvelopeFilter: sessionEnvelopeFilter);
            _coordinator = new GridStateCoordinator(TableGateway, ProductWorkspace.OnNotification);
            TableGateway.BindingChanged += _coordinator.ResetForTableChange;
            Workspace.Notification += ProductWorkspace.OnNotification;
            Dispatcher = new WorkspaceRequestDispatcher(
                Workspace, picker, reply, _coordinator,
                sessionEnvelopeFilter: sessionEnvelopeFilter,
                pluginContext: () => PluginProjectContext.FromSession(sessions.Current),
                authority: authority, databaseOpens: databaseOpens);
            SidecarGatewayLifecycle = new ProductSidecarGatewayLifecycle(runtime, Dispatcher);
            runtime.RegisterProductSidecarGatewayLifecycle(SidecarGatewayLifecycle, bindProductGateways);
        }
        catch
        {
            Dispose();
            throw;
        }
    }

    public LazyProductTableGateway TableGateway { get; } = null!;
    public TableWorkspaceService Workspace { get; } = null!;
    public ProductWorkspaceController ProductWorkspace { get; } = null!;
    public WorkspaceRequestDispatcher Dispatcher { get; } = null!;
    public IProductSidecarGatewayLifecycle SidecarGatewayLifecycle { get; } = null!;

    public void DisposeSidecarGateway()
    {
        if (Interlocked.Exchange(ref _sidecarDisposed, 1) == 0)
            SidecarGatewayLifecycle?.Dispose();
    }

    public void DisposeRequests()
    {
        if (Interlocked.Exchange(ref _requestsDisposed, 1) == 0)
            Dispatcher?.Dispose();
    }

    public void DisposeWorkspace()
    {
        if (Interlocked.Exchange(ref _workspaceDisposed, 1) != 0) return;
        if (Workspace is not null && ProductWorkspace is not null)
            Workspace.Notification -= ProductWorkspace.OnNotification;
        if (TableGateway is not null && _coordinator is not null)
            TableGateway.BindingChanged -= _coordinator.ResetForTableChange;
        try { _coordinator?.ResetForTableChange(); }
        finally
        {
            try { ProductWorkspace?.Dispose(); }
            finally { TableGateway?.Dispose(); }
        }
    }

    public void Dispose()
    {
        try { DisposeSidecarGateway(); }
        finally
        {
            try { DisposeRequests(); }
            finally { DisposeWorkspace(); }
        }
    }
}
