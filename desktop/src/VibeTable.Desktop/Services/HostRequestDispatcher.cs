using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal interface IHostLifecycleActions
{
    void RendererReady();

    void RequestExit();

    void RetryStartup();

    bool OpenAdmin();

    Task BuildDiagnosticsAsync(RoutedWebRequest request);
}

/// <summary>
/// Owns the host-lifecycle request contract, including strict payload
/// validation and stable renderer failures. WPF supplies lifecycle actions.
/// </summary>
internal sealed class HostLifecycleRequestController
{
    private readonly IWebReplySink _reply;
    private readonly IHostLifecycleActions _host;

    public HostLifecycleRequestController(
        IWebReplySink reply,
        IHostLifecycleActions host)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _host = host ?? throw new ArgumentNullException(nameof(host));
    }

    public static bool Handles(string requestType) =>
        requestType is
            "app.ready" or
            "host.startupCancelRequested" or
            "host.startupRetryRequested" or
            "admin.openRequested" or
            "diagnostics.get";

    public void Dispatch(RoutedWebRequest request)
    {
        switch (request.Type)
        {
            case "app.ready":
                _host.RendererReady();
                return;
            case "host.startupCancelRequested":
                _host.RequestExit();
                return;
            case "host.startupRetryRequested":
                _host.RetryStartup();
                return;
            case "admin.openRequested":
                if (!_host.OpenAdmin())
                {
                    _reply.PostOperationFailed(
                        request.RequestId,
                        "当前版本没有可安全打开的本地管理页面。",
                        "ADMIN_UNAVAILABLE");
                }
                return;
            case "diagnostics.get":
                if (!HasEmptyObjectPayload(request.Payload))
                {
                    _reply.PostOperationFailed(
                        request.RequestId,
                        "The diagnostics request is invalid.",
                        "DIAGNOSTICS_BAD_PAYLOAD");
                    return;
                }
                _ = _host.BuildDiagnosticsAsync(request);
                return;
            default:
                _reply.PostOperationFailed(
                    request.RequestId,
                    $"Unhandled request type '{request.Type}'.",
                    "UNKNOWN_TYPE");
                return;
        }
    }

    private static bool HasEmptyObjectPayload(JsonElement payload) =>
        payload.ValueKind == JsonValueKind.Object
        && !payload.EnumerateObject().Any();
}

internal sealed class HostLifecycleActions(
    Action rendererReady,
    Action requestExit,
    Action retryStartup,
    Func<bool> openAdmin,
    Func<RoutedWebRequest, Task> buildDiagnostics) : IHostLifecycleActions
{
    public void RendererReady() => rendererReady();

    public void RequestExit() => requestExit();

    public void RetryStartup() => retryStartup();

    public bool OpenAdmin() => openAdmin();

    public Task BuildDiagnosticsAsync(RoutedWebRequest request) =>
        buildDiagnostics(request);
}

/// <summary>
/// Routes one renderer request to one closed product module. Each module owns
/// its route set; this dispatcher only composes descriptors and reports an
/// unknown top-level message.
/// </summary>
internal sealed class HostRequestDispatcher
{
    private readonly IWebReplySink _reply;
    private readonly RequestRoute[] _routes;
    private readonly Action<RoutedWebRequest>? _beforeDispatch;

    public HostRequestDispatcher(
        IWebReplySink reply,
        HostLifecycleRequestController lifecycle,
        WorkspaceProductController workspaceProduct,
        ApplicationRequestController application,
        DocumentRequestController documents,
        NativeProductFileRequestController nativeFiles,
        PluginRequestDispatcher plugins,
        WorkspaceRequestDispatcher workspace,
        Action<RoutedWebRequest>? beforeDispatch = null)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        ArgumentNullException.ThrowIfNull(lifecycle);
        ArgumentNullException.ThrowIfNull(workspaceProduct);
        ArgumentNullException.ThrowIfNull(application);
        ArgumentNullException.ThrowIfNull(documents);
        ArgumentNullException.ThrowIfNull(nativeFiles);
        ArgumentNullException.ThrowIfNull(plugins);
        ArgumentNullException.ThrowIfNull(workspace);
        _beforeDispatch = beforeDispatch;
        _routes =
        [
            new(HostLifecycleRequestController.Handles, lifecycle.Dispatch),
            new(WorkspaceProductController.Handles,
                request => _ = workspaceProduct.DispatchAsync(request)),
            new(ApplicationRequestController.Handles,
                request => _ = application.DispatchAsync(request)),
            new(DocumentRequestController.Handles,
                request => _ = documents.DispatchAsync(request)),
            new(NativeProductFileRequestController.Handles,
                request => _ = nativeFiles.DispatchAsync(request)),
            new(PluginRequestDispatcher.Handles, plugins.Dispatch),
            new(workspace.Handles, workspace.Dispatch),
        ];
    }

    public void Dispatch(RoutedWebRequest request)
    {
        ArgumentNullException.ThrowIfNull(request);
        _beforeDispatch?.Invoke(request);
        foreach (RequestRoute route in _routes)
        {
            if (!route.Handles(request.Type)) continue;
            route.Dispatch(request);
            return;
        }
        _reply.PostOperationFailed(
            request.RequestId,
            $"Unhandled request type '{request.Type}'.",
            "UNKNOWN_TYPE");
    }

    private sealed record RequestRoute(
        Func<string, bool> Handles,
        Action<RoutedWebRequest> Dispatch);
}
