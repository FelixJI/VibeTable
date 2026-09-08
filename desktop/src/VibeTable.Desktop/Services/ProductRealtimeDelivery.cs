namespace VibeTable.Desktop.Services;

internal sealed class ProductRealtimeDelivery(
    Func<Action, CancellationToken, Task> dispatch,
    Func<bool> available,
    Action<string, object?> post)
{
    private readonly object _gate = new();
    private long _generation;
    private bool _ready;
    internal event Action? Changed;
    internal long? Current { get { lock (_gate) return _ready ? _generation : null; } }

    internal void SetReady(RendererReadyPhase phase)
    {
        if (phase != RendererReadyPhase.Business) return;
        lock (_gate)
        {
            if (_ready) return;
            _ready = true;
        }
        Changed?.Invoke();
    }

    internal void Retire()
    {
        lock (_gate) { _ready = false; _generation++; }
        Changed?.Invoke();
    }

    internal async Task<bool> PostAsync(long generation,
        Func<Action<string, object?>, bool> commit, CancellationToken token)
    {
        bool posted = false;
        await dispatch(() =>
        {
            lock (_gate)
            {
                if (!token.IsCancellationRequested && _ready
                    && _generation == generation && available())
                    posted = commit(post);
            }
        }, token).ConfigureAwait(false);
        return posted;
    }
}
