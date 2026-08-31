using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductRpcRouteSelectorTests
{
    [TestMethod]
    public void GeneratedPolicyKeepsEveryExistingTypedRouteOnPython()
    {
        var selector = new ProductRpcRouteSelector(
            ProductRpcCapabilityManifest.Default);

        foreach (string method in ProductDataRpcRegistry.RequestTypes)
        {
            Assert.IsTrue(ProductDataRpcRegistry.TryGet(
                method,
                out ProductDataRpcEndpoint endpoint));
            Assert.IsTrue(selector.TrySelectProduct(
                method,
                endpoint.CapabilityCatalog,
                out ProductRpcRoute route), method);
            Assert.AreEqual(ProductRpcRoute.PythonBff, route, method);
        }
        foreach (string method in RelationLookupRpcRegistry.RequestTypes)
        {
            Assert.IsTrue(selector.TrySelectRelation(method, out ProductRpcRoute route), method);
            Assert.AreEqual(ProductRpcRoute.PythonBff, route, method);
        }
    }

    [TestMethod]
    public void ProductPolicyCanSelectGoForOneClosedProductMethod()
    {
        ProductRpcCapabilityManifest policy = Policy(
            new ProductRpcCapability(
                "query.page",
                "workspace",
                "rendererPublic",
                "product.query.page",
                "goSidecar",
                "read"));
        var selector = new ProductRpcRouteSelector(policy);

        Assert.IsTrue(selector.TrySelectProduct(
            "query.page",
            ProductRpcCapabilityCatalog.Product,
            out ProductRpcRoute route));
        Assert.AreEqual(ProductRpcRoute.GoSidecar, route);
    }

    [TestMethod]
    public void SelectorFailsClosedForMissingOrNonTransportOwner()
    {
        ProductRpcCapabilityManifest policy = Policy(
            Capability("schema.getTable", "wpfHost"),
            Capability("query.page", "pythonWorker"));
        var selector = new ProductRpcRouteSelector(policy);

        Assert.IsFalse(selector.TrySelectProduct(
            "schema.getTable",
            ProductRpcCapabilityCatalog.Product,
            out _));
        Assert.IsFalse(selector.TrySelectProduct(
            "query.page",
            ProductRpcCapabilityCatalog.Product,
            out _));
        Assert.IsFalse(selector.TrySelectProduct(
            "schema.describe",
            ProductRpcCapabilityCatalog.Product,
            out _));
    }

    [TestMethod]
    public void WorkspaceCatalogStaysOnPythonWithoutProductManifestEntry()
    {
        var selector = new ProductRpcRouteSelector(Policy());

        Assert.IsTrue(selector.TrySelectProduct(
            "field.settings.describe",
            ProductRpcCapabilityCatalog.Workspace,
            out ProductRpcRoute route));
        Assert.AreEqual(ProductRpcRoute.PythonBff, route);
    }

    [TestMethod]
    public void RelationPolicyCannotSelectGoSidecar()
    {
        var selector = new ProductRpcRouteSelector(Policy(
            Capability("relation.searchTargets", "goSidecar")));

        Assert.IsFalse(selector.TrySelectRelation("relation.searchTargets", out _));
    }

    private static ProductRpcCapability Capability(string method, string owner)
        => new(
            method,
            "workspace",
            "rendererPublic",
            $"product.{method}",
            owner,
            "read");

    private static ProductRpcCapabilityManifest Policy(
        params ProductRpcCapability[] capabilities)
        => ProductRpcCapabilityManifest.CreateForTests(capabilities);
}
