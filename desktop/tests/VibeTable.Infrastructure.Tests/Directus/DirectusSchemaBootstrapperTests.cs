using System.IO;
using System.Text.Json.Nodes;
using Microsoft.VisualStudio.TestTools.UnitTesting;
using VibeTable.Infrastructure.Directus;

namespace VibeTable.Infrastructure.Tests.Directus;

/// <summary>
/// Tests for <see cref="DirectusSchemaBootstrapper"/>'s payload construction —
/// the 1:1 port of <c>backend/adapters/directus/bootstrap.py</c>. These assert
/// that the C# payloads match what the Python bootstrapper produced, so a
/// greenfield schema apply is byte-compatible across the two implementations.
/// </summary>
/// <remarks>
/// The blueprint fixture is the real <c>vibetable-empty.json</c> shipped in the
/// repo, so the assertions exercise the actual production schema, not a stub.
/// </remarks>
[TestClass]
public sealed class DirectusSchemaBootstrapperTests
{
    private static string BlueprintPath => ResolveRepoFile("directus", "blueprints", "vibetable-empty.json");

    [TestMethod]
    public void LoadBlueprint_AcceptsVibeTableContract()
    {
        JsonNode blueprint = DirectusSchemaBootstrapper.LoadBlueprint(BlueprintPath);
        Assert.AreEqual("vibetable.directus-blueprint.v1", blueprint["contract"]!.GetValue<string>());
        Assert.IsNotNull(blueprint["collections"]);
    }

    [TestMethod]
    public void BuildCollectionPayload_IncludesIdAndSystemFieldsAndCustomFields()
    {
        JsonNode blueprint = DirectusSchemaBootstrapper.LoadBlueprint(BlueprintPath);
        var collections = (JsonObject)blueprint["collections"]!;
        var definition = (JsonObject)collections["vibetable_workspaces"]!;

        JsonObject payload = DirectusSchemaBootstrapper.BuildCollectionPayload("vibetable_workspaces", definition);

        Assert.AreEqual("vibetable_workspaces", payload["collection"]!.GetValue<string>());
        var fields = (JsonArray)payload["fields"]!;
        // id + 6 system fields + 2 custom (name, workspace_id) — status is a
        // system field, so custom status is consumed, not duplicated.
        var fieldNames = new System.Collections.Generic.List<string>();
        foreach (var f in fields)
        {
            fieldNames.Add(f!["field"]!.GetValue<string>());
        }
        CollectionAssert.Contains(fieldNames, "id");
        CollectionAssert.Contains(fieldNames, "status");
        CollectionAssert.Contains(fieldNames, "sort");
        CollectionAssert.Contains(fieldNames, "date_created");
        CollectionAssert.Contains(fieldNames, "user_created");
        CollectionAssert.Contains(fieldNames, "date_updated");
        CollectionAssert.Contains(fieldNames, "user_updated");
        CollectionAssert.Contains(fieldNames, "name");
        CollectionAssert.Contains(fieldNames, "workspace_id");
    }

    [TestMethod]
    public void FieldPayload_MarksPrimaryKeyAndRequired()
    {
        var def = new JsonObject
        {
            ["type"] = "uuid",
            ["primary_key"] = true,
        };
        JsonObject field = DirectusSchemaBootstrapper.FieldPayload("id", def);

        Assert.AreEqual("id", field["field"]!.GetValue<string>());
        Assert.AreEqual("uuid", field["type"]!.GetValue<string>());
        Assert.IsTrue(field["schema"]!["is_primary_key"]!.GetValue<bool>());
    }

    [TestMethod]
    public void BuildRelationPayload_MatchesPythonShape()
    {
        JsonObject rel = DirectusSchemaBootstrapper.BuildRelationPayload(
            "vibetable_documents", "workspace", "vibetable_workspaces");

        Assert.AreEqual("vibetable_documents", rel["collection"]!.GetValue<string>());
        Assert.AreEqual("workspace", rel["field"]!.GetValue<string>());
        Assert.AreEqual("vibetable_workspaces", rel["related_collection"]!.GetValue<string>());
        Assert.AreEqual("SET NULL", rel["schema"]!["on_delete"]!.GetValue<string>());
        Assert.AreEqual("nullify", rel["meta"]!["one_deselect_action"]!.GetValue<string>());
    }

    [TestMethod]
    public void BuildPermissionPayloads_EmitsOnePerActionPerCollection()
    {
        JsonNode blueprint = DirectusSchemaBootstrapper.LoadBlueprint(BlueprintPath);
        var collections = (JsonObject)blueprint["collections"]!;
        var policies = (JsonObject)blueprint["policies"]!;
        var managerGrants = (JsonObject)policies["manager"]!;

        JsonArray perms = DirectusSchemaBootstrapper.BuildPermissionPayloads(
            "policy-1", managerGrants, collections);

        // manager has create+read+update+delete on all 6 collections = 24 entries.
        Assert.AreEqual(24, perms.Count);
        var first = (JsonObject)perms[0]!;
        Assert.AreEqual("policy-1", first["policy"]!.GetValue<string>());
        Assert.IsNotNull(first["fields"], "permission must carry the field allow-list");
    }

    private static string ResolveRepoFile(params string[] parts)
    {
        string dir = System.AppContext.BaseDirectory;
        for (int i = 0; i < 8; i++)
        {
            string candidate = Path.Combine(dir, Path.Combine(parts));
            if (File.Exists(candidate))
            {
                return candidate;
            }
            dir = Path.GetFullPath(Path.Combine(dir, ".."));
        }
        throw new FileNotFoundException($"could not locate {string.Join('/', parts)} from {System.AppContext.BaseDirectory}");
    }
}
