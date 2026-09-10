using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductRpcCapabilityManifestTests
{
    [TestMethod]
    public void ProductSidecarRegistrationsAreGoOwnedSortedAndDefensive()
    {
        ProductRpcCapabilityManifest manifest =
            ProductRpcCapabilityManifest.CreateForTests(
                new ProductRpcCapability(
                    "query.z", "workspace", "rendererPublic",
                    "schema.query", "goSidecar", "read"),
                new ProductRpcCapability(
                    "query.python", "workspace", "rendererPublic",
                    "schema.query", "pythonBff", "read"),
                new ProductRpcCapability(
                    "query.a", "global", "rendererPublic",
                    "schema.query", "goSidecar", "read"));

        IReadOnlyList<ProductSidecarRegistration> registrations =
            manifest.GetProductSidecarRegistrations();

        CollectionAssert.AreEqual(
            new[] { "query.a:global", "query.z:workspace" },
            registrations.Select(item => $"{item.Method}:{item.Scope}").ToArray());
        Assert.IsFalse(registrations is ProductSidecarRegistration[]);
    }

    [TestMethod]
    public void GeneratedManifestProvidesClosedRouteLookupForCurrentOwners()
    {
        ProductRpcCapabilityManifest manifest = ProductRpcCapabilityManifest.Default;

        Assert.IsTrue(manifest.TryGet("schema.getTable", out ProductRpcCapability capability));
        Assert.AreEqual("workspace", capability.Scope);
        Assert.AreEqual("rendererPublic", capability.Audience);
        Assert.AreEqual("goSidecar", capability.Owner);
        Assert.AreEqual("read", capability.Effect);
        Assert.IsFalse(manifest.TryGet("schema.unknown", out _));
        Assert.IsTrue(manifest.TryGetEvent("data.changed", out ProductEventCapability dataChanged));
        Assert.AreEqual("notification", dataChanged.Effect);
        Assert.AreEqual("goSidecar", dataChanged.Owner);
        Assert.IsTrue(manifest.TryGetEvent("realtime.recovered", out ProductEventCapability recovery));
        Assert.AreEqual("goSidecar", recovery.Owner);
        Assert.IsFalse(manifest.TryGetEvent("data.unknown", out _));
        CollectionAssert.AreEqual(
            new[]
            {
                "events.reconcile:workspace",
                "field.settings.describe:workspace",
                "file.list:workspace",
                "history.applyRestore:workspace",
                "history.previewRestore:workspace",
                "history.read:workspace",
                "interface.commit:workspace",
                "interface.delete:workspace",
                "interface.list:workspace",
                "interface.load:workspace",
                "lookup.list:workspace",
                "lookup.query:workspace",
                "lookup.valuePage:workspace",
                "mutation.apply:workspace",
                "mutation.preview:workspace",
                "query.cursorFetch:workspace",
                "query.cursorOpen:workspace",
                "query.page:workspace",
                "query.readRows:workspace",
                "query.selectionOpen:workspace",
                "query.validateSnapshot:workspace",
                "query.view:workspace",
                "relation.inspectPair:workspace",
                "relation.previewDelta:workspace",
                "relation.searchTargets:workspace",
                "schema.describe:workspace",
                "schema.getTable:workspace",
                "schema.list:workspace",
            },
            manifest.GetProductSidecarRegistrations()
                .Select(item => $"{item.Method}:{item.Scope}").ToArray());
        Assert.IsTrue(manifest.TryGet("history.read", out ProductRpcCapability historyRead));
        Assert.AreEqual("goSidecar", historyRead.Owner);
    }

    [TestMethod]
    public void ParserRejectsUnknownCapabilityFields()
    {
        const string source = """
            {"contractVersion":"2.0","rpcMethods":[],"eventTopics":[],"unknown":true}
            """;

        Assert.ThrowsExactly<JsonException>(() => ProductRpcCapabilityManifest.Parse(source));
    }

    [TestMethod]
    public void ParserRejectsDuplicateEventTopics()
    {
        const string source = """
            {"contractVersion":"2.0","rpcMethods":[],"eventTopics":[
              {"topic":"data.changed","scope":"workspace","audience":"rendererPublic","capabilityId":"realtime","owner":"pythonBff","effect":"notification"},
              {"topic":"data.changed","scope":"workspace","audience":"rendererPublic","capabilityId":"realtime","owner":"pythonBff","effect":"notification"}
            ]}
            """;

        Assert.ThrowsExactly<JsonException>(() => ProductRpcCapabilityManifest.Parse(source));
    }

    [TestMethod]
    public void TestFactoryRejectsInvalidAndDuplicateCapabilities()
    {
        Assert.ThrowsExactly<ArgumentException>(() => ProductRpcCapabilityManifest.CreateForTests(
            new ProductRpcCapability("schema.getTable", "workspace", "invalid", "schema", "pythonBff", "read")));
        Assert.ThrowsExactly<ArgumentException>(() => ProductRpcCapabilityManifest.CreateForTests(
            new ProductRpcCapability("schema.getTable", "workspace", "rendererPublic", "schema", "pythonBff", "read"),
            new ProductRpcCapability("schema.getTable", "workspace", "rendererPublic", "schema", "pythonBff", "read")));
        Assert.ThrowsExactly<ArgumentException>(() => ProductRpcCapabilityManifest.CreateForTests(
            Array.Empty<ProductRpcCapability>(),
            [new ProductEventCapability("", "workspace", "rendererPublic", "realtime", "pythonBff", "notification")]));
        Assert.ThrowsExactly<ArgumentException>(() => ProductRpcCapabilityManifest.CreateForTests(
            Array.Empty<ProductRpcCapability>(),
            [
                new ProductEventCapability("data.changed", "workspace", "rendererPublic", "realtime", "pythonBff", "notification"),
                new ProductEventCapability("data.changed", "workspace", "rendererPublic", "realtime", "pythonBff", "notification"),
            ]));
    }
}
