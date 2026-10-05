using System.Net.Http;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;
using TaskStatus = VibeTable.Contracts.TaskStatus;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Workspace-runtime owner of public Data IO task identity, history and events.
/// Worker clients only execute and report; replacing one never loses a terminal receipt.
/// The durable import history journal is owned here as well, so terminal
/// settlements outlive invoker replacements and disposals.
/// </summary>
internal sealed partial class HostDataIoTaskRegistry : IDisposable
{
    private const int MaxTasks = 256;
    private readonly object _gate = new();
    private readonly Dictionary<string, Record> _tasks = new(StringComparer.Ordinal);
    private JsonRpcClient? _client;
    private Action? _clientTerminatedHandler;
    private Action<string, JsonElement>? _reportHandler;
    private ProductSidecarIdentity? _identity;
    private ProductImportHistoryJournal? _history;
    private long _generation;
    private long _historyBinds;
    private long _sequence;
    private bool _disposed;
    private static readonly JsonSerializerOptions Wire = new(JsonSerializerDefaults.Web);

    internal event Action<JsonElement>? TaskChanged;
    internal JsonRpcClient? CurrentClient { get { lock (_gate) return _client; } }

    /// <summary>
    /// Configures the durable import history sink for the current Go Sidecar
    /// generation. Only a still-current generation may bind, so a retired
    /// invoker can never shadow the journal of a newer generation.
    /// </summary>
    internal void BindImportHistory(
        ProductSidecarGenerationSnapshot snapshot,
        HttpMessageHandler? handler = null)
    {
        ArgumentNullException.ThrowIfNull(snapshot);
        long observedBinds;
        lock (_gate)
        {
            if (_disposed
                || ReferenceEquals(_history?.Snapshot, snapshot))
                return;
            observedBinds = _historyBinds;
        }
        if (!snapshot.TryUseCurrent(() => true)) return;
        var journal = new ProductImportHistoryJournal(snapshot, handler);
        ProductImportHistoryJournal? retired = null;
        lock (_gate)
        {
            // The bind sequence guard closes the retire race between the
            // currency check and this assignment: a stale snapshot can never
            // overwrite the journal of a newer generation that bound in
            // between. Per-call fencing covers the remaining retirement
            // window fail-closed.
            if (_disposed
                || ReferenceEquals(_history?.Snapshot, snapshot)
                || _historyBinds != observedBinds)
            {
                journal.Dispose();
                return;
            }
            retired = _history;
            _history = journal;
            _historyBinds++;
        }
        retired?.Dispose();
    }

    /// <summary>
    /// Persists the durable start entry for an admitted import task. The
    /// caller must invoke this before starting worker execution; its failure
    /// blocks the business run.
    /// </summary>
    internal async Task StartImportHistoryAsync(
        string taskId,
        string collection,
        string sourceType,
        string sourceName,
        string idempotencyKey,
        CancellationToken token)
    {
        ProductImportHistoryJournal? journal;
        ProductSidecarIdentity identity;
        long generation;
        lock (_gate)
        {
            if (_disposed
                || !_tasks.TryGetValue(taskId, out Record? record)
                || record.Generation != _generation)
            {
                throw new InvalidOperationException(
                    "Data IO task is no longer current.");
            }
            journal = _history;
            identity = _identity!;
            generation = record.Generation;
            if (journal is null || journal.Identity != identity)
                throw new BackendUnavailableException(
                    "Import history persistence is not bound to this session.");
        }
        await journal.StartAsync(
            new ImportHistoryStart(
                taskId,
                collection,
                sourceType,
                sourceName,
                idempotencyKey,
                identity.SessionEpoch),
            token).ConfigureAwait(false);
        bool settleAborted = false;
        lock (_gate)
        {
            if (!_disposed
                && _tasks.TryGetValue(taskId, out Record? record)
                && record.Generation == generation
                && ReferenceEquals(_history, journal))
            {
                record.HistoryStarted = true;
                settleAborted = Terminal(record.Status.State);
            }
        }
        if (settleAborted) journal.Finish(taskId, TaskStates.Aborted);
    }

    /// <summary>
    /// Reads the durable import history and overlays the live queued/running
    /// state of the current session's in-flight tasks. Liveness is re-checked
    /// after the durable read, so a worker exit during the wait cannot
    /// project a dead task as running. This read never starts Python and
    /// never rewrites a Go receipt.
    /// </summary>
    internal Task<JsonElement> ReadImportHistoryAsync(CancellationToken token)
    {
        ProductImportHistoryJournal? journal;
        lock (_gate)
        {
            journal = _history;
            if (_disposed) journal = null;
            if (journal is null) return Task.FromException<JsonElement>(
                new BackendUnavailableException(
                    "Import history persistence is not bound."));
        }
        return journal.ListAsync(LiveTask, token);

        (string State, ulong SessionEpoch)? LiveTask(string taskId)
        {
            lock (_gate)
            {
                if (_disposed
                    || !_tasks.TryGetValue(taskId, out Record? record)
                    || !ReferenceEquals(record.Client, _client)
                    || record.Generation != _generation
                    || record.Identity != _identity
                    || record.Status.State is not (TaskStates.Queued
                        or TaskStates.Running))
                    return null;
                return (record.Status.State, record.Identity.SessionEpoch);
            }
        }
    }

    /// <summary>Awaits started terminal history settlements within a budget.</summary>
    internal Task DrainImportHistoryAsync(TimeSpan budget)
    {
        ProductImportHistoryJournal? journal;
        lock (_gate) journal = _history;
        return journal?.DrainAsync(budget) ?? Task.CompletedTask;
    }


    internal void BindClient(JsonRpcClient? client, ProductSidecarIdentity identity)
    {
        List<JsonElement> changed;
        List<string> historyAborts = [];
        ProductImportHistoryJournal? journal;
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            if (ReferenceEquals(client, _client) && identity == _identity) return;
            journal = _history;
            changed = AbortCurrentLocked("数据执行通道已更换；业务提交结果待核实，请核对数据后重新预览。",
                historyAborts);
            if (_client is not null)
            {
                if (_clientTerminatedHandler is not null) _client.Terminated -= _clientTerminatedHandler;
                if (_reportHandler is not null) _client.NotificationReceived -= _reportHandler;
            }
            _client = client;
            _identity = identity;
            _generation++;
            _clientTerminatedHandler = client is null ? null : () => RetireClient(client);
            _reportHandler = client is null ? null : (method, payload) =>
            {
                if (method == "task.executionReport") ApplyReport(client, payload);
            };
            if (client is not null)
            {
                client.NotificationReceived += _reportHandler;
                client.Terminated += _clientTerminatedHandler;
            }
        }
        // Replacing the worker client settles its abandoned imports; a Go
        // commit that raced the replacement still wins in its transaction.
        foreach (string taskId in historyAborts) journal?.Finish(taskId, TaskStates.Aborted);
        Publish(changed);
    }

    internal (string TaskId, JsonElement Initial) Admit(
        JsonRpcClient client, ProductSidecarIdentity identity, string kind)
    {
        if (kind is not ("data.import" or "data.export"))
            throw new JsonException("Unsupported Data IO task kind.");
        lock (_gate)
        {
            if (_disposed || !ReferenceEquals(client, _client) || identity != _identity)
                throw new InvalidOperationException("Data IO execution client is no longer current.");
            string taskId = "task-" + Guid.NewGuid().ToString("N")[..12];
            var record = new Record(client, _generation, identity,
                new TaskStatus(taskId, kind, TaskStates.Queued,
                    new TaskProgress(0, 0, ""), null, null));
            _tasks.Add(taskId, record);
            PruneLocked();
            return (taskId, Snapshot(record));
        }
    }

    internal JsonElement Status(string taskId)
    {
        lock (_gate)
        {
            if (_sourceTasks.TryGetValue(taskId, out SourceTask? source))
                return JsonSerializer.SerializeToElement(source.Status, Wire);
            return _tasks.TryGetValue(taskId, out Record? record)
                ? Snapshot(record)
                : throw new KeyNotFoundException("Data IO task is not in this workspace.");
        }
    }

    internal (JsonElement Snapshot, JsonRpcClient? Client) RequestCancel(string taskId)
    {
        if (TryCancelSourceImport(taskId, out JsonElement source)) return (source, null);
        lock (_gate)
        {
            if (!_tasks.TryGetValue(taskId, out Record? record))
                throw new KeyNotFoundException("Data IO task is not in this workspace.");
            if (Terminal(record.Status.State)) return (Snapshot(record), null);
            if (!ReferenceEquals(record.Client, _client) || record.Generation != _generation)
                return (Snapshot(record), null);
            return (Snapshot(record), record.Client);
        }
    }

    internal void ApplyReport(JsonRpcClient client, JsonElement payload)
    {
        TaskStatus report;
        try { report = payload.Deserialize<TaskStatus>(Wire)
            ?? throw new JsonException("Data IO report is empty."); }
        catch (JsonException) { return; }
        JsonElement? changed = null;
        string? settleState = null;
        ProductImportHistoryJournal? journal;
        lock (_gate)
        {
            journal = _history;
            if (!_tasks.TryGetValue(report.TaskId, out Record? record)
                || !ReferenceEquals(client, _client)
                || !ReferenceEquals(client, record.Client)
                || record.Generation != _generation
                || record.Identity != _identity
                || report.Kind != record.Status.Kind
                || report.Progress is null
                || report.Progress.Done < 0
                || report.Progress.Message is null
                || Terminal(record.Status.State)
                || report.State is not (TaskStates.Running or TaskStates.Succeeded
                    or TaskStates.Failed or TaskStates.Cancelled)
                || report.Progress.Done < record.Status.Progress.Done
                || report.Progress.Total < 0) return;
            record.Status = report;
            changed = EventLocked(record);
            // Success is proven only by the Go commit transaction; the Host
            // persists finishes for the other terminal outcomes here.
            if (record.HistoryStarted
                && report.State is (TaskStates.Failed or TaskStates.Cancelled))
                settleState = report.State;
        }
        if (settleState is not null) journal?.Finish(report.TaskId, settleState);
        if (changed is JsonElement notification) Publish([notification]);
    }

    internal JsonElement AbortTask(string taskId, string reason)
    {
        JsonElement? changed = null;
        JsonElement snapshot;
        bool settleAborted = false;
        ProductImportHistoryJournal? journal;
        lock (_gate)
        {
            journal = _history;
            if (!_tasks.TryGetValue(taskId, out Record? record))
                throw new KeyNotFoundException("Data IO task is not in this workspace.");
            if (!Terminal(record.Status.State))
            {
                record.Status = record.Status with
                {
                    State = TaskStates.Aborted,
                    Error = reason,
                };
                changed = EventLocked(record);
                settleAborted = record.HistoryStarted;
            }
            snapshot = Snapshot(record);
        }
        if (settleAborted) journal?.Finish(taskId, TaskStates.Aborted);
        if (changed is JsonElement notification) Publish([notification]);
        return snapshot;
    }

    internal void RetireClient(JsonRpcClient? expected)
    {
        List<JsonElement> changed;
        List<string> historyAborts = [];
        ProductImportHistoryJournal? journal;
        lock (_gate)
        {
            if (expected is not null && !ReferenceEquals(expected, _client)) return;
            journal = _history;
            changed = AbortCurrentLocked(
                "数据执行通道已中断；业务提交结果待核实，请核对数据后重新预览。",
                historyAborts);
            if (_client is not null)
            {
                if (_clientTerminatedHandler is not null) _client.Terminated -= _clientTerminatedHandler;
                if (_reportHandler is not null) _client.NotificationReceived -= _reportHandler;
            }
            _client = null;
            _generation++;
        }
        // The worker transport died mid-session; the Go authority is still
        // alive, so the durable entries are settled as aborted. A commit that
        // raced this call still wins inside the Go transaction.
        foreach (string taskId in historyAborts) journal?.Finish(taskId, TaskStates.Aborted);
        Publish(changed);
    }


    private List<JsonElement> AbortCurrentLocked(
        string reason,
        List<string>? historyAborts = null)
    {
        List<JsonElement> changed = [];
        foreach (Record record in _tasks.Values)
        {
            if (!ReferenceEquals(record.Client, _client)
                || record.Generation != _generation || Terminal(record.Status.State))
                continue;
            record.Status = record.Status with { State = TaskStates.Aborted, Error = reason };
            changed.Add(EventLocked(record));
            if (historyAborts is not null && record.HistoryStarted)
                historyAborts.Add(record.Status.TaskId);
        }
        return changed;
    }

    private JsonElement EventLocked(Record record) => EventLocked(record.Status);

    private JsonElement EventLocked(TaskStatus status)
    {
        TaskProgress progress = status.Progress;
        double ratio = progress.Total > 0
            ? Math.Min(1.0, (double)progress.Done / progress.Total) : 0;
        string state = status.State switch
        {
            TaskStates.Queued => "pending",
            TaskStates.Aborted or TaskStates.Failed => "failed",
            _ => status.State,
        };
        return JsonSerializer.SerializeToElement(new
        {
            contractVersion = "2.0",
            topic = "task.changed",
            eventId = "evt_task_" + Guid.NewGuid().ToString("N")[..24],
            sequence = ++_sequence,
            occurredAt = DateTimeOffset.UtcNow.ToString("O"),
            taskId = status.TaskId,
            taskType = status.Kind is "data.import" or "data.sourceImport" ? "import" : "export",
            state,
            progress = ratio,
            cursor = (string?)null,
            error = status.Error is null ? null : new
            {
                contractVersion = "2.0",
                code = "task.failed",
                path = (string?)null,
                message = status.Error,
                details = new { },
                retryable = false,
            },
        }, Wire);
    }

    private static JsonElement Snapshot(Record record)
        => JsonSerializer.SerializeToElement(record.Status, Wire);

    private static bool Terminal(string state) => state is
        TaskStates.Succeeded or TaskStates.Failed or TaskStates.Cancelled or TaskStates.Aborted;

    private void PruneLocked()
    {
        if (_tasks.Count <= MaxTasks) return;
        foreach (string id in _tasks.Where(pair => Terminal(pair.Value.Status.State))
            .OrderBy(pair => pair.Value.Order).Select(pair => pair.Key).ToArray())
        {
            if (_tasks.Count <= MaxTasks) break;
            _tasks.Remove(id);
        }
    }

    private void Publish(IEnumerable<JsonElement> notifications)
    {
        foreach (JsonElement notification in notifications)
        {
            if (TaskChanged is not { } observers) continue;
            foreach (Action<JsonElement> observer in observers.GetInvocationList())
            {
                try { observer(notification); }
                catch (Exception error)
                {
                    System.Diagnostics.Trace.TraceError(
                        $"Data IO task observer failed: {error}");
                }
            }
        }
    }

    public void Dispose()
    {
        List<JsonElement> changed;
        ProductImportHistoryJournal? journal;
        lock (_gate)
        {
            if (_disposed) return;
            // Workspace close deliberately persists no new finishes: the
            // Sidecar is stopping with the runtime, and the durable entries
            // correctly stay interrupted (pending verification) for reopen.
            changed = AbortCurrentLocked(
                "工作区已关闭；业务提交结果待核实，请核对数据后重新预览。");
            _disposed = true;
            if (_client is not null)
            {
                if (_clientTerminatedHandler is not null) _client.Terminated -= _clientTerminatedHandler;
                if (_reportHandler is not null) _client.NotificationReceived -= _reportHandler;
            }
            _client = null;
            _generation++;
            journal = _history;
            _history = null;
        }
        Publish(changed);
        journal?.Dispose();
        DisposeSourceImports();
    }

    private sealed class Record(
        JsonRpcClient client, long generation, ProductSidecarIdentity identity,
        TaskStatus status)
    {
        private static long _nextOrder;
        internal JsonRpcClient Client { get; } = client;
        internal long Generation { get; } = generation;
        internal ProductSidecarIdentity Identity { get; } = identity;
        internal TaskStatus Status { get; set; } = status;
        internal bool HistoryStarted { get; set; }
        internal long Order { get; } = Interlocked.Increment(ref _nextOrder);
    }
}
