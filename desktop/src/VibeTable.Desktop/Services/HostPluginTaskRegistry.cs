using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using System.Threading;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Binding between a plugin task and the gateway/context generation that
/// started it. Late events, confirmations and file selections captured from
/// another binding are rejected instead of being replayed.
/// </summary>
internal sealed record HostPluginTaskBinding(
    IPluginRpcGateway Gateway,
    long GatewayGeneration,
    PluginProjectContext Context,
    ProductAuthoritySnapshot Authority);

internal enum HostPluginTaskCancelOutcome
{
    NotFound,
    Terminal,
    Active,
}

internal sealed record HostPluginTaskSettlement(
    PluginEventEnvelope Envelope,
    PluginRuntimeTaskSnapshot Snapshot);

/// <summary>
/// The single owner of public plugin action task identity, snapshots,
/// progress/terminal states and confirmation/file pending records. The
/// registry lives for the workspace shell lifetime, so a lost or replaced
/// Python client settles every still-running task to an explicit
/// <c>aborted</c> terminal state with an unknown business-commit boundary.
/// Python keeps only its execution context, cancel handles and reply futures;
/// it is never queried for public task state.
/// </summary>
internal sealed class HostPluginTaskRegistry
{
    private const string AbortedErrorCode = "plugin_task_aborted";
    private const int MaxTrackedTasks = 256;

    private static readonly string[] TerminalStates =
        ["succeeded", "failed", "cancelled", "aborted"];

    private readonly ProductAuthorityEpoch _authority;
    private readonly object _gate = new();
    private readonly Dictionary<string, HostPluginTaskRecord> _tasks =
        new(StringComparer.Ordinal);
    private readonly Dictionary<string, HostPluginTaskRecord> _tasksByRun =
        new(StringComparer.Ordinal);
    private readonly Dictionary<string, HostPluginInteractionRecord> _interactions =
        new(StringComparer.Ordinal);
    private readonly Dictionary<string, HostPluginFileRecord> _fileRequests =
        new(StringComparer.Ordinal);
    private IPluginRpcGateway? _gateway;
    private PluginProjectContext? _context;
    private PluginProjectContext? _workspaceContext;
    private long _gatewayGeneration;

    public HostPluginTaskRegistry(ProductAuthorityEpoch authority)
    {
        _authority = authority ?? throw new ArgumentNullException(nameof(authority));
    }

    public IReadOnlyList<HostPluginTaskSettlement> SetGateway(
        IPluginRpcGateway gateway,
        PluginProjectContext? context)
    {
        ArgumentNullException.ThrowIfNull(gateway);
        lock (_gate)
        {
            IReadOnlyList<HostPluginTaskSettlement> settled =
                SettleAllLocked("backend-client-replaced");
            _gateway = gateway;
            _context = context;
            _gatewayGeneration += 1;
            return settled;
        }
    }

    public IReadOnlyList<HostPluginTaskSettlement> ClearGateway(
        IPluginRpcGateway expected)
    {
        ArgumentNullException.ThrowIfNull(expected);
        lock (_gate)
        {
            if (!ReferenceEquals(_gateway, expected)) return [];
            IReadOnlyList<HostPluginTaskSettlement> settled =
                SettleAllLocked("backend-client-unavailable");
            _gateway = null;
            _gatewayGeneration += 1;
            return settled;
        }
    }

    public IReadOnlyList<HostPluginTaskSettlement> SetContext(
        PluginProjectContext? context)
    {
        lock (_gate)
        {
            IReadOnlyList<HostPluginTaskSettlement> settled =
                SettleAllLocked("project-context-changed");
            _context = context;
            return settled;
        }
    }

    /// <summary>Settles tasks of one gateway whose transport has terminated.</summary>
    public IReadOnlyList<HostPluginTaskSettlement> SettleGateway(
        IPluginRpcGateway gateway)
    {
        ArgumentNullException.ThrowIfNull(gateway);
        lock (_gate)
        {
            return SettleLocked(
                record => ReferenceEquals(record.Binding.Gateway, gateway),
                "backend-client-lost");
        }
    }

    /// <summary>
    /// Updates the workspace-open identity used for history visibility. This
    /// is deliberately separate from the admission authority: losing the
    /// Python client or rebuilding its gateway must settle running tasks but
    /// must keep their terminal states queryable for the still-open
    /// workspace, while a real workspace switch hides foreign tasks.
    /// </summary>
    public void SetWorkspaceContext(PluginProjectContext? context)
    {
        lock (_gate) _workspaceContext = context;
    }

    public HostPluginTaskBinding? Capture()
    {
        lock (_gate)
        {
            ProductAuthoritySnapshot snapshot = _authority.Snapshot();
            return _gateway is null
                || _context is null
                || snapshot.Context != _context
                ? null
                : new HostPluginTaskBinding(
                    _gateway,
                    _gatewayGeneration,
                    _context,
                    snapshot);
        }
    }

    public bool AdmitTask(
        HostPluginTaskBinding binding,
        PluginRuntimeTaskSnapshot snapshot)
    {
        ArgumentNullException.ThrowIfNull(binding);
        ArgumentNullException.ThrowIfNull(snapshot);
        lock (_gate)
        {
            if (!IsCurrentLocked(binding)) return false;
            var record = new HostPluginTaskRecord(binding, snapshot, revision: 1);
            _tasks.Add(snapshot.TaskId, record);
            _tasksByRun.Add(snapshot.RunId, record);
            PruneTerminalHistoryLocked();
            return true;
        }
    }

    /// <summary>
    /// Applies the executor's initial report for a host-generated identity.
    /// The identity must echo the admitted task exactly. Execution reports can
    /// legitimately arrive before the start response; in that case the
    /// advanced state/result is preserved and never downgraded back to the
    /// queued report.
    /// </summary>
    public bool TryApplyStartResult(
        IPluginRpcGateway gateway,
        PluginRuntimeTaskSnapshot reported)
    {
        ArgumentNullException.ThrowIfNull(gateway);
        ArgumentNullException.ThrowIfNull(reported);
        lock (_gate)
        {
            if (!ReferenceEquals(_gateway, gateway)) return false;
            if (!_tasks.TryGetValue(reported.TaskId, out HostPluginTaskRecord? record)
                || !ReferenceEquals(record.Binding.Gateway, gateway)
                || !HasSameIdentityLocked(record, reported))
            {
                return false;
            }
            if (record.ExecutionReportSeen || IsTerminalLocked(record))
            {
                // A faster execution report already advanced (or finished)
                // this task; the initial queued report cannot downgrade it.
                return true;
            }
            bool stickyCancel = record.Snapshot.CancelRequested;
            record.Snapshot = reported with { CancelRequested = stickyCancel || reported.CancelRequested };
            return true;
        }
    }

    /// <summary>Settles one admitted task to a failed terminal state.</summary>
    public HostPluginTaskSettlement? FailTask(
        string taskId,
        string code,
        string message)
    {
        lock (_gate)
        {
            if (!_tasks.TryGetValue(taskId, out HostPluginTaskRecord? record)
                || IsTerminalLocked(record))
            {
                return null;
            }
            PluginRuntimeSafeError error = new(
                "vibetable.plugin-error.v1",
                code,
                message,
                "reconfigure",
                record.Snapshot.PluginId,
                record.Snapshot.ActionId,
                record.Snapshot.RunId,
                new Dictionary<string, JsonElement>(),
                null);
            record.Snapshot = record.Snapshot with { State = "failed", Error = error };
            record.Revision += 1;
            _interactions.Remove(record.Snapshot.RunId, out _);
            return new HostPluginTaskSettlement(
                ProjectLocked(record, "plugin.task.changed"),
                record.Snapshot);
        }
    }

    /// <summary>
    /// Applies one execution report from the gateway that produced it. Reports
    /// for unknown tasks, foreign identities or already-terminal tasks are
    /// rejected; the first terminal state stays authoritative.
    /// </summary>
    public PluginEventEnvelope? ApplyExecutionReport(
        IPluginRpcGateway gateway,
        PluginEventEnvelope envelope)
    {
        ArgumentNullException.ThrowIfNull(gateway);
        ArgumentNullException.ThrowIfNull(envelope);
        lock (_gate)
        {
            if (!ReferenceEquals(_gateway, gateway)) return null;
            if (!_tasks.TryGetValue(envelope.EntityId, out HostPluginTaskRecord? record)
                || !ReferenceEquals(record.Binding.Gateway, gateway))
            {
                return null;
            }
            PluginRuntimeTaskSnapshot? reported;
            try
            {
                reported = envelope.Snapshot.Deserialize<PluginRuntimeTaskSnapshot>(
                    PluginTaskJson.Options);
            }
            catch (JsonException)
            {
                return null;
            }
            if (reported is null
                || !HasSameIdentityLocked(record, reported)
                || IsTerminalLocked(record))
            {
                return null;
            }
            bool stickyCancel = record.Snapshot.CancelRequested;
            record.Snapshot = reported with { CancelRequested = stickyCancel || reported.CancelRequested };
            record.ExecutionReportSeen = true;
            record.Revision += 1;
            if (IsTerminalLocked(record))
            {
                _interactions.Remove(record.Snapshot.RunId, out _);
            }
            return ProjectLocked(record, envelope.EventType);
        }
    }

    public bool TryGetTask(string taskId, out PluginRuntimeTaskSnapshot snapshot)
    {
        lock (_gate)
        {
            if (_tasks.TryGetValue(taskId, out HostPluginTaskRecord? record)
                && IsVisibleInCurrentContextLocked(record))
            {
                snapshot = record.Snapshot;
                return true;
            }
            snapshot = null!;
            return false;
        }
    }

    public HostPluginTaskCancelOutcome RequestCancel(
        string taskId,
        out PluginRuntimeTaskSnapshot snapshot,
        out HostPluginTaskBinding binding)
    {
        lock (_gate)
        {
            if (!_tasks.TryGetValue(taskId, out HostPluginTaskRecord? record)
                || !IsVisibleInCurrentContextLocked(record))
            {
                snapshot = null!;
                binding = null!;
                return HostPluginTaskCancelOutcome.NotFound;
            }
            snapshot = record.Snapshot;
            binding = record.Binding;
            if (IsTerminalLocked(record)) return HostPluginTaskCancelOutcome.Terminal;
            // The public cancel-request flag is owned here; the gateway call is
            // only the execution-side handle trigger and cannot overwrite a
            // terminal state that has already been recorded.
            record.Snapshot = record.Snapshot with { CancelRequested = true };
            record.Revision += 1;
            snapshot = record.Snapshot;
            return HostPluginTaskCancelOutcome.Active;
        }
    }

    /// <summary>Records a pending confirmation owned by this workspace shell.</summary>
    public bool RecordInteraction(
        IPluginRpcGateway gateway,
        PluginEventEnvelope envelope)
    {
        ArgumentNullException.ThrowIfNull(gateway);
        ArgumentNullException.ThrowIfNull(envelope);
        lock (_gate)
        {
            if (!ReferenceEquals(_gateway, gateway)) return false;
            if (!_tasksByRun.TryGetValue(envelope.EntityId, out HostPluginTaskRecord? record)
                || !ReferenceEquals(record.Binding.Gateway, gateway)
                || IsTerminalLocked(record))
            {
                return false;
            }
            _interactions[record.Snapshot.RunId] =
                new HostPluginInteractionRecord(record.Snapshot.RunId, gateway);
            return true;
        }
    }

    /// <summary>
    /// Admits a resolution only while its run is still pending on the current
    /// gateway generation; late confirmations from retired generations are
    /// rejected without reaching the executor.
    /// </summary>
    public IPluginRpcGateway? TryBeginInteractionResolve(string runId)
    {
        lock (_gate)
        {
            if (!_interactions.TryGetValue(runId, out HostPluginInteractionRecord? pending)
                || !ReferenceEquals(pending.Gateway, _gateway))
            {
                return null;
            }
            if (!_tasksByRun.TryGetValue(runId, out HostPluginTaskRecord? record)
                || !ReferenceEquals(record.Binding.Gateway, _gateway)
                || !IsVisibleInCurrentContextLocked(record)
                || IsTerminalLocked(record))
            {
                return null;
            }
            return pending.Gateway;
        }
    }

    public bool RecordFileRequest(
        IPluginRpcGateway gateway,
        string requestId,
        string runId)
    {
        lock (_gate)
        {
            if (!ReferenceEquals(_gateway, gateway)) return false;
            if (!_tasksByRun.TryGetValue(runId, out HostPluginTaskRecord? record)
                || !ReferenceEquals(record.Binding.Gateway, gateway)
                || IsTerminalLocked(record))
            {
                return false;
            }
            _fileRequests[requestId] = new HostPluginFileRecord(requestId, runId, gateway);
            return true;
        }
    }

    /// <summary>
    /// Consumes one native picker result only while its request is still
    /// pending on the current gateway generation; a picker that returns after
    /// the executor generation died is dropped without issuing a grant.
    /// </summary>
    public IPluginRpcGateway? TryBeginFileResolve(string requestId)
    {
        lock (_gate)
        {
            if (!_fileRequests.TryGetValue(requestId, out HostPluginFileRecord? pending)
                || !ReferenceEquals(pending.Gateway, _gateway))
            {
                return null;
            }
            if (!_tasksByRun.TryGetValue(pending.RunId, out HostPluginTaskRecord? record)
                || !ReferenceEquals(record.Binding.Gateway, _gateway)
                || IsTerminalLocked(record))
            {
                return null;
            }
            return pending.Gateway;
        }
    }

    public void ForgetFileRequest(string requestId)
    {
        lock (_gate) _fileRequests.Remove(requestId, out _);
    }

    private IReadOnlyList<HostPluginTaskSettlement> SettleAllLocked(string reason)
        => SettleLocked(_ => true, reason);

    private IReadOnlyList<HostPluginTaskSettlement> SettleLocked(
        Func<HostPluginTaskRecord, bool> filter,
        string reason)
    {
        List<HostPluginTaskSettlement> settled = [];
        foreach (HostPluginTaskRecord record in _tasks.Values.Where(filter).ToList())
        {
            if (IsTerminalLocked(record)) continue;
            PluginRuntimeSafeError error = new(
                "vibetable.plugin-error.v1",
                AbortedErrorCode,
                $"插件执行通道已失效（{reason}），任务已中止；业务提交结果未知，请核对数据后手动重试。",
                "retry",
                record.Snapshot.PluginId,
                record.Snapshot.ActionId,
                record.Snapshot.RunId,
                new Dictionary<string, JsonElement>
                {
                    ["reason"] = JsonSerializer.SerializeToElement(reason),
                    ["commitOutcome"] = JsonSerializer.SerializeToElement("unknown"),
                },
                null);
            record.Snapshot = record.Snapshot with { State = "aborted", Error = error };
            record.Revision += 1;
            _interactions.Remove(record.Snapshot.RunId, out _);
            settled.Add(new HostPluginTaskSettlement(
                ProjectLocked(record, "plugin.task.changed"),
                record.Snapshot));
        }
        return settled;
    }

    private PluginEventEnvelope ProjectLocked(
        HostPluginTaskRecord record,
        string eventType) => new(
        PluginContractVersions.Event,
        eventType,
        record.Snapshot.ProjectKey,
        record.Snapshot.TaskId,
        record.Revision,
        JsonSerializer.SerializeToElement(record.Snapshot, PluginTaskJson.Options));

    private bool IsCurrentLocked(HostPluginTaskBinding binding) =>
        ReferenceEquals(_gateway, binding.Gateway)
        && _gatewayGeneration == binding.GatewayGeneration
        && _context == binding.Context
        && _authority.IsCurrent(binding.Authority);

    private static bool IsTerminalLocked(HostPluginTaskRecord record)
        => TerminalStates.Contains(record.Snapshot.State);

    /// <summary>
    /// Keeps the registry a bounded workspace history: the oldest terminal
    /// records are pruned first, while active tasks and recent terminal
    /// states (needed to answer queries after a client loss) are retained.
    /// </summary>
    private void PruneTerminalHistoryLocked()
    {
        if (_tasks.Count <= MaxTrackedTasks) return;
        foreach (HostPluginTaskRecord candidate in _tasks.Values
            .Where(record => IsTerminalLocked(record))
            .OrderBy(record => record.Order)
            .ToList())
        {
            if (_tasks.Count <= MaxTrackedTasks) break;
            _tasks.Remove(candidate.Snapshot.TaskId);
            _tasksByRun.Remove(candidate.Snapshot.RunId);
            _interactions.Remove(candidate.Snapshot.RunId);
        }
    }

    /// <summary>
    /// Tasks are only publicly visible inside the workspace session that
    /// admitted them; foreign-session queries fail closed as unknown.
    /// </summary>
    private bool IsVisibleInCurrentContextLocked(HostPluginTaskRecord record) =>
        _workspaceContext is not null
        && record.Binding.Context.ProjectKey == _workspaceContext.ProjectKey
        && record.Binding.Context.SessionGeneration == _workspaceContext.SessionGeneration;

    private static bool HasSameIdentityLocked(
        HostPluginTaskRecord record,
        PluginRuntimeTaskSnapshot reported) =>
        record.Snapshot.TaskId == reported.TaskId
        && record.Snapshot.RunId == reported.RunId
        && record.Snapshot.PluginId == reported.PluginId
        && record.Snapshot.ActionId == reported.ActionId
        && record.Snapshot.ProjectKey == reported.ProjectKey;

    internal static class PluginTaskJson
    {
        public static readonly JsonSerializerOptions Options =
            new(JsonSerializerDefaults.Web);
    }

    private sealed class HostPluginTaskRecord(
        HostPluginTaskBinding binding,
        PluginRuntimeTaskSnapshot snapshot,
        int revision)
    {
        public HostPluginTaskBinding Binding { get; } = binding;
        public PluginRuntimeTaskSnapshot Snapshot { get; set; } = snapshot;
        public int Revision { get; set; } = revision;
        public long Order { get; } = System.Threading.Interlocked.Increment(ref _orderCounter);
        public bool ExecutionReportSeen { get; set; }
    }

    private static long _orderCounter;

    private sealed record HostPluginInteractionRecord(
        string RunId,
        IPluginRpcGateway Gateway);

    private sealed record HostPluginFileRecord(
        string RequestId,
        string RunId,
        IPluginRpcGateway Gateway);
}
