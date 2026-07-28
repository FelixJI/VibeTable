using System.IO;
using System.Text.Json;
using System.Text.Json.Nodes;

namespace VibeTable.Contracts.Tests;

[TestClass]
public sealed class WorkspaceV2ContractTests
{
    private static readonly Dictionary<string, Func<string, IWorkspaceV2Contract>> Readers =
        new(StringComparer.Ordinal)
        {
            ["workspace-manifest.json"] = WorkspaceV2Json.DeserializeStrict<WorkspaceManifestV2>,
            ["workspace-registry-entry.json"] = WorkspaceV2Json.DeserializeStrict<WorkspaceRegistryEntryV2>,
            ["workspace-session.json"] = WorkspaceV2Json.DeserializeStrict<WorkspaceSessionV2>,
            ["file-document.json"] = WorkspaceV2Json.DeserializeStrict<FileDocumentV2>,
            ["file-revision.json"] = WorkspaceV2Json.DeserializeStrict<FileRevisionV2>,
            ["snapshot-manifest.json"] = WorkspaceV2Json.DeserializeStrict<SnapshotManifestV2>,
            ["snapshot-seal.json"] = WorkspaceV2Json.DeserializeStrict<SnapshotSealV2>,
            ["snapshot-catalog-entry.json"] = WorkspaceV2Json.DeserializeStrict<SnapshotCatalogEntryV2>,
            ["lease-claim.json"] = WorkspaceV2Json.DeserializeStrict<LeaseClaimV2>,
            ["retention-policy.json"] = WorkspaceV2Json.DeserializeStrict<RetentionPolicyV2>,
            ["workspace-event.json"] = WorkspaceV2Json.DeserializeStrict<WorkspaceEventV2>,
            ["rpc-catalog.json"] = WorkspaceV2Json.DeserializeStrict<RpcContractCatalogV2>,
        };

    [TestMethod]
    public void V2FixturesDeserializeWithClosedModels()
    {
        foreach (var (name, read) in Readers)
        {
            var value = read(ReadFixture(name));
            Assert.IsNotNull(value, name);
        }
    }

    [TestMethod]
    public void V2ModelsRejectUnknownMissingInvalidAndTrailingJson()
    {
        var original = JsonNode.Parse(ReadFixture("workspace-manifest.json"))!.AsObject();

        var unknown = original.DeepClone().AsObject();
        unknown["unexpected"] = true;
        Assert.ThrowsExactly<JsonException>(
            () => WorkspaceV2Json.DeserializeStrict<WorkspaceManifestV2>(unknown.ToJsonString()));

        var missing = original.DeepClone().AsObject();
        missing.Remove("formatVersion");
        Assert.ThrowsExactly<JsonException>(
            () => WorkspaceV2Json.DeserializeStrict<WorkspaceManifestV2>(missing.ToJsonString()));

        var invalid = original.DeepClone().AsObject();
        invalid["storageMode"] = "remote";
        Assert.ThrowsExactly<JsonException>(
            () => WorkspaceV2Json.DeserializeStrict<WorkspaceManifestV2>(invalid.ToJsonString()));

        Assert.ThrowsExactly<JsonException>(
            () => WorkspaceV2Json.DeserializeStrict<WorkspaceManifestV2>(
                original.ToJsonString() + " {}"));
    }

    [TestMethod]
    public void SharedNegativeFixtureCorpusFailsClosed()
    {
        var corpus = JsonNode.Parse(ReadFixture(Path.Combine("..", "negative-fixtures.json")))!
            .AsObject();
        Assert.AreEqual(1, corpus["schemaVersion"]!.GetValue<int>());
        foreach (var node in corpus["cases"]!.AsArray())
        {
            var testCase = node!.AsObject();
            var name = testCase["name"]!.GetValue<string>();
            var fixture = testCase["fixture"]!.GetValue<string>();
            var operation = testCase["operation"]!.GetValue<string>();
            var source = ReadFixture(fixture);
            Assert.IsTrue(Readers.TryGetValue(fixture, out var read), name);
            if (operation == "appendRaw")
            {
                Assert.ThrowsExactly<JsonException>(
                    () => read!(source + testCase["value"]!.GetValue<string>()),
                    name);
                continue;
            }
            var document = JsonNode.Parse(source)!.AsObject();
            var target = document;
            var path = testCase["path"]!.AsArray();
            for (var index = 0; index < path.Count - 1; index++)
                target = target[path[index]!.GetValue<string>()]!.AsObject();
            var key = path[path.Count - 1]!.GetValue<string>();
            if (operation == "remove")
                target.Remove(key);
            else
                target[key] = testCase["value"]!.DeepClone();
            Assert.ThrowsExactly<JsonException>(() => read!(document.ToJsonString()), name);
        }
    }

    [TestMethod]
    public void RpcCatalogRejectsUnclosedArrayItemSchemas()
    {
        foreach (string mutation in new[]
                 {
                     "untyped-items",
                     "open-item",
                     "missing-required",
                 })
        {
            JsonObject catalog =
                JsonNode.Parse(ReadFixture("rpc-catalog.json"))!.AsObject();
            JsonObject conflict = catalog["rpcCases"]!.AsArray()
                .Select(item => item!.AsObject())
                .Single(item =>
                    item["method"]!.GetValue<string>() ==
                    "conflict.inspect");
            JsonObject array = conflict["resultSchema"]!["properties"]![
                "items"]!.AsObject();
            JsonObject item = array["items"]!.AsObject();
            switch (mutation)
            {
                case "untyped-items":
                    array["items"] = new JsonObject();
                    break;
                case "open-item":
                    item["additionalProperties"] = true;
                    break;
                case "missing-required":
                    item["required"]!.AsArray().RemoveAt(0);
                    break;
            }
            Assert.ThrowsExactly<JsonException>(
                () => WorkspaceV2Json.DeserializeStrict<RpcContractCatalogV2>(
                    catalog.ToJsonString()),
                mutation);
        }
    }

    [TestMethod]
    public void WorkspaceScopeRejectsLateEpochAndSequence()
    {
        var scope = new WorkspaceWireScope
        {
            Scope = "workspace",
            WorkspaceId = Guid.Parse("11111111-1111-4111-8111-111111111111"),
            SessionEpoch = 7,
            OperationId = Guid.Parse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
            Sequence = 12,
        };
        scope.Validate();
        scope.EnsureCurrent(scope.WorkspaceId, 7, 12);
        var oldEpoch = Assert.ThrowsExactly<InvalidOperationException>(
            () => scope.EnsureCurrent(scope.WorkspaceId, 8));
        Assert.AreEqual("workspace.session_epoch_stale", oldEpoch.Message);
        var oldSequence = Assert.ThrowsExactly<InvalidOperationException>(
            () => scope.EnsureCurrent(scope.WorkspaceId, 7, 13));
        Assert.AreEqual("workspace.sequence_stale", oldSequence.Message);
    }

    private static string ReadFixture(string name)
    {
        foreach (var start in new[] { Environment.CurrentDirectory, AppContext.BaseDirectory })
        {
            for (var current = new DirectoryInfo(Path.GetFullPath(start));
                 current is not null;
                 current = current.Parent)
            {
                var path = Path.Combine(current.FullName, "contracts", "v2", "fixtures", name);
                if (File.Exists(path))
                    return File.ReadAllText(path);
            }
        }
        throw new FileNotFoundException($"Could not locate contracts/v2/fixtures/{name}.");
    }
}
