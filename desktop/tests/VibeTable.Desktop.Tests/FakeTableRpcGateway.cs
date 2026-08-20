using System;
using System.Collections.Generic;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// Deterministic fake of <see cref="ITableRpcGateway"/> for workspace service
/// tests. Returns canned <see cref="DatabaseOpenResult"/> / <see cref="TablePage"/>
/// fixtures keyed by path / table name, and records every call for assertion.
/// </summary>
/// <remarks>
/// <para>
/// The fake is fully synchronous-but-async (no real I/O) so tests are stable
/// and fast. It also supports an optional per-table <c>window-read gate</c>: when set,
/// the window read for that table awaits the supplied task before returning,
/// letting tests simulate a slow/stale backend response.
/// </para>
/// <para>
/// The fixture window reader cooperates with cancellation: if the
/// supplied <c>CancellationToken</c> is cancelled while waiting on a gate, the
/// fake throws <see cref="OperationCanceledException"/> (matching how a real
/// gateway would surface a cancelled RPC).
/// </para>
/// </remarks>
public sealed class FakeTableRpcGateway : ITableRpcGateway
{
    public readonly struct WindowReadCall
    {
        public WindowReadCall(string table, int offset, int limit)
        {
            Table = table;
            Offset = offset;
            Limit = limit;
        }
        public readonly string Table;
        public readonly int Offset;
        public readonly int Limit;
    }

    public Dictionary<string, DatabaseOpenResult> DatabaseOpenResults { get; } =
        new(StringComparer.Ordinal);

    public Dictionary<string, Dictionary<int, TablePage>> TablePages { get; } =
        new(StringComparer.Ordinal);

    public List<string> OpenDatabaseCalls { get; } = new();

    public List<WindowReadCall> WindowWindowReadCalls { get; } = new();

    /// <summary>
    /// Optional scripted read used by consistency/retry tests. The fourth
    /// argument is the 1-based total read call count.
    /// </summary>
    public Func<string, int, int, int, TablePage>? WindowReadOverride { get; set; }

    /// <summary>
    /// When set, <see cref="ListTablesAsync"/> returns this instead of falling
    /// back to the first <see cref="DatabaseOpenResults"/> entry. Lets tests
    /// control the refresh result independently of the open result.
    /// </summary>
    public TableSummary? ListTablesResult { get; set; }

    public Func<CancellationToken, Task<TableSummary>>? ListTablesOverride { get; set; }

    /// <summary>
    /// Optional scripted open used by workspace-recovery tests. When set it
    /// replaces the dictionary lookup entirely, so a test can fail the first
    /// attempts (sidecar recycling) and then succeed.
    /// </summary>
    public Func<string, Task<DatabaseOpenResult>>? OpenDatabaseOverride { get; set; }

    private readonly Dictionary<string, Task> _readGates = new(StringComparer.Ordinal);

    public void SetWindowReadGate(string table, Task gate)
    {
        _readGates[table] = gate;
    }

    public Task<DatabaseOpenResult> OpenDatabaseAsync(string path, CancellationToken token)
    {
        OpenDatabaseCalls.Add(path);
        if (OpenDatabaseOverride is { } scripted)
        {
            return scripted(path);
        }
        if (!DatabaseOpenResults.TryGetValue(path, out var result))
        {
            return Task.FromException<DatabaseOpenResult>(
                new InvalidOperationException($"fake: no open result for '{path}'"));
        }
        return Task.FromResult(result);
    }

    public Task<TableSummary> ListTablesAsync(CancellationToken token)
    {
        if (ListTablesOverride is { } scripted)
        {
            return scripted(token);
        }
        // table.list reuses the same object catalog as database.open in Phase A.
        // Tests key on the open result; list returns the same tables/views.
        // When ListTablesResult is set explicitly, prefer it (used by the
        // known-tables refresh tests, which need to distinguish the list result
        // from the open result).
        if (ListTablesResult is not null)
        {
            return Task.FromResult(ListTablesResult);
        }
        foreach (var kv in DatabaseOpenResults)
        {
            return Task.FromResult(new TableSummary(
                kv.Value.Tables, kv.Value.Views, kv.Value.DisplayNames));
        }
        return Task.FromResult(new TableSummary(
            Array.Empty<string>(),
            Array.Empty<string>(),
            TestDisplayNames.For()));
    }

    private async Task<TablePage> ReadFixturePageAsync(
        string table, int offset, int limit, CancellationToken token)
    {
        WindowWindowReadCalls.Add(new WindowReadCall(table, offset, limit));

        if (_readGates.TryGetValue(table, out var gate))
        {
            // If the caller cancels while waiting, surface cancellation the way
            // a real gateway would: OperationCanceledException. Use Register so
            // we don't hang the test when the workspace service cancels a stale
            // fetch.
            using var reg = token.Register(() => { });
            await gate.WaitAsync(token).ConfigureAwait(false);
        }

        if (token.IsCancellationRequested)
        {
            throw new OperationCanceledException(token);
        }

        if (WindowReadOverride is not null)
        {
            return WindowReadOverride(
                table, offset, limit, WindowWindowReadCalls.Count);
        }

        if (!TablePages.TryGetValue(table, out var pages))
        {
            return TablePageForMissing(table, offset, limit);
        }
        if (pages.TryGetValue(offset, out var page))
        {
            return page;
        }
        // Offset past the end: return an empty page so the fetcher stops.
        return new TablePage(
            Table: table,
            Columns: pages.Values.Count > 0
                ? pages[0].Columns
                : Array.Empty<ColumnSchema>(),
            Rows: Array.Empty<Dictionary<string, object?>>(),
            Offset: offset,
            Limit: limit,
            TotalRows: pages.Values.Count > 0 ? pages[0].TotalRows : 0,
            Mode: pages.Values.Count > 0 ? pages[0].Mode : "remote");
    }

    private static TablePage TablePageForMissing(string table, int offset, int limit)
        => new(
            Table: table,
            Columns: Array.Empty<ColumnSchema>(),
            Rows: Array.Empty<Dictionary<string, object?>>(),
            Offset: offset,
            Limit: limit,
            TotalRows: 0,
            Mode: "remote");

    // -------------------------------------------------------------------
    // B1 mutation methods (deterministic stubs for workspace tests).
    // -------------------------------------------------------------------

    public Dictionary<string, EditSchemaResult> EditSchemaResults { get; } =
        new(StringComparer.Ordinal);

    public Func<string, CancellationToken, Task<EditSchemaResult>>?
        EditSchemaOverride
    { get; set; }

    public List<string> UpdateCellCalls { get; } = new();
    public List<string> InsertRowCalls { get; } = new();
    public List<string> DeleteRowsCalls { get; } = new();

    /// <summary>When set, the next UpdateCell call throws this exception
    /// (used to exercise the error-mapping path).</summary>
    public Exception? NextUpdateCellException { get; set; }
    public TaskCompletionSource<UpdateCellResult>? PendingUpdateCell { get; set; }

    public Task<EditSchemaResult> GetEditSchemaAsync(string table, CancellationToken token)
    {
        if (EditSchemaOverride is { } scripted)
            return scripted(table, token);
        if (EditSchemaResults.TryGetValue(table, out var schema))
        {
            return Task.FromResult(schema);
        }
        return Task.FromException<EditSchemaResult>(
            new InvalidOperationException($"fake: no edit schema for '{table}'"));
    }

    public Task<UpdateCellResult> UpdateCellAsync(
        string table, object rowKey, string column,
        object? oldValue, object? newValue, string schemaRevision,
        CancellationToken token, string? expectedDigest = null)
    {
        UpdateCellCalls.Add(table);
        if (PendingUpdateCell is not null)
        {
            return PendingUpdateCell.Task;
        }
        if (NextUpdateCellException is not null)
        {
            var ex = NextUpdateCellException;
            NextUpdateCellException = null;
            return Task.FromException<UpdateCellResult>(ex);
        }
        var row = new Dictionary<string, object?>
        {
            ["rowKey"] = rowKey,
            [column] = newValue,
        };
        return Task.FromResult(new UpdateCellResult(
            rowKey, column, newValue, row,
            new MutationRevision("fake", schemaRevision, 1)));
    }

    public Task<InsertRowResult> InsertRowAsync(
        string table, IReadOnlyDictionary<string, object?> values,
        string schemaRevision, CancellationToken token)
    {
        InsertRowCalls.Add(table);
        return Task.FromResult(new InsertRowResult(
            RowKey: 1,
            Row: new Dictionary<string, object?>(values),
            Revision: new MutationRevision("fake", schemaRevision, 1)));
    }

    public Task<DeleteRowsResult> DeleteRowsAsync(
        string table, IReadOnlyList<(object RowKey, string ExpectedDigest)> rows,
        string schemaRevision, CancellationToken token)
    {
        DeleteRowsCalls.Add(table);
        var keys = new List<object>();
        foreach (var r in rows)
        {
            keys.Add(r.RowKey);
        }
        return Task.FromResult(new DeleteRowsResult(
            keys, new MutationRevision("fake", schemaRevision, 1)));
    }

    public Task<ReadRowsResult> ReadRowsAsync(
        string table, IReadOnlyList<object> rowKeys, CancellationToken token)
    {
        var rows = new List<IReadOnlyDictionary<string, object?>>();
        foreach (var k in rowKeys)
        {
            rows.Add(new Dictionary<string, object?> { ["rowKey"] = k });
        }
        return Task.FromResult(new ReadRowsResult(
            rows, new MutationRevision("fake", "rev", 1)));
    }

    // -------------------------------------------------------------------
    // G1 history methods.
    // -------------------------------------------------------------------

    public List<ReadChangeSetsParams> ReadChangeSetsCalls { get; } = new();
    public List<PreviewRestoreParams> PreviewRestoreCalls { get; } = new();
    public List<ApplyRestoreParams> ApplyRestoreCalls { get; } = new();
    public HistoryPage? NextHistoryPage { get; set; }
    public RestorePreview? NextRestorePreview { get; set; }
    public RestoreResult? NextRestoreResult { get; set; }
    public Exception? NextHistoryException { get; set; }

    public Task<HistoryPage> ReadChangeSetsAsync(
        ReadChangeSetsParams parameters, CancellationToken token)
    {
        ReadChangeSetsCalls.Add(parameters);
        if (NextHistoryException is not null)
        {
            var exception = NextHistoryException;
            NextHistoryException = null;
            return Task.FromException<HistoryPage>(exception);
        }
        return Task.FromResult(NextHistoryPage ?? new HistoryPage(
            parameters.Collection,
            parameters.ItemId,
            new List<HistoryChangeSet>(),
            0,
            "fake-capability",
            "fake-schema",
            parameters.Scope,
            parameters.Field,
            false));
    }

    public Task<RestorePreview> PreviewRestoreAsync(
        PreviewRestoreParams parameters, CancellationToken token)
    {
        PreviewRestoreCalls.Add(parameters);
        if (NextHistoryException is not null)
        {
            var exception = NextHistoryException;
            NextHistoryException = null;
            return Task.FromException<RestorePreview>(exception);
        }
        return Task.FromResult(NextRestorePreview ?? new RestorePreview(
            parameters.Collection,
            parameters.ItemId,
            parameters.TargetRevision,
            "fake-current",
            "fake-schema",
            new List<ScalarFieldChange>(),
            new List<RelationFieldChange>(),
            new List<RestoreDiagnostic>(),
            "fake-restore-token",
            "2099-01-01T00:00:00Z",
            parameters.Scope,
            parameters.Field,
            true,
            new List<string>()));
    }

    public Task<RestoreResult> ApplyRestoreAsync(
        ApplyRestoreParams parameters, CancellationToken token)
    {
        ApplyRestoreCalls.Add(parameters);
        if (NextHistoryException is not null)
        {
            var exception = NextHistoryException;
            NextHistoryException = null;
            return Task.FromException<RestoreResult>(exception);
        }
        return Task.FromResult(NextRestoreResult ?? new RestoreResult(
            parameters.Collection,
            parameters.ItemId,
            "fake-target",
            "fake-new-revision",
            new Dictionary<string, object?>()));
    }

    // -------------------------------------------------------------------
    // B3 query/state methods (deterministic stubs for workspace/coordinator
    // tests).
    // -------------------------------------------------------------------

    public List<string> QueryWindowCalls { get; } = new();
    public List<JsonElement> RawViewQueries { get; } = new();
    public List<string> CursorFetchCalls { get; } = new();
    public Dictionary<string, TablePage> CursorPageResults { get; } = new(StringComparer.Ordinal);
    public Dictionary<string, TablePage> CursorOpenResults { get; } =
        new(StringComparer.Ordinal);
    public Dictionary<string, TablePage> QueryWindowResults { get; } =
        new(StringComparer.Ordinal);

    /// <summary>
    /// Optional async cursor-open script used by selection recovery tests.
    /// It runs before canned cursor results so tests can model a sidecar
    /// restart, cancellation, and a replacement read without real delays.
    /// </summary>
    public Func<string, JsonElement, CancellationToken, Task<TablePage>>?
        CursorOpenOverride
    { get; set; }

    public List<string> ValidateSnapshotCalls { get; } = new();
    public SnapshotValidation? NextValidateSnapshotResult { get; set; }

    public Dictionary<string, GridStateResult> GridStateResults { get; } =
        new(StringComparer.Ordinal);
    public List<string> GetGridStateCalls { get; } = new();
    public List<string> SaveGridStateCalls { get; } = new();
    public List<(string DatabaseId, string Table, GridState State, string? Revision)>
        SavedGridStates
    { get; } = new();
    public GridStateResult? NextSaveGridStateResult { get; set; }

    public Task<TablePage> QueryTableViewRawAsync(
        string table, JsonElement query, CancellationToken token)
    {
        QueryWindowCalls.Add(table);
        RawViewQueries.Add(query.Clone());
        if (QueryWindowResults.TryGetValue(table, out var page))
        {
            return Task.FromResult(page);
        }
        return ReadFixturePageAsync(table, 0, TableWorkspaceLimits.MaxPageLimit, token);
    }

    public Task<TablePage> OpenTableCursorRawAsync(
        string table, JsonElement query, CancellationToken token)
    {
        if (CursorOpenOverride is { } scripted)
        {
            QueryWindowCalls.Add(table);
            RawViewQueries.Add(query.Clone());
            return scripted(table, query, token);
        }
        if (CursorOpenResults.TryGetValue(table, out var page))
        {
            QueryWindowCalls.Add(table);
            RawViewQueries.Add(query.Clone());
            return Task.FromResult(page);
        }
        return QueryTableViewRawAsync(table, query, token);
    }

    public Task<TablePage> FetchTableCursorAsync(string cursor, CancellationToken token)
    {
        CursorFetchCalls.Add(cursor);
        if (CursorPageResults.TryGetValue(cursor, out var page))
        {
            return Task.FromResult(page);
        }
        throw new InvalidOperationException("Unknown fake cursor.");
    }

    public Task<SnapshotValidation> ValidateSnapshotAsync(
        QuerySnapshot snapshot, int? currentRevision, CancellationToken token)
    {
        ValidateSnapshotCalls.Add(snapshot.Table);
        return Task.FromResult(
            NextValidateSnapshotResult ?? new SnapshotValidation(Valid: true));
    }

    public Task<GridStateResult> GetGridStateAsync(
        string databaseId, string table, CancellationToken token)
    {
        GetGridStateCalls.Add(table);
        if (GridStateResults.TryGetValue(table, out var result))
        {
            return Task.FromResult(result);
        }
        return Task.FromResult(new GridStateResult(
            new GridState(), "rev-1", Conflict: false));
    }

    public Task<GridStateResult> SaveGridStateAsync(
        string databaseId, string table, GridState state,
        string? revision, CancellationToken token)
    {
        SaveGridStateCalls.Add(table);
        SavedGridStates.Add((databaseId, table, state, revision));
        return Task.FromResult(
            NextSaveGridStateResult ?? new GridStateResult(
                state, "rev-2", Conflict: false));
    }

    // -------------------------------------------------------------------
    // B2 paste methods (deterministic stubs for workspace tests).
    // -------------------------------------------------------------------

    public List<string> PreviewPasteCalls { get; } = new();
    public List<string> ApplyPasteCalls { get; } = new();

    /// <summary>When set, the next PreviewPaste call returns this plan.</summary>
    public PastePlan? NextPreviewPasteResult { get; set; }

    /// <summary>When set, the next ApplyPaste call returns this result.</summary>
    public ApplyPasteResult? NextApplyPasteResult { get; set; }

    public Task<PastePlan> PreviewPasteAsync(
        string collection, string schemaRevision,
        IReadOnlyDictionary<string, object?> selection,
        PasteStartCell startCell,
        IReadOnlyList<IReadOnlyList<PasteCell>> cells,
        CancellationToken token)
    {
        PreviewPasteCalls.Add(collection);
        return Task.FromResult(NextPreviewPasteResult ?? new PastePlan(
            Collection: collection,
            SchemaRevision: schemaRevision,
            CapabilityHash: "fake-capability",
            Summary: new PasteSummary(0, 0, 0, 0, 0),
            Rows: Array.Empty<PastePlanRow>(),
            Diagnostics: Array.Empty<PasteCellDiagnostic>(),
            Token: new PasteToken("fake-token", 0, Consumed: false),
            Overflow: false));
    }

    public Task<ApplyPasteResult> ApplyPasteAsync(
        string collection, string token, string idempotencyKey,
        CancellationToken cancellationToken)
    {
        ApplyPasteCalls.Add(collection);
        return Task.FromResult(NextApplyPasteResult ?? new ApplyPasteResult(
            Collection: collection,
            Outcome: ApplyOutcomes.Committed,
            CreatedRowKeys: Array.Empty<object>(),
            UpdatedRowKeys: Array.Empty<object>(),
            SkippedRowKeys: Array.Empty<object>(),
            Conflicts: Array.Empty<ApplyPasteConflict>(),
            RequestId: idempotencyKey));
    }
}

internal static class TestDisplayNames
{
    internal static IReadOnlyDictionary<string, string> For(params string[] collections)
        => collections.ToDictionary(
            collection => collection,
            collection => collection,
            StringComparer.Ordinal);
}
