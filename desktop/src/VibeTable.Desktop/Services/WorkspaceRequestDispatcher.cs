using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Diagnostics;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Infrastructure.Workspace;

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
/// <item><c>database.openRequested</c> — resolves the configured local data
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
    private static readonly TimeSpan RecoveryReadPollInterval =
        TimeSpan.FromMilliseconds(25);

    private readonly TableWorkspaceService _workspace;
    private readonly IDatabasePicker _picker;
    private readonly IWebReplySink _reply;
    private readonly GridStateCoordinator? _coordinator;
    private readonly DashboardFeatureOptions _dashboardFeatures;
    private readonly AutoDateFeatureOptions _autoDateFeatures;
    private readonly TimeSpan _dashboardRequestTimeout;
    private readonly TimeSpan _readRecoveryTimeout;
    private readonly WorkspaceSessionEnvelopeFilter? _sessionEnvelopeFilter;
    private readonly ConcurrentDictionary<string, DashboardRequestState> _dashboardRequests = new();
    private readonly SemaphoreSlim _dashboardQueryGate = new(6, 6);
    private IProductDataRpcGateway? _productDataGateway;
    private IDashboardRpcGateway? _dashboardGateway;
    private CancellationToken _dashboardSessionToken;
    private WorkspaceDocumentOsAdapter? _documents;

    public WorkspaceRequestDispatcher(
        TableWorkspaceService workspace,
        IDatabasePicker picker,
        IWebReplySink reply,
        GridStateCoordinator? coordinator = null,
        DashboardFeatureOptions? dashboardFeatures = null,
        TimeSpan? dashboardRequestTimeout = null,
        TimeSpan? readRecoveryTimeout = null,
        AutoDateFeatureOptions? autoDateFeatures = null,
        WorkspaceSessionEnvelopeFilter? sessionEnvelopeFilter = null)
    {
        _workspace = workspace ?? throw new ArgumentNullException(nameof(workspace));
        _picker = picker ?? throw new ArgumentNullException(nameof(picker));
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _coordinator = coordinator;
        _dashboardFeatures = dashboardFeatures ?? DashboardFeatureOptions.Disabled;
        _autoDateFeatures = autoDateFeatures ?? AutoDateFeatureOptions.Disabled;
        _dashboardRequestTimeout = dashboardRequestTimeout ?? TimeSpan.FromSeconds(60);
        _readRecoveryTimeout =
            readRecoveryTimeout ?? TimeSpan.FromSeconds(3);
        _sessionEnvelopeFilter = sessionEnvelopeFilter;
    }

    /// <summary>
    /// Injects the provider-neutral product data gateway. The composition root
    /// owns its lifecycle and must never expose its sidecar credentials.
    /// </summary>
    public void SetProductDataGateway(IProductDataRpcGateway gateway)
        => Interlocked.Exchange(
            ref _productDataGateway,
            gateway ?? throw new ArgumentNullException(nameof(gateway)));

    /// <summary>
    /// Removes a product gateway only when it is still the currently published
    /// instance. This prevents a late backend-stop callback from clearing a
    /// newly configured gateway.
    /// </summary>
    public bool ClearProductDataGateway(IProductDataRpcGateway expected)
    {
        ArgumentNullException.ThrowIfNull(expected);
        return ReferenceEquals(
            Interlocked.CompareExchange(
                ref _productDataGateway,
                null,
                expected),
            expected);
    }

    public void SetDashboardGateway(
        IDashboardRpcGateway gateway,
        CancellationToken sessionToken = default)
    {
        _dashboardGateway = gateway ?? throw new ArgumentNullException(nameof(gateway));
        _dashboardSessionToken = sessionToken;
    }

    public void SetDocumentWorkspace(WorkspaceDocumentOsAdapter documents)
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
                Trace.TraceError($"Workspace request failed ({request.Type}): {ex}");
                _reply.PostOperationFailed(
                    request.RequestId,
                    "Workspace operation failed.",
                    code: "WORKSPACE_ERROR");
            }
        });
    }

    private async Task DispatchAsync(RoutedWebRequest request)
    {
        if (ProductDataRpcRegistry.Contains(request.Type))
        {
            await OnProductDataRequestAsync(request).ConfigureAwait(false);
            return;
        }
        if (RelationLookupRpcRegistry.Contains(request.Type))
        {
            await OnRelationLookupRequestAsync(request).ConfigureAwait(false);
            return;
        }

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
            case "history.queryRequested":
                await OnHistoryQueryRequestedAsync(request).ConfigureAwait(false);
                break;
            case "history.previewRestoreRequested":
                await OnHistoryPreviewRestoreRequestedAsync(request).ConfigureAwait(false);
                break;
            case "history.applyRestoreRequested":
                await OnHistoryApplyRestoreRequestedAsync(request).ConfigureAwait(false);
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
            case "identifierMappings.reconcileRequested":
                await OnReconcileIdentifierMappingsAsync(request).ConfigureAwait(false);
                break;
            case "dashboard.listRequested":
                await OnDashboardListRequestedAsync(request).ConfigureAwait(false);
                break;
            case "dashboard.readRequested":
                await OnDashboardReadRequestedAsync(request).ConfigureAwait(false);
                break;
            case "dashboard.manifestRequested":
                await OnDashboardManifestRequestedAsync(request).ConfigureAwait(false);
                break;
            case "dashboard.queryRequested":
                await OnDashboardQueryRequestedAsync(request).ConfigureAwait(false);
                break;
            case "dashboard.saveRequested":
                await OnDashboardSaveRequestedAsync(request).ConfigureAwait(false);
                break;
            case "dashboard.deleteRequested":
                await OnDashboardDeleteRequestedAsync(request).ConfigureAwait(false);
                break;
            case "dashboard.cancelRequested":
                OnDashboardCancelRequested(request);
                break;
            case "document.listRequested":
                await OnDocumentListRequestedAsync(request).ConfigureAwait(false);
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
                features = new HostFeatureFlags(
                    _dashboardFeatures.Enabled,
                    _autoDateFeatures.Enabled),
            });
            return;
        }

        var result = await _workspace.OpenDatabaseAsync(path).ConfigureAwait(false);
        _reply.PostNotification("database.opened", new
        {
            tables = result.Tables,
            views = result.Views,
            displayNames = result.DisplayNames,
            features = new HostFeatureFlags(
                _dashboardFeatures.Enabled,
                _autoDateFeatures.Enabled),
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
        string? expectedDigest = TryGetString(request.Payload, "expectedDigest");
        if (expectedDigest is not null
            && (expectedDigest.Length != 71
                || !expectedDigest.StartsWith("sha256:", StringComparison.Ordinal)
                || expectedDigest[7..].Any(character =>
                    !((character >= '0' && character <= '9')
                      || (character >= 'a' && character <= 'f')))))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "Invalid update-cell digest guard.",
                "BAD_PAYLOAD");
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
        await _workspace.InsertRowAsync(
            table,
            values,
            schemaRevision,
            request.RequestId).ConfigureAwait(false);
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
        await _workspace.DeleteRowsAsync(
            table,
            rows,
            schemaRevision,
            request.RequestId).ConfigureAwait(false);
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
    // G1: permission-filtered history + two-phase safe restore.
    // -------------------------------------------------------------------

    private async Task OnHistoryQueryRequestedAsync(RoutedWebRequest request)
    {
        string? collection = TryGetCollection(request.Payload);
        string scope = TryGetString(request.Payload, "scope") ?? "table";
        string? itemId = TryGetScalarString(request.Payload, "itemId");
        string? field = TryGetString(request.Payload, "field");
        if (string.IsNullOrWhiteSpace(collection)
            || !IsHistoryQueryScope(scope)
            || (scope is "row" or "cell" && string.IsNullOrWhiteSpace(itemId))
            || (scope == "cell" && string.IsNullOrWhiteSpace(field)))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "history.queryRequested 的表、范围或选择无效。",
                "BAD_PAYLOAD");
            return;
        }

        IReadOnlyList<string>? actions = null;
        if (TryGetProperty(request.Payload, "actions", out _))
        {
            actions = TryGetStringArray(request.Payload, "actions");
            if (actions is null)
            {
                _reply.PostOperationFailed(
                    request.RequestId,
                    "history.queryRequested 的 actions 必须是字符串数组。",
                    "BAD_PAYLOAD");
                return;
            }
        }

        var parameters = new ReadChangeSetsParams(
            Collection: collection,
            ItemId: itemId,
            Limit: Math.Clamp(TryGetInt(request.Payload, "limit", 50), 1, 100),
            Offset: Math.Max(0, TryGetInt(request.Payload, "offset", 0)),
            Scope: scope,
            Field: field,
            Search: TryGetString(request.Payload, "search"),
            DateFrom: TryGetString(request.Payload, "dateFrom"),
            DateTo: TryGetString(request.Payload, "dateTo"),
            ActorId: TryGetString(request.Payload, "actorId"),
            Actions: actions ?? Array.Empty<string>(),
            RecordId: TryGetScalarString(request.Payload, "recordId"));
        try
        {
            var page = await ReadHistoryWithRecoveryAsync(parameters)
                .ConfigureAwait(false);
            _reply.PostResponse("history.pageLoaded", request.RequestId, page);
        }
        catch (Exception ex)
        {
            PostHistoryFailure(request, ex, "HISTORY_QUERY_FAILED");
        }
    }

    private async Task OnHistoryPreviewRestoreRequestedAsync(RoutedWebRequest request)
    {
        string? collection = TryGetCollection(request.Payload);
        string scope = TryGetString(request.Payload, "scope") ?? "row";
        string? itemId = TryGetScalarString(request.Payload, "itemId");
        string? targetRevision = TryGetString(request.Payload, "targetRevision");
        string? field = TryGetString(request.Payload, "field");
        if (string.IsNullOrWhiteSpace(collection)
            || !IsRestoreScope(scope)
            || string.IsNullOrWhiteSpace(itemId)
            || string.IsNullOrWhiteSpace(targetRevision)
            || (scope == "cell" && string.IsNullOrWhiteSpace(field)))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "history.previewRestoreRequested 的表、范围或目标修订无效。",
                "BAD_PAYLOAD");
            return;
        }

        try
        {
            var preview = await _workspace.Gateway.PreviewRestoreAsync(
                new PreviewRestoreParams(
                    collection, itemId, targetRevision, scope, field),
                CancellationToken.None).ConfigureAwait(false);
            _reply.PostResponse(
                "history.restorePreviewReady", request.RequestId, preview);
        }
        catch (Exception ex)
        {
            PostHistoryFailure(request, ex, "HISTORY_PREVIEW_FAILED");
        }
    }

    private async Task OnHistoryApplyRestoreRequestedAsync(RoutedWebRequest request)
    {
        string? collection = TryGetCollection(request.Payload);
        string? itemId = TryGetScalarString(request.Payload, "itemId");
        string? token = TryGetString(request.Payload, "token");
        if (string.IsNullOrWhiteSpace(collection)
            || string.IsNullOrWhiteSpace(itemId)
            || string.IsNullOrWhiteSpace(token))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "history.applyRestoreRequested 缺少表、记录或预览令牌。",
                "BAD_PAYLOAD");
            return;
        }

        try
        {
            var result = await _workspace.Gateway.ApplyRestoreAsync(
                new ApplyRestoreParams(collection, itemId, token),
                CancellationToken.None).ConfigureAwait(false);
            _reply.PostResponse("history.restoreApplied", request.RequestId, result);
        }
        catch (Exception ex)
        {
            PostHistoryFailure(request, ex, "HISTORY_APPLY_FAILED");
        }
    }

    private void PostHistoryFailure(
        RoutedWebRequest request,
        Exception exception,
        string fallbackCode)
    {
        Trace.TraceError($"History request failed ({fallbackCode}): {exception}");
        var failure = HistoryErrorMapper.Map(exception, fallbackCode);
        _reply.PostOperationFailed(request.RequestId, failure.Message, failure.Code);
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
            catch (Exception ex)
                when (ex is BackendUnavailableException
                    or ObjectDisposedException)
            {
                lastFailure = ex;
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

    private static bool IsHistoryQueryScope(string scope)
        => scope is "table" or "row" or "cell" or "archived";

    private static bool IsRestoreScope(string scope)
        => scope is "row" or "cell" or "archived";

    private static string? TryGetCollection(JsonElement payload)
        => TryGetString(payload, "table") ?? TryGetString(payload, "collection");

    // -------------------------------------------------------------------
    // Table creation is one host-owned intent. Generic schema validation and
    // apply methods are deliberately not exposed to the renderer.
    // -------------------------------------------------------------------

    private async Task OnCreateTableRequestedAsync(RoutedWebRequest request)
    {
        string? displayName = TryGetString(request.Payload, "displayName")?.Trim();
        if (string.IsNullOrWhiteSpace(displayName)
            || displayName.Length > 128
            || displayName.Any(char.IsControl))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "创建数据表的请求无效。",
                code: "BAD_PAYLOAD");
            return;
        }
        IProductDataRpcGateway? gateway = Volatile.Read(ref _productDataGateway);
        if (gateway is null)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "本地数据服务尚未就绪。",
                code: "BACKEND_UNAVAILABLE");
            return;
        }
        try
        {
            string opaque = Guid.NewGuid().ToString("N")[..20];
            JsonElement definition = JsonSerializer.SerializeToElement(new
            {
                contractVersion = "1.0",
                tableId = "tbl_" + opaque,
                physicalName = "t_" + opaque,
                displayName,
                kind = "base",
                schemaRevision = "schema_0000",
                archivePolicy = new
                {
                    mode = "none",
                    fieldId = (string?)null,
                    archivedValue = (object?)null,
                },
                fields = Array.Empty<object>(),
                indexes = Array.Empty<object>(),
            });
            const int expectedRevision = 0;
            JsonElement change = JsonSerializer.SerializeToElement(new
            {
                definition,
                expectedRevision,
            });
            JsonElement validation = await gateway.ValidateSchemaAsync(
                change,
                CancellationToken.None).ConfigureAwait(false);
            if (validation.ValueKind == JsonValueKind.Object
                && validation.TryGetProperty("error", out JsonElement validationError)
                && validationError.ValueKind != JsonValueKind.Null)
            {
                throw new InvalidOperationException(
                    "Schema validation rejected the table definition.");
            }
            JsonElement normalized = validation.ValueKind == JsonValueKind.Object
                && validation.TryGetProperty("definition", out JsonElement validated)
                && validated.ValueKind == JsonValueKind.Object
                    ? validated
                    : definition;
            JsonElement applied = await gateway.ApplySchemaAsync(
                JsonSerializer.SerializeToElement(new
                {
                    definition = normalized,
                    expectedRevision,
                }),
                CancellationToken.None).ConfigureAwait(false);
            if (applied.ValueKind == JsonValueKind.Object
                && applied.TryGetProperty("error", out JsonElement applyError)
                && applyError.ValueKind != JsonValueKind.Null)
            {
                throw new InvalidOperationException(
                    "Schema apply rejected the table definition.");
            }
            await RefreshCollectionListAsync().ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            Trace.TraceError($"Create table failed: {ex}");
            _reply.PostOperationFailed(
                request.RequestId,
                "创建数据表失败。",
                code: "SCHEMA_APPLY_FAILED");
        }
    }

    private async Task OnDeleteTableRequestedAsync(RoutedWebRequest request)
    {
        string? collection = TryGetString(request.Payload, "collection");
        if (string.IsNullOrWhiteSpace(collection))
        {
            _reply.PostOperationFailed(request.RequestId, "缺少表名。", code: "BAD_PAYLOAD");
            return;
        }
        IProductDataRpcGateway? gateway = Volatile.Read(ref _productDataGateway);
        if (gateway is null)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "本地数据服务尚未就绪。",
                code: "BACKEND_UNAVAILABLE");
            return;
        }
        try
        {
            JsonElement schema = await gateway.GetTableSchemaAsync(
                JsonSerializer.SerializeToElement(new { tableId = collection }),
                CancellationToken.None).ConfigureAwait(false);
            string? revision = TryGetString(schema, "schemaRevision");
            if (string.IsNullOrWhiteSpace(revision))
            {
                throw new InvalidOperationException("结构版本不可用。");
            }
            await gateway.DeleteSchemaAsync(
                JsonSerializer.SerializeToElement(new
                {
                    tableId = collection,
                    expectedRevision = revision,
                }),
                CancellationToken.None).ConfigureAwait(false);
            await RefreshCollectionListAsync().ConfigureAwait(false);
        }
        catch (RpcRemoteException ex) when (ex.Code == -32602)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "删除数据表的请求无效。",
                code: "BAD_PAYLOAD");
        }
        catch (Exception ex)
        {
            Trace.TraceError($"Schema delete failed ({collection}): {ex}");
            _reply.PostOperationFailed(
                request.RequestId,
                "删除数据表失败。",
                code: "SCHEMA_DELETE_FAILED");
        }
    }

    private async Task OnListIdentifierMappingsAsync(RoutedWebRequest request)
    {
        await RunIdentifierRequestAsync(
            request,
            (gateway, token) => gateway.ListIdentifierMappingsAsync(
                request.Payload,
                token)).ConfigureAwait(false);
    }

    private async Task OnUpdateIdentifierAliasesAsync(RoutedWebRequest request)
    {
        await RunIdentifierRequestAsync(
            request,
            (gateway, token) => gateway.UpdateIdentifierAliasesAsync(
                request.Payload,
                token)).ConfigureAwait(false);
    }

    private async Task OnReconcileIdentifierMappingsAsync(RoutedWebRequest request)
    {
        await RunIdentifierRequestAsync(
            request,
            (gateway, token) => gateway.ReconcileIdentifierMappingsAsync(
                request.Payload,
                token)).ConfigureAwait(false);
    }

    private async Task RunIdentifierRequestAsync(
        RoutedWebRequest request,
        Func<IProductDataRpcGateway, CancellationToken, Task<JsonElement>> action)
    {
        IProductDataRpcGateway? gateway = Volatile.Read(ref _productDataGateway);
        if (gateway is null)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "本地数据服务尚未就绪。",
                "BACKEND_UNAVAILABLE");
            return;
        }
        try
        {
            JsonElement result = await action(gateway, CancellationToken.None)
                .ConfigureAwait(false);
            _reply.PostResponse(
                "identifierMappings.result",
                request.RequestId,
                result);
        }
        catch (RpcRemoteException ex) when (ex.Code == -32602)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "标识映射请求参数无效。",
                "BAD_PAYLOAD");
        }
        catch (Exception ex)
        {
            Trace.TraceError(
                $"Identifier mapping request failed ({request.Type}): {ex}");
            _reply.PostOperationFailed(
                request.RequestId,
                "标识映射操作失败。",
                "IDENTIFIER_MAPPING_FAILED");
        }
    }

    // -------------------------------------------------------------------
    // Native dashboard bridge. Every operation is correlated, bounded and
    // feature-gated; no generic JSON-RPC method name crosses the WebView.
    // -------------------------------------------------------------------

    private Task OnDashboardListRequestedAsync(RoutedWebRequest request)
        => RunDashboardRequestAsync(
            request,
            "dashboard.listLoaded",
            isQuery: false,
            (gateway, token) => gateway.ListDashboardsAsync(token));

    private async Task OnDashboardReadRequestedAsync(RoutedWebRequest request)
    {
        if (!TryRequireDashboardFeature(request)) return;
        string? dashboardId = TryGetString(request.Payload, "dashboardId");
        if (string.IsNullOrWhiteSpace(dashboardId))
        {
            PostDashboardPayloadFailure(request, "缺少仪表盘标识。");
            return;
        }
        await RunDashboardRequestAsync(
            request,
            "dashboard.loaded",
            isQuery: false,
            (gateway, token) => gateway.ReadDashboardWorkspaceAsync(dashboardId, token))
            .ConfigureAwait(false);
    }

    private Task OnDashboardManifestRequestedAsync(RoutedWebRequest request)
        => RunDashboardRequestAsync(
            request,
            "dashboard.manifestLoaded",
            isQuery: false,
            LoadDashboardManifestAsync);

    private async Task OnDashboardQueryRequestedAsync(RoutedWebRequest request)
    {
        if (!TryRequireDashboardFeature(request)) return;
        if (!TryDeserializeDashboardPayload<ExecuteDashboardQueryParams>(
                request.Payload, out var parameters)
            || parameters is null
            || string.IsNullOrWhiteSpace(parameters.PanelType)
            || parameters.Query is null)
        {
            PostDashboardPayloadFailure(request, "仪表盘查询参数无效。");
            return;
        }
        parameters = parameters with { RequestId = request.RequestId };
        await RunDashboardRequestAsync(
            request,
            "dashboard.queryLoaded",
            isQuery: true,
            (gateway, token) => gateway.ExecuteDashboardQueryAsync(parameters, token))
            .ConfigureAwait(false);
    }

    private async Task OnDashboardSaveRequestedAsync(RoutedWebRequest request)
    {
        if (!TryRequireDashboardFeature(request)) return;
        if (!TryDeserializeDashboardPayload<SaveDashboardDraftParams>(
                request.Payload, out var parameters)
            || parameters is null
            || string.IsNullOrWhiteSpace(parameters.Name)
            || string.IsNullOrWhiteSpace(parameters.IdempotencyKey))
        {
            PostDashboardPayloadFailure(request, "仪表盘草稿参数无效。");
            return;
        }
        await RunDashboardRequestAsync(
            request,
            "dashboard.saved",
            isQuery: false,
            (gateway, token) => gateway.SaveDashboardDraftAsync(parameters, token))
            .ConfigureAwait(false);
    }

    private async Task OnDashboardDeleteRequestedAsync(RoutedWebRequest request)
    {
        if (!TryRequireDashboardFeature(request)) return;
        string? dashboardId = TryGetString(request.Payload, "dashboardId");
        if (string.IsNullOrWhiteSpace(dashboardId))
        {
            PostDashboardPayloadFailure(request, "缺少仪表盘标识。");
            return;
        }
        await RunDashboardRequestAsync(
            request,
            "dashboard.deleted",
            isQuery: false,
            (gateway, token) => gateway.DeleteDashboardAsync(dashboardId, token))
            .ConfigureAwait(false);
    }

    private void OnDashboardCancelRequested(RoutedWebRequest request)
    {
        if (!TryRequireDashboardFeature(request)) return;
        string? targetRequestId = TryGetString(request.Payload, "targetRequestId");
        if (string.IsNullOrWhiteSpace(targetRequestId))
        {
            PostDashboardPayloadFailure(request, "缺少待取消的请求标识。");
            return;
        }
        if (_dashboardRequests.TryGetValue(targetRequestId, out var state))
        {
            state.MarkCancelledByRenderer();
            // Publish cancellation only after the token is observably
            // cancelled. Otherwise a renderer unblocked by the reply can make
            // an ignored backend task fail before TryCancel runs, producing a
            // second, generic operation.failed response.
            state.TryCancel();
            if (state.TryMarkCancellationReply())
            {
                _reply.PostOperationFailed(
                    targetRequestId,
                    "仪表盘请求已取消。",
                    "DASHBOARD_CANCELLED");
            }
        }
    }

    private async Task RunDashboardRequestAsync<TResult>(
        RoutedWebRequest request,
        string responseType,
        bool isQuery,
        Func<IDashboardRpcGateway, CancellationToken, Task<TResult>> operation)
    {
        if (!TryRequireDashboardFeature(request)) return;
        if (_dashboardGateway is null)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "仪表盘服务尚未连接。",
                "NOT_AUTHENTICATED");
            return;
        }
        if (string.IsNullOrWhiteSpace(request.RequestId))
        {
            PostDashboardPayloadFailure(request, "仪表盘请求必须携带 requestId。");
            return;
        }

        using var cancellation = _dashboardSessionToken.CanBeCanceled
            ? CancellationTokenSource.CreateLinkedTokenSource(_dashboardSessionToken)
            : new CancellationTokenSource();
        cancellation.CancelAfter(_dashboardRequestTimeout);
        var state = new DashboardRequestState(cancellation);
        if (!_dashboardRequests.TryAdd(request.RequestId, state))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "仪表盘请求标识重复。",
                "DASHBOARD_DUPLICATE_REQUEST");
            return;
        }

        bool queryLease = false;
        try
        {
            if (isQuery)
            {
                await _dashboardQueryGate.WaitAsync(cancellation.Token).ConfigureAwait(false);
                queryLease = true;
            }
            TResult result = await operation(_dashboardGateway, cancellation.Token)
                .ConfigureAwait(false);
            if (!cancellation.IsCancellationRequested
                && _dashboardRequests.TryGetValue(request.RequestId, out var current)
                && ReferenceEquals(current, state))
            {
                _reply.PostResponse(responseType, request.RequestId, result);
            }
        }
        catch (OperationCanceledException)
        {
            bool timeout = !state.CancelledByRenderer
                && !_dashboardSessionToken.IsCancellationRequested;
            if (state.TryMarkCancellationReply())
            {
                _reply.PostOperationFailed(
                    request.RequestId,
                    timeout ? "仪表盘请求超时。" : "仪表盘请求已取消。",
                    timeout ? "DASHBOARD_TIMEOUT" : "DASHBOARD_CANCELLED");
            }
        }
        catch (Exception exception)
        {
            // A backend implementation may complete with a non-cancellation
            // exception after its token has already been cancelled. Preserve
            // the original cancel/timeout semantics and suppress duplicate
            // late failures in that case.
            if (state.CancelledByRenderer || cancellation.IsCancellationRequested)
            {
                bool timeout = !state.CancelledByRenderer
                    && !_dashboardSessionToken.IsCancellationRequested;
                if (state.TryMarkCancellationReply())
                {
                    _reply.PostOperationFailed(
                        request.RequestId,
                        timeout ? "仪表盘请求超时。" : "仪表盘请求已取消。",
                        timeout ? "DASHBOARD_TIMEOUT" : "DASHBOARD_CANCELLED");
                }
                return;
            }
            Trace.TraceError($"Dashboard request failed: {exception}");
            var failure = DashboardErrorMapper.Map(exception);
            _reply.PostOperationFailed(request.RequestId, failure.Message, failure.Code);
        }
        finally
        {
            if (queryLease) _dashboardQueryGate.Release();
            _dashboardRequests.TryRemove(request.RequestId, out _);
        }
    }

    private static async Task<DashboardManifestBundle> LoadDashboardManifestAsync(
        IDashboardRpcGateway gateway,
        CancellationToken token)
    {
        Task<PanelManifestResult> manifest = gateway.GetPanelManifestAsync(token);
        Task<DashboardQueryLimits> limits = gateway.GetDashboardQueryLimitsAsync(token);
        await Task.WhenAll(manifest, limits).ConfigureAwait(false);
        return new DashboardManifestBundle(
            await manifest.ConfigureAwait(false),
            await limits.ConfigureAwait(false));
    }

    private bool TryRequireDashboardFeature(RoutedWebRequest request)
    {
        if (_dashboardFeatures.Enabled) return true;
        _reply.PostOperationFailed(
            request.RequestId,
            "仪表盘功能尚未启用。",
            "DASHBOARD_DISABLED");
        return false;
    }

    private void PostDashboardPayloadFailure(RoutedWebRequest request, string message)
        => _reply.PostOperationFailed(request.RequestId, message, "BAD_PAYLOAD");

    private static bool TryDeserializeDashboardPayload<T>(
        JsonElement payload,
        out T? value)
    {
        try
        {
            if (payload.ValueKind != JsonValueKind.Object)
            {
                value = default;
                return false;
            }
            value = payload.Deserialize<T>(new JsonSerializerOptions(JsonSerializerDefaults.Web));
            return value is not null;
        }
        catch (JsonException)
        {
            value = default;
            return false;
        }
        catch (NotSupportedException)
        {
            value = default;
            return false;
        }
    }

    private sealed class DashboardRequestState
    {
        private int _cancelledByRenderer;
        private int _cancellationReplyPosted;

        public DashboardRequestState(CancellationTokenSource cancellation)
            => Cancellation = cancellation;

        public CancellationTokenSource Cancellation { get; }
        public bool CancelledByRenderer => Volatile.Read(ref _cancelledByRenderer) != 0;
        public void MarkCancelledByRenderer()
            => Interlocked.Exchange(ref _cancelledByRenderer, 1);
        public void TryCancel()
        {
            try { Cancellation.Cancel(); }
            catch (ObjectDisposedException) { }
        }
        public bool TryMarkCancellationReply()
            => Interlocked.Exchange(ref _cancellationReplyPosted, 1) == 0;
    }

    // Product relation + realtime Lookup. This is a closed dispatch table:
    // no renderer-provided method name can reach JsonRpcClient.
    // -------------------------------------------------------------------

    private async Task OnRelationLookupRequestAsync(RoutedWebRequest request)
    {
        if (!RelationLookupRpcRegistry.TryGet(request.Type, out var endpoint))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                $"Unhandled relation/Lookup request '{request.Type}'.",
                "UNKNOWN_TYPE");
            return;
        }

        if (!endpoint.IsValidPayload(request.Payload))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                $"{request.Type} has an invalid payload.",
                "BAD_PAYLOAD");
            return;
        }
        IProductDataRpcGateway? productGateway =
            Volatile.Read(ref _productDataGateway);
        IRelationLookupRpcGateway? relationGateway = productGateway;
        if (relationGateway is null)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "数据服务尚未就绪。",
                "NOT_AUTHENTICATED");
            return;
        }

        try
        {
            JsonElement result = await endpoint.InvokeAsync(
                relationGateway,
                request.Payload,
                CancellationToken.None).ConfigureAwait(false);
            _reply.PostResponse(request.Type, request.RequestId, result);
        }
        catch (JsonException)
        {
            _reply.PostOperationFailed(request.RequestId, "Invalid request payload.", "BAD_PAYLOAD");
        }
        catch (RpcRemoteException ex) when (ex.Code == -32602)
        {
            // Canonical Pydantic validation happens in Python. Normalize its
            // JSON-RPC invalid-params response at the WebView boundary.
            _reply.PostOperationFailed(request.RequestId, "Invalid request payload.", "BAD_PAYLOAD");
        }
        catch (RpcRemoteException ex) when (ex.Code == -32030)
        {
            // The gateway can exist while the private sidecar is recovering.
            // Never expose loopback or credential details to the page.
            _reply.PostOperationFailed(
                request.RequestId,
                "Local data service is unavailable.",
                "BACKEND_UNAVAILABLE");
        }
        catch (Exception ex)
        {
            Trace.TraceError($"Relation/Lookup request failed ({request.Type}): {ex}");
            _reply.PostOperationFailed(
                request.RequestId,
                "Relation or lookup operation failed.",
                "RELATION_LOOKUP_FAILED");
        }
    }

    private async Task OnProductDataRequestAsync(RoutedWebRequest request)
    {
        WorkspaceRequestEpochLease? epochLease = null;
        if (_sessionEnvelopeFilter is not null
            && !_sessionEnvelopeFilter.TryCapture(request.Scope, out epochLease))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "Workspace request belongs to a stale or invalid session.",
                "BAD_WORKSPACE_SCOPE");
            return;
        }

        if (!ProductDataRpcRegistry.TryGet(request.Type, out var endpoint))
        {
            _reply.PostOperationFailed(request.RequestId, "Unknown product data request.", "UNKNOWN_TYPE");
            epochLease?.Dispose();
            return;
        }
        if (!endpoint.IsValidPayload(request.Payload))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                $"{request.Type} has an invalid payload.",
                "BAD_PAYLOAD");
            epochLease?.Dispose();
            return;
        }
        if (endpoint.MutatesWorkspace &&
            _sessionEnvelopeFilter?.Current.OpenMode ==
                WorkspaceOpenMode.ReadOnly)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "The workspace is open read-only.",
                "WORKSPACE_READ_ONLY");
            epochLease?.Dispose();
            return;
        }
        try
        {
            JsonElement forwardedPayload = request.Payload;
            if (endpoint.RequiresProtectionSnapshot &&
                _sessionEnvelopeFilter is not null)
            {
                ProtectionSnapshotReceipt? protection =
                    await _sessionEnvelopeFilter.ProtectCurrentWithReceiptAsync(
                    $"before-{request.Type}",
                    epochLease?.CancellationToken
                        ?? CancellationToken.None).ConfigureAwait(false);
                if (!IsRequestCurrent(epochLease))
                    return;
                if (protection is not null)
                {
                    string propertyName = string.Equals(
                        request.Type,
                        "field.change.plan",
                        StringComparison.Ordinal)
                            ? "backupReceipt"
                            : string.Equals(
                                request.Type,
                                "field.change.apply",
                                StringComparison.Ordinal)
                                    ? "protectionSnapshotId"
                                    : "";
                    if (propertyName.Length > 0)
                    {
                        forwardedPayload = WithStringProperty(
                            request.Payload,
                            propertyName,
                            protection.SnapshotId.ToString("D"));
                    }
                }
            }
            IProductDataRpcGateway? productGateway =
                Volatile.Read(ref _productDataGateway);
            JsonElement result = string.Equals(
                request.Type,
                "query.page",
                StringComparison.Ordinal)
                    ? await InvokeRecoverableProductReadAsync(
                        endpoint,
                        request.Payload,
                        productGateway,
                        epochLease).ConfigureAwait(false)
                    : productGateway is null
                        ? throw new BackendUnavailableException(
                            "The local data service is not ready.")
                        : await endpoint.InvokeAsync(
                            productGateway,
                            forwardedPayload,
                            epochLease?.CancellationToken
                                ?? CancellationToken.None).ConfigureAwait(false);
            if (!IsRequestCurrent(epochLease))
                return;
            _reply.PostResponse(request.Type, request.RequestId, result);
            if (string.Equals(request.Type, "schema.apply", StringComparison.Ordinal)
                || string.Equals(request.Type, "field.change.apply", StringComparison.Ordinal))
            {
                await RefreshCollectionListAsync().ConfigureAwait(false);
            }
        }
        catch (OperationCanceledException)
            when (epochLease?.CancellationToken.IsCancellationRequested == true)
        {
            // A workspace switch invalidated the request. The old response
            // must not leak into the newly active renderer session.
        }
        catch (RpcRemoteException ex)
            when (ex.ErrorData is JsonElement data
                && ProductRpcErrorMapper.TryMap(data, out _))
        {
            if (!IsRequestCurrent(epochLease))
                return;
            ProductRpcErrorMapper.TryMap(ex.ErrorData!.Value, out var mapped);
            _reply.PostResponse(request.Type, request.RequestId, mapped);
        }
        catch (RpcRemoteException ex) when (ex.Code == -32602)
        {
            if (!IsRequestCurrent(epochLease))
                return;
            _reply.PostOperationFailed(request.RequestId, "Invalid request payload.", "BAD_PAYLOAD");
        }
        catch (WorkspaceRegistryException ex)
        {
            if (!IsRequestCurrent(epochLease))
                return;
            _reply.PostOperationFailed(
                request.RequestId,
                ex.Message,
                ex.Code);
        }
        catch (Exception ex)
            when (ex is BackendUnavailableException
                or ObjectDisposedException)
        {
            if (!IsRequestCurrent(epochLease))
                return;
            Trace.TraceWarning(
                $"Product data backend unavailable ({request.Type}): {ex.Message}");
            _reply.PostOperationFailed(
                request.RequestId,
                "Local data service is reconnecting.",
                "BACKEND_UNAVAILABLE");
        }
        catch (Exception ex)
        {
            if (!IsRequestCurrent(epochLease))
                return;
            Trace.TraceError($"Product data request failed ({request.Type}): {ex}");
            _reply.PostOperationFailed(
                request.RequestId,
                "Product data operation failed.",
                "PRODUCT_DATA_FAILED");
        }
        finally
        {
            epochLease?.Dispose();
        }
    }

    private static JsonElement WithStringProperty(
        JsonElement payload,
        string propertyName,
        string value)
    {
        var properties = payload.EnumerateObject().ToDictionary(
            property => property.Name,
            property => property.Value.Clone(),
            StringComparer.Ordinal);
        properties[propertyName] = JsonSerializer.SerializeToElement(value);
        return JsonSerializer.SerializeToElement(properties);
    }

    private async Task<JsonElement> InvokeRecoverableProductReadAsync(
        ProductDataRpcEndpoint endpoint,
        JsonElement payload,
        IProductDataRpcGateway? initialGateway,
        WorkspaceRequestEpochLease? epochLease)
    {
        IProductDataRpcGateway? attemptedGateway = initialGateway;
        Exception? lastFailure = null;
        long deadline = Stopwatch.GetTimestamp()
            + (long)(_readRecoveryTimeout.TotalSeconds * Stopwatch.Frequency);

        while (true)
        {
            epochLease?.CancellationToken.ThrowIfCancellationRequested();
            if (!IsRequestCurrent(epochLease))
                throw new OperationCanceledException(
                    epochLease?.CancellationToken ?? CancellationToken.None);

            if (attemptedGateway is not null)
            {
                try
                {
                    JsonElement result = await endpoint.InvokeAsync(
                        attemptedGateway,
                        payload,
                        epochLease?.CancellationToken
                            ?? CancellationToken.None).ConfigureAwait(false);
                    if (!IsRequestCurrent(epochLease))
                        throw new OperationCanceledException(
                            epochLease?.CancellationToken
                                ?? CancellationToken.None);
                    return result;
                }
                catch (Exception ex)
                    when (ex is BackendUnavailableException
                        or ObjectDisposedException)
                {
                    lastFailure = ex;
                }
            }

            IProductDataRpcGateway? replacement =
                Volatile.Read(ref _productDataGateway);
            if (replacement is not null
                && !ReferenceEquals(replacement, attemptedGateway))
            {
                attemptedGateway = replacement;
                continue;
            }
            if (Stopwatch.GetTimestamp() >= deadline)
            {
                throw new BackendUnavailableException(
                    "The local data service did not recover before the read deadline.",
                    lastFailure ?? new InvalidOperationException(
                        "No product data gateway is currently available."));
            }
            await Task.Delay(
                    RecoveryReadPollInterval,
                    epochLease?.CancellationToken
                        ?? CancellationToken.None)
                .ConfigureAwait(false);
        }
    }

    private bool IsRequestCurrent(WorkspaceRequestEpochLease? epochLease)
        => epochLease is null
            || _sessionEnvelopeFilter?.IsCurrent(epochLease) == true;

    private async Task RefreshCollectionListAsync()
    {
        TableSummary summary = await _workspace.Gateway.ListTablesAsync(
            CancellationToken.None).ConfigureAwait(false);
        _workspace.UpdateKnownTables(summary.Tables);
        _reply.PostNotification("database.collectionsChanged", new
        {
            tables = summary.Tables,
            displayNames = summary.DisplayNames
                ?? new Dictionary<string, string>(),
        });
    }

    private async Task OnDocumentListRequestedAsync(RoutedWebRequest request)
    {
        if (!TryRequireDocuments(request)) return;
        string? authority = TryGetString(request.Payload, "authority");
        if (!string.Equals(authority, "workspace", StringComparison.Ordinal))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "当前入口仅支持本地工作区文档。",
                "DOCUMENT_AUTHORITY_UNSUPPORTED");
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
                result = await _documents!.ListRecordAsync(
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
        // Build a TableQuery from the payload's "query" object. Only the
        // frozen contract fields are forwarded, while filter values retain
        // their JSON scalar/array/object shape for the provider-neutral AST.
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
            int groupOffset = q.TryGetProperty("groupOffset", out var groupOff)
                && groupOff.TryGetInt32(out var parsedGroupOffset) ? parsedGroupOffset : 0;
            int groupLimit = q.TryGetProperty("groupLimit", out var groupLim)
                && groupLim.TryGetInt32(out var parsedGroupLimit) ? parsedGroupLimit : 100;
            return new TableQuery(
                keyword,
                ParseFilterExpressions(q),
                ParseSortConditions(q),
                offset,
                limit,
                ParseGroupConditions(q),
                ParseSummaryConditions(q),
                groupOffset,
                groupLimit);
        }
        return new TableQuery();
    }

    private static IReadOnlyList<FilterExpression>? ParseFilterExpressions(JsonElement query)
    {
        if (!query.TryGetProperty("filters", out var filters)
            || filters.ValueKind != JsonValueKind.Array)
        {
            return null;
        }
        return ParseFilterExpressionArray(filters);
    }

    private static IReadOnlyList<FilterExpression> ParseFilterExpressionArray(JsonElement filters)
    {
        var result = new List<FilterExpression>();
        foreach (var item in filters.EnumerateArray())
        {
            if (item.ValueKind != JsonValueKind.Object)
            {
                continue;
            }
            if (item.TryGetProperty("filters", out var childrenElement)
                && childrenElement.ValueKind == JsonValueKind.Array)
            {
                var children = ParseFilterExpressionArray(childrenElement);
                if (children.Count == 0)
                {
                    continue;
                }
                string groupLogic = item.TryGetProperty("groupLogic", out var groupLogicElement)
                    && groupLogicElement.ValueKind == JsonValueKind.String
                    && string.Equals(
                        groupLogicElement.GetString(),
                        "OR",
                        StringComparison.OrdinalIgnoreCase)
                        ? "OR"
                        : "AND";
                string groupConnector = item.TryGetProperty("logic", out var groupConnectorElement)
                    && groupConnectorElement.ValueKind == JsonValueKind.String
                    && string.Equals(
                        groupConnectorElement.GetString(),
                        "OR",
                        StringComparison.OrdinalIgnoreCase)
                        ? "OR"
                        : "AND";
                result.Add(new FilterExpression(
                    Logic: groupConnector,
                    Filters: children,
                    GroupLogic: groupLogic));
                continue;
            }
            if (!item.TryGetProperty("field", out var fieldElement)
                || fieldElement.ValueKind != JsonValueKind.String
                || string.IsNullOrWhiteSpace(fieldElement.GetString())
                || !item.TryGetProperty("operator", out var operatorElement)
                || operatorElement.ValueKind != JsonValueKind.String
                || !IsKnownFilterOperator(operatorElement.GetString()))
            {
                continue;
            }
            string logic = item.TryGetProperty("logic", out var logicElement)
                && logicElement.ValueKind == JsonValueKind.String
                && string.Equals(logicElement.GetString(), "OR", StringComparison.OrdinalIgnoreCase)
                    ? "OR"
                    : "AND";
            object? value = item.TryGetProperty("value", out var valueElement)
                ? ToObject(valueElement)
                : null;
            result.Add(new FilterCondition(
                fieldElement.GetString()!,
                operatorElement.GetString()!,
                value,
                logic));
        }
        return result;
    }

    private static IReadOnlyList<SortCondition>? ParseSortConditions(JsonElement query)
    {
        if (!query.TryGetProperty("sorts", out var sorts)
            || sorts.ValueKind != JsonValueKind.Array)
        {
            return null;
        }
        var result = new List<SortCondition>();
        foreach (var item in sorts.EnumerateArray())
        {
            if (item.ValueKind != JsonValueKind.Object
                || !item.TryGetProperty("field", out var fieldElement)
                || fieldElement.ValueKind != JsonValueKind.String
                || string.IsNullOrWhiteSpace(fieldElement.GetString()))
            {
                continue;
            }
            string direction = item.TryGetProperty("direction", out var directionElement)
                && directionElement.ValueKind == JsonValueKind.String
                && string.Equals(directionElement.GetString(), "desc", StringComparison.OrdinalIgnoreCase)
                    ? "desc"
                    : "asc";
            bool nullsLast = !item.TryGetProperty("nullsLast", out var nullsLastElement)
                || nullsLastElement.ValueKind != JsonValueKind.False;
            result.Add(new SortCondition(fieldElement.GetString()!, direction, nullsLast));
        }
        return result;
    }

    private static IReadOnlyList<GroupCondition>? ParseGroupConditions(JsonElement query)
    {
        if (!query.TryGetProperty("groups", out var groups)
            || groups.ValueKind != JsonValueKind.Array)
        {
            return null;
        }
        var result = new List<GroupCondition>();
        foreach (var item in groups.EnumerateArray().Take(2))
        {
            if (item.ValueKind != JsonValueKind.Object
                || !item.TryGetProperty("field", out var field)
                || field.ValueKind != JsonValueKind.String
                || string.IsNullOrWhiteSpace(field.GetString()))
            {
                continue;
            }
            string direction = item.TryGetProperty("direction", out var directionElement)
                && directionElement.ValueKind == JsonValueKind.String
                && string.Equals(directionElement.GetString(), "desc", StringComparison.OrdinalIgnoreCase)
                    ? "desc"
                    : "asc";
            string bucket = item.TryGetProperty("bucket", out var bucketElement)
                && bucketElement.ValueKind == JsonValueKind.String
                && bucketElement.GetString() is "year" or "quarter" or "month" or "week" or "day" or "hour"
                    ? bucketElement.GetString()!
                    : "value";
            result.Add(new GroupCondition(field.GetString()!, direction, bucket));
        }
        return result;
    }

    private static IReadOnlyList<SummaryCondition>? ParseSummaryConditions(JsonElement query)
    {
        if (!query.TryGetProperty("summaries", out var summaries)
            || summaries.ValueKind != JsonValueKind.Array)
        {
            return null;
        }
        var result = new List<SummaryCondition>();
        foreach (var item in summaries.EnumerateArray().Take(3))
        {
            if (item.ValueKind != JsonValueKind.Object
                || !item.TryGetProperty("field", out var field)
                || field.ValueKind != JsonValueKind.String
                || string.IsNullOrWhiteSpace(field.GetString())
                || !item.TryGetProperty("function", out var function)
                || function.ValueKind != JsonValueKind.String
                || function.GetString() is not ("sum" or "avg" or "min" or "max"))
            {
                continue;
            }
            result.Add(new SummaryCondition(field.GetString()!, function.GetString()!));
        }
        return result;
    }

    private static bool IsKnownFilterOperator(string? value)
        => value is FilterOperators.Contains
            or FilterOperators.Equal
            or FilterOperators.NotEqual
            or FilterOperators.StartsWith
            or FilterOperators.EndsWith
            or FilterOperators.Greater
            or FilterOperators.Less
            or FilterOperators.GreaterEqual
            or FilterOperators.LessEqual
            or FilterOperators.Between
            or FilterOperators.In
            or FilterOperators.IsNull
            or FilterOperators.IsNotNull
            or FilterOperators.Regex;

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
