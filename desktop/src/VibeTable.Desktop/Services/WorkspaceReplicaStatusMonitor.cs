using System.IO;
using System.Net.Http;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

public sealed record WorkspaceReplicaStatus(
    WorkspaceCoordinationStrength CoordinationStrength,
    string SyncState,
    bool PendingSync);

/// <summary>
/// Session-bounded polling for the Sidecar's durable replica projection.
/// HTTP has no server-push channel, so active/pending states are sampled
/// quickly and terminal states at a low background cadence. Rebinding or
/// disposal cancels the prior epoch and every individual request is bounded.
/// </summary>
public sealed class WorkspaceReplicaStatusMonitor : IAsyncDisposable
{
    internal static readonly TimeSpan DefaultActiveInterval =
        TimeSpan.FromSeconds(2);
    internal static readonly TimeSpan DefaultIdleInterval =
        TimeSpan.FromSeconds(15);
    internal static readonly TimeSpan DefaultRequestTimeout =
        TimeSpan.FromSeconds(10);

    private readonly object _gate = new();
    private readonly SemaphoreSlim _refreshGate = new(1, 1);
    private readonly Func<
        Guid,
        ulong,
        CancellationToken,
        Task<WorkspaceReplicaStatus>> _refresh;
    private readonly TimeSpan _activeInterval;
    private readonly TimeSpan _idleInterval;
    private readonly TimeSpan _requestTimeout;
    private readonly List<Task> _retiredBindings = [];
    private CancellationTokenSource? _binding;
    private Task _loop = Task.CompletedTask;
    private Guid? _workspaceId;
    private ulong _sessionEpoch;
    private bool _disposed;

    public WorkspaceReplicaStatusMonitor(
        Func<
            Guid,
            ulong,
            CancellationToken,
            Task<WorkspaceReplicaStatus>> refresh,
        TimeSpan? activeInterval = null,
        TimeSpan? idleInterval = null,
        TimeSpan? requestTimeout = null)
    {
        _refresh = refresh ?? throw new ArgumentNullException(nameof(refresh));
        _activeInterval = activeInterval ?? DefaultActiveInterval;
        _idleInterval = idleInterval ?? DefaultIdleInterval;
        _requestTimeout = requestTimeout ?? DefaultRequestTimeout;
        if (_activeInterval <= TimeSpan.Zero ||
            _idleInterval <= TimeSpan.Zero ||
            _requestTimeout <= TimeSpan.Zero)
            throw new ArgumentOutOfRangeException(
                nameof(activeInterval),
                "Replica polling intervals and request timeout must be positive.");
    }

    public void Bind(Guid workspaceId, ulong sessionEpoch, bool enabled)
    {
        CancellationTokenSource? previous;
        Task previousLoop;
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            if (enabled &&
                workspaceId != Guid.Empty &&
                sessionEpoch > 0 &&
                _workspaceId == workspaceId &&
                _sessionEpoch == sessionEpoch &&
                _binding is not null)
                return;
            previous = _binding;
            previousLoop = _loop;
            if (previous is not null)
                _retiredBindings.Add(
                    RetireBindingAsync(previousLoop, previous));
            _binding = null;
            _workspaceId = null;
            _sessionEpoch = 0;
            if (enabled)
            {
                if (workspaceId == Guid.Empty || sessionEpoch == 0)
                    throw new ArgumentOutOfRangeException(nameof(workspaceId));
                _workspaceId = workspaceId;
                _sessionEpoch = sessionEpoch;
                _binding = new CancellationTokenSource();
                _loop = RunAsync(
                    workspaceId,
                    sessionEpoch,
                    _binding.Token);
            }
            else
            {
                _loop = Task.CompletedTask;
            }
        }
        previous?.Cancel();
    }

    public async Task RefreshNowAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken)
    {
        lock (_gate)
        {
            if (_disposed ||
                _binding is null ||
                _workspaceId != workspaceId ||
                _sessionEpoch != sessionEpoch)
                return;
        }
        try
        {
            _ = await RefreshBoundedAsync(
                    workspaceId,
                    sessionEpoch,
                    cancellationToken)
                .ConfigureAwait(false);
        }
        catch (OperationCanceledException) when (
            !cancellationToken.IsCancellationRequested)
        {
            // A status refresh timeout must not fail the completed user action.
        }
        catch (Exception exception) when (
            exception is IOException
                or HttpRequestException
                or JsonException
                or InvalidOperationException
                or WorkspaceRegistryException)
        {
            // The durable projection remains authoritative and the session
            // loop will retry. Never turn a successful sync/takeover RPC into
            // a renderer-visible failure because the follow-up read failed.
        }
    }

    internal static WorkspaceReplicaStatus Parse(JsonElement result)
    {
        if (result.ValueKind != JsonValueKind.Object)
            throw InvalidStatus();
        string[] actual = result.EnumerateObject()
            .Select(property => property.Name)
            .Order(StringComparer.Ordinal)
            .ToArray();
        string[] expected =
            ["coordinationStrength", "pendingSync", "syncState"];
        if (!actual.SequenceEqual(expected, StringComparer.Ordinal))
            throw InvalidStatus();
        string coordination = RequiredString(
            result,
            "coordinationStrength");
        WorkspaceCoordinationStrength strength = coordination switch
        {
            "strong" => WorkspaceCoordinationStrength.Strong,
            "advisory" => WorkspaceCoordinationStrength.Advisory,
            _ => throw InvalidStatus(),
        };
        string syncState = RequiredString(result, "syncState");
        if (syncState is not (
                "localOnly" or "pending" or "syncing" or
                "replicated" or "failed"))
            throw InvalidStatus();
        if (!result.TryGetProperty(
                "pendingSync",
                out JsonElement pending) ||
            pending.ValueKind is not (
                JsonValueKind.True or JsonValueKind.False))
            throw InvalidStatus();
        bool pendingSync = pending.GetBoolean();
        if ((syncState == "replicated" && pendingSync) ||
            (syncState is "pending" or "syncing" or "failed" &&
             !pendingSync))
            throw InvalidStatus();
        return new WorkspaceReplicaStatus(
            strength,
            syncState,
            pendingSync);
    }

    internal static WorkspaceHealthObservation ProjectHealth(
        WorkspaceRegistryEntryV2 current,
        WorkspaceReplicaStatus status,
        DateTimeOffset observedAt)
    {
        ArgumentNullException.ThrowIfNull(current);
        ArgumentNullException.ThrowIfNull(status);
        WorkspaceHealth health = current.LastKnownHealth ==
            WorkspaceHealth.Corrupt
                ? WorkspaceHealth.Corrupt
                : status.SyncState switch
                {
                    "replicated" or "syncing" =>
                        WorkspaceHealth.Healthy,
                    "pending" or "failed" =>
                        WorkspaceHealth.Degraded,
                    "localOnly" => current.LastKnownHealth,
                    _ => throw InvalidStatus(),
                };
        DateTimeOffset? lastSync =
            status.SyncState == "replicated" &&
            (current.PendingSync || current.LastSyncAt is null)
                ? observedAt
                : null;
        return new WorkspaceHealthObservation(
            health,
            status.PendingSync,
            LastSyncAt: lastSync);
    }

    private async Task RunAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken)
    {
        while (!cancellationToken.IsCancellationRequested)
        {
            WorkspaceReplicaStatus? status = null;
            try
            {
                status = await RefreshBoundedAsync(
                        workspaceId,
                        sessionEpoch,
                        cancellationToken)
                    .ConfigureAwait(false);
            }
            catch (OperationCanceledException) when (
                cancellationToken.IsCancellationRequested)
            {
                return;
            }
            catch (Exception exception) when (
                exception is IOException
                    or HttpRequestException
                    or JsonException
                    or InvalidOperationException
                    or WorkspaceRegistryException)
            {
                // Runtime startup/reconnect can race an observation. Retry at
                // the low cadence without escaping a background exception.
            }
            TimeSpan delay = status?.SyncState is "pending" or "syncing"
                ? _activeInterval
                : _idleInterval;
            try
            {
                await Task.Delay(delay, cancellationToken)
                    .ConfigureAwait(false);
            }
            catch (OperationCanceledException)
            {
                return;
            }
        }
    }

    private async Task<WorkspaceReplicaStatus> RefreshBoundedAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken)
    {
        await _refreshGate.WaitAsync(cancellationToken)
            .ConfigureAwait(false);
        try
        {
            using var request =
                CancellationTokenSource.CreateLinkedTokenSource(
                    cancellationToken);
            request.CancelAfter(_requestTimeout);
            return await _refresh(
                    workspaceId,
                    sessionEpoch,
                    request.Token)
                .ConfigureAwait(false);
        }
        finally
        {
            _refreshGate.Release();
        }
    }

    public async ValueTask DisposeAsync()
    {
        CancellationTokenSource? binding;
        Task[] bindings;
        lock (_gate)
        {
            if (_disposed)
                return;
            _disposed = true;
            binding = _binding;
            _binding = null;
            _workspaceId = null;
            _sessionEpoch = 0;
            if (binding is not null)
                _retiredBindings.Add(
                    RetireBindingAsync(_loop, binding));
            bindings = _retiredBindings.ToArray();
        }
        binding?.Cancel();
        try
        {
            await Task.WhenAll(bindings).ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
        }
        _refreshGate.Dispose();
    }

    private static async Task RetireBindingAsync(
        Task loop,
        CancellationTokenSource binding)
    {
        try
        {
            await loop.ConfigureAwait(false);
        }
        finally
        {
            binding.Dispose();
        }
    }

    private static string RequiredString(JsonElement root, string name)
        => root.TryGetProperty(name, out JsonElement value) &&
           value.ValueKind == JsonValueKind.String &&
           !string.IsNullOrWhiteSpace(value.GetString())
            ? value.GetString()!
            : throw InvalidStatus();

    private static InvalidOperationException InvalidStatus()
        => new("Sidecar replica.status returned an invalid projection.");
}
