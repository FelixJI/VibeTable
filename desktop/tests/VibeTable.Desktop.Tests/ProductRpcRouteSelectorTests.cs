using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductRpcRouteSelectorTests
{
    [TestMethod]
    public void GeneratedPolicySelectsCurrentOwners()
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
            Assert.AreEqual(method is "settings.readWorkCalendar" or "settings.commitWorkCalendar" or "preset.list" or "preset.save" or "preset.delete" or "relation.inspectPair" or "events.reconcile" or "field.settings.describe" or "file.list" or "history.read" or "lookup.list" or "mutation.apply" or "mutation.preview" or "query.page" or "query.view" or "query.cursorOpen" or "query.cursorFetch" or "query.readRows" or "query.selectionOpen" or "query.validateSnapshot" or "schema.describe" or "schema.getTable" or "schema.list"
                ? ProductRpcRoute.GoSidecar : ProductRpcRoute.PythonBff,
                route, method);
        }
        foreach (string method in RelationLookupRpcRegistry.RequestTypes)
        {
            Assert.IsTrue(selector.TrySelectRelation(method, out ProductRpcRoute route), method);
            Assert.AreEqual(method is "relation.searchTargets" or "relation.previewDelta" or "lookup.query" or "lookup.valuePage"
                ? ProductRpcRoute.GoSidecar : ProductRpcRoute.PythonBff, route, method);
        }
    }

    [TestMethod]
    public void FieldSettingsDescriptionUsesItsDeclaredGoOwner()
    {
        Assert.IsTrue(ProductDataRpcRegistry.TryGet(
            "field.settings.describe", out ProductDataRpcEndpoint endpoint));
        Assert.AreEqual(ProductRpcCapabilityCatalog.Product, endpoint.CapabilityCatalog);
        Assert.IsTrue(ProductRpcRouteSelector.Default.TrySelectProduct(
            endpoint.Type, endpoint.CapabilityCatalog, out ProductRpcRoute route));
        Assert.AreEqual(ProductRpcRoute.GoSidecar, route);
        Assert.IsFalse(new ProductRpcRouteSelector(Policy()).TrySelectProduct(
            endpoint.Type, endpoint.CapabilityCatalog, out _));
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
            "field.change.status",
            ProductRpcCapabilityCatalog.Workspace,
            out ProductRpcRoute route));
        Assert.AreEqual(ProductRpcRoute.PythonBff, route);
    }

    [TestMethod]
    [DataRow("lookup.valuePage")]
    [DataRow("relation.searchTargets")]
    [DataRow("relation.previewDelta")]
    [DataRow("lookup.query")]
    public void RelationPolicySelectsItsDeclaredTransportOwner(string method)
    {
        var selector = new ProductRpcRouteSelector(Policy(
            Capability(method, "goSidecar")));

        Assert.IsTrue(selector.TrySelectRelation(method, out ProductRpcRoute route));
        Assert.AreEqual(ProductRpcRoute.GoSidecar, route);
        Assert.IsFalse(selector.TrySelectRelation("history.previewRestore", out _));
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
