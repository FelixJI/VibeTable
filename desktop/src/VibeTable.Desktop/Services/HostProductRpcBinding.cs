using System.Net.Http;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Opaque, fixed Host Product generation. Capturing does not lease the runtime;
/// every typed invocation revalidates the complete tuple through its owner.
/// </summary>
internal sealed class HostProductRpcBinding(
    object runtime,
    JsonRpcClient client,
    ProductSidecarGenerationSnapshot snapshot,
    ProductRpcRouteSelector routes,
    Func<Func<bool>, bool> tryUseCurrent)
{
    private readonly object _runtime = runtime;
    private readonly ProductSidecarGenerationSnapshot _snapshot = snapshot;
    internal JsonRpcClient Client { get; } = client;

    internal bool Matches(HostProductRpcBinding other)
        => ReferenceEquals(_runtime, other._runtime)
            && ReferenceEquals(Client, other.Client)
            && ReferenceEquals(_snapshot, other._snapshot);

    internal JsonRpcProductDataGateway CreateGateway(
        IWorkspaceHostEpochLeaseSource leases, HttpMessageHandler? handler = null)
        => new(new HostProductRpcInvoker(Client, _snapshot, leases,
            tryUseCurrent, routes, handler));
}
