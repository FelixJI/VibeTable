using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Binding between an isolated-surface task and its authority context.
/// Late confirmations from another binding cannot revive a retired task.
/// </summary>
internal sealed record HostPluginTaskBinding(
    PluginProjectContext Context,
    ProductAuthoritySnapshot Authority);

internal enum HostPluginTaskCancelOutcome
{
    NotFound,
    Terminal,
    Active,
}

/// <summary>Host-owned isolated-surface tasks, confirmations and terminal history.</summary>
internal sealed class HostPluginTaskRegistry
{
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
    private PluginProjectContext? _workspaceContext;

    public HostPluginTaskRegistry(ProductAuthorityEpoch authority)
    {
        _authority = authority ?? throw new ArgumentNullException(nameof(authority));
    }

    public void SetWorkspaceContext(PluginProjectContext? context)
    {
        lock (_gate) _workspaceContext = context;
    }

    internal HostPluginTaskBinding? CaptureNative()
    {
        ProductAuthoritySnapshot authority = _authority.Snapshot();
        return authority.Context is null ? null : new(authority.Context, authority);
    }

    internal (CancellationToken Token, PluginEventEnvelope Envelope) NativeTask(HostPluginTaskBinding binding, PluginRuntimeTaskSnapshot snapshot, bool nativeOnlyApproval = false)
    {
        lock (_gate)
        {
            if (_tasks.Values.Count(record => !IsTerminalLocked(record)) >= 64)
                throw new PluginSurfacePolicyException("plugin_concurrency_limit", "Too many active plugin tasks.");
            if (!AdmitTask(binding, snapshot)) throw new InvalidOperationException("Plugin session is stale.");
            var record = _tasks[snapshot.TaskId];
            record.NativeOnlyApproval = nativeOnlyApproval;
            return (record.Lifetime.Token, ProjectLocked(record, "plugin.task.changed"));
        }
    }

    internal PluginEventEnvelope? FinishNative(string taskId, string state, PluginRuntimeResult? result = null, PluginRuntimeSafeError? error = null)
    {
        lock (_gate)
        {
            if (!_tasks.TryGetValue(taskId, out var record) || IsTerminalLocked(record)) return null;
            record.Snapshot = record.Snapshot with { State = state, Result = result, Error = error };
            record.Revision++;
            CancelLifetimeLocked(record);
            ClearPendingLocked(record.Snapshot.RunId);
            return IsCurrentLocked(record.Binding) && IsVisibleInCurrentContextLocked(record)
                ? ProjectLocked(record, "plugin.task.changed") : null;
        }
    }

    internal (PluginEventEnvelope Envelope, Task<bool> Decision) NativeConfirmation(string taskId, JsonElement plan, int count,
        string? title = null, IReadOnlyList<IReadOnlyDictionary<string, JsonElement>>? summary = null, bool exact = false)
    {
        lock (_gate)
        {
            var record = _tasks[taskId];
            if (!IsCurrentLocked(record.Binding) || record.Snapshot.CancelRequested || IsTerminalLocked(record))
                throw new OperationCanceledException();
            string interactionId = Guid.NewGuid().ToString("N");
            double expires = Now() + 120;
            // Any delete — whatever the surface presentation or the action's
            // declared risk — is native-only: only the exact native-host
            // decision can consume the pending confirmation, never the renderer.
            bool hasDelete = false;
            string confirmationRisk = record.Snapshot.Risk!;
            foreach (var operation in plan.GetProperty("operations").EnumerateArray())
            {
                if (operation.TryGetProperty("kind", out var kind) && kind.GetString() == "delete")
                { confirmationRisk = "destructive"; hasDelete = true; break; }
            }
            var pending = new HostPluginInteractionRecord(record.Binding, interactionId, expires) { NativeDecision = new(TaskCreationOptions.RunContinuationsAsynchronously), NativeOnlyApproval = record.NativeOnlyApproval || hasDelete };
            _interactions[record.Snapshot.RunId] = pending;
            var preview = new PluginRuntimeConfirmationPreview(
                summary ?? [new Dictionary<string, JsonElement> { ["collection"] = plan.GetProperty("collection").Clone() }],
                plan.GetProperty("operations").EnumerateArray().Take(exact ? count : 10)
                    .Select(operation => (IReadOnlyDictionary<string, JsonElement>)operation.EnumerateObject().ToDictionary(property => property.Name, property => property.Value.Clone())).ToArray(), count, []);
            var confirmation = new PluginRuntimePendingConfirmation(interactionId, confirmationRisk, title ?? "批准插件保存当前精确计划", preview, expires);
            var interaction = new PluginRuntimeInteractionSnapshot(record.Snapshot.RunId, record.Snapshot.ProjectKey, record.Snapshot.PluginId, record.Snapshot.ActionId, "desktop-host", null, confirmation, false);
            record.Revision++;
            return (new(PluginContractVersions.Event, "plugin.interaction.requested", record.Snapshot.ProjectKey, record.Snapshot.RunId, record.Revision,
                JsonSerializer.SerializeToElement(interaction, PluginTaskJson.Options)), pending.NativeDecision.Task);
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

    /// <summary>
    /// Copies the existing bounded history for a renderer that has re-established
    /// this workspace session. These are task receipts, never pending decisions
    /// or authority to reopen the original surface.
    /// </summary>
    internal IReadOnlyList<PluginEventEnvelope> ProjectHistory(PluginProjectContext context)
    {
        lock (_gate)
        {
            if (_workspaceContext is null
                || context.ProjectKey != _workspaceContext.ProjectKey
                || context.SessionGeneration != _workspaceContext.SessionGeneration) return [];
            return _tasks.Values.Where(IsVisibleInCurrentContextLocked)
                .OrderBy(record => record.Order)
                .Select(record => ProjectLocked(record, "plugin.task.changed")).ToArray();
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
            // Late cancellation cannot overwrite an already recorded terminal receipt.
            record.Snapshot = record.Snapshot with { CancelRequested = true };
            record.Revision += 1;
            CancelLifetimeLocked(record);
            ClearPendingLocked(record.Snapshot.RunId);
            snapshot = record.Snapshot;
            return HostPluginTaskCancelOutcome.Active;
        }
    }

    internal bool RequiresNativeConfirmation(string runId)
    {
        lock (_gate) return _interactions.TryGetValue(runId, out var pending) && pending.NativeOnlyApproval;
    }

    internal bool IsInteractionPending(PluginResolveInteractionParams request)
    {
        lock (_gate)
        {
            return _interactions.TryGetValue(request.RunId, out var pending)
                && pending.InteractionId == request.InteractionId
                && pending.Decision is null && pending.ExpiresAt > Now()
                && _tasksByRun.TryGetValue(request.RunId, out var record)
                && ReferenceEquals(record.Binding, pending.Binding)
                && IsVisibleInCurrentContextLocked(record) && IsCurrentLocked(pending.Binding)
                && !IsTerminalLocked(record) && !record.Snapshot.CancelRequested;
        }
    }

    /// <summary>Atomically consumes a decision for the current Host-owned confirmation.</summary>
    public PluginRuntimeInteractionResolveResult BeginInteractionResolve(
        PluginResolveInteractionParams request, bool fromNativeHost = false)
    {
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
            if (pending.NativeOnlyApproval && !fromNativeHost) return new("native-required", null);
            if (pending.Decision is not null)
                return new("already-resolved", pending.Decision);
            if (pending.ExpiresAt <= Now()) return new("expired", null);
            if (request.Decision is not ("approved" or "rejected"))
                return new("expired", null);
            pending.Decision = request.Decision;
            pending.NativeDecision?.TrySetResult(request.Decision == "approved");
            return new("resolved", request.Decision);
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
    }

    internal void RetireContext()
    {
        lock (_gate)
        {
            foreach (var record in _tasks.Values.Where(record => !IsTerminalLocked(record)))
            {
                // The native owner settles success/unknown; retirement only requests cancellation.
                CancelLifetimeLocked(record);
                ClearPendingLocked(record.Snapshot.RunId);
            }
        }
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

    private bool IsCurrentLocked(HostPluginTaskBinding binding)
        => _authority.IsCurrent(binding.Authority);

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
        public bool NativeOnlyApproval { get; set; }
        public CancellationTokenSource Lifetime { get; } = new();
    }

    private static long _orderCounter;

    private sealed class HostPluginInteractionRecord(
        HostPluginTaskBinding binding, string interactionId, double expiresAt)
    {
        public TaskCompletionSource<bool>? NativeDecision { get; init; }
        public bool NativeOnlyApproval { get; init; }
        public HostPluginTaskBinding Binding { get; } = binding;
        public string InteractionId { get; } = interactionId;
        public double ExpiresAt { get; } = expiresAt;
        public string? Decision { get; set; }
    }

}
