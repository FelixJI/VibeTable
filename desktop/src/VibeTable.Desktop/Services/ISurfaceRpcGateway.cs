using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts.Generated;

namespace VibeTable.Desktop.Services;

/// <summary>Closed, typed JSON-RPC boundary for Interface authoring.</summary>
public interface ISurfaceRpcGateway
{
    Task<InterfaceListResult> ListAsync(CancellationToken token);
    Task<InterfaceSnapshot> LoadAsync(string interfaceId, CancellationToken token);
    Task<InterfaceSnapshot> CommitAsync(
        InterfaceCommitRequest parameters,
        CancellationToken token);
    Task<InterfaceDeleteResult> DeleteAsync(
        InterfaceDeleteRequest parameters,
        CancellationToken token);
}
