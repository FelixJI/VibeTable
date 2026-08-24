using System.IO;
using System.Text.Json;
using System.Text.Json.Nodes;
using VibeTable.Contracts;

namespace VibeTable.Contracts.Tests;

/// <summary>
/// Pins the language-neutral product fixtures under contracts/v2 using only
/// System.Text.Json. These checks deliberately do not depend on provider DTOs.
/// </summary>
[TestClass]
public sealed class ProductContractV2RoundTripTests
{
    private static readonly string[] FixtureNames =
    [
        "data-changed-event.json",
        "formula-error.json",
        "managed-attachment-ref.json",
        "mutation-receipt.json",
        "mutation-request.json",
        "product-error.json",
        "product-rpc-catalog.json",
        "table-definition.json",
        "task-changed-event.json",
    ];

    private static readonly string FixturesDirectory = FindFixturesDirectory();

    [TestMethod]
    public void AllProductFixtures_SystemTextJsonRoundTripWithoutShapeChanges()
    {
        var actualNames = Directory.GetFiles(FixturesDirectory, "*.json")
            .Select(Path.GetFileName)
            .OrderBy(name => name, StringComparer.Ordinal)
            .ToArray();

        CollectionAssert.IsSubsetOf(FixtureNames, actualNames);

        foreach (var name in FixtureNames)
        {
            var json = File.ReadAllText(Path.Combine(FixturesDirectory, name));
            var parsed = JsonNode.Parse(json);
            Assert.IsNotNull(parsed, $"{name} must contain a JSON value");

            var roundTripped = JsonNode.Parse(parsed!.ToJsonString());
            Assert.IsTrue(
                JsonNode.DeepEquals(parsed, roundTripped),
                $"{name} changed shape after System.Text.Json round-trip");

            Assert.AreEqual(
                "2.0",
                parsed["contractVersion"]?.GetValue<string>(),
                $"{name} must declare contractVersion 2.0");

            var wire = parsed.ToJsonString().ToLowerInvariant();
            Assert.IsFalse(wire.Contains(
                "dire" + "ctus", StringComparison.Ordinal), name);
        }
    }

    [TestMethod]
    public void SchemaGetTableFixtureUsesGeneratedSchemaV2WithoutLegacyWireShape()
    {
        JsonObject catalog = ReadObject("product-rpc-catalog.json");
        JsonObject tableCase = catalog["rpcCases"]!.AsArray()
            .Select(node => node!.AsObject())
            .Single(item =>
                item["method"]!.GetValue<string>() == "schema.getTable");
        JsonObject result = tableCase["success"]!["result"]!.AsObject();
        JsonObject field = result["fields"]!.AsArray()[0]!.AsObject();

        Assert.AreEqual("SchemaSnapshot", tableCase["resultModel"]!.GetValue<string>());
        Assert.AreEqual("vibetable.schema.v2", result["contract"]!.GetValue<string>());
        Assert.IsTrue(field.ContainsKey("identity"));
        Assert.IsTrue(field.ContainsKey("logicalType"));
        Assert.IsTrue(field.ContainsKey("storage"));
        Assert.IsFalse(field.ContainsKey("dataType"));
        Assert.IsFalse(field.ContainsKey("storageType"));
        Assert.AreEqual(
            "pocketbase-text",
            field["storage"]!["kind"]!.GetValue<string>(),
            "Schema V2 provider storage kinds are a legal closed enum.");

        string tableWire = tableCase.ToJsonString();
        Assert.IsFalse(tableWire.Contains("ProductTableDefinition", StringComparison.Ordinal));
        Assert.IsFalse(tableWire.Contains("ProductFieldDefinition", StringComparison.Ordinal));
        Assert.IsFalse(tableWire.Contains("\"dataType\"", StringComparison.Ordinal));
        Assert.IsFalse(tableWire.Contains("\"storageType\"", StringComparison.Ordinal));

        string catalogWire = catalog.ToJsonString();
        Assert.IsFalse(catalogWire.Contains("/v1/schema", StringComparison.Ordinal));
        Assert.IsFalse(catalogWire.Contains(
            "/api/vibetable/v1/schema",
            StringComparison.Ordinal));
        Assert.IsFalse(catalogWire.Contains("ProductTableDefinition", StringComparison.Ordinal));
        Assert.IsFalse(catalogWire.Contains("ProductFieldDefinition", StringComparison.Ordinal));
    }

    [TestMethod]
    public void EventsAndMutationReceipt_PinRequiredProductFields()
    {
        var dataChanged = ReadObject("data-changed-event.json");
        var taskChanged = ReadObject("task-changed-event.json");
        var receipt = ReadObject("mutation-receipt.json");

        Assert.AreEqual("data.changed", dataChanged["topic"]?.GetValue<string>());
        Assert.AreEqual("task.changed", taskChanged["topic"]?.GetValue<string>());

        string[] requiredReceiptFields =
        [
            "status",
            "changeSetId",
            "affectedRows",
            "computedFields",
            "newRevision",
            "emittedEvents",
            "warnings",
        ];
        foreach (var field in requiredReceiptFields)
        {
            Assert.IsTrue(receipt.ContainsKey(field), $"mutation receipt is missing {field}");
        }
    }

    [TestMethod]
    public void RpcCatalogPinsEveryMethodAndEventEnvelope()
    {
        var catalog = ReadObject("product-rpc-catalog.json");
        var methods = catalog["rpcMethods"]!.AsArray();
        var cases = catalog["rpcCases"]!.AsArray();
        Assert.AreEqual(methods.Count, cases.Count);
        for (var index = 0; index < methods.Count; index++)
        {
            var item = cases[index]!.AsObject();
            var result = item["success"]!["result"];
            var method = item["method"]!.GetValue<string>();
            Assert.AreEqual(methods[index]!.GetValue<string>(), method);
            Assert.IsFalse(
                string.IsNullOrWhiteSpace(item["resultModel"]?.GetValue<string>()),
                $"{method} must name its actual result DTO");
            Assert.IsInstanceOfType<JsonObject>(
                item["resultSchema"],
                $"{method} must carry an executable result schema");
            Assert.AreEqual(
                item["request"]!["method"]!.GetValue<string>(),
                item["method"]!.GetValue<string>());
            Assert.AreEqual(
                item["request"]!["id"]!.GetValue<string>(),
                item["success"]!["id"]!.GetValue<string>());
            Assert.AreEqual(
                item["request"]!["id"]!.GetValue<string>(),
                item["error"]!["id"]!.GetValue<string>());
            if (result is JsonObject resultObject)
            {
                Assert.IsFalse(
                    resultObject.ContainsKey("method")
                    && resultObject.ContainsKey("status")
                    && resultObject.ContainsKey("contractVersion"),
                    $"{method} still uses the generic placeholder result");
            }
        }

        var topics = catalog["eventTopics"]!.AsArray();
        var events = catalog["eventCases"]!.AsArray();
        Assert.AreEqual(topics.Count, events.Count);
        for (var index = 0; index < topics.Count; index++)
        {
            var eventObject = events[index]!["event"]!.AsObject();
            var eventTopic = eventObject["topic"] ?? eventObject["eventType"];
            Assert.AreEqual(
                topics[index]!.GetValue<string>(),
                eventTopic!.GetValue<string>());
        }
    }

    [TestMethod]
    public void RpcCatalogPinsHighRiskMethodSpecificResponseDtos()
    {
        var catalog = ReadObject("product-rpc-catalog.json");
        var cases = catalog["rpcCases"]!.AsArray()
            .Select(node => node!.AsObject())
            .ToDictionary(
                item => item["method"]!.GetValue<string>(),
                StringComparer.Ordinal);

        var import = cases["data.applyImport"];
        Assert.AreEqual("ApplyImportResult", import["resultModel"]!.GetValue<string>());
        CollectionAssert.AreEquivalent(
            new[]
            {
                "collection", "createdCount", "updatedCount", "failedRows",
                "chunks", "requestIds",
            },
            import["success"]!["result"]!.AsObject().Select(pair => pair.Key).ToArray());
        var importDto = JsonSerializer.Deserialize<ApplyImportResult>(
            import["success"]!["result"]!.ToJsonString(),
            new JsonSerializerOptions { PropertyNameCaseInsensitive = true });
        Assert.IsNotNull(importDto);
        Assert.AreEqual("orders", importDto.Collection);
        Assert.IsNotNull(importDto.Chunks);
        Assert.IsNotNull(importDto.RequestIds);

        foreach (var method in new[] { "mutation.apply", "file.applyHostChange" })
        {
            var mutation = cases[method];
            Assert.AreEqual("MutationReceipt", mutation["resultModel"]!.GetValue<string>());
            CollectionAssert.AreEquivalent(
                new[]
                {
                    "contractVersion", "status", "changeSetId", "affectedRows",
                    "computedFields", "newRevision", "emittedEvents", "warnings",
                },
                mutation["success"]!["result"]!.AsObject()
                    .Select(pair => pair.Key)
                    .ToArray());
        }

        var table = cases["schema.getTable"];
        Assert.AreEqual("SchemaSnapshot", table["resultModel"]!.GetValue<string>());
        var tableResult = table["success"]!["result"]!.AsObject();
        Assert.IsTrue(tableResult.ContainsKey("tableId"));
        Assert.IsTrue(tableResult.ContainsKey("schemaRevision"));
        Assert.IsInstanceOfType<JsonArray>(tableResult["fields"]);
        var snapshot = tableResult.Deserialize<SchemaSnapshotV2>();
        Assert.IsNotNull(snapshot);

        var plugins = cases["plugin.listCatalog"];
        Assert.AreEqual("PluginSnapshotList", plugins["resultModel"]!.GetValue<string>());
        var plugin = plugins["success"]!["result"]!.AsArray()[0]!.AsObject();
        foreach (var field in new[] { "projectKey", "pluginId", "version", "packageHash", "manifest" })
        {
            Assert.IsTrue(plugin.ContainsKey(field), $"plugin snapshot is missing {field}");
        }
    }

    [TestMethod]
    public void SchemaV2CatalogResultsRejectUnsupportedContracts()
    {
        JsonObject catalog = ReadObject("product-rpc-catalog.json");
        var cases = catalog["rpcCases"]!.AsArray()
            .Select(node => node!.AsObject())
            .ToDictionary(
                item => item["method"]!.GetValue<string>(),
                StringComparer.Ordinal);

        foreach (string method in new[] { "schema.table.create", "schema.getTable" })
        {
            JsonObject result = cases[method]["success"]!["result"]!.AsObject();
            object valid = DeserializeSchemaResult(method, result);
            Assert.IsTrue(
                SchemaV2Contract.ValidateResult(valid, out string validReason),
                $"{method}: {validReason}");

            JsonObject invalidResult = result.DeepClone().AsObject();
            invalidResult["contract"] = "vibetable.schema.v1";
            object invalid = DeserializeSchemaResult(method, invalidResult);
            Assert.IsFalse(
                SchemaV2Contract.ValidateResult(invalid, out string invalidReason),
                method);
            Assert.AreEqual("unsupported contract", invalidReason, method);
        }
    }

    [TestMethod]
    public void SchemaSnapshotRejectsUnsupportedNestedFieldContract()
    {
        JsonObject catalog = ReadObject("product-rpc-catalog.json");
        JsonObject result = catalog["rpcCases"]!.AsArray()
            .Select(node => node!.AsObject())
            .Single(item => item["method"]!.GetValue<string>() == "schema.getTable")
            ["success"]!["result"]!.AsObject()
            .DeepClone().AsObject();
        result["fields"]!.AsArray()[0]!["contract"] = "vibetable.schema.v1";

        var snapshot = result.Deserialize<SchemaSnapshotV2>();
        Assert.IsNotNull(snapshot);
        Assert.IsFalse(
            SchemaV2Contract.ValidateResult(snapshot, out string reason));
        Assert.AreEqual("unsupported field contract", reason);
    }

    private static object DeserializeSchemaResult(string method, JsonObject result)
        => method switch
        {
            "schema.table.create" => result.Deserialize<SchemaTableCreateReceiptV2>()!,
            "schema.getTable" => result.Deserialize<SchemaSnapshotV2>()!,
            _ => throw new ArgumentOutOfRangeException(nameof(method), method, null),
        };

    private static JsonObject ReadObject(string name)
    {
        var json = File.ReadAllText(Path.Combine(FixturesDirectory, name));
        var node = JsonNode.Parse(json);
        Assert.IsInstanceOfType<JsonObject>(node, $"{name} must contain a JSON object");
        return (JsonObject)node!;
    }

    private static string FindFixturesDirectory()
    {
        string[] starts = [Environment.CurrentDirectory, AppContext.BaseDirectory];
        foreach (var start in starts)
        {
            for (var current = new DirectoryInfo(Path.GetFullPath(start));
                 current is not null;
                 current = current.Parent)
            {
                var candidate = Path.Combine(
                    current.FullName,
                    "contracts",
                    "v2",
                    "fixtures");
                if (Directory.Exists(candidate))
                {
                    return candidate;
                }
            }
        }

        throw new DirectoryNotFoundException(
            "Could not locate contracts/v2/fixtures from the test cwd or output directory.");
    }
}
