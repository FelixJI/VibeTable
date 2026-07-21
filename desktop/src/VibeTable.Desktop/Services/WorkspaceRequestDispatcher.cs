using System;
using System.Collections.Generic;
using System.Diagnostics;
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
    private DocumentWorkspaceHostService? _documents;

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

    public void SetDocumentWorkspace(DocumentWorkspaceHostService documents)
        => _documents = documents ?? throw new ArgumentNullException(nameof(documents));

    /// <summary>
    /// Invalidates renderer-visible document handles at a host lifecycle
    /// boundary. Safe before document workspace initialization.
    /// </summary>
    public void RotateDocumentCapabilityEpoch()
        => _documents?.RotateCapabilityEpoch();

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
            case "identifierMappings.listRequested":
                await OnListIdentifierMappingsAsync(request).ConfigureAwait(false);
                break;
            case "identifierMappings.updateAliasesRequested":
                await OnUpdateIdentifierAliasesAsync(request).ConfigureAwait(false);
                break;
            case "identifierMappings.importRequested":
                await OnImportIdentifierMappingsAsync(request).ConfigureAwait(false);
                break;
            case "identifierMappings.reconcileRequested":
                await OnReconcileIdentifierMappingsAsync(request).ConfigureAwait(false);
                break;
            case "identifierMappings.deleteRequested":
                await OnDeleteIdentifierMappingAsync(request).ConfigureAwait(false);
                break;
            case "identifierMappings.purgeRequested":
                await OnPurgeIdentifierMappingsAsync(request).ConfigureAwait(false);
                break;
            case "document.listRequested":
                await OnDocumentListRequestedAsync(request).ConfigureAwait(false);
                break;
            case "document.historyRequested":
                await OnDocumentHistoryRequestedAsync(request).ConfigureAwait(false);
                break;
            case "document.openRequested":
                OnDocumentOpenRequested(request);
                break;
            case "document.revealRequested":
                OnDocumentRevealRequested(request);
                break;
            case "document.previewRequested":
                OnDocumentPreviewRequested(request);
                break;
            case "document.pickRequested":
                _reply.PostOperationFailed(
                    request.RequestId,
                    "文件导入协议尚未就绪。",
                    "DOCUMENT_IMPORT_NOT_READY");
                break;
            case "document.relinkRequested":
                _reply.PostOperationFailed(
                    request.RequestId,
                    "文件重新定位协议尚未就绪。",
                    "DOCUMENT_RELINK_NOT_READY");
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
            _reply.PostNotification("database.opened", new
            {
                tables = Array.Empty<string>(),
                views = Array.Empty<string>(),
                displayNames = new Dictionary<string, string>(),
            });
            return;
        }

        var result = await _workspace.OpenDatabaseAsync(path).ConfigureAwait(false);
        _reply.PostNotification("database.opened", new
        {
            tables = result.Tables,
            views = result.Views,
            displayNames = result.DisplayNames,
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
            _reply.PostOperationFailed(
                request.RequestId,
                $"创建表失败：{ex.Message}",
                code: "CREATE_TABLE_FAILED");
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
            var result = await _directusGateway.DeleteTableAsync(collection, CancellationToken.None)
                .ConfigureAwait(false);
            // The backend may decline to delete (e.g. protected/system collection,
            // or a no-op because the table no longer exists). Surface that as an
            // explicit operation.failed rather than silently reporting success via
            // collectionsChanged — mirrors the deleted native TableManagementWindow
            // which warned on !result.Deleted.
            if (!result.Deleted)
            {
                _reply.PostOperationFailed(
                    request.RequestId,
                    $"后端未删除表 \"{collection}\"。",
                    code: "DELETE_DECLINED");
                return;
            }
            await PostCollectionsChangedAsync().ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            _reply.PostOperationFailed(request.RequestId, ex.Message, code: "DELETE_TABLE_FAILED");
        }
    }

    private async Task OnListIdentifierMappingsAsync(RoutedWebRequest request)
    {
        if (!TryRequireDirectus(request)) return;
        try
        {
            var result = await _directusGateway!.ListIdentifierMappingsAsync(
                TryGetString(request.Payload, "search"), CancellationToken.None)
                .ConfigureAwait(false);
            _reply.PostResponse("identifierMappings.result", request.RequestId, result);
        }
        catch (Exception ex)
        {
            _reply.PostOperationFailed(request.RequestId, ex.Message, "MAPPING_LIST_FAILED");
        }
    }

    private async Task OnUpdateIdentifierAliasesAsync(RoutedWebRequest request)
    {
        if (!TryRequireDirectus(request)) return;
        string? mappingId = TryGetString(request.Payload, "mappingId");
        var aliases = TryGetStringArray(request.Payload, "aliases");
        if (string.IsNullOrWhiteSpace(mappingId) || aliases is null)
        {
            _reply.PostOperationFailed(request.RequestId, "映射别名参数无效。", "BAD_PAYLOAD");
            return;
        }
        try
        {
            var result = await _directusGateway!.UpdateIdentifierAliasesAsync(
                mappingId, aliases, CancellationToken.None).ConfigureAwait(false);
            _reply.PostResponse("identifierMappings.result", request.RequestId, result);
        }
        catch (Exception ex)
        {
            _reply.PostOperationFailed(request.RequestId, ex.Message, "MAPPING_UPDATE_FAILED");
        }
    }

    private async Task OnImportIdentifierMappingsAsync(RoutedWebRequest request)
    {
        if (!TryRequireDirectus(request)) return;
        if (!TryGetProperty(request.Payload, "mappings", out var mappingsElement)
            || mappingsElement.ValueKind != JsonValueKind.Array)
        {
            _reply.PostOperationFailed(request.RequestId, "映射导入文件格式无效。", "BAD_PAYLOAD");
            return;
        }
        var mappings = new List<IdentifierMappingImportItem>();
        foreach (var item in mappingsElement.EnumerateArray())
        {
            string? entityKind = TryGetString(item, "entityKind");
            string? physicalName = TryGetString(item, "physicalName");
            string? displayName = TryGetString(item, "displayName");
            var aliases = TryGetStringArray(item, "aliases");
            if (string.IsNullOrWhiteSpace(entityKind)
                || string.IsNullOrWhiteSpace(physicalName)
                || string.IsNullOrWhiteSpace(displayName)
                || aliases is null)
            {
                _reply.PostOperationFailed(request.RequestId, "映射导入项格式无效。", "BAD_PAYLOAD");
                return;
            }
            mappings.Add(new IdentifierMappingImportItem(
                entityKind,
                TryGetString(item, "parentPhysicalName"),
                physicalName,
                displayName,
                aliases));
        }
        try
        {
            var result = await _directusGateway!.ImportIdentifierMappingsAsync(
                mappings, CancellationToken.None).ConfigureAwait(false);
            _reply.PostResponse("identifierMappings.result", request.RequestId, result);
        }
        catch (Exception ex)
        {
            _reply.PostOperationFailed(request.RequestId, ex.Message, "MAPPING_IMPORT_FAILED");
        }
    }

    private async Task OnReconcileIdentifierMappingsAsync(RoutedWebRequest request)
    {
        if (!TryRequireDirectus(request)) return;
        try
        {
            var result = await _directusGateway!.ReconcileIdentifierMappingsAsync(
                CancellationToken.None).ConfigureAwait(false);
            _reply.PostResponse("identifierMappings.result", request.RequestId, result);
            await PostCollectionsChangedAsync().ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            _reply.PostOperationFailed(request.RequestId, ex.Message, "MAPPING_RECONCILE_FAILED");
        }
    }

    private async Task OnDeleteIdentifierMappingAsync(RoutedWebRequest request)
    {
        if (!TryRequireDirectus(request)) return;
        string? mappingId = TryGetString(request.Payload, "mappingId");
        if (string.IsNullOrWhiteSpace(mappingId))
        {
            _reply.PostOperationFailed(request.RequestId, "映射标识无效。", "BAD_PAYLOAD");
            return;
        }
        try
        {
            var result = await _directusGateway!.DeleteIdentifierMappingAsync(
                mappingId, CancellationToken.None).ConfigureAwait(false);
            _reply.PostResponse("identifierMappings.result", request.RequestId, result);
        }
        catch (Exception ex)
        {
            _reply.PostOperationFailed(request.RequestId, ex.Message, "MAPPING_DELETE_FAILED");
        }
    }

    private async Task OnPurgeIdentifierMappingsAsync(RoutedWebRequest request)
    {
        if (!TryRequireDirectus(request)) return;
        try
        {
            var result = await _directusGateway!.PurgeIdentifierMappingsAsync(
                CancellationToken.None).ConfigureAwait(false);
            _reply.PostResponse("identifierMappings.result", request.RequestId, result);
        }
        catch (Exception ex)
        {
            _reply.PostOperationFailed(request.RequestId, ex.Message, "MAPPING_PURGE_FAILED");
        }
    }

    private async Task OnDocumentListRequestedAsync(RoutedWebRequest request)
    {
        if (!TryRequireDocuments(request)) return;
        string? authority = TryGetString(request.Payload, "authority");
        if (!string.Equals(authority, "workspace", StringComparison.Ordinal))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "云端资源附件需要记录关系字段，当前入口仅显示工作区文档。",
                "CLOUD_ATTACHMENT_SCOPE_REQUIRED");
            return;
        }
        if (!TryGetProperty(request.Payload, "scope", out var scope)
            || scope.ValueKind != JsonValueKind.Object)
        {
            _reply.PostOperationFailed(request.RequestId, "缺少文件范围。", "BAD_PAYLOAD");
            return;
        }

        string? kind = TryGetString(scope, "kind");
        try
        {
            DocumentListPayload result;
            if (string.Equals(kind, "global", StringComparison.Ordinal))
            {
                result = await _documents!.ListGlobalAsync(CancellationToken.None)
                    .ConfigureAwait(false);
            }
            else if (string.Equals(kind, "record", StringComparison.Ordinal))
            {
                string? collection = TryGetString(scope, "collection");
                string? itemId = TryGetScalarString(scope, "itemId");
                if (string.IsNullOrWhiteSpace(collection) || string.IsNullOrWhiteSpace(itemId))
                {
                    _reply.PostOperationFailed(
                        request.RequestId,
                        "记录文件范围缺少 collection 或 itemId。",
                        "BAD_PAYLOAD");
                    return;
                }
                result = await _documents!.ListAsync(
                    collection,
                    itemId,
                    CancellationToken.None).ConfigureAwait(false);
            }
            else
            {
                _reply.PostOperationFailed(request.RequestId, "未知文件范围。", "BAD_PAYLOAD");
                return;
            }
            _reply.PostResponse("document.listLoaded", request.RequestId, result);
        }
        catch (Exception ex)
        {
            PostDocumentFailure(request, ex, "DOCUMENT_LIST_FAILED");
        }
    }

    private async Task OnDocumentHistoryRequestedAsync(RoutedWebRequest request)
    {
        if (!TryRequireDocuments(request)) return;
        string? handle = TryGetString(request.Payload, "entryHandle");
        if (string.IsNullOrWhiteSpace(handle))
        {
            _reply.PostOperationFailed(request.RequestId, "缺少文档授权。", "BAD_PAYLOAD");
            return;
        }
        try
        {
            var result = await _documents!.ReadHistoryAsync(
                handle,
                TryGetInt(request.Payload, "limit", 50),
                TryGetInt(request.Payload, "offset", 0),
                CancellationToken.None).ConfigureAwait(false);
            _reply.PostResponse("document.historyLoaded", request.RequestId, result);
        }
        catch (Exception ex)
        {
            PostDocumentFailure(request, ex, "DOCUMENT_HISTORY_FAILED");
        }
    }

    private void OnDocumentOpenRequested(RoutedWebRequest request)
        => RunDocumentAction(request, "open", handle => _documents!.Open(handle));

    private void OnDocumentRevealRequested(RoutedWebRequest request)
        => RunDocumentAction(request, "reveal", handle => _documents!.Reveal(handle));

    private void OnDocumentPreviewRequested(RoutedWebRequest request)
        => RunDocumentAction(request, "preview", handle => _documents!.Preview(handle));

    private void RunDocumentAction(
        RoutedWebRequest request,
        string action,
        Action<string>? execute)
    {
        if (!TryRequireDocuments(request)) return;
        string? handle = TryGetString(request.Payload, "entryHandle");
        if (string.IsNullOrWhiteSpace(handle))
        {
            _reply.PostOperationFailed(request.RequestId, "缺少文档授权。", "BAD_PAYLOAD");
            return;
        }
        try
        {
            execute!(handle);
            _reply.PostResponse(
                "document.actionCompleted",
                request.RequestId,
                new { entryHandle = handle, action });
        }
        catch (Exception ex)
        {
            PostDocumentFailure(request, ex, "DOCUMENT_ACTION_FAILED");
        }
    }

    private bool TryRequireDocuments(RoutedWebRequest request)
    {
        if (_documents is not null) return true;
        _reply.PostOperationFailed(
            request.RequestId,
            "文档工作区尚未连接。",
            "DOCUMENT_WORKSPACE_UNAVAILABLE");
        return false;
    }

    private void PostDocumentFailure(
        RoutedWebRequest request,
        Exception exception,
        string fallbackCode)
    {
        string candidateCode = exception is DocumentCapabilityException capabilityError
            ? capabilityError.Code
            : exception is DocumentPreviewException previewError
                ? previewError.Code
            : fallbackCode;
        var (code, message) = GetSafeDocumentFailure(candidateCode, fallbackCode);

        // Keep diagnostic detail on the native side. Exception messages may
        // contain absolute paths or COM details and must never cross the web bridge.
        Trace.TraceError($"Document request failed ({code}): {exception}");
        _reply.PostOperationFailed(request.RequestId, message, code);
    }

    private static (string Code, string Message) GetSafeDocumentFailure(
        string candidateCode,
        string fallbackCode)
        => candidateCode switch
        {
            "DOCUMENT_HANDLE_INVALID" =>
                (candidateCode, "文档授权已失效，请刷新文件列表后重试。"),
            "DOCUMENT_HANDLE_EXPIRED" =>
                (candidateCode, "文档授权已过期，请刷新文件列表后重试。"),
            "DOCUMENT_CAPABILITY_DENIED" =>
                (candidateCode, "当前文档不允许执行此操作。"),
            "REVISION_HANDLE_INVALID" =>
                (candidateCode, "版本授权已失效，请重新打开版本历史。"),
            "REVISION_HANDLE_EXPIRED" =>
                (candidateCode, "版本授权已过期，请重新打开版本历史。"),
            "REVISION_CAPABILITY_DENIED" =>
                (candidateCode, "当前版本不允许执行此操作。"),
            "DOCUMENT_LINK_UNAVAILABLE" =>
                (candidateCode, "当前文档没有可解除的记录关联。"),
            "WORKSPACE_UNMOUNTED" =>
                (candidateCode, "此工作区尚未挂载到本机。"),
            "DOCUMENT_MISSING" =>
                (candidateCode, "文件已移动或删除，请重新定位。"),
            "PREVIEW_HANDLER_UNAVAILABLE" =>
                (candidateCode, "系统没有可用的文件预览器，请使用默认应用打开。"),
            "PREVIEW_HOST_CREATE_FAILED" =>
                (candidateCode, "无法创建文件预览窗口，请稍后重试。"),
            "PREVIEW_HANDLER_FAILED" =>
                (candidateCode, "文件预览失败，请使用默认应用打开。"),
            "DOCUMENT_LIST_FAILED" =>
                (candidateCode, "文件列表加载失败，请稍后重试。"),
            "DOCUMENT_HISTORY_FAILED" =>
                (candidateCode, "版本历史加载失败，请稍后重试。"),
            "DOCUMENT_ACTION_FAILED" =>
                (candidateCode, "文档操作失败，请稍后重试。"),
            _ => GetSafeDocumentFallback(fallbackCode),
        };

    private static (string Code, string Message) GetSafeDocumentFallback(string fallbackCode)
        => fallbackCode switch
        {
            "DOCUMENT_LIST_FAILED" =>
                (fallbackCode, "文件列表加载失败，请稍后重试。"),
            "DOCUMENT_HISTORY_FAILED" =>
                (fallbackCode, "版本历史加载失败，请稍后重试。"),
            "DOCUMENT_ACTION_FAILED" =>
                (fallbackCode, "文档操作失败，请稍后重试。"),
            _ => ("DOCUMENT_OPERATION_FAILED", "文档操作失败，请稍后重试。"),
        };

    private bool TryRequireDirectus(RoutedWebRequest request)
    {
        if (_directusGateway is not null) return true;
        _reply.PostOperationFailed(request.RequestId, "Directus 尚未登录。", "NOT_AUTHENTICATED");
        return false;
    }

    /// <summary>
    /// Re-lists collections, filters to user tables, and pushes
    /// database.collectionsChanged so the sidebar refreshes. Also refreshes the
    /// workspace's known-tables cache so a subsequent <c>table.selected</c> for
    /// the new (or just-removed) collection validates against the FRESH list.
    /// </summary>
    private async Task PostCollectionsChangedAsync()
    {
        var list = await _directusGateway!.ListCollectionsAsync(CancellationToken.None)
            .ConfigureAwait(false);
        var tables = DirectusCollectionFilter.FilterUserTables(list.Collections);
        // Keep the workspace cache in sync with the sidebar: without this, the
        // cache populated once at session open would reject a freshly-created
        // table (or accept a just-deleted one) at the next SelectTableAsync.
        _workspace.UpdateKnownTables(tables);
        _reply.PostNotification("database.collectionsChanged", new
        {
            tables,
            capabilityHashes = list.CapabilityHashes,
            displayNames = list.DisplayNames,
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

    private static string? TryGetScalarString(JsonElement payload, string name)
    {
        if (payload.ValueKind != JsonValueKind.Object
            || !payload.TryGetProperty(name, out var element))
        {
            return null;
        }
        return element.ValueKind switch
        {
            JsonValueKind.String => element.GetString(),
            JsonValueKind.Number => element.GetRawText(),
            _ => null,
        };
    }

    private static IReadOnlyList<string>? TryGetStringArray(JsonElement payload, string name)
    {
        if (payload.ValueKind != JsonValueKind.Object
            || !payload.TryGetProperty(name, out var element)
            || element.ValueKind != JsonValueKind.Array)
        {
            return null;
        }
        var values = new List<string>();
        foreach (var item in element.EnumerateArray())
        {
            if (item.ValueKind != JsonValueKind.String) return null;
            values.Add(item.GetString() ?? string.Empty);
        }
        return values;
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
