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

        Assert.HasCount(11, types);
        Assert.AreEqual(
            types.Count,
            types.Distinct(StringComparer.Ordinal).Count(),
            "RPC request types must be unique under ordinal comparison.");
        Assert.IsFalse(RelationLookupRpcRegistry.Contains("rpc.invoke"));
        Assert.IsFalse(RelationLookupRpcRegistry.Contains("lookup.create"));
        Assert.IsFalse(RelationLookupRpcRegistry.Contains("lookup.update"));
        Assert.IsFalse(RelationLookupRpcRegistry.Contains("lookup.delete"));
        Assert.IsFalse(RelationLookupRpcRegistry.Contains("table_admin.previewRelationChange"));
        Assert.IsFalse(RelationLookupRpcRegistry.Contains("table_admin.applyRelationChange"));

        foreach (string type in types)
        {
            Assert.IsTrue(RelationLookupRpcRegistry.TryGet(type, out var endpoint), type);
            Assert.AreEqual(type, endpoint.Type, type);
        }
    }

    [TestMethod]
    public void CreateTargetRequiresVisualRelationLabelAndIdempotencyKey()
    {
        Assert.IsTrue(RelationLookupRpcRegistry.TryGet("relation.createTarget", out var endpoint));
        Assert.IsTrue(endpoint.IsValidPayload(JsonDocument.Parse(
            """{"relationId":"orders.customer","label":"Acme","idempotencyKey":"create-1"}""")
            .RootElement));
        Assert.IsFalse(endpoint.IsValidPayload(JsonDocument.Parse(
            """{"relationId":"orders.customer","rowId":"raw","idempotencyKey":"create-1"}""")
            .RootElement));
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

    [TestMethod]
    public void LookupValuePageRequiresStableIdentityRevisionsAndPaging()
    {
        Assert.IsTrue(RelationLookupRpcRegistry.TryGet("lookup.valuePage", out var endpoint));
        Assert.IsTrue(endpoint.IsValidPayload(JsonDocument.Parse(
            """{"collection":"orders","fieldRef":"line_skus","sourceRecordId":"order-1","offset":100,"limit":100,"schemaRevision":"schema_7","permissionRevision":"permission_7","lookupRevision":"lookup_7"}""")
            .RootElement));
        Assert.IsFalse(endpoint.IsValidPayload(JsonDocument.Parse(
            """{"collection":"orders","fieldRef":"line_skus","sourceRecordId":"order-1","offset":100,"schemaRevision":"schema_7","permissionRevision":"permission_7","lookupRevision":"lookup_7"}""")
            .RootElement));
    }
}
