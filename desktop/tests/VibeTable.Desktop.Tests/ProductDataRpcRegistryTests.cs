using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using System.Text.Json.Nodes;
using VibeTable.Contracts;
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
            "schema.getTable", "query.page", "query.cursorOpen", "query.cursorFetch", "query.view",
            "contentProfile.load", "contentProfile.commit", "contentProfile.delete",
            "recordDocumentLink.list", "recordDocumentLink.commit",
            "recordDocumentLink.repair", "recordDocumentLink.delete",
            "mutation.preview", "mutation.apply",
            "data.previewImport", "data.applyImport", "data.export",
            "task.create", "task.cancel", "task.status",
            "formula.validate", "formula.draft.validate", "formula.preview",
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
    public void ContentModelValidatorsAreClosedAndMutationsRequireProtection()
    {
        Assert.IsTrue(ProductDataRpcRegistry.TryGet(
            "contentProfile.commit", out var profile));
        Assert.IsTrue(ProductDataRpcRegistry.TryGet(
            "recordDocumentLink.repair", out var repair));
        JsonElement profilePayload = JsonDocument.Parse(
            """{"profile":{},"expectedRevision":null,"idempotencyKey":"op-1"}""")
            .RootElement.Clone();
        JsonElement repairPayload = JsonDocument.Parse(
            """{"linkId":"link-1","documentId":"22222222-2222-4222-8222-222222222222","expectedRevision":"rev-1","idempotencyKey":"op-2"}""")
            .RootElement.Clone();
        Assert.IsTrue(profile.IsValidPayload(profilePayload));
        Assert.IsFalse(profile.IsValidPayload(JsonDocument.Parse(
            """{"profile":{},"expectedRevision":null,"idempotencyKey":"op-1","rawSql":"select 1"}""")
            .RootElement));
        Assert.IsTrue(repair.IsValidPayload(repairPayload));
        Assert.IsTrue(profile.MutatesWorkspace);
        Assert.IsNotNull(profile.ProtectionPolicy);
        Assert.IsTrue(repair.MutatesWorkspace);
        Assert.IsNotNull(repair.ProtectionPolicy);
    }

    [TestMethod]
    public void FormulaDraftValidatorRequiresOnlyVisualAuthoringInputs()
    {
        Assert.IsTrue(ProductDataRpcRegistry.TryGet("formula.draft.validate", out var endpoint));
        Assert.IsTrue(endpoint.IsValidPayload(JsonDocument.Parse(
            """{"tableId":"tbl_orders","displaySource":"SUM({明细}.{金额})"}""").RootElement));
        Assert.IsFalse(endpoint.IsValidPayload(JsonDocument.Parse(
            """{"tableId":"tbl_orders","displaySource":"SUM({明细}.{金额})","fieldId":"raw"}""").RootElement));
        Assert.IsFalse(endpoint.IsValidPayload(JsonDocument.Parse(
            """{"tableId":"tbl_orders","displaySource":7}""").RootElement));
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

        Assert.IsTrue(plan.IsValidPayload(ValidPlanPayload("update")));
        Assert.IsTrue(plan.IsValidPayload(ValidPlanPayload(
            "create",
            relationPair: new
            {
                reciprocalDisplayName = "Orders",
                reciprocalCardinality = "many",
                sourceDisplayFieldId = "fld_order_number",
            })));
        JsonObject planWithProviderField = JsonNode.Parse(
            ValidPlanPayload("update").GetRawText())!.AsObject();
        planWithProviderField["providerFieldId"] = "secret";
        Assert.IsFalse(plan.IsValidPayload(
            JsonSerializer.SerializeToElement(planWithProviderField)));

        Assert.IsTrue(apply.IsValidPayload(ValidApplyPayload()));
        Assert.IsTrue(apply.IsValidPayload(ValidApplyPayload(
            protectionSnapshotId: "renderer-supplied")));
        JsonObject forcedApply = JsonNode.Parse(
            ValidApplyPayload().GetRawText())!.AsObject();
        forcedApply["force"] = true;
        Assert.IsFalse(apply.IsValidPayload(
            JsonSerializer.SerializeToElement(forcedApply)));

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
    public void ProtectedEndpointsExposeCompletePolicyBehavior()
    {
        var cases = new (string Type, JsonElement Payload,
            bool RequiresSnapshot, string? ReceiptProperty)[]
        {
            ("field.change.plan", ValidPlanPayload("update"), false, "backupReceipt"),
            ("field.change.apply", ValidApplyPayload(), true, "protectionSnapshotId"),
            ("contentProfile.commit", JsonDocument.Parse(
                """{"profile":{},"expectedRevision":null,"idempotencyKey":"op-1"}""")
                .RootElement.Clone(), true, null),
            ("contentProfile.delete", JsonDocument.Parse(
                """{"tableId":"tbl_orders","expectedRevision":"rev-1","idempotencyKey":"op-2"}""")
                .RootElement.Clone(), true, null),
            ("recordDocumentLink.commit", JsonDocument.Parse(
                """{"link":{},"expectedRevision":null,"idempotencyKey":"op-3"}""")
                .RootElement.Clone(), true, null),
            ("recordDocumentLink.repair", JsonDocument.Parse(
                """{"linkId":"link-1","documentId":"doc-1","expectedRevision":"rev-1","idempotencyKey":"op-4"}""")
                .RootElement.Clone(), true, null),
            ("recordDocumentLink.delete", JsonDocument.Parse(
                """{"linkId":"link-1","expectedRevision":"rev-1","idempotencyKey":"op-5"}""")
                .RootElement.Clone(), true, null),
            ("data.applyImport", JsonDocument.Parse(
                """{"grantId":"grant-1","collection":"orders","token":"import-1"}""")
                .RootElement.Clone(), true, null),
            ("task.create", JsonDocument.Parse(
                """{"kind":"import","params":{}}""").RootElement.Clone(), true, null),
            ("version.promote", JsonDocument.Parse(
                """{"collection":"orders","itemId":"row-1","versionId":"v1","mainHash":"main-1","operationId":"op-6"}""")
                .RootElement.Clone(), true, null),
        };
        var ledger = new FieldChangeProtectionPlanLedger();
        FieldChangeProtectionLedgerContext context = ledger.BeginRequest(null);
        var receipt = new ProtectionSnapshotReceipt(
            Guid.Parse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
            1);

        foreach (var item in cases)
        {
            Assert.IsTrue(ProductDataRpcRegistry.TryGet(item.Type, out var endpoint));
            Assert.IsTrue(endpoint.IsValidPayload(item.Payload), item.Type);
            ProtectionSnapshotPolicy? policy = endpoint.ProtectionPolicy;
            Assert.IsNotNull(policy, item.Type);
            Assert.AreEqual(
                item.RequiresSnapshot,
                policy.RequiresSnapshot(item.Payload, ledger, context),
                item.Type);
            JsonElement rewritten = policy.RewritePayload(item.Payload, receipt);
            if (item.ReceiptProperty is null)
            {
                Assert.AreEqual(item.Payload.GetRawText(), rewritten.GetRawText(), item.Type);
            }
            else
            {
                Assert.AreEqual(
                    receipt.SnapshotId.ToString("D"),
                    rewritten.GetProperty(item.ReceiptProperty).GetString(),
                    item.Type);
            }
        }
    }

    [TestMethod]
    public void FieldChangePlanLedgerIsBoundedAndUnknownEntriesFailClosed()
    {
        var ledger = new FieldChangeProtectionPlanLedger(capacity: 2);
        FieldChangeProtectionLedgerContext context = ledger.BeginRequest(new WorkspaceWireScope
        {
            Scope = "workspace",
            WorkspaceId = Guid.Parse("11111111-1111-4111-8111-111111111111"),
            SessionEpoch = 7,
            OperationId = Guid.NewGuid(),
            Sequence = 1,
        });
        ledger.RecordPlan(context, ValidPlanResult("plan-1", "hash-1", "update"));
        ledger.RecordPlan(context, ValidPlanResult("plan-2", "hash-2", "update"));
        ledger.RecordPlan(context, ValidPlanResult("plan-3", "hash-3", "purge"));

        Assert.IsTrue(ledger.RequiresProtectionForApply(
            context,
            ValidApplyPayload("plan-1", "hash-1")));
        Assert.IsFalse(ledger.RequiresProtectionForApply(
            context,
            ValidApplyPayload("plan-2", "hash-2")));
        Assert.IsTrue(ledger.RequiresProtectionForApply(
            context,
            ValidApplyPayload("plan-3", "hash-3")));

        ledger.ResetGateway();
        Assert.IsTrue(ledger.RequiresProtectionForApply(
            context,
            ValidApplyPayload("plan-2", "hash-2")));
    }

    [TestMethod]
    public void FieldChangePlanLedgerRejectsNonUniqueConfirmationAuthority()
    {
        var ledger = new FieldChangeProtectionPlanLedger();
        FieldChangeProtectionLedgerContext context = ledger.BeginRequest(null);
        ledger.RecordPlan(context, ValidPlanResult("plan-1", "hash-1", "update"));
        Assert.IsFalse(ledger.RequiresProtectionForApply(
            context,
            ValidApplyPayload("plan-1", "hash-1")));
        JsonObject malformed = JsonNode.Parse(
            ValidPlanResult("plan-1", "hash-1", "update").GetRawText())!
            .AsObject();
        malformed["confirmations"] = new JsonArray("fieldName", "fieldName");

        ledger.RecordPlan(
            context,
            JsonSerializer.SerializeToElement(malformed));

        Assert.IsTrue(ledger.RequiresProtectionForApply(
            context,
            ValidApplyPayload("plan-1", "hash-1")));
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

    [TestMethod]
    public void PresetSaveRequiresPairedIdentityAndRevision()
    {
        Assert.IsTrue(ProductDataRpcRegistry.TryGet("preset.save", out var endpoint));
        Assert.IsTrue(endpoint.IsValidPayload(JsonSerializer.SerializeToElement(new
        {
            collection = "orders",
            name = "New view",
            view = new { },
            presetId = (string?)null,
            expectedRevision = (string?)null,
            operationId = "op-create",
        })));
        Assert.IsTrue(endpoint.IsValidPayload(JsonSerializer.SerializeToElement(new
        {
            collection = "orders",
            name = "Updated view",
            view = new { },
            presetId = "preset-1",
            expectedRevision = "rev-preset-1",
            operationId = "op-update",
        })));
        Assert.IsFalse(endpoint.IsValidPayload(JsonSerializer.SerializeToElement(new
        {
            collection = "orders",
            name = "Unsafe update",
            view = new { },
            presetId = "preset-1",
            expectedRevision = (string?)null,
            operationId = "op-unsafe",
        })));
        Assert.IsFalse(endpoint.IsValidPayload(JsonSerializer.SerializeToElement(new
        {
            collection = "orders",
            name = "Unsafe create",
            view = new { },
            presetId = (string?)null,
            expectedRevision = "rev-preset-1",
            operationId = "op-unsafe",
        })));
    }

    private static JsonElement ValidPlanPayload(
        string action,
        object? relationPair = null)
    {
        JsonElement serialized = JsonSerializer.SerializeToElement(new
        {
            action,
            tableId = "tbl_orders",
            fieldId = action == "create" ? "" : "fld_status",
            expectedSchemaRevision = "schema_7",
            expectedDataRevision = (long?)null,
            draft = (object?)null,
            actor = new { id = "user_1", kind = "user" },
            conversionRule = "",
            confirmation = action == "purge" ? "Status" : "",
            backupReceipt = "",
        });
        if (relationPair is null)
            return serialized;
        JsonObject payload = JsonNode.Parse(serialized.GetRawText())!.AsObject();
        payload["relationPair"] = JsonSerializer.SerializeToNode(relationPair);
        return JsonSerializer.SerializeToElement(payload);
    }

    private static JsonElement ValidApplyPayload(
        string planId = "plan_1",
        string planHash = "sha256:1",
        string? protectionSnapshotId = null)
    {
        var payload = new JsonObject
        {
            ["planId"] = planId,
            ["planHash"] = planHash,
            ["operationId"] = "op_1",
            ["actor"] = new JsonObject
            {
                ["id"] = "user_1",
                ["kind"] = "user",
            },
            ["confirmations"] = new JsonArray(),
        };
        if (protectionSnapshotId is not null)
            payload["protectionSnapshotId"] = protectionSnapshotId;
        return JsonSerializer.SerializeToElement(payload);
    }

    private static JsonElement ValidPlanResult(
        string planId,
        string planHash,
        string action)
        => JsonSerializer.SerializeToElement(new
        {
            contract = SchemaV2Contract.Name,
            planId,
            planHash,
            expiresAt = "2026-08-20T12:00:00Z",
            intent = JsonSerializer.Deserialize<JsonElement>(
                ValidPlanPayload(action).GetRawText()),
            before = (object?)null,
            after = (object?)null,
            classes = action == "purge" ? new[] { "danger" } : new[] { "display" },
            expectedSchemaRevision = "schema_7",
            expectedDataRevision = (long?)null,
            impact = new
            {
                records = 0,
                missing = 0,
                ambiguous = 0,
                failures = Array.Empty<object>(),
                dependencies = Array.Empty<object>(),
            },
            steps = new[] { new { kind = "validate", details = new { } } },
            warnings = Array.Empty<object>(),
            errors = Array.Empty<object>(),
            confirmations = action == "purge"
                ? new[] { "backupReceipt", "fieldName" }
                : Array.Empty<string>(),
            createsMigration = false,
            canApply = true,
        });
}
