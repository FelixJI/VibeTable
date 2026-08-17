using System;
using System.Collections.Generic;
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
    private readonly Dictionary<string, DownloadedPluginPackage> _remotePlans =
        new(StringComparer.Ordinal);
    private IPluginRpcGateway? _gateway;
    private bool _disposed;

    public PluginRequestDispatcher(
        IWebReplySink reply,
        PluginSurfaceSessionManager surfaces,
        IPluginPackageSourcePicker packagePicker,
        PluginWebViewResourceHost resourceHost,
        IPluginFilePicker? filePicker = null)
        : this(reply, surfaces, packagePicker, resourceHost, filePicker, null, null)
    {
    }

    internal PluginRequestDispatcher(
        IWebReplySink reply,
        PluginSurfaceSessionManager surfaces,
        IPluginPackageSourcePicker packagePicker,
        PluginWebViewResourceHost resourceHost,
        IPluginFilePicker? filePicker,
        IGitHubPluginPackageSource? githubSource,
        Action<string>? diagnosticTrace = null)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _surfaces = surfaces ?? throw new ArgumentNullException(nameof(surfaces));
        _packagePicker = packagePicker ?? throw new ArgumentNullException(nameof(packagePicker));
        _resourceHost = resourceHost ?? throw new ArgumentNullException(nameof(resourceHost));
        _filePicker = filePicker;
        _githubSource = githubSource;
        _diagnosticTrace = diagnosticTrace;
    }

    public event Action<PluginSurfaceEvent>? SurfaceEventReceived;

    public static bool Handles(string requestType) =>
        requestType.StartsWith("plugin.", StringComparison.Ordinal);

    public bool HasGateway => _gateway is not null;

    public void SetGateway(IPluginRpcGateway gateway)
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        ArgumentNullException.ThrowIfNull(gateway);
        DetachGateway();
        _gateway = gateway;
        _gateway.CatalogChanged += OnCatalogChanged;
        _gateway.TaskChanged += OnTaskChanged;
        _gateway.InteractionRequested += OnInteractionRequested;
        _gateway.FileRequested += OnFileRequested;
    }

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
            if (_gateway is null)
            {
                _reply.PostOperationFailed(
                    request.RequestId,
                    "Plugin services are not available for the current project.",
                    "PLUGIN_NOT_READY");
                return;
            }

            object result = request.Type switch
            {
                "plugin.catalog.list" => await ListCatalogAsync(
                    Read<PluginCatalogListParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.audit.list" => await _gateway.ListAuditAsync(
                    Read<PluginAuditListParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.cleanup.listPending" => await _gateway.ListPendingCleanupAsync(
                    Read<PluginCatalogListParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.install.inspect" => await InspectInstallAsync(
                    Read<PluginInspectInstallParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.install.github.inspect" => await InspectGitHubInstallAsync(
                    Read<PluginGitHubInspectParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.install.commit" => await CommitInstallAsync(
                    Read<PluginCommitInstallParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.install.cancel" => CancelInstall(
                    Read<PluginInstallCancelParams>(request.Payload)),
                "plugin.lifecycle.setEnabled" => ProjectSnapshot(await _gateway.SetEnabledAsync(
                    Read<PluginSetEnabledParams>(request.Payload), token).ConfigureAwait(false)),
                "plugin.lifecycle.upgrade" => await UpgradeAsync(
                    Read<PluginUpgradeParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.lifecycle.rollback" => ProjectSnapshot(await _gateway.RollbackAsync(
                    Read<PluginRollbackParams>(request.Payload), token).ConfigureAwait(false)),
                "plugin.lifecycle.uninstall" => await UninstallAsync(
                    Read<PluginUninstallParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.action.describe" => await _gateway.DescribeActionAsync(
                    Read<PluginDescribeActionParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.action.start" => await _gateway.StartActionAsync(
                    Read<PluginStartActionParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.interaction.resolve" => await _gateway.ResolveInteractionAsync(
                    Read<PluginResolveInteractionParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.task.cancel" => await _gateway.CancelTaskAsync(
                    Read<PluginTaskParams>(request.Payload), token).ConfigureAwait(false),
                "plugin.task.get" => await _gateway.GetTaskAsync(
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
            Trace.TraceError(DiagnosticEvent.Failure(
                "VibeTable.Desktop.PluginRequestDispatcher",
                request.Type,
                ex.GetType().Name));
            _diagnosticTrace?.Invoke(
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
        if (_disposed)
        {
            return;
        }
        _disposed = true;
        DetachGateway();
        _githubSource?.Dispose();
        _surfaces.CloseAll();
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
        var plan = await _gateway!.InspectInstallAsync(
            request with { SourceLocation = sourceLocation },
            token).ConfigureAwait(false);
        ClearRemotePlans();
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
        DownloadedPluginPackage download = await _githubSource.DownloadLatestAsync(
            request.Repository,
            token).ConfigureAwait(false);
        try
        {
            var plan = await _gateway!.InspectInstallAsync(
                new PluginInspectInstallParams(
                    request.ProjectKey,
                    request.ProjectRevision,
                    download.Path),
                token).ConfigureAwait(false);
            ClearRemotePlans();
            _remotePlans.Add(plan.PlanId, download);
            return plan with { SourceLocation = HostManagedSource };
        }
        catch
        {
            download.Dispose();
            throw;
        }
    }

    private async Task<PluginRuntimeSnapshot> CommitInstallAsync(
        PluginCommitInstallParams request,
        CancellationToken token)
    {
        PluginRuntimeSnapshot snapshot = await _gateway!.CommitInstallAsync(request, token)
            .ConfigureAwait(false);
        ReleaseRemotePlan(request.PlanId);
        return ProjectSnapshot(snapshot);
    }

    private async Task<PluginRuntimeSnapshot> UpgradeAsync(
        PluginUpgradeParams request,
        CancellationToken token)
    {
        PluginRuntimeSnapshot snapshot = await _gateway!.UpgradeAsync(request, token)
            .ConfigureAwait(false);
        ReleaseRemotePlan(request.PlanId);
        return ProjectSnapshot(snapshot);
    }

    private PluginInstallCancelResult CancelInstall(PluginInstallCancelParams request) =>
        new(ReleaseRemotePlan(request.PlanId));

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
            Trace.TraceError($"Plugin catalog event was dropped: {ex}");
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
            Trace.TraceError($"Plugin file request failed: {ex}");
        }
    }

    private void DetachGateway()
    {
        ClearRemotePlans();
        if (_gateway is null)
        {
            return;
        }
        _gateway.CatalogChanged -= OnCatalogChanged;
        _gateway.TaskChanged -= OnTaskChanged;
        _gateway.InteractionRequested -= OnInteractionRequested;
        _gateway.FileRequested -= OnFileRequested;
        _gateway = null;
    }

    private bool ReleaseRemotePlan(string planId)
    {
        if (!_remotePlans.Remove(planId, out DownloadedPluginPackage? package))
        {
            return false;
        }
        package.Dispose();
        return true;
    }

    private void ClearRemotePlans()
    {
        foreach (DownloadedPluginPackage package in _remotePlans.Values)
        {
            package.Dispose();
        }
        _remotePlans.Clear();
    }

    private sealed class PluginDispatchException : Exception
    {
        public PluginDispatchException(string code, string message) : base(message)
            => Code = code;

        public string Code { get; }
    }
}
