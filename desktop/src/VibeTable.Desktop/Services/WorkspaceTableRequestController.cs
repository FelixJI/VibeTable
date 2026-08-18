using System.Diagnostics;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Diagnostics;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Owns the renderer-authored table command lifecycle behind one closed
/// interface. The workspace dispatcher only selects this module; it does not
/// interpret row, paste, history, or schema payloads.
/// </summary>
public sealed class WorkspaceTableRequestController
{
    private static readonly TimeSpan RecoveryReadPollInterval =
        TimeSpan.FromMilliseconds(25);

    private readonly TableWorkspaceService _workspace;
    private readonly IDatabasePicker _picker;
    private readonly IWebReplySink _reply;
    private readonly Func<IProductDataRpcGateway?> _productGateway;
    private readonly TimeSpan _readRecoveryTimeout;
    private readonly TimeSpan _schemaLifecycleTimeout;
    private readonly Func<CancellationToken> _sessionToken;
    private readonly TimeProvider _timeProvider;

    public WorkspaceTableRequestController(
        TableWorkspaceService workspace,
        IDatabasePicker picker,
        IWebReplySink reply,
        Func<IProductDataRpcGateway?> productGateway,
        TimeSpan? readRecoveryTimeout = null,
        TimeSpan? schemaLifecycleTimeout = null,
        Func<CancellationToken>? sessionToken = null,
        TimeProvider? timeProvider = null)
    {
        _workspace = workspace ?? throw new ArgumentNullException(nameof(workspace));
        _picker = picker ?? throw new ArgumentNullException(nameof(picker));
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _productGateway = productGateway
            ?? throw new ArgumentNullException(nameof(productGateway));
        _readRecoveryTimeout = readRecoveryTimeout ?? TimeSpan.FromSeconds(3);
        _schemaLifecycleTimeout = schemaLifecycleTimeout
            ?? SchemaLifecycleBudget.DefaultTimeout;
        if (_schemaLifecycleTimeout <= TimeSpan.Zero)
            throw new ArgumentOutOfRangeException(nameof(schemaLifecycleTimeout));
        _sessionToken = sessionToken ?? (() => CancellationToken.None);
        _timeProvider = timeProvider ?? TimeProvider.System;
    }

    public static bool Handles(string requestType)
        => requestType is
            "database.openRequested" or
            "table.selected" or
            "table.updateCellRequested" or
            "table.insertRowRequested" or
            "table.deleteRowsRequested" or
            "table.previewPasteRequested" or
            "table.applyPasteRequested" or
            "history.queryRequested" or
            "history.previewRestoreRequested" or
            "history.applyRestoreRequested" or
            "tableAdmin.createRequested" or
            "tableAdmin.deleteRequested";

    public Task DispatchAsync(RoutedWebRequest request)
        => request.Type switch
        {
            "database.openRequested" => OpenDatabaseAsync(),
            "table.selected" => SelectTableAsync(request),
            "table.updateCellRequested" => UpdateCellAsync(request),
            "table.insertRowRequested" => InsertRowAsync(request),
            "table.deleteRowsRequested" => DeleteRowsAsync(request),
            "table.previewPasteRequested" => PreviewPasteAsync(request),
            "table.applyPasteRequested" => ApplyPasteAsync(request),
            "history.queryRequested" => QueryHistoryAsync(request),
            "history.previewRestoreRequested" => PreviewRestoreAsync(request),
            "history.applyRestoreRequested" => ApplyRestoreAsync(request),
            "tableAdmin.createRequested" => CreateTableAsync(request),
            "tableAdmin.deleteRequested" => DeleteTableAsync(request),
            _ => RejectUnknownAsync(request),
        };

    private async Task OpenDatabaseAsync()
    {
        string? path = await _picker.PickDatabaseAsync().ConfigureAwait(false);
        if (string.IsNullOrEmpty(path))
        {
            _reply.PostNotification("database.opened", new
            {
                tables = Array.Empty<string>(),
                views = Array.Empty<string>(),
                displayNames = new Dictionary<string, string>(),
            });
            return;
        }

        DatabaseOpenResult result = await _workspace.OpenDatabaseAsync(path)
            .ConfigureAwait(false);
        _reply.PostNotification("database.opened", new
        {
            tables = result.Tables,
            views = result.Views,
            displayNames = result.DisplayNames,
        });
    }

    private async Task SelectTableAsync(RoutedWebRequest request)
    {
        string? table = GetString(request.Payload, "table");
        if (string.IsNullOrEmpty(table))
        {
            Reject(
                request,
                "table.selected requires a non-empty 'table' payload field.");
            return;
        }
        await _workspace.SelectTableAsync(table).ConfigureAwait(false);
        await _workspace.GetEditSchemaAsync(table).ConfigureAwait(false);
    }

    private async Task UpdateCellAsync(RoutedWebRequest request)
    {
        string? table = GetString(request.Payload, "table");
        string? column = GetString(request.Payload, "column");
        string? schemaRevision = GetString(request.Payload, "schemaRevision");
        if (string.IsNullOrEmpty(table)
            || string.IsNullOrEmpty(column)
            || string.IsNullOrEmpty(schemaRevision)
            || !TryGetProperty(request.Payload, "rowKey", out JsonElement rowKey))
        {
            Reject(request, "Invalid update-cell payload.");
            return;
        }
        TryGetProperty(request.Payload, "oldValue", out JsonElement oldValue);
        TryGetProperty(request.Payload, "newValue", out JsonElement newValue);
        string? expectedDigest = GetString(request.Payload, "expectedDigest");
        if (expectedDigest is not null && !IsValidDigest(expectedDigest))
        {
            Reject(request, "Invalid update-cell digest guard.");
            return;
        }
        await _workspace.UpdateCellAsync(
            table,
            ToObject(rowKey)!,
            column,
            ToObject(oldValue),
            ToObject(newValue),
            schemaRevision,
            expectedDigest,
            request.RequestId).ConfigureAwait(false);
    }

    private async Task InsertRowAsync(RoutedWebRequest request)
    {
        string? table = GetString(request.Payload, "table");
        string? schemaRevision = GetString(request.Payload, "schemaRevision");
        if (string.IsNullOrEmpty(table)
            || string.IsNullOrEmpty(schemaRevision)
            || !TryGetProperty(request.Payload, "values", out JsonElement valuesElement)
            || valuesElement.ValueKind != JsonValueKind.Object)
        {
            Reject(request, "Invalid insert-row payload.");
            return;
        }
        Dictionary<string, object?> values = valuesElement.EnumerateObject()
            .ToDictionary(
                property => property.Name,
                property => ToObject(property.Value));
        await _workspace.InsertRowAsync(
            table,
            values,
            schemaRevision,
            request.RequestId).ConfigureAwait(false);
    }

    private async Task DeleteRowsAsync(RoutedWebRequest request)
    {
        string? table = GetString(request.Payload, "table");
        string? schemaRevision = GetString(request.Payload, "schemaRevision");
        if (string.IsNullOrEmpty(table)
            || string.IsNullOrEmpty(schemaRevision)
            || !TryGetProperty(request.Payload, "rows", out JsonElement rowsElement)
            || rowsElement.ValueKind != JsonValueKind.Array)
        {
            Reject(request, "Invalid delete-rows payload.");
            return;
        }
        var rows = new List<(object RowKey, string ExpectedDigest)>();
        foreach (JsonElement element in rowsElement.EnumerateArray())
        {
            string? expectedDigest = GetString(element, "expectedDigest");
            if (!TryGetProperty(element, "rowKey", out JsonElement rowKey)
                || string.IsNullOrEmpty(expectedDigest))
            {
                Reject(request, "Invalid delete-row item.");
                return;
            }
            rows.Add((ToObject(rowKey)!, expectedDigest));
        }
        await _workspace.DeleteRowsAsync(
            table,
            rows,
            schemaRevision,
            request.RequestId).ConfigureAwait(false);
    }

    private async Task PreviewPasteAsync(RoutedWebRequest request)
    {
        string? collection = GetString(request.Payload, "collection");
        string? schemaRevision = GetString(request.Payload, "schemaRevision");
        if (string.IsNullOrEmpty(collection) || string.IsNullOrEmpty(schemaRevision))
        {
            Reject(
                request,
                "table.previewPasteRequested requires 'collection' and 'schemaRevision'.");
            return;
        }
        IReadOnlyDictionary<string, object?>? selection =
            ToObjectDictionary(request.Payload, "selection");
        PasteStartCell? startCell = ReadStartCell(request.Payload);
        IReadOnlyList<IReadOnlyList<PasteCell>>? cells = ReadCells(request.Payload);
        if (selection is null || startCell is null || cells is null)
        {
            Reject(
                request,
                "table.previewPasteRequested requires 'selection', 'startCell' and 'cells'.");
            return;
        }
        try
        {
            PastePlan plan = await _workspace.Gateway.PreviewPasteAsync(
                collection,
                schemaRevision,
                selection,
                startCell,
                cells,
                CancellationToken.None).ConfigureAwait(false);
            _reply.PostNotification("table.pastePreviewReady", plan);
        }
        catch (Exception exception)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                exception.Message,
                "PASTE_PREVIEW_FAILED");
        }
    }

    private async Task ApplyPasteAsync(RoutedWebRequest request)
    {
        string? collection = GetString(request.Payload, "collection");
        string? token = GetString(request.Payload, "token");
        string? idempotencyKey = GetString(request.Payload, "idempotencyKey");
        if (string.IsNullOrEmpty(collection)
            || string.IsNullOrEmpty(token)
            || string.IsNullOrEmpty(idempotencyKey))
        {
            Reject(
                request,
                "table.applyPasteRequested requires 'collection', 'token' and 'idempotencyKey'.");
            return;
        }
        try
        {
            ApplyPasteResult result = await _workspace.Gateway.ApplyPasteAsync(
                collection,
                token,
                idempotencyKey,
                CancellationToken.None).ConfigureAwait(false);
            _reply.PostNotification("table.pasteApplied", result);
        }
        catch (Exception exception)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                exception.Message,
                "PASTE_APPLY_FAILED");
        }
    }

    private async Task QueryHistoryAsync(RoutedWebRequest request)
    {
        string? collection = GetCollection(request.Payload);
        string scope = GetString(request.Payload, "scope") ?? "table";
        string? itemId = GetScalarString(request.Payload, "itemId");
        string? field = GetString(request.Payload, "field");
        if (string.IsNullOrWhiteSpace(collection)
            || !IsHistoryQueryScope(scope)
            || (scope is "row" or "cell" && string.IsNullOrWhiteSpace(itemId))
            || (scope == "cell" && string.IsNullOrWhiteSpace(field)))
        {
            Reject(request, "history.queryRequested 的表、范围或选择无效。");
            return;
        }

        IReadOnlyList<string>? actions = null;
        if (TryGetProperty(request.Payload, "actions", out _))
        {
            actions = GetStringArray(request.Payload, "actions");
            if (actions is null)
            {
                Reject(
                    request,
                    "history.queryRequested 的 actions 必须是字符串数组。");
                return;
            }
        }
        var parameters = new ReadChangeSetsParams(
            Collection: collection,
            ItemId: itemId,
            Limit: Math.Clamp(GetInt(request.Payload, "limit", 50), 1, 100),
            Offset: Math.Max(0, GetInt(request.Payload, "offset", 0)),
            Scope: scope,
            Field: field,
            Search: GetString(request.Payload, "search"),
            DateFrom: GetString(request.Payload, "dateFrom"),
            DateTo: GetString(request.Payload, "dateTo"),
            ActorId: GetString(request.Payload, "actorId"),
            Actions: actions ?? Array.Empty<string>(),
            RecordId: GetScalarString(request.Payload, "recordId"));
        try
        {
            HistoryPage page = await ReadHistoryWithRecoveryAsync(parameters)
                .ConfigureAwait(false);
            _reply.PostResponse("history.pageLoaded", request.RequestId, page);
        }
        catch (Exception exception)
        {
            PostHistoryFailure(request, exception, "HISTORY_QUERY_FAILED");
        }
    }

    private async Task PreviewRestoreAsync(RoutedWebRequest request)
    {
        string? collection = GetCollection(request.Payload);
        string scope = GetString(request.Payload, "scope") ?? "row";
        string? itemId = GetScalarString(request.Payload, "itemId");
        string? targetRevision = GetString(request.Payload, "targetRevision");
        string? field = GetString(request.Payload, "field");
        if (string.IsNullOrWhiteSpace(collection)
            || !IsRestoreScope(scope)
            || string.IsNullOrWhiteSpace(itemId)
            || string.IsNullOrWhiteSpace(targetRevision)
            || (scope == "cell" && string.IsNullOrWhiteSpace(field)))
        {
            Reject(
                request,
                "history.previewRestoreRequested 的表、范围或目标修订无效。");
            return;
        }
        try
        {
            RestorePreview preview = await _workspace.Gateway.PreviewRestoreAsync(
                new PreviewRestoreParams(
                    collection,
                    itemId,
                    targetRevision,
                    scope,
                    field),
                CancellationToken.None).ConfigureAwait(false);
            _reply.PostResponse(
                "history.restorePreviewReady",
                request.RequestId,
                preview);
        }
        catch (Exception exception)
        {
            PostHistoryFailure(request, exception, "HISTORY_PREVIEW_FAILED");
        }
    }

    private async Task ApplyRestoreAsync(RoutedWebRequest request)
    {
        string? collection = GetCollection(request.Payload);
        string? itemId = GetScalarString(request.Payload, "itemId");
        string? token = GetString(request.Payload, "token");
        if (string.IsNullOrWhiteSpace(collection)
            || string.IsNullOrWhiteSpace(itemId)
            || string.IsNullOrWhiteSpace(token))
        {
            Reject(
                request,
                "history.applyRestoreRequested 缺少表、记录或预览令牌。");
            return;
        }
        try
        {
            RestoreResult result = await _workspace.Gateway.ApplyRestoreAsync(
                new ApplyRestoreParams(collection, itemId, token),
                CancellationToken.None).ConfigureAwait(false);
            _reply.PostResponse("history.restoreApplied", request.RequestId, result);
        }
        catch (Exception exception)
        {
            PostHistoryFailure(request, exception, "HISTORY_APPLY_FAILED");
        }
    }

    private async Task<HistoryPage> ReadHistoryWithRecoveryAsync(
        ReadChangeSetsParams parameters)
    {
        Exception? lastFailure = null;
        long deadline = Stopwatch.GetTimestamp()
            + (long)(_readRecoveryTimeout.TotalSeconds * Stopwatch.Frequency);
        while (true)
        {
            try
            {
                return await _workspace.Gateway.ReadChangeSetsAsync(
                    parameters,
                    CancellationToken.None).ConfigureAwait(false);
            }
            catch (Exception exception)
                when (exception is BackendUnavailableException
                    or ObjectDisposedException)
            {
                lastFailure = exception;
            }
            if (Stopwatch.GetTimestamp() >= deadline)
            {
                throw new BackendUnavailableException(
                    "The history service did not recover before the read deadline.",
                    lastFailure);
            }
            await Task.Delay(RecoveryReadPollInterval).ConfigureAwait(false);
        }
    }

    private async Task CreateTableAsync(RoutedWebRequest request)
    {
        string? displayName = GetString(request.Payload, "displayName")?.Trim();
        if (string.IsNullOrWhiteSpace(displayName)
            || displayName.Length > 128
            || displayName.Any(char.IsControl))
        {
            Reject(request, "创建数据表的请求无效。");
            return;
        }
        IProductDataRpcGateway? gateway = RequireProductGateway(request);
        if (gateway is null)
            return;
        CancellationToken sessionToken = _sessionToken();
        try
        {
            using var budget = SchemaLifecycleBudget.Begin(
                _schemaLifecycleTimeout,
                sessionToken,
                _timeProvider);
            JsonElement applied = await budget.RunAsync(
                SchemaLifecycleStage.Apply,
                token => gateway.CreateTableAsync(
                    JsonSerializer.SerializeToElement(new
                    {
                        displayName,
                        operationId = "table-create-" + Guid.NewGuid().ToString("N"),
                        actor = new { id = "desktop-host", kind = "host" },
                    }),
                    token)).ConfigureAwait(false);
            if (applied.ValueKind != JsonValueKind.Object
                || !applied.TryGetProperty("tableId", out JsonElement tableId)
                || tableId.ValueKind != JsonValueKind.String
                || string.IsNullOrWhiteSpace(tableId.GetString()))
            {
                throw new InvalidOperationException(
                    "SchemaCore create returned no table identity.");
            }
            TableSummary summary = await budget.RunAsync(
                SchemaLifecycleStage.Refresh,
                token => LoadCollectionListAsync(token)).ConfigureAwait(false);
            budget.Complete(SchemaLifecycleStage.Refresh);
            PublishCollectionList(
                request.RequestId,
                summary,
                createdTableId: tableId.GetString());
        }
        catch (SchemaLifecycleTimeoutException)
        {
            TraceFailure("table.create", "SCHEMA_LIFECYCLE_TIMEOUT");
            _reply.PostOperationFailed(
                request.RequestId,
                "数据表操作超时，完成状态尚未确认。",
                "SCHEMA_LIFECYCLE_TIMEOUT");
        }
        catch (OperationCanceledException) when (sessionToken.IsCancellationRequested)
        {
            TraceFailure("table.create", "SCHEMA_LIFECYCLE_CANCELLED");
            _reply.PostOperationFailed(
                request.RequestId,
                "工作区已关闭，数据表操作已取消。",
                "SCHEMA_LIFECYCLE_CANCELLED");
        }
        catch (Exception)
        {
            TraceFailure("table.create", "TABLE_CREATE_FAILED");
            _reply.PostOperationFailed(
                request.RequestId,
                "创建数据表失败。",
                "SCHEMA_APPLY_FAILED");
        }
    }

    private async Task DeleteTableAsync(RoutedWebRequest request)
    {
        string? collection = GetString(request.Payload, "collection");
        if (string.IsNullOrWhiteSpace(collection))
        {
            Reject(request, "缺少表名。");
            return;
        }
        IProductDataRpcGateway? gateway = RequireProductGateway(request);
        if (gateway is null)
            return;
        CancellationToken sessionToken = _sessionToken();
        try
        {
            using var budget = SchemaLifecycleBudget.Begin(
                _schemaLifecycleTimeout,
                sessionToken,
                _timeProvider);
            JsonElement schema = await budget.RunAsync(
                SchemaLifecycleStage.Inspect,
                token => gateway.GetTableSchemaAsync(
                    JsonSerializer.SerializeToElement(new { tableId = collection }),
                    token)).ConfigureAwait(false);
            string? revision = GetString(schema, "schemaRevision");
            if (string.IsNullOrWhiteSpace(revision))
                throw new InvalidOperationException("结构版本不可用。");
            await budget.RunAsync(
                SchemaLifecycleStage.Apply,
                token => gateway.DeleteSchemaAsync(
                    JsonSerializer.SerializeToElement(new
                    {
                        tableId = collection,
                        expectedRevision = revision,
                    }),
                    token)).ConfigureAwait(false);
            TableSummary summary = await budget.RunAsync(
                SchemaLifecycleStage.Refresh,
                token => LoadCollectionListAsync(token)).ConfigureAwait(false);
            budget.Complete(SchemaLifecycleStage.Refresh);
            PublishCollectionList(
                request.RequestId,
                summary,
                deletedTableId: collection);
        }
        catch (RpcRemoteException exception) when (exception.Code == -32602)
        {
            Reject(request, "删除数据表的请求无效。");
        }
        catch (SchemaLifecycleTimeoutException)
        {
            TraceFailure("schema.delete", "SCHEMA_LIFECYCLE_TIMEOUT");
            _reply.PostOperationFailed(
                request.RequestId,
                "数据表操作超时，完成状态尚未确认。",
                "SCHEMA_LIFECYCLE_TIMEOUT");
        }
        catch (OperationCanceledException) when (sessionToken.IsCancellationRequested)
        {
            TraceFailure("schema.delete", "SCHEMA_LIFECYCLE_CANCELLED");
            _reply.PostOperationFailed(
                request.RequestId,
                "工作区已关闭，数据表操作已取消。",
                "SCHEMA_LIFECYCLE_CANCELLED");
        }
        catch (Exception)
        {
            TraceFailure("schema.delete", "SCHEMA_DELETE_FAILED");
            _reply.PostOperationFailed(
                request.RequestId,
                "删除数据表失败。",
                "SCHEMA_DELETE_FAILED");
        }
    }

    private async Task<TableSummary> LoadCollectionListAsync(
        CancellationToken cancellationToken)
    {
        TableSummary summary = await _workspace.Gateway.ListTablesAsync(
            cancellationToken).ConfigureAwait(false);
        return summary;
    }

    private void PublishCollectionList(
        string? requestId,
        TableSummary summary,
        string? createdTableId = null,
        string? deletedTableId = null)
    {
        _workspace.UpdateKnownTables(summary.Tables);
        var broadcast = new
        {
            tables = summary.Tables,
            displayNames = summary.DisplayNames,
        };
        if (requestId is not null)
        {
            object response = createdTableId is not null
                ? new
                {
                    tables = summary.Tables,
                    displayNames = summary.DisplayNames,
                    createdTableId,
                }
                : deletedTableId is not null
                    ? new
                    {
                        tables = summary.Tables,
                        displayNames = summary.DisplayNames,
                        deletedTableId,
                    }
                    : broadcast;
            _reply.PostResponse(
                "database.collectionsChanged",
                requestId,
                response);
        }
        _reply.PostNotification("database.collectionsChanged", broadcast);
    }

    private IProductDataRpcGateway? RequireProductGateway(
        RoutedWebRequest request)
    {
        IProductDataRpcGateway? gateway = _productGateway();
        if (gateway is null)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "本地数据服务尚未就绪。",
                "BACKEND_UNAVAILABLE");
        }
        return gateway;
    }

    private void PostHistoryFailure(
        RoutedWebRequest request,
        Exception exception,
        string fallbackCode)
    {
        HistoryErrorMapper.Failure failure = HistoryErrorMapper.Map(
            exception,
            fallbackCode);
        TraceFailure("history", failure.Code);
        _reply.PostOperationFailed(request.RequestId, failure.Message, failure.Code);
    }

    private Task RejectUnknownAsync(RoutedWebRequest request)
    {
        _reply.PostOperationFailed(
            request.RequestId,
            "Table request type is not supported.",
            "UNKNOWN_TYPE");
        return Task.CompletedTask;
    }

    private void Reject(
        RoutedWebRequest request,
        string message,
        string code = "BAD_PAYLOAD")
        => _reply.PostOperationFailed(request.RequestId, message, code);

    private static bool IsValidDigest(string digest)
        => digest.Length == 71
            && digest.StartsWith("sha256:", StringComparison.Ordinal)
            && digest[7..].All(character =>
                (character >= '0' && character <= '9')
                || (character >= 'a' && character <= 'f'));

    private static bool IsHistoryQueryScope(string scope)
        => scope is "table" or "row" or "cell" or "archived";

    private static bool IsRestoreScope(string scope)
        => scope is "row" or "cell" or "archived";

    private static string? GetCollection(JsonElement payload)
        => GetString(payload, "table") ?? GetString(payload, "collection");

    private static PasteStartCell? ReadStartCell(JsonElement payload)
    {
        if (!TryGetProperty(payload, "startCell", out JsonElement startCell)
            || startCell.ValueKind != JsonValueKind.Object
            || !TryGetProperty(startCell, "column", out JsonElement column)
            || column.ValueKind != JsonValueKind.String)
        {
            return null;
        }
        object? rowKey = TryGetProperty(startCell, "rowKey", out JsonElement value)
            ? ToObject(value)
            : null;
        return new PasteStartCell(rowKey, column.GetString()!);
    }

    private static IReadOnlyList<IReadOnlyList<PasteCell>>? ReadCells(
        JsonElement payload)
    {
        if (!TryGetProperty(payload, "cells", out JsonElement cells)
            || cells.ValueKind != JsonValueKind.Array)
        {
            return null;
        }
        var rows = new List<IReadOnlyList<PasteCell>>();
        foreach (JsonElement rowElement in cells.EnumerateArray())
        {
            if (rowElement.ValueKind != JsonValueKind.Array)
                return null;
            var row = new List<PasteCell>();
            foreach (JsonElement cell in rowElement.EnumerateArray())
            {
                if (cell.ValueKind != JsonValueKind.Object
                    || !TryGetProperty(cell, "rowIndex", out JsonElement rowIndexValue)
                    || !rowIndexValue.TryGetInt32(out int rowIndex)
                    || !TryGetProperty(cell, "columnIndex", out JsonElement columnIndexValue)
                    || !columnIndexValue.TryGetInt32(out int columnIndex)
                    || !TryGetProperty(cell, "rawValue", out JsonElement rawValue)
                    || rawValue.ValueKind != JsonValueKind.String)
                {
                    return null;
                }
                string? column = GetString(cell, "column");
                object? parsed = TryGetProperty(
                        cell,
                        "parsedValue",
                        out JsonElement parsedValue)
                    && parsedValue.ValueKind != JsonValueKind.Null
                        ? ToObject(parsedValue)
                        : null;
                row.Add(new PasteCell(
                    rowIndex,
                    columnIndex,
                    column,
                    rawValue.GetString()!,
                    parsed));
            }
            rows.Add(row);
        }
        return rows;
    }

    private static IReadOnlyDictionary<string, object?>? ToObjectDictionary(
        JsonElement payload,
        string propertyName)
    {
        if (!TryGetProperty(payload, propertyName, out JsonElement value)
            || value.ValueKind != JsonValueKind.Object)
        {
            return null;
        }
        return value.EnumerateObject().ToDictionary(
            property => property.Name,
            property => ToObject(property.Value),
            StringComparer.Ordinal);
    }

    private static string? GetString(JsonElement payload, string propertyName)
        => payload.ValueKind == JsonValueKind.Object
            && payload.TryGetProperty(propertyName, out JsonElement value)
            && value.ValueKind == JsonValueKind.String
                ? value.GetString()
                : null;

    private static string? GetScalarString(
        JsonElement payload,
        string propertyName)
    {
        if (payload.ValueKind != JsonValueKind.Object
            || !payload.TryGetProperty(propertyName, out JsonElement value))
        {
            return null;
        }
        return value.ValueKind switch
        {
            JsonValueKind.String => value.GetString(),
            JsonValueKind.Number => value.GetRawText(),
            _ => null,
        };
    }

    private static IReadOnlyList<string>? GetStringArray(
        JsonElement payload,
        string propertyName)
    {
        if (!TryGetProperty(payload, propertyName, out JsonElement value)
            || value.ValueKind != JsonValueKind.Array)
        {
            return null;
        }
        var values = new List<string>();
        foreach (JsonElement item in value.EnumerateArray())
        {
            if (item.ValueKind != JsonValueKind.String)
                return null;
            values.Add(item.GetString() ?? string.Empty);
        }
        return values;
    }

    private static int GetInt(
        JsonElement payload,
        string propertyName,
        int defaultValue)
        => payload.ValueKind == JsonValueKind.Object
            && payload.TryGetProperty(propertyName, out JsonElement value)
            && value.ValueKind == JsonValueKind.Number
            && value.TryGetInt32(out int result)
                ? result
                : defaultValue;

    private static bool TryGetProperty(
        JsonElement payload,
        string propertyName,
        out JsonElement value)
    {
        if (payload.ValueKind == JsonValueKind.Object
            && payload.TryGetProperty(propertyName, out value))
        {
            value = value.Clone();
            return true;
        }
        value = default;
        return false;
    }

    private static object? ToObject(JsonElement value)
        => value.ValueKind switch
        {
            JsonValueKind.Undefined or JsonValueKind.Null => null,
            JsonValueKind.String => value.GetString(),
            JsonValueKind.True => true,
            JsonValueKind.False => false,
            JsonValueKind.Number when value.TryGetInt64(out long integer) => integer,
            JsonValueKind.Number => value.GetDouble(),
            JsonValueKind.Object => value.EnumerateObject().ToDictionary(
                property => property.Name,
                property => ToObject(property.Value)),
            JsonValueKind.Array => value.EnumerateArray().Select(ToObject).ToArray(),
            _ => value.Clone(),
        };

    private static void TraceFailure(string operation, string code)
        => Trace.TraceError(DiagnosticEvent.Failure(
            "VibeTable.Desktop.WorkspaceTableRequestController",
            operation,
            code));
}
