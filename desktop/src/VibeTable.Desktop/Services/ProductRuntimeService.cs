using System;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Owns the product runtime topology: the private local data process starts
/// before Python, its ephemeral connection material is copied directly into
/// the child environment, and a sidecar recovery rotates the Python RPC
/// client before consumers are notified.
/// </summary>
public sealed class ProductRuntimeService : IBackendLifecycle, IAsyncDisposable
{
    private readonly LocalDataService _localData;
    private readonly PocketBaseSupervisor _sidecar;
    private readonly PythonBackendSupervisor _backend;
    private readonly BackendLaunchOptions _backendOptions;
    private readonly SemaphoreSlim _lifecycle = new(1, 1);
    private readonly SemaphoreSlim _recovery = new(1, 1);
    private int _started;
    private int _disposed;

    public ProductRuntimeService(
        LocalDataService localData,
        PocketBaseSupervisor sidecar,
        PythonBackendSupervisor backend,
        BackendLaunchOptions backendOptions)
    {
        _localData = localData ?? throw new ArgumentNullException(nameof(localData));
        _sidecar = sidecar ?? throw new ArgumentNullException(nameof(sidecar));
        _backend = backend ?? throw new ArgumentNullException(nameof(backend));
        _backendOptions = backendOptions
            ?? throw new ArgumentNullException(nameof(backendOptions));
        _sidecar.StatusChanged += OnSidecarStatusChanged;
    }

    public event Action? ClientReady;
    public event Action<Exception>? RecoveryFailed;

    public PythonBackendSupervisor Backend => _backend;

    public async Task StartAsync(CancellationToken cancellationToken)
    {
        ThrowIfDisposed();
        await _lifecycle.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            ThrowIfDisposed();
            LocalDataStatus status = _localData.GetStatus();
            if (!status.IsReady)
            {
                await _localData.StartAsync(cancellationToken).ConfigureAwait(false);
            }
            _sidecar.ConfigureBackendEnvironment(_backendOptions.Environment);
            if (_backend.State != BackendState.Ready)
            {
                await _backend.StartAsync(cancellationToken).ConfigureAwait(false);
            }
            Volatile.Write(ref _started, 1);
            ClientReady?.Invoke();
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
    }

    public async Task StopAsync(CancellationToken cancellationToken)
    {
        Volatile.Write(ref _started, 0);
        await _lifecycle.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
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
        finally
        {
            _lifecycle.Release();
        }
        cancellationToken.ThrowIfCancellationRequested();
    }

    /// <summary>
    /// Closes Python/job ingress while leaving the private Sidecar available
    /// for its authenticated write-coordinator drain.
    /// </summary>
    public async Task StopIngressAsync(CancellationToken cancellationToken)
    {
        Volatile.Write(ref _started, 0);
        await _lifecycle.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            if (_backend.State != BackendState.Stopped)
                await _backend.StopAsync(CancellationToken.None)
                    .ConfigureAwait(false);
        }
        finally
        {
            _lifecycle.Release();
        }
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
            _sidecar.ConfigureBackendEnvironment(_backendOptions.Environment);
            if (_backend.State != BackendState.Ready)
                await _backend.StartAsync(cancellationToken)
                    .ConfigureAwait(false);
            Volatile.Write(ref _started, 1);
            ClientReady?.Invoke();
        }
        finally
        {
            _lifecycle.Release();
        }
    }

    public async ValueTask DisposeAsync()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0)
        {
            return;
        }
        Volatile.Write(ref _started, 0);
        _sidecar.StatusChanged -= OnSidecarStatusChanged;
        try
        {
            await _backend.DisposeAsync().ConfigureAwait(false);
        }
        finally
        {
            await _localData.DisposeAsync().ConfigureAwait(false);
            _lifecycle.Dispose();
            _recovery.Dispose();
        }
    }

    private void OnSidecarStatusChanged(object? sender, PocketBaseStatus status)
    {
        if (status.State == PocketBaseState.Ready
            && Volatile.Read(ref _started) != 0
            && _backend.State == BackendState.Ready)
        {
            _ = RecoverBackendAsync();
        }
    }

    private async Task RecoverBackendAsync()
    {
        if (!await _recovery.WaitAsync(0).ConfigureAwait(false))
        {
            return;
        }
        try
        {
            if (Volatile.Read(ref _started) == 0
                || _sidecar.GetStatus().State != PocketBaseState.Ready)
            {
                return;
            }
            await _backend.StopAsync(CancellationToken.None).ConfigureAwait(false);
            _sidecar.ConfigureBackendEnvironment(_backendOptions.Environment);
            await _backend.StartAsync(CancellationToken.None).ConfigureAwait(false);
            ClientReady?.Invoke();
        }
        catch (Exception exception)
        {
            RecoveryFailed?.Invoke(exception);
        }
        finally
        {
            _recovery.Release();
        }
    }

    private void ThrowIfDisposed()
        => ObjectDisposedException.ThrowIf(
            Volatile.Read(ref _disposed) != 0,
            this);
}
