using System.IO;
using System.Text.Json;

namespace VibeTable.Contracts.Tests;

/// <summary>
/// Fixture-driven deserialization tests for the G1 full-field history contracts.
/// </summary>
/// <remarks>
/// The fixture under <c>tests/contract/fixtures/table-g1-history-contracts.json</c>
/// is the authoritative cross-language wire contract. These tests pin that the
/// C# records deserialize the exact camelCase bytes the Python service emits.
/// </remarks>
[TestClass]
public sealed class G1HistoryFixtureTests
{
    private static readonly JsonSerializerOptions Options =
        new(JsonSerializerDefaults.Web);

    [TestMethod]
    public void ReadChangeSetsResult_Deserializes_AllFields()
    {
        var json = ReadFixture("table-g1-history-contracts.json");
        using var doc = JsonDocument.Parse(json);
        var resultElement = doc.RootElement.GetProperty("readChangeSets").GetProperty("result");
        var page = JsonSerializer.Deserialize<HistoryPage>(resultElement.GetRawText(), Options);

        Assert.IsNotNull(page);
        Assert.AreEqual("vibetable_demo", page!.Collection);
        Assert.AreEqual("c-001", page.ItemId);
        Assert.AreEqual(2, page.ChangeSets.Count);
        Assert.AreEqual(2, page.Total);
        Assert.AreEqual("vibetable-1.0", page.SchemaRevision);
    }

    [TestMethod]
    public void ChangeSet_HasScalarAndRelationChanges()
    {
        var json = ReadFixture("table-g1-history-contracts.json");
        using var doc = JsonDocument.Parse(json);
        var resultElement = doc.RootElement.GetProperty("readChangeSets").GetProperty("result");
        var page = JsonSerializer.Deserialize<HistoryPage>(resultElement.GetRawText(), Options);

        Assert.IsNotNull(page);
        var cs = page!.ChangeSets[0];
        Assert.AreEqual("update", cs.Action);
        Assert.AreEqual("Ada Lovelace", cs.Actor?.DisplayName);
        Assert.AreEqual(2, cs.ScalarChanges.Count);
        Assert.AreEqual(1, cs.RelationChanges.Count);

        var sc = cs.ScalarChanges[0];
        Assert.AreEqual("title", sc.Field);

        var rc = cs.RelationChanges[0];
        Assert.AreEqual("project", rc.Field);
        Assert.AreEqual("m2o", rc.Kind);
        Assert.AreEqual("vibetable_demo", rc.RelatedCollection);
    }

    [TestMethod]
    public void PreviewRestore_Deserializes_AllFields()
    {
        var json = ReadFixture("table-g1-history-contracts.json");
        using var doc = JsonDocument.Parse(json);
        var previewElement = doc.RootElement.GetProperty("previewRestore").GetProperty("result");
        var preview = JsonSerializer.Deserialize<RestorePreview>(previewElement.GetRawText(), Options);

        Assert.IsNotNull(preview);
        Assert.AreEqual("current-hash-abc", preview!.CurrentHash);
        Assert.AreEqual("vibetable-1.0", preview.SchemaRevision);
        Assert.AreEqual(2, preview.ScalarChanges.Count);
        Assert.AreEqual(0, preview.Diagnostics.Count);
    }

    [TestMethod]
    public void PreviewRestoreWithDiagnostics_Deserializes_Diagnostics()
    {
        var json = ReadFixture("table-g1-history-contracts.json");
        using var doc = JsonDocument.Parse(json);
        var previewElement = doc.RootElement.GetProperty("previewRestoreWithDiagnostics").GetProperty("result");
        var preview = JsonSerializer.Deserialize<RestorePreview>(previewElement.GetRawText(), Options);

        Assert.IsNotNull(preview);
        Assert.AreEqual(2, preview!.Diagnostics.Count);
        var diag = preview.Diagnostics[0];
        Assert.AreEqual("legacy_field", diag.Field);
        Assert.AreEqual("schema_retired", diag.Classification);
        Assert.AreEqual("error", diag.Severity);
    }

    [TestMethod]
    public void ApplyRestoreResult_Deserializes_AllFields()
    {
        var json = ReadFixture("table-g1-history-contracts.json");
        using var doc = JsonDocument.Parse(json);
        var resultElement = doc.RootElement.GetProperty("applyRestore").GetProperty("result");
        var result = JsonSerializer.Deserialize<RestoreResult>(resultElement.GetRawText(), Options);

        Assert.IsNotNull(result);
        Assert.AreEqual("rev-9", result!.RestoredToRevision);
        Assert.AreEqual("rev-11", result.NewRevisionId);
        Assert.IsTrue(result.Item.ContainsKey("title"));
    }

    [TestMethod]
    public void Fixture_DisablesContentVersions()
    {
        var json = ReadFixture("table-g1-history-contracts.json");
        using var doc = JsonDocument.Parse(json);
        var disabled = doc.RootElement.GetProperty("disabledFeatures");
        var hasContentVersions = false;
        foreach (var item in disabled.EnumerateArray())
        {
            if (item.GetString() == "content_versions")
            {
                hasContentVersions = true;
                break;
            }
        }
        Assert.IsTrue(hasContentVersions, "content_versions must be in disabledFeatures");
    }

    private static string ReadFixture(string name)
    {
        var path = Path.Combine(AppContext.BaseDirectory, "fixtures", name);
        return File.ReadAllText(path);
    }
}
