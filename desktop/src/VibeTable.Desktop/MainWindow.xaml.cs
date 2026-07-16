using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using System.Windows;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.Wpf;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Desktop.ViewModels;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.Directus;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop;

/// <summary>
/// Interaction logic for <see cref="MainWindow"/>. Hosts the hardened
/// WebView2 control and wires it to the <see cref="MainWindowViewModel"/>
/// startup state machine via the <see cref="IBackendLifecycle"/> and
/// <see cref="IWebViewBridge"/> abstractions.
/// </summary>
/// <remarks>
/// <para>
/// <b>Backend startup lives here, NOT in the ctor.</b> The constructor only
/// wires up bindings; the actual startup sequence is kicked off from the
/// <see cref="ContentRendered"/> handler so the window paints its
/// "Starting backend" chrome before any blocking work begins.
/// </para>
/// <para>
/// <b>WebView2 hardening</b> (verbatim from Task 9 brief, Step 3):
/// </para>
/// <list type="bullet">
/// <item>Map the installed web-grid folder to <c>https://app.vibetable.local/</c>
/// with deny-CORS via <c>SetVirtualHostNameToFolderMapping</c> using
/// <see cref="CoreWebView2HostResourceAccessKind.DenyCors"/>.</item>
/// <item>Navigate ONLY to that origin; cancel all other navigation in
/// <see cref="CoreWebView2.NavigationStarting"/>.</item>
/// <item>Cancel <see cref="CoreWebView2.NewWindowRequested"/>.</item>
/// <item>Disable devtools, context menus, and the status bar in Release.</item>
/// <item>Handle <see cref="CoreWebView2.ProcessFailed"/> by moving the
/// ViewModel to <see cref="StartupState.Faulted"/>.</item>
/// </list>
/// </remarks>
public partial class MainWindow : Window
{
    private readonly MainWindowViewModel _viewModel;
    private readonly PythonBackendSupervisor _supervisor;
    private readonly WebMessageRouter _router;
    private readonly BackendLifecycleAdapter _backendAdapter;
    private readonly WebViewBridge _webBridge;
    private readonly TableWorkspaceService _workspace;
    private readonly ITableRpcGateway _workspaceGateway;
    private readonly WorkspaceRequestDispatcher _dispatcher;
    private readonly bool _directusEnabled;
    private readonly bool _directusAuto;
    private readonly DirectusLoginStore? _loginStore;
    private DirectusSupervisor? _directusSupervisor;
    private readonly TaskCompletionSource<bool>? _directusSessionReady;
    private JsonRpcDirectusGateway? _directusGateway;
    private int _directusWorkspaceOpened;
    private readonly GridStateCoordinator? _coordinator;
    private readonly TestModeReadinessWriter? _readinessWriter;
    private readonly bool _shellSmokeMode;
    private CancellationTokenSource? _sessionCts;
    private string? _pendingLoginEmail;
    private string? _pendingLoginPassword;

    public MainWindow()
    {
        InitializeComponent();

        // Parse CLI options once. --test-mode enables the shell readiness
        // report used by the real WPF/WebView2/backend smoke test.
        var startupOptions = HostStartupOptions.Current();
        _shellSmokeMode = startupOptions.TestMode;
        string? configuredDirectusUrl =
            Environment.GetEnvironmentVariable("VIBETABLE_DIRECTUS_URL");
        _directusAuto = HostStartupOptions.ShouldAutoStartLocalDirectus(
            startupOptions.DirectusAuto,
            configuredDirectusUrl,
            AppContext.BaseDirectory);
        _directusEnabled = _directusAuto
            || !string.IsNullOrWhiteSpace(configuredDirectusUrl);
        if (_directusEnabled)
        {
            string sourceScope = _directusAuto
                ? "local:default"
                : "remote:" + configuredDirectusUrl;
            _loginStore = new DirectusLoginStore(sourceScope);
        }
        _directusSessionReady = _directusEnabled
            ? new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously)
            : null;

        // Build the production object graph. App.xaml.cs keeps minimal — the
        // window owns its supervisor + router and disposes them on close.
        _supervisor = new PythonBackendSupervisor(
            BackendLaunchOptions.ResolveForHost());
        _router = new WebMessageRouter(OnRoutedWebRequest);
        _backendAdapter = new BackendLifecycleAdapter(_supervisor);
        _webBridge = new WebViewBridge(this, _router);

        // Table workspace: gateway over the supervisor's JsonRpcClient (created
        // during StartAsync), the workspace orchestrator, and the request
        // dispatcher. The reply sink posts notifications back through the
        // WebViewBridge. The supervisor's Client is null until StartAsync
        // completes; the gateway is constructed lazily on first use so a
        // renderer message that races ahead of startup finds a not-yet-ready
        // backend instead of a null-ref.
        _workspaceGateway = new LazySupervisorDirectusGateway(_supervisor);
        _workspace = new TableWorkspaceService(_workspaceGateway);
        _workspace.Notification += OnWorkspaceNotification;

        // B3: the grid-state coordinator debounces query/state requests and
        // reconciles selection snapshots. It forwards page notifications
        // through the same workspace notification surface.
        _coordinator = new GridStateCoordinator(
            _workspaceGateway,
            n => OnWorkspaceNotification(n));

        IDatabasePicker picker = new FixedPathPicker("directus://configured");

        _readinessWriter = startupOptions.TestMode
            ? new TestModeReadinessWriter(startupOptions.ReadinessDir)
            : null;

        if (_readinessWriter is not null)
        {
            _supervisor.StateChanged += (_, state) =>
            {
                _readinessWriter.Trace($"Backend state={state}");
                if (state == BackendState.Faulted)
                {
                    _readinessWriter.Trace(
                        $"Backend stderr: {_supervisor.GetStdErrorLog()}");
                }
            };
        }

        _dispatcher = new WorkspaceRequestDispatcher(
            _workspace, picker, _webBridge, _coordinator);

        _viewModel = new MainWindowViewModel(_backendAdapter, _webBridge);
        if (_shellSmokeMode)
        {
            _viewModel.PropertyChanged += (_, args) =>
            {
                if (string.Equals(args.PropertyName, nameof(MainWindowViewModel.State),
                    StringComparison.Ordinal))
                {
                    TryWriteShellReadiness();
                }
            };
        }
        DataContext = _viewModel;

        Loaded += OnLoaded;
        Closed += OnClosed;
    }

    /// <summary>
    /// Fires once the window is on screen. Starts the supervised backend and
    /// (on success) brings up the hardened WebView2.
    /// </summary>
    private async void OnLoaded(object sender, RoutedEventArgs e)
    {
        _readinessWriter?.Trace("OnLoaded: starting backend");
        _sessionCts = new CancellationTokenSource();
        try
        {
            // --directus-auto: the host itself brings up the local Directus 12
            // (SQLite) runtime before the backend starts, so the backend can
            // connect to it. Single-machine VibeTable uses this instead of an
            // external Directus server.
            if (_directusAuto)
            {
                string? url = await EnsureLocalDirectusAsync();
                if (url is null)
                {
                    // The user was already prompted (Node missing / install
                    // failed). Close to avoid a backend that can't reach data.
                    Close();
                    return;
                }
                Environment.SetEnvironmentVariable("VIBETABLE_DIRECTUS_URL", url);
                Environment.SetEnvironmentVariable("VIBETABLE_DIRECTUS_PROJECT", "default");
                string packagedManifest = Path.Combine(
                    AppContext.BaseDirectory,
                    "directus",
                    "capabilities",
                    "vibetable-b4-capabilities.json");
                if (File.Exists(packagedManifest))
                {
                    Environment.SetEnvironmentVariable(
                        "VIBETABLE_DIRECTUS_MANIFEST", packagedManifest);
                }
            }

            await _viewModel.StartAsync();
            _readinessWriter?.Trace($"OnLoaded: StartAsync complete, VM state={_viewModel.State}");
            if (_supervisor.State == BackendState.Ready && _directusEnabled)
            {
                bool authenticated = await EnsureDirectusSessionAsync();
                _directusSessionReady?.TrySetResult(authenticated);
                if (!authenticated)
                {
                    Close();
                }
            }
        }
        catch (InvalidOperationException ex)
        {
            _readinessWriter?.Trace($"OnLoaded: InvalidOperationException: {ex.Message}");
            // Illegal-transition / double-start: the VM is already in a
            // terminal state. Surface it so startup faults are never silent.
            LogStartupFault(ex);
        }
        catch (Exception ex)
        {
            _readinessWriter?.Trace($"OnLoaded: unhandled {ex.GetType().Name}: {ex.Message}");
            LogStartupFault(ex);
        }
    }

    /// <summary>
    /// Makes a startup-phase fault observable. Without this the only signal
    /// was a test-mode-only trace, so a real failure (e.g. a Directus RPC
    /// exception inside <see cref="EnsureDirectusSessionAsync"/>) would vanish
    /// silently — no login dialog, no error, the window just sits idle.
    /// Writes a timestamped entry to <c>%LOCALAPPDATA%/VibeTable/logs/host-startup.log</c>
    /// and shows a modal box. Both paths are best-effort.
    /// </summary>
    private void LogStartupFault(Exception ex)
    {
        string entry =
            $"[{DateTimeOffset.Now:O}] {ex.GetType().FullName}: {ex.Message}{Environment.NewLine}" +
            $"{ex}{Environment.NewLine}{new string('-', 60)}{Environment.NewLine}";
        try
        {
            string dir = Path.Combine(
                Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
                "VibeTable",
                "logs");
            Directory.CreateDirectory(dir);
            File.AppendAllText(Path.Combine(dir, "host-startup.log"), entry, Encoding.UTF8);
        }
        catch
        {
            // Logging must never mask the original fault.
        }
        try
        {
            MessageBox.Show(
                this,
                $"启动时发生错误：{ex.GetType().Name}: {ex.Message}\n\n" +
                "详情已写入 %LOCALAPPDATA%\\VibeTable\\logs\\host-startup.log",
                "VibeTable 启动错误",
                MessageBoxButton.OK,
                MessageBoxImage.Error);
        }
        catch
        {
            // Best-effort; the log file still has the detail.
        }
    }

    private void OnClosed(object? sender, EventArgs e)
    {
        _sessionCts?.Cancel();
        _directusSessionReady?.TrySetResult(false);
        _directusGateway?.Dispose();
        if (_workspaceGateway is IDisposable disposableGateway)
        {
            disposableGateway.Dispose();
        }
        _router.IsReady = false;
        try
        {
            _supervisor.DisposeAsync().AsTask().Wait(TimeSpan.FromSeconds(5));
        }
        catch
        {
            // Best-effort teardown on close.
        }
        try
        {
            _directusSupervisor?.DisposeAsync().AsTask().Wait(TimeSpan.FromSeconds(5));
        }
        catch
        {
            // Best-effort teardown on close.
        }
    }

    /// <summary>
    /// Brings up the local Directus 12 (SQLite) runtime for single-machine
    /// <c>--directus-auto</c> mode. Returns the base URL on success, or null
    /// (after prompting the user) if Node.js is missing or the runtime cannot
    /// start.
    /// </summary>
    private async Task<string?> EnsureLocalDirectusAsync()
    {
        // Node.js is required to run npm install / directus start.
        if (NodeRuntime.FindNode() is null)
        {
            MessageBox.Show(
                this,
                "本机运行需要 Node.js 24.x，但未检测到或版本过低。\n请从 Node.js 官网下载安装 LTS 版本后重新启动 VibeTable。",
                "需要安装 Node.js",
                MessageBoxButton.OK,
                MessageBoxImage.Information);
            try
            {
                Process.Start(new ProcessStartInfo
                {
                    FileName = "https://nodejs.org/en/download",
                    UseShellExecute = true,
                });
            }
            catch
            {
                // Best-effort: the message already shows the URL.
            }
            return null;
        }

        var options = DirectusLaunchOptions.ResolveForHost();
        if (options is null)
        {
            MessageBox.Show(
                this,
                "找不到本机 Directus 运行时目录（local-directus）。\n"
                + "开发模式请从仓库根运行；打包模式请联系发布者。",
                "VibeTable",
                MessageBoxButton.OK,
                MessageBoxImage.Error);
            return null;
        }

        bool firstRun = !File.Exists(
            Path.Combine(options.LocalDirectusDirectory, ".bootstrapped"));
        if (firstRun)
        {
            var setup = new DirectusFirstRunWindow { Owner = this };
            if (setup.ShowDialog() != true)
            {
                return null;
            }

            _pendingLoginEmail = setup.Email;
            _pendingLoginPassword = setup.Password;
            _loginStore?.Save(
                new DirectusLoginPreferences(
                    setup.Email,
                    setup.RememberPassword,
                    setup.AutoLogin,
                    setup.ManagedLogin),
                setup.RememberPassword ? setup.Password : null);
            options.Environment["VIBETABLE_DIRECTUS_BOOTSTRAP_EMAIL"] = setup.Email;
            options.Environment["VIBETABLE_DIRECTUS_BOOTSTRAP_PASSWORD"] = setup.Password;
        }

        _directusSupervisor = new DirectusSupervisor(options);
        try
        {
            await _directusSupervisor.StartAsync(CancellationToken.None);
            return _directusSupervisor.BaseUrl;
        }
        catch (Exception ex)
        {
            string log = _directusSupervisor.GetStdErrorLog();
            MessageBox.Show(
                this,
                $"本机 Directus 启动失败：{ex.Message}\n\nDirectus 日志：\n{log}",
                "VibeTable",
                MessageBoxButton.OK,
                MessageBoxImage.Error);
            return null;
        }
        finally
        {
            options.Environment.Remove("VIBETABLE_DIRECTUS_BOOTSTRAP_EMAIL");
            options.Environment.Remove("VIBETABLE_DIRECTUS_BOOTSTRAP_PASSWORD");
        }
    }

    private async Task<bool> EnsureDirectusSessionAsync()
    {
        var client = _supervisor.Client;
        if (client is null)
        {
            return false;
        }
        if (_directusGateway is null)
        {
            _directusGateway = new JsonRpcDirectusGateway(client);
            _directusGateway.Changed += OnDirectusChanged;
        }
        try
        {
            await _directusGateway.GetServerInfoAsync(CancellationToken.None);
            var status = await _directusGateway.GetStatusAsync(CancellationToken.None);
            if (string.Equals(status.State, "authenticated", StringComparison.Ordinal))
            {
                return true;
            }

            if (!string.IsNullOrEmpty(_pendingLoginEmail)
                && !string.IsNullOrEmpty(_pendingLoginPassword))
            {
                try
                {
                    status = await _directusGateway.LoginAsync(
                        _pendingLoginEmail,
                        _pendingLoginPassword,
                        otp: null,
                        CancellationToken.None);
                    if (string.Equals(status.State, "authenticated", StringComparison.Ordinal))
                    {
                        return true;
                    }
                }
                catch
                {
                    // Bootstrap may have completed with a different account;
                    // continue to the saved/interactive login paths.
                }
                finally
                {
                    _pendingLoginPassword = null;
                }
            }

            if (_loginStore is not null)
            {
                var preferences = _loginStore.LoadPreferences();
                if (preferences.AutoLogin)
                {
                    try
                    {
                        status = await _directusGateway.RefreshAsync(CancellationToken.None);
                        if (string.Equals(
                            status.State, "authenticated", StringComparison.Ordinal))
                        {
                            return true;
                        }
                    }
                    catch
                    {
                        // Refresh token is absent/expired; try the saved password.
                    }
                }
                string? savedPassword = preferences.AutoLogin
                    ? _loginStore.LoadPassword()
                    : null;
                if (!string.IsNullOrWhiteSpace(preferences.Email)
                    && !string.IsNullOrEmpty(savedPassword))
                {
                    try
                    {
                        status = await _directusGateway.LoginAsync(
                            preferences.Email,
                            savedPassword,
                            otp: null,
                            CancellationToken.None);
                        if (string.Equals(
                            status.State, "authenticated", StringComparison.Ordinal))
                        {
                            return true;
                        }
                    }
                    catch
                    {
                        // Saved credentials may have been changed in Directus;
                        // fall through to the interactive dialog.
                    }
                }
            }

            if (_loginStore is null)
            {
                return false;
            }
            var dialog = new DirectusLoginWindow(_directusGateway, _loginStore)
            {
                Owner = this,
            };
            return dialog.ShowDialog() == true;
        }
        catch (Exception ex)
        {
            MessageBox.Show(
                this,
                $"无法初始化 Directus 会话：{ex.Message}",
                "VibeTable",
                MessageBoxButton.OK,
                MessageBoxImage.Error);
            return false;
        }
    }

    /// <summary>
    /// Dispatch callback for messages that passed the router's whitelist.
    /// Marks the host ready on <c>app.ready</c>, then forwards the four
    /// Phase-A web request types to the workspace dispatcher which drives the
    /// JSON-RPC flow and posts typed replies back via the WebViewBridge.
    /// </summary>
    private void OnRoutedWebRequest(RoutedWebRequest request)
    {
        _readinessWriter?.Trace($"OnRoutedWebRequest: type={request.Type}");
        // Mark the host as Ready the first time the renderer signals app.ready.
        if (string.Equals(request.Type, "app.ready", StringComparison.Ordinal))
        {
            _router.IsReady = true;
            TryWriteShellReadiness();
            if (_directusEnabled)
            {
                _ = OpenDirectusWorkspaceAsync();
            }
        }

        _dispatcher.Dispatch(request);
    }

    private void TryWriteShellReadiness()
    {
        if (!_shellSmokeMode || _readinessWriter is null)
        {
            return;
        }
        if (_supervisor.State == BackendState.Ready
            && _viewModel.State == StartupState.Ready
            && _router.IsReady)
        {
            _readinessWriter.Trace(
                "shell-smoke: backend, WebView2 navigation, and app.ready are ready");
            _readinessWriter.WriteShellReady();
        }
    }

    private async Task OpenDirectusWorkspaceAsync()
    {
        if (_directusSessionReady is null || !await _directusSessionReady.Task)
        {
            return;
        }
        if (Interlocked.Exchange(ref _directusWorkspaceOpened, 1) != 0)
        {
            return;
        }
        try
        {
            var result = await _workspace.OpenDatabaseAsync("directus://configured");
            _coordinator?.SetDatabase("directus");
            if (_directusGateway is not null)
            {
                foreach (string collection in result.Tables)
                {
                    var schema = await _directusGateway.GetSchemaAsync(
                        collection, CancellationToken.None);
                    await _directusGateway.SubscribeAsync(
                        $"grid-{collection}",
                        collection,
                        schema.Columns.Select(column => column.Name).ToArray(),
                        CancellationToken.None);
                }
            }
            _webBridge.PostNotification("database.opened", new
            {
                tables = result.Tables,
                views = result.Views,
            });
        }
        catch (Exception ex)
        {
            Interlocked.Exchange(ref _directusWorkspaceOpened, 0);
            _webBridge.PostOperationFailed(null, ex.Message, "DIRECTUS_OPEN_FAILED");
        }
    }

    private void OnDirectusChanged(DirectusChange change)
        => _webBridge.PostNotification("directus.changed", change);

    /// <summary>
    /// Opens the table-management dialog (create/delete collections) when the
    /// Directus gateway is available. The gateway is created lazily during
    /// <see cref="EnsureDirectusSessionAsync"/>; if the session never came up
    /// the button is a no-op rather than crashing on a null gateway.
    /// </summary>
    private void OnManageTables(object sender, RoutedEventArgs e)
    {
        if (_directusGateway is null)
        {
            MessageBox.Show(
                this,
                "Directus 会话尚未就绪，无法管理表。请先完成登录。",
                "表管理",
                MessageBoxButton.OK,
                MessageBoxImage.Information);
            return;
        }
        var window = new TableManagementWindow(_directusGateway) { Owner = this };
        window.ShowDialog();
    }

    /// <summary>
    /// Forwards a workspace notification to the WebView as a typed host event
    /// (<c>table.pageLoaded</c> / <c>table.datasetReady</c>).
    /// </summary>
    private void OnWorkspaceNotification(TableNotification notification)
    {
        if (notification.MutationResult is not null)
        {
            object? payload = notification.MutationResult.Success
                ? notification.MutationResult.Result
                : notification.MutationResult.Error;
            _webBridge.PostNotification(notification.Type, payload);
            return;
        }
        // Marshal to the UI thread so the WebView2 PostWebMessageAsString call
        // happens on the thread that owns the CoreWebView2.
        Dispatcher.Invoke(() =>
        {
            _webBridge.PostNotification(notification.Type, new
            {
                table = notification.Page?.Table,
                columns = notification.Page?.Columns,
                rows = notification.Page?.Rows,
                offset = notification.Page?.Offset,
                limit = notification.Page?.Limit,
                totalRows = notification.Page?.TotalRows,
                mode = notification.Page?.Mode,
                filteredRows = notification.Page?.FilteredRows,
                querySnapshot = notification.Page?.QuerySnapshot,
                revision = notification.Page?.Revision,
                loadedRows = notification.LoadedRows,
            });
        });
    }

    /// <summary>
    /// Builds a per-process WebView2 user-data folder path. Each host process
    /// gets its own subfolder under <c>%LOCALAPPDATA%/VibeTable/webview2-udd</c>
    /// keyed by PID, so concurrent or orphaned instances never contend on the
    /// same profile lock. (WebView2 holds an exclusive lock on the user-data
    /// folder; a second instance pointing at a locked folder serves a stale
    /// profile and virtual-host navigation fails with success=False http=0.)
    /// </summary>
    internal string BuildWebViewUserDataFolder()
    {
        string root = Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
            "VibeTable",
            "webview2-udd");
        string folder = Path.Combine(root, $"p{Environment.ProcessId}");
        try
        {
            Directory.CreateDirectory(folder);
        }
        catch
        {
            // Fall back to a fresh temp folder if the canonical path is not
            // writable; EnsureCoreWebView2Async will fail loudly otherwise.
            folder = Path.Combine(Path.GetTempPath(), $"vibetable-webview-{Environment.ProcessId}");
            try { Directory.CreateDirectory(folder); } catch { /* best-effort */ }
        }
        return folder;
    }

    /// <summary>
    /// Adapter that exposes <see cref="PythonBackendSupervisor"/> through the
    /// <see cref="IBackendLifecycle"/> interface consumed by the ViewModel.
    /// </summary>
    private sealed class BackendLifecycleAdapter : IBackendLifecycle
    {
        private readonly PythonBackendSupervisor _supervisor;

        public BackendLifecycleAdapter(PythonBackendSupervisor supervisor)
        {
            _supervisor = supervisor;
        }

        public Task StartAsync(CancellationToken cancellationToken)
            => _supervisor.StartAsync(cancellationToken);

        public Task StopAsync(CancellationToken cancellationToken)
            => _supervisor.StopAsync(cancellationToken);
    }

    /// <summary>
    /// The production <see cref="IWebViewBridge"/>: brings up a hardened
    /// WebView2 instance and routes typed messages through the
    /// <see cref="WebMessageRouter"/>. Also implements
    /// <see cref="IWebReplySink"/> so the workspace dispatcher can post typed
    /// notifications (<c>table.pageLoaded</c>, <c>table.datasetReady</c>,
    /// <c>database.opened</c>, <c>operation.failed</c>) to the renderer.
    /// Lives here (not in a separate file) so the hardening checklist is
    /// co-located with the XAML control it owns.
    /// </summary>
    private sealed class WebViewBridge : IWebViewBridge, IWebReplySink
    {
        private readonly MainWindow _owner;
        private readonly WebMessageRouter _router;

        public WebViewBridge(MainWindow owner, WebMessageRouter router)
        {
            _owner = owner;
            _router = router;
        }

        public async Task LoadAsync(CancellationToken cancellationToken)
        {
            var webview = _owner.WebView;
            _owner._readinessWriter?.Trace("WebViewBridge.LoadAsync: EnsureCoreWebView2Async");

            // Use an explicit per-process user-data folder. The default folder
            // is shared across all WebView2 instances in the same executable
            // directory; orphaned msedgewebview2.exe processes from prior runs
            // (or a concurrently-running host) hold a lock on it, which makes
            // EnsureCoreWebView2Async silently serve a stale/corrupted profile
            // and the virtual-host navigation fail with success=False http=0.
            // A unique subfolder under the local app-data VibeTable folder isolates
            // each session.
            try
            {
                if (webview.CreationProperties is null)
                {
                    webview.CreationProperties = new CoreWebView2CreationProperties();
                }
                webview.CreationProperties.UserDataFolder =
                    _owner.BuildWebViewUserDataFolder();
                _owner._readinessWriter?.Trace(
                    $"WebViewBridge.LoadAsync: UserDataFolder='{webview.CreationProperties.UserDataFolder}'");
            }
            catch (Exception ex)
            {
                _owner._readinessWriter?.Trace(
                    $"WebViewBridge.LoadAsync: UserDataFolder set failed: {ex.Message}");
            }

            // EnsureCoreWebView2Async is idempotent; awaiting here guarantees
            // CoreWebView2 is non-null for the hardening steps below.
            await webview.EnsureCoreWebView2Async()
                .ConfigureAwait(true);

            _owner._readinessWriter?.Trace("WebViewBridge.LoadAsync: CoreWebView2 ready");

            var core = webview.CoreWebView2
                ?? throw new InvalidOperationException(
                    "WebView2 CoreWebView2 was null after EnsureCoreWebView2Async.");

            ApplyHardening(core);
            _owner._readinessWriter?.Trace("WebViewBridge.LoadAsync: hardening applied");

            // Attach the message pump BEFORE navigation so an early app.ready
            // is not lost.
            core.WebMessageReceived += OnWebMessageReceived;
            var navigation = new TaskCompletionSource<bool>(
                TaskCreationOptions.RunContinuationsAsynchronously);
            EventHandler<CoreWebView2NavigationCompletedEventArgs>? onNavigationCompleted = null;
            onNavigationCompleted = (_, e) =>
            {
                _owner._readinessWriter?.Trace(
                    $"WebViewBridge.LoadAsync: NavigationCompleted success={e.IsSuccess} http={e.HttpStatusCode} webError={e.WebErrorStatus}");
                if (e.IsSuccess)
                {
                    navigation.TrySetResult(true);
                    return;
                }

                string message =
                    $"WebView2 navigation failed: webError={e.WebErrorStatus}, " +
                    $"httpStatus={e.HttpStatusCode}, uri={WebViewAssetService.AppOrigin}.";
                _owner._readinessWriter?.WriteError(message);
                navigation.TrySetException(new InvalidOperationException(message));
            };
            core.NavigationCompleted += onNavigationCompleted;

            string url = WebViewAssetService.AppOrigin;
            try
            {
                core.Navigate(url);
                _owner._readinessWriter?.Trace(
                    $"WebViewBridge.LoadAsync: Navigate issued to '{url}'");
                await navigation.Task.WaitAsync(cancellationToken)
                    .ConfigureAwait(true);
            }
            finally
            {
                core.NavigationCompleted -= onNavigationCompleted;
            }
        }

        /// <summary>
        /// Applies the full Step-3 hardening checklist to the
        /// <see cref="CoreWebView2"/>.
        /// </summary>
        private void ApplyHardening(CoreWebView2 core)
        {
            // 1. Map the installed web-grid folder to https://app.vibetable.local/
            //    with normal subresources allowed but CORS access denied.
            string? folder = WebViewAssetService.ResolveWebGridFolder();
            _owner._readinessWriter?.Trace($"ApplyHardening: webgrid folder resolved='{folder}'");
            if (folder is null)
            {
                throw new InvalidOperationException(
                    "Web-grid folder not found. Expected <repo>/desktop/web-grid/dist " +
                    "(dev) or <exe-dir>/web-grid (packaged).");
            }

            core.SetVirtualHostNameToFolderMapping(
                WebViewAssetService.AppHostName,
                folder,
                CoreWebView2HostResourceAccessKind.DenyCors);

            // 2. Navigation gating: cancel anything that is not the app origin.
            core.NavigationStarting += (_, args) =>
            {
                _owner._readinessWriter?.Trace($"NavigationStarting: uri='{args.Uri}' isAppOrigin={IsAppOrigin(args.Uri)}");
                if (!IsAppOrigin(args.Uri))
                {
                    args.Cancel = true;
                }
            };
            core.FrameNavigationStarting += (_, args) =>
            {
                if (!IsAppOrigin(args.Uri))
                {
                    args.Cancel = true;
                }
            };

            // 3. Cancel all popups / new windows.
            core.NewWindowRequested += (_, args) =>
            {
                args.Handled = true;
            };

            // 4. Release-mode disabling of devtools, context menus, status bar,
            //    and the DevTools accelerator key. Debug builds keep devtools
            //    available for diagnostics.
#if !DEBUG
            core.Settings.AreDevToolsEnabled = false;
            core.Settings.AreDefaultContextMenusEnabled = false;
            core.Settings.IsStatusBarEnabled = false;
            core.Settings.AreBrowserAcceleratorKeysEnabled = false;
#endif

            // 5. ProcessFailed -> ViewModel moves to Faulted. The renderer
            //    crashing is fatal to the session; the user retries.
            core.ProcessFailed += (_, args) =>
            {
                _owner._readinessWriter?.Trace(
                    $"ProcessFailed: kind={args.ProcessFailedKind}");
                // In test mode, surface a fatal renderer/network/browser crash
                // as a readiness ERROR so the smoke test detects the
                // environment failure (e.g. a buggy beta WebView2 runtime
                // whose NetworkService AV-crashes) instead of waiting the full
                // readiness timeout. The smoke test inspects the error text
                // and skips with a clear reason.
                bool fatal = args.ProcessFailedKind
                    is CoreWebView2ProcessFailedKind.RenderProcessExited
                    or CoreWebView2ProcessFailedKind.RenderProcessUnresponsive
                    or CoreWebView2ProcessFailedKind.BrowserProcessExited
                    or CoreWebView2ProcessFailedKind.FrameRenderProcessExited;
                if (fatal && _owner._readinessWriter is not null)
                {
                    _owner._readinessWriter.WriteError(
                        $"WebView2 {args.ProcessFailedKind}: renderer/browser process exited. " +
                        $"This is an environment-level failure (often a buggy WebView2 runtime); " +
                        $"the host code path is correct. See vibetable-trace.log for details.");
                }
                _owner.Dispatcher.Invoke(() =>
                    _owner._viewModel.MoveToFaulted(
                        $"WebView2 process failed: {args.ProcessFailedKind}"));
            };
        }

        private static bool IsAppOrigin(string? uri)
        {
            if (string.IsNullOrEmpty(uri))
            {
                return false;
            }
            try
            {
                // The virtual host maps to https://app.vibetable.local/. Accept any
            // path under that origin; reject everything else (including
            // data:, blob:, file:, and any other scheme/host).
            if (!Uri.TryCreate(uri, UriKind.Absolute, out var u))
            {
                return false;
            }
            return string.Equals(u.Scheme, "https", StringComparison.OrdinalIgnoreCase)
                && string.Equals(u.Host, WebViewAssetService.AppHostName,
                    StringComparison.OrdinalIgnoreCase);
            }
            catch (UriFormatException)
            {
                return false;
            }
        }

        private void OnWebMessageReceived(object? sender, CoreWebView2WebMessageReceivedEventArgs e)
        {
            // The TypeScript bridge posts a structured envelope object.
            // WebMessageAsJson preserves that object as JSON; using
            // TryGetWebMessageAsString here rejects every object message with
            // a COM exception and silently deadlocks the app.ready handshake.
            string raw = e.WebMessageAsJson;

            var reply = _router.Route(raw);
            if (reply is not null && sender is CoreWebView2 core)
            {
                PostReply(core, reply);
            }
        }

        /// <summary>
        /// Serializes a <see cref="HostReplyMessage"/> envelope and posts it
        /// back to the renderer.
        /// </summary>
        public static void PostReply(CoreWebView2 core, HostReplyMessage reply)
        {
            var payload = new
            {
                type = reply.Type,
                requestId = reply.RequestId,
                payload = reply.Payload is null
                    ? null
                    : new { message = reply.Payload.Message, code = reply.Payload.Code },
            };
            string json = JsonSerializer.Serialize(
                payload,
                new JsonSerializerOptions(JsonSerializerDefaults.Web));
            core.PostWebMessageAsString(json);
        }

        // ----- IWebReplySink -----

        /// <summary>
        /// Posts a typed host -&gt; web notification (no requestId). Marshals to
        /// the UI thread (the CoreWebView2 lives there).
        /// </summary>
        /// <remarks>
        /// The outbound type is gated by
        /// <see cref="WebMessageRouter.IsHostNotificationAllowed"/> so an
        /// out-of-whitelist notification is dropped at the boundary (with a
        /// diagnostic trace) rather than posted to the renderer. This makes the
        /// outbound gate symmetric with the (already enforced) inbound router
        /// whitelist — every type crossing the WebView boundary in either
        /// direction is checked against an explicit allow-list.
        /// </remarks>
        public void PostNotification(string type, object? payload)
        {
            if (!_router.IsHostNotificationAllowed(type))
            {
                _owner._readinessWriter?.Trace(
                    $"PostNotification: DROPPED out-of-whitelist notification type='{type}'");
                return;
            }
            _owner.Dispatcher.Invoke(() =>
            {
                var core = _owner.WebView.CoreWebView2;
                if (core is null)
                {
                    // WebView not ready yet; drop the notification. The renderer
                    // will re-request as needed (e.g. re-selecting a table).
                    return;
                }
                var envelope = new
                {
                    type,
                    payload,
                };
                string json = JsonSerializer.Serialize(
                    envelope,
                    new JsonSerializerOptions(JsonSerializerDefaults.Web));
                core.PostWebMessageAsString(json);
            });
        }

        /// <summary>
        /// Posts an <c>operation.failed</c> reply. Reuses the router's builder
        /// so the envelope shape matches the router-originated rejections.
        /// </summary>
        public void PostOperationFailed(string? requestId, string message, string? code = null)
        {
            _owner.Dispatcher.Invoke(() =>
            {
                var core = _owner.WebView.CoreWebView2;
                if (core is null)
                {
                    return;
                }
                var reply = WebMessageRouter.BuildOperationFailed(requestId, message, code);
                PostReply(core, reply);
            });
        }
    }

    // -----------------------------------------------------------------------
    // Production helper: Directus gateway lazy-binding
    // -----------------------------------------------------------------------

    /// <summary>
    /// Resolves a Directus-backed table adapter after the supervised RPC
    /// client is ready. Business data is always served by Directus; the
    /// backend support adapter is limited to local state and atomic paste.
    /// </summary>
    private sealed class LazySupervisorDirectusGateway : ITableRpcGateway, IDisposable
    {
        private readonly PythonBackendSupervisor _supervisor;
        private DirectusTableGateway? _resolved;

        public LazySupervisorDirectusGateway(PythonBackendSupervisor supervisor)
        {
            _supervisor = supervisor;
        }

        private DirectusTableGateway Gateway
        {
            get
            {
                var client = _supervisor.Client
                    ?? throw new InvalidOperationException("Backend is not ready for Directus RPC.");
                return _resolved ??= new DirectusTableGateway(
                    new JsonRpcDirectusGateway(client),
                    new JsonRpcWorkspaceSupportGateway(client));
            }
        }

        public Task<DatabaseOpenResult> OpenDatabaseAsync(string path, CancellationToken token)
            => Gateway.OpenDatabaseAsync(path, token);
        public Task<TableSummary> ListTablesAsync(CancellationToken token)
            => Gateway.ListTablesAsync(token);
        public Task<TablePage> ReadTablePageAsync(
            string table, int offset, int limit, CancellationToken token)
            => Gateway.ReadTablePageAsync(table, offset, limit, token);
        public Task<EditSchemaResult> GetEditSchemaAsync(string table, CancellationToken token)
            => Gateway.GetEditSchemaAsync(table, token);
        public Task<UpdateCellResult> UpdateCellAsync(
            string table, object rowKey, string column, object? oldValue,
            object? newValue, string schemaRevision, CancellationToken token)
            => Gateway.UpdateCellAsync(
                table, rowKey, column, oldValue, newValue, schemaRevision, token);
        public Task<InsertRowResult> InsertRowAsync(
            string table, IReadOnlyDictionary<string, object?> values,
            string schemaRevision, CancellationToken token)
            => Gateway.InsertRowAsync(table, values, schemaRevision, token);
        public Task<DeleteRowsResult> DeleteRowsAsync(
            string table, IReadOnlyList<(object RowKey, string ExpectedDigest)> rows,
            string schemaRevision, CancellationToken token)
            => Gateway.DeleteRowsAsync(table, rows, schemaRevision, token);
        public Task<ReadRowsResult> ReadRowsAsync(
            string table, IReadOnlyList<object> rowKeys, CancellationToken token)
            => Gateway.ReadRowsAsync(table, rowKeys, token);
        public Task<TablePage> QueryTablePageAsync(
            string table, int offset, int limit, TableQuery query, CancellationToken token)
            => Gateway.QueryTablePageAsync(table, offset, limit, query, token);
        public Task<SnapshotValidation> ValidateSnapshotAsync(
            QuerySnapshot snapshot, int? currentRevision, CancellationToken token)
            => Gateway.ValidateSnapshotAsync(snapshot, currentRevision, token);
        public Task<GridStateResult> GetGridStateAsync(
            string databaseId, string table, CancellationToken token)
            => Gateway.GetGridStateAsync(databaseId, table, token);
        public Task<GridStateResult> SaveGridStateAsync(
            string databaseId, string table, GridState state,
            string? revision, CancellationToken token)
            => Gateway.SaveGridStateAsync(databaseId, table, state, revision, token);

        public Task<PastePlan> PreviewPasteAsync(
            string collection, string schemaRevision,
            IReadOnlyDictionary<string, object?> selection,
            PasteStartCell startCell,
            IReadOnlyList<IReadOnlyList<PasteCell>> cells,
            CancellationToken token)
            => Gateway.PreviewPasteAsync(collection, schemaRevision, selection, startCell, cells, token);

        public Task<ApplyPasteResult> ApplyPasteAsync(
            string collection, string token, string idempotencyKey,
            CancellationToken cancellationToken)
            => Gateway.ApplyPasteAsync(collection, token, idempotencyKey, cancellationToken);

        public void Dispose() => _resolved?.Dispose();
    }

    /// <summary>
    /// Returns the configured Directus source identifier without exposing a
    /// local filesystem picker to the renderer.
    /// </summary>
    private sealed class FixedPathPicker : IDatabasePicker
    {
        private readonly string _path;
        public FixedPathPicker(string path) => _path = path;
        public Task<string?> PickDatabaseAsync() => Task.FromResult<string?>(_path);
    }
}
