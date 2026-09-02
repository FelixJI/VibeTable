using System;
using System.Diagnostics;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.Diagnostics;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Services;

internal sealed record ProductRuntimeSidecarGeneration(
    long GenerationId,
    PocketBaseAdminContext Context);

internal interface IProductRuntimeRecoveryCandidate
{
    ProductRuntimeSidecarGeneration Generation { get; }
    bool TryCommit(Func<bool> action);
    void Clear();
}

internal sealed class ProductRuntimeRecoveryCandidate(
    ProductRuntimeSidecarGeneration generation,
    Func<Func<bool>, bool> tryCommit,
    Action clear) : IProductRuntimeRecoveryCandidate
{
    public ProductRuntimeSidecarGeneration Generation { get; } = generation;

    public bool TryCommit(Func<bool> action) => tryCommit(action);

    public void Clear() => clear();
}

internal delegate Task<IProductRuntimeRecoveryCandidate?>
    ProductRuntimeRecoveryPreparation(CancellationToken cancellationToken);

internal interface IProductRuntimeRecoveryCoordinator
{
    ProductRuntimeSidecarGeneration? CaptureCurrentGeneration();
    bool IsCurrent(ProductRuntimeSidecarGeneration generation);

    Task<ProductRuntimeRecoveryPreparation?> GetCapabilitiesAsync(
        ProductRuntimeSidecarGeneration generation,
        CancellationToken cancellationToken);
}

/// <summary>
/// Owns the product runtime topology: the private local data process starts
/// before Python, its ephemeral connection material is copied directly into
/// the child environment, and a sidecar recovery rotates the Python RPC
/// client before consumers are notified.
/// </summary>
public sealed class ProductRuntimeService : IAsyncDisposable
{
    private const string SidecarUrlEnvironment = "VIBETABLE_SIDECAR_URL";
    private const string SidecarSecretEnvironment =
        "VIBETABLE_SIDECAR_SESSION_SECRET";
    private readonly ILocalDataService _localData;
    private readonly IPocketBaseSupervisor _sidecar;
    private readonly IBackendSupervisor _backend;
    private readonly IDictionary<string, string> _backendEnvironment;
    private readonly IProductRuntimeRecoveryCoordinator? _recoveryCoordinator;
    private readonly SemaphoreSlim _lifecycle = new(1, 1);
    private readonly object _recoveryGate = new();
    private readonly Lazy<Task> _dispose;
    private RecoveryRequest? _pendingRecovery;
    private ActiveRecovery? _activeRecovery;
    private Task? _recoveryWorker;
    private long _latestGenerationId;
    private int _started;
    private int _disposed;

    public ProductRuntimeService(
        ILocalDataService localData,
        IPocketBaseSupervisor sidecar,
        IBackendSupervisor backend,
        IDictionary<string, string> backendEnvironment)
        : this(localData, sidecar, backend, backendEnvironment, null)
    {
    }

    internal ProductRuntimeService(
        ILocalDataService localData,
        IPocketBaseSupervisor sidecar,
        IBackendSupervisor backend,
        IDictionary<string, string> backendEnvironment,
        IProductRuntimeRecoveryCoordinator? recoveryCoordinator)
    {
        _localData = localData ?? throw new ArgumentNullException(nameof(localData));
        _sidecar = sidecar ?? throw new ArgumentNullException(nameof(sidecar));
        _backend = backend ?? throw new ArgumentNullException(nameof(backend));
        _backendEnvironment = backendEnvironment
            ?? throw new ArgumentNullException(nameof(backendEnvironment));
        _recoveryCoordinator = recoveryCoordinator;
        _dispose = new(DisposeCoreAsync);
        _sidecar.StatusChanged += OnSidecarStatusChanged;
    }

    public event Action? ClientReady;
    public event Action<Exception>? RecoveryFailed;

    internal async Task StartAsync(WorkspaceActivationBudget budget)
    {
        ArgumentNullException.ThrowIfNull(budget);
        ThrowIfDisposed();
        await _lifecycle.WaitAsync().ConfigureAwait(false);
        try
        {
            ThrowIfDisposed();
            await budget.RunAsync(
                WorkspaceActivationStage.Sidecar,
                async token =>
                {
                    try
                    {
                        if (!_localData.GetStatus().IsReady)
                            await _localData.StartAsync(token).ConfigureAwait(false);
                    }
                    finally
                    {
                        PocketBaseStartupTimings? timings =
                            _sidecar.LastStartupTimings;
                        if (timings is not null)
                        {
                            budget.RecordSidecarStartup(
                                timings.SpawnDuration,
                                timings.ReadyRecordDuration,
                                timings.HealthDuration,
                                timings.LastStage);
                        }
                    }
                }).ConfigureAwait(false);
            _sidecar.ConfigureBackendEnvironment(_backendEnvironment);
            await budget.RunAsync(
                WorkspaceActivationStage.Backend,
                async token =>
                {
                    if (_backend.State != BackendState.Ready)
                        await _backend.StartAsync(token).ConfigureAwait(false);
                }).ConfigureAwait(false);
            Volatile.Write(ref _started, 1);
        }
        catch
        {
            Volatile.Write(ref _started, 0);
            throw;
        }
        finally
        {
            _lifecycle.Release();
        }
        Notify(nameof(ClientReady), ClientReady, observer => observer());
    }

    public async Task StopAsync(CancellationToken cancellationToken)
    {
        Task? disposal = null;
        Volatile.Write(ref _started, 0);
        await CancelAndJoinRecoveryAsync().ConfigureAwait(false);
        await _lifecycle.WaitAsync(CancellationToken.None).ConfigureAwait(false);
        try
        {
            if (Volatile.Read(ref _disposed) != 0)
                disposal = _dispose.Value;
            else
            {
                try
                {
                    await _backend.StopAsync(CancellationToken.None)
                        .ConfigureAwait(false);
                }
                finally
                {
                    await _localData.StopAsync(CancellationToken.None)
                        .ConfigureAwait(false);
                }
            }
        }
        finally
        {
            _lifecycle.Release();
        }
        if (disposal is not null)
            await disposal.ConfigureAwait(false);
        cancellationToken.ThrowIfCancellationRequested();
    }

    /// <summary>
    /// Closes Python/job ingress while leaving the private Sidecar available
    /// for its authenticated write-coordinator drain.
    /// </summary>
    public async Task StopIngressAsync(CancellationToken cancellationToken)
    {
        Task? disposal = null;
        Volatile.Write(ref _started, 0);
        await CancelAndJoinRecoveryAsync().ConfigureAwait(false);
        await _lifecycle.WaitAsync(CancellationToken.None).ConfigureAwait(false);
        try
        {
            if (Volatile.Read(ref _disposed) != 0)
                disposal = _dispose.Value;
            else if (_backend.State != BackendState.Stopped)
                await _backend.StopAsync(CancellationToken.None)
                    .ConfigureAwait(false);
        }
        finally
        {
            _lifecycle.Release();
        }
        if (disposal is not null)
            await disposal.ConfigureAwait(false);
        cancellationToken.ThrowIfCancellationRequested();
    }

    /// <summary>
    /// Reopens Python ingress when a switch fails before the previous runtime
    /// is stopped. The Sidecar process and session identity are unchanged.
    /// </summary>
    public async Task ResumeIngressAsync(CancellationToken cancellationToken)
    {
        ThrowIfDisposed();
        await _lifecycle.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            ThrowIfDisposed();
            if (!_localData.GetStatus().IsReady)
                throw new InvalidOperationException(
                    "The workspace Sidecar is no longer ready.");
            _sidecar.ConfigureBackendEnvironment(_backendEnvironment);
            if (_backend.State != BackendState.Ready)
                await _backend.StartAsync(cancellationToken)
                    .ConfigureAwait(false);
            Volatile.Write(ref _started, 1);
        }
        finally
        {
            _lifecycle.Release();
        }
        Notify(nameof(ClientReady), ClientReady, observer => observer());
    }

    public ValueTask DisposeAsync() => new(_dispose.Value);

    private async Task DisposeCoreAsync()
    {
        Volatile.Write(ref _disposed, 1);
        Volatile.Write(ref _started, 0);
        _sidecar.StatusChanged -= OnSidecarStatusChanged;
        await CancelAndJoinRecoveryAsync().ConfigureAwait(false);
        await _lifecycle.WaitAsync(CancellationToken.None).ConfigureAwait(false);
        try
        {
            await _backend.DisposeAsync().ConfigureAwait(false);
        }
        finally
        {
            try
            {
                await _localData.DisposeAsync().ConfigureAwait(false);
            }
            finally
            {
                _lifecycle.Release();
            }
        }
    }

    private void OnSidecarStatusChanged(object? sender, PocketBaseStatus status)
    {
        if (status.State == PocketBaseState.Ready
            && Volatile.Read(ref _started) != 0
            && _recoveryCoordinator?.CaptureCurrentGeneration() is { } generation)
        {
            EnqueueRecovery(generation);
        }
    }

    private void EnqueueRecovery(ProductRuntimeSidecarGeneration generation)
    {
        CancellationTokenSource? superseded = null;
        lock (_recoveryGate)
        {
            if (Volatile.Read(ref _started) == 0
                || Volatile.Read(ref _disposed) != 0
                || generation.GenerationId <= _latestGenerationId)
                return;
            _latestGenerationId = generation.GenerationId;
            _pendingRecovery = new RecoveryRequest(generation);
            superseded = _activeRecovery?.Cancellation;
            _recoveryWorker ??= Task.Run(RunRecoveryWorkerAsync);
        }
        Cancel(superseded);
    }

    private async Task RunRecoveryWorkerAsync()
    {
        while (true)
        {
            ActiveRecovery active;
            lock (_recoveryGate)
            {
                if (Volatile.Read(ref _started) == 0
                    || Volatile.Read(ref _disposed) != 0)
                {
                    _pendingRecovery = null;
                }
                if (_pendingRecovery is null)
                {
                    _recoveryWorker = null;
                    return;
                }
                active = new ActiveRecovery(
                    _pendingRecovery,
                    new CancellationTokenSource());
                _pendingRecovery = null;
                _activeRecovery = active;
            }
            try
            {
                await RecoverGenerationAsync(active).ConfigureAwait(false);
            }
            catch (Exception exception)
            {
                if (IsLatest(active)
                    && _recoveryCoordinator!.IsCurrent(active.Request.Generation))
                    Notify(nameof(RecoveryFailed), RecoveryFailed, observer => observer(exception));
            }
            finally
            {
                lock (_recoveryGate)
                {
                    if (ReferenceEquals(_activeRecovery, active))
                        _activeRecovery = null;
                }
                active.Cancellation.Dispose();
            }
        }
    }

    private async Task RecoverGenerationAsync(ActiveRecovery active)
    {
        IProductRuntimeRecoveryCandidate? candidate = null;
        bool published = false;
        await _lifecycle.WaitAsync(CancellationToken.None).ConfigureAwait(false);
        try
        {
            await _backend.StopAsync(CancellationToken.None).ConfigureAwait(false);
            active.Cancellation.Token.ThrowIfCancellationRequested();
            ProductRuntimeRecoveryPreparation? preparation =
                await _recoveryCoordinator!.GetCapabilitiesAsync(
                    active.Request.Generation,
                    active.Cancellation.Token)
                .ConfigureAwait(false);
            if (preparation is null)
                return;
            candidate = await preparation(active.Cancellation.Token)
                .ConfigureAwait(false);
            if (candidate is null)
                return;
            active.Cancellation.Token.ThrowIfCancellationRequested();
            ConfigureBackendEnvironment(candidate.Generation.Context);
            await _backend.StartAsync(active.Cancellation.Token).ConfigureAwait(false);
            if (!candidate.TryCommit(() => TryCommit(active)))
                return;
            published = TryPublish(active);
        }
        finally
        {
            if (!published)
            {
                try
                {
                    candidate?.Clear();
                }
                finally
                {
                    await _backend.StopAsync(CancellationToken.None)
                        .ConfigureAwait(false);
                }
            }
            _lifecycle.Release();
        }
        if (published && IsLatest(active))
            Notify(nameof(ClientReady), ClientReady, observer => observer());
    }

    private bool TryCommit(ActiveRecovery active)
    {
        lock (_recoveryGate)
        {
            if (!ReferenceEquals(_activeRecovery, active)
                || _pendingRecovery is not null
                || active.Cancellation.IsCancellationRequested
                || Volatile.Read(ref _started) == 0
                || Volatile.Read(ref _disposed) != 0)
                return false;
            active.Committed = true;
            return true;
        }
    }

    private bool TryPublish(ActiveRecovery active)
    {
        lock (_recoveryGate)
        {
            if (!active.Committed
                || active.Published
                || !ReferenceEquals(_activeRecovery, active)
                || _pendingRecovery is not null
                || active.Cancellation.IsCancellationRequested
                || Volatile.Read(ref _started) == 0
                || Volatile.Read(ref _disposed) != 0)
                return false;
            active.Published = true;
            return true;
        }
    }

    private bool IsLatest(ActiveRecovery active)
    {
        lock (_recoveryGate)
        {
            return ReferenceEquals(_activeRecovery, active)
                && _pendingRecovery is null
                && !active.Cancellation.IsCancellationRequested
                && Volatile.Read(ref _started) != 0
                && Volatile.Read(ref _disposed) == 0;
        }
    }

    private async Task CancelAndJoinRecoveryAsync()
    {
        CancellationTokenSource? active;
        Task? worker;
        lock (_recoveryGate)
        {
            _pendingRecovery = null;
            active = _activeRecovery?.Cancellation;
            worker = _recoveryWorker;
        }
        Cancel(active);
        if (worker is not null)
            await worker.ConfigureAwait(false);
    }

    private static void Cancel(CancellationTokenSource? cancellation)
    {
        try
        {
            cancellation?.Cancel();
        }
        catch (ObjectDisposedException)
        {
        }
    }

    private static void Notify<T>(string eventName, T? observers, Action<T> invoke)
        where T : Delegate
    {
        if (observers is null)
            return;
        foreach (T observer in observers.GetInvocationList())
        {
            try { invoke(observer); }
            catch (Exception exception)
            {
                try { Trace.TraceError(DiagnosticEvent.Failure("product", eventName, exception.GetType().Name)); }
                catch (Exception) { }
            }
        }
    }

    private void ConfigureBackendEnvironment(PocketBaseAdminContext context)
    {
        _backendEnvironment[SidecarUrlEnvironment] =
            context.Origin.GetLeftPart(UriPartial.Authority);
        _backendEnvironment[SidecarSecretEnvironment] = context.SessionSecret;
    }

    private void ThrowIfDisposed()
        => ObjectDisposedException.ThrowIf(
            Volatile.Read(ref _disposed) != 0,
            this);

    private sealed record RecoveryRequest(
        ProductRuntimeSidecarGeneration Generation);

    private sealed class ActiveRecovery(
        RecoveryRequest request,
        CancellationTokenSource cancellation)
    {
        internal RecoveryRequest Request { get; } = request;
        internal CancellationTokenSource Cancellation { get; } = cancellation;
        internal bool Committed { get; set; }
        internal bool Published { get; set; }
    }
}
