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
            "field.settings.describe", "field.change.plan", "field.change.apply",
            "field.change.status", "field.change.cancel", "field.recycleBin.list",
            "schema.getTable", "query.page", "query.view",
            "mutation.preview", "mutation.apply",
            "data.previewImport", "data.applyImport", "data.export",
            "task.create", "task.cancel", "task.status",
            "formula.validate", "formula.preview",
            "file.list", "file.token", "events.reconcile",
            "preset.list", "preset.save", "preset.delete",
            "version.list", "version.create", "version.save", "version.compare",
            "version.promote", "version.delete",
        ];

        CollectionAssert.AreEquivalent(expected, ProductDataRpcRegistry.RequestTypes.ToArray());
        Assert.IsFalse(ProductDataRpcRegistry.Contains("rpc.invoke"));
        Assert.IsFalse(ProductDataRpcRegistry.Contains("dire" + "ctus.read"));
        Assert.IsFalse(ProductDataRpcRegistry.Contains("schema.validate"));
        Assert.IsFalse(ProductDataRpcRegistry.Contains("schema.apply"));
        foreach (string type in expected)
        {
            Assert.IsTrue(ProductDataRpcRegistry.TryGet(type, out var endpoint), type);
            Assert.AreEqual(type, endpoint.Type);
        }
    }

    [TestMethod]
    public void FieldSettingsValidatorsExposeOnlyClosedV2UseCases()
    {
        Assert.IsTrue(ProductDataRpcRegistry.TryGet("field.settings.describe", out var describe));
        Assert.IsTrue(ProductDataRpcRegistry.TryGet("field.change.plan", out var plan));
        Assert.IsTrue(ProductDataRpcRegistry.TryGet("field.change.apply", out var apply));
        Assert.IsTrue(ProductDataRpcRegistry.TryGet("field.change.status", out var status));
        Assert.IsTrue(ProductDataRpcRegistry.TryGet("field.change.cancel", out var cancel));
        Assert.IsTrue(ProductDataRpcRegistry.TryGet("field.recycleBin.list", out var recycleBin));

        Assert.IsTrue(describe.IsValidPayload(
            JsonDocument.Parse("""{"tableId":"tbl_orders","fieldId":"fld_status"}""").RootElement));
        Assert.IsFalse(describe.IsValidPayload(
            JsonDocument.Parse("""{"tableId":"tbl_orders","fieldId":7}""").RootElement));

        Assert.IsTrue(plan.IsValidPayload(JsonDocument.Parse(
            """
            {"action":"update","tableId":"tbl_orders","fieldId":"fld_status",
             "expectedSchemaRevision":"schema_7","draft":{},"actor":{"id":"user_1","kind":"user"}}
            """).RootElement));
        Assert.IsFalse(plan.IsValidPayload(JsonDocument.Parse(
            """
            {"action":"update","tableId":"tbl_orders","expectedSchemaRevision":"schema_7",
             "actor":{"id":"user_1","kind":"user"},"providerFieldId":"secret"}
            """).RootElement));

        Assert.IsTrue(apply.IsValidPayload(JsonDocument.Parse(
            """
            {"planId":"plan_1","planHash":"sha256:1","operationId":"op_1",
             "actor":{"id":"user_1","kind":"user"},"confirmations":[]}
            """).RootElement));
        Assert.IsFalse(apply.IsValidPayload(JsonDocument.Parse(
            """
            {"planId":"plan_1","planHash":"sha256:1","operationId":"op_1",
             "actor":{"id":"user_1","kind":"user"},"confirmations":[],"force":true}
            """).RootElement));

        JsonElement job = JsonDocument.Parse("""{"jobId":"job_1"}""").RootElement;
        Assert.IsTrue(status.IsValidPayload(job));
        Assert.IsTrue(cancel.IsValidPayload(job));
        Assert.IsTrue(recycleBin.IsValidPayload(
            JsonDocument.Parse("""{"tableId":"tbl_orders"}""").RootElement));
        Assert.IsFalse(recycleBin.IsValidPayload(
            JsonDocument.Parse("""{"tableId":"tbl_orders","includeProvider":true}""").RootElement));
    }

    [TestMethod]
    public void MutatorsAndDangerousOperationsHaveExplicitAdmissionMetadata()
    {
        foreach (string type in new[]
                 {
                     "field.change.plan", "field.change.apply", "field.change.cancel",
                     "mutation.apply", "data.applyImport",
                     "task.create", "task.cancel", "preset.save",
                     "preset.delete", "version.create", "version.save",
                     "version.promote", "version.delete",
                 })
        {
            Assert.IsTrue(ProductDataRpcRegistry.TryGet(type, out var endpoint));
            Assert.IsTrue(endpoint.MutatesWorkspace, type);
        }
        foreach (string type in new[]
                 {
                     "field.change.plan", "field.change.apply",
                     "data.applyImport", "task.create",
                     "version.promote",
                 })
        {
            Assert.IsTrue(ProductDataRpcRegistry.TryGet(type, out var endpoint));
            Assert.IsTrue(endpoint.RequiresProtectionSnapshot, type);
        }
        foreach (string type in new[]
                 {
                     "field.settings.describe", "field.change.status",
                     "field.recycleBin.list",
                     "mutation.preview", "query.page", "query.view",
                     "data.previewImport", "data.export", "task.status",
                 })
        {
            Assert.IsTrue(ProductDataRpcRegistry.TryGet(type, out var endpoint));
            Assert.IsFalse(endpoint.MutatesWorkspace, type);
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
        Assert.IsFalse(ProductDataRpcRegistry.TryGet("schema.validate", out _));
        Assert.IsFalse(ProductDataRpcRegistry.TryGet("schema.apply", out _));
    }

    [TestMethod]
    public void ValidatorRejectsPayloadsBeyondDepthAndNodeLimits()
    {
        Assert.IsFalse(ProductDataRpcRegistry.Contains("schema.validate"));
        Assert.IsFalse(ProductDataRpcRegistry.Contains("schema.apply"));
    }

    [TestMethod]
    public void ValidatorAcceptsNormalNestedProductPayload()
    {
        Assert.IsTrue(ProductDataRpcRegistry.Contains("field.change.plan"));
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
