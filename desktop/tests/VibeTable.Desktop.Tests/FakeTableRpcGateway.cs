using System;
using System.Collections.Generic;
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
/// and fast. It also supports an optional per-table <c>read gate</c>: when set,
/// the page read for that table awaits the supplied task before returning,
/// letting tests simulate a slow/stale backend response.
/// </para>
/// <para>
/// <see cref="ReadTablePageAsync"/> cooperates with cancellation: if the
/// supplied <c>CancellationToken</c> is cancelled while waiting on a gate, the
/// fake throws <see cref="OperationCanceledException"/> (matching how a real
/// gateway would surface a cancelled RPC).
/// </para>
/// </remarks>
public sealed class FakeTableRpcGateway : ITableRpcGateway
{
    public readonly struct ReadCall
    {
        public ReadCall(string table, int offset, int limit)
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

    public List<ReadCall> ReadTablePageCalls { get; } = new();

    /// <summary>
    /// When set, <see cref="ListTablesAsync"/> returns this instead of falling
    /// back to the first <see cref="DatabaseOpenResults"/> entry. Lets tests
    /// control the refresh result independently of the open result.
    /// </summary>
    public TableSummary? ListTablesResult { get; set; }

    private readonly Dictionary<string, Task> _readGates = new(StringComparer.Ordinal);

    public void SetReadGate(string table, Task gate)
    {
        _readGates[table] = gate;
    }

    public Task<DatabaseOpenResult> OpenDatabaseAsync(string path, CancellationToken token)
    {
        OpenDatabaseCalls.Add(path);
        if (!DatabaseOpenResults.TryGetValue(path, out var result))
        {
            return Task.FromException<DatabaseOpenResult>(
                new InvalidOperationException($"fake: no open result for '{path}'"));
        }
        return Task.FromResult(result);
    }

    public Task<TableSummary> ListTablesAsync(CancellationToken token)
    {
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
                kv.Value.Tables, kv.Value.Views));
        }
        return Task.FromResult(new TableSummary(
            Array.Empty<string>(), Array.Empty<string>()));
    }

    public async Task<TablePage> ReadTablePageAsync(
        string table, int offset, int limit, CancellationToken token)
    {
        ReadTablePageCalls.Add(new ReadCall(table, offset, limit));

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
            Mode: pages.Values.Count > 0 ? pages[0].Mode : "client");
    }

    private static TablePage TablePageForMissing(string table, int offset, int limit)
        => new(
            Table: table,
            Columns: Array.Empty<ColumnSchema>(),
            Rows: Array.Empty<Dictionary<string, object?>>(),
            Offset: offset,
            Limit: limit,
            TotalRows: 0,
            Mode: "client");

    // -------------------------------------------------------------------
    // B1 mutation methods (deterministic stubs for workspace tests).
    // -------------------------------------------------------------------

    public Dictionary<string, EditSchemaResult> EditSchemaResults { get; } =
        new(StringComparer.Ordinal);

    public List<string> UpdateCellCalls { get; } = new();
    public List<string> InsertRowCalls { get; } = new();
    public List<string> DeleteRowsCalls { get; } = new();

    /// <summary>When set, the next UpdateCell call throws this exception
    /// (used to exercise the error-mapping path).</summary>
    public Exception? NextUpdateCellException { get; set; }

    public Task<EditSchemaResult> GetEditSchemaAsync(string table, CancellationToken token)
    {
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
        CancellationToken token)
    {
        UpdateCellCalls.Add(table);
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
    // B3 query/state methods (deterministic stubs for workspace/coordinator
    // tests).
    // -------------------------------------------------------------------

    public List<string> QueryTablePageCalls { get; } = new();
    public Dictionary<string, TablePage> QueryTablePageResults { get; } =
        new(StringComparer.Ordinal);

    public List<string> ValidateSnapshotCalls { get; } = new();
    public SnapshotValidation? NextValidateSnapshotResult { get; set; }

    public Dictionary<string, GridStateResult> GridStateResults { get; } =
        new(StringComparer.Ordinal);
    public List<string> GetGridStateCalls { get; } = new();
    public List<string> SaveGridStateCalls { get; } = new();
    public GridStateResult? NextSaveGridStateResult { get; set; }

    public Task<TablePage> QueryTablePageAsync(
        string table, int offset, int limit, TableQuery query, CancellationToken token)
    {
        QueryTablePageCalls.Add(table);
        if (QueryTablePageResults.TryGetValue(table, out var page))
        {
            return Task.FromResult(page);
        }
        // Fall back to the plain page fixture so query reads work without extra
        // setup when the test only cares about the call being made.
        return ReadTablePageAsync(table, offset, limit, token);
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
