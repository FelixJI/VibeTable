using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductDataRpcRegistryTests
{
    [TestMethod]
    public void RegistryIsClosedAndContainsEveryUiProductCapability()
    {
        string[] expected =
        [
            "schema.getTable", "schema.validate", "schema.apply", "query.page",
            "mutation.preview", "mutation.apply",
            "data.previewImport", "data.applyImport", "data.export",
            "task.create", "task.cancel", "task.status",
            "formula.validate", "formula.preview",
            "file.list", "file.token", "events.reconcile",
            "preset.list", "preset.save", "preset.delete",
            "version.list", "version.create", "version.save", "version.compare",
            "version.promote", "version.delete",
            "backup.list", "backup.create", "backup.delete", "backup.restore",
        ];

        CollectionAssert.AreEquivalent(expected, ProductDataRpcRegistry.RequestTypes.ToArray());
        Assert.IsFalse(ProductDataRpcRegistry.Contains("rpc.invoke"));
        Assert.IsFalse(ProductDataRpcRegistry.Contains("dire" + "ctus.read"));
        foreach (string type in expected)
        {
            Assert.IsTrue(ProductDataRpcRegistry.TryGet(type, out var endpoint), type);
            Assert.AreEqual(type, endpoint.Type);
        }
    }

    [TestMethod]
    public void SchemaGetTableAcceptsOnlyAClosedProductTableIdentity()
    {
        Assert.IsTrue(ProductDataRpcRegistry.TryGet("schema.getTable", out var endpoint));
        Assert.IsTrue(endpoint.IsValidPayload(
            JsonDocument.Parse("""{"tableId":"tbl_orders"}""").RootElement));
        Assert.IsFalse(endpoint.IsValidPayload(
            JsonDocument.Parse("""{"tableId":"tbl_orders","sessionSecret":"nope"}""").RootElement));
        Assert.IsFalse(endpoint.IsValidPayload(
            JsonDocument.Parse("""{"tableId":7}""").RootElement));
    }

    [TestMethod]
    public void BackupValidatorsAcceptOnlyClosedSafeArchivePayloads()
    {
        Assert.IsTrue(ProductDataRpcRegistry.TryGet("backup.list", out var list));
        Assert.IsTrue(ProductDataRpcRegistry.TryGet("backup.create", out var create));
        Assert.IsTrue(ProductDataRpcRegistry.TryGet("backup.delete", out var delete));
        Assert.IsTrue(ProductDataRpcRegistry.TryGet("backup.restore", out var restore));

        Assert.IsTrue(list.IsValidPayload(JsonDocument.Parse("{}").RootElement));
        Assert.IsFalse(list.IsValidPayload(
            JsonDocument.Parse("""{"url":"http://127.0.0.1"}""").RootElement));

        Assert.IsTrue(create.IsValidPayload(
            JsonDocument.Parse("""{"name":"manual_20260724_101500.zip"}""").RootElement));
        Assert.IsFalse(create.IsValidPayload(
            JsonDocument.Parse("""{"name":"../data.db.zip"}""").RootElement));
        Assert.IsFalse(create.IsValidPayload(
            JsonDocument.Parse("""{"name":"safe.zip","path":"C:\\private"}""").RootElement));

        Assert.IsTrue(delete.IsValidPayload(
            JsonDocument.Parse("""{"name":"manual_20260724_101500.zip"}""").RootElement));
        Assert.IsFalse(delete.IsValidPayload(
            JsonDocument.Parse("""{"name":"../data.db.zip"}""").RootElement));

        Assert.IsTrue(restore.IsValidPayload(JsonDocument.Parse(
            """{"name":"manual_20260724_101500.zip","confirmed":true}""").RootElement));
        Assert.IsFalse(restore.IsValidPayload(JsonDocument.Parse(
            """{"name":"manual_20260724_101500.zip","confirmed":false}""").RootElement));
        Assert.IsFalse(restore.IsValidPayload(JsonDocument.Parse(
            """{"name":"manual_20260724_101500.zip","confirmed":true,"sessionSecret":"x"}"""
        ).RootElement));
    }

    [TestMethod]
    public void ValidatorsRejectProviderCredentialsAndMalformedPayloads()
    {
        JsonElement credentials = JsonDocument.Parse(
            """{"tableId":"tbl_orders","sessionSecret":"nope"}""").RootElement.Clone();
        JsonElement nonObject = JsonDocument.Parse("[]").RootElement.Clone();

        foreach (string type in ProductDataRpcRegistry.RequestTypes)
        {
            Assert.IsTrue(ProductDataRpcRegistry.TryGet(type, out var endpoint));
            Assert.IsFalse(endpoint.IsValidPayload(nonObject), type);
            Assert.IsFalse(endpoint.IsValidPayload(credentials), type);
        }
    }

    [TestMethod]
    public void ValidatorRejectsCredentialsNestedInsideObjectsAndArrays()
    {
        Assert.IsTrue(ProductDataRpcRegistry.TryGet("schema.validate", out var endpoint));
        JsonElement nestedObject = JsonDocument.Parse(
            """
            {
              "definition": {
                "fields": [{
                  "editor": {
                    "config": {
                      "accessToken": "must-not-cross"
                    }
                  }
                }]
              },
              "expectedRevision": 0
            }
            """).RootElement.Clone();
        JsonElement nestedArray = JsonDocument.Parse(
            """
            {
              "definition": {
                "fields": [
                  {"constraints": [{"password": "must-not-cross"}]}
                ]
              },
              "expectedRevision": 0
            }
            """).RootElement.Clone();

        Assert.IsFalse(endpoint.IsValidPayload(nestedObject));
        Assert.IsFalse(endpoint.IsValidPayload(nestedArray));
    }

    [TestMethod]
    public void ValidatorRejectsPayloadsBeyondDepthAndNodeLimits()
    {
        Assert.IsTrue(ProductDataRpcRegistry.TryGet("schema.validate", out var endpoint));
        object nested = "leaf";
        for (int index = 0; index < 40; index++)
        {
            nested = new Dictionary<string, object?> { ["next"] = nested };
        }
        JsonElement tooDeep = JsonSerializer.SerializeToElement(new
        {
            definition = nested,
            expectedRevision = 0,
        });
        JsonElement tooManyNodes = JsonSerializer.SerializeToElement(new
        {
            definition = new
            {
                values = Enumerable.Range(0, 10_001).ToArray(),
            },
            expectedRevision = 0,
        });

        Assert.IsFalse(endpoint.IsValidPayload(tooDeep));
        Assert.IsFalse(endpoint.IsValidPayload(tooManyNodes));
    }

    [TestMethod]
    public void ValidatorAcceptsNormalNestedProductPayload()
    {
        Assert.IsTrue(ProductDataRpcRegistry.TryGet("schema.validate", out var endpoint));
        JsonElement payload = JsonDocument.Parse(
            """
            {
              "definition": {
                "tableId": "tbl_orders",
                "fields": [{
                  "fieldId": "status",
                  "constraints": [
                    {"kind": "enum", "values": ["draft", "submitted"]}
                  ],
                  "editor": {
                    "kind": "select",
                    "config": {"allowClear": false}
                  }
                }]
              },
              "expectedRevision": 7
            }
            """).RootElement.Clone();

        Assert.IsTrue(endpoint.IsValidPayload(payload));
    }

    [TestMethod]
    public void PresetAndVersionWritesRequireOperationId()
    {
        var payloads = new Dictionary<string, JsonElement>
        {
            ["preset.save"] = JsonSerializer.SerializeToElement(new
            {
                collection = "orders", name = "My view", view = new { },
            }),
            ["preset.delete"] = JsonSerializer.SerializeToElement(new
            {
                presetId = "p1", expectedRevision = "rev-p1",
            }),
            ["version.create"] = JsonSerializer.SerializeToElement(new
            {
                collection = "orders", itemId = "row-1",
            }),
            ["version.save"] = JsonSerializer.SerializeToElement(new
            {
                collection = "orders", itemId = "row-1", versionId = "v1", values = new { },
            }),
            ["version.promote"] = JsonSerializer.SerializeToElement(new
            {
                collection = "orders", itemId = "row-1", versionId = "v1", mainHash = "hash-1",
            }),
            ["version.delete"] = JsonSerializer.SerializeToElement(new
            {
                collection = "orders", itemId = "row-1", versionId = "v1",
                expectedRevision = "rev-v1",
            }),
        };

        foreach (var (type, payload) in payloads)
        {
            Assert.IsTrue(ProductDataRpcRegistry.TryGet(type, out var endpoint));
            Assert.IsFalse(endpoint.IsValidPayload(payload), type);
        }
    }
}
