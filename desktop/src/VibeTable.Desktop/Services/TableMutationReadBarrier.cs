using System.Text.Json;

namespace VibeTable.Desktop.Services;

/// <summary>Orders recovery reads after admitted local writes without replaying an uncertain write.</summary>
internal sealed class TableMutationReadBarrier : IDisposable
{
    private readonly object _gate = new();
    private readonly Dictionary<(Guid, ulong, string), PendingTable> _tables = [];
    private CancellationTokenSource _reads = new();
    private bool _disposed;

    internal async Task TrackAsync(RoutedWebRequest request, Func<Action<bool>, Task> operation)
    {
        var key = Key(request);
        var completion = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        PendingTable state;
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            if (!_tables.TryGetValue(key, out state!)) _tables[key] = state = new();
            state.Writes.Add(completion.Task);
        }
        bool terminalKnown = true; // Payload rejection cannot have submitted a remote write.
        try { await operation(known => terminalKnown = known).ConfigureAwait(false); }
        finally
        {
            lock (_gate)
            {
                state.Uncertain |= !terminalKnown;
                state.Writes.Remove(completion.Task);
                completion.TrySetResult(terminalKnown);
                if (!state.Uncertain && state.Writes.Count == 0
                    && _tables.TryGetValue(key, out var current) && ReferenceEquals(current, state))
                    _tables.Remove(key);
            }
        }
    }

    internal async Task WaitAsync(RoutedWebRequest request, CancellationToken token)
    {
        Task<bool>[] writes;
        CancellationTokenSource lifetime;
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            token.ThrowIfCancellationRequested();
            if (_tables.TryGetValue(Key(request), out var state))
            {
                if (state.Uncertain) throw Unconfirmed();
                writes = state.Writes.ToArray();
            }
            else writes = [];
            lifetime = CancellationTokenSource.CreateLinkedTokenSource(token, _reads.Token);
        }
        using (lifetime)
        {
            bool[] terminal = await Task.WhenAll(writes).WaitAsync(lifetime.Token).ConfigureAwait(false);
            lifetime.Token.ThrowIfCancellationRequested();
            if (terminal.Any(known => !known)) throw Unconfirmed();
        }
    }

    // A table change retires its waiting reads, but cannot cancel a remotely sent write.
    internal void RetireReads(bool retireAuthority = false)
    {
        CancellationTokenSource retired;
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            retired = _reads;
            _reads = new();
            if (retireAuthority) _tables.Clear();
        }
        retired.Cancel();
        retired.Dispose();
    }

    public void Dispose()
    {
        CancellationTokenSource retired;
        lock (_gate)
        {
            if (_disposed) return;
            _disposed = true;
            retired = _reads;
            _tables.Clear();
        }
        retired.Cancel();
        retired.Dispose();
    }

    private static (Guid, ulong, string) Key(RoutedWebRequest request)
    {
        string table = "";
        if (request.Payload.ValueKind == JsonValueKind.Object)
        {
            foreach (string name in new[] { "table", "collection" })
                if (request.Payload.TryGetProperty(name, out var value) && value.ValueKind == JsonValueKind.String)
                { table = value.GetString()!; break; }
        }
        return (request.Scope?.WorkspaceId ?? Guid.Empty, request.Scope?.SessionEpoch ?? 0, table);
    }

    private static TableMutationUnconfirmedException Unconfirmed() => new();

    private sealed class PendingTable
    {
        internal HashSet<Task<bool>> Writes { get; } = [];
        internal bool Uncertain { get; set; }
    }
}

internal sealed class TableMutationUnconfirmedException() : InvalidOperationException(
    "先前写入结果尚未确认；请重新打开工作区并核实数据，不要直接重试写入。");
