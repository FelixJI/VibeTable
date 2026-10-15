using System;
using System.Threading;
using System.Threading.Tasks;
using System.Windows.Input;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.ViewModels;

/// <summary>
/// Drives the WPF shell's startup state machine and exposes bindable UI
/// properties (<see cref="StatusText"/>, <see cref="IsGridVisible"/>,
/// <see cref="IsRetryVisible"/>, <see cref="RetryCommand"/>).
/// </summary>
/// <remarks>
/// <para>
/// <b>Testability strategy.</b> The ViewModel knows nothing about
/// <c>PythonBackendSupervisor</c> or <c>WebView2</c> directly. It depends on
/// two injected interfaces — <see cref="IBackendLifecycle"/> and
/// <see cref="IWebViewBridge"/> — so unit tests can drive the full state
/// machine WITHOUT STA / WebView2 / a real child process. The concrete
/// adapters live in <c>MainWindow.xaml.cs</c>.
/// </para>
/// <para>
/// <b>State machine.</b> Legal transitions (verbatim from Task 9 brief):
/// </para>
/// <list type="bullet">
/// <item><see cref="StartupState.StartingBackend"/> -&gt;
/// <see cref="StartupState.LoadingWeb"/> -&gt;
/// <see cref="StartupState.Ready"/></item>
/// <item><see cref="StartupState.StartingBackend"/> -&gt;
/// <see cref="StartupState.Faulted"/></item>
/// <item><see cref="StartupState.LoadingWeb"/> -&gt;
/// <see cref="StartupState.Faulted"/></item>
/// <item><see cref="StartupState.Ready"/> -&gt;
/// <see cref="StartupState.Faulted"/></item>
/// <item><see cref="StartupState.Faulted"/> -&gt;
/// <see cref="StartupState.StartingBackend"/> (explicit retry only)</item>
/// </list>
/// <para>
/// The <c>StartingBackend -&gt; Faulted</c> transition happens internally
/// when <see cref="IBackendLifecycle.StartAsync"/> throws; the public
/// <see cref="MoveToFaulted"/> surface is reserved for external fault signals
/// (e.g. WebView2 <c>ProcessFailed</c>) that can only fire while the WebView
/// exists, i.e. from <see cref="StartupState.LoadingWeb"/> or
/// <see cref="StartupState.Ready"/>. Calling <see cref="MoveToFaulted"/> from
/// any other state is a programming error and throws
/// <see cref="InvalidOperationException"/>.
/// </para>
/// </remarks>
public sealed class MainWindowViewModel : ViewModelBase
{
    private readonly IBackendLifecycle _backend;
    private readonly IWebViewBridge _webView;
    private readonly Func<string, Task>? _recoverRenderer;
    private StartupState _state = StartupState.StartingBackend;

    /// <summary>
    /// Constructs the ViewModel. The initial state is
    /// <see cref="StartupState.StartingBackend"/>; the caller drives the first
    /// transition by awaiting <see cref="StartAsync"/>.
    /// </summary>
    public MainWindowViewModel(
        IBackendLifecycle backend,
        IWebViewBridge webView,
        Action? stopPlugins = null,
        Action? returnToPlatform = null,
        Action? managePlugins = null,
        Func<string, Task>? recoverRenderer = null)
    {
        _backend = backend ?? throw new ArgumentNullException(nameof(backend));
        _webView = webView ?? throw new ArgumentNullException(nameof(webView));
        _recoverRenderer = recoverRenderer;
        StopPluginsCommand = new RelayCommand(execute: () => { if (CanRecoverRenderer) stopPlugins?.Invoke(); }, canExecute: () => CanRecoverRenderer);
        ReturnToPlatformCommand = new RelayCommand(execute: () => returnToPlatform?.Invoke(), canExecute: () => true);
        ManagePluginsCommand = new RelayCommand(execute: () => managePlugins?.Invoke(), canExecute: () => true);
        RecoverRendererCommand = new RelayCommand(
            execute: () => _ = RunRendererRecoveryAsync(_recoverRenderer),
            canExecute: () => CanRecoverRenderer);
        RetryCommand = new RelayCommand(
            execute: () => _ = StartAsync(),
            canExecute: () => State == StartupState.Faulted);
    }

    /// <summary>
    /// Current startup state. Set via <see cref="TransitionTo"/> which enforces
    /// the legal-transition table.
    /// </summary>
    public StartupState State
    {
        get => _state;
        private set
        {
            if (_state != value)
            {
                _state = value;
                RaisePropertyChanged(nameof(State));
                RaisePropertyChanged(nameof(StatusText));
                RaisePropertyChanged(nameof(IsGridVisible));
                RaisePropertyChanged(nameof(IsWebViewVisible));
                RaisePropertyChanged(nameof(IsHostFallbackVisible));
                RaisePropertyChanged(nameof(IsRetryVisible));
                ((RelayCommand)RetryCommand).RaiseCanExecuteChanged();
                ((RelayCommand)RecoverRendererCommand).RaiseCanExecuteChanged();
                ((RelayCommand)StopPluginsCommand).RaiseCanExecuteChanged();
            }
        }
    }

    /// <summary>
    /// Human-readable status line for the chrome around the grid.
    /// </summary>
    public string StatusText => State switch
    {
        StartupState.StartingBackend => "正在启动后端…",
        StartupState.LoadingWeb => IsRendererRecovering ? "正在恢复界面…" : "正在加载界面…",
        StartupState.Ready => "就绪",
        StartupState.Faulted => "出现错误",
        _ => State.ToString(),
    };

    private string _detailMessage = string.Empty;

    /// <summary>
    /// The most recent backend progress line, shown in the bottom
    /// status bar while the system is starting or busy. Cleared by the host
    /// (MainWindow code-behind) when the WebView reaches Ready. The ViewModel
    /// itself does not clear it on state transitions — it has no notion of
    /// "host progress" semantics.
    /// </summary>
    public string DetailMessage
    {
        get => _detailMessage;
        set
        {
            if (_detailMessage != value)
            {
                _detailMessage = value;
                RaisePropertyChanged(nameof(DetailMessage));
            }
        }
    }

    /// <summary>
    /// Whether the WebView2 grid area is visible (only in
    /// <see cref="StartupState.Ready"/>).
    /// </summary>
    public bool IsGridVisible => State == StartupState.Ready;

    /// <summary>
    /// Whether the underlying WebView2 HWND should be realized on screen.
    /// True whenever a WebView2 instance exists (LoadingWeb and Ready) so the
    /// control has a non-zero size DURING navigation — a Collapsed/Hidden
    /// WebView2 has a zero-size HWND and the virtual-host navigation can fail
    /// with <c>success=False http=0</c>. <see cref="IsGridVisible"/> remains
    /// the user-facing "grid is interactive" signal; this is the
    /// realization/HWND signal.
    /// </summary>
    public bool IsWebViewVisible => true;

    private bool _shellLoaded;
    private bool _rendererLoadStarted;

    /// <summary>
    /// The native surface is only an initial WebView2 guard and renderer-error
    /// fallback. Backend faults remain visible in the already loaded global
    /// shell, where the user can retry or choose another workspace.
    /// </summary>
    public bool IsHostFallbackVisible => !_shellLoaded;

    public void MarkShellLoaded()
    {
        if (_shellLoaded) return;
        _shellLoaded = true;
        RaisePropertyChanged(nameof(IsHostFallbackVisible));
    }

    public void MarkShellUnavailable()
    {
        if (!_shellLoaded) return;
        _shellLoaded = false;
        RaisePropertyChanged(nameof(IsHostFallbackVisible));
    }

    /// <summary>
    /// Whether the Retry button is visible (only in
    /// <see cref="StartupState.Faulted"/>).
    /// </summary>
    public bool IsRetryVisible => State == StartupState.Faulted;

    /// <summary>
    /// Retry button binding. Enabled only in
    /// <see cref="StartupState.Faulted"/>; executing restarts the backend
    /// (the legal <c>Faulted -&gt; StartingBackend</c> transition).
    /// </summary>
    public ICommand RetryCommand { get; }

    /// <summary>Native host-chrome bindings; always reachable, independent of the WebView.</summary>
    public ICommand StopPluginsCommand { get; }

    public ICommand ReturnToPlatformCommand { get; }

    public ICommand ManagePluginsCommand { get; }

    /// <summary>Renderer-only recovery; does not restart the backend.</summary>
    public ICommand RecoverRendererCommand { get; }

    private bool _isRendererRecovering;

    /// <summary>True while a renderer-only recovery is rebuilding the WebView control.</summary>
    public bool IsRendererRecovering
    {
        get => _isRendererRecovering;
        private set
        {
            if (_isRendererRecovering != value)
            {
                _isRendererRecovering = value;
                RaisePropertyChanged(nameof(IsRendererRecovering));
                RaisePropertyChanged(nameof(StatusText));
                ((RelayCommand)StopPluginsCommand).RaiseCanExecuteChanged();
                ((RelayCommand)RecoverRendererCommand).RaiseCanExecuteChanged();
            }
        }
    }

    public bool CanRecoverRenderer => _rendererLoadStarted && !IsRendererRecovering && State is StartupState.Ready or StartupState.Faulted;

    internal bool TryBeginRendererRecovery(bool rendererFailure = false)
    {
        if (!CanRecoverRenderer && !(rendererFailure && !IsRendererRecovering && State == StartupState.LoadingWeb)) return false;
        IsRendererRecovering = true;
        DetailMessage = "正在恢复界面，请等待…";
        TransitionTo(StartupState.LoadingWeb);
        return true;
    }

    internal Exception? LastStartupError { get; private set; }

    /// <summary>
    /// Begins the startup sequence. Transitions
    /// <c>StartingBackend -&gt; LoadingWeb</c> once the backend is ready, then
    /// (as a fire-and-forget continuation) <c>LoadingWeb -&gt; Ready</c> when
    /// the WebView reports its load completed. Any failure moves to
    /// <see cref="StartupState.Faulted"/>.
    /// </summary>
    /// <remarks>
    /// The method returns as soon as the WebView load has been ISSUED — i.e.
    /// the state is <see cref="StartupState.LoadingWeb"/>. The
    /// <c>LoadingWeb -&gt; Ready</c> transition happens later, when
    /// <see cref="IWebViewBridge.LoadAsync"/> completes. A retry after renderer
    /// failure instead delegates reset/reload and real app.ready settlement to
    /// the existing renderer recovery owner.
    /// </remarks>
    public async Task StartAsync()
    {
        bool rebuildRenderer = State == StartupState.Faulted && _rendererLoadStarted && _recoverRenderer is not null;
        LastStartupError = null;
        // Retry re-enters StartingBackend from Faulted; the legal-transition
        // guard in TransitionTo enforces this.
        TransitionTo(StartupState.StartingBackend);

        try
        {
            await _backend.StartAsync(CancellationToken.None)
                .ConfigureAwait(true);
        }
        catch (Exception ex)
        {
            FaultFromStart(ex);
            return;
        }

        _rendererLoadStarted = true;
        TransitionTo(StartupState.LoadingWeb);

        // Fire-and-forget the WebView load so StartAsync returns in LoadingWeb;
        // a completion/failure here drives the final Ready / Faulted move.
        _ = DriveWebViewLoadAsync(rebuildRenderer);
    }

    /// <summary>
    /// Begins a renderer-only recovery: the shell is unavailable while the
    /// WebView control is rebuilt; the backend and workspace session stay.
    /// Legal from Ready/LoadingWeb/Faulted and re-enters LoadingWeb without a
    /// backend restart (the explicit Faulted→StartingBackend retry stays the
    /// full-restart path).
    /// </summary>
    public void BeginRendererRecovery()
    {
        if (State is not (StartupState.Ready or StartupState.LoadingWeb or StartupState.Faulted))
        {
            throw new InvalidOperationException(
                $"BeginRendererRecovery is not legal from state {State}.");
        }
        if (!TryBeginRendererRecovery(rendererFailure: true))
            throw new InvalidOperationException("Renderer recovery is already active.");
    }

    /// <summary>Ends a successful renderer recovery after real app.ready.</summary>
    public void CompleteRendererRecovery()
    {
        if (!IsRendererRecovering) return;
        IsRendererRecovering = false;
        DetailMessage = string.Empty;
        if (State == StartupState.LoadingWeb)
        {
            MarkShellLoaded();
            TransitionTo(StartupState.Ready);
        }
    }

    /// <summary>Fails a renderer recovery; the full-restart retry stays available.</summary>
    public void FailRendererRecovery(string detail)
    {
        if (!IsRendererRecovering) return;
        IsRendererRecovering = false;
        DetailMessage = detail;
        if (State is StartupState.LoadingWeb or StartupState.Ready)
        {
            MarkShellUnavailable();
            TransitionTo(StartupState.Faulted);
        }
    }

    private async Task RunRendererRecoveryAsync(Func<string, Task>? recoverRenderer)
    {
        if (recoverRenderer is null) return;
        if (CanRecoverRenderer)
        {
            try
            {
                await recoverRenderer("native-recovery-command").ConfigureAwait(true);
            }
            catch (Exception ex)
            {
                FailRendererRecovery(ex.Message);
            }
        }
    }

    /// <summary>
    /// Externally-driven fault transition (e.g. WebView2 ProcessFailed). Only
    /// legal from <see cref="StartupState.LoadingWeb"/> or
    /// <see cref="StartupState.Ready"/> — the states in which a WebView exists
    /// to send the signal. Throws <see cref="InvalidOperationException"/>
    /// otherwise (the StartingBackend -&gt; Faulted transition is owned by
    /// <see cref="StartAsync"/>; Faulted -&gt; Faulted is a no-op-style
    /// programming error).
    /// </summary>
    public void MoveToFaulted(string reason)
    {
        if (State is not (StartupState.LoadingWeb or StartupState.Ready))
        {
            throw new InvalidOperationException(
                $"MoveToFaulted is not legal from state {State}; only " +
                $"{StartupState.LoadingWeb} and {StartupState.Ready} can " +
                $"externally signal a fault. (reason: {reason})");
        }
        TransitionTo(StartupState.Faulted);
    }

    private async Task DriveWebViewLoadAsync(bool rebuildRenderer)
    {
        try
        {
            if (rebuildRenderer && _recoverRenderer is { } recoverRenderer)
            {
                // A completed navigation cache cannot settle a failed business app.ready handshake.
                await recoverRenderer("renderer:full-retry").ConfigureAwait(true);
                if (State == StartupState.LoadingWeb && !IsRendererRecovering)
                    throw new InvalidOperationException("Renderer retry did not complete its recovery admission.");
                // The existing recovery owner settles Ready/Faulted after the new app.ready.
                return;
            }
            await _webView.LoadAsync(CancellationToken.None)
                .ConfigureAwait(true);
        }
        catch (Exception ex)
        {
            // The WebView faulted (runtime missing, renderer crash, etc.).
            // MoveToFaulted guards against double-fault races.
            if (IsRendererRecovering)
            {
                if (rebuildRenderer) FailRendererRecovery(ex.Message);
                return;
            }
            if (State != StartupState.Faulted)
            {
                MarkShellUnavailable();
                TransitionTo(StartupState.Faulted);
            }
            return;
        }

        if (!IsRendererRecovering && State != StartupState.Faulted)
        {
            MarkShellLoaded();
            TransitionTo(StartupState.Ready);
        }
    }

    private void FaultFromStart(Exception ex)
    {
        LastStartupError = ex;
        // StartingBackend -> Faulted. Unconditional: StartAsync's caller has
        // already decided this is fatal.
        TransitionTo(StartupState.Faulted);
    }

    private void TransitionTo(StartupState next)
    {
        if (!IsLegalTransition(State, next))
        {
            throw new InvalidOperationException(
                $"Illegal startup transition: {State} -> {next}.");
        }
        State = next;
    }

    /// <summary>
    /// Encodes the legal-transition table from the Task 9 brief, plus the
    /// #482 renderer-only recovery re-entry into LoadingWeb. Internal so
    /// tests can assert the table directly without re-deriving it.
    /// </summary>
    internal static bool IsLegalTransition(StartupState from, StartupState to)
    {
        return (from, to) switch
        {
            (StartupState.StartingBackend, StartupState.LoadingWeb) => true,
            (StartupState.StartingBackend, StartupState.Faulted) => true,
            (StartupState.LoadingWeb, StartupState.Ready) => true,
            (StartupState.LoadingWeb, StartupState.Faulted) => true,
            (StartupState.Ready, StartupState.Faulted) => true,
            (StartupState.Faulted, StartupState.StartingBackend) => true,
            // Renderer-only recovery rebuilds the WebView control without a
            // backend restart; only BeginRendererRecovery uses this edge.
            (StartupState.Ready, StartupState.LoadingWeb) => true,
            (StartupState.Faulted, StartupState.LoadingWeb) => true,
            // Idempotent re-transition to the same state is allowed so a
            // retried MoveToFaulted under a race is not fatal.
            _ when from == to => true,
            _ => false,
        };
    }
}
