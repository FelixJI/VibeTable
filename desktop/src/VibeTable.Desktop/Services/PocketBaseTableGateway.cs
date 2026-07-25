using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using System.Text.Json;
using System.Text.RegularExpressions;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Adapts the established workspace use cases to the closed PocketBase product
/// RPC surface. The adapter never accepts an endpoint, credential, provider
/// collection type, or arbitrary backend method from the renderer.
/// </summary>
public sealed class PocketBaseTableGateway : ITableRpcGateway, IDisposable
{
    private static readonly Regex RevisionPattern =
        new("^data_([0-9]+)$", RegexOptions.CultureInvariant);
    private static readonly JsonSerializerOptions JsonOptions =
        new(JsonSerializerDefaults.Web);

    private readonly IProductDataRpcGateway _product;
    private readonly IWorkspaceSupportRpcGateway _localState;
    private readonly ConcurrentDictionary<string, JsonElement> _schemas = new();
    private bool _disposed;

    public PocketBaseTableGateway(
        IProductDataRpcGateway product,
        IWorkspaceSupportRpcGateway localState)
    {
        _product = product ?? throw new ArgumentNullException(nameof(product));
        _localState = localState ?? throw new ArgumentNullException(nameof(localState));
    }

    public async Task<DatabaseOpenResult> OpenDatabaseAsync(
        string path,
        CancellationToken token)
    {
        // Preserve the established open-order contract: identifier aliases
        // must be reconciled against the authoritative schema before the
        // collection list is exposed to the renderer.  Keeping this inside
        // the provider-neutral gateway also prevents a second, racy reconcile
        // later in MainWindow.
        await _product.ReconcileIdentifierMappingsAsync(
            JsonSerializer.SerializeToElement(
                new Dictionary<string, object?>(),
                JsonOptions),
            token).ConfigureAwait(false);
        var summary = await ReadTableSummaryAsync(token).ConfigureAwait(false);
        return new DatabaseOpenResult(
            summary.Tables,
            summary.Views,
            DisplayNames: summary.DisplayNames);
    }

    public Task<TableSummary> ListTablesAsync(CancellationToken token)
        => ReadTableSummaryAsync(token);

    public Task<TablePage> ReadTablePageAsync(
        string table,
        int offset,
        int limit,
        CancellationToken token)
        => QueryTablePageAsync(
            table,
            offset,
            limit,
            new TableQuery(Offset: offset, Limit: limit),
            token);

    public async Task<EditSchemaResult> GetEditSchemaAsync(
        string table,
        CancellationToken token)
    {
        JsonElement schema = await GetSchemaAsync(table, token).ConfigureAwait(false);
        string schemaRevision = RequiredString(schema, "schemaRevision");
        var columns = ReadColumns(schema);
        string primaryKey = FindPrimaryKey(schema);
        var edits = columns.Select(column =>
        {
            bool hasField = TryFindField(schema, column.FieldId!, out JsonElement field);
            var editor = hasField
                ? BuildProductEditor(field, column.DataType)
                : new Dictionary<string, object?> { ["kind"] = EditorKind(column.DataType) };
            var constraints = hasField
                && field.TryGetProperty("constraints", out var constraintsElement)
                ? ToDictionaryList(constraintsElement)
                : Array.Empty<IReadOnlyDictionary<string, object?>>();
            return new ColumnEditSchema(
                column.Name,
                column.Name,
                column.DataType,
                column.Editable,
                column.Nullable,
                string.Equals(column.Name, primaryKey, StringComparison.Ordinal),
                editor,
                constraints);
        }).ToArray();
        return new EditSchemaResult(
            table,
            schemaRevision,
            "pocketbase-record-id",
            true,
            edits.Any(column => column.Editable),
            edits);
    }

    public async Task<UpdateCellResult> UpdateCellAsync(
        string table,
        object rowKey,
        string column,
        object? oldValue,
        object? newValue,
        string schemaRevision,
        CancellationToken token,
        string? expectedDigest = null)
    {
        string recordId = RowId(rowKey);
        IReadOnlyList<IReadOnlyDictionary<string, object?>> before =
            await ReadRowsInternalAsync(table, new[] { recordId }, schemaRevision, token)
                .ConfigureAwait(false);
        if (before.Count != 1
            || !before[0].TryGetValue(column, out object? actual)
            || !JsonEquivalent(actual, oldValue))
        {
            throw new TableEditConflictException(
                "The row changed before the edit could be applied.");
        }
        string digestGuard = expectedDigest ?? RequiredRowDigest(before[0]);

        JsonElement receipt = await ApplyAsync(
            table,
            schemaRevision,
            new object[]
            {
                new Dictionary<string, object?>
                {
                    ["kind"] = "update",
                    ["recordId"] = recordId,
                    ["values"] = new Dictionary<string, object?> { [column] = newValue },
                    ["expectedDigest"] = digestGuard,
                },
            },
            token).ConfigureAwait(false);
        var rows = await ReadRowsInternalAsync(
            table,
            new[] { recordId },
            schemaRevision,
            token).ConfigureAwait(false);
        if (rows.Count != 1)
        {
            throw new InvalidOperationException("PocketBase omitted the updated row.");
        }
        rows[0].TryGetValue(column, out object? storedValue);
        return new UpdateCellResult(
            rowKey,
            column,
            storedValue,
            rows[0],
            Revision(schemaRevision, receipt));
    }

    public async Task<InsertRowResult> InsertRowAsync(
        string table,
        IReadOnlyDictionary<string, object?> values,
        string schemaRevision,
        CancellationToken token)
    {
        JsonElement schema = await GetSchemaAsync(table, token).ConfigureAwait(false);
        if (!string.Equals(
            RequiredString(schema, "schemaRevision"),
            schemaRevision,
            StringComparison.Ordinal))
        {
            throw new InvalidOperationException("The table schema changed.");
        }
        string recordId = values.TryGetValue("id", out object? requestedId)
            ? RowId(requestedId!)
            : Guid.NewGuid().ToString("N", CultureInfo.InvariantCulture)[..15];
        if (!IsPocketBaseRecordId(recordId))
        {
            throw new InvalidOperationException("The requested record id is invalid.");
        }
        var writable = schema.GetProperty("fields")
            .EnumerateArray()
            .Where(field => !RequiredBoolean(field, "readOnly"))
            .Select(field => RequiredString(field, "physicalName"))
            .ToHashSet(StringComparer.Ordinal);
        var sanitizedValues = new Dictionary<string, object?>(StringComparer.Ordinal);
        foreach ((string name, object? value) in values)
        {
            if (name is "id" or "rowKey" or "__vibetableDigest")
            {
                continue;
            }
            if (!writable.Contains(name))
            {
                throw new InvalidOperationException(
                    $"Insert contains unknown or read-only field '{name}'.");
            }
            sanitizedValues[name] = value;
        }
        JsonElement receipt = await ApplyAsync(
            table,
            schemaRevision,
            new object[]
            {
                new Dictionary<string, object?>
                {
                    ["kind"] = "insert",
                    ["recordId"] = recordId,
                    ["values"] = sanitizedValues,
                },
            },
            token).ConfigureAwait(false);
        var rows = await ReadRowsInternalAsync(
            table,
            new[] { recordId },
            schemaRevision,
            token).ConfigureAwait(false);
        if (rows.Count != 1)
        {
            throw new InvalidOperationException("PocketBase omitted the inserted row.");
        }
        return new InsertRowResult(
            recordId,
            rows[0],
            Revision(schemaRevision, receipt));
    }

    public async Task<DeleteRowsResult> DeleteRowsAsync(
        string table,
        IReadOnlyList<(object RowKey, string ExpectedDigest)> rows,
        string schemaRevision,
        CancellationToken token)
    {
        JsonElement schema = await GetSchemaAsync(table, token).ConfigureAwait(false);
        if (!string.Equals(
            RequiredString(schema, "schemaRevision"),
            schemaRevision,
            StringComparison.Ordinal))
        {
            throw new InvalidOperationException("The table schema changed.");
        }
        string operationKind = ArchiveOperationKind(schema);
        object[] operations = rows.Select(row =>
        {
            if (!IsProductDigest(row.ExpectedDigest))
            {
                throw new InvalidOperationException(
                    "Delete requires the authoritative row digest.");
            }
            return (object)new Dictionary<string, object?>
            {
                ["kind"] = operationKind,
                ["recordId"] = RowId(row.RowKey),
                ["expectedDigest"] = row.ExpectedDigest,
            };
        }).ToArray();
        JsonElement receipt = await ApplyAsync(
            table,
            schemaRevision,
            operations,
            token).ConfigureAwait(false);
        return new DeleteRowsResult(
            rows.Select(row => row.RowKey).ToArray(),
            Revision(schemaRevision, receipt));
    }

    public async Task<ReadRowsResult> ReadRowsAsync(
        string table,
        IReadOnlyList<object> rowKeys,
        CancellationToken token)
    {
        JsonElement schema = await GetSchemaAsync(table, token).ConfigureAwait(false);
        string schemaRevision = RequiredString(schema, "schemaRevision");
        var rows = await ReadRowsInternalAsync(
            table,
            rowKeys.Select(RowId).ToArray(),
            schemaRevision,
            token).ConfigureAwait(false);
        return new ReadRowsResult(rows, new MutationRevision(
            "pocketbase",
            schemaRevision,
            0));
    }

    public Task<HistoryPage> ReadChangeSetsAsync(
        ReadChangeSetsParams parameters,
        CancellationToken token)
        => _product.ReadHistoryAsync(parameters, token);

    public Task<RestorePreview> PreviewRestoreAsync(
        PreviewRestoreParams parameters,
        CancellationToken token)
        => _product.PreviewHistoryRestoreAsync(parameters, token);

    public Task<RestoreResult> ApplyRestoreAsync(
        ApplyRestoreParams parameters,
        CancellationToken token)
        => _product.ApplyHistoryRestoreAsync(parameters, token);

    public async Task<TablePage> QueryTablePageAsync(
        string table,
        int offset,
        int limit,
        TableQuery query,
        CancellationToken token)
    {
        JsonElement schema = await GetSchemaAsync(
            table,
            token,
            refresh: offset == 0).ConfigureAwait(false);
        var normalizedQuery = QueryBody(query, offset, limit);
        JsonElement request = JsonSerializer.SerializeToElement(
            new Dictionary<string, object?>
            {
                ["tableId"] = table,
                ["query"] = normalizedQuery,
            },
            JsonOptions);
        for (int attempt = 0; attempt < 2; attempt++)
        {
            JsonElement response = await _product.QueryPageAsync(
                request,
                token).ConfigureAwait(false);
            QuerySnapshot snapshot = ReadQuerySnapshot(
                RequiredProperty(response, "snapshot"));
            string schemaRevision = RequiredString(schema, "schemaRevision");
            if (!string.Equals(
                snapshot.SchemaRevision,
                schemaRevision,
                StringComparison.Ordinal))
            {
                if (attempt == 0)
                {
                    schema = await GetSchemaAsync(
                        table,
                        token,
                        refresh: true).ConfigureAwait(false);
                    continue;
                }
                throw new InvalidOperationException(
                    "The table schema changed while the page was loading.");
            }

            int total = RequiredInt(response, "totalRows");
            int filtered = RequiredInt(response, "filteredRows");
            var rows = ReadRows(response.GetProperty("rows"), FindPrimaryKey(schema));
            return new TablePage(
                table,
                ReadColumns(schema),
                rows,
                RequiredInt(response, "offset"),
                RequiredInt(response, "limit"),
                total,
                total <= TableWorkspaceLimits.ClientRowBudget ? "client" : "remote",
                filtered,
                snapshot,
                new MutationRevision(
                    "pocketbase",
                    schemaRevision,
                    snapshot.DataRevision));
        }

        throw new InvalidOperationException(
            "The table schema changed while the page was loading.");
    }

    public async Task<SnapshotValidation> ValidateSnapshotAsync(
        QuerySnapshot snapshot,
        int? currentRevision,
        CancellationToken token)
    {
        JsonElement response = await _product.ValidateSnapshotAsync(
            JsonSerializer.SerializeToElement(
                new Dictionary<string, object?> { ["snapshot"] = snapshot },
                JsonOptions),
            token).ConfigureAwait(false);
        return ReadSnapshotValidation(response);
    }

    public Task<GridStateResult> GetGridStateAsync(
        string databaseId,
        string table,
        CancellationToken token)
        => _localState.GetGridStateAsync(databaseId, table, token);

    public Task<GridStateResult> SaveGridStateAsync(
        string databaseId,
        string table,
        GridState state,
        string? revision,
        CancellationToken token)
        => _localState.SaveGridStateAsync(databaseId, table, state, revision, token);

    public Task<PastePlan> PreviewPasteAsync(
        string collection,
        string schemaRevision,
        IReadOnlyDictionary<string, object?> selection,
        PasteStartCell startCell,
        IReadOnlyList<IReadOnlyList<PasteCell>> cells,
        CancellationToken token)
        => _localState.PreviewPasteAsync(
            collection,
            schemaRevision,
            selection,
            startCell,
            cells,
            token);

    public Task<ApplyPasteResult> ApplyPasteAsync(
        string collection,
        string token,
        string idempotencyKey,
        CancellationToken cancellationToken)
        => _localState.ApplyPasteAsync(
            collection,
            token,
            idempotencyKey,
            cancellationToken);

    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true;
        _product.Dispose();
    }

    private async Task<TableSummary> ReadTableSummaryAsync(CancellationToken token)
    {
        ThrowIfDisposed();
        JsonElement response = await _product.ListTablesAsync(
            JsonSerializer.SerializeToElement(new Dictionary<string, object?>(), JsonOptions),
            token).ConfigureAwait(false);
        if (!response.TryGetProperty("tables", out var values)
            || values.ValueKind != JsonValueKind.Array)
        {
            throw new InvalidOperationException("PocketBase returned an invalid table catalog.");
        }
        var tables = new List<string>();
        var views = new List<string>();
        var displayNames = new Dictionary<string, string>(StringComparer.Ordinal);
        foreach (JsonElement item in values.EnumerateArray())
        {
            string tableId = RequiredString(item, "tableId");
            string kind = RequiredString(item, "kind");
            (kind == "view" ? views : tables).Add(tableId);
            displayNames[tableId] = RequiredString(item, "displayName");
        }
        return new TableSummary(tables, views, displayNames);
    }

    private async Task<JsonElement> GetSchemaAsync(
        string table,
        CancellationToken token,
        bool refresh = false)
    {
        ThrowIfDisposed();
        if (!refresh && _schemas.TryGetValue(table, out JsonElement cached))
        {
            return cached;
        }
        JsonElement schema = await _product.GetTableSchemaAsync(
            JsonSerializer.SerializeToElement(
                new Dictionary<string, object?> { ["tableId"] = table },
                JsonOptions),
            token).ConfigureAwait(false);
        schema = schema.Clone();
        _schemas[table] = schema;
        return schema;
    }

    private async Task<IReadOnlyList<IReadOnlyDictionary<string, object?>>> ReadRowsInternalAsync(
        string table,
        IReadOnlyList<string> rowIds,
        string schemaRevision,
        CancellationToken token)
    {
        JsonElement schema = await GetSchemaAsync(table, token).ConfigureAwait(false);
        if (!string.Equals(
            RequiredString(schema, "schemaRevision"),
            schemaRevision,
            StringComparison.Ordinal))
        {
            throw new InvalidOperationException("The table schema changed.");
        }
        JsonElement response = await _product.ReadRowsAsync(
            JsonSerializer.SerializeToElement(
                new Dictionary<string, object?>
                {
                    ["tableId"] = table,
                    ["rowIds"] = rowIds,
                },
                JsonOptions),
            token).ConfigureAwait(false);
        return ReadRows(response.GetProperty("rows"), FindPrimaryKey(schema))
            .Cast<IReadOnlyDictionary<string, object?>>()
            .ToArray();
    }

    private async Task<JsonElement> ApplyAsync(
        string table,
        string schemaRevision,
        IReadOnlyList<object> operations,
        CancellationToken token)
    {
        string requestId = Guid.NewGuid().ToString("N", CultureInfo.InvariantCulture);
        var request = new Dictionary<string, object?>
        {
            ["contractVersion"] = "1.0",
            ["requestId"] = requestId,
            ["idempotencyKey"] = requestId,
            ["tableId"] = table,
            ["schemaRevision"] = schemaRevision,
            ["operations"] = operations,
            ["actor"] = new Dictionary<string, object?>
            {
                ["type"] = "user",
                ["id"] = "local-user",
                ["displayName"] = null,
            },
            ["expectedRevision"] = null,
            ["expectedDigest"] = null,
        };
        return await _product.ApplyMutationAsync(
            JsonSerializer.SerializeToElement(request, JsonOptions),
            token).ConfigureAwait(false);
    }

    private static IReadOnlyList<ColumnSchema> ReadColumns(JsonElement schema)
    {
        if (!schema.TryGetProperty("fields", out var fields)
            || fields.ValueKind != JsonValueKind.Array)
        {
            throw new InvalidOperationException("PocketBase returned an invalid field catalog.");
        }
        var result = new List<ColumnSchema>();
        string tableId = RequiredString(schema, "tableId");
        bool hasRecordId = false;
        foreach (JsonElement field in fields.EnumerateArray())
        {
            string fieldId = RequiredString(field, "fieldId");
            hasRecordId |= fieldId == "id"
                || RequiredString(field, "physicalName") == "id";
            string kind = RequiredString(field, "kind");
            string dataType = GridDataType(field);
            int? precision = null;
            int? scale = null;
            JsonElement constraints = RequiredProperty(field, "constraints");
            if (constraints.ValueKind != JsonValueKind.Array)
            {
                throw new InvalidOperationException(
                    "PocketBase returned invalid field constraints.");
            }
            foreach (JsonElement constraint in constraints.EnumerateArray())
            {
                if (constraint.ValueKind != JsonValueKind.Object)
                {
                    throw new InvalidOperationException(
                        "PocketBase returned invalid field constraint.");
                }
                if (constraint.TryGetProperty("kind", out var constraintKind)
                    && constraintKind.ValueKind == JsonValueKind.String
                    && constraintKind.GetString() == "precisionScale")
                {
                    precision = RequiredInt(constraint, "precision");
                    scale = RequiredInt(constraint, "scale");
                }
            }
            result.Add(new ColumnSchema(
                RequiredString(field, "physicalName"),
                RequiredString(field, "displayName"),
                dataType,
                !RequiredBoolean(field, "readOnly"),
                RequiredBoolean(field, "nullable"),
                scale,
                precision,
                fieldId,
                kind,
                kind == "relation" ? $"{tableId}.{fieldId}" : null,
                kind == "lookup" ? $"{tableId}.{fieldId}" : null,
                kind == "attachment"
                    ? ReadAttachmentPolicy(field)
                    : null));
        }
        if (!hasRecordId)
        {
            result.Insert(0, new ColumnSchema(
                "id",
                "ID",
                "text",
                false,
                false,
                FieldId: "id",
                Kind: "scalar"));
        }
        return result;
    }

    private static string FindPrimaryKey(JsonElement schema)
    {
        foreach (JsonElement field in schema.GetProperty("fields").EnumerateArray())
        {
            string physical = RequiredString(field, "physicalName");
            if (physical == "id" || RequiredString(field, "fieldId") == "id")
            {
                return physical;
            }
        }
        // PocketBase's record id is a system field and is intentionally not
        // duplicated in the normalized product definition.
        return "id";
    }

    private static bool TryFindField(
        JsonElement schema,
        string fieldId,
        out JsonElement result)
    {
        foreach (JsonElement field in schema.GetProperty("fields").EnumerateArray())
        {
            if (RequiredString(field, "fieldId") == fieldId)
            {
                result = field;
                return true;
            }
        }
        result = default;
        return false;
    }

    private static string ArchiveOperationKind(JsonElement schema)
    {
        if (!schema.TryGetProperty("archivePolicy", out JsonElement policy)
            || policy.ValueKind != JsonValueKind.Object)
        {
            throw new InvalidOperationException(
                "PocketBase schema omitted 'archivePolicy'.");
        }
        return RequiredString(policy, "mode") switch
        {
            "none" => "delete",
            "status" or "deletedAt" => "archive",
            _ => throw new InvalidOperationException(
                "PocketBase returned an invalid archive policy."),
        };
    }

    private static IReadOnlyDictionary<string, object?> ReadAttachmentPolicy(
        JsonElement field)
    {
        if (!field.TryGetProperty("attachmentPolicy", out JsonElement policy)
            || policy.ValueKind != JsonValueKind.Object)
        {
            throw new InvalidOperationException(
                "PocketBase attachment field omitted 'attachmentPolicy'.");
        }
        return ToDictionary(policy);
    }

    private static List<Dictionary<string, object?>> ReadRows(
        JsonElement rows,
        string primaryKey)
    {
        if (rows.ValueKind != JsonValueKind.Array)
        {
            throw new InvalidOperationException("PocketBase returned invalid rows.");
        }
        var result = new List<Dictionary<string, object?>>();
        foreach (JsonElement row in rows.EnumerateArray())
        {
            var converted = ToDictionary(row);
            if (!converted.TryGetValue(primaryKey, out object? rowKey) || rowKey is null)
            {
                throw new InvalidOperationException("PocketBase row omitted the stable record id.");
            }
            converted["rowKey"] = rowKey;
            result.Add(converted);
        }
        return result;
    }

    private static Dictionary<string, object?> QueryBody(
        TableQuery query,
        int offset,
        int limit)
    {
        var result = new Dictionary<string, object?>
        {
            ["keyword"] = query.Keyword ?? string.Empty,
            ["filters"] = query.Filters ?? Array.Empty<FilterCondition>(),
            ["sorts"] = query.Sorts ?? Array.Empty<SortCondition>(),
            ["offset"] = offset,
            ["limit"] = limit,
        };
        return result;
    }

    private static MutationRevision Revision(string schemaRevision, JsonElement receipt)
    {
        string value = RequiredString(receipt, "newRevision");
        Match match = RevisionPattern.Match(value);
        if (!match.Success
            || !int.TryParse(
                match.Groups[1].Value,
                NumberStyles.None,
                CultureInfo.InvariantCulture,
                out int dataRevision))
        {
            throw new InvalidOperationException(
                "PocketBase returned an invalid data revision.");
        }
        return new MutationRevision("pocketbase", schemaRevision, dataRevision);
    }

    private static string GridDataType(JsonElement field)
    {
        string value = RequiredString(field, "dataType");
        if (value == "formula")
        {
            value = RequiredString(RequiredProperty(field, "formula"), "resultType");
        }
        else if (value == "lookup")
        {
            value = RequiredString(field, "storageType");
        }
        return value switch
        {
            "shortText" or "longText" or "richText"
                or "email" or "url" or "uuid" or "select"
                or "multiSelect" or "list" or "hash" or "secret"
                or "text" or "editor" => "text",
            "integer" => "integer",
            "float" or "decimal" or "number" => "decimal",
            "boolean" or "bool" => "boolean",
            "date" => "date",
            "dateTime" or "autoDate" or "autodate" => "datetime",
            "time" => "time",
            "json" or "geoPoint" or "geoJson" => "json",
            "file" or "relation" => "text",
            _ => throw new InvalidOperationException(
                $"PocketBase returned unknown data type '{value}'."),
        };
    }

    private static string EditorKind(string dataType) => dataType switch
    {
        "integer" or "decimal" => "number",
        "boolean" => "boolean",
        "date" or "datetime" or "time" => "date",
        "json" => "json",
        _ => "text",
    };

    private static Dictionary<string, object?> BuildProductEditor(
        JsonElement field,
        string rendererDataType)
    {
        string productDataType = RequiredString(field, "dataType");
        var editor = new Dictionary<string, object?>(StringComparer.Ordinal);
        switch (productDataType)
        {
            case "multiSelect":
                editor["kind"] = "multi_select";
                editor["options"] = ReadEnumOptions(field);
                editor["allowCustom"] = false;
                break;
            case "select":
                editor["kind"] = "single_select";
                editor["options"] = ReadEnumOptions(field);
                editor["allowCustom"] = false;
                break;
            case "json":
            case "geoJson":
            case "geoPoint":
            case "list":
                editor["kind"] = "json";
                editor["schema"] = ReadJsonSchema(field);
                break;
            case "integer":
            case "float":
            case "decimal":
                editor["kind"] = "number";
                editor["storage"] = productDataType == "integer" ? "integer" : "decimal";
                AddNumericEditorLimits(editor, field);
                break;
            case "boolean":
                editor["kind"] = "boolean";
                break;
            case "date":
            case "dateTime":
            case "time":
            case "autoDate":
                editor["kind"] = "date";
                editor["dateType"] = productDataType switch
                {
                    "date" => "date",
                    "time" => "time",
                    _ => "datetime",
                };
                break;
            case "formula":
            case "lookup":
                AddRendererEditor(editor, rendererDataType);
                break;
            default:
                editor["kind"] = EditorKind(rendererDataType);
                editor["multiline"] = productDataType is "longText" or "richText";
                break;
        }
        return editor;
    }

    private static void AddRendererEditor(
        IDictionary<string, object?> editor,
        string rendererDataType)
    {
        string kind = EditorKind(rendererDataType);
        editor["kind"] = kind;
        if (kind == "number")
        {
            editor["storage"] = rendererDataType == "integer"
                ? "integer"
                : "decimal";
        }
        else if (kind == "date")
        {
            editor["dateType"] = rendererDataType;
        }
    }

    private static object?[] ReadEnumOptions(JsonElement field)
    {
        foreach (JsonElement constraint in field.GetProperty("constraints").EnumerateArray())
        {
            if (RequiredString(constraint, "kind") != "enum"
                || !constraint.TryGetProperty("options", out JsonElement options)
                || options.ValueKind != JsonValueKind.Array)
            {
                continue;
            }
            return options.EnumerateArray()
                .Where(option => option.TryGetProperty("value", out _))
                .Select(option => ToObject(option.GetProperty("value")))
                .ToArray();
        }
        return Array.Empty<object?>();
    }

    private static object? ReadJsonSchema(JsonElement field)
    {
        foreach (JsonElement constraint in field.GetProperty("constraints").EnumerateArray())
        {
            if (RequiredString(constraint, "kind") == "jsonSchema"
                && constraint.TryGetProperty("schema", out JsonElement schema))
            {
                return ToObject(schema);
            }
        }
        return null;
    }

    private static void AddNumericEditorLimits(
        IDictionary<string, object?> editor,
        JsonElement field)
    {
        foreach (JsonElement constraint in field.GetProperty("constraints").EnumerateArray())
        {
            switch (RequiredString(constraint, "kind"))
            {
                case "precisionScale":
                    editor["precision"] = ToObject(constraint.GetProperty("precision"));
                    editor["scale"] = ToObject(constraint.GetProperty("scale"));
                    break;
                case "range":
                    editor["minValue"] = ToObject(constraint.GetProperty("min"));
                    editor["maxValue"] = ToObject(constraint.GetProperty("max"));
                    break;
            }
        }
    }

    private static string RowId(object value)
        => Convert.ToString(value, CultureInfo.InvariantCulture)
            ?? throw new ArgumentException("Row id cannot be null.", nameof(value));

    private static string RequiredString(JsonElement value, string name)
    {
        if (!value.TryGetProperty(name, out JsonElement property)
            || property.ValueKind != JsonValueKind.String
            || string.IsNullOrWhiteSpace(property.GetString()))
        {
            throw new InvalidOperationException($"PocketBase response omitted '{name}'.");
        }
        return property.GetString()!;
    }

    private static int RequiredInt(JsonElement value, string name)
    {
        if (!value.TryGetProperty(name, out JsonElement property)
            || !property.TryGetInt32(out int result))
        {
            throw new InvalidOperationException($"PocketBase response omitted '{name}'.");
        }
        return result;
    }

    private static JsonElement RequiredProperty(JsonElement value, string name)
    {
        if (value.ValueKind != JsonValueKind.Object
            || !value.TryGetProperty(name, out JsonElement property))
        {
            throw new InvalidOperationException(
                $"PocketBase response omitted '{name}'.");
        }
        return property;
    }

    private static bool RequiredBoolean(JsonElement value, string name)
    {
        if (!value.TryGetProperty(name, out JsonElement property)
            || property.ValueKind is not (
                JsonValueKind.True or JsonValueKind.False))
        {
            throw new InvalidOperationException(
                $"PocketBase response omitted '{name}'.");
        }
        return property.GetBoolean();
    }

    private static int? OptionalInt(JsonElement value, string name)
        => value.TryGetProperty(name, out JsonElement property)
            && property.TryGetInt32(out int result)
                ? result
                : null;

    private static QuerySnapshot ReadQuerySnapshot(JsonElement value)
    {
        if (value.ValueKind != JsonValueKind.Object)
        {
            throw new InvalidOperationException(
                "PocketBase returned an invalid query snapshot.");
        }
        string snapshotId = RequiredString(value, "snapshotId");
        string digest = RequiredString(value, "digest");
        string databaseId = RequiredString(value, "databaseId");
        string table = RequiredString(value, "table");
        string schemaRevision = RequiredString(value, "schemaRevision");
        int dataRevision = RequiredInt(value, "dataRevision");
        JsonElement query = RequiredProperty(value, "normalizedQuery");
        if (snapshotId.Length != 32
            || snapshotId.Any(character => !Uri.IsHexDigit(character))
            || digest.Length != 64
            || digest.Any(character => !Uri.IsHexDigit(character))
            || dataRevision < 0
            || query.ValueKind != JsonValueKind.Object
            || RequiredInt(query, "offset") < 0
            || RequiredInt(query, "limit") < 1)
        {
            throw new InvalidOperationException(
                "PocketBase returned an invalid query snapshot.");
        }
        return new QuerySnapshot(
            snapshotId,
            digest,
            databaseId,
            table,
            schemaRevision,
            dataRevision,
            ToDictionary(query));
    }

    private static SnapshotValidation ReadSnapshotValidation(JsonElement value)
    {
        bool valid = RequiredBoolean(value, "valid");
        int currentDataRevision = RequiredInt(value, "currentDataRevision");
        string currentSchemaRevision =
            RequiredString(value, "currentSchemaRevision");
        string? reason = null;
        if (value.TryGetProperty("reason", out JsonElement reasonValue))
        {
            if (reasonValue.ValueKind == JsonValueKind.String)
            {
                reason = reasonValue.GetString();
            }
            else if (reasonValue.ValueKind != JsonValueKind.Null)
            {
                throw new InvalidOperationException(
                    "PocketBase returned invalid snapshot validation.");
            }
        }
        if (currentDataRevision < 0
            || (valid && !string.IsNullOrEmpty(reason))
            || (!valid && reason is not (
                "query_changed" or "schema_changed" or "application_write")))
        {
            throw new InvalidOperationException(
                "PocketBase returned invalid snapshot validation.");
        }
        return new SnapshotValidation(
            valid,
            reason,
            currentDataRevision,
            currentSchemaRevision);
    }

    private static Dictionary<string, object?> ToDictionary(JsonElement value)
    {
        if (value.ValueKind != JsonValueKind.Object)
        {
            throw new InvalidOperationException("PocketBase response expected an object.");
        }
        return value.EnumerateObject().ToDictionary(
            property => property.Name,
            property => ToObject(property.Value),
            StringComparer.Ordinal);
    }

    private static IReadOnlyList<IReadOnlyDictionary<string, object?>> ToDictionaryList(
        JsonElement value)
    {
        if (value.ValueKind != JsonValueKind.Array)
        {
            return Array.Empty<IReadOnlyDictionary<string, object?>>();
        }
        return value.EnumerateArray()
            .Select(item => (IReadOnlyDictionary<string, object?>)ToDictionary(item))
            .ToArray();
    }

    private static object? ToObject(JsonElement value) => value.ValueKind switch
    {
        JsonValueKind.Null or JsonValueKind.Undefined => null,
        JsonValueKind.String => value.GetString(),
        JsonValueKind.True => true,
        JsonValueKind.False => false,
        JsonValueKind.Number when value.TryGetInt64(out long integer) => integer,
        JsonValueKind.Number => value.GetDouble(),
        JsonValueKind.Object => ToDictionary(value),
        JsonValueKind.Array => value.EnumerateArray().Select(ToObject).ToArray(),
        _ => value.Clone(),
    };

    private static bool JsonEquivalent(object? left, object? right)
        => JsonEquivalent(
            JsonSerializer.SerializeToElement(left, JsonOptions),
            JsonSerializer.SerializeToElement(right, JsonOptions));

    private static bool JsonEquivalent(JsonElement left, JsonElement right)
    {
        if (left.ValueKind != right.ValueKind)
        {
            return false;
        }
        return left.ValueKind switch
        {
            JsonValueKind.Object => JsonObjectsEquivalent(left, right),
            JsonValueKind.Array => left.GetArrayLength() == right.GetArrayLength()
                && left.EnumerateArray()
                    .Zip(right.EnumerateArray(), JsonEquivalent)
                    .All(equivalent => equivalent),
            JsonValueKind.String => string.Equals(
                left.GetString(),
                right.GetString(),
                StringComparison.Ordinal),
            JsonValueKind.Number => JsonNumbersEquivalent(left, right),
            JsonValueKind.True or JsonValueKind.False => left.GetBoolean() == right.GetBoolean(),
            JsonValueKind.Null or JsonValueKind.Undefined => true,
            _ => false,
        };
    }

    private static bool JsonObjectsEquivalent(JsonElement left, JsonElement right)
    {
        var leftProperties = left.EnumerateObject().ToArray();
        var rightProperties = right.EnumerateObject().ToArray();
        if (leftProperties.Length != rightProperties.Length)
        {
            return false;
        }
        return leftProperties.All(property =>
            right.TryGetProperty(property.Name, out JsonElement rightValue)
            && JsonEquivalent(property.Value, rightValue));
    }

    private static bool JsonNumbersEquivalent(JsonElement left, JsonElement right)
    {
        if (left.TryGetDecimal(out decimal leftDecimal)
            && right.TryGetDecimal(out decimal rightDecimal))
        {
            return leftDecimal == rightDecimal;
        }
        return left.GetDouble().Equals(right.GetDouble());
    }

    private static bool IsProductDigest(string? value)
        => value is { Length: 71 }
            && value.StartsWith("sha256:", StringComparison.Ordinal)
            && value.AsSpan(7).IndexOfAnyExcept(
                "0123456789abcdef".AsSpan()) < 0;

    private static string RequiredRowDigest(
        IReadOnlyDictionary<string, object?> row)
    {
        if (!row.TryGetValue("__vibetableDigest", out object? value)
            || value is not string digest
            || !IsProductDigest(digest))
        {
            throw new InvalidOperationException(
                "PocketBase row omitted its authoritative digest.");
        }
        return digest;
    }

    private static bool IsPocketBaseRecordId(string value)
        => value.Length == 15
            && value.All(character =>
                character is >= 'a' and <= 'z'
                or >= '0' and <= '9');

    private void ThrowIfDisposed() => ObjectDisposedException.ThrowIf(_disposed, this);
}
