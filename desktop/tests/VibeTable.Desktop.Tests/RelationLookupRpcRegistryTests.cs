using System;
using System.Linq;
using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class RelationLookupRpcRegistryTests
{
    [TestMethod]
    public void RegistryIsClosedUniqueAndEveryNameResolvesToItsExplicitEndpoint()
    {
        var types = RelationLookupRpcRegistry.RequestTypes;

        Assert.HasCount(14, types);
        Assert.AreEqual(
            types.Count,
            types.Distinct(StringComparer.Ordinal).Count(),
            "RPC request types must be unique under ordinal comparison.");
        Assert.IsFalse(RelationLookupRpcRegistry.Contains("rpc.invoke"));

        foreach (string type in types)
        {
            Assert.IsTrue(RelationLookupRpcRegistry.TryGet(type, out var endpoint), type);
            Assert.AreEqual(type, endpoint.Type, type);
        }
    }

    [TestMethod]
    public void EveryRegisteredPayloadValidatorRejectsANonObjectPayload()
    {
        JsonElement nonObject = JsonDocument.Parse("[]").RootElement.Clone();

        foreach (string type in RelationLookupRpcRegistry.RequestTypes)
        {
            Assert.IsTrue(RelationLookupRpcRegistry.TryGet(type, out var endpoint), type);
            Assert.IsFalse(endpoint.IsValidPayload(nonObject), type);
        }
    }
}
