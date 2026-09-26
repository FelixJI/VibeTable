using System;
using System.Collections.Generic;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>
/// B3 Task 4: coordinates debounced query/state requests against the backend.
/// </summary>
/// <remarks>
/// <para>
/// <b>Debounce.</b> Search/filter requests are debounced 250 ms so a user
/// typing does not flood the backend. Superseded reads are cancelled.
/// </para>
/// <para>
/// <b>Stale suppression.</b> Responses are ignored when the request generation
/// or query snapshot has advanced past the one the response belongs to.
/// </para>
/// <para>
/// <b>Selection snapshot.</b> A <see cref="SelectionSnapshot"/> is produced only
/// after loaded row keys are reconciled to the current query snapshot; it is
/// invalidated on query, schema or data revision changes.
/// </para>
/// <para>
/// Persisted grid state is no longer owned by this coordinator: the public
/// <c>gridState.get</c>/<c>gridState.save</c> requests are served by
/// <see cref="GridPresentationRequestController"/> over the Host-owned
/// <see cref="HostGridStateStore"/>.
/// </para>
/// </remarks>
public sealed class GridStateCoordinator
{
    /// <summary>Search/filter debounce window (ms).</summary>
    public const int QueryDebounceMs = 250;

    /// <summary>
    /// Bounded recovery window for notify-path reads hit by transient
    /// transport failures (for example a Sidecar generation rebinding).
    /// Mirrors the workspace selection recovery timeout.
    /// </summary>
    private static readonly TimeSpan NotifyRecoveryWindow = TimeSpan.FromSeconds(3);

    /// <summary>Retry delay between notify-path recovery attempts.</summary>
    private static readonly TimeSpan NotifyRecoveryRetryDelay = TimeSpan.FromMilliseconds(250);

    private static bool IsTransientTransportFailure(Exception exception)
        => exception is BackendUnavailableException or ObjectDisposedException;

    private readonly ITableRpcGateway _gateway;
    private readonly Action<TableNotification> _notify;
    private readonly TimeProvider _timeProvider;

    private int _generation;
    private CancellationTokenSource? _queryCts;
    private ITimer? _queryDebounce;

    private QuerySnapshot? _activeSnapshot;
    private int _lastDataRevision;
    private bool _cursorFetchInFlight;

    /// <summary>
    /// Raised when a selection snapshot is produced (after loaded row keys are
    /// reconciled) or invalidated (null payload).
    /// </summary>
    public event Action<SelectionSnapshot?>? SelectionSnapshotChanged;

    public GridStateCoordinator(
        ITableRpcGateway gateway,
        Action<TableNotification> notify,
        TimeProvider? timeProvider = null)
    {
        _gateway = gateway ?? throw new ArgumentNullException(nameof(gateway));
        _notify = notify ?? throw new ArgumentNullException(nameof(notify));
        _timeProvider = timeProvider ?? TimeProvider.System;
    }

    /// <summary>The currently active query snapshot, or null before the first
    /// query read completes.</summary>
    public QuerySnapshot? ActiveSnapshot => _activeSnapshot;

    /// <summary>The last confirmed data revision (0 before any read).</summary>
    public int LastDataRevision => _lastDataRevision;

    /// <summary>
    /// Requests a renderer-authored canonical query. WPF treats the JSON as an
    /// opaque contract and leaves all AST validation to the data service.
    /// </summary>
    public void RequestQuery(string table, JsonElement query)
    {
        if (string.IsNullOrEmpty(table) || query.ValueKind != JsonValueKind.Object)
        {
            return;
        }
        ScheduleQuery(table, query, null, CancellationToken.None);
    }

    /// <summary>
    /// Completes one correlated query without broadcasting its result.
    /// Notify-path callers set <paramref name="notifyRecovery"/> to run the
    /// same bounded transient recovery as direct notifications; terminal
    /// failures still fault the returned task instead of broadcasting, so the
    /// caller keeps ownership of the reply.
    /// </summary>
    public Task<TablePage> RequestQueryAsync(
        string table, JsonElement query, CancellationToken cancellationToken,
        bool notifyRecovery = false)
    {
        ArgumentException.ThrowIfNullOrEmpty(table);
        if (query.ValueKind != JsonValueKind.Object)
            throw new ArgumentException("Expected a canonical query object.", nameof(query));
        var completion = new TaskCompletionSource<TablePage>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        CancellationToken token = ScheduleQuery(
            table, query, completion, cancellationToken, notifyRecovery);
        // A superseded debounce may never run; its caller must still complete.
        return completion.Task.WaitAsync(token);
    }

    private CancellationToken ScheduleQuery(
        string table, JsonElement query, TaskCompletionSource<TablePage>? completion,
        CancellationToken cancellationToken, bool notifyRecovery = false)
    {
        int generation = Interlocked.Increment(ref _generation);
        CancelQuery();
        _queryCts = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        var token = _queryCts.Token;
        _queryDebounce?.Dispose();
        JsonElement stableQuery = query.Clone();
        var state = (table, stableQuery, generation, token, completion, notifyRecovery);
        _queryDebounce = _timeProvider.CreateTimer(
            _ => _ = ExecuteRawQueryAsync(
                state.table, state.stableQuery, state.generation, state.token,
                state.completion, state.notifyRecovery),
            null,
            TimeSpan.FromMilliseconds(QueryDebounceMs),
            Timeout.InfiniteTimeSpan);
        return token;
    }

    /// <summary>
    /// Completes one cursor read without broadcasting its result.
    /// Notify-path callers set <paramref name="notifyRecovery"/> to run the
    /// same bounded transient recovery as direct notifications; terminal
    /// failures still fault the returned task instead of broadcasting.
    /// </summary>
    public async Task<TablePage?> RequestNextWindowAsync(
        string cursor, CancellationToken cancellationToken, bool notifyRecovery = false)
    {
        if (string.IsNullOrWhiteSpace(cursor) || _cursorFetchInFlight || _queryCts is null)
            return null;
        _cursorFetchInFlight = true;
        using var lifetime = CancellationTokenSource.CreateLinkedTokenSource(
            _queryCts.Token, cancellationToken);
        var completion = new TaskCompletionSource<TablePage>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        await FetchNextWindowAsync(
            cursor, _generation, lifetime.Token, completion, notifyRecovery)
            .ConfigureAwait(false);
        return await completion.Task.ConfigureAwait(false);
    }

    public void RequestNextWindow(string cursor)
    {
        if (string.IsNullOrWhiteSpace(cursor) || _cursorFetchInFlight || _queryCts is null)
        {
            return;
        }
        _cursorFetchInFlight = true;
        _ = FetchNextWindowAsync(cursor, _generation, _queryCts.Token);
    }

    /// <summary>
    /// Invalidates the active selection snapshot (e.g. after a query, schema or
    /// data revision change). Emits a null selection to subscribers.
    /// </summary>
    public void InvalidateSelection()
    {
        _activeSnapshot = null;
        SelectionSnapshotChanged?.Invoke(null);
    }

    /// <summary>
    /// Produces a <see cref="SelectionSnapshot"/> from the loaded row keys once
    /// they are reconciled to the current query snapshot. Called by the
    /// workspace service after a page load completes.
    /// </summary>
    public void ReconcileSelection(QuerySnapshot snapshot, IReadOnlyList<object> rowKeys)
    {
        if (snapshot is null)
        {
            InvalidateSelection();
            return;
        }
        _activeSnapshot = snapshot;
        _lastDataRevision = snapshot.DataRevision;
        var sel = new SelectionSnapshot(snapshot, snapshot.DataRevision, rowKeys);
        SelectionSnapshotChanged?.Invoke(sel);
    }

    /// <summary>
    /// Cancels any in-flight query and invalidates the selection snapshot.
    /// Called by the workspace service when the active table changes.
    /// </summary>
    public void ResetForTableChange()
    {
        CancelQuery();
        _activeSnapshot = null;
        _lastDataRevision = 0;
        SelectionSnapshotChanged?.Invoke(null);
    }

    // -------------------------------------------------------------------
    // Private execution
    // -------------------------------------------------------------------

    private async Task ExecuteRawQueryAsync(
        string table, JsonElement query, int generation, CancellationToken token,
        TaskCompletionSource<TablePage>? completion, bool notifyRecovery = false)
    {
        TablePage? page;
        if (completion is null)
        {
            // Notify-path loads own the renderer UI and have no correlated
            // caller to reject; a transient transport failure (for example a
            // Sidecar generation rebinding) must recover within the bounded
            // window instead of flashing operation.failed at the user.
            page = await TryFetchNotifyPageAsync(table, query, generation, token)
                .ConfigureAwait(true);
            if (page is null || IsStale(generation) || token.IsCancellationRequested)
            {
                return;
            }
        }
        else
        {
            try
            {
                // Awaiting notify reads keep the recovery window, but their
                // terminal failures still fault the completion so the caller
                // that owns the reply surfaces them exactly once.
                page = notifyRecovery
                    ? await ReadWithNotifyRecoveryAsync(
                        readToken => FetchQueryPageAsync(table, query, readToken),
                        generation, token).ConfigureAwait(true)
                    : await FetchQueryPageAsync(table, query, token).ConfigureAwait(true);
            }
            catch (OperationCanceledException) when (token.IsCancellationRequested)
            {
                completion.TrySetCanceled(token);
                return;
            }
            catch (Exception ex)
            {
                if (IsStale(generation) || token.IsCancellationRequested)
                {
                    completion.TrySetCanceled(token);
                    return;
                }
                completion.TrySetException(ex);
                return;
            }
            if (IsStale(generation) || token.IsCancellationRequested)
            {
                completion.TrySetCanceled(token);
                return;
            }
        }
        if (completion is not null)
        {
            completion.TrySetResult(page);
        }
        else
        {
            _notify(new TableNotification
            {
                Type = "table.datasetReady",
                Page = page,
            });
        }
    }

    /// <summary>
    /// Runs one read with the shared bounded notify recovery window, retrying
    /// transient transport failures. A terminal failure or cancellation is
    /// rethrown; the caller decides whether and how to surface it.
    /// </summary>
    private async Task<TablePage> ReadWithNotifyRecoveryAsync(
        Func<CancellationToken, Task<TablePage>> read,
        int generation,
        CancellationToken token)
    {
        DateTimeOffset deadline = _timeProvider.GetUtcNow() + NotifyRecoveryWindow;
        while (true)
        {
            try
            {
                return await read(token).ConfigureAwait(true);
            }
            catch (Exception ex) when (IsRecoverableNotifyFailure(
                ex, generation, token, deadline))
            {
                await Task.Delay(NotifyRecoveryRetryDelay, _timeProvider, token)
                    .ConfigureAwait(true);
            }
        }
    }

    private bool IsRecoverableNotifyFailure(
        Exception exception, int generation, CancellationToken token,
        DateTimeOffset deadline)
        => IsTransientTransportFailure(exception)
            && !IsStale(generation)
            && !token.IsCancellationRequested
            && _timeProvider.GetUtcNow() + NotifyRecoveryRetryDelay <= deadline;

    /// <summary>
    /// Runs the notify-path query with a bounded transient recovery window.
    /// Returns the loaded page, or null when the load was superseded or has
    /// already surfaced as operation.failed.
    /// </summary>
    private async Task<TablePage?> TryFetchNotifyPageAsync(
        string table, JsonElement query, int generation, CancellationToken token)
    {
        try
        {
            return await ReadWithNotifyRecoveryAsync(
                readToken => FetchQueryPageAsync(table, query, readToken),
                generation, token).ConfigureAwait(true);
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested)
        {
            return null;
        }
        catch (Exception ex)
        {
            if (IsStale(generation) || token.IsCancellationRequested)
            {
                return null;
            }
            _notify(new TableNotification
            {
                Type = "operation.failed",
                MutationResult = new MutationOutcome(
                    "query", false, MutationErrorMapper.Map(ex), null),
            });
            return null;
        }
    }

    private async Task<TablePage> FetchQueryPageAsync(
        string table, JsonElement query, CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        Task<TablePage> windowTask = _gateway.OpenTableCursorRawAsync(table, query, token);
        if (!HasViewAggregates(query))
        {
            return await windowTask.ConfigureAwait(true);
        }
        Task<TablePage> groupsTask = _gateway.QueryTableViewRawAsync(table, query, token);
        await Task.WhenAll(windowTask, groupsTask).ConfigureAwait(true);
        TablePage window = await windowTask.ConfigureAwait(true);
        TablePage groups = await groupsTask.ConfigureAwait(true);
        if (!HaveSameQueryRevision(window, groups))
        {
            throw new InvalidOperationException(
                "The table changed while loading the cursor and group summary.");
        }
        return window with
        {
            GroupRows = groups.GroupRows,
            GroupOffset = groups.GroupOffset,
            GroupLimit = groups.GroupLimit,
            HasMoreGroups = groups.HasMoreGroups,
        };
    }

    private async Task FetchNextWindowAsync(
        string cursor, int generation, CancellationToken token,
        TaskCompletionSource<TablePage>? completion = null,
        bool notifyRecovery = false)
    {
        try
        {
            token.ThrowIfCancellationRequested();
            TablePage? page;
            if (completion is null)
                page = await TryFetchNotifyWindowAsync(cursor, generation, token)
                    .ConfigureAwait(true);
            else if (notifyRecovery)
                page = await ReadWithNotifyRecoveryAsync(
                    readToken => _gateway.FetchTableCursorAsync(cursor, readToken),
                    generation, token).ConfigureAwait(true);
            else
                page = await _gateway.FetchTableCursorAsync(cursor, token)
                    .ConfigureAwait(true);
            if (page is null || IsStale(generation) || token.IsCancellationRequested)
            {
                completion?.TrySetCanceled(token);
                return;
            }
            if (completion is not null)
                completion.TrySetResult(page);
            else
                _notify(new TableNotification
                {
                    Type = "table.windowLoaded",
                    Page = page,
                });
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested)
        {
            completion?.TrySetCanceled(token);
        }
        catch (Exception ex)
        {
            if (IsStale(generation) || token.IsCancellationRequested)
            {
                completion?.TrySetCanceled(token);
                return;
            }
            if (completion is not null)
                completion.TrySetException(ex);
            else
                _notify(new TableNotification
                {
                    Type = "operation.failed",
                    MutationResult = new MutationOutcome(
                        "query.cursor", false, MutationErrorMapper.Map(ex), null),
                });
        }
        finally
        {
            _cursorFetchInFlight = false;
        }
    }

    /// <summary>
    /// Runs the notify-path cursor window fetch with the same bounded transient
    /// recovery window as <see cref="TryFetchNotifyPageAsync"/>. Returns the
    /// loaded page, or null when the fetch was superseded or has already
    /// surfaced as operation.failed.
    /// </summary>
    private async Task<TablePage?> TryFetchNotifyWindowAsync(
        string cursor, int generation, CancellationToken token)
    {
        try
        {
            return await ReadWithNotifyRecoveryAsync(
                readToken => _gateway.FetchTableCursorAsync(cursor, readToken),
                generation, token).ConfigureAwait(true);
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested)
        {
            return null;
        }
        catch (Exception ex)
        {
            if (IsStale(generation) || token.IsCancellationRequested)
            {
                return null;
            }
            _notify(new TableNotification
            {
                Type = "operation.failed",
                MutationResult = new MutationOutcome(
                    "query.cursor", false, MutationErrorMapper.Map(ex), null),
            });
            return null;
        }
    }

    private static bool HasViewAggregates(JsonElement query)
        => HasNonEmptyArray(query, "groups")
            || HasNonEmptyArray(query, "summaries")
            || (query.TryGetProperty("groupOffset", out JsonElement offset)
                && offset.ValueKind == JsonValueKind.Number
                && offset.TryGetInt32(out int value)
                && value > 0);

    private static bool HasNonEmptyArray(JsonElement query, string property)
        => query.TryGetProperty(property, out JsonElement value)
            && value.ValueKind == JsonValueKind.Array
            && value.GetArrayLength() > 0;

    private static bool HaveSameQueryRevision(TablePage first, TablePage next)
    {
        if (first.QuerySnapshot is not null || next.QuerySnapshot is not null)
        {
            return first.QuerySnapshot is not null
                && next.QuerySnapshot is not null
                && first.QuerySnapshot.DatabaseId == next.QuerySnapshot.DatabaseId
                && first.QuerySnapshot.Table == next.QuerySnapshot.Table
                && first.QuerySnapshot.SchemaRevision == next.QuerySnapshot.SchemaRevision
                && first.QuerySnapshot.DataRevision == next.QuerySnapshot.DataRevision;
        }

        if (first.Revision is not null || next.Revision is not null)
        {
            return first.Revision is not null
                && next.Revision is not null
                && first.Revision.DatabaseSessionId == next.Revision.DatabaseSessionId
                && first.Revision.SchemaRevision == next.Revision.SchemaRevision
                && first.Revision.DataRevision == next.Revision.DataRevision;
        }

        return false;
    }

    private bool IsStale(int generation)
        => Volatile.Read(ref _generation) != generation;

    private void CancelQuery()
    {
        var existing = Interlocked.Exchange(ref _queryCts, null);
        if (existing is not null)
        {
            try { existing.Cancel(); } catch { /* best-effort */ }
            existing.Dispose();
        }
    }
}
