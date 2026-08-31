using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal enum ProductRpcRoute
{
    PythonBff,
    GoSidecar,
}

internal interface IProductSidecarRpcForwarder
{
    Task<ProductSidecarForwardResult> ForwardAsync(
        string requestId,
        string method,
        JsonElement wire,
        JsonElement parameters,
        CancellationToken cancellationToken);
}

/// <summary>
/// Resolves the single transport owner for a typed Product RPC. Workspace
/// catalog entries keep their existing Python route; Relation remains on
/// Python until its own lifecycle migration is complete.
/// </summary>
internal sealed class ProductRpcRouteSelector
{
    private readonly ProductRpcCapabilityManifest _manifest;

    internal ProductRpcRouteSelector(ProductRpcCapabilityManifest manifest)
    {
        _manifest = manifest ?? throw new ArgumentNullException(nameof(manifest));
    }

    internal static ProductRpcRouteSelector Default { get; } =
        new(ProductRpcCapabilityManifest.Default);

    internal bool TrySelectProduct(
        string method,
        ProductRpcCapabilityCatalog catalog,
        out ProductRpcRoute route)
    {
        route = default;
        if (catalog == ProductRpcCapabilityCatalog.Workspace)
        {
            route = ProductRpcRoute.PythonBff;
            return true;
        }
        return _manifest.TryGet(method, out ProductRpcCapability capability)
            && TryMapOwner(capability.Owner, out route);
    }

    internal bool TrySelectRelation(string method, out ProductRpcRoute route)
    {
        route = default;
        return _manifest.TryGet(method, out ProductRpcCapability capability)
            && capability.Owner == "pythonBff"
            && TryMapOwner(capability.Owner, out route);
    }

    private static bool TryMapOwner(string owner, out ProductRpcRoute route)
    {
        switch (owner)
        {
            case "pythonBff":
                route = ProductRpcRoute.PythonBff;
                return true;
            case "goSidecar":
                route = ProductRpcRoute.GoSidecar;
                return true;
            default:
                route = default;
                return false;
        }
    }
}
