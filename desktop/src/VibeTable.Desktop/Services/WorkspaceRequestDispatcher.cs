using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Routes the four Phase-A web request types to the
/// <see cref="TableWorkspaceService"/> and posts typed host notifications back
/// to the renderer via <see cref="IWebReplySink"/>.
/// </summary>
/// <remarks>
/// <para>
/// This is the glue between the untrusted WebView2 boundary (the
/// <see cref="WebMessageRouter"/>) and the workspace flow. It owns no state
/// beyond references to the workspace, source selector, and reply sink; it is safe to
/// construct in unit tests without WPF.
/// </para>
/// <para>
/// The four request types:
/// </para>
/// <list type="bullet">
/// <item><c>app.ready</c> — marks the host ready (caller does this); this
/// dispatcher has no additional work for this handshake.</item>
/// <item><c>database.openRequested</c> — resolves the configured Directus
/// source, then <see cref="TableWorkspaceService.OpenDatabaseAsync"/>,
/// then posts <c>database.opened</c> or <c>operation.failed</c>. The web
/// payload's path field is ignored — the host never trusts a renderer-supplied
/// endpoint or filesystem path.</item>
/// <item><c>table.selected</c> — calls
/// <see cref="TableWorkspaceService.SelectTableAsync"/>; failures surface as
/// <c>operation.failed</c>. The workspace drives the multi-page client-mode
/// fetch and emits <c>table.pageLoaded</c>/<c>table.datasetReady</c>.</item>
/// <item><c>table.pageRequested</c> — Phase A remote-mode single-page fetch
/// (the host already drove the client-mode dataset on select). Reads one page
/// and emits <c>table.pageLoaded</c>.</item>
/// </list>
/// </remarks>
public sealed class WorkspaceRequestDispatcher
{
    private readonly TableWorkspaceService _workspace;
    private readonly IDatabasePicker _picker;
    private readonly IWebReplySink _reply;
    private readonly GridStateCoordinator? _coordinator;
    private IDirectusRpcGateway? _directusGateway;

    public WorkspaceRequestDispatcher(
        TableWorkspaceService workspace,
        IDatabasePicker picker,
        IWebReplySink reply,
        GridStateCoordinator? coordinator = null)
    {
        _workspace = workspace ?? throw new ArgumentNullException(nameof(workspace));
        _picker = picker ?? throw new ArgumentNullException(nameof(picker));
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _coordinator = coordinator;
    }

    /// <summary>
    /// Injects the Directus RPC gateway used by table-management handlers.
    /// Called by MainWindow after the session is authenticated; null before
    /// that (handlers return operation.failed code NOT_AUTHENTICATED).
    /// </summary>
    public void SetDirectusGateway(IDirectusRpcGateway gateway)
        => _directusGateway = gateway ?? throw new ArgumentNullException(nameof(gateway));

    /// <summary>
    /// Dispatches a routed web request to its handler. Each handler is
    /// fire-and-forget (the router returns null); failures surface as
    /// <c>operation.failed</c> via <see cref="IWebReplySink"/>.
    /// </summary>
    public void Dispatch(RoutedWebRequest request)
    {
        // Fire-and-forget on the thread pool so the router callback returns
        // immediately. Each branch catches its own exceptions and posts
        // operation.failed on failure.
        _ = Task.Run(async () =>
        {
            try
            {
                await DispatchAsync(request).ConfigureAwait(false);
            }
            catch (Exception ex)
            {
                _reply.PostOperationFailed(
                    request.RequestId,
                    ex.Message,
                    code: "WORKSPACE_ERROR");
            }
        });
    }

    private async Task DispatchAsync(RoutedWebRequest request)
    {
        switch (request.Type)
        {
            case "app.ready":
                break;
            case "database.openRequested":
                await OnDatabaseOpenRequestedAsync(request).ConfigureAwait(false);
                break;
            case "table.selected":
                await OnTableSelectedAsync(request).ConfigureAwait(false);
                break;
            case "table.pageRequested":
                await OnTablePageRequestedAsync(request).ConfigureAwait(false);
                break;
            case "table.updateCellRequested":
                await OnUpdateCellRequestedAsync(request).ConfigureAwait(false);
                break;
            case "table.insertRowRequested":
                await OnInsertRowRequestedAsync(request).ConfigureAwait(false);
                break;
            case "table.deleteRowsRequested":
                await OnDeleteRowsRequestedAsync(request).ConfigureAwait(false);
                break;
            case "table.queryRequested":
                await OnTableQueryRequestedAsync(request).ConfigureAwait(false);
                break;
            case "gridState.saveRequested":
                await OnGridStateSaveRequestedAsync(request).ConfigureAwait(false);
                break;
            case "table.previewPasteRequested":
                await OnPreviewPasteRequestedAsync(request).ConfigureAwait(false);
                break;
            case "table.applyPasteRequested":
                await OnApplyPasteRequestedAsync(request).ConfigureAwait(false);
                break;
            case "tableAdmin.createRequested":
                await OnCreateTableRequestedAsync(request).ConfigureAwait(false);
                break;
            case "tableAdmin.deleteRequested":
                await OnDeleteTableRequestedAsync(request).ConfigureAwait(false);
                break;
            default:
                // Unknown types never reach here (router whitelists), but be
                // defensive: surface as operation.failed.
                _reply.PostOperationFailed(
                    request.RequestId,
                    $"Unhandled request type '{request.Type}'.",
                    code: "UNKNOWN_TYPE");
                break;
        }
    }

    private async Task OnDatabaseOpenRequestedAsync(RoutedWebRequest request)
    {
        // The configured source comes only from the host. The renderer's path
        // field is deliberately ignored.
        string? path = await _picker.PickDatabaseAsync().ConfigureAwait(false);
        if (string.IsNullOrEmpty(path))
        {
            // User cancelled the picker — not an error, but nothing to open.
            // Post an empty database.opened so the web resets its state.
            _reply.PostNotification("database.opened", new { tables = Array.Empty<string>(), views = Array.Empty<string>() });
            return;
        }

        var result = await _workspace.OpenDatabaseAsync(path).ConfigureAwait(false);
        _reply.PostNotification("database.opened", new
        {
            tables = result.Tables,
            views = result.Views,
        });
    }

    private async Task OnTableSelectedAsync(RoutedWebRequest request)
    {
        // Extract the table name from the payload (must be a non-empty string).
        string? table = TryGetString(request.Payload, "table");
        if (string.IsNullOrEmpty(table))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "table.selected requires a non-empty 'table' payload field.",
                code: "BAD_PAYLOAD");
            return;
        }

        // The workspace service validates the name against the advertised list
        // (it throws ArgumentException for unknown names). That exception lands
        // in the dispatcher's catch and becomes operation.failed.
        await _workspace.SelectTableAsync(table).ConfigureAwait(false);
        await _workspace.GetEditSchemaAsync(table).ConfigureAwait(false);
    }

    private async Task OnTablePageRequestedAsync(RoutedWebRequest request)
    {
        string? table = TryGetString(request.Payload, "table");
        if (string.IsNullOrEmpty(table))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "table.pageRequested requires a non-empty 'table' payload field.",
                code: "BAD_PAYLOAD");
            return;
        }
        int offset = TryGetInt(request.Payload, "offset", defaultValue: 0);
        int limit = TryGetInt(request.Payload, "limit", defaultValue: TableWorkspaceLimits.MaxPageLimit);
        // Clamp to the legal page range — the workspace also enforces this, but
        // we clamp the web-supplied value defensively before it reaches RPC.
        limit = Math.Clamp(limit, 1, TableWorkspaceLimits.MaxPageLimit);

        var page = await _workspace.ReadPageForRemoteAsync(table, offset, limit)
            .ConfigureAwait(false);
        _reply.PostNotification("table.pageLoaded", page);
    }

    private async Task OnUpdateCellRequestedAsync(RoutedWebRequest request)
    {
        string? table = TryGetString(request.Payload, "table");
        string? column = TryGetString(request.Payload, "column");
        string? schemaRevision = TryGetString(request.Payload, "schemaRevision");
        if (string.IsNullOrEmpty(table) || string.IsNullOrEmpty(column)
            || string.IsNullOrEmpty(schemaRevision)
            || !TryGetProperty(request.Payload, "rowKey", out var rowKey))
        {
            _reply.PostOperationFailed(request.RequestId, "Invalid update-cell payload.", "BAD_PAYLOAD");
            return;
        }
        TryGetProperty(request.Payload, "oldValue", out var oldValue);
        TryGetProperty(request.Payload, "newValue", out var newValue);
        await _workspace.UpdateCellAsync(
            table,
            ToObject(rowKey)!,
            column,
            ToObject(oldValue),
            ToObject(newValue),
            schemaRevision).ConfigureAwait(false);
    }

    private async Task OnInsertRowRequestedAsync(RoutedWebRequest request)
    {
        string? table = TryGetString(request.Payload, "table");
        string? schemaRevision = TryGetString(request.Payload, "schemaRevision");
        if (string.IsNullOrEmpty(table) || string.IsNullOrEmpty(schemaRevision)
            || !TryGetProperty(request.Payload, "values", out var valuesElement)
            || valuesElement.ValueKind != JsonValueKind.Object)
        {
            _reply.PostOperationFailed(request.RequestId, "Invalid insert-row payload.", "BAD_PAYLOAD");
            return;
        }
        var values = valuesElement.EnumerateObject().ToDictionary(
            property => property.Name,
            property => ToObject(property.Value));
        await _workspace.InsertRowAsync(table, values, schemaRevision).ConfigureAwait(false);
    }

    private async Task OnDeleteRowsRequestedAsync(RoutedWebRequest request)
    {
        string? table = TryGetString(request.Payload, "table");
        string? schemaRevision = TryGetString(request.Payload, "schemaRevision");
        if (string.IsNullOrEmpty(table) || string.IsNullOrEmpty(schemaRevision)
            || !TryGetProperty(request.Payload, "rows", out var rowsElement)
            || rowsElement.ValueKind != JsonValueKind.Array)
        {
            _reply.PostOperationFailed(request.RequestId, "Invalid delete-rows payload.", "BAD_PAYLOAD");
            return;
        }
        var rows = new List<(object RowKey, string ExpectedDigest)>();
        foreach (var element in rowsElement.EnumerateArray())
        {
            if (!TryGetProperty(element, "rowKey", out var rowKey)
                || string.IsNullOrEmpty(TryGetString(element, "expectedDigest")))
            {
                _reply.PostOperationFailed(request.RequestId, "Invalid delete-row item.", "BAD_PAYLOAD");
                return;
            }
            rows.Add((ToObject(rowKey)!, TryGetString(element, "expectedDigest")!));
        }
        await _workspace.DeleteRowsAsync(table, rows, schemaRevision).ConfigureAwait(false);
    }

    // -------------------------------------------------------------------
    // B3: query + grid-state request handlers.
    // -------------------------------------------------------------------

    private async Task OnTableQueryRequestedAsync(RoutedWebRequest request)
    {
        if (_coordinator is null)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "Query requests are not wired in this host configuration.",
                code: "NOT_CONFIGURED");
            return;
        }
        string? table = TryGetString(request.Payload, "table");
        if (string.IsNullOrEmpty(table))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "table.queryRequested requires a non-empty 'table' payload field.",
                code: "BAD_PAYLOAD");
            return;
        }
        // The query AST is forwarded as an opaque JSON object so the backend
        // Pydantic model validates it. The coordinator debounces the request.
        var query = TryGetQuery(request.Payload);
        _coordinator.RequestQuery(table, query);
    }

    private async Task OnGridStateSaveRequestedAsync(RoutedWebRequest request)
    {
        if (_coordinator is null)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "Grid-state save is not wired in this host configuration.",
                code: "NOT_CONFIGURED");
            return;
        }
        var state = TryGetGridState(request.Payload);
        if (state is null)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "gridState.saveRequested requires a 'state' payload field.",
                code: "BAD_PAYLOAD");
            return;
        }
        _coordinator.RequestSave(state);
    }

    // -------------------------------------------------------------------
    // B2: paste preview + apply handlers.
    // -------------------------------------------------------------------

    private async Task OnPreviewPasteRequestedAsync(RoutedWebRequest request)
    {
        string? collection = TryGetString(request.Payload, "collection");
        string? schemaRevision = TryGetString(request.Payload, "schemaRevision");
        if (string.IsNullOrEmpty(collection) || string.IsNullOrEmpty(schemaRevision))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "table.previewPasteRequested requires 'collection' and 'schemaRevision'.",
                code: "BAD_PAYLOAD");
            return;
        }
        // The selection + startCell + cells are forwarded as opaque JSON so the
        // backend Pydantic model validates them. The host does not interpret
        // cell contents.
        var selection = ToObjectDictionary(request.Payload, "selection");
        var startCell = TryGetStartCell(request.Payload);
        var cells = TryGetCells(request.Payload);
        if (selection is null || startCell is null || cells is null)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "table.previewPasteRequested requires 'selection', 'startCell' and 'cells'.",
                code: "BAD_PAYLOAD");
            return;
        }
        try
        {
            var plan = await _workspace.Gateway.PreviewPasteAsync(
                collection, schemaRevision, selection, startCell, cells,
                CancellationToken.None).ConfigureAwait(false);
            _reply.PostNotification("table.pastePreviewReady", plan);
        }
        catch (Exception ex)
        {
            _reply.PostOperationFailed(request.RequestId, ex.Message, code: "PASTE_PREVIEW_FAILED");
        }
    }

    private async Task OnApplyPasteRequestedAsync(RoutedWebRequest request)
    {
        string? collection = TryGetString(request.Payload, "collection");
        string? token = TryGetString(request.Payload, "token");
        string? idempotencyKey = TryGetString(request.Payload, "idempotencyKey");
        if (string.IsNullOrEmpty(collection) || string.IsNullOrEmpty(token)
            || string.IsNullOrEmpty(idempotencyKey))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "table.applyPasteRequested requires 'collection', 'token' and 'idempotencyKey'.",
                code: "BAD_PAYLOAD");
            return;
        }
        try
        {
            var result = await _workspace.Gateway.ApplyPasteAsync(
                collection, token, idempotencyKey, CancellationToken.None)
                .ConfigureAwait(false);
            _reply.PostNotification("table.pasteApplied", result);
        }
        catch (Exception ex)
        {
            _reply.PostOperationFailed(request.RequestId, ex.Message, code: "PASTE_APPLY_FAILED");
        }
    }

    // -------------------------------------------------------------------
    // Task 8: table admin (create/delete) handlers wired to the Directus
    // RPC gateway. On success both re-list collections and push
    // database.collectionsChanged so the web sidebar refreshes.
    // -------------------------------------------------------------------

    private async Task OnCreateTableRequestedAsync(RoutedWebRequest request)
    {
        if (_directusGateway is null)
        {
            _reply.PostOperationFailed(request.RequestId, "Directus 尚未登录。", code: "NOT_AUTHENTICATED");
            return;
        }
        string? name = TryGetString(request.Payload, "name");
        if (string.IsNullOrWhiteSpace(name))
        {
            _reply.PostOperationFailed(request.RequestId, "缺少表名。", code: "BAD_PAYLOAD");
            return;
        }
        var fields = new List<FieldDefinition>();
        if (TryGetProperty(request.Payload, "fields", out var fieldsEl)
            && fieldsEl.ValueKind == JsonValueKind.Array)
        {
            foreach (var item in fieldsEl.EnumerateArray())
            {
                string? key = item.ValueKind == JsonValueKind.Object
                    && item.TryGetProperty("key", out var kEl) && kEl.ValueKind == JsonValueKind.String
                    ? kEl.GetString() : null;
                string? type = item.ValueKind == JsonValueKind.Object
                    && item.TryGetProperty("type", out var tEl) && tEl.ValueKind == JsonValueKind.String
                    ? tEl.GetString() : null;
                if (!string.IsNullOrWhiteSpace(key) && !string.IsNullOrWhiteSpace(type))
                {
                    fields.Add(new FieldDefinition(key!, type!));
                }
            }
        }
        try
        {
            await _directusGateway.CreateTableAsync(name, fields, CancellationToken.None)
                .ConfigureAwait(false);
            await PostCollectionsChangedAsync().ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            _reply.PostOperationFailed(request.RequestId, ex.Message, code: "CREATE_TABLE_FAILED");
        }
    }

    private async Task OnDeleteTableRequestedAsync(RoutedWebRequest request)
    {
        if (_directusGateway is null)
        {
            _reply.PostOperationFailed(request.RequestId, "Directus 尚未登录。", code: "NOT_AUTHENTICATED");
            return;
        }
        string? collection = TryGetString(request.Payload, "collection");
        if (string.IsNullOrWhiteSpace(collection))
        {
            _reply.PostOperationFailed(request.RequestId, "缺少表名。", code: "BAD_PAYLOAD");
            return;
        }
        try
        {
            await _directusGateway.DeleteTableAsync(collection, CancellationToken.None)
                .ConfigureAwait(false);
            await PostCollectionsChangedAsync().ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            _reply.PostOperationFailed(request.RequestId, ex.Message, code: "DELETE_TABLE_FAILED");
        }
    }

    /// <summary>
    /// Re-lists collections, filters to user tables, and pushes
    /// database.collectionsChanged so the sidebar refreshes.
    /// </summary>
    private async Task PostCollectionsChangedAsync()
    {
        var list = await _directusGateway!.ListCollectionsAsync(CancellationToken.None)
            .ConfigureAwait(false);
        var tables = DirectusCollectionFilter.FilterUserTables(list.Collections);
        _reply.PostNotification("database.collectionsChanged", new
        {
            tables,
            capabilityHashes = list.CapabilityHashes,
        });
    }

    private static PasteStartCell? TryGetStartCell(JsonElement payload)
    {
        if (payload.ValueKind != JsonValueKind.Object
            || !payload.TryGetProperty("startCell", out var sc)
            || sc.ValueKind != JsonValueKind.Object
            || !sc.TryGetProperty("column", out var col)
            || col.ValueKind != JsonValueKind.String)
        {
            return null;
        }
        object? rowKey = null;
        if (sc.TryGetProperty("rowKey", out var rk))
        {
            rowKey = ToObject(rk);
        }
        return new PasteStartCell(rowKey, col.GetString()!);
    }

    private static IReadOnlyList<IReadOnlyList<PasteCell>>? TryGetCells(JsonElement payload)
    {
        if (payload.ValueKind != JsonValueKind.Object
            || !payload.TryGetProperty("cells", out var cellsElement)
            || cellsElement.ValueKind != JsonValueKind.Array)
        {
            return null;
        }
        var rows = new List<IReadOnlyList<PasteCell>>();
        foreach (var rowElement in cellsElement.EnumerateArray())
        {
            if (rowElement.ValueKind != JsonValueKind.Array)
            {
                return null;
            }
            var row = new List<PasteCell>();
            foreach (var cell in rowElement.EnumerateArray())
            {
                if (cell.ValueKind != JsonValueKind.Object
                    || !cell.TryGetProperty("rowIndex", out var ri) || !ri.TryGetInt32(out var rowIndex)
                    || !cell.TryGetProperty("columnIndex", out var ci) || !ci.TryGetInt32(out var columnIndex)
                    || !cell.TryGetProperty("rawValue", out var rv) || rv.ValueKind != JsonValueKind.String)
                {
                    return null;
                }
                string? column = null;
                if (cell.TryGetProperty("column", out var colEl) && colEl.ValueKind == JsonValueKind.String)
                {
                    column = colEl.GetString();
                }
                object? parsed = null;
                if (cell.TryGetProperty("parsedValue", out var pv) && pv.ValueKind != JsonValueKind.Null)
                {
                    parsed = ToObject(pv);
                }
                row.Add(new PasteCell(rowIndex, columnIndex, column, rv.GetString()!, parsed));
            }
            rows.Add(row);
        }
        return rows;
    }

    private static IReadOnlyDictionary<string, object?>? ToObjectDictionary(JsonElement payload, string field)
    {
        if (payload.ValueKind != JsonValueKind.Object
            || !payload.TryGetProperty(field, out var element)
            || element.ValueKind != JsonValueKind.Object)
        {
            return null;
        }
        var dict = new Dictionary<string, object?>(StringComparer.Ordinal);
        foreach (var property in element.EnumerateObject())
        {
            dict[property.Name] = ToObject(property.Value);
        }
        return dict;
    }

    private static TableQuery TryGetQuery(JsonElement payload)
    {
        // Build a TableQuery from the payload's "query" object (best-effort).
        // The backend re-validates; the host only forwards known fields.
        string? keyword = null;
        if (payload.ValueKind == JsonValueKind.Object
            && payload.TryGetProperty("query", out var q)
            && q.ValueKind == JsonValueKind.Object)
        {
            if (q.TryGetProperty("keyword", out var kw) && kw.ValueKind == JsonValueKind.String)
            {
                keyword = kw.GetString();
            }
            int offset = q.TryGetProperty("offset", out var off) && off.TryGetInt32(out var o) ? o : 0;
            int limit = q.TryGetProperty("limit", out var lim) && lim.TryGetInt32(out var l) ? l : 100;
            return new TableQuery(keyword, null, null, offset, limit);
        }
        return new TableQuery();
    }

    private static GridState? TryGetGridState(JsonElement payload)
    {
        if (payload.ValueKind != JsonValueKind.Object
            || !payload.TryGetProperty("state", out var st)
            || st.ValueKind != JsonValueKind.Object)
        {
            return null;
        }
        string density = st.TryGetProperty("density", out var d) && d.ValueKind == JsonValueKind.String
            ? d.GetString() ?? "comfortable"
            : "comfortable";
        bool forcedRemote = st.TryGetProperty("forcedRemote", out var fr)
            && fr.ValueKind == JsonValueKind.True;
        string? revision = st.TryGetProperty("revision", out var rev) && rev.ValueKind == JsonValueKind.String
            ? rev.GetString()
            : null;
        return new GridState(null, null, null, null, density, forcedRemote, revision);
    }

    private static string? TryGetString(JsonElement payload, string name)
    {
        if (payload.ValueKind == JsonValueKind.Object
            && payload.TryGetProperty(name, out var el)
            && el.ValueKind == JsonValueKind.String)
        {
            return el.GetString();
        }
        return null;
    }

    private static int TryGetInt(JsonElement payload, string name, int defaultValue)
    {
        if (payload.ValueKind == JsonValueKind.Object
            && payload.TryGetProperty(name, out var el)
            && el.ValueKind == JsonValueKind.Number
            && el.TryGetInt32(out var value))
        {
            return value;
        }
        return defaultValue;
    }

    private static bool TryGetProperty(JsonElement payload, string name, out JsonElement value)
    {
        if (payload.ValueKind == JsonValueKind.Object && payload.TryGetProperty(name, out value))
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
                property => property.Name, property => ToObject(property.Value)),
            JsonValueKind.Array => value.EnumerateArray().Select(ToObject).ToArray(),
            _ => value.Clone(),
        };
}
