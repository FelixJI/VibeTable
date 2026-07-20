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
using System.Windows.Controls;
using System.Windows.Input;
using System.Windows.Threading;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.Wpf;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Desktop.ViewModels;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.Directus;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Infrastructure.Workspace;

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
    private readonly IDirectusAdminAuthenticator _adminAuth = new DirectusAdminAuthenticator();
    private readonly AdminSurfaceStateMachine _adminSurfaceState = new();
    private readonly SemaphoreSlim _adminOpenGate = new(1, 1);
    private readonly DispatcherTimer _adminIdleReleaseTimer;
    private CancellationTokenSource? _adminOpenCts;
    private Task<CoreWebView2Environment>? _webViewEnvironmentTask;
    private string? _lastAdminRequestId;
    private int _adminSurfaceGeneration;
    private bool _adminFloatingButtonEnabled = true;
    private bool _adminConfirmClose = true;
    private bool _adminReleaseWhenIdle = true;
    private DirectusSupervisor? _directusSupervisor;
    private readonly TaskCompletionSource<bool>? _directusSessionReady;
    private JsonRpcDirectusGateway? _directusGateway;
    private DocumentWorkspaceHostService? _documentWorkspace;
    private bool _directusSessionAuthenticated;
    private int _directusWorkspaceOpened;
    private DatabaseOpenResult? _directusWorkspaceSnapshot;
    private readonly GridStateCoordinator? _coordinator;
    private readonly TestModeReadinessWriter? _readinessWriter;
    private readonly bool _shellSmokeMode;
    private CancellationTokenSource? _sessionCts;
    private string? _pendingLoginEmail;
    private string? _pendingLoginPassword;
    private string? _localDirectusDirectory;
    private DirectusStartupWindow? _directusStartupWindow;
    private CancellationTokenSource? _directusStartupCts;

    public MainWindow()
    {
        InitializeComponent();
        _adminIdleReleaseTimer = new DispatcherTimer
        {
            Interval = TimeSpan.FromMinutes(10),
        };
        _adminIdleReleaseTimer.Tick += OnAdminIdleReleaseTick;

        // Parse CLI options once. --test-mode enables the shell readiness
        // report used by the real WPF/WebView2/backend smoke test.
        var startupOptions = HostStartupOptions.Current();
        _shellSmokeMode = startupOptions.TestMode;
        string? configuredDirectusUrl =
            Environment.GetEnvironmentVariable("VIBETABLE_DIRECTUS_URL");
        _directusAuto = HostStartupOptions.ShouldAutoStartLocalDirectus(
            startupOptions.DirectusAuto,
            configuredDirectusUrl,
            AppContext.BaseDirectory,
            startupOptions.NoDirectusAuto);
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

        _dispatcher = new WorkspaceRequestDispatcher(
            _workspace, picker, _webBridge, _coordinator);

        _viewModel = new MainWindowViewModel(_backendAdapter, _webBridge);
        _supervisor.LogReceived += OnBackendLogReceived;
        _supervisor.StateChanged += OnBackendStateChanged;
        // Unconditional subscription: clear DetailMessage once the system
        // reaches Ready (so the bootstrap fallback disappears), and — only in
        // shell smoke mode — write the readiness marker. Previously this was
        // an inline lambda gated by _shellSmokeMode.
        _viewModel.PropertyChanged += OnViewModelPropertyChanged;
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
                UpdateStartupHostStage(
                    5,
                    "启动应用后端",
                    "Directus 已就绪，正在启动 VibeTable 后端。");
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
            if (_directusAuto && _supervisor.State != BackendState.Ready)
            {
                _directusStartupWindow?.ShowFailure(
                    "VibeTable 后端未能启动。主窗口已切换到故障状态，可关闭初始化窗口后重试。");
                MessageBox.Show(
                    this,
                    "VibeTable 后端启动失败，请查看 Rider 输出或主窗口状态后重试。",
                    "VibeTable",
                    MessageBoxButton.OK,
                    MessageBoxImage.Error);
                CloseStartupWindow();
                return;
            }
            if (_supervisor.State == BackendState.Ready && _directusEnabled)
            {
                UpdateStartupHostStage(
                    6,
                    "建立登录会话",
                    "正在使用首次设置的本地管理员自动登录。");
                bool authenticated = await EnsureDirectusSessionAsync();
                SetDirectusSessionAuthenticated(
                    authenticated,
                    sessionBoundary: authenticated);
                _directusSessionReady?.TrySetResult(authenticated);
                if (authenticated && _directusAuto)
                {
                    MarkFirstRunCompleted();
                    _directusStartupWindow?.UpdateHostStage(
                        6,
                        "首次启动完成",
                        "本地数据服务、应用后端和登录会话均已就绪。");
                    await Task.Delay(450);
                    CompleteStartupWindow();
                }
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

    private void OnBackendLogReceived(object? sender, string line)
    {
        TraceProcessLog("backend", line);
        SetDetailMessage("后端：" + line);
    }

    private void OnBackendStateChanged(object? sender, BackendState state)
    {
        string status = $"Backend state={state}";
        if (state == BackendState.Faulted)
        {
            status += $", exitCode={_supervisor.ExitCode?.ToString() ?? "unknown"}";
        }
        string? detail = state switch
        {
            BackendState.Starting => "正在启动应用后端…",
            BackendState.Ready => "应用后端已就绪。",
            BackendState.Faulted => "应用后端进程意外退出。",
            _ => null,
        };
        if (detail is not null)
        {
            SetDetailMessage(detail);
        }
        Trace.WriteLine($"[backend] {status}");
        _readinessWriter?.Trace(status);

        if (state == BackendState.Faulted)
        {
            _readinessWriter?.Trace(
                $"Backend stderr: {_supervisor.GetStdErrorLog()}");
            MoveUiToFaulted("Backend process exited unexpectedly.");
        }
    }

    private void OnDirectusLogReceived(object? sender, string line)
    {
        TraceProcessLog("directus", line);
        // Surface significant Directus lifecycle/error lines to the bootstrap
        // fallback. Filtered to avoid drowning the host UI in routine Directus
        // logs; uses the marshalling SetDetailMessage (not Core) because this
        // handler runs on the directus stdout pump thread, not the UI thread.
        if (ContainsSignificantDirectusKeyword(line))
        {
            SetDetailMessage("Directus：" + line);
        }
        try
        {
            Dispatcher.BeginInvoke(() =>
            {
                if (ReferenceEquals(sender, _directusSupervisor))
                {
                    _directusStartupWindow?.AppendLog(line);
                }
            });
        }
        catch
        {
            // The progress window may be closing.
        }
    }

    private void OnDirectusProgressChanged(
        object? sender,
        DirectusStartupProgress progress)
    {
        Trace.WriteLine($"[directus] startup stage={progress.Stage}: {progress.Detail}");
        _readinessWriter?.Trace(
            $"Directus startup stage={progress.Stage}: {progress.Detail}");
        try
        {
            Dispatcher.BeginInvoke(() =>
            {
                if (ReferenceEquals(sender, _directusSupervisor))
                {
                    _directusStartupWindow?.UpdateProgress(progress);
                    // Already inside the outer Dispatcher.BeginInvoke above;
                    // call the core writer directly to avoid a redundant nested
                    // dispatch (whose deferred execution would also escape the
                    // surrounding try/catch).
                    SetDetailMessageCore(DirectusStartupWindow.TranslateDetail(progress.Detail));
                }
            });
        }
        catch
        {
            // The progress window may be closing.
        }
    }

    private void OnDirectusStateChanged(object? sender, DirectusState state)
    {
        string status = $"Directus state={state}";
        Trace.WriteLine($"[directus] {status}");
        _readinessWriter?.Trace(status);
        if (state == DirectusState.Faulted)
        {
            MoveUiToFaulted("Local Directus process exited unexpectedly.");
        }
    }

    private void TraceProcessLog(string source, string line)
    {
        Trace.WriteLine($"[{source}] {line}");
        _readinessWriter?.Trace($"[{source}] {line}");
    }

    private void MoveUiToFaulted(string reason)
    {
        try
        {
            Dispatcher.BeginInvoke(() =>
            {
                // Startup failures are handled by StartAsync itself. This path
                // is for a child that disappears after the shell has moved on.
                if (_viewModel.State is StartupState.LoadingWeb or StartupState.Ready)
                {
                    _viewModel.MoveToFaulted(reason);
                }
            });
        }
        catch
        {
            // The window may already be closing; the process state was still
            // written to Trace for diagnostics.
        }
    }

    private void OnClosed(object? sender, EventArgs e)
    {
        _adminIdleReleaseTimer.Stop();
        _sessionCts?.Cancel();
        _directusStartupCts?.Cancel();
        CloseStartupWindow();
        // Closing is always a capability boundary, including startup failures
        // that never reached an authenticated Directus session.
        SetDirectusSessionAuthenticated(false, sessionBoundary: true);
        _directusSessionReady?.TrySetResult(false);
        _directusGateway?.Dispose();
        _documentWorkspace?.Dispose();
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
        // Node.js is required to run npm install / directus start. The bundled
        // portable Node (runtime/node/) is tried first; PATH is the fallback.
        if (NodeRuntime.FindNode(appBaseDirectory: AppContext.BaseDirectory) is null)
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

        _localDirectusDirectory = options.LocalDirectusDirectory;

        var firstRunStatus = DirectusFirstRunState.Inspect(
            options.LocalDirectusDirectory);
        options.ForcePackageVerification = firstRunStatus.NeedsRuntimeInitialization;

        // Only a genuinely fresh runtime needs local administrator creation.
        // An existing database with an incomplete schema resumes the
        // idempotent schema step with its persisted credentials.
        if (firstRunStatus.IsFresh)
        {
            var setup = new DirectusFirstRunWindow
            {
                Owner = this,
            };
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

        // The native progress window is a runtime bootstrap/fatal-error
        // fallback only. A missing experience marker can mean that login or
        // the renderer was interrupted after the database and schema were
        // already ready; it must not show initialization again or trigger a
        // destructive reset.
        if (firstRunStatus.NeedsRuntimeInitialization)
        {
            EnsureStartupWindow();
        }
        try
        {
            while (true)
            {
                _directusSupervisor = new DirectusSupervisor(options);
                _directusSupervisor.LogReceived += OnDirectusLogReceived;
                _directusSupervisor.ProgressChanged += OnDirectusProgressChanged;
                _directusSupervisor.StateChanged += OnDirectusStateChanged;
                using var attemptCts = CancellationTokenSource.CreateLinkedTokenSource(
                    _sessionCts?.Token ?? CancellationToken.None);
                _directusStartupCts = attemptCts;
                try
                {
                    await _directusSupervisor.StartAsync(attemptCts.Token);
                    return _directusSupervisor.BaseUrl;
                }
                catch (OperationCanceledException)
                {
                    return null;
                }
                catch (Exception ex)
                {
                    // An already-initialized runtime normally starts without
                    // native chrome. Materialize the fallback only when a real
                    // runtime failure needs a retry/close decision.
                    EnsureStartupWindow();
                    _directusStartupWindow?.ShowFailure(ex.Message);
                    bool retry = _directusStartupWindow is not null
                        && await _directusStartupWindow.WaitForFailureDecisionAsync();
                    await _directusSupervisor.DisposeAsync();
                    if (!retry)
                    {
                        return null;
                    }
                    _directusStartupWindow?.ResetForRetry();
                }
                finally
                {
                    if (ReferenceEquals(_directusStartupCts, attemptCts))
                    {
                        _directusStartupCts = null;
                    }
                }
            }
        }
        finally
        {
            options.Environment.Remove("VIBETABLE_DIRECTUS_BOOTSTRAP_EMAIL");
            options.Environment.Remove("VIBETABLE_DIRECTUS_BOOTSTRAP_PASSWORD");
        }
    }

    private void EnsureStartupWindow()
    {
        if (_directusStartupWindow is not null)
        {
            return;
        }
        _directusStartupWindow = new DirectusStartupWindow { Owner = this };
        _directusStartupWindow.CancelRequested += (_, _) =>
            _directusStartupCts?.Cancel();
        _directusStartupWindow.Show();
    }

    private void UpdateStartupHostStage(int step, string title, string detail)
    {
        _directusStartupWindow?.UpdateHostStage(step, title, detail);
        SetDetailMessage(detail);
    }

    /// <summary>
    /// Pushes a progress line into <see cref="MainWindowViewModel.DetailMessage"/>
    /// so it shows in the host bootstrap fallback while the system is starting.
    /// Truncates to 80 chars to keep the message compact, and is a no-op
    /// once the VM has left the starting states (Ready/Faulted). Marshals the write to the UI thread
    /// best-effort, then delegates to <see cref="SetDetailMessageCore"/> for
    /// the actual VM-state-gated write.
    /// </summary>
    /// <remarks>
    /// Use this overload for callers that are NOT already on the UI thread
    /// (e.g. <see cref="OnBackendLogReceived"/>, <see cref="OnBackendStateChanged"/>,
    /// <see cref="OnDirectusLogReceived"/>). Callers already inside a
    /// <c>Dispatcher.BeginInvoke</c> block should call
    /// <see cref="SetDetailMessageCore"/> directly to avoid a redundant nested
    /// dispatch (whose deferred execution also escapes the caller's try/catch).
    /// </remarks>
    private void SetDetailMessage(string? message)
    {
        if (string.IsNullOrWhiteSpace(message))
        {
            return;
        }
        string trimmed = message.Length > 80
            ? message[..77] + "…"
            : message;
        try
        {
            Dispatcher.BeginInvoke(() => SetDetailMessageCore(trimmed));
        }
        catch
        {
            // Window may be closing; best-effort.
        }
    }

    /// <summary>
    /// Performs the VM-state-gated write to
    /// <see cref="MainWindowViewModel.DetailMessage"/>. The caller MUST already
    /// be on the UI thread — no marshalling is performed here. Only updates
    /// while the system is in a starting state (<c>StartingBackend</c> /
    /// <c>LoadingWeb</c>); once <c>Ready</c> the host fallback disappears and
    /// <see cref="MainWindowViewModel.DetailMessage"/> is cleared.
    /// </summary>
    private void SetDetailMessageCore(string trimmed)
    {
        // Only update while the system is not yet Ready; normal operation has
        // no host status strip.
        if (_viewModel.State is StartupState.StartingBackend
            or StartupState.LoadingWeb)
        {
            _viewModel.DetailMessage = trimmed;
        }
    }

    /// <summary>
    /// Conservative keyword filter for <see cref="OnDirectusLogReceived"/>:
    /// only directus stdout lines that look significant (server lifecycle /
    /// errors / readiness) are surfaced to the bootstrap fallback, to avoid
    /// drowning it in verbose routine logs. Case-sensitive as directus emits
    /// these tokens in English.
    /// </summary>
    private static bool ContainsSignificantDirectusKeyword(string line)
    {
        foreach (string keyword in SignificantDirectusLogKeywords)
        {
            if (line.Contains(keyword, StringComparison.Ordinal))
            {
                return true;
            }
        }
        return false;
    }

    private static readonly string[] SignificantDirectusLogKeywords =
        { "Listening", "Server", "started", "error", "Error", "ready", "Ready" };

    /// <summary>
    /// Reacts to ViewModel state changes. When the system reaches Ready the
    /// stale bootstrap progress line is cleared. In shell smoke mode, also
    /// writes the readiness marker.
    /// </summary>
    private void OnViewModelPropertyChanged(
        object? sender,
        System.ComponentModel.PropertyChangedEventArgs e)
    {
        if (!string.Equals(e.PropertyName, nameof(MainWindowViewModel.State),
                StringComparison.Ordinal))
        {
            return;
        }
        // Clear the host fallback detail once the system reaches Ready.
        //
        // This direct write (no Dispatcher marshal) is safe because this
        // handler is invoked synchronously from the VM's State setter via
        // RaisePropertyChanged, and every TransitionTo/state-change call site
        // already runs on the UI thread — so the handler observes the UI
        // thread and the DetailMessage write happens there.
        if (_viewModel.State == StartupState.Ready)
        {
            _viewModel.DetailMessage = string.Empty;
        }
        if (_shellSmokeMode)
        {
            TryWriteShellReadiness();
        }
    }

    private void MarkFirstRunCompleted()
    {
        if (string.IsNullOrWhiteSpace(_localDirectusDirectory))
        {
            return;
        }
        try
        {
            DirectusFirstRunState.MarkExperienceComplete(
                _localDirectusDirectory);
        }
        catch (Exception ex)
        {
            Trace.WriteLine($"[directus] unable to persist first-run marker: {ex.Message}");
        }
    }

    private void CompleteStartupWindow()
    {
        var window = _directusStartupWindow;
        _directusStartupWindow = null;
        try { window?.CompleteAndClose(); }
        catch { /* best-effort UI cleanup */ }
    }

    private void CloseStartupWindow()
    {
        var window = _directusStartupWindow;
        _directusStartupWindow = null;
        try { window?.ForceClose(); }
        catch { /* best-effort UI cleanup */ }
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
            _dispatcher.SetDirectusGateway(_directusGateway);
            _documentWorkspace = new DocumentWorkspaceHostService(
                new JsonRpcDocumentWorkspaceGateway(client),
                new WorkspaceMountStore(),
                new DocumentCapabilityStore(),
                new WindowsLocalDocumentActions());
            _dispatcher.SetDocumentWorkspace(_documentWorkspace);
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
    /// Applies an authentication lifecycle boundary and invalidates all
    /// renderer-visible document handles. Call with <paramref name="sessionBoundary"/>
    /// for every successful login/refresh, logout, and host shutdown.
    /// </summary>
    private void SetDirectusSessionAuthenticated(
        bool authenticated,
        bool sessionBoundary)
    {
        bool changed = _directusSessionAuthenticated != authenticated;
        _directusSessionAuthenticated = authenticated;
        if (sessionBoundary || changed)
        {
            _dispatcher.RotateDocumentCapabilityEpoch();
        }
    }

    /// <summary>
    /// Determines whether a navigation target belongs to the primary app.
    /// Directus deliberately is not accepted here; its WebView has a separate
    /// policy bound to the current local Directus origin.
    /// </summary>
    /// <remarks>
    /// Kept as a small wrapper for trace readability in the bridge.
    /// </remarks>
    private bool IsAppOrigin(string? uri)
        => WebViewNavigationPolicy.IsAppNavigation(uri);

    /// <summary>
    /// Dispatch callback for messages that passed the router's whitelist.
    /// Marks the host ready on <c>app.ready</c>, then forwards the four
    /// Phase-A web request types to the workspace dispatcher which drives the
    /// JSON-RPC flow and posts typed replies back via the WebViewBridge.
    /// </summary>
    private void OnRoutedWebRequest(RoutedWebRequest request)
    {
        _readinessWriter?.Trace($"OnRoutedWebRequest: type={request.Type}");
        // app.ready is replayable: a renderer refresh/rebuild must receive the
        // current authoritative workspace snapshot instead of remaining in an
        // old "opening" state.
        if (string.Equals(request.Type, "app.ready", StringComparison.Ordinal))
        {
            // app.ready identifies a renderer generation. Revoke all handles
            // issued to an earlier page before enabling or replaying state to
            // the new renderer.
            _dispatcher.RotateDocumentCapabilityEpoch();
            _router.IsReady = true;
            TryWriteShellReadiness();
            if (_directusEnabled)
            {
                _ = OpenDirectusWorkspaceAsync();
            }
        }

        if (string.Equals(request.Type, "admin.openRequested", StringComparison.Ordinal))
        {
            ApplyAdminPreferences(request.Payload);
            // Navigation/cookie-injection is a UI concern handled in MainWindow
            // (Task 5). Do NOT forward to _dispatcher.
            _ = OpenDirectusAdminAsync(request.RequestId);
            return;
        }

        _dispatcher.Dispatch(request);
    }

    private async Task OpenDirectusAdminAsync(string? requestId)
    {
        _adminIdleReleaseTimer.Stop();
        _lastAdminRequestId = requestId;
        int generation = Interlocked.Increment(ref _adminSurfaceGeneration);
        var openCts = new CancellationTokenSource(TimeSpan.FromSeconds(30));
        Interlocked.Exchange(ref _adminOpenCts, openCts)?.Cancel();
        bool gateEntered = false;
        try
        {
            await _adminOpenGate.WaitAsync(openCts.Token);
            gateEntered = true;
            if (generation != Volatile.Read(ref _adminSurfaceGeneration))
            {
                return;
            }

            string? baseUrl = _directusSupervisor?.BaseUrl;
            if (string.IsNullOrEmpty(baseUrl))
            {
                ShowAdminFailure(requestId, "Directus 尚未启动，请稍后重试。", "ADMIN_NOT_READY");
                return;
            }

            bool initializeWebView = _adminSurfaceState.BeginOpen();
            ShowAdminLoading();

            // Closing the admin surface deliberately keeps the Directus page
            // alive. Reopening it must reveal that existing page directly:
            // requesting another bootstrap login can invalidate or race the
            // session that the retained page is already using.
            if (_adminSurfaceState.HasReadyPage
                && DirectusWebView.CoreWebView2 is { } reusableCore)
            {
                await reusableCore.ExecuteScriptAsync(
                    $"window.__vibetableSetFloating?.({(_adminFloatingButtonEnabled ? "true" : "false")});");
                DirectusWebView.Visibility = Visibility.Visible;
                AdminLoadingOverlay.Visibility = Visibility.Collapsed;
                AdminErrorOverlay.Visibility = Visibility.Collapsed;
                DirectusWebView.Focus();
                return;
            }

            string runtimeDir = _localDirectusDirectory
                ?? throw new InvalidOperationException("Directus runtime directory not initialized");

            if (!DirectusEnvMaterializer.TryReadBootstrapCredentials(
                    runtimeDir, out string email, out string password))
            {
                ShowAdminFailure(requestId, "找不到本地管理员凭据。", "ADMIN_CREDENTIALS_MISSING");
                return;
            }

            string? cookie = await _adminAuth.LoginAsync(
                    baseUrl, email, password, openCts.Token)
                .ConfigureAwait(true);
            if (generation != Volatile.Read(ref _adminSurfaceGeneration))
            {
                return;
            }
            if (cookie is null)
            {
                ShowAdminFailure(requestId, "管理员会话建立失败，请稍后重试。", "ADMIN_LOGIN_FAILED");
                return;
            }

            if (initializeWebView || DirectusWebView.CoreWebView2 is null)
            {
                await InitializeAdminWebViewAsync(baseUrl);
                _adminSurfaceState.MarkInitialized();
            }
            if (generation != Volatile.Read(ref _adminSurfaceGeneration))
            {
                return;
            }

            var core = DirectusWebView.CoreWebView2;
            if (core is null)
            {
                ShowAdminFailure(requestId, "管理后台 WebView 未就绪。", "WEBVIEW_NOT_READY");
                return;
            }

            var cm = core.CookieManager;
            string host = new Uri(baseUrl).Host; // matches the navigated URL's host (127.0.0.1)
            var c = cm.CreateCookie("directus_session_token", cookie, host, "/");
            cm.AddOrUpdateCookie(c);

            _readinessWriter?.Trace($"OpenDirectusAdminAsync: navigating to admin at {baseUrl}");
            await NavigateAdminAsync(
                core, baseUrl.TrimEnd('/') + "/admin/", openCts.Token);
            if (generation != Volatile.Read(ref _adminSurfaceGeneration))
            {
                return;
            }
            _adminSurfaceState.MarkReady();
            await core.ExecuteScriptAsync(
                $"window.__vibetableSetFloating?.({(_adminFloatingButtonEnabled ? "true" : "false")});");
            DirectusWebView.Visibility = Visibility.Visible;
            AdminLoadingOverlay.Visibility = Visibility.Collapsed;
            AdminErrorOverlay.Visibility = Visibility.Collapsed;
            DirectusWebView.Focus();
        }
        catch (OperationCanceledException) when (openCts.IsCancellationRequested)
        {
            // Closing the surface or starting a newer open cancels this flow.
            // A 30-second timeout while the same surface remains visible is a
            // real failure and should offer the normal retry UI.
            if (ReferenceEquals(Volatile.Read(ref _adminOpenCts), openCts)
                && generation == Volatile.Read(ref _adminSurfaceGeneration)
                && _adminSurfaceState.IsVisible)
            {
                ShowAdminFailure(
                    requestId,
                    "管理后台打开超时，请重试。",
                    "ADMIN_OPEN_TIMEOUT");
            }
        }
        catch (Exception ex)
        {
            if (generation == Volatile.Read(ref _adminSurfaceGeneration))
            {
                ShowAdminFailure(requestId, $"管理后台打开失败：{ex.Message}", "ADMIN_OPEN_ERROR");
            }
        }
        finally
        {
            if (gateEntered)
            {
                _adminOpenGate.Release();
            }
            Interlocked.CompareExchange(ref _adminOpenCts, null, openCts);
            openCts.Dispose();
        }
    }

    private async Task InitializeAdminWebViewAsync(string baseUrl)
    {
        var environment = await GetWebViewEnvironmentAsync();
        await DirectusWebView.EnsureCoreWebView2Async(environment);
        var core = DirectusWebView.CoreWebView2
            ?? throw new InvalidOperationException("Directus WebView2 initialization returned no core.");

        core.NavigationStarting += (_, args) =>
        {
            if (!WebViewNavigationPolicy.IsAdminNavigation(args.Uri, ResolveDirectusBaseUrl()))
            {
                args.Cancel = true;
            }
        };
        core.FrameNavigationStarting += (_, args) =>
        {
            if (!WebViewNavigationPolicy.IsAdminNavigation(args.Uri, ResolveDirectusBaseUrl()))
            {
                args.Cancel = true;
            }
        };
        core.NewWindowRequested += (_, args) =>
        {
            args.Handled = true;
            switch (WebViewNavigationPolicy.ClassifyAdminNewWindow(
                        args.Uri, ResolveDirectusBaseUrl()))
            {
                case WebViewLinkDisposition.CurrentView:
                    core.Navigate(args.Uri);
                    break;
                case WebViewLinkDisposition.ExternalBrowser:
                    OpenExternalUri(args.Uri);
                    break;
            }
        };
        core.ProcessFailed += (_, args) => Dispatcher.BeginInvoke(() =>
        {
            if (_adminSurfaceState.IsVisible)
            {
                ShowAdminFailure(
                    _lastAdminRequestId,
                    $"管理后台渲染进程异常：{args.ProcessFailedKind}",
                    "ADMIN_WEBVIEW_FAILED");
            }
        });
        core.WebMessageReceived += OnAdminWebMessageReceived;
        await core.AddScriptToExecuteOnDocumentCreatedAsync(
            BuildAdminFloatingButtonScript(_adminFloatingButtonEnabled));

#if !DEBUG
        core.Settings.AreDevToolsEnabled = false;
        core.Settings.AreDefaultContextMenusEnabled = false;
        core.Settings.AreBrowserAcceleratorKeysEnabled = false;
#endif
        core.Settings.IsStatusBarEnabled = false;
        _readinessWriter?.Trace($"DirectusWebView initialized for {baseUrl}");
    }

    private static async Task NavigateAdminAsync(
        CoreWebView2 core,
        string url,
        CancellationToken cancellationToken)
    {
        var navigation = new TaskCompletionSource<bool>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        EventHandler<CoreWebView2NavigationCompletedEventArgs>? completed = null;
        completed = (_, args) =>
        {
            if (args.IsSuccess)
            {
                navigation.TrySetResult(true);
            }
            else
            {
                navigation.TrySetException(new InvalidOperationException(
                    $"Directus navigation failed: {args.WebErrorStatus}, HTTP {args.HttpStatusCode}."));
            }
        };
        core.NavigationCompleted += completed;
        using var cancellation = cancellationToken.Register(
            () => navigation.TrySetCanceled(cancellationToken));
        try
        {
            cancellationToken.ThrowIfCancellationRequested();
            core.Navigate(url);
            await navigation.Task;
        }
        finally
        {
            core.NavigationCompleted -= completed;
        }
    }

    private string? ResolveDirectusBaseUrl()
        => _directusSupervisor?.BaseUrl;

    private void ShowAdminLoading()
    {
        // WebView2 uses a child HWND; WPF Panel.ZIndex cannot place another
        // sibling WebView or host chrome above that native surface. Hide only
        // the AppWebView HWND while keeping its CoreWebView2/page alive so the
        // table selection, scroll position, and in-memory state are preserved.
        AppWebView.SetCurrentValue(VisibilityProperty, Visibility.Hidden);
        DirectusWebView.Visibility = Visibility.Hidden;
        AdminSurface.Visibility = Visibility.Visible;
        AdminLoadingOverlay.Visibility = Visibility.Visible;
        AdminErrorOverlay.Visibility = Visibility.Collapsed;
    }

    private void ShowAdminFailure(string? requestId, string message, string code)
    {
        if (!_adminSurfaceState.IsVisible)
        {
            _adminSurfaceState.BeginOpen();
        }
        _adminSurfaceState.MarkFailed(message);
        AppWebView.SetCurrentValue(VisibilityProperty, Visibility.Hidden);
        DirectusWebView.Visibility = Visibility.Hidden;
        AdminSurface.Visibility = Visibility.Visible;
        AdminLoadingOverlay.Visibility = Visibility.Collapsed;
        AdminErrorText.Text = message;
        AdminErrorOverlay.Visibility = Visibility.Visible;
        _webBridge.PostOperationFailed(requestId, message, code);
    }

    private void CloseAdminSurface()
    {
        Interlocked.Increment(ref _adminSurfaceGeneration);
        Interlocked.Exchange(ref _adminOpenCts, null)?.Cancel();
        _adminSurfaceState.Close();
        AdminSurface.Visibility = Visibility.Collapsed;
        AppWebView.SetCurrentValue(VisibilityProperty, Visibility.Visible);
        AdminCloseConfirmOverlay.Visibility = Visibility.Collapsed;
        if (_adminReleaseWhenIdle)
        {
            _adminIdleReleaseTimer.Stop();
            _adminIdleReleaseTimer.Start();
        }
        AppWebView.Focus();
        _ = ReconcileAndRefreshCollectionsAsync(reportFailure: true);
    }

    /// <summary>
    /// Directus can change schema while its Studio is open. Re-listing on close
    /// lets the backend reconcile identifier mappings and refreshes the web
    /// sidebar without recreating the persistent app WebView.
    /// </summary>
    private async Task ReconcileAndRefreshCollectionsAsync(bool reportFailure)
    {
        if (_directusGateway is null || _directusSessionReady is null
            || !_directusSessionReady.Task.IsCompletedSuccessfully
            || !_directusSessionReady.Task.Result)
        {
            return;
        }

        try
        {
            using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(15));
            await _directusGateway.ReconcileIdentifierMappingsAsync(timeout.Token);
            var list = await _directusGateway.ListCollectionsAsync(timeout.Token);
            var tables = DirectusCollectionFilter.FilterUserTables(list.Collections);
            _webBridge.PostNotification("database.collectionsChanged", new
            {
                tables,
                capabilityHashes = list.CapabilityHashes,
                displayNames = list.DisplayNames,
            });
        }
        catch (Exception ex)
        {
            Trace.WriteLine($"[directus] identifier reconcile degraded: {ex.Message}");
            if (reportFailure)
            {
                _webBridge.PostOperationFailed(
                    null,
                    "Directus 结构刷新失败，请稍后重试。",
                    "DIRECTUS_SCHEMA_REFRESH_FAILED");
            }
        }
    }

    private void OnCloseAdminClick(object sender, RoutedEventArgs e)
        => RequestCloseAdminSurface();

    private void OnReloadAdminClick(object sender, RoutedEventArgs e)
        => DirectusWebView.CoreWebView2?.Reload();

    private void OnAdminMoreClick(object sender, RoutedEventArgs e)
    {
        if (sender is System.Windows.Controls.Button button
            && button.ContextMenu is { } menu)
        {
            menu.PlacementTarget = button;
            menu.IsOpen = true;
        }
    }

    private void OnRetryAdminClick(object sender, RoutedEventArgs e)
        => _ = OpenDirectusAdminAsync(_lastAdminRequestId);

    private void OnPreviewKeyDown(object sender, KeyEventArgs e)
    {
        if (e.Key == Key.Escape && _adminSurfaceState.IsVisible)
        {
            if (AdminCloseConfirmOverlay.Visibility == Visibility.Visible)
            {
                AdminCloseConfirmOverlay.Visibility = Visibility.Collapsed;
                DirectusWebView.Visibility = Visibility.Visible;
            }
            else
            {
                RequestCloseAdminSurface();
            }
            e.Handled = true;
        }
    }

    private void ApplyAdminPreferences(JsonElement payload)
    {
        if (payload.ValueKind != JsonValueKind.Object) return;
        if (payload.TryGetProperty("floatingButtonEnabled", out var floating)
            && floating.ValueKind is JsonValueKind.True or JsonValueKind.False)
        {
            _adminFloatingButtonEnabled = floating.GetBoolean();
        }
        if (payload.TryGetProperty("confirmClose", out var confirm)
            && confirm.ValueKind is JsonValueKind.True or JsonValueKind.False)
        {
            _adminConfirmClose = confirm.GetBoolean();
        }
        if (payload.TryGetProperty("releaseWhenIdle", out var release)
            && release.ValueKind is JsonValueKind.True or JsonValueKind.False)
        {
            _adminReleaseWhenIdle = release.GetBoolean();
        }
    }

    private void RequestCloseAdminSurface()
    {
        if (_adminConfirmClose && _adminSurfaceState.State == AdminSurfaceState.Ready)
        {
            // The Directus WebView is another child HWND; hide only its visual
            // surface while the WPF confirmation card is displayed above it.
            DirectusWebView.Visibility = Visibility.Hidden;
            AdminCloseConfirmOverlay.Visibility = Visibility.Visible;
            return;
        }
        CloseAdminSurface();
    }

    private void OnCancelAdminCloseClick(object sender, RoutedEventArgs e)
    {
        AdminCloseConfirmOverlay.Visibility = Visibility.Collapsed;
        DirectusWebView.Visibility = Visibility.Visible;
        DirectusWebView.Focus();
    }

    private void OnConfirmAdminCloseClick(object sender, RoutedEventArgs e)
        => CloseAdminSurface();

    private void OnAdminIdleReleaseTick(object? sender, EventArgs e)
    {
        _adminIdleReleaseTimer.Stop();
        if (_adminReleaseWhenIdle && !_adminSurfaceState.IsVisible)
        {
            ReleaseAdminWebView();
        }
    }

    private void ReleaseAdminWebView()
    {
        Interlocked.Increment(ref _adminSurfaceGeneration);
        Interlocked.Exchange(ref _adminOpenCts, null)?.Cancel();
        var previous = DirectusWebView;
        try { previous.CoreWebView2?.Stop(); } catch { }
        previous.Dispose();
        AdminSurface.Children.Remove(previous);
        try { UnregisterName("DirectusWebView"); } catch { }

        var replacement = new WebView2 { Name = "DirectusWebView" };
        Grid.SetRow(replacement, 1);
        AdminSurface.Children.Insert(1, replacement);
        DirectusWebView = replacement;
        try { RegisterName("DirectusWebView", replacement); } catch { }
        _adminSurfaceState.Release();
        _readinessWriter?.Trace("DirectusWebView released after 10 minutes idle.");
    }

    private void OnAdminWebMessageReceived(
        object? sender, CoreWebView2WebMessageReceivedEventArgs e)
    {
        if (!WebViewNavigationPolicy.IsAdminNavigation(e.Source, ResolveDirectusBaseUrl()))
        {
            return;
        }
        try
        {
            using var document = JsonDocument.Parse(e.WebMessageAsJson);
            if (document.RootElement.ValueKind == JsonValueKind.Object
                && document.RootElement.TryGetProperty("type", out var type)
                && type.ValueKind == JsonValueKind.String
                && string.Equals(type.GetString(), "admin.closeRequested", StringComparison.Ordinal))
            {
                Dispatcher.BeginInvoke(new Action(RequestCloseAdminSurface));
            }
        }
        catch (JsonException)
        {
            // The admin bridge accepts one closed message shape only.
        }
    }

    public static string BuildAdminFloatingButtonScript(bool enabled)
    {
        string initial = enabled ? "true" : "false";
        return $$"""
            (() => {
              if (window.top !== window) return;
              const hostId = 'vibetable-admin-return-host';
              const storageKey = 'vibetable.admin-return-position.v1';
              function remove() { document.getElementById(hostId)?.remove(); }
              function create() {
                if (document.getElementById(hostId) || !document.body) return;
                const host = document.createElement('div');
                host.id = hostId;
                host.style.cssText = 'position:fixed;z-index:2147483647;right:20px;bottom:20px;width:max-content;height:36px;';
                const shadow = host.attachShadow({ mode: 'closed' });
                const button = document.createElement('button');
                button.type = 'button';
                button.textContent = '返回 VibeTable';
                button.setAttribute('aria-label', '返回 VibeTable');
                button.style.cssText = 'height:36px;padding:0 14px;border:1px solid #d9dce1;border-radius:18px;background:#fff;color:#1f2329;font:500 13px/34px -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;box-shadow:0 4px 16px rgba(31,35,41,.14);cursor:grab;user-select:none;touch-action:none;';
                shadow.append(button);
                document.body.append(host);
                try {
                  const saved = JSON.parse(localStorage.getItem(storageKey) || 'null');
                  if (saved && Number.isFinite(saved.x) && Number.isFinite(saved.y)) {
                    host.style.left = `${Math.max(8, Math.min(innerWidth - host.offsetWidth - 8, saved.x * innerWidth))}px`;
                    host.style.top = `${Math.max(8, Math.min(innerHeight - 44, saved.y * innerHeight))}px`;
                    host.style.right = host.style.bottom = 'auto';
                  }
                } catch {}
                let startX = 0, startY = 0, originX = 0, originY = 0, moved = false;
                button.addEventListener('pointerdown', event => {
                  const rect = host.getBoundingClientRect();
                  startX = event.clientX; startY = event.clientY;
                  originX = rect.left; originY = rect.top; moved = false;
                  button.setPointerCapture(event.pointerId); button.style.cursor = 'grabbing';
                });
                button.addEventListener('pointermove', event => {
                  if (!button.hasPointerCapture(event.pointerId)) return;
                  const dx = event.clientX - startX, dy = event.clientY - startY;
                  moved ||= Math.abs(dx) + Math.abs(dy) > 4;
                  host.style.left = `${Math.max(8, Math.min(innerWidth - host.offsetWidth - 8, originX + dx))}px`;
                  host.style.top = `${Math.max(8, Math.min(innerHeight - host.offsetHeight - 8, originY + dy))}px`;
                  host.style.right = host.style.bottom = 'auto';
                });
                button.addEventListener('pointerup', event => {
                  button.releasePointerCapture(event.pointerId); button.style.cursor = 'grab';
                  const rect = host.getBoundingClientRect();
                  try { localStorage.setItem(storageKey, JSON.stringify({ x: rect.left / innerWidth, y: rect.top / innerHeight })); } catch {}
                  if (!moved) window.chrome?.webview?.postMessage({ type: 'admin.closeRequested' });
                });
                addEventListener('resize', () => {
                  const rect = host.getBoundingClientRect();
                  host.style.left = `${Math.max(8, Math.min(innerWidth - rect.width - 8, rect.left))}px`;
                  host.style.top = `${Math.max(8, Math.min(innerHeight - rect.height - 8, rect.top))}px`;
                }, { passive: true });
              }
              window.__vibetableSetFloating = value => value ? create() : remove();
              if ({{initial}}) {
                if (document.readyState === 'loading') addEventListener('DOMContentLoaded', create, { once: true });
                else create();
              }
            })();
            """;
    }

    private static void OpenExternalUri(string? uri)
    {
        if (string.IsNullOrWhiteSpace(uri))
        {
            return;
        }
        try
        {
            Process.Start(new ProcessStartInfo(uri) { UseShellExecute = true });
        }
        catch
        {
            // External navigation is best-effort and never weakens the in-app gate.
        }
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
            if (_directusWorkspaceSnapshot is { } snapshot)
            {
                PostDirectusWorkspaceOpened(snapshot);
            }
            return;
        }
        DatabaseOpenResult result;
        try
        {
            result = await _workspace.OpenDatabaseAsync("directus://configured");
            _coordinator?.SetDatabase("directus");
        }
        catch (Exception ex)
        {
            Interlocked.Exchange(ref _directusWorkspaceOpened, 0);
            _webBridge.PostOperationFailed(null, ex.Message, "DIRECTUS_OPEN_FAILED");
            return;
        }

        // Collection discovery is the authoritative "workspace opened"
        // boundary. Publish and cache it before optional per-collection schema
        // and realtime setup, so those secondary capabilities cannot leave the
        // renderer stuck on "connecting".
        _directusWorkspaceSnapshot = result;
        PostDirectusWorkspaceOpened(result);
        _ = ReconcileAndRefreshCollectionsAsync(reportFailure: false);

        if (_directusGateway is null)
        {
            return;
        }
        foreach (string collection in result.Tables)
        {
            try
            {
                var schema = await _directusGateway.GetSchemaAsync(
                    collection, _sessionCts?.Token ?? CancellationToken.None);
                await _directusGateway.SubscribeAsync(
                    $"grid-{collection}",
                    collection,
                    schema.Columns.Select(column => column.Name).ToArray(),
                    _sessionCts?.Token ?? CancellationToken.None);
            }
            catch (OperationCanceledException)
            {
                return;
            }
            catch (Exception ex)
            {
                // Realtime is an optional enhancement. Keep the opened
                // workspace usable; the next host startup will retry setup.
                Trace.WriteLine(
                    $"[directus] realtime subscription degraded for {collection}: {ex.Message}");
            }
        }
    }

    private void PostDirectusWorkspaceOpened(DatabaseOpenResult result)
        => _webBridge.PostNotification("database.opened", new
        {
            tables = result.Tables,
            views = result.Views,
            displayNames = result.DisplayNames,
        });

    private void OnDirectusChanged(DirectusChange change)
        => _webBridge.PostNotification("directus.changed", change);

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
    /// Both WebViews share one environment/profile so the existing host-owned
    /// Directus session cookie is visible to the lazy admin renderer.
    /// </summary>
    private Task<CoreWebView2Environment> GetWebViewEnvironmentAsync()
        => _webViewEnvironmentTask ??= CoreWebView2Environment.CreateAsync(
            browserExecutableFolder: null,
            userDataFolder: BuildWebViewUserDataFolder());

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
            var webview = _owner.AppWebView;
            _owner._readinessWriter?.Trace("WebViewBridge.LoadAsync: EnsureCoreWebView2Async");

            // A single explicit environment/profile is shared with the lazy
            // Directus WebView. EnsureCoreWebView2Async is idempotent.
            var environment = await _owner.GetWebViewEnvironmentAsync();
            await webview.EnsureCoreWebView2Async(environment)
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

            // 2. App navigation is deliberately limited to the virtual app
            // origin. Directus is trusted only by the separate admin WebView.
            core.NavigationStarting += (_, args) =>
            {
                _owner._readinessWriter?.Trace($"NavigationStarting: uri='{args.Uri}' isAppOrigin={_owner.IsAppOrigin(args.Uri)}");
                if (!_owner.IsAppOrigin(args.Uri))
                {
                    args.Cancel = true;
                }
            };
            core.FrameNavigationStarting += (_, args) =>
            {
                if (!_owner.IsAppOrigin(args.Uri))
                {
                    args.Cancel = true;
                }
            };

            // 3. Same-app links stay here; ordinary http(s) links go to the
            // system browser; unsafe or malformed schemes are swallowed.
            core.NewWindowRequested += (_, args) =>
            {
                args.Handled = true;
                switch (WebViewNavigationPolicy.ClassifyAppNewWindow(args.Uri))
                {
                    case WebViewLinkDisposition.CurrentView:
                        _owner.Dispatcher.Invoke(() => core.Navigate(args.Uri));
                        break;
                    case WebViewLinkDisposition.ExternalBrowser:
                        OpenExternalUri(args.Uri);
                        break;
                }
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
                var core = _owner.AppWebView.CoreWebView2;
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


        public void PostResponse(string type, string? requestId, object? payload)
        {
            if (!_router.IsHostNotificationAllowed(type))
            {
                return;
            }
            _owner.Dispatcher.Invoke(() =>
            {
                var core = _owner.AppWebView.CoreWebView2;
                if (core is null) return;
                string json = JsonSerializer.Serialize(
                    new { type, requestId, payload },
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
                var core = _owner.AppWebView.CoreWebView2;
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
