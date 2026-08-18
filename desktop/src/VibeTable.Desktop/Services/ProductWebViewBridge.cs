using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using System.Windows;
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
    private readonly WebView2 _webView;
    private readonly WebMessageRouter _router;
    private readonly PluginWebViewResourceHost _pluginResources;
    private readonly TestModeReadinessWriter? _readiness;
    private readonly Action<string> _processFailed;
    private readonly string? _isolatedUserDataRoot;
    private readonly bool _stableIsolatedUserDataRoot;
    private readonly object _loadGate = new();
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
        => PostEnvelope(type, requestId: null, payload);

    public void PostResponse(string type, string? requestId, object? payload)
        => PostEnvelope(type, requestId, payload);

    public void PostWorkspaceV2Response(
        string? requestId,
        object? payload,
        JsonElement wire)
        => PostEnvelope("workspace.v2.response", requestId, payload, wire);

    public void PostWorkspaceV2Event(object? payload, JsonElement wire)
        => PostEnvelope("workspace.v2.event", requestId: null, payload, wire);

    public void PostOperationFailed(
        string? requestId,
        string message,
        string? code = null,
        string? operation = null)
    {
        _owner.Dispatcher.BeginInvoke(() =>
        {
            CoreWebView2? core = _webView.CoreWebView2;
            if (core is null) return;
            PostRouterReply(
                core,
                WebMessageRouter.BuildOperationFailed(
                    requestId,
                    message,
                    code,
                    operation));
        });
    }

    private async Task LoadCoreAsync()
    {
        _readiness?.Trace("ProductWebView: initializing runtime");
        await _webView.EnsureCoreWebView2Async(await GetEnvironmentAsync())
            .ConfigureAwait(true);
        CoreWebView2 core = _webView.CoreWebView2
            ?? throw new InvalidOperationException(
                "WebView2 did not provide a CoreWebView2 instance.");
        ApplyHardening(core);
        core.WebMessageReceived -= OnWebMessageReceived;
        core.WebMessageReceived += OnWebMessageReceived;

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

        core.NavigationStarting += (_, args) =>
        {
            if (!WebViewNavigationPolicy.IsAppNavigation(args.Uri))
            {
                args.Cancel = true;
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
        core.ProcessFailed += (_, args) =>
        {
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
            _owner.Dispatcher.BeginInvoke(() => _processFailed(reason));
        };
    }

    private void OnWebMessageReceived(
        object? sender,
        CoreWebView2WebMessageReceivedEventArgs args)
    {
        CoreWebView2? core = sender as CoreWebView2 ?? _webView.CoreWebView2;
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

    private void PostEnvelope(
        string type,
        string? requestId,
        object? payload,
        JsonElement wire = default)
    {
        if (!_router.IsHostNotificationAllowed(type))
        {
            return;
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
        _owner.Dispatcher.BeginInvoke(() =>
        {
            _webView.CoreWebView2?.PostWebMessageAsString(json);
        });
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
        string localAppData = Environment.GetFolderPath(
            Environment.SpecialFolder.LocalApplicationData);
        string folder = ResolveUserDataFolder(
            localAppData,
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
            string fallbackRoot = Path.Combine(
                Path.GetTempPath(),
                "VibeTable",
                "webview2-udd");
            string? fallbackIsolatedRoot =
                !string.IsNullOrWhiteSpace(isolatedRoot)
                && Path.IsPathFullyQualified(isolatedRoot)
                    ? Path.Combine(
                        fallbackRoot,
                        _stableIsolatedUserDataRoot ? "dev" : "e2e")
                    : null;
            folder = ResolveUserDataFolder(
                fallbackRoot,
                fallbackIsolatedRoot,
                Environment.ProcessId,
                _stableIsolatedUserDataRoot);
            Directory.CreateDirectory(folder);
            return folder;
        }
    }

    internal static string ResolveUserDataFolder(
        string localAppData,
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
            Path.GetFullPath(localAppData),
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
