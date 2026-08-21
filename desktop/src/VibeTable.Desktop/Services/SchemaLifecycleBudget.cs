namespace VibeTable.Desktop.Services;

public enum SchemaLifecycleStage
{
    Inspect,
    Apply,
    Refresh,
}

public sealed class SchemaLifecycleTimeoutException : TimeoutException
{
    public SchemaLifecycleTimeoutException(
        SchemaLifecycleStage stage,
        TimeSpan elapsed)
        : base($"Schema lifecycle exceeded its native deadline during {stage}.")
    {
        Stage = stage;
        Elapsed = elapsed;
    }

    public SchemaLifecycleStage Stage { get; }

    public TimeSpan Elapsed { get; }
}

/// <summary>
/// Owns one native, absolute deadline for a correlated schema lifecycle.
/// Every gateway stage consumes the same budget; a non-cooperative adapter
/// cannot keep the renderer request pending or publish a late success.
/// </summary>
public sealed class SchemaLifecycleBudget : IDisposable
{
    public static TimeSpan DefaultTimeout => TimeSpan.FromSeconds(60);

    private readonly TimeSpan _timeout;
    private readonly CancellationToken _callerToken;
    private readonly TimeProvider _timeProvider;
    private readonly long _startedAt;
    private bool _stageRunning;
    private bool _complete;
    private bool _disposed;

    private SchemaLifecycleBudget(
        TimeSpan timeout,
        CancellationToken callerToken,
        TimeProvider timeProvider)
    {
        _timeout = timeout > TimeSpan.Zero
            ? timeout
            : throw new ArgumentOutOfRangeException(nameof(timeout));
        _callerToken = callerToken;
        _timeProvider = timeProvider ?? throw new ArgumentNullException(nameof(timeProvider));
        _startedAt = _timeProvider.GetTimestamp();
    }

    public static SchemaLifecycleBudget Begin(
        TimeSpan timeout,
        CancellationToken callerToken,
        TimeProvider? timeProvider = null)
        => new(timeout, callerToken, timeProvider ?? TimeProvider.System);

    public async Task<TResult> RunAsync<TResult>(
        SchemaLifecycleStage stage,
        Func<CancellationToken, Task<TResult>> action)
    {
        ArgumentNullException.ThrowIfNull(action);
        ObjectDisposedException.ThrowIf(_disposed, this);
        if (_complete)
            throw new InvalidOperationException("Schema lifecycle is already complete.");
        if (_stageRunning)
            throw new InvalidOperationException("Schema lifecycle stages cannot overlap.");

        _callerToken.ThrowIfCancellationRequested();
        TimeSpan remaining = _timeout - Elapsed();
        if (remaining <= TimeSpan.Zero)
            throw new SchemaLifecycleTimeoutException(stage, Elapsed());

        _stageRunning = true;
        using var operationCancellation =
            CancellationTokenSource.CreateLinkedTokenSource(_callerToken);
        var winner = new TaskCompletionSource<SchemaLifecycleSignal>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        using ITimer deadlineTimer = _timeProvider.CreateTimer(
            static state =>
                ((TaskCompletionSource<SchemaLifecycleSignal>)state!)
                    .TrySetResult(SchemaLifecycleSignal.Deadline),
            winner,
            remaining,
            Timeout.InfiniteTimeSpan);
        using CancellationTokenRegistration callerRegistration =
            _callerToken.CanBeCanceled
                ? _callerToken.Register(
                    static state =>
                        ((TaskCompletionSource<SchemaLifecycleSignal>)state!)
                            .TrySetResult(SchemaLifecycleSignal.Caller),
                    winner)
                : default;
        Task<TResult>? operation = null;
        try
        {
            operation = action(operationCancellation.Token);
            _ = operation.ContinueWith(
                static (_, state) =>
                    ((TaskCompletionSource<SchemaLifecycleSignal>)state!)
                        .TrySetResult(SchemaLifecycleSignal.Operation),
                winner,
                CancellationToken.None,
                TaskContinuationOptions.ExecuteSynchronously,
                TaskScheduler.Default);
            SchemaLifecycleSignal signal = await winner.Task.ConfigureAwait(false);
            if (signal == SchemaLifecycleSignal.Caller)
            {
                TryCancel(operationCancellation);
                ObserveLateCompletion(operation);
                throw new OperationCanceledException(_callerToken);
            }
            if (signal == SchemaLifecycleSignal.Deadline)
            {
                TryCancel(operationCancellation);
                ObserveLateCompletion(operation);
                throw new SchemaLifecycleTimeoutException(stage, Elapsed());
            }
            if (_callerToken.IsCancellationRequested)
            {
                TryCancel(operationCancellation);
                ObserveLateCompletion(operation);
                throw new OperationCanceledException(_callerToken);
            }

            if (Elapsed() >= _timeout)
            {
                ObserveLateCompletion(operation);
                throw new SchemaLifecycleTimeoutException(stage, Elapsed());
            }
            TResult result = await operation.ConfigureAwait(false);
            _callerToken.ThrowIfCancellationRequested();
            if (Elapsed() >= _timeout)
                throw new SchemaLifecycleTimeoutException(stage, Elapsed());
            return result;
        }
        finally
        {
            _stageRunning = false;
        }
    }

    public void Complete(SchemaLifecycleStage lastStage)
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        if (_stageRunning)
            throw new InvalidOperationException("A schema lifecycle stage is still running.");
        _callerToken.ThrowIfCancellationRequested();
        if (Elapsed() >= _timeout)
            throw new SchemaLifecycleTimeoutException(lastStage, Elapsed());
        _complete = true;
    }

    public void Dispose() => _disposed = true;

    private TimeSpan Elapsed()
        => _timeProvider.GetElapsedTime(_startedAt);

    private static void TryCancel(CancellationTokenSource cancellation)
    {
        try { cancellation.Cancel(); }
        catch (ObjectDisposedException) { }
    }

    private static void ObserveLateCompletion(Task operation)
        => _ = operation.ContinueWith(
            completed => _ = completed.Exception,
            CancellationToken.None,
            TaskContinuationOptions.OnlyOnFaulted
                | TaskContinuationOptions.ExecuteSynchronously,
            TaskScheduler.Default);

    private enum SchemaLifecycleSignal
    {
        Operation,
        Deadline,
        Caller,
    }
}
