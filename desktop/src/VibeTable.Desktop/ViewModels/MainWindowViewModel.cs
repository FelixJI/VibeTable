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
    private StartupState _state = StartupState.StartingBackend;

    /// <summary>
    /// Constructs the ViewModel. The initial state is
    /// <see cref="StartupState.StartingBackend"/>; the caller drives the first
    /// transition by awaiting <see cref="StartAsync"/>.
    /// </summary>
    public MainWindowViewModel(IBackendLifecycle backend, IWebViewBridge webView)
    {
        _backend = backend ?? throw new ArgumentNullException(nameof(backend));
        _webView = webView ?? throw new ArgumentNullException(nameof(webView));
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
                RaisePropertyChanged(nameof(IsRetryVisible));
                ((RelayCommand)RetryCommand).RaiseCanExecuteChanged();
            }
        }
    }

    /// <summary>
    /// Human-readable status line for the chrome around the grid.
    /// </summary>
    public string StatusText => State switch
    {
        StartupState.StartingBackend => "Starting backend",
        StartupState.LoadingWeb => "Loading web",
        StartupState.Ready => "Ready",
        StartupState.Faulted => "Faulted",
        _ => State.ToString(),
    };

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
    public bool IsWebViewVisible =>
        State is StartupState.LoadingWeb or StartupState.Ready;

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
    /// <see cref="IWebViewBridge.LoadAsync"/> completes.
    /// </remarks>
    public async Task StartAsync()
    {
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

        TransitionTo(StartupState.LoadingWeb);

        // Fire-and-forget the WebView load so StartAsync returns in LoadingWeb;
        // a completion/failure here drives the final Ready / Faulted move.
        _ = DriveWebViewLoadAsync();
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

    private async Task DriveWebViewLoadAsync()
    {
        try
        {
            await _webView.LoadAsync(CancellationToken.None)
                .ConfigureAwait(true);
        }
        catch (Exception)
        {
            // The WebView faulted (runtime missing, renderer crash, etc.).
            // MoveToFaulted guards against double-fault races.
            if (State != StartupState.Faulted)
            {
                TransitionTo(StartupState.Faulted);
            }
            return;
        }

        if (State != StartupState.Faulted)
        {
            TransitionTo(StartupState.Ready);
        }
    }

    private void FaultFromStart(Exception ex)
    {
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
    /// Encodes the legal-transition table from the Task 9 brief. Internal so
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
            // Idempotent re-transition to the same state is allowed so a
            // retried MoveToFaulted under a race is not fatal.
            _ when from == to => true,
            _ => false,
        };
    }
}
