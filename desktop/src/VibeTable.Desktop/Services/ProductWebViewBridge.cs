using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using System.Windows;
using System.Windows.Threading;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.Wpf;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Hardened product renderer boundary. It owns one app-origin WebView, a
/// closed message router, plugin iframe resource mapping, and the only native
/// file-object ingress. Filesystem paths never enter renderer JSON.
/// </summary>
public sealed class ProductWebViewBridge : IWebViewBridge, IWebReplySink
{
    private readonly Window _owner;
    private WebView2? _webView;
    /// <summary>
    /// #482 renderer generation. Incremented on control replacement and on
    /// recovery sealing; queued emissions and inbound events capture it and
    /// are dropped when they belong to a retired generation.
    /// </summary>
    internal static readonly TimeSpan HeadlessExecutionBudget = TimeSpan.FromMinutes(2);
    private long _rendererGeneration;
    private CoreWebView2? _currentCore;
    private readonly Dictionary<string, (CoreWebView2Frame Frame, long Generation)> _pluginFrames = new(StringComparer.Ordinal);
    private readonly WebMessageRouter _router;
    private readonly PluginWebViewResourceHost _pluginResources;
    private readonly TestModeReadinessWriter? _readiness;
    private readonly Action<string> _processFailed;
    private readonly string? _isolatedUserDataRoot;
    private readonly bool _stableIsolatedUserDataRoot;
    private readonly object _loadGate = new();
    private Action<Action> _uiEmit;
    private Task<CoreWebView2Environment>? _environment;
    private Task? _loadTask;

    public ProductWebViewBridge(
        Window owner,
        WebView2 webView,
        WebMessageRouter router,
        PluginWebViewResourceHost pluginResources,
        TestModeReadinessWriter? readiness,
        Action<string> processFailed,
        string? isolatedUserDataRoot = null,
        bool stableIsolatedUserDataRoot = false)
    {
        _owner = owner ?? throw new ArgumentNullException(nameof(owner));
        _webView = webView ?? throw new ArgumentNullException(nameof(webView));
        _router = router ?? throw new ArgumentNullException(nameof(router));
        _pluginResources = pluginResources
            ?? throw new ArgumentNullException(nameof(pluginResources));
        _readiness = readiness;
        _processFailed = processFailed
            ?? throw new ArgumentNullException(nameof(processFailed));
        _isolatedUserDataRoot = isolatedUserDataRoot;
        _stableIsolatedUserDataRoot = stableIsolatedUserDataRoot;
        _uiEmit = action => _owner.Dispatcher.BeginInvoke(action);
        Realtime = new ProductRealtimeDelivery(
            (action, token) => _owner.Dispatcher.InvokeAsync(action, DispatcherPriority.Normal, token).Task,
            () => _router.IsReady && _webView.CoreWebView2 is not null,
            PostRealtimeNotification);
    }

    internal ProductRealtimeDelivery Realtime { get; }
    internal PluginSurfaceApi? PluginApi { get; set; }

    /// <summary>
    /// Raised (on the emission path) for every pending native plugin
    /// confirmation. The #482 host chrome listens and shows the native
    /// confirmation window; both the web footer and the window resolve through
    /// the same one-shot registry decision, so neither is a bypass.
    /// </summary>
    internal event Action<object?>? PluginInteractionRequested;

    /// <summary>
    /// Current renderer generation; #482 recovery and tests compare against
    /// it to reject late work from retired renderers.
    /// </summary>
    internal long RendererGeneration => Volatile.Read(ref _rendererGeneration);

    /// <summary>
    /// Raised with each queued emission's commit result so tests can verify
    /// that a retired generation's queued notification was dropped instead of
    /// posted to a newer renderer.
    /// </summary>
    internal event Action<bool>? EmissionCommitted;

    /// <summary>
    /// #482 renderer recovery: detaches from the old control (null) so late
    /// events die with it, or binds a freshly created control for the next
    /// full LoadAsync. The environment/UDF stay; only the control is new. The
    /// renderer generation is retired so queued old notifications can never
    /// reach the new renderer.
    /// </summary>
    internal void AttachControl(WebView2? webView)
    {
        _webView = webView;
        _currentCore = null;
        _pluginFrames.Clear();
        Interlocked.Increment(ref _rendererGeneration);
    }

    /// <summary>
    /// Seals host→web routing for a renderer-only recovery: no new inbound
    /// requests are accepted, realtime delivery retires and the renderer
    /// generation is retired, so a late emission or callback from the old
    /// renderer can never reach the new renderer generation.
    /// </summary>
    internal void SealForRendererRecovery()
    {
        _router.IsReady = false;
        Realtime.Retire();
        Interlocked.Increment(ref _rendererGeneration);
        _currentCore = null;
        _pluginFrames.Clear();
        lock (_loadGate)
        {
            _loadTask = null;
        }
    }


    private void PostRealtimeNotification(string type, object? payload)
    {
        if (!_router.IsHostNotificationAllowed(type))
            throw new InvalidOperationException("Realtime notification is not registered.");
        if (_currentCore is not { } core)
        {
            throw new InvalidOperationException("The WebView control is not available.");
        }
        string json = JsonSerializer.Serialize(new { type, requestId = (string?)null, payload },
            new JsonSerializerOptions(JsonSerializerDefaults.Web));
        core.PostWebMessageAsString(json);
    }

    /// <summary>
    /// Available only while the synchronously routed native-file request is
    /// executing. A consumer must copy it before returning.
    /// </summary>
    public IReadOnlyList<string>? CurrentNativeFilePaths { get; private set; }

    public Task LoadAsync(CancellationToken cancellationToken)
    {
        Task loadTask;
        lock (_loadGate)
        {
            if (_loadTask is null || _loadTask.IsFaulted || _loadTask.IsCanceled)
            {
                _loadTask = LoadCoreAsync();
            }
            loadTask = _loadTask;
        }
        return loadTask.WaitAsync(cancellationToken);
    }

    public void PostNotification(string type, object? payload)
    {
        if (string.Equals(type, "plugin.interaction.requested", StringComparison.Ordinal))
        {
            PluginInteractionRequested?.Invoke(payload);
        }
        PostEnvelope(type, requestId: null, payload);
    }

    public void PostResponse(string type, string? requestId, object? payload)
        => PostEnvelope(type, requestId, payload);

    public void PostWorkspaceV2Response(
        string? requestId,
        object? payload,
        JsonElement wire)
        => PostEnvelope("workspace.v2.response", requestId, payload, wire);

    public void PostWorkspaceV2Event(object? payload, JsonElement wire)
        => PostEnvelope("workspace.v2.event", requestId: null, payload, wire);

    /// <summary>
    /// Queue seam for the guarded reply emissions. Production parks each
    /// emission on the owner's UI dispatcher; tests substitute a controllable
    /// queue so the actual emission callback can be pumped deterministically.
    /// </summary>
    internal Action<Action> UiEmission
    {
        get => _uiEmit;
        set => _uiEmit = value ?? throw new ArgumentNullException(nameof(value));
    }

    public bool TryPostResponse(
        string type,
        string? requestId,
        object? payload,
        Func<Func<bool>, bool>? emitGate = null)
        => PostEnvelope(type, requestId, payload, default, emitGate);

    public bool TryPostOperationFailed(
        string? requestId,
        string message,
        string? code = null,
        string? operation = null,
        string? operationId = null,
        Func<Func<bool>, bool>? emitGate = null)
    {
        EnqueueUiEmission(
            SerializeRouterReply(WebMessageRouter.BuildOperationFailed(
                requestId,
                message,
                code,
                operation,
                operationId)),
            emitGate);
        return true;
    }

    public void PostOperationFailed(
        string? requestId,
        string message,
        string? code = null,
        string? operation = null,
        string? operationId = null)
        => EnqueueUiEmission(
            SerializeRouterReply(WebMessageRouter.BuildOperationFailed(
                requestId,
                message,
                code,
                operation,
                operationId)),
            emitGate: null);

    /// <summary>
    /// Parks one reply emission on the UI queue. The gate — when present — is
    /// invoked inside this callback and wraps the synchronous wire post, so
    /// the scope check and the actual PostWebMessageAsString commit atomically
    /// against the gate's serialization point. A scope retired between
    /// enqueue and pump can therefore never emit its late terminal.
    /// </summary>
    private void EnqueueUiEmission(string json, Func<Func<bool>, bool>? emitGate)
    {
        // #482: the generation is captured at ENQUEUE time. An emission queued
        // by the old renderer (or before a recovery seal) must never be posted
        // to a newer renderer generation.
        long generation = Volatile.Read(ref _rendererGeneration);
        _uiEmit(() =>
        {
            Func<bool> commit = () =>
            {
                bool posted = false;
                if (Volatile.Read(ref _rendererGeneration) == generation
                    && _currentCore is { } core)
                {
                    core.PostWebMessageAsString(json);
                    posted = true;
                }
                EmissionCommitted?.Invoke(posted);
                return posted;
            };
            if (emitGate is null)
            {
                commit();
                return;
            }
            emitGate(commit);
        });
    }

    private async Task LoadCoreAsync()
    {
        _readiness?.Trace("ProductWebView: initializing runtime");
        var control = _webView ?? throw new InvalidOperationException(
            "Renderer recovery has not attached a WebView control.");
        long startGeneration = Volatile.Read(ref _rendererGeneration);
        await control.EnsureCoreWebView2Async(await GetEnvironmentAsync())
            .ConfigureAwait(true);
        // #482: a recovery may have replaced the control while the await was
        // in flight; the stale load must never wire itself as current.
        if (!ReferenceEquals(control, _webView)
            || Volatile.Read(ref _rendererGeneration) != startGeneration)
        {
            throw new InvalidOperationException(
                "Renderer recovery replaced the WebView control during initialization.");
        }
        CoreWebView2 core = control.CoreWebView2
            ?? throw new InvalidOperationException(
                "WebView2 did not provide a CoreWebView2 instance.");
        _currentCore = core;
        ApplyHardening(core);
        await DisableWebRTCBeforeAnyNavigationAsync(core).ConfigureAwait(true);
        if (!ReferenceEquals(control, _webView)
            || Volatile.Read(ref _rendererGeneration) != startGeneration)
        {
            throw new InvalidOperationException(
                "Renderer recovery replaced the WebView control during initialization.");
        }
        core.WebMessageReceived -= OnWebMessageReceived;
        core.WebMessageReceived += OnWebMessageReceived;
        core.FrameCreated -= OnPluginFrameCreated;
        core.FrameCreated += OnPluginFrameCreated;

        var navigation = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        EventHandler<CoreWebView2NavigationCompletedEventArgs>? completed = null;
        completed = (_, args) =>
        {
            if (args.IsSuccess)
            {
                navigation.TrySetResult();
            }
            else
            {
                navigation.TrySetException(new InvalidOperationException(
                    $"Product renderer navigation failed: {args.WebErrorStatus}, " +
                    $"HTTP {args.HttpStatusCode}."));
            }
        };
        core.NavigationCompleted += completed;
        try
        {
            core.Navigate(ResolveAppUri(_readiness is not null));
            await navigation.Task.ConfigureAwait(true);
            _readiness?.Trace("ProductWebView: renderer navigation completed");
        }
        finally
        {
            core.NavigationCompleted -= completed;
        }
    }

    internal static string ResolveAppUri(bool e2eMode)
        => e2eMode
            ? $"{WebViewAssetService.AppOrigin}?vibetable-e2e=1"
            : WebViewAssetService.AppOrigin;

    // Registered through the stable AddScriptToExecuteOnDocumentCreatedAsync API,
    // which runs in every future top-level and child frame before any page
    // script. One script handles both aliases so no ordering is assumed; the
    // bindings are undefined and non-writable/non-configurable, so construction
    // is impossible, redefinition throws, and delete cannot restore them.
    internal const string WebRtcDisableScript =
        """
        (function () {
          Object.defineProperty(window, 'RTCPeerConnection', { value: undefined, writable: false, configurable: false });
          Object.defineProperty(window, 'webkitRTCPeerConnection', { value: undefined, writable: false, configurable: false });
        })();
        """;

    private CoreWebView2? _webRtcDisabledCore;

    private async Task DisableWebRTCBeforeAnyNavigationAsync(CoreWebView2 core)
    {
        if (ReferenceEquals(_webRtcDisabledCore, core)) return;
        // A failed registration must stop navigation instead of shipping an
        // unhardened renderer; await before the first Navigate.
        await core.AddScriptToExecuteOnDocumentCreatedAsync(WebRtcDisableScript)
            .ConfigureAwait(true);
        _webRtcDisabledCore = core;
    }

    private void ApplyHardening(CoreWebView2 core)
    {
        string folder = WebViewAssetService.ResolveWebGridFolder()
            ?? throw new InvalidOperationException(
                "The bundled web-grid assets were not found.");
        core.SetVirtualHostNameToFolderMapping(
            WebViewAssetService.AppHostName,
            folder,
            CoreWebView2HostResourceAccessKind.DenyCors);
        _pluginResources.Attach(core);

        // Downloads are denied by default; only the exact blob URL of one
        // registered dashboard export action is admitted, redirected into the
        // broker-managed temporary file and settled through the gate.
        core.DownloadStarting -= OnDownloadStarting;
        core.DownloadStarting += OnDownloadStarting;

        core.NavigationStarting += (_, args) =>
        {
            if (!WebViewNavigationPolicy.IsAppNavigation(args.Uri))
            {
                args.Cancel = true;
            }
            else
            {
                _router.IsReady = false;
                Realtime.Retire();
            }
        };
        core.FrameNavigationStarting += (_, args) =>
        {
            bool isPlugin = Uri.TryCreate(args.Uri, UriKind.Absolute, out Uri? uri)
                && _pluginResources.IsRegisteredUri(uri);
            if (!isPlugin)
            {
                args.Cancel = true;
            }
        };
        core.NewWindowRequested += (_, args) =>
        {
            args.Handled = true;
            if (Uri.TryCreate(
                    args.OriginalSourceFrameInfo.Source,
                    UriKind.Absolute,
                    out Uri? source)
                && PluginWebViewResourceHost.IsPluginUri(source))
            {
                return;
            }
            if (Uri.TryCreate(args.Uri, UriKind.Absolute, out Uri? target)
                && _pluginResources.IsRegisteredUri(target))
            {
                return;
            }
            switch (WebViewNavigationPolicy.ClassifyAppNewWindow(args.Uri))
            {
                case WebViewLinkDisposition.CurrentView:
                    _owner.Dispatcher.BeginInvoke(() => core.Navigate(args.Uri));
                    break;
                case WebViewLinkDisposition.ExternalBrowser:
                    OpenExternal(args.Uri);
                    break;
            }
        };
#if !DEBUG
        core.Settings.AreDevToolsEnabled = false;
        core.Settings.AreDefaultContextMenusEnabled = false;
        core.Settings.AreBrowserAcceleratorKeysEnabled = false;
#endif
        core.Settings.IsStatusBarEnabled = false;
        core.ProcessFailed += (sender, args) =>
        {
            // #482 generation seal: a late ProcessFailed from a retired
            // (old/disposed) renderer must never tear down the healthy new
            // generation. The check runs synchronously here AND the recovery
            // owner re-verifies after its dispatcher hop.
            if (sender is not CoreWebView2 failedCore || !ReferenceEquals(failedCore, _currentCore))
            {
                _readiness?.Trace(
                    $"Ignored a late renderer failure from a retired generation: kind={args.ProcessFailedKind}");
                return;
            }
            string reason =
                $"WebView2 process failed: kind={args.ProcessFailedKind}; " +
                $"reason={args.Reason}; exitCode={args.ExitCode}; " +
                $"process={args.ProcessDescription}; " +
                $"failureModule={args.FailureSourceModulePath}";
            _readiness?.Trace(reason);
            if (args.ProcessFailedKind == CoreWebView2ProcessFailedKind.GpuProcessExited)
            {
                // WebView2 automatically recreates the GPU process. Treating
                // this recoverable event as a fatal renderer crash makes
                // software-rendered and remote Windows sessions fail startup.
                return;
            }
            _readiness?.WriteError(reason);
            _router.IsReady = false;
            Realtime.Retire();
            _owner.Dispatcher.BeginInvoke(() => _processFailed(reason));
        };
    }

    private void OnDownloadStarting(object? sender, CoreWebView2DownloadStartingEventArgs args)
        => args.Cancel = true;
    internal async Task<bool> PreparePluginPrintAsync(PluginSurfaceSession surface, string preparationId, CancellationToken token)
    {
        CoreWebView2? core = _currentCore;
        long generation = RendererGeneration;
        if (core is null || !WebViewNavigationPolicy.IsAppNavigation(core.Source)
            || !_pluginFrames.TryGetValue(surface.SurfaceToken, out var frame) || frame.Generation != generation) return false;
        // Fixed layout-only probe in the actual frame, never caller HTML/selectors or cross-frame data.
        string layout = await frame.Frame.ExecuteScriptAsync(
            "({source:location.href,height:Math.ceil(Math.max(document.documentElement.scrollHeight,document.body?.scrollHeight??0))})").WaitAsync(token);
        int pixels = ReadPluginPrintHeight(surface.DocumentUri, layout);
        if (generation != RendererGeneration || !ReferenceEquals(core, _currentCore)) return false;
        string script = $"globalThis.__vibetablePluginPrint?.prepare({JsonSerializer.Serialize(surface.SurfaceToken)}, {JsonSerializer.Serialize(preparationId)}, {pixels}) === true";
        string prepared = await core.ExecuteScriptAsync(script).WaitAsync(token);
        return prepared == "true" && generation == RendererGeneration && ReferenceEquals(core, _currentCore);
    }

    internal static int ReadPluginPrintHeight(Uri expected, string layout)
    {
        using var document = JsonDocument.Parse(layout);
        if (document.RootElement.ValueKind != JsonValueKind.Object
            || !document.RootElement.TryGetProperty("source", out var source) || source.ValueKind != JsonValueKind.String
            || !Uri.TryCreate(source.GetString(), UriKind.Absolute, out var current)
            || current.GetLeftPart(UriPartial.Query) != expected.GetLeftPart(UriPartial.Query))
            throw new PluginSurfacePolicyException("plugin_print_denied", "插件打印来源已失效。");
        if (!document.RootElement.TryGetProperty("height", out var height) || height.ValueKind != JsonValueKind.Number
            || !height.TryGetInt32(out int pixels)
            || pixels < 1 || pixels > 100_000)
            throw new PluginSurfacePolicyException("plugin_print_limit", "插件打印内容超出布局预算。");
        return pixels;
    }

    internal async Task ReleasePluginPrintAsync(string preparationId)
    {
        if (_currentCore is not { } core) return;
        try { await core.ExecuteScriptAsync($"globalThis.__vibetablePluginPrint?.release({JsonSerializer.Serialize(preparationId)})"); }
        catch (Exception) { /* A retired renderer has no printable prepared path. */ }
    }

    private void OnPluginFrameCreated(object? sender, CoreWebView2FrameCreatedEventArgs args)
    {
        if (!ReferenceEquals(sender, _currentCore)) return;
        long generation = RendererGeneration;
        CoreWebView2Frame frame = args.Frame;
        // Name was assigned by the trusted app's iframe element before plugin code runs.
        string token = frame.Name;
        if (!_pluginResources.TryGetSurface(token, out var surface) || surface is null || surface.ApiVersion != "3.x") return;
        if (!_pluginResources.MarkSurfaceMounted(token)) return;
        string source = surface.DocumentUri.AbsoluteUri;
        _pluginFrames[token] = (frame, generation);
        bool active = true;
        DispatcherTimer? headlessDeadline = null;
        Action<string>? headlessRetired = null;
        if (surface.DocumentUri.AbsolutePath == "/" + PluginResourceHost.HeadlessDocumentPath)
        {
            // A native timer retires and rebuilds even when the renderer is
            // frozen. Application pages deliberately have no total lifetime.
            headlessDeadline = new DispatcherTimer(HeadlessExecutionBudget, DispatcherPriority.Normal,
                (_, _) =>
                {
                    headlessDeadline?.Stop();
                    if (!active || generation != RendererGeneration || !_pluginResources.TryGetSurface(token, out _)) return;
                    PluginApi?.Retire(token);
                    _pluginResources.CloseSurface(token);
                    _processFailed("headless-execution-timeout");
                }, _owner.Dispatcher);
            headlessRetired = closed =>
            {
                if (closed != token) return;
                headlessDeadline.Dispatcher.BeginInvoke(() => headlessDeadline.Stop());
                _pluginResources.SurfaceClosed -= headlessRetired;
            };
            _pluginResources.SurfaceClosed += headlessRetired;
        }
        frame.NavigationStarting += (_, navigation) =>
        {
            // Single-entry SPA routing: same-document fragment-only changes
            // keep the initial source identity (AC1 internal pages). Any other
            // navigation is cancelled and retires the surface.
            if (navigation.Uri != source
                && !(Uri.TryCreate(navigation.Uri, UriKind.Absolute, out var target)
                    && Uri.TryCreate(source, UriKind.Absolute, out var initial)
                    && string.Equals(
                        target.GetLeftPart(UriPartial.Query),
                        initial.GetLeftPart(UriPartial.Query),
                        StringComparison.Ordinal)))
            {
                navigation.Cancel = true;
                active = false;
                PluginApi?.Retire(token);
                _pluginResources.CloseSurface(token);
            }
        };
        frame.Destroyed += (_, _) =>
        {
            if (_pluginFrames.TryGetValue(token, out var tracked) && ReferenceEquals(tracked.Frame, frame)) _pluginFrames.Remove(token);
            headlessDeadline?.Stop();
            if (headlessRetired is not null) _pluginResources.SurfaceClosed -= headlessRetired;
            active = false;
            PluginApi?.Retire(token);
            _pluginResources.CloseSurface(token);
        };
        PluginApi?.BindFrameNotifier(token, CreatePluginFrameNotifier(
            (action, cancellation) => _owner.Dispatcher.InvokeAsync(action, DispatcherPriority.Normal, cancellation).Task,
            () => active && generation == RendererGeneration && ReferenceEquals(sender, _currentCore)
                && _pluginResources.TryGetSurface(token, out _),
            frame.PostWebMessageAsJson));
        frame.WebMessageReceived += async (_, message) =>
        {
            if (!active || generation != RendererGeneration || PluginApi is not { } api) return;
            _readiness?.Trace("PluginSurfaceAPI: received isolated frame request; owner=Host/Go; worker=false");
            string reply = await api.HandleAsync(token, message.Source, message.WebMessageAsJson);
            if (active && generation == RendererGeneration && _pluginResources.TryGetSurface(token, out var currentSurface))
                frame.PostWebMessageAsJson(reply);
        };
    }

    // The callback is queued first; both frame identity and the supplied session/surface gate
    // are checked around the actual synchronous wire post, never only before enqueue.
    internal static Func<string, Func<Func<bool>, bool>, CancellationToken, Task<bool>> CreatePluginFrameNotifier(
        Func<Action, CancellationToken, Task> dispatch, Func<bool> current, Action<string> post)
        => async (json, gate, token) =>
        {
            bool posted = false;
            await dispatch(() =>
            {
                if (!token.IsCancellationRequested && current())
                    posted = gate(() => { if (!current()) return false; post(json); return true; });
            }, token).ConfigureAwait(false);
            return posted;
        };

    private void OnWebMessageReceived(
        object? sender,
        CoreWebView2WebMessageReceivedEventArgs args)
    {
        // #482 generation seal: only the CURRENT core's messages are routed;
        // an event from a retired (old/disposed) renderer is dropped before
        // any reply or notification can be produced.
        if (sender is not CoreWebView2 senderCore || !ReferenceEquals(senderCore, _currentCore))
        {
            return;
        }
        CoreWebView2? core = senderCore;
        if (core is null) return;
        if (!WebViewNavigationPolicy.IsAppNavigation(args.Source))
        {
            PostRouterReply(
                core,
                WebMessageRouter.BuildOperationFailed(
                    null,
                    "消息来源不受信任。",
                    "UNTRUSTED_MESSAGE_SOURCE"));
            return;
        }

        IReadOnlyList<string>? paths = null;
        string? inboundType = null;
        string? inboundRequestId = null;
        int additionalObjectCount = 0;
        bool carriesNativeObjects = false;
        if (args.WebMessageAsJson.Contains(
            "\"file.",
            StringComparison.Ordinal))
        {
            _readiness?.Trace(
                $"Raw attachment bridge message observed; jsonBytes=" +
                $"{System.Text.Encoding.UTF8.GetByteCount(args.WebMessageAsJson)}");
        }
        try
        {
            using JsonDocument document = JsonDocument.Parse(args.WebMessageAsJson);
            inboundType = document.RootElement.ValueKind == JsonValueKind.Object
                && document.RootElement.TryGetProperty("type", out JsonElement typeNode)
                && typeNode.ValueKind == JsonValueKind.String
                    ? typeNode.GetString()
                    : null;
            inboundRequestId = document.RootElement.ValueKind == JsonValueKind.Object
                && document.RootElement.TryGetProperty(
                    "requestId",
                    out JsonElement requestIdNode)
                && requestIdNode.ValueKind == JsonValueKind.String
                    ? requestIdNode.GetString()
                    : null;
            carriesNativeObjects = document.RootElement.ValueKind == JsonValueKind.Object
                && document.RootElement.TryGetProperty(
                    "nativeObjects",
                    out JsonElement nativeObjectsNode)
                && nativeObjectsNode.ValueKind == JsonValueKind.True;
            if (carriesNativeObjects)
            {
                NativeFileIngressInspection inspection = InspectNativeFileIngress(
                    inboundType,
                    () => args.AdditionalObjects.Cast<object>().ToArray(),
                    value => value is CoreWebView2File file ? file.Path : null);
                additionalObjectCount = inspection.ObjectCount;
                if (inspection.ErrorCode is not null)
                {
                    PostRouterReply(
                        core,
                        WebMessageRouter.BuildOperationFailed(
                            inboundRequestId,
                            inspection.ErrorMessage!,
                            inspection.ErrorCode));
                    return;
                }
                paths = inspection.Paths;
            }
        }
        catch (JsonException)
        {
            // The router produces the canonical BAD_JSON response.
        }
        if (inboundType?.StartsWith("file.", StringComparison.Ordinal) == true)
        {
            _readiness?.Trace(
                $"Attachment bridge message received; type={inboundType}; " +
                $"additionalObjects={additionalObjectCount}");
        }
        if (inboundType?.StartsWith("history.", StringComparison.Ordinal) == true)
        {
            _readiness?.Trace(
                $"History bridge message received; type={inboundType}; " +
                $"requestIdPresent={!string.IsNullOrWhiteSpace(inboundRequestId)}");
        }

        CurrentNativeFilePaths = paths;
        try
        {
            HostReplyMessage? reply;
            try
            {
                reply = _router.Route(args.WebMessageAsJson);
            }
            catch (Exception exception)
            {
                _readiness?.Trace(
                    $"Web message dispatch failed; type={inboundType ?? "unknown"}; " +
                    $"exception={exception.GetType().Name}");
                reply = WebMessageRouter.BuildOperationFailed(
                    inboundRequestId,
                    "The desktop host could not dispatch the request.",
                    "HOST_DISPATCH_FAILED");
            }
            if (reply is not null)
            {
                PostRouterReply(core, reply);
            }
        }
        finally
        {
            CurrentNativeFilePaths = null;
        }
    }

    internal static NativeFileIngressInspection InspectNativeFileIngress(
        string? messageType,
        Func<IReadOnlyList<object>?> readAdditionalObjects,
        Func<object, string?> readFilePath)
    {
        if (messageType is not ("document.externalDropRequested"
            or "file.uploadRequested"
            or "file.replaceRequested"))
        {
            return NativeFileIngressInspection.Failed(
                "NATIVE_OBJECTS_NOT_ALLOWED",
                "Native file objects are not allowed for this request type.");
        }

        IReadOnlyList<object>? additionalObjects;
        try
        {
            additionalObjects = readAdditionalObjects();
        }
        catch (Exception)
        {
            return NativeFileIngressInspection.Failed(
                "NATIVE_OBJECTS_UNAVAILABLE",
                "Native file objects could not be read by the desktop host.");
        }
        if (additionalObjects is null)
        {
            return NativeFileIngressInspection.Failed(
                "NATIVE_OBJECTS_UNAVAILABLE",
                "Native file objects could not be read by the desktop host.");
        }

        int count = additionalObjects.Count;
        int maximum = messageType == "file.uploadRequested" ? 32 : 100;
        if (count == 0)
        {
            return NativeFileIngressInspection.Failed(
                "NATIVE_OBJECTS_MISSING",
                "The request declared native file objects but supplied none.",
                count);
        }
        if (count > maximum)
        {
            return NativeFileIngressInspection.Failed(
                "NATIVE_OBJECT_LIMIT_EXCEEDED",
                $"The request supplied more than {maximum} native file objects.",
                count);
        }

        var paths = new List<string>(count);
        foreach (object value in additionalObjects)
        {
            string? path;
            try
            {
                path = readFilePath(value);
            }
            catch (Exception)
            {
                path = null;
            }
            if (string.IsNullOrWhiteSpace(path))
            {
                return NativeFileIngressInspection.Failed(
                    "INVALID_NATIVE_OBJECT",
                    "The request contained an invalid native file object.",
                    count);
            }
            paths.Add(path);
        }
        return new NativeFileIngressInspection(paths, count, null, null);
    }

    private bool PostEnvelope(
        string type,
        string? requestId,
        object? payload,
        JsonElement wire = default,
        Func<Func<bool>, bool>? emitGate = null)
    {
        if (!_router.IsHostNotificationAllowed(type))
        {
            return false;
        }
        if (type.StartsWith("history.", StringComparison.Ordinal)
            || string.Equals(type, "operation.failed", StringComparison.Ordinal))
        {
            _readiness?.Trace(
                $"Bridge response posted; type={type}; " +
                $"requestIdPresent={!string.IsNullOrWhiteSpace(requestId)}");
        }
        var envelope = new Dictionary<string, object?>
        {
            ["type"] = type,
            ["requestId"] = requestId,
            ["payload"] = payload,
        };
        if (wire.ValueKind != JsonValueKind.Undefined)
            envelope["wire"] = wire;
        string json = JsonSerializer.Serialize(
            envelope,
            new JsonSerializerOptions(JsonSerializerDefaults.Web));
        EnqueueUiEmission(json, emitGate);
        return true;
    }

    private static void PostRouterReply(
        CoreWebView2 core,
        HostReplyMessage reply)
    {
        core.PostWebMessageAsString(SerializeRouterReply(reply));
    }

    internal static string SerializeRouterReply(HostReplyMessage reply)
    {
        ArgumentNullException.ThrowIfNull(reply);
        var envelope = new Dictionary<string, object?>
        {
            ["type"] = reply.Type,
            ["requestId"] = reply.RequestId,
            ["payload"] = reply.Payload is null
                ? null
                : reply.Payload,
        };
        if (reply.Wire.ValueKind != JsonValueKind.Undefined)
            envelope["wire"] = reply.Wire;
        return JsonSerializer.Serialize(
            envelope,
            new JsonSerializerOptions(JsonSerializerDefaults.Web));
    }

    private const string OfflineBrowserArguments =
        "--disable-background-networking " +
        // The renderer has no direct-network product capability: authorized
        // plugin networking is mediated by the host. These Chromium switches
        // are a best-effort reduction of WebView2 Runtime background traffic;
        // runtime-owned Microsoft service connections may still occur and are
        // recorded separately from product-process egress in acceptance tests.
        // User-selected online capabilities use typed host RPC, while renderer
        // product traffic itself stays loopback-only.
        "--proxy-server=http://127.0.0.1:9 " +
        "--host-resolver-rules=\"MAP * 127.0.0.1, " +
        "EXCLUDE localhost, EXCLUDE app.vibetable.local, " +
        "EXCLUDE *.plugins.vibetable.local\" " +
        "--disable-quic " +
        "--disable-field-trial-config " +
        "--disable-metrics " +
        "--disable-metrics-reporting " +
        "--disable-signin-scoped-device-id " +
        "--disable-client-side-phishing-detection " +
        "--disable-component-update " +
        "--disable-default-apps " +
        "--disable-domain-reliability " +
        "--disable-sync " +
        "--metrics-recording-only " +
        "--no-first-run " +
        "--safebrowsing-disable-auto-update " +
        "--disable-features=AutofillServerCommunication," +
        "CertificateTransparencyComponentUpdater," +
        "MediaRouter,OptimizationHints,Translate," +
        "msEdgeAccountConsistency,msEdgeIdentity,msEdgeSignin,msEdgeSync," +
        "msEdge3PTelemetry,msEdgeAutoOpenTeamsHubAppV2,msEdgeCohorts," +
        "msEdgeFirstSyncOnFirstRun,msEdgeFreeOfficeShowRecentDocuments," +
        "msEdgeFreeOfficeUI,msEdgeHubAppsAutoOpenOutlookV2," +
        "msEdgeM365PopupDecider,msEdgeOnlineAccounts," +
        "msEdgeOSAccountInfoManagerCache,msEdgeOSAccountInfoSubstrate," +
        "msEdgePrioritizeM365LinksForProfileSelection," +
        "msEdgeProfileIntegratedAccountsInfo,msEdgeSignInAccountPicker," +
        "M365WamSsoNavigator,UseM365CopilotAadIdentity," +
        "UseM365CopilotMsaIdentity,msBrowserLaunchProtocolOutlookTrigger," +
        "msDynamicCSPPolicyCheckOnM365,msM365BrowsingSignals," +
        "msM365BrowsingSignalsCanInstallExtension,msM365LinkInsights," +
        "msM365Links3rdPartyNoPane,msM365LinksEnableRePrompt," +
        "msM365LinksImplicitSignin,msM365LinksUXForTeams," +
        "msM365LinksWebpageSummary,msM365LinksWindowsAccountTelemetry," +
        "msPrimaryOSAccountInfoCache," +
        "msEdgeNetworkPrediction,msSmartScreenProtection,NetworkPrediction";

    private Task<CoreWebView2Environment> GetEnvironmentAsync()
    {
        if (_environment is not null) return _environment;
        CoreWebView2EnvironmentOptions options = BuildEnvironmentOptions(
            Environment.GetEnvironmentVariable(
                "VIBETABLE_WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"),
            testMode: _readiness is not null);
        // WebView2 processes environment-level switches after programmatic
        // options. Set the same closed policy on both channels so runtime
        // defaults or field trials cannot re-enable an online service later.
        Environment.SetEnvironmentVariable(
            "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
            options.AdditionalBrowserArguments);
        return _environment = CoreWebView2Environment.CreateAsync(
            browserExecutableFolder: null,
            userDataFolder: BuildUserDataFolder(),
            options: options);
    }

    internal static CoreWebView2EnvironmentOptions BuildEnvironmentOptions(
        string? additionalBrowserArguments = null,
        bool testMode = false)
    {
        string arguments = OfflineBrowserArguments;
        if (!string.IsNullOrWhiteSpace(additionalBrowserArguments))
        {
            if (!testMode)
            {
                throw new InvalidOperationException(
                    "Additional WebView2 arguments require explicit test mode.");
            }
            string[] switches = additionalBrowserArguments.Split(
                (char[]?)null,
                StringSplitOptions.RemoveEmptyEntries);
            foreach (string browserSwitch in switches)
            {
                if (browserSwitch != "--disable-gpu"
                    && !IsValidRemoteDebuggingPort(browserSwitch))
                {
                    throw new InvalidOperationException(
                        $"WebView2 test argument is not allowed: {browserSwitch}");
                }
            }
            arguments = $"{OfflineBrowserArguments} {string.Join(" ", switches)}";
        }
        return new CoreWebView2EnvironmentOptions
        {
            AdditionalBrowserArguments = arguments,
            AllowSingleSignOnUsingOSPrimaryAccount = false,
            AreBrowserExtensionsEnabled = false,
            EnableTrackingPrevention = false,
            IsCustomCrashReportingEnabled = true,
        };
    }

    private static bool IsValidRemoteDebuggingPort(string value)
    {
        const string prefix = "--remote-debugging-port=";
        return value.StartsWith(prefix, StringComparison.Ordinal)
            && int.TryParse(
                value[prefix.Length..],
                System.Globalization.NumberStyles.None,
                System.Globalization.CultureInfo.InvariantCulture,
                out int port)
            && port is >= 1 and <= 65535
            && string.Equals(
                value[prefix.Length..],
                port.ToString(System.Globalization.CultureInfo.InvariantCulture),
                StringComparison.Ordinal);
    }

    private string BuildUserDataFolder()
    {
        string? isolatedRoot = _isolatedUserDataRoot
            ?? Environment.GetEnvironmentVariable(
                "VIBETABLE_E2E_WEBVIEW2_USER_DATA_ROOT");
        string folder = ResolveUserDataFolder(
            isolatedRoot,
            Environment.ProcessId,
            _stableIsolatedUserDataRoot);
        try
        {
            Directory.CreateDirectory(folder);
            return folder;
        }
        catch
        {
            // The WebView2 profile is disposable cache; when the configured
            // root cannot be created (read-only location), degrade to TEMP
            // instead of failing the shell.
            string fallbackRoot = Path.Combine(
                Path.GetTempPath(),
                "VibeTable",
                "webview2-udd");
            string? fallbackIsolatedRoot =
                !string.IsNullOrWhiteSpace(isolatedRoot)
                && Path.IsPathFullyQualified(isolatedRoot)
                    ? Path.Combine(
                        fallbackRoot,
                        _stableIsolatedUserDataRoot ? "stable" : "e2e")
                    : null;
            folder = ResolveUserDataFolder(
                fallbackIsolatedRoot ?? fallbackRoot,
                Environment.ProcessId,
                _stableIsolatedUserDataRoot);
            Directory.CreateDirectory(folder);
            return folder;
        }
    }

    /// <summary>
    /// The composition root always supplies the data-root-scoped profile
    /// folder; the E2E environment override and per-process isolation remain
    /// for redirected scenarios. A missing root (defensive only) lands in
    /// TEMP so a misconfigured host still renders.
    /// </summary>
    internal static string ResolveUserDataFolder(
        string? isolatedRoot,
        int processId,
        bool stableIsolatedRoot = false)
    {
        if (!string.IsNullOrWhiteSpace(isolatedRoot)
            && Path.IsPathFullyQualified(isolatedRoot))
        {
            if (stableIsolatedRoot)
            {
                return Path.GetFullPath(isolatedRoot);
            }
            return Path.Combine(
                Path.GetFullPath(isolatedRoot),
                $"p{processId}");
        }
        return Path.Combine(
            Path.GetTempPath(),
            "VibeTable",
            "webview2-udd");
    }

    private static void OpenExternal(string? value)
    {
        if (string.IsNullOrWhiteSpace(value)) return;
        try
        {
            Process.Start(new ProcessStartInfo(value) { UseShellExecute = true });
        }
        catch
        {
            // External navigation is best-effort and cannot weaken the gate.
        }
    }
}

internal sealed record NativeFileIngressInspection(
    IReadOnlyList<string>? Paths,
    int ObjectCount,
    string? ErrorCode,
    string? ErrorMessage)
{
    public static NativeFileIngressInspection Failed(
        string code,
        string message,
        int objectCount = 0)
        => new(null, objectCount, code, message);
}
