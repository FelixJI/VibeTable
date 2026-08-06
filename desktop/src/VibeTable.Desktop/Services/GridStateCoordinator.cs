using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// B3 Task 4: coordinates debounced query/state requests against the backend.
/// </summary>
/// <remarks>
/// <para>
/// <b>Debounce.</b> Search/filter requests are debounced 250 ms and state saves
/// 500 ms so a user typing or dragging columns does not flood the backend.
/// Superseded reads are cancelled.
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
/// <b>Flush.</b> Confirmed state is flushed on table switch and clean shutdown;
/// shutdown does not block longer than 2 seconds.
/// </para>
/// </remarks>
public sealed class GridStateCoordinator
{
    /// <summary>Search/filter debounce window (ms).</summary>
    public const int QueryDebounceMs = 250;

    /// <summary>State-save debounce window (ms).</summary>
    public const int SaveDebounceMs = 500;

    /// <summary>Maximum shutdown flush wait (ms).</summary>
    public const int ShutdownFlushTimeoutMs = 2000;

    private readonly ITableRpcGateway _gateway;
    private readonly Action<TableNotification> _notify;

    private int _generation;
    private CancellationTokenSource? _queryCts;
    private Timer? _queryDebounce;
    private Timer? _saveDebounce;
    private TaskCompletionSource<bool>? _pendingSave;

    private QuerySnapshot? _activeSnapshot;
    private string? _databaseId;
    private string? _currentTable;
    private GridState? _confirmedState;
    private string? _confirmedRevision;
    private int _lastDataRevision;

    /// <summary>
    /// Raised when a selection snapshot is produced (after loaded row keys are
    /// reconciled) or invalidated (null payload).
    /// </summary>
    public event Action<SelectionSnapshot?>? SelectionSnapshotChanged;

    public GridStateCoordinator(
        ITableRpcGateway gateway,
        Action<TableNotification> notify)
    {
        _gateway = gateway ?? throw new ArgumentNullException(nameof(gateway));
        _notify = notify ?? throw new ArgumentNullException(nameof(notify));
    }

    /// <summary>The currently active query snapshot, or null before the first
    /// query read completes.</summary>
    public QuerySnapshot? ActiveSnapshot => _activeSnapshot;

    /// <summary>The last confirmed data revision (0 before any read).</summary>
    public int LastDataRevision => _lastDataRevision;

    /// <summary>
    /// Sets the active database identity so grid-state get/save can be scoped.
    /// </summary>
    public void SetDatabase(string databaseId)
    {
        _databaseId = databaseId;
    }

    /// <summary>
    /// Requests a debounced query read. The read is cancelled if a newer query
    /// arrives within <see cref="QueryDebounceMs"/>. Stale responses (older
    /// generation or snapshot) are dropped.
    /// </summary>
    public void RequestQuery(string table, TableQuery query)
    {
        if (string.IsNullOrEmpty(table))
        {
            return;
        }
        _currentTable = table;
        int generation = Interlocked.Increment(ref _generation);
        // Cancel any in-flight query read.
        CancelQuery();
        _queryCts = new CancellationTokenSource();
        var token = _queryCts.Token;

        // Debounce: coalesce rapid search/filter changes into one read.
        _queryDebounce?.Dispose();
        var state = (table, query, generation, token);
        _queryDebounce = new Timer(
            _ => _ = ExecuteQueryAsync(state.table, state.query, state.generation, state.token),
            null,
            QueryDebounceMs,
            Timeout.Infinite);
    }

    /// <summary>
    /// Requests a debounced grid-state save. Superseded saves within
    /// <see cref="SaveDebounceMs"/> coalesce into one. Confirmed state is
    /// flushed on table switch / shutdown via <see cref="FlushAsync"/>.
    /// </summary>
    public void RequestSave(GridState state)
    {
        if (_databaseId is null || _currentTable is null)
        {
            return;
        }
        var pending = _pendingSave;
        if (pending is not null && !pending.Task.IsCompleted)
        {
            // A previous save is still debouncing; replace it.
            pending.TrySetResult(false);
        }
        _pendingSave = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        _confirmedState = state;
        var snapshot = (_databaseId, _currentTable, state, _confirmedRevision);
        _saveDebounce?.Dispose();
        _saveDebounce = new Timer(
            _ => _ = ExecuteSaveAsync(snapshot.Item1, snapshot.Item2, snapshot.Item3, snapshot.Item4),
            null,
            SaveDebounceMs,
            Timeout.Infinite);
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
    /// Flushes any pending debounced save. Called on table switch and shutdown.
    /// Waits at most <see cref="ShutdownFlushTimeoutMs"/> so shutdown is not
    /// blocked indefinitely.
    /// </summary>
    public async Task FlushAsync()
    {
        if (_pendingSave is not null && !_pendingSave.Task.IsCompleted)
        {
            // Trigger the debounced save immediately.
            _saveDebounce?.Dispose();
            _saveDebounce = null;
            if (_databaseId is not null && _currentTable is not null && _confirmedState is not null)
            {
                _ = ExecuteSaveAsync(_databaseId, _currentTable, _confirmedState, _confirmedRevision);
            }
            try
            {
                await Task.WhenAny(_pendingSave.Task, Task.Delay(ShutdownFlushTimeoutMs))
                    .ConfigureAwait(false);
            }
            catch
            {
                // Best-effort flush; never block shutdown beyond the timeout.
            }
        }
    }

    /// <summary>
    /// Switches table: flushes pending state, cancels in-flight queries, and
    /// invalidates the selection snapshot.
    /// </summary>
    public async Task SwitchTableAsync(string table)
    {
        await FlushAsync().ConfigureAwait(false);
        CancelQuery();
        _currentTable = table;
        _activeSnapshot = null;
        _lastDataRevision = 0;
        SelectionSnapshotChanged?.Invoke(null);
    }

    // -------------------------------------------------------------------
    // Private execution
    // -------------------------------------------------------------------

    private async Task ExecuteQueryAsync(
        string table, TableQuery query, int generation, CancellationToken token)
    {
        try
        {
            int pageLimit = Math.Min(
                Math.Max(query.Limit, 1), TableWorkspaceLimits.MaxPageLimit);
            var firstPage = await _gateway.QueryTableViewAsync(
                table, query.Offset, pageLimit, query, token).ConfigureAwait(true);
            if (IsStale(generation) || token.IsCancellationRequested)
            {
                return;
            }

            int expectedRows = firstPage.FilteredRows ?? firstPage.TotalRows;
            int loadedRows = firstPage.Rows.Count;
            int offset = query.Offset + loadedRows;
            var rows = new List<Dictionary<string, object?>>(firstPage.Rows);
            while (loadedRows < expectedRows)
            {
                if (IsStale(generation) || token.IsCancellationRequested)
                {
                    return;
                }

                int limit = Math.Min(pageLimit, expectedRows - loadedRows);
                var nextPage = await _gateway.QueryTableViewAsync(
                    table, offset, limit, query, token).ConfigureAwait(true);
                if (!HaveSameQueryRevision(firstPage, nextPage))
                {
                    throw new InvalidOperationException(
                        "The table changed while loading the complete query result. Refresh and try again.");
                }
                if (nextPage.Rows.Count == 0)
                {
                    throw new InvalidOperationException(
                        "The query ended before its advertised filtered row count was loaded.");
                }
                rows.AddRange(nextPage.Rows);
                loadedRows += nextPage.Rows.Count;
                offset += nextPage.Rows.Count;
            }

            if (IsStale(generation) || token.IsCancellationRequested)
            {
                return;
            }
            var completePage = firstPage with
            {
                Rows = rows,
                Offset = query.Offset,
                Limit = loadedRows,
                Mode = "client",
            };
            _notify(new TableNotification
            {
                // A debounced query is one complete authoritative dataset.
                // Partial pages stay inside the host and never leak stale rows
                // into projections such as calendar, kanban, and timeline.
                Type = "table.datasetReady",
                Page = completePage,
                LoadedRows = loadedRows,
            });
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested)
        {
            // Superseded by a newer query: drop quietly.
        }
        catch (Exception ex)
        {
            if (IsStale(generation))
            {
                return;
            }
            _notify(new TableNotification
            {
                Type = "operation.failed",
                LoadedRows = 0,
                MutationResult = new MutationOutcome(
                    "query", false,
                    new MutationError(MutationErrorKind.Unknown, ex.Message, null, null, null),
                    null),
            });
        }
    }

    private static bool HaveSameQueryRevision(TablePage first, TablePage next)
    {
        if (first.Revision is not null || next.Revision is not null)
        {
            return first.Revision is not null && first.Revision == next.Revision;
        }
        if (first.QuerySnapshot is not null || next.QuerySnapshot is not null)
        {
            return first.QuerySnapshot is not null
                && next.QuerySnapshot is not null
                && first.QuerySnapshot.DatabaseId == next.QuerySnapshot.DatabaseId
                && first.QuerySnapshot.Table == next.QuerySnapshot.Table
                && first.QuerySnapshot.SchemaRevision == next.QuerySnapshot.SchemaRevision
                && first.QuerySnapshot.DataRevision == next.QuerySnapshot.DataRevision
                && first.QuerySnapshot.Digest == next.QuerySnapshot.Digest;
        }
        return true;
    }

    private async Task ExecuteSaveAsync(
        string databaseId, string table, GridState state, string? revision)
    {
        try
        {
            var result = await _gateway.SaveGridStateAsync(
                databaseId, table, state, revision, CancellationToken.None)
                .ConfigureAwait(false);
            if (result.Conflict)
            {
                // Stale revision: adopt the server's current state/revision.
                _confirmedState = result.State;
                _confirmedRevision = result.Revision;
            }
            else
            {
                _confirmedState = result.State;
                _confirmedRevision = result.Revision;
            }
        }
        catch
        {
            // Best-effort save; the local state is not authoritative.
        }
        finally
        {
            _pendingSave?.TrySetResult(true);
        }
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

    /// <summary>
    /// Loads the saved grid state for ``(databaseId, table)`` on table select.
    /// Returns null when no database is set.
    /// </summary>
    public async Task<GridStateResult?> LoadStateAsync(string table)
    {
        if (_databaseId is null)
        {
            return null;
        }
        try
        {
            var result = await _gateway.GetGridStateAsync(
                _databaseId, table, CancellationToken.None).ConfigureAwait(false);
            _confirmedState = result.State;
            _confirmedRevision = result.Revision;
            return result;
        }
        catch
        {
            return null;
        }
    }
}
