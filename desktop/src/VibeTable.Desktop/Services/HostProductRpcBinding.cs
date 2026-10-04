using System.Net.Http;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Opaque, fixed Host Product generation. Capturing does not lease the runtime;
/// every typed invocation revalidates the complete tuple through its owner.
/// </summary>
internal sealed class HostProductRpcBinding(
    object runtime,
    JsonRpcClient? client,
    ProductSidecarGenerationSnapshot snapshot,
    ProductRpcRouteSelector routes,
    HostDataIoTaskRegistry taskOwner,
    Func<Func<bool>, bool> tryUsePython,
    Func<Func<bool>, bool>? tryUseGo = null,
    Func<IWorkspaceHostEpochLeaseSource, CancellationToken, Task<JsonRpcClient>>? ensurePython = null,
    Func<JsonRpcClient, Func<bool>, bool>? tryUseExactPython = null,
    Func<IWorkspaceHostEpochLeaseSource, HostSessionFileBroker>? hostFiles = null)
{
    private readonly object _runtime = runtime;
    private readonly ProductSidecarGenerationSnapshot _snapshot = snapshot;
    internal JsonRpcClient? Client { get; } = client;

    internal bool Matches(HostProductRpcBinding other)
        => ReferenceEquals(_runtime, other._runtime)
            && ReferenceEquals(_snapshot, other._snapshot);

    internal bool Matches(ProductSidecarGenerationSnapshot other)
        => ReferenceEquals(_snapshot, other);

    internal void BindSourceImportCatalogRefresh(Func<ProductSidecarGenerationSnapshot, CancellationToken, Task> refresh)
        => taskOwner.BindSourceImportCatalogRefresh(_snapshot, refresh);

    internal JsonRpcProductDataGateway CreateGateway(
        IWorkspaceHostEpochLeaseSource leases, HttpMessageHandler? handler = null)
        => new(new HostProductRpcInvoker(Client, _snapshot, leases,
            tryUsePython, routes, handler, tryUseGo ?? tryUsePython, taskOwner,
            ensurePython is null ? null : token => ensurePython(leases, token),
            tryUseExactPython, hostFiles is null ? null : () => hostFiles(leases)));

    internal Task<JsonRpcClient> EnsurePythonClientAsync(
        IWorkspaceHostEpochLeaseSource leases, CancellationToken token)
        => ensurePython is not null ? ensurePython(leases, token)
            : Task.FromResult(Client ?? throw new BackendUnavailableException("Python is unavailable."));

    internal bool TryUsePython(JsonRpcClient exact, Func<bool> action)
        => tryUseExactPython is not null ? tryUseExactPython(exact, action)
            : ReferenceEquals(Client, exact) && tryUsePython(action);
}
