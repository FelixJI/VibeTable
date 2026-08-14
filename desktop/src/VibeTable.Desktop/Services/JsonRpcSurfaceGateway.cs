using System;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts.Generated;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>Interface adapter over the supervisor-owned local JSON-RPC pipe.</summary>
public sealed class JsonRpcSurfaceGateway : ISurfaceRpcGateway
{
    private readonly JsonRpcClient _client;

    public JsonRpcSurfaceGateway(JsonRpcClient client)
        => _client = client ?? throw new ArgumentNullException(nameof(client));

    public Task<InterfaceListResult> ListAsync(CancellationToken token)
        => _client.InvokeAsync<InterfaceListRequest, InterfaceListResult>(
            "interface.list", new(), token);

    public Task<InterfaceSnapshot> LoadAsync(
        string interfaceId,
        CancellationToken token)
        => _client.InvokeAsync<InterfaceLoadRequest, InterfaceSnapshot>(
            "interface.load",
            new InterfaceLoadRequest { InterfaceId = interfaceId },
            token);

    public Task<InterfaceSnapshot> CommitAsync(
        InterfaceCommitRequest parameters,
        CancellationToken token)
        => _client.InvokeAsync<InterfaceCommitRequest, InterfaceSnapshot>(
            "interface.commit", parameters, token);

    public Task<InterfaceDeleteResult> DeleteAsync(
        InterfaceDeleteRequest parameters,
        CancellationToken token)
        => _client.InvokeAsync<InterfaceDeleteRequest, InterfaceDeleteResult>(
            "interface.delete", parameters, token);
}
