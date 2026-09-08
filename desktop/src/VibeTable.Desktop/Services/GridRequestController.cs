using System;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Owns renderer-authored grid query, cursor, and persisted-state requests.
/// The workspace dispatcher delegates these messages and does not interpret
/// their payloads or the coordinator's configuration state.
/// </summary>
public sealed class GridRequestController
{
    private static readonly JsonSerializerOptions WireOptions =
        new(JsonSerializerDefaults.Web);

    private readonly GridStateCoordinator? _coordinator;
    private readonly IWebReplySink _reply;
    private readonly Func<CancellationToken> _sessionToken;
    private readonly WorkspaceSessionEnvelopeFilter? _sessions;

    public GridRequestController(
        GridStateCoordinator? coordinator,
        IWebReplySink reply,
        Func<CancellationToken>? sessionToken = null,
        WorkspaceSessionEnvelopeFilter? sessions = null)
    {
        _coordinator = coordinator;
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _sessionToken = sessionToken ?? (() => CancellationToken.None);
        _sessions = sessions;
    }

    public static bool Handles(string requestType)
        => requestType is
            "table.queryRequested" or
            "table.cursorRequested" or
            "gridState.saveRequested";

    public Task DispatchAsync(RoutedWebRequest request)
        => request.Type switch
        {
            "table.queryRequested" => QueryAsync(request),
            "table.cursorRequested" => CursorAsync(request),
            "gridState.saveRequested" => SaveStateAsync(request),
            _ => RejectAsync(
                request,
                "Grid request type is not supported.",
                "UNKNOWN_TYPE"),
        };

    private Task QueryAsync(RoutedWebRequest request)
    {
        if (_coordinator is null)
        {
            return RejectAsync(
                request,
                "Query requests are not wired in this host configuration.",
                "NOT_CONFIGURED");
        }

        string? table = GetString(request.Payload, "table");
        if (string.IsNullOrEmpty(table))
        {
            return RejectAsync(
                request,
                "table.queryRequested requires a non-empty 'table' payload field.",
                "BAD_PAYLOAD");
        }

        if (!TryGetProperty(request.Payload, "query", out JsonElement query)
            || query.ValueKind != JsonValueKind.Object)
        {
            return RejectAsync(
                request,
                "table.queryRequested requires a canonical query object.",
                "QUERY_INVALID");
        }

        if (request.RequestId is not null)
            return CompleteQueryAsync(request, table, query);
        _coordinator.RequestQuery(table, query);
        return Task.CompletedTask;
    }

    private async Task CompleteQueryAsync(RoutedWebRequest request, string table, JsonElement query)
    {
        WorkspaceRequestEpochLease? lease = null;
        if (_sessions is not null && !_sessions.TryCapture(request.Scope, out lease))
        {
            await RejectAsync(request, "Workspace request belongs to a stale session.", "BAD_WORKSPACE_SCOPE");
            return;
        }
        using (lease)
        using (var lifetime = CancellationTokenSource.CreateLinkedTokenSource(
            _sessionToken(), lease?.CancellationToken ?? CancellationToken.None))
        {
            bool IsCurrent() => !lifetime.IsCancellationRequested
                && (_sessions is null || _sessions.IsCurrent(lease));
            try
            {
                TablePage page = await _coordinator!.RequestQueryAsync(table, query, lifetime.Token)
                    .ConfigureAwait(false);
                if (IsCurrent()) _reply.PostResponse("table.pageLoaded", request.RequestId, page);
            }
            catch (OperationCanceledException)
            {
                if (IsCurrent())
                    await RejectAsync(request, "Table query was cancelled.", "QUERY_CANCELLED");
            }
            catch (Exception exception)
            {
                if (!IsCurrent()) return;
                MutationError error = MutationErrorMapper.Map(exception);
                await RejectAsync(request, error.Message, error.Code ?? "QUERY_FAILED");
            }
        }
    }

    private Task CursorAsync(RoutedWebRequest request)
    {
        string? cursor = GetString(request.Payload, "cursor");
        if (_coordinator is null || string.IsNullOrWhiteSpace(cursor))
        {
            return RejectAsync(
                request,
                "table.cursorRequested requires an active query and opaque cursor.",
                _coordinator is null ? "NOT_CONFIGURED" : "QUERY_INVALID");
        }

        _coordinator.RequestNextWindow(cursor);
        return Task.CompletedTask;
    }

    private Task SaveStateAsync(RoutedWebRequest request)
    {
        if (_coordinator is null)
        {
            return RejectAsync(
                request,
                "Grid-state save is not wired in this host configuration.",
                "NOT_CONFIGURED");
        }

        if (!TryReadGridState(request.Payload, out GridState? state) || state is null)
        {
            return RejectAsync(
                request,
                "gridState.saveRequested requires a 'state' payload field.",
                "BAD_PAYLOAD");
        }

        _coordinator.RequestSave(state);
        return Task.CompletedTask;
    }

    private Task RejectAsync(
        RoutedWebRequest request,
        string message,
        string code)
    {
        _reply.PostOperationFailed(request.RequestId, message, code);
        return Task.CompletedTask;
    }

    private static bool TryReadGridState(
        JsonElement payload,
        out GridState? state)
    {
        state = null;
        if (!TryGetProperty(payload, "state", out JsonElement value)
            || value.ValueKind != JsonValueKind.Object)
        {
            return false;
        }

        try
        {
            state = value.Deserialize<GridState>(WireOptions);
            return state is not null;
        }
        catch (JsonException)
        {
            return false;
        }
        catch (NotSupportedException)
        {
            return false;
        }
    }

    private static string? GetString(JsonElement payload, string name)
        => TryGetProperty(payload, name, out JsonElement value)
            && value.ValueKind == JsonValueKind.String
                ? value.GetString()
                : null;

    private static bool TryGetProperty(
        JsonElement payload,
        string name,
        out JsonElement value)
    {
        if (payload.ValueKind == JsonValueKind.Object
            && payload.TryGetProperty(name, out value))
        {
            return true;
        }

        value = default;
        return false;
    }
}
