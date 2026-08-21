using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

internal sealed class TableSelectionRecoveryExhaustedException(
    string message,
    Exception innerException) : Exception(message, innerException);

/// <summary>
/// Host paging limits for product collection views.
/// </summary>
public static class TableWorkspaceLimits
{
    /// <summary>
    /// Hard cap on a single product collection page. The workspace service
    /// opens cursor windows of this size and never exceeds it.
    /// </summary>
    public const int MaxPageLimit = 500;
}

/// <summary>
/// Orchestrates collection discovery, paging, and host notifications over the
/// product workspace gateway.
/// </summary>
/// <remarks>
/// <para>
/// <b>Ownership.</b> The service owns:
/// </para>
/// <list type="bullet">
/// <item>The current logical source identifier.</item>
/// <item>The current table name (set ONLY via <see cref="SelectTableAsync"/>,
/// and only if the name was advertised by source discovery).</item>
/// <item>Request generation: one revision-bound 500-row cursor window.</item>
/// <item>A per-selection <see cref="CancellationTokenSource"/> that cancels
/// immediately on table/database switch, suppressing stale pages.</item>
/// </list>
/// <para>
/// <b>DTO forwarding.</b> The service forwards the RPC DTOs to the WebView
/// unchanged. It does NOT format cells, build SQL, or otherwise transform the
/// data — that all lives in the backend / web layer.
/// </para>
/// <para>
/// <b>Stale suppression.</b> Each <see cref="SelectTableAsync"/> call bumps an
/// internal <c>generation</c> counter and links a fresh
/// <see cref="CancellationToken"/>. The cursor open checks BOTH the
/// token AND the generation before emitting a page, so a late-arriving page
/// from a superseded table (e.g. token cancelled but the RPC already returned)
/// is silently dropped.
/// </para>
/// </remarks>
public sealed class TableWorkspaceService
{
    private const int ProductDataRpcErrorCode = -32150;
    private static readonly TimeSpan DefaultSelectionRecoveryTimeout =
        TimeSpan.FromSeconds(3);
    private static readonly TimeSpan SelectionRecoveryPollInterval =
        TimeSpan.FromMilliseconds(25);

    private readonly ITableRpcGateway _gateway;
    private readonly TimeSpan _selectionRecoveryTimeout;
    private readonly TimeProvider _timeProvider;
    private readonly object _databaseGate = new();

    /// <summary>
    /// The RPC gateway, exposed so the B2 paste dispatcher handlers can call
    /// <c>table.previewPaste</c>/<c>table.applyPaste</c> directly (paste owns
    /// its own request/notification lifecycle, separate from the mutation
    /// notification stream).
    /// </summary>
    public ITableRpcGateway Gateway => _gateway;

    private string? _currentDatabase;
    private string? _currentTable;
    private IReadOnlyList<string> _knownTables = Array.Empty<string>();
    private IReadOnlyList<string> _knownViews = Array.Empty<string>();
    private long _databaseGeneration;

    private int _generation;
    private CancellationTokenSource? _selectCts;

    /// <summary>Creates the table-selection module.</summary>
    /// <param name="gateway">The product-table transport adapter.</param>
    /// <param name="selectionRecoveryTimeout">
    /// The absolute recovery window after the first cursor-open failure that is
    /// classified as transient. It is not a total selection timeout: the initial
    /// RPC remains governed by the transport lifecycle and does not consume this
    /// window before a classified transient failure is observed.
    /// </param>
    /// <param name="timeProvider">
    /// The clock used for recovery delays and the absolute recovery deadline.
    /// </param>
    public TableWorkspaceService(
        ITableRpcGateway gateway,
        TimeSpan? selectionRecoveryTimeout = null,
        TimeProvider? timeProvider = null)
    {
        _gateway = gateway ?? throw new ArgumentNullException(nameof(gateway));
        _selectionRecoveryTimeout = selectionRecoveryTimeout
            ?? DefaultSelectionRecoveryTimeout;
        if (_selectionRecoveryTimeout <= TimeSpan.Zero)
            throw new ArgumentOutOfRangeException(nameof(selectionRecoveryTimeout));
        _timeProvider = timeProvider ?? TimeProvider.System;
    }

    /// <summary>
    /// Raised whenever the workspace emits a Phase-A host notification
    /// (<c>table.pageLoaded</c>, <c>table.datasetReady</c>). MainWindow forwards
    /// these to the WebView.
    /// </summary>
    public event Action<TableNotification>? Notification;

    /// <summary>The logical source identifier currently open.</summary>
    public string? CurrentDatabase
    {
        get { lock (_databaseGate) return _currentDatabase; }
    }

    /// <summary>
    /// Opens the configured source identified by <paramref name="path"/> and
    /// caches the advertised collection names so <see cref="SelectTableAsync"/>
    /// can enforce the "known name" invariant.
    /// </summary>
    public async Task<DatabaseOpenResult> OpenDatabaseAsync(string path)
    {
        DatabaseOpenResult result = await PrepareDatabaseOpenAsync(
            path, CancellationToken.None).ConfigureAwait(true);
        using DatabaseOpenAdmission admission = BeginDatabaseOpenAdmission(path, result);
        admission.Complete();
        return result;
    }

    internal async Task<DatabaseOpenResult> PrepareDatabaseOpenAsync(
        string path,
        CancellationToken token)
    {
        if (path is null)
        {
            throw new ArgumentNullException(nameof(path));
        }
        if (string.IsNullOrWhiteSpace(path))
        {
            throw new ArgumentException(
                "Source identifier must be a non-empty string.", nameof(path));
        }

        // Cancel any in-flight table fetch before switching databases.
        CancelSelection();

        var result = await _gateway.OpenDatabaseAsync(path, token)
            .ConfigureAwait(true);
        token.ThrowIfCancellationRequested();
        return result;
    }

    internal DatabaseOpenAdmission BeginDatabaseOpenAdmission(
        string path,
        DatabaseOpenResult result)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(path);
        ArgumentNullException.ThrowIfNull(result);
        IReadOnlyList<string> tables = FilterUserTables(result.Tables);
        IReadOnlyList<string> views = result.Views
            ?? throw new InvalidOperationException("Database views are unavailable.");
        lock (_databaseGate)
        {
            var previous = new DatabaseState(
                _currentDatabase,
                _knownTables,
                _knownViews);
            _databaseGeneration += 1;
            _currentDatabase = path;
            Volatile.Write(ref _knownTables, tables);
            Volatile.Write(ref _knownViews, views);
            return new DatabaseOpenAdmission(this, _databaseGeneration, previous);
        }
    }

    private void RollbackDatabaseOpen(long generation, DatabaseState previous)
    {
        lock (_databaseGate)
        {
            if (_databaseGeneration != generation) return;
            _databaseGeneration += 1;
            _currentDatabase = previous.Database;
            Volatile.Write(ref _knownTables, previous.Tables);
            Volatile.Write(ref _knownViews, previous.Views);
        }
    }

    internal sealed class DatabaseOpenAdmission(
        TableWorkspaceService owner,
        long generation,
        DatabaseState previous) : IDisposable
    {
        private int _completed;

        public void Complete() => Interlocked.Exchange(ref _completed, 1);

        public void Dispose()
        {
            if (Interlocked.Exchange(ref _completed, 1) == 0)
                owner.RollbackDatabaseOpen(generation, previous);
        }
    }

    internal sealed record DatabaseState(
        string? Database,
        IReadOnlyList<string> Tables,
        IReadOnlyList<string> Views);

    /// <summary>
    /// Replaces the cached known-tables list with <paramref name="tables"/>. Use
    /// this from collection-mutation flows (create/delete/reconcile) that have
    /// ALREADY re-listed collections: it swaps the cache without a second RPC
    /// round-trip. The list is filtered through
    /// <see cref="FilterUserTables"/> defensively so product metadata names
    /// cannot accidentally widen the selectable set. Views are reset because
    /// normalized schema mutations currently expose base tables only.
    /// </summary>
    /// <remarks>
    /// This is the fix for the "create-then-open throws ArgumentException" bug:
    /// without it, <see cref="SelectTableAsync"/> validated new tables against a
    /// stale cache populated only by <see cref="OpenDatabaseAsync"/> (once per
    /// session). Mutation handlers re-listed collections for the web sidebar but
    /// never refreshed this cache.
    /// </remarks>
    public void UpdateKnownTables(IReadOnlyList<string> tables)
    {
        var filtered = FilterUserTables(tables);
        Volatile.Write(ref _knownTables, filtered);
        Volatile.Write(ref _knownViews, Array.Empty<string>());
    }

    /// <summary>
    /// Standalone cache refresh: re-queries the gateway's collection list and
    /// swaps the known-tables cache. Used after normalized schema changes when
    /// the cache needs refreshing without necessarily re-posting
    /// <c>database.collectionsChanged</c>. On cancellation the cache is left
    /// untouched.
    /// </summary>
    public async Task RefreshKnownTablesAsync(CancellationToken token)
    {
        var summary = await _gateway.ListTablesAsync(token).ConfigureAwait(true);
        var filtered = FilterUserTables(summary.Tables);
        Volatile.Write(ref _knownTables, filtered);
        Volatile.Write(ref _knownViews, summary.Views);
    }

    /// <summary>
    /// Selects the given table and kicks off the Phase-A fetch.
    /// </summary>
    /// <remarks>
    /// <para>
    /// <paramref name="table"/> MUST be one of the names advertised by
    /// <see cref="OpenDatabaseAsync"/>; a name not in that list is a contract
    /// violation and throws <see cref="ArgumentException"/>. This enforces the
    /// "collection names come only from source discovery" invariant.
    /// </para>
    /// <para>
    /// Selecting a new table cancels any in-flight fetch for the previous table
    /// (the old generation's cancellation token fires), and late-arriving pages
    /// for the superseded table are suppressed by the generation check.
    /// The result is <see langword="true"/> only while this selection still
    /// owns the generation and published its initial dataset.
    /// </para>
    /// </remarks>
    /// <exception cref="BackendUnavailableException">
    /// The bounded recovery window expired after a transient cursor-open failure.
    /// </exception>
    public async Task<bool> SelectTableAsync(string table)
    {
        try
        {
            return await SelectOwnedTableAsync(table, CancellationToken.None)
                .ConfigureAwait(true) is not null;
        }
        catch (TableSelectionRecoveryExhaustedException exception)
        {
            // Keep the public service contract on the stable transport-facing
            // exception. The controller-only orchestration below needs the
            // closed internal outcome to distinguish recovery exhaustion from
            // notification subscriber failures of the same public type.
            throw new BackendUnavailableException(
                exception.Message,
                exception.InnerException!);
        }
    }

    internal async Task SelectTableWithSchemaAsync(
        string table,
        CancellationToken sessionToken)
    {
        OwnedSelection? selection = await SelectOwnedTableAsync(table, sessionToken)
            .ConfigureAwait(true);
        if (selection is not OwnedSelection owned || !Owns(owned.Ticket))
            return;

        Emit(owned.Ticket.Generation, new TableNotification(
            Type: "table.editSchemaLoaded", Page: null,
            MutationResult: new MutationOutcome(
                Kind: "editSchema",
                Success: true,
                Error: null,
                Result: owned.Projection.EditSchema)));
    }

    private async Task<OwnedSelection?> SelectOwnedTableAsync(
        string table,
        CancellationToken sessionToken)
    {
        if (string.IsNullOrEmpty(table))
        {
            throw new ArgumentException(
                "Table name must be a non-empty string.", nameof(table));
        }
        if (!ContainsName(_knownTables, table) && !ContainsName(_knownViews, table))
        {
            throw new ArgumentException(
                $"Table '{table}' is not one of the names advertised by " +
                $"source discovery for the current local data source.",
                nameof(table));
        }

        // Bump the generation and arm a fresh cancellation token. The previous
        // fetch's continuation will observe either cancellation (token fired)
        // or a generation mismatch and drop its pages.
        CancelSelection();
        Volatile.Write(ref _currentTable, table);
        int generation = System.Threading.Interlocked.Increment(ref _generation);
        CancellationTokenSource cts = sessionToken.CanBeCanceled
            ? CancellationTokenSource.CreateLinkedTokenSource(sessionToken)
            : new CancellationTokenSource();
        _selectCts = cts;
        var ticket = new SelectionTicket(table, generation, cts.Token);

        TableSelectionProjection? projection = await FetchAsync(
            table,
            generation,
            cts.Token)
            .ConfigureAwait(true);
        return projection is not null && Owns(ticket)
            ? new OwnedSelection(ticket, projection)
            : null;
    }

    private readonly record struct SelectionTicket(
        string Table,
        int Generation,
        CancellationToken OwnershipToken);

    private readonly record struct OwnedSelection(
        SelectionTicket Ticket,
        TableSelectionProjection Projection);

    private bool Owns(SelectionTicket ticket)
        => !ticket.OwnershipToken.IsCancellationRequested
            && !IsStale(ticket.Generation)
            && string.Equals(
                Volatile.Read(ref _currentTable),
                ticket.Table,
                StringComparison.Ordinal);

    /// <summary>
    /// Opens one revision-bound cursor window. Further windows are requested
    /// explicitly through the coordinator using the opaque next cursor.
    /// </summary>
    /// <remarks>
    /// Cancellation is EXPECTED on a table/database switch: the previous
    /// selection's token fires, the in-flight RPC throws
    /// <see cref="OperationCanceledException"/>, and we simply stop fetching.
    /// We never rethrow that ownership cancellation — the superseding selection
    /// drives the next fetch. Exact transient read failures instead open one
    /// bounded recovery window; exhausting it becomes a stable backend-
    /// unavailable outcome at the controller boundary.
    /// </remarks>
    private async Task<TableSelectionProjection?> FetchAsync(
        string table,
        int generation,
        CancellationToken ownershipToken)
    {
        SelectionRecoveryWindow? recovery = null;
        Exception? lastFailure = null;
        try
        {
            while (true)
            {
                if (IsStale(generation) || ownershipToken.IsCancellationRequested)
                    return null;

                TableSelectionProjection? projection = null;
                bool retryRequired = false;
                RecoveryOutcome attemptOutcome = RecoveryOutcome.Completed;
                try
                {
                    JsonElement query = JsonSerializer.SerializeToElement(new
                    {
                        keyword = "",
                        filters = Array.Empty<object>(),
                        sorts = Array.Empty<object>(),
                        offset = 0,
                        limit = TableWorkspaceLimits.MaxPageLimit,
                    });
                    RecoveryWait<TableSelectionProjection> attempt = recovery is null
                        ? await RunOwnedAsync(
                            token => _gateway.OpenTableSelectionAsync(
                                table,
                                query,
                                token),
                            ownershipToken).ConfigureAwait(true)
                        : await recovery.RunAsync(
                            token => _gateway.OpenTableSelectionAsync(
                                table,
                                query,
                                token)).ConfigureAwait(true);
                    attemptOutcome = attempt.Outcome;
                    projection = attempt.Value!;
                }
                catch (OperationCanceledException)
                    when (IsStale(generation) || ownershipToken.IsCancellationRequested)
                {
                    // A newer selection owns the UI. The old selection must not
                    // retry, publish a page, or let its controller load schema.
                    return null;
                }
                catch (Exception exception) when (IsTransientReadFailure(exception))
                {
                    lastFailure = exception;
                    retryRequired = true;
                    if (recovery is null)
                    {
                        // The recovery window starts at the first classified
                        // transient failure. The initial RPC has its own
                        // transport lifecycle; charging its tail latency to
                        // recovery would expire just as a restarted sidecar
                        // becomes ready.
                        recovery = new SelectionRecoveryWindow(
                            _selectionRecoveryTimeout,
                            _timeProvider,
                            ownershipToken);
                    }
                }

                if (attemptOutcome == RecoveryOutcome.Superseded)
                    return null;
                if (attemptOutcome == RecoveryOutcome.Expired)
                    throw SelectionUnavailable(lastFailure);

                if (!retryRequired)
                {
                    if (IsStale(generation) || ownershipToken.IsCancellationRequested)
                        return null;

                    // Subscriber failures are deliberately outside the transient
                    // transport catch above. A consumer exception must not reopen
                    // the cursor and duplicate a dataset read.
                    Emit(generation, new TableNotification
                    {
                        Type = "table.datasetReady",
                        Page = projection!.Page,
                    });
                    return projection;
                }

                if (IsStale(generation) || ownershipToken.IsCancellationRequested)
                    return null;
                RecoveryWait<bool> delay = await recovery!.RunAsync(async token =>
                {
                    await Task.Delay(
                        SelectionRecoveryPollInterval,
                        _timeProvider,
                        token).ConfigureAwait(true);
                    return true;
                }).ConfigureAwait(true);
                if (delay.Outcome == RecoveryOutcome.Superseded)
                    return null;
                if (delay.Outcome == RecoveryOutcome.Expired)
                    throw SelectionUnavailable(lastFailure);
            }
        }
        finally
        {
            recovery?.Dispose();
        }
    }

    private enum RecoveryOutcome
    {
        Completed,
        Superseded,
        Expired,
    }

    private readonly record struct RecoveryWait<T>(
        RecoveryOutcome Outcome,
        T? Value = default);

    private static async Task<RecoveryWait<T>> RunOwnedAsync<T>(
        Func<CancellationToken, Task<T>> operationFactory,
        CancellationToken ownershipToken)
    {
        if (ownershipToken.IsCancellationRequested)
            return new RecoveryWait<T>(RecoveryOutcome.Superseded);

        var ownershipLost = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        using CancellationTokenRegistration registration = ownershipToken.UnsafeRegister(
            static state => ((TaskCompletionSource)state!).TrySetResult(),
            ownershipLost);
        Task<T> operation = operationFactory(ownershipToken);
        Task winner = await Task.WhenAny(operation, ownershipLost.Task)
            .ConfigureAwait(true);
        if (ownershipToken.IsCancellationRequested
            || ReferenceEquals(winner, ownershipLost.Task))
        {
            ObserveLate(operation);
            return new RecoveryWait<T>(RecoveryOutcome.Superseded);
        }

        T value = await operation.ConfigureAwait(true);
        if (ownershipToken.IsCancellationRequested)
            return new RecoveryWait<T>(RecoveryOutcome.Superseded);
        return new RecoveryWait<T>(RecoveryOutcome.Completed, value);
    }

    private sealed class SelectionRecoveryWindow : IDisposable
    {
        private readonly CancellationTokenSource _deadlineLifetime = new();
        private readonly CancellationToken _ownershipToken;
        private readonly Task _deadline;
        private readonly TaskCompletionSource _ownershipLost = new(
            TaskCreationOptions.RunContinuationsAsynchronously);
        private readonly CancellationTokenRegistration _ownershipRegistration;

        public SelectionRecoveryWindow(
            TimeSpan timeout,
            TimeProvider timeProvider,
            CancellationToken ownershipToken)
        {
            _ownershipToken = ownershipToken;
            _deadline = Task.Delay(timeout, timeProvider, _deadlineLifetime.Token);
            _ownershipRegistration = ownershipToken.UnsafeRegister(
                static state => ((TaskCompletionSource)state!).TrySetResult(),
                _ownershipLost);
        }

        public async Task<RecoveryWait<T>> RunAsync<T>(
            Func<CancellationToken, Task<T>> operationFactory)
        {
            using var attempt = CancellationTokenSource.CreateLinkedTokenSource(
                _ownershipToken);
            Task<T> operation = operationFactory(attempt.Token);
            Task winner = await Task.WhenAny(
                operation,
                _deadline,
                _ownershipLost.Task).ConfigureAwait(true);

            if (_ownershipToken.IsCancellationRequested
                || ReferenceEquals(winner, _ownershipLost.Task))
            {
                attempt.Cancel();
                ObserveLate(operation);
                return new RecoveryWait<T>(RecoveryOutcome.Superseded);
            }
            if (_deadline.IsCompleted)
            {
                attempt.Cancel();
                ObserveLate(operation);
                return new RecoveryWait<T>(RecoveryOutcome.Expired);
            }

            T value = await operation.ConfigureAwait(true);
            if (_ownershipToken.IsCancellationRequested)
                return new RecoveryWait<T>(RecoveryOutcome.Superseded);
            if (_deadline.IsCompleted)
                return new RecoveryWait<T>(RecoveryOutcome.Expired);
            return new RecoveryWait<T>(RecoveryOutcome.Completed, value);
        }

        public void Dispose()
        {
            _ownershipRegistration.Dispose();
            _deadlineLifetime.Cancel();
            _deadlineLifetime.Dispose();
        }

        private static void ObserveLate(Task operation)
            => TableWorkspaceService.ObserveLate(operation);
    }

    private static void ObserveLate(Task operation)
    {
        _ = operation.ContinueWith(
            static completed => _ = completed.Exception,
            CancellationToken.None,
            TaskContinuationOptions.ExecuteSynchronously
                | TaskContinuationOptions.OnlyOnFaulted,
            TaskScheduler.Default);
    }

    private static TableSelectionRecoveryExhaustedException SelectionUnavailable(
        Exception? lastFailure)
        => new(
            "The table selection did not recover before the read deadline.",
            lastFailure ?? new InvalidOperationException(
                "The table selection read deadline expired."));

    private static bool IsTransientReadFailure(Exception exception)
        => exception is BackendUnavailableException
            or ObjectDisposedException
            || exception is RpcRemoteException remote
                && remote.Code == ProductDataRpcErrorCode
                && HasBackendCode(remote.ErrorData, "sidecar.unavailable");

    private static bool HasBackendCode(JsonElement? data, string expected)
        => data is JsonElement value
            && value.ValueKind == JsonValueKind.Object
            && value.TryGetProperty("code", out JsonElement code)
            && code.ValueKind == JsonValueKind.String
            && string.Equals(code.GetString(), expected, StringComparison.Ordinal);

    /// <summary>
    /// True if the current generation has advanced past
    /// <paramref name="generation"/> (a newer selection superseded it).
    /// </summary>
    private bool IsStale(int generation)
        => Volatile.Read(ref _generation) != generation;

    // -------------------------------------------------------------------
    // B1 Task 5: mutation orchestration.
    //
    // Every mutation method captures the current generation up front and
    // suppresses the response (no notification) if the user switched tables
    // before the backend replied — a stale commit must never land on a
    // different table. A same-table data refresh also advances the selection
    // generation, but must not discard the mutation confirmation that caused
    // that refresh. Retry after an unknown commit state is prohibited: the
    // host surfaces the error and lets the user re-issue explicitly.
    // -------------------------------------------------------------------

    private int CurrentGeneration => Volatile.Read(ref _generation);

    private bool IsMutationStale(int generation, string table)
    {
        if (!IsStale(generation))
        {
            return false;
        }
        string? currentTable = Volatile.Read(ref _currentTable);
        return currentTable is not null
            && !string.Equals(currentTable, table, StringComparison.Ordinal);
    }

    private void EmitMutation(
        int generation,
        string table,
        TableNotification notification)
    {
        if (IsMutationStale(generation, table))
        {
            return;
        }
        Notification?.Invoke(notification);
    }

    /// <summary>
    /// B1 Task 1: fetch the editable schema for a table. Notifies
    /// <c>table.editSchemaLoaded</c> on success.
    /// </summary>
    public async Task<EditSchemaResult?> GetEditSchemaAsync(string table)
    {
        if (string.IsNullOrEmpty(table))
        {
            return null;
        }
        var ticket = new SelectionTicket(
            table,
            CurrentGeneration,
            _selectCts?.Token ?? CancellationToken.None);
        return await GetEditSchemaAsync(ticket).ConfigureAwait(false);
    }

    private async Task<EditSchemaResult?> GetEditSchemaAsync(
        SelectionTicket ticket)
    {
        if (!Owns(ticket))
            return null;

        try
        {
            RecoveryWait<EditSchemaResult> schemaRead = await RunOwnedAsync(
                token => _gateway.GetEditSchemaAsync(ticket.Table, token),
                ticket.OwnershipToken).ConfigureAwait(false);
            if (schemaRead.Outcome == RecoveryOutcome.Superseded || !Owns(ticket))
            {
                return null;
            }
            EditSchemaResult schema = schemaRead.Value!;
            Emit(ticket.Generation, new TableNotification(
                Type: "table.editSchemaLoaded", Page: null,
                MutationResult: new MutationOutcome(
                    Kind: "editSchema", Success: true, Error: null, Result: schema)));
            return schema;
        }
        catch (OperationCanceledException) when (!Owns(ticket))
        {
            return null;
        }
        catch (Exception ex)
        {
            if (!Owns(ticket))
            {
                return null;
            }
            EmitMutationError(
                ticket.Generation,
                "editSchema",
                MutationErrorMapper.Map(ex));
            return null;
        }
    }

    /// <summary>
    /// B1 Task 3: update one cell. Emits <c>table.editCommitted</c> on success
    /// or <c>table.editRejected</c> on conflict/validation failure.
    /// </summary>
    public async Task<bool> UpdateCellAsync(
        string table,
        object rowKey,
        string column,
        object? oldValue,
        object? newValue,
        string schemaRevision,
        string? expectedDigest = null,
        string? requestId = null)
    {
        if (string.IsNullOrEmpty(table))
        {
            return false;
        }
        int generation = CurrentGeneration;
        try
        {
            var result = await _gateway.UpdateCellAsync(
                table, rowKey, column, oldValue, newValue, schemaRevision,
                CancellationToken.None, expectedDigest).ConfigureAwait(false);
            if (IsMutationStale(generation, table))
            {
                // The user switched tables: do NOT apply the commit anywhere.
                return false;
            }
            EmitMutation(generation, table, new TableNotification(
                Type: "table.editCommitted", Page: null,
                MutationResult: new MutationOutcome(
                    Kind: "updateCell", Success: true, Error: null, Result: result),
                RequestId: requestId));
            return true;
        }
        catch (Exception ex)
        {
            if (IsMutationStale(generation, table))
            {
                return false;
            }
            EmitMutation(generation, table, new TableNotification(
                Type: "table.editRejected", Page: null,
                MutationResult: new MutationOutcome(
                    Kind: "updateCell",
                    Success: false,
                    Error: MutationErrorMapper.Map(ex),
                    Result: null),
                RequestId: requestId));
            return false;
        }
    }

    /// <summary>B1 Task 4: insert one row. Emits <c>table.rowsInserted</c>.</summary>
    public async Task<bool> InsertRowAsync(
        string table,
        IReadOnlyDictionary<string, object?> values,
        string schemaRevision,
        string? requestId = null)
    {
        if (string.IsNullOrEmpty(table))
        {
            return false;
        }
        int generation = CurrentGeneration;
        try
        {
            var result = await _gateway.InsertRowAsync(
                table, values, schemaRevision, CancellationToken.None)
                .ConfigureAwait(false);
            if (IsMutationStale(generation, table))
            {
                return false;
            }
            EmitMutation(generation, table, new TableNotification(
                Type: "table.rowsInserted", Page: null,
                MutationResult: new MutationOutcome(
                    Kind: "insertRow", Success: true, Error: null, Result: result),
                RequestId: requestId));
            return true;
        }
        catch (Exception ex)
        {
            if (IsMutationStale(generation, table))
            {
                return false;
            }
            EmitMutation(generation, table, new TableNotification(
                Type: "table.editRejected", Page: null,
                MutationResult: new MutationOutcome(
                    Kind: "insertRow",
                    Success: false,
                    Error: MutationErrorMapper.Map(ex),
                    Result: null),
                RequestId: requestId));
            return false;
        }
    }

    /// <summary>B1 Task 4: delete rows. Emits <c>table.rowsDeleted</c>.</summary>
    public async Task<bool> DeleteRowsAsync(
        string table,
        IReadOnlyList<(object RowKey, string ExpectedDigest)> rows,
        string schemaRevision,
        string? requestId = null)
    {
        if (string.IsNullOrEmpty(table) || rows.Count == 0)
        {
            return false;
        }
        int generation = CurrentGeneration;
        try
        {
            var result = await _gateway.DeleteRowsAsync(
                table, rows, schemaRevision, CancellationToken.None)
                .ConfigureAwait(false);
            if (IsMutationStale(generation, table))
            {
                return false;
            }
            EmitMutation(generation, table, new TableNotification(
                Type: "table.rowsDeleted", Page: null,
                MutationResult: new MutationOutcome(
                    Kind: "deleteRows", Success: true, Error: null, Result: result),
                RequestId: requestId));
            return true;
        }
        catch (Exception ex)
        {
            if (IsMutationStale(generation, table))
            {
                return false;
            }
            EmitMutation(generation, table, new TableNotification(
                Type: "table.editRejected", Page: null,
                MutationResult: new MutationOutcome(
                    Kind: "deleteRows",
                    Success: false,
                    Error: MutationErrorMapper.Map(ex),
                    Result: null),
                RequestId: requestId));
            return false;
        }
    }

    private void EmitMutationError(
        int generation,
        string kind,
        MutationError error,
        string? requestId = null)
    {
        Emit(generation, new TableNotification(
            Type: "table.editRejected", Page: null,
            MutationResult: new MutationOutcome(
                Kind: kind, Success: false, Error: error, Result: null),
            RequestId: requestId));
    }

    /// <summary>
    /// Emits a notification only if the generation is still current.
    /// </summary>
    private void Emit(int generation, TableNotification notification)
    {
        if (IsStale(generation))
        {
            return;
        }
        Notification?.Invoke(notification);
    }

    private void CancelSelection()
    {
        var existing = System.Threading.Interlocked.Exchange(ref _selectCts, null);
        if (existing is not null)
        {
            try { existing.Cancel(); } catch { /* best-effort */ }
            existing.Dispose();
        }
    }

    private static bool ContainsName(IReadOnlyList<string> names, string name)
    {
        foreach (var n in names)
        {
            if (string.Equals(n, name, StringComparison.Ordinal))
            {
                return true;
            }
        }
        return false;
    }

    /// <summary>
    /// Filters a raw collection list to user tables. Product table discovery
    /// is already closed over normalized user tables.
    /// Keep a final defensive filter for internal VibeTable namespaces in case
    /// a future catalog adapter is misconfigured.
    /// </summary>
    private static IReadOnlyList<string> FilterUserTables(IEnumerable<string> collections)
        => collections
            .Where(name => !string.IsNullOrWhiteSpace(name)
                && !name.StartsWith("vibetable_", StringComparison.OrdinalIgnoreCase))
            .Distinct(StringComparer.Ordinal)
            .ToArray();
}

/// <summary>
/// A Phase-A host -&gt; web notification from the table workspace.
/// </summary>
/// <remarks>
/// <see cref="Type"/> is one of <c>table.pageLoaded</c> (bounded query window),
/// <c>table.datasetReady</c> (initial authoritative window), or — for B1 mutations —
/// <c>table.editSchemaLoaded</c> / <c>table.editCommitted</c> /
/// <c>table.editRejected</c> / <c>table.rowsInserted</c> /
/// <c>table.rowsDeleted</c>. <see cref="Page"/> carries the DTO for page
/// notifications; <see cref="MutationResult"/> carries the mutation outcome.
/// </remarks>
public sealed class TableNotification
{
    public TableNotification() { }

    public TableNotification(
        string Type,
        TablePage? Page,
        MutationOutcome? MutationResult = null,
        string? RequestId = null)
    {
        this.Type = Type;
        this.Page = Page;
        this.MutationResult = MutationResult;
        this.RequestId = RequestId;
    }

    public string Type { get; set; } = string.Empty;
    public TablePage? Page { get; set; }
    public MutationOutcome? MutationResult { get; set; }
    public string? RequestId { get; set; }
}

/// <summary>
/// B1 mutation outcome forwarded to the WebView. On success <see cref="Result"/>
/// is non-null; on failure <see cref="Error"/> carries the mapped, localizable
/// error the web layer renders.
/// </summary>
public sealed record MutationOutcome(
    string Kind,
    bool Success,
    MutationError? Error,
    object? Result);
