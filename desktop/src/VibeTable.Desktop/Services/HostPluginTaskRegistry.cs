using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
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
            IReadOnlyList<HostPluginTaskSettlement> settled = SettleLocked(
                record => ReferenceEquals(record.Binding.Gateway, gateway),
                "backend-client-lost");
            if (ReferenceEquals(_gateway, gateway))
            {
                _gateway = null;
                _gatewayGeneration += 1;
            }
            return settled;
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
                || !IsCurrentLocked(record.Binding)
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

    /// <summary>Settles a start with an unknown commit outcome.</summary>
    public HostPluginTaskSettlement? AbortTask(string taskId)
    {
        lock (_gate)
        {
            return SettleLocked(
                record => record.Snapshot.TaskId == taskId,
                "start-response-unavailable").FirstOrDefault();
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
                || !IsCurrentLocked(record.Binding))
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
                CancelLifetimeLocked(record);
                ClearPendingLocked(record.Snapshot.RunId);
            }
            return ProjectLocked(record, envelope.EventType);
        }
    }

    public IPluginRpcGateway? GatewayForTask(string taskId)
    {
        lock (_gate)
            return _tasks.TryGetValue(taskId, out HostPluginTaskRecord? record)
                ? record.Binding.Gateway : null;
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
            CancelLifetimeLocked(record);
            ClearPendingLocked(record.Snapshot.RunId);
            snapshot = record.Snapshot;
            return HostPluginTaskCancelOutcome.Active;
        }
    }

    /// <summary>Accepts only a current run's complete pending confirmation.</summary>
    public bool RecordInteraction(IPluginRpcGateway gateway, PluginEventEnvelope envelope)
    {
        ArgumentNullException.ThrowIfNull(gateway);
        ArgumentNullException.ThrowIfNull(envelope);
        PluginRuntimeInteractionSnapshot? snapshot;
        try
        {
            snapshot = envelope.Snapshot.Deserialize<PluginRuntimeInteractionSnapshot>(
                PluginTaskJson.Options);
        }
        catch (JsonException) { return false; }
        PluginRuntimePendingConfirmation? pending = snapshot?.PendingConfirmation;
        if (snapshot is null || pending is null
            || string.IsNullOrWhiteSpace(pending.InteractionId)
            || pending.ExpiresAt <= Now()) return false;
        lock (_gate)
        {
            if (!ReferenceEquals(_gateway, gateway)
                || !_tasksByRun.TryGetValue(envelope.EntityId, out HostPluginTaskRecord? record)
                || !IsCurrentLocked(record.Binding)
                || IsTerminalLocked(record)
                || record.Snapshot.CancelRequested
                || envelope.ProjectKey != record.Snapshot.ProjectKey
                || snapshot.RunId != record.Snapshot.RunId
                || snapshot.ProjectKey != record.Snapshot.ProjectKey
                || snapshot.PluginId != record.Snapshot.PluginId
                || snapshot.ActionId != record.Snapshot.ActionId
                || snapshot.Caller != "desktop-host") return false;
            if (_interactions.TryGetValue(snapshot.RunId, out HostPluginInteractionRecord? existing)
                && (existing.Decision is null
                    || existing.InteractionId == pending.InteractionId)) return false;
            _interactions[snapshot.RunId] = new HostPluginInteractionRecord(
                record.Binding, pending.InteractionId, pending.ExpiresAt);
            return true;
        }
    }

    /// <summary>Atomically decides a current confirmation before contacting Python.</summary>
    public PluginRuntimeInteractionResolveResult BeginInteractionResolve(
        PluginResolveInteractionParams request, out IPluginRpcGateway? gateway)
    {
        gateway = null;
        lock (_gate)
        {
            if (!_interactions.TryGetValue(request.RunId, out HostPluginInteractionRecord? pending)
                || pending.InteractionId != request.InteractionId
                || !_tasksByRun.TryGetValue(request.RunId, out HostPluginTaskRecord? record)
                || !ReferenceEquals(record.Binding, pending.Binding)
                || !IsVisibleInCurrentContextLocked(record)
                || !IsCurrentLocked(pending.Binding)
                || IsTerminalLocked(record)
                || record.Snapshot.CancelRequested) return new("expired", null);
            if (pending.Decision is not null)
                return new("already-resolved", pending.Decision);
            if (pending.ExpiresAt <= Now()) return new("expired", null);
            if (request.Decision is not ("approved" or "rejected"))
                return new("expired", null);
            pending.Decision = request.Decision;
            gateway = pending.Binding.Gateway;
            return new("resolved", request.Decision);
        }
    }

    public bool RecordFileRequest(IPluginRpcGateway gateway, PluginEventEnvelope envelope,
        PluginRuntimeFileRequest request)
    {
        lock (_gate)
        {
            if (!ReferenceEquals(_gateway, gateway)
                || !_tasksByRun.TryGetValue(request.RunId, out HostPluginTaskRecord? record)
                || !IsCurrentLocked(record.Binding)
                || IsTerminalLocked(record)
                || record.Snapshot.CancelRequested
                || request.RequestId != envelope.EntityId
                || request.ProjectKey != envelope.ProjectKey
                || request.ProjectKey != record.Snapshot.ProjectKey
                || request.PluginId != record.Snapshot.PluginId
                || request.ActionId != record.Snapshot.ActionId
                || request.Direction is not ("read" or "write")
                || request.ExpiresAt <= Now()
                || _fileRequests.ContainsKey(request.RequestId)) return false;
            _fileRequests.Add(request.RequestId, new HostPluginFileRecord(
                record.Binding, request));
            return true;
        }
    }

    /// <summary>Consumes the exact live request before a path grant can be issued.</summary>
    public IPluginRpcGateway? TryBeginFileResolve(
        PluginRuntimeFileRequest request, out CancellationToken runToken)
    {
        runToken = CancellationToken.None;
        lock (_gate)
        {
            if (!_fileRequests.TryGetValue(request.RequestId, out HostPluginFileRecord? pending)
                || pending.Consumed
                || pending.Request != request
                || !_tasksByRun.TryGetValue(request.RunId, out HostPluginTaskRecord? record)
                || !ReferenceEquals(record.Binding, pending.Binding)
                || !IsCurrentLocked(pending.Binding)
                || !IsVisibleInCurrentContextLocked(record)
                || IsTerminalLocked(record)
                || record.Snapshot.CancelRequested
                || request.ExpiresAt <= Now()) return null;
            pending.Consumed = true;
            runToken = record.Lifetime.Token;
            return pending.Binding.Gateway;
        }
    }

    public void ForgetFileRequest(string requestId)
    {
        lock (_gate)
        {
            if (_fileRequests.TryGetValue(requestId, out HostPluginFileRecord? pending))
                pending.Consumed = true;
        }
    }

    private static void CancelLifetimeLocked(HostPluginTaskRecord record)
    {
        // CancelAsync marks the token cancelled immediately and runs callbacks
        // asynchronously, away from the registry gate.
        Task cancellation = record.Lifetime.CancelAsync();
        if (!cancellation.IsCompletedSuccessfully)
            _ = cancellation.ContinueWith(
                completed => System.Diagnostics.Trace.TraceError(
                    $"Plugin task cancellation callback failed: {completed.Exception}"),
                CancellationToken.None,
                TaskContinuationOptions.OnlyOnFaulted,
                TaskScheduler.Default);
    }
    private static double Now() => DateTimeOffset.UtcNow.ToUnixTimeMilliseconds() / 1000.0;

    private void ClearPendingLocked(string runId)
    {
        _interactions.Remove(runId);
        foreach (string id in _fileRequests.Where(pair => pair.Value.Request.RunId == runId)
            .Select(pair => pair.Key).ToArray())
            _fileRequests.Remove(id);
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
            CancelLifetimeLocked(record);
            ClearPendingLocked(record.Snapshot.RunId);
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
            ClearPendingLocked(candidate.Snapshot.RunId);
            candidate.Lifetime.Dispose();
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
        public CancellationTokenSource Lifetime { get; } = new();
    }

    private static long _orderCounter;

    private sealed class HostPluginInteractionRecord(
        HostPluginTaskBinding binding, string interactionId, double expiresAt)
    {
        public HostPluginTaskBinding Binding { get; } = binding;
        public string InteractionId { get; } = interactionId;
        public double ExpiresAt { get; } = expiresAt;
        public string? Decision { get; set; }
    }

    private sealed class HostPluginFileRecord(
        HostPluginTaskBinding binding, PluginRuntimeFileRequest request)
    {
        public HostPluginTaskBinding Binding { get; } = binding;
        public PluginRuntimeFileRequest Request { get; } = request;
        public bool Consumed { get; set; }
    }
}