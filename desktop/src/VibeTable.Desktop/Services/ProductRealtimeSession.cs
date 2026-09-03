using System.Net.Http;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

internal sealed class ProductRealtimeSession : IAsyncDisposable
{
    private readonly object _gate = new();
    private readonly object _refreshGate = new();
    private readonly IProductSidecarGenerationAuthority _authority;
    private readonly Func<ProductSidecarGenerationSnapshot?> _capture;
    private readonly WorkspaceSessionManager _sessions;
    private readonly IWorkspaceHostEpochLeaseSource _leases;
    private readonly ProductRealtimeDelivery _delivery;
    private readonly Action<TableSummary> _applyCatalog;
    private readonly Action<string> _failed;
    private readonly HttpMessageHandler? _handler;
    private readonly Func<TimeSpan, CancellationToken, Task> _delay;
    private Binding? _requested;
    private Scope? _scope;
    private string? _bookmark;
    private CancellationTokenSource? _active;
    private Task? _worker;
    private bool _disposed;

    internal ProductRealtimeSession(IProductSidecarGenerationAuthority authority,
        Func<ProductSidecarGenerationSnapshot?> capture, WorkspaceSessionManager sessions,
        IWorkspaceHostEpochLeaseSource leases, ProductRealtimeDelivery delivery,
        Action<TableSummary> applyCatalog, Action<string> failed,
        HttpMessageHandler? handler = null,
        Func<TimeSpan, CancellationToken, Task>? delay = null)
    {
        _authority = authority;
        _capture = capture;
        _sessions = sessions;
        _leases = leases;
        _delivery = delivery;
        _applyCatalog = applyCatalog;
        _failed = failed;
        _handler = handler;
        _delay = delay ?? Task.Delay;
        authority.CurrentChanged += Refresh;
        sessions.Changed += OnSessionChanged;
        delivery.Changed += Refresh;
        Refresh();
    }

    private void OnSessionChanged(object? sender, WorkspaceSessionChangedEventArgs args) => Refresh();

    private void Refresh()
    {
        lock (_refreshGate)
        {
            ProductSidecarGenerationSnapshot? snapshot = _capture();
            WorkspaceSessionV2 session = _sessions.Current;
            long? renderer = _delivery.Current;
            Scope? scope = renderer is { } generation && session.WorkspaceId is { } workspace
                && _sessions.TryUseCurrentSession(workspace, session.SessionEpoch, () => true)
                ? new(workspace, session.SessionEpoch, generation) : null;
            Binding? next = scope is not null && snapshot is not null
                && snapshot.Identity.WorkspaceId == scope.WorkspaceId.ToString("D")
                && snapshot.Identity.SessionEpoch == scope.Epoch ? new(snapshot, scope) : null;
            lock (_gate)
            {
                if (_disposed) return;
                if (_scope != scope) { _scope = scope; _bookmark = null; }
                if (_requested == next) return;
                _requested = next;
                _active?.Cancel();
                _worker ??= Task.Run(RunAsync);
            }
        }
    }

    private async Task RunAsync()
    {
        while (true)
        {
            Binding? binding;
            CancellationTokenSource cancellation;
            lock (_gate)
            {
                binding = _requested;
                if (_disposed || binding is null) { _worker = null; return; }
                cancellation = new();
                _active = cancellation;
            }
            try { await RunBindingAsync(binding, cancellation.Token).ConfigureAwait(false); }
            catch (OperationCanceledException) when (cancellation.IsCancellationRequested) { }
            catch (Exception error)
            {
                if (!cancellation.IsCancellationRequested)
                    _failed("realtime.failed:" + error.GetType().Name);
            }
            finally
            {
                lock (_gate) { _active = null; cancellation.Dispose(); }
            }
            lock (_gate)
            {
                if (_requested == binding) { _worker = null; return; }
            }
        }
    }

    private async Task RunBindingAsync(Binding binding, CancellationToken lifetime)
    {
        if (!_leases.TryCaptureHost(binding.Scope.WorkspaceId, binding.Scope.Epoch,
                Guid.NewGuid(), out WorkspaceRequestEpochLease? lease) || lease is null) return;
        using (lease)
        using (var cancellation = CancellationTokenSource.CreateLinkedTokenSource(lifetime, lease.CancellationToken))
        {
            CancellationToken token = cancellation.Token;
            bool Current(Func<bool> action) => _authority.TryUseCurrent(binding.Snapshot, () =>
                _sessions.TryUseCurrentSession(binding.Scope.WorkspaceId, binding.Scope.Epoch, () =>
                {
                    lock (_gate)
                        return !_disposed && !token.IsCancellationRequested && _requested == binding
                            && _leases.IsCurrent(lease) && action();
                }));
            using var catalog = new ProductRealtimeCatalog(binding.Snapshot, Current, _handler);
            int attempt = 0;
            bool posting = false;
            try
            {
                while (!token.IsCancellationRequested && Current(() => true))
                {
                    string? after;
                    lock (_gate) after = _bookmark;
                    var stream = new ProductRealtimeStream(binding.Snapshot.Context, _handler);
                    bool reading = true;
                    try
                    {
                        await using var reader = stream.ReadAsync(after, token).GetAsyncEnumerator(token);
                        while (await reader.MoveNextAsync().ConfigureAwait(false))
                        {
                            reading = false;
                            ProductRealtimeFrame frame = reader.Current;
                            TableSummary? summary = frame.Topic == "realtime.recovered"
                                ? await catalog.ReadAsync(lease, token).ConfigureAwait(false) : null;
                            posting = true;
                            bool posted = await _delivery.PostAsync(binding.Scope.Renderer, post => Current(() =>
                            {
                                if (summary is not null)
                                    post("database.collectionsChanged", new
                                    { tables = summary.Tables, views = summary.Views, displayNames = summary.DisplayNames });
                                post(frame.Topic, frame.Payload);
                                if (summary is not null) _applyCatalog(summary);
                                _bookmark = frame.Cursor;
                                return true;
                            }), token).ConfigureAwait(false);
                            posting = false;
                            if (!posted) return;
                            attempt = 0;
                            reading = true;
                        }
                    }
                    catch (BackendUnavailableException) when (reading) { }
                    catch (ProductRealtimeRequestException error) when (reading && error.Retryable) { }
                    await _delay(TimeSpan.FromSeconds(Math.Min(30, Math.Pow(2, Math.Min(++attempt, 5)))), token)
                        .ConfigureAwait(false);
                }
            }
            catch (OperationCanceledException) when (token.IsCancellationRequested) { }
            catch (Exception error)
            {
                if (token.IsCancellationRequested) return;
                _failed("realtime.failed:" + error.GetType().Name);
                // A failed renderer Post cannot carry another notification.
                if (posting) return;
                try
                {
                    await _delivery.PostAsync(binding.Scope.Renderer, post => Current(() =>
                    {
                        post("operation.failed", new
                        {
                            operation = "realtime.stream", code = "realtime.stopped",
                            message = "Live updates stopped. Close and reopen the workspace.",
                        });
                        return true;
                    }), token).ConfigureAwait(false);
                }
                catch (OperationCanceledException) when (token.IsCancellationRequested) { }
            }
        }
    }

    public async ValueTask DisposeAsync()
    {
        Task? worker;
        lock (_gate)
        {
            _disposed = true;
            _requested = null;
            _scope = null;
            _bookmark = null;
            _active?.Cancel();
            worker = _worker;
        }
        _authority.CurrentChanged -= Refresh;
        _sessions.Changed -= OnSessionChanged;
        _delivery.Changed -= Refresh;
        if (worker is not null) await worker.ConfigureAwait(false);
    }

    private sealed record Scope(Guid WorkspaceId, ulong Epoch, long Renderer);
    private sealed record Binding(ProductSidecarGenerationSnapshot Snapshot, Scope Scope);
}
