using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;
using TaskStatus = VibeTable.Contracts.TaskStatus;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Workspace-runtime owner of public Data IO task identity, history and events.
/// Worker clients only execute and report; replacing one never loses a terminal receipt.
/// </summary>
internal sealed class HostDataIoTaskRegistry : IDisposable
{
    private const int MaxTasks = 256;
    private readonly object _gate = new();
    private readonly Dictionary<string, Record> _tasks = new(StringComparer.Ordinal);
    private JsonRpcClient? _client;
    private Action? _clientTerminatedHandler;
    private ProductSidecarIdentity? _identity;
    private long _generation;
    private long _sequence;
    private bool _disposed;
    private static readonly JsonSerializerOptions Wire = new(JsonSerializerDefaults.Web);

    internal event Action<JsonElement>? TaskChanged;

    internal void BindClient(JsonRpcClient? client, ProductSidecarIdentity identity)
    {
        List<JsonElement> changed;
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            if (ReferenceEquals(client, _client) && identity == _identity) return;
            changed = AbortCurrentLocked("数据执行通道已更换；业务提交结果待核实，请核对数据后重新预览。");
            if (_client is not null && _clientTerminatedHandler is not null)
                _client.Terminated -= _clientTerminatedHandler;
            _client = client;
            _identity = identity;
            _generation++;
            _clientTerminatedHandler = client is null ? null : () => RetireClient(client);
            if (client is not null) client.Terminated += _clientTerminatedHandler;
        }
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
            return _tasks.TryGetValue(taskId, out Record? record)
                ? Snapshot(record)
                : throw new KeyNotFoundException("Data IO task is not in this workspace.");
    }

    internal (JsonElement Snapshot, JsonRpcClient? Client) RequestCancel(string taskId)
    {
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
        lock (_gate)
        {
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
        }
        if (changed is JsonElement notification) Publish([notification]);
    }

    internal JsonElement AbortTask(string taskId, string reason)
    {
        JsonElement? changed = null;
        JsonElement snapshot;
        lock (_gate)
        {
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
            }
            snapshot = Snapshot(record);
        }
        if (changed is JsonElement notification) Publish([notification]);
        return snapshot;
    }

    internal void RetireClient(JsonRpcClient? expected)
    {
        List<JsonElement> changed;
        lock (_gate)
        {
            if (expected is not null && !ReferenceEquals(expected, _client)) return;
            changed = AbortCurrentLocked(
                "数据执行通道已中断；业务提交结果待核实，请核对数据后重新预览。");
            if (_client is not null && _clientTerminatedHandler is not null)
                _client.Terminated -= _clientTerminatedHandler;
            _client = null;
            _generation++;
        }
        Publish(changed);
    }


    private List<JsonElement> AbortCurrentLocked(string reason)
    {
        List<JsonElement> changed = [];
        foreach (Record record in _tasks.Values)
        {
            if (!ReferenceEquals(record.Client, _client)
                || record.Generation != _generation || Terminal(record.Status.State))
                continue;
            record.Status = record.Status with { State = TaskStates.Aborted, Error = reason };
            changed.Add(EventLocked(record));
        }
        return changed;
    }

    private JsonElement EventLocked(Record record)
    {
        TaskStatus status = record.Status;
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
            taskType = status.Kind == "data.import" ? "import" : "export",
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
        lock (_gate)
        {
            if (_disposed) return;
            changed = AbortCurrentLocked(
                "工作区已关闭；业务提交结果待核实，请核对数据后重新预览。");
            _disposed = true;
            if (_client is not null && _clientTerminatedHandler is not null)
                _client.Terminated -= _clientTerminatedHandler;
            _client = null;
            _generation++;
        }
        Publish(changed);
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
        internal long Order { get; } = Interlocked.Increment(ref _nextOrder);
    }
}
