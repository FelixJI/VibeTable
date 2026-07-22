using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Linq;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Adapts the established A/B1/B3 table workspace to Directus collections.
/// Business data always uses Directus; backend RPC is limited to per-user
/// grid state and brokered atomic paste operations.
/// </summary>
public sealed class DirectusTableGateway : ITableRpcGateway, IDisposable
{
    private readonly IDirectusRpcGateway _directus;
    private readonly IWorkspaceSupportRpcGateway _localState;
    private readonly ConcurrentDictionary<string, DirectusSchema> _schemas = new();

    public DirectusTableGateway(
        IDirectusRpcGateway directus,
        IWorkspaceSupportRpcGateway localState)
    {
        _directus = directus ?? throw new ArgumentNullException(nameof(directus));
        _localState = localState ?? throw new ArgumentNullException(nameof(localState));
    }

    public async Task<DatabaseOpenResult> OpenDatabaseAsync(
        string path, CancellationToken token)
    {
        var result = await _directus.ListCollectionsAsync(token);
        return new DatabaseOpenResult(
            result.Collections, Array.Empty<string>(), DisplayNames: result.DisplayNames);
    }

    public async Task<TableSummary> ListTablesAsync(CancellationToken token)
    {
        var result = await _directus.ListCollectionsAsync(token);
        return new TableSummary(result.Collections, Array.Empty<string>(), result.DisplayNames);
    }

    public Task<TablePage> ReadTablePageAsync(
        string table, int offset, int limit, CancellationToken token)
        => QueryTablePageAsync(table, offset, limit, new TableQuery(Offset: offset, Limit: limit), token);

    public async Task<EditSchemaResult> GetEditSchemaAsync(
        string table, CancellationToken token)
    {
        var schema = await GetSchemaCachedAsync(table, token);
        var columns = schema.Columns.Select(column => new ColumnEditSchema(
            column.Name,
            column.Name,
            column.DataType,
            column.Editable,
            column.Nullable,
            string.Equals(column.Name, schema.PrimaryKey, StringComparison.Ordinal),
            EditorFor(column),
            Array.Empty<IReadOnlyDictionary<string, object?>>())).ToArray();
        return new EditSchemaResult(
            table,
            schema.SchemaRevision,
            "directus-primary-key",
            true,
            columns.Any(column => column.Editable),
            columns);
    }

    public async Task<UpdateCellResult> UpdateCellAsync(
        string table, object rowKey, string column,
        object? oldValue, object? newValue, string schemaRevision,
        CancellationToken token)
    {
        var item = await _directus.UpdateAsync(
            table,
            Convert.ToString(rowKey, System.Globalization.CultureInfo.InvariantCulture) ?? string.Empty,
            new Dictionary<string, object?> { [column] = newValue },
            expectedDateUpdated: null,
            requestId: Guid.NewGuid().ToString("N"),
            token);
        var row = ToObjectDictionary(item.Item);
        row["rowKey"] = rowKey;
        row.TryGetValue(column, out object? stored);
        return new UpdateCellResult(
            rowKey,
            column,
            stored,
            row,
            Revision(schemaRevision));
    }

    public async Task<InsertRowResult> InsertRowAsync(
        string table, IReadOnlyDictionary<string, object?> values,
        string schemaRevision, CancellationToken token)
    {
        var item = await _directus.CreateAsync(
            table, values, Guid.NewGuid().ToString("N"), token);
        var schema = await GetSchemaCachedAsync(table, token);
        var row = ToObjectDictionary(item.Item);
        object rowKey = row.TryGetValue(schema.PrimaryKey, out object? value) && value is not null
            ? value
            : throw new InvalidOperationException("Directus create response omitted the primary key.");
        row["rowKey"] = rowKey;
        return new InsertRowResult(rowKey, row, Revision(schemaRevision));
    }

    public async Task<DeleteRowsResult> DeleteRowsAsync(
        string table, IReadOnlyList<(object RowKey, string ExpectedDigest)> rows,
        string schemaRevision, CancellationToken token)
    {
        var deleted = new List<object>(rows.Count);
        foreach (var row in rows)
        {
            string itemId = Convert.ToString(
                row.RowKey, System.Globalization.CultureInfo.InvariantCulture) ?? string.Empty;
            await _directus.ArchiveAsync(table, itemId, token);
            deleted.Add(row.RowKey);
        }
        return new DeleteRowsResult(deleted, Revision(schemaRevision));
    }

    public async Task<ReadRowsResult> ReadRowsAsync(
        string table, IReadOnlyList<object> rowKeys, CancellationToken token)
    {
        var schema = await GetSchemaCachedAsync(table, token);
        var query = new TableQuery(
            Filters: new[] { new FilterCondition(schema.PrimaryKey, FilterOperators.In, rowKeys) },
            Limit: Math.Max(1, rowKeys.Count));
        var page = await _directus.ReadAsync(table, query, includeArchived: true, token);
        var rows = page.Rows.Select(row =>
            (IReadOnlyDictionary<string, object?>)ToRow(row, schema.PrimaryKey)).ToArray();
        return new ReadRowsResult(rows, Revision(schema.SchemaRevision));
    }

    public Task<HistoryPage> ReadChangeSetsAsync(
        ReadChangeSetsParams parameters, CancellationToken token)
        => _directus.ReadChangeSetsAsync(parameters, token);

    public Task<RestorePreview> PreviewRestoreAsync(
        PreviewRestoreParams parameters, CancellationToken token)
        => _directus.PreviewRestoreAsync(parameters, token);

    public Task<RestoreResult> ApplyRestoreAsync(
        ApplyRestoreParams parameters, CancellationToken token)
        => _directus.ApplyRestoreAsync(parameters, token);

    public async Task<TablePage> QueryTablePageAsync(
        string table, int offset, int limit, TableQuery query, CancellationToken token)
    {
        // A first-page read is the refresh boundary used by table selection and
        // Ctrl+R. Directus Studio may have changed the collection schema since
        // the previous selection, so refresh the cache here. Later pages reuse
        // that freshly loaded schema to keep one revision across a paged read.
        var schema = await GetSchemaCachedAsync(table, token, refresh: offset == 0);
        query = query with { Offset = offset, Limit = limit };
        var page = await _directus.ReadAsync(table, query, includeArchived: false, token);
        int total = page.FilteredRows ?? page.TotalRows ?? page.Rows.Count;
        string mode = total <= TableWorkspaceLimits.ClientRowBudget ? "client" : "remote";
        var rows = page.Rows.Select(row => ToRow(row, schema.PrimaryKey)).ToArray();
        var snapshot = BuildQuerySnapshot(table, schema, query);
        return new TablePage(
            table,
            schema.Columns,
            rows,
            offset,
            limit,
            total,
            mode,
            FilteredRows: total,
            QuerySnapshot: snapshot,
            Revision: Revision(schema.SchemaRevision));
    }

    public async Task<SnapshotValidation> ValidateSnapshotAsync(
        QuerySnapshot snapshot, int? currentRevision, CancellationToken token)
    {
        var schema = await GetSchemaCachedAsync(snapshot.Table, token);
        bool valid = string.Equals(
            snapshot.SchemaRevision, schema.SchemaRevision, StringComparison.Ordinal);
        return new SnapshotValidation(
            valid,
            valid ? null : "Directus schema capability changed.",
            currentRevision,
            schema.SchemaRevision);
    }

    public Task<GridStateResult> GetGridStateAsync(
        string databaseId, string table, CancellationToken token)
        => _localState.GetGridStateAsync(databaseId, table, token);

    public Task<GridStateResult> SaveGridStateAsync(
        string databaseId, string table, GridState state,
        string? revision, CancellationToken token)
        => _localState.SaveGridStateAsync(databaseId, table, state, revision, token);

    // -------------------------------------------------------------------
    // B2: paste is served by the Python broker (table.previewPaste /
    // table.applyPaste), which internally drives the Directus data plane and
    // bulk-mutation endpoint. We forward verbatim — the gateway adds no
    // client-side batching.
    // -------------------------------------------------------------------

    public Task<PastePlan> PreviewPasteAsync(
        string collection, string schemaRevision,
        IReadOnlyDictionary<string, object?> selection,
        PasteStartCell startCell,
        IReadOnlyList<IReadOnlyList<PasteCell>> cells,
        CancellationToken token)
        => _localState.PreviewPasteAsync(
            collection, schemaRevision, selection, startCell, cells, token);

    public Task<ApplyPasteResult> ApplyPasteAsync(
        string collection, string token, string idempotencyKey,
        CancellationToken cancellationToken)
        => _localState.ApplyPasteAsync(collection, token, idempotencyKey, cancellationToken);

    public void Dispose() => _directus.Dispose();

    private async Task<DirectusSchema> GetSchemaCachedAsync(
        string collection, CancellationToken token, bool refresh = false)
    {
        if (!refresh && _schemas.TryGetValue(collection, out var cached))
        {
            return cached;
        }
        var schema = await _directus.GetSchemaAsync(collection, token);
        _schemas[collection] = schema;
        return schema;
    }

    private static MutationRevision Revision(string schemaRevision)
        => new("directus", schemaRevision, 0);

    private static QuerySnapshot BuildQuerySnapshot(
        string table, DirectusSchema schema, TableQuery query)
    {
        var normalized = new Dictionary<string, object?>
        {
            ["keyword"] = query.Keyword,
            ["filters"] = query.Filters ?? Array.Empty<FilterCondition>(),
            ["sorts"] = query.Sorts ?? Array.Empty<SortCondition>(),
            ["offset"] = query.Offset,
            ["limit"] = query.Limit,
        };
        string canonical = JsonSerializer.Serialize(
            normalized, new JsonSerializerOptions(JsonSerializerDefaults.Web));
        string digest = Convert.ToHexString(
            SHA256.HashData(Encoding.UTF8.GetBytes(canonical))).ToLowerInvariant();
        return new QuerySnapshot(
            $"directus:{table}:{digest[..16]}",
            digest,
            "directus",
            table,
            schema.SchemaRevision,
            0,
            normalized);
    }

    private static IReadOnlyDictionary<string, object?> EditorFor(ColumnSchema column)
    {
        var editor = new Dictionary<string, object?>
        {
            ["kind"] = column.DataType switch
            {
                "boolean" => "boolean",
                "integer" or "float" or "decimal" => "number",
                "date" or "datetime" or "time" => "date",
                _ => "text",
            },
        };
        if (column.DataType is "date" or "datetime" or "time")
        {
            editor["dateType"] = column.DataType;
        }
        if (column.DataType is "integer" or "float" or "decimal")
        {
            // Carry Directus numeric precision/scale so the web layer can drive
            // decimal display precision and block edits that exceed the column's
            // scale (preventing silent DB truncation). Integer columns report
            // scale=null upstream; treat them as integer storage.
            editor["storage"] = column.DataType == "integer" ? "integer" : "decimal";
            editor["scale"] = column.Scale;
            editor["precision"] = column.Precision;
        }
        return editor;
    }

    private static Dictionary<string, object?> ToRow(
        IReadOnlyDictionary<string, JsonElement> source, string primaryKey)
    {
        var row = ToObjectDictionary(source);
        if (!row.TryGetValue("rowKey", out object? rowKey))
        {
            row.TryGetValue(primaryKey, out rowKey);
            row["rowKey"] = rowKey;
        }
        return row;
    }

    private static Dictionary<string, object?> ToObjectDictionary(
        IReadOnlyDictionary<string, JsonElement> source)
        => source.ToDictionary(pair => pair.Key, pair => ToObject(pair.Value));

    private static object? ToObject(JsonElement value)
        => value.ValueKind switch
        {
            JsonValueKind.Null or JsonValueKind.Undefined => null,
            JsonValueKind.String => value.GetString(),
            JsonValueKind.True => true,
            JsonValueKind.False => false,
            JsonValueKind.Number when value.TryGetInt64(out long integer) => integer,
            JsonValueKind.Number => value.GetDouble(),
            _ => value.Clone(),
        };
}
