using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

internal sealed class FakeDatabasePicker : IDatabasePicker
{
    private readonly string _path;

    public FakeDatabasePicker(string path) => _path = path;

    public Task<string?> PickDatabaseAsync()
        => Task.FromResult<string?>(_path);
}

internal sealed class FakeWebReplySink : IWebReplySink
{
    public sealed record Reply(
        string Type,
        string? RequestId,
        object? Payload);

    private readonly List<Reply> _replies = [];
    private readonly object _gate = new();
    private TaskCompletionSource<bool> _replyAdded = CreateReplySignal();

    public List<Reply> Replies
    {
        get
        {
            lock (_gate)
            {
                return _replies.ToList();
            }
        }
    }

    public void PostNotification(string type, object? payload)
        => Add(new Reply(type, null, payload));

    public void PostResponse(
        string type,
        string? requestId,
        object? payload)
        => Add(new Reply(type, requestId, payload));

    public void PostOperationFailed(
        string? requestId,
        string message,
        string? code = null)
        => Add(new Reply(
            "operation.failed",
            requestId,
            new { message, code }));

    public async Task<Reply?> WaitForAsync(
        string type,
        int timeoutMs = 2000)
    {
        using var timeout = new CancellationTokenSource(timeoutMs);
        while (true)
        {
            Task replyAdded;
            lock (_gate)
            {
                Reply? match = _replies.FirstOrDefault(
                    reply => reply.Type == type);
                if (match is not null)
                {
                    return match;
                }
                replyAdded = _replyAdded.Task;
            }

            try
            {
                await replyAdded.WaitAsync(timeout.Token).ConfigureAwait(false);
            }
            catch (OperationCanceledException) when (timeout.IsCancellationRequested)
            {
                return null;
            }
        }
    }

    public Task<Reply?> WaitForFailedAsync(int timeoutMs = 2000)
        => WaitForAsync("operation.failed", timeoutMs);

    private void Add(Reply reply)
    {
        TaskCompletionSource<bool> replyAdded;
        lock (_gate)
        {
            _replies.Add(reply);
            replyAdded = _replyAdded;
            _replyAdded = CreateReplySignal();
        }
        replyAdded.TrySetResult(true);
    }

    private static TaskCompletionSource<bool> CreateReplySignal()
        => new(TaskCreationOptions.RunContinuationsAsynchronously);
}
