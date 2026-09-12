using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal enum RendererReadyPhase { Shell, Business }

internal interface IHostLifecycleActions
{
    void RendererReady(RendererReadyPhase phase);

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
                if (!TryReadReadyPhase(request.Payload, out RendererReadyPhase phase))
                {
                    _reply.PostOperationFailed(request.RequestId,
                        "The renderer readiness request is invalid.", "APP_READY_BAD_PAYLOAD");
                    return;
                }
                _host.RendererReady(phase);
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

    private static bool TryReadReadyPhase(JsonElement payload, out RendererReadyPhase phase)
    {
        phase = RendererReadyPhase.Shell;
        if (HasEmptyObjectPayload(payload)) return true; // Legacy startup handshake only.
        if (payload.ValueKind != JsonValueKind.Object || payload.EnumerateObject().Count() != 1
            || !payload.TryGetProperty("phase", out JsonElement value)
            || value.ValueKind != JsonValueKind.String) return false;
        if (value.GetString() == "shell") return true;
        if (value.GetString() != "business") return false;
        phase = RendererReadyPhase.Business;
        return true;
    }
}

internal sealed class HostLifecycleActions(
    Action<RendererReadyPhase> rendererReady,
    Action requestExit,
    Action retryStartup,
    Func<bool> openAdmin,
    Func<RoutedWebRequest, Task> buildDiagnostics) : IHostLifecycleActions
{
    public void RendererReady(RendererReadyPhase phase) => rendererReady(phase);

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
        DeviceSettingsRequestController deviceSettings,
        GridPresentationRequestController gridPresentation,
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
        ArgumentNullException.ThrowIfNull(deviceSettings);
        ArgumentNullException.ThrowIfNull(gridPresentation);
        _beforeDispatch = beforeDispatch;
        _routes =
        [
            new(HostLifecycleRequestController.Handles, lifecycle.Dispatch),
            new(WorkspaceProductController.Handles,
                request => _ = workspaceProduct.DispatchAsync(request)),
            new(ApplicationRequestController.Handles,
                request => _ = application.DispatchAsync(request)),
            new(DeviceSettingsRequestController.Handles,
                request => _ = deviceSettings.DispatchAsync(request)),
            new(GridPresentationRequestController.Handles,
                request => _ = gridPresentation.DispatchAsync(request)),
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
