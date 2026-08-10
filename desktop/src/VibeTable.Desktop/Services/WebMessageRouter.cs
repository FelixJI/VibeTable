using System;
using System.Collections.Generic;
using System.Text.Json;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Whitelisting message router that sits between the untrusted WebView2
/// boundary and the rest of the .NET host.
/// </summary>
/// <remarks>
/// <para>
/// Every inbound web message is one of:
/// </para>
/// <list type="bullet">
/// <item><b>Dispatched</b> — type is in the Phase A whitelist AND the host is
/// <see cref="IsReady"/> AND the payload is within size limits AND the JSON is
/// valid. The message is handed to the injected
/// <see cref="Action{RoutedWebRequest}"/> (which forwards to the JSON-RPC
/// client in production); the router returns <c>null</c> (no immediate
/// reply).</item>
/// <item><b>Rejected with <c>operation.failed</c></b> — unknown type, payload
/// over 4 MiB, invalid JSON, missing type, or any message received before the
/// host reached <see cref="IsReady"/>. The router returns a
/// <see cref="HostReplyMessage"/> envelope of type <c>operation.failed</c>;
/// the message MUST NOT reach <see cref="JsonRpcClient"/> / the backend.
/// </item>
/// </list>
/// <para>
/// Host -&gt; web notifications use a SEPARATE whitelist
/// (<see cref="IsHostNotificationAllowed"/>). RPC notifications are NOT
/// generically forwarded; each future business notification requires an
/// explicit C# DTO + mapping (Phase A defines only the three structural
/// notification types from <c>desktop/web-grid/src/contracts.ts</c>).
/// </para>
/// </remarks>
public sealed class WebMessageRouter
{
    /// <summary>
    /// Maximum total inbound message size (UTF-8 bytes). 4 MiB matches the
    /// cap asserted by the web-grid side and bounds memory pressure from a
    /// malicious / buggy renderer.
    /// </summary>
    public const int MaxMessageBytes = 4 * 1024 * 1024;

    /// <summary>
    /// The Phase A + B1 + B3 web request types (verbatim from
    /// <c>desktop/web-grid/src/contracts.ts</c>). B3 adds
    /// <c>table.queryRequested</c> (debounced remote query) and
    /// <c>gridState.saveRequested</c> (debounced state save).
    /// </summary>
    private static readonly HashSet<string> WebRequestWhitelist = new(StringComparer.Ordinal)
    {
        "app.ready",
        "host.startupRetryRequested",
        "host.startupCancelRequested",
        "database.openRequested",
        "table.selected",
        "table.pageRequested",
        // B1 mutation requests.
        "table.updateCellRequested",
        "table.insertRowRequested",
        "table.deleteRowsRequested",
        // B3 query + state requests.
        "table.queryRequested",
        "gridState.saveRequested",
        // B2 paste preview + apply requests.
        "table.previewPasteRequested",
        "table.applyPasteRequested",
        "data.importSourceRequested",
        "data.exportTargetRequested",
        "dailyQuote.fetch",
        "diagnostics.get",
        "appPreferences.get",
        "appPreferences.update",
        "update.check",
        "update.install",
        // G1 history query + two-phase safe restore.
        "history.queryRequested",
        "history.previewRestoreRequested",
        "history.applyRestoreRequested",
        // Table management (web sidebar).
        "tableAdmin.createRequested",
        "tableAdmin.deleteRequested",
        "identifierMappings.listRequested",
        "identifierMappings.updateAliasesRequested",
        "identifierMappings.reconcileRequested",
        // Native dashboards. Each entry maps to one typed use case; there is
        // deliberately no generic provider/rpc invocation message.
        "dashboard.listRequested",
        "dashboard.readRequested",
        "dashboard.manifestRequested",
        "dashboard.queryRequested",
        "dashboard.saveRequested",
        "dashboard.deleteRequested",
        "dashboard.cancelRequested",
        "document.listRequested",
        "document.importRequested",
        "document.externalDropRequested",
        "document.dragOutRequested",
        "document.openRequested",
        "document.previewRequested",
        "document.diffRequested",
        "document.diffCancelRequested",
        "document.revealRequested",
        "document.relinkRequested",
        // Native-file attachment actions. File paths arrive only as WebView2
        // AdditionalObjects and are never accepted in renderer JSON.
        "file.uploadRequested",
        "file.replaceRequested",
        "file.removeRequested",
        "file.previewRequested",
        "file.downloadRequested",
        // Local-worker plugin platform. Each entry is a complete use case; no
        // generic rpc.invoke bridge is accepted.
        "plugin.catalog.list",
        "plugin.audit.list",
        "plugin.cleanup.listPending",
        "plugin.install.inspect",
        "plugin.install.github.inspect",
        "plugin.install.commit",
        "plugin.install.cancel",
        "plugin.lifecycle.setEnabled",
        "plugin.lifecycle.upgrade",
        "plugin.lifecycle.rollback",
        "plugin.lifecycle.uninstall",
        "plugin.action.describe",
        "plugin.action.start",
        "plugin.interaction.resolve",
        "plugin.task.cancel",
        "plugin.task.get",
        "plugin.surface.event",
        // Open the embedded data administration surface in this webview.
        "admin.openRequested",
        // Single closed bridge for the generated workspace-v2 RPC catalog.
        "workspace.v2.request",
    };

    // These use cases remain in the internal plugin/workspace catalogs so
    // producers and correlated host responses stay typed. They are not yet a
    // public renderer capability because no packaged-host scenario proves
    // their complete lifecycle.
    private static readonly HashSet<string> RendererInternalOnlyRequestTypes =
        new(StringComparer.Ordinal)
        {
            "plugin.lifecycle.upgrade",
            "plugin.lifecycle.rollback",
            "plugin.lifecycle.uninstall",
        };

    private static readonly HashSet<string> RendererInternalOnlyWorkspaceV2Methods =
        new(StringComparer.Ordinal)
        {
            "workspace.relink",
            "replica.synchronize",
        };

    /// <summary>
    /// The host -&gt; web notification types. Phase A defines the framework;
    /// B1 adds mutation outcomes; B3 reuses <c>table.pageLoaded</c> for query
    /// results (no new outbound type needed since the page DTO carries the
    /// snapshot/revision).
    /// </summary>
    private static readonly HashSet<string> HostNotificationWhitelist = new(StringComparer.Ordinal)
    {
        "host.startupStateChanged",
        "database.opened",
        "table.pageLoaded",
        "table.datasetReady",
        "operation.failed",
        // B1 mutation notifications.
        "table.editSchemaLoaded",
        "table.editCommitted",
        "table.editRejected",
        "table.rowsInserted",
        "table.rowsDeleted",
        // Provider-neutral, permission-filtered realtime invalidation.
        "data.changed",
        "task.changed",
        // B2 paste preview + apply outcomes.
        "table.pastePreviewReady",
        "table.pasteApplied",
        "data.importSourceRequested",
        "data.exportTargetRequested",
        "dailyQuote.fetch",
        "diagnostics.get",
        "appPreferences.get",
        "appPreferences.update",
        "update.check",
        "update.install",
        // G1 history query + two-phase safe restore outcomes.
        "history.pageLoaded",
        "history.restorePreviewReady",
        "history.restoreApplied",
        // Table management: host pushes refreshed collection list after create/delete.
        "database.collectionsChanged",
        "identifierMappings.result",
        "dashboard.listLoaded",
        "dashboard.loaded",
        "dashboard.manifestLoaded",
        "dashboard.queryLoaded",
        "dashboard.saved",
        "dashboard.deleted",
        "document.listLoaded",
        "document.actionCompleted",
        "document.diffCompleted",
        "document.diffCancelCompleted",
        "document.operationFailed",
        "document.workspaceChanged",
        // Correlated native attachment action acknowledgements.
        "file.uploadRequested",
        "file.replaceRequested",
        "file.removeRequested",
        // Versioned plugin domain events and local surface messages.
        "plugin.catalog.changed",
        "plugin.task.changed",
        "plugin.interaction.requested",
        "plugin.surface.message",
        // Correlated responses reuse the closed request type and requestId.
        "plugin.catalog.list",
        "plugin.audit.list",
        "plugin.cleanup.listPending",
        "plugin.install.inspect",
        "plugin.install.github.inspect",
        "plugin.install.commit",
        "plugin.install.cancel",
        "plugin.lifecycle.setEnabled",
        "plugin.lifecycle.upgrade",
        "plugin.lifecycle.rollback",
        "plugin.lifecycle.uninstall",
        "plugin.action.describe",
        "plugin.action.start",
        "plugin.interaction.resolve",
        "plugin.task.cancel",
        "plugin.task.get",
        "plugin.surface.event",
        "workspace.v2.response",
        "workspace.v2.bootstrap",
        "workspace.v2.event",
    };

    private static readonly IReadOnlyDictionary<string, string> WorkspaceV2Methods =
        new Dictionary<string, string>(StringComparer.Ordinal)
        {
            ["workspace.list"] = "global",
            ["workspace.create"] = "global",
            ["workspace.register"] = "global",
            ["workspace.relink"] = "global",
            ["workspace.open"] = "global",
            ["workspace.switch"] = "workspace",
            ["workspace.close"] = "workspace",
            ["workspace.remove"] = "global",
            ["workspace.planDelete"] = "global",
            ["workspace.applyDelete"] = "global",
            ["workspace.storage.preview"] = "global",
            ["workspace.storage.apply"] = "global",
            ["snapshot.request"] = "workspace",
            ["snapshot.list"] = "workspace",
            ["snapshot.inspect"] = "workspace",
            ["snapshot.update"] = "workspace",
            ["snapshot.previewRestore"] = "workspace",
            ["snapshot.applyRestore"] = "workspace",
            ["snapshot.openAsNewWorkspace"] = "workspace",
            ["snapshot.previewExtract"] = "workspace",
            ["snapshot.applyExtract"] = "workspace",
            ["snapshot.export"] = "workspace",
            ["snapshot.inspectPackage"] = "global",
            ["snapshot.import"] = "global",
            ["history.query"] = "workspace",
            ["history.previewRestore"] = "workspace",
            ["history.applyRestore"] = "workspace",
            ["repository.verify"] = "workspace",
            ["repository.previewKeyRotation"] = "workspace",
            ["repository.applyKeyRotation"] = "workspace",
            ["fileHistory.listDocuments"] = "workspace",
            ["fileHistory.listPendingChanges"] = "workspace",
            ["fileHistory.import"] = "workspace",
            ["fileHistory.relink"] = "workspace",
            ["fileHistory.unlink"] = "workspace",
            ["fileHistory.readTree"] = "workspace",
            ["fileHistory.restore"] = "workspace",
            ["fileHistory.upgrade"] = "workspace",
            ["fileHistory.activateLeaf"] = "workspace",
            ["fileHistory.applyPendingChange"] = "workspace",
            ["retention.get"] = "workspace",
            ["retention.status"] = "workspace",
            ["retention.update"] = "workspace",
            ["retention.plan"] = "workspace",
            ["retention.apply"] = "workspace",
            ["replica.status"] = "workspace",
            ["replica.synchronize"] = "workspace",
            ["replica.forceTakeover"] = "workspace",
            ["conflict.list"] = "workspace",
            ["conflict.inspect"] = "workspace",
            ["conflict.preview"] = "workspace",
            ["conflict.apply"] = "workspace",
        };

    static WebMessageRouter()
    {
        WebRequestWhitelist.UnionWith(ProductDataRpcRegistry.RequestTypes);
        HostNotificationWhitelist.UnionWith(ProductDataRpcRegistry.RequestTypes);
        // Correlated relation/Lookup responses reuse the closed request type.
        // The endpoint names are registered once together with their payload
        // validator and typed gateway binding.
        WebRequestWhitelist.UnionWith(RelationLookupRpcRegistry.RequestTypes);
        HostNotificationWhitelist.UnionWith(RelationLookupRpcRegistry.RequestTypes);
    }

    private readonly Action<RoutedWebRequest> _dispatch;

    /// <summary>
    /// Constructs the router. <paramref name="dispatch"/> is invoked for every
    /// accepted inbound web request; the router itself performs no I/O.
    /// </summary>
    public WebMessageRouter(Action<RoutedWebRequest> dispatch)
    {
        _dispatch = dispatch ?? throw new ArgumentNullException(nameof(dispatch));
    }

    /// <summary>
    /// Whether the host has reached <see cref="StartupState.Ready"/>. Until
    /// this flips true, business requests are rejected with
    /// <c>operation.failed</c>. The sole exception is the renderer's
    /// <c>app.ready</c> bootstrap handshake, which is what flips this flag.
    /// </summary>
    public bool IsReady { get; set; }

    /// <summary>
    /// Routes a raw JSON string posted from the WebView. Returns the
    /// <c>operation.failed</c> reply if the message is rejected, or
    /// <c>null</c> if it was accepted and dispatched.
    /// </summary>
    public HostReplyMessage? Route(string raw)
    {
        // Size guard FIRST: don't even parse an over-large blob.
        if (raw is null)
        {
            return BuildOperationFailed(null, "Message was null.", "BAD_MESSAGE");
        }
        if (System.Text.Encoding.UTF8.GetByteCount(raw) > MaxMessageBytes)
        {
            return BuildOperationFailed(
                TryPeekRequestId(raw),
                $"Message exceeds the {MaxMessageBytes / (1024 * 1024)} MiB cap.",
                "MESSAGE_TOO_LARGE");
        }

        JsonDocument doc;
        try
        {
            doc = JsonDocument.Parse(raw);
        }
        catch (JsonException)
        {
            return BuildOperationFailed(null, "Invalid JSON.", "BAD_JSON");
        }

        using (doc)
        {
            var root = doc.RootElement;
            if (root.ValueKind != JsonValueKind.Object)
            {
                return BuildOperationFailed(null, "Message root must be an object.", "BAD_MESSAGE");
            }

            string? requestId = root.TryGetProperty("requestId", out var ridEl)
                && ridEl.ValueKind == JsonValueKind.String
                    ? ridEl.GetString()
                    : null;

            if (!root.TryGetProperty("type", out var typeEl)
                || typeEl.ValueKind != JsonValueKind.String)
            {
                return BuildOperationFailed(requestId, "Missing 'type'.", "BAD_MESSAGE");
            }

            string type = typeEl.GetString() ?? string.Empty;

            if (!IsReady && !string.Equals(type, "app.ready", StringComparison.Ordinal))
            {
                return BuildOperationFailed(
                    requestId,
                    "Host is not ready; please retry shortly.",
                    "HOST_NOT_READY");
            }

            if (!WebRequestWhitelist.Contains(type))
            {
                return BuildOperationFailed(
                    requestId,
                    $"Unknown web request type '{type}'.",
                    "UNKNOWN_TYPE");
            }

            if (RendererInternalOnlyRequestTypes.Contains(type))
            {
                return BuildOperationFailed(
                    requestId,
                    $"Web request type '{type}' is not a public renderer capability.",
                    "CAPABILITY_NOT_PUBLIC");
            }

            // Clone the payload element so the dispatched handler can read it
            // after the JsonDocument is disposed.
            JsonElement payload = root.TryGetProperty("payload", out var payloadEl)
                ? payloadEl.Clone()
                : default;

            WorkspaceWireScope? scope = null;
            JsonElement wire = default;
            string? v2Method = null;
            if (root.TryGetProperty("scope", out JsonElement scopeElement))
            {
                if (scopeElement.ValueKind != JsonValueKind.Object)
                    return BuildOperationFailed(
                        requestId,
                        "Workspace scope must be an object.",
                        "BAD_WORKSPACE_SCOPE");
                try
                {
                    scope = WorkspaceV2Json.DeserializeStrict<WorkspaceWireScope>(
                        scopeElement.GetRawText());
                    scope.Validate();
                }
                catch (Exception exception) when (
                    exception is JsonException or InvalidOperationException)
                {
                    return BuildOperationFailed(
                        requestId,
                        "Workspace scope is invalid.",
                        "BAD_WORKSPACE_SCOPE");
                }
            }
            if (string.Equals(type, "workspace.v2.request", StringComparison.Ordinal))
            {
                if (payload.ValueKind != JsonValueKind.Object
                    || !payload.TryGetProperty("method", out JsonElement methodElement)
                    || methodElement.ValueKind != JsonValueKind.String
                    || !WorkspaceV2Methods.TryGetValue(
                        methodElement.GetString() ?? string.Empty,
                        out string? expectedScope))
                {
                    return BuildOperationFailed(
                        requestId,
                        "Workspace v2 method is not in the closed catalog.",
                        "UNKNOWN_V2_METHOD");
                }
                v2Method = methodElement.GetString();
                if (RendererInternalOnlyWorkspaceV2Methods.Contains(v2Method ?? string.Empty))
                {
                    return BuildOperationFailed(
                        requestId,
                        $"Workspace v2 method '{v2Method}' is not a public renderer capability.",
                        "CAPABILITY_NOT_PUBLIC");
                }
                if (!root.TryGetProperty("wire", out JsonElement wireElement)
                    || wireElement.ValueKind != JsonValueKind.Object)
                    return BuildOperationFailed(
                        requestId,
                        "Workspace v2 wire envelope is required.",
                        "BAD_WORKSPACE_WIRE");
                try
                {
                    if (expectedScope == "workspace")
                    {
                        scope = WorkspaceV2Json.DeserializeStrict<WorkspaceWireScope>(
                            wireElement.GetRawText());
                        scope.Validate();
                    }
                    else
                    {
                        var global = WorkspaceV2Json.DeserializeStrict<GlobalWireScope>(
                            wireElement.GetRawText());
                        global.Validate();
                    }
                    wire = wireElement.Clone();
                }
                catch (JsonException)
                {
                    return BuildOperationFailed(
                        requestId,
                        "Workspace v2 wire envelope is invalid for this method.",
                        "BAD_WORKSPACE_WIRE");
                }
            }

            _dispatch(new RoutedWebRequest(
                type, requestId, payload, raw, scope, wire, v2Method));
            return null;
        }
    }

    /// <summary>
    /// Whether a host -&gt; web notification type is in the Phase A whitelist.
    /// RPC notifications are NOT generically forwarded; each future business
    /// notification requires an explicit DTO + mapping.
    /// </summary>
    public bool IsHostNotificationAllowed(string type)
        => type is not null && HostNotificationWhitelist.Contains(type);

    /// <summary>
    /// Builds an <c>operation.failed</c> reply envelope. Public so MainWindow
    /// can reuse it for non-router-originated rejections (e.g. an RPC failure
    /// surfaced to the renderer).
    /// </summary>
    public static HostReplyMessage BuildOperationFailed(
        string? requestId,
        string message,
        string? code = null)
    {
        return new HostReplyMessage(
            Type: "operation.failed",
            RequestId: requestId,
            Payload: new OperationFailedPayload(message, code));
    }

    /// <summary>
    /// Best-effort peek at a <c>requestId</c> from a (possibly malformed)
    /// JSON blob, for correlating size-limit rejections. Returns null on any
    /// parse failure.
    /// </summary>
    private static string? TryPeekRequestId(string raw)
    {
        try
        {
            using var doc = JsonDocument.Parse(raw);
            if (doc.RootElement.ValueKind == JsonValueKind.Object
                && doc.RootElement.TryGetProperty("requestId", out var el)
                && el.ValueKind == JsonValueKind.String)
            {
                return el.GetString();
            }
        }
        catch
        {
            // Best-effort; return null.
        }
        return null;
    }
}

/// <summary>
/// A web -&gt; host request that passed the router's whitelist + size + JSON +
/// readiness checks and is ready to be forwarded (or handled locally).
/// </summary>
public sealed record RoutedWebRequest(
    string Type,
    string? RequestId,
    JsonElement Payload,
    string Raw,
    WorkspaceWireScope? Scope = null,
    JsonElement Wire = default,
    string? V2Method = null);

/// <summary>
/// Host -&gt; web reply envelope. <see cref="Type"/> is one of the Phase A
/// notification types (<c>database.opened</c>, <c>table.pageLoaded</c>,
/// <c>operation.failed</c>); <see cref="RequestId"/> echoes the inbound
/// request when applicable.
/// </summary>
public sealed record HostReplyMessage(
    string Type,
    string? RequestId,
    OperationFailedPayload? Payload,
    JsonElement Wire = default);

/// <summary>
/// Payload for the <c>operation.failed</c> notification. Mirrors
/// <c>desktop/web-grid/src/contracts.ts:OperationFailedPayload</c>.
/// </summary>
public sealed record OperationFailedPayload(string Message, string? Code);
