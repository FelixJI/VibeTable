using System.IO;
using System.Linq;
using System.Text.Json;

namespace VibeTable.Contracts.Tests;

/// <summary>
/// Fixture-driven deserialization tests for the B3 query/state contracts.
/// </summary>
/// <remarks>
/// The fixtures under <c>tests/contract/fixtures</c> are generated verbatim by
/// the Python backend's Pydantic serialization (camelCase wire form). These
/// tests pin the cross-language contract: the C# records MUST deserialize the
/// exact bytes the Python service emits.
/// </remarks>
[TestClass]
public sealed class B3ContractsFixtureTests
{
    private static readonly JsonSerializerOptions Options =
        new(JsonSerializerDefaults.Web);

    [TestMethod]
    public void TableQueryFixture_Deserializes_AllFields()
    {
        var json = ReadFixture("table-query.json");
        var query = JsonSerializer.Deserialize<TableQuery>(json, Options);

        Assert.IsNotNull(query);
        // The fixture has 5 filters and 1 sort.
        Assert.IsNotNull(query!.Filters);
        Assert.AreEqual(5, query.Filters!.Count);
        Assert.IsNotNull(query.Sorts);
        Assert.AreEqual(1, query.Sorts!.Count);
        Assert.AreEqual(0, query.Offset);
        Assert.AreEqual(100, query.Limit);
    }

    [TestMethod]
    public void QuerySnapshotFixture_Deserializes_AllFields()
    {
        var json = ReadFixture("query-snapshot.json");
        var snap = JsonSerializer.Deserialize<QuerySnapshot>(json, Options);

        Assert.IsNotNull(snap);
        Assert.AreEqual("contracts", snap!.Table);
        Assert.AreEqual(42, snap.DataRevision);
        Assert.AreEqual(16, snap.Digest.Length);
        Assert.IsNotNull(snap.NormalizedQuery);
    }

    [TestMethod]
    public void SelectionSnapshotFixture_Deserializes_AllFields()
    {
        var json = ReadFixture("selection-snapshot.json");
        var sel = JsonSerializer.Deserialize<SelectionSnapshot>(json, Options);

        Assert.IsNotNull(sel);
        Assert.IsNotNull(sel!.QuerySnapshot);
        Assert.AreEqual(42, sel.DataRevision);
        // RowKeys deserializes as JsonElement values; compare their int form.
        var keys = sel.RowKeys.Select(k =>
            k is JsonElement je ? je.GetInt32() : Convert.ToInt32(k)).ToList();
        CollectionAssert.AreEqual(new[] { 17, 23, 41 }, keys);
    }

    [TestMethod]
    public void GridStateFixture_Deserializes_AllFields()
    {
        var json = ReadFixture("grid-state.json");
        var state = JsonSerializer.Deserialize<GridState>(json, Options);

        Assert.IsNotNull(state);
        Assert.IsNotNull(state!.Columns);
        Assert.AreEqual(2, state.Columns!.Count);
        Assert.AreEqual("comfortable", state.Density);
        Assert.IsFalse(state.ForcedRemote);
        Assert.IsNotNull(state.Sorts);
        Assert.AreEqual(1, state.Sorts!.Count);
    }

    [TestMethod]
    public void FilterConditionRecord_RoundTrips_CamelCase()
    {
        var original = new FilterCondition("amount", "gt", 100, "AND");
        var json = JsonSerializer.Serialize(original, Options);
        // camelCase wire form.
        StringAssert.Contains(json, "field");
        StringAssert.Contains(json, "operator");
        StringAssert.Contains(json, "logic");
        var round = JsonSerializer.Deserialize<FilterCondition>(json, Options);
        Assert.IsNotNull(round);
        Assert.AreEqual("amount", round!.Field);
        Assert.AreEqual("gt", round.Operator);
    }

    private static string ReadFixture(string name)
    {
        var path = Path.Combine(AppContext.BaseDirectory, "fixtures", name);
        return File.ReadAllText(path);
    }
}
