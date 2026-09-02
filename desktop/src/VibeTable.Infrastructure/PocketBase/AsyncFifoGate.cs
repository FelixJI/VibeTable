namespace VibeTable.Infrastructure.PocketBase;

/// <summary>
/// Grants one asynchronous lease at a time in admission order.
/// Cancellation returns to the caller immediately, but its queue turn is not
/// released until the predecessor completes, so successors cannot overtake it.
/// </summary>
internal sealed class AsyncFifoGate
{
    private readonly object _gate = new();
    private Task _tail = Task.CompletedTask;

    public ValueTask<Lease> EnterAsync(CancellationToken cancellationToken)
    {
        cancellationToken.ThrowIfCancellationRequested();
        Task predecessor;
        var released = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        lock (_gate)
        {
            predecessor = _tail;
            _tail = released.Task;
        }
        return AwaitTurnAsync(predecessor, released, cancellationToken);
    }

    private static async ValueTask<Lease> AwaitTurnAsync(
        Task predecessor,
        TaskCompletionSource released,
        CancellationToken cancellationToken)
    {
        try
        {
            await predecessor.WaitAsync(cancellationToken).ConfigureAwait(false);
            cancellationToken.ThrowIfCancellationRequested();
            return new Lease(released);
        }
        catch (OperationCanceledException)
            when (cancellationToken.IsCancellationRequested)
        {
            // Every predecessor is a successful release signal. Preserve the
            // canceled turn in the chain until that signal becomes terminal.
            _ = ReleaseCanceledTurnAsync(predecessor, released);
            throw;
        }
    }

    private static async Task ReleaseCanceledTurnAsync(
        Task predecessor,
        TaskCompletionSource released)
    {
        try
        {
            await predecessor.ConfigureAwait(false);
        }
        finally
        {
            released.TrySetResult();
        }
    }

    internal sealed class Lease(TaskCompletionSource released) : IDisposable
    {
        private TaskCompletionSource? _released = released;

        public void Dispose()
            => Interlocked.Exchange(ref _released, null)?.TrySetResult();
    }
}
