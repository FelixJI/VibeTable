using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

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
    private readonly ITableRpcGateway _gateway;

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

    private int _generation;
    private CancellationTokenSource? _selectCts;

    public TableWorkspaceService(ITableRpcGateway gateway)
    {
        _gateway = gateway ?? throw new ArgumentNullException(nameof(gateway));
    }

    /// <summary>
    /// Raised whenever the workspace emits a Phase-A host notification
    /// (<c>table.pageLoaded</c>, <c>table.datasetReady</c>). MainWindow forwards
    /// these to the WebView.
    /// </summary>
    public event Action<TableNotification>? Notification;

    /// <summary>The logical source identifier currently open.</summary>
    public string? CurrentDatabase => _currentDatabase;

    /// <summary>
    /// Opens the configured source identified by <paramref name="path"/> and
    /// caches the advertised collection names so <see cref="SelectTableAsync"/>
    /// can enforce the "known name" invariant.
    /// </summary>
    public async Task<DatabaseOpenResult> OpenDatabaseAsync(string path)
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

        var result = await _gateway.OpenDatabaseAsync(path, CancellationToken.None)
            .ConfigureAwait(true);
        _currentDatabase = path;
        // Filter on the way in so the cache matches the sidebar exactly: the
        // The gateway returns the raw collection list, including vibetable_*
        // product metadata tables, but those are never user-selectable.
        Volatile.Write(ref _knownTables, FilterUserTables(result.Tables));
        Volatile.Write(ref _knownViews, result.Views);
        return result;
    }

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
    /// </para>
    /// </remarks>
    public async Task SelectTableAsync(string table)
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
        var cts = new CancellationTokenSource();
        _selectCts = cts;

        await FetchAsync(table, generation, cts.Token).ConfigureAwait(true);
    }

    /// <summary>
    /// Opens one revision-bound cursor window. Further windows are requested
    /// explicitly through the coordinator using the opaque next cursor.
    /// </summary>
    /// <remarks>
    /// Cancellation is EXPECTED on a table/database switch: the previous
    /// selection's token fires, the in-flight RPC throws
    /// <see cref="OperationCanceledException"/>, and we simply stop fetching.
    /// We never rethrow — the superseding selection drives the next fetch.
    /// </remarks>
    private async Task FetchAsync(
        string table,
        int generation,
        CancellationToken token)
    {
        TablePage firstWindow;
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
            firstWindow = await _gateway.OpenTableCursorRawAsync(table, query, token)
                .ConfigureAwait(true);
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested || IsStale(generation))
        {
            // Superseded by a newer selection (or cancelled): stop quietly. The
            // newer selection's fetch is responsible for emitting pages.
            return;
        }

        if (IsStale(generation))
        {
            return;
        }

        Emit(generation, new TableNotification
        {
            Type = "table.datasetReady",
            Page = firstWindow,
        });
    }

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
        int generation = CurrentGeneration;
        try
        {
            var schema = await _gateway.GetEditSchemaAsync(table, CancellationToken.None)
                .ConfigureAwait(false);
            if (IsStale(generation))
            {
                return null;
            }
            Emit(generation, new TableNotification(
                Type: "table.editSchemaLoaded", Page: null,
                MutationResult: new MutationOutcome(
                    Kind: "editSchema", Success: true, Error: null, Result: schema)));
            return schema;
        }
        catch (Exception ex)
        {
            if (IsStale(generation))
            {
                return null;
            }
            EmitMutationError(generation, "editSchema", MutationErrorMapper.Map(ex));
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
