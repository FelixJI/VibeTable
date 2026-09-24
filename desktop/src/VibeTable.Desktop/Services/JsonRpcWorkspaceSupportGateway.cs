using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

public sealed record GridStateGetParams(string DatabaseId, string Table);

public sealed record GridStateSaveParams(
    string DatabaseId, string Table, GridState State, string? Revision);

/// <summary>
/// JSON-RPC adapter for backend support operations that remain local after
/// the provider migration. It intentionally exposes no local business-table
/// read/write methods.
/// </summary>
public sealed class JsonRpcWorkspaceSupportGateway : IWorkspaceSupportRpcGateway
{
    private readonly JsonRpcClient _client;

    public JsonRpcWorkspaceSupportGateway(JsonRpcClient client)
    {
        _client = client ?? throw new ArgumentNullException(nameof(client));
    }

    public Task<GridStateResult> GetGridStateAsync(
        string databaseId, string table, CancellationToken token)
        => _client.InvokeAsync<GridStateGetParams, GridStateResult>(
            "gridState.get",
            new GridStateGetParams(databaseId, table),
            token);

    public Task<GridStateResult> SaveGridStateAsync(
        string databaseId, string table, GridState state,
        string? revision, CancellationToken token)
        => _client.InvokeAsync<GridStateSaveParams, GridStateResult>(
            "gridState.save",
            new GridStateSaveParams(databaseId, table, state, revision),
            token);

}
