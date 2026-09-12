using System.IO;
using System.Text.Json;
using System.Text.Json.Serialization;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>Owns scoped renderer layout requests without a Python or business-data write path.</summary>
internal sealed class GridPresentationRequestController(
    IWebReplySink reply,
    WorkspaceSessionEnvelopeFilter sessions,
    HostGridStateStore store)
{
    private static readonly JsonSerializerOptions Options = new(JsonSerializerDefaults.Web)
    {
        PropertyNameCaseInsensitive = false,
        UnmappedMemberHandling = JsonUnmappedMemberHandling.Disallow,
        AllowDuplicateProperties = false,
    };

    public static bool Handles(string type) => type is "gridState.get" or "gridState.save";

    public async Task DispatchAsync(RoutedWebRequest request)
    {
        if (!Handles(request.Type))
        {
            reply.PostOperationFailed(request.RequestId, "Unknown grid-state request.", "UNKNOWN_TYPE");
            return;
        }
        if (request.Scope is not { } scope || !sessions.TryCapture(scope, out var lease))
        {
            reply.PostOperationFailed(request.RequestId, "Grid state requires the current workspace.", "BAD_WORKSPACE_SCOPE");
            return;
        }
        using (lease)
        {
            string table;
            GridState? state = null;
            string? revision = null;
            try
            {
                if (request.Type == "gridState.get")
                {
                    var parameters = request.Payload.Deserialize<ReadParameters>(Options)
                        ?? throw new JsonException("Expected grid-state parameters.");
                    table = parameters.Table;
                }
                else
                {
                    var parameters = request.Payload.Deserialize<SaveParameters>(Options)
                        ?? throw new JsonException("Expected grid-state parameters.");
                    table = parameters.Table;
                    state = parameters.State ?? throw new JsonException("Expected grid state.");
                    revision = parameters.Revision;
                }
            }
            catch (JsonException)
            {
                reply.PostOperationFailed(request.RequestId, "Invalid grid-state parameters.", "BAD_PAYLOAD");
                return;
            }
            void EnsureCurrent()
            {
                if (!sessions.IsCurrent(lease)) throw new OperationCanceledException("Workspace changed.");
            }
            try
            {
                EnsureCurrent();
                GridStateResult result = state is null
                    ? await store.ReadAsync(scope.WorkspaceId, table, lease!.CancellationToken)
                    : await store.SaveAsync(scope.WorkspaceId, table, state, revision, EnsureCurrent, lease!.CancellationToken);
                EnsureCurrent();
                reply.PostResponse(request.Type, request.RequestId, result);
            }
            catch (OperationCanceledException)
            {
                if (sessions.IsCurrent(lease))
                    reply.PostOperationFailed(request.RequestId, "Grid-state request cancelled.", "GRID_STATE_CANCELLED");
            }
            catch (ArgumentException)
            {
                reply.PostOperationFailed(request.RequestId, "Invalid grid layout.", "BAD_PAYLOAD");
            }
            catch (Exception exception) when (exception is IOException or UnauthorizedAccessException or JsonException)
            {
                if (sessions.IsCurrent(lease))
                    reply.PostOperationFailed(request.RequestId, "Grid-state storage is unavailable.", "GRID_STATE_UNAVAILABLE");
            }
        }
    }

    private sealed record ReadParameters([property: JsonRequired] string Table);
    private sealed record SaveParameters(
        [property: JsonRequired] string Table,
        [property: JsonRequired] GridState State,
        [property: JsonRequired] string? Revision);
}
