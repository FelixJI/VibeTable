using System.IO;
using System.Text.Json;

namespace VibeTable.Contracts.Tests;

/// <summary>
/// Fixture-driven deserialization tests for the Task-10 table contracts.
/// </summary>
/// <remarks>
/// <para>
/// The fixtures under <c>tests/contract/fixtures</c> are generated verbatim by
/// the Python backend's Pydantic serialization (camelCase wire form). These
/// tests pin the cross-language contract: the C# records MUST deserialize the
/// exact bytes the Python service emits, byte-for-byte on the field names the
/// web grid reads.
/// </para>
/// <para>
/// TDD order: this file is written BEFORE the production wiring, so a RED run
/// (records missing or field names mismatched) drives the GREEN implementation
/// in <c>TableContracts.cs</c>.
/// </para>
/// </remarks>
[TestClass]
public sealed class TableContractsFixtureTests
{
    private static readonly JsonSerializerOptions Options =
        new(JsonSerializerDefaults.Web);

    [TestMethod]
    public void DatabaseOpenResultFixture_Deserializes_TablesAndViews()
    {
        var json = ReadFixture("database-open-result.json");
        var result = JsonSerializer.Deserialize<DatabaseOpenResult>(json, Options);

        Assert.IsNotNull(result);
        CollectionAssert.AreEqual(
            new[] { "contracts" },
            (System.Collections.ICollection)result!.Tables);
        Assert.AreEqual(0, result.Views.Count);
        Assert.IsNull(result.DisplayNames, "legacy payloads omit the optional displayNames map");
    }

    [TestMethod]
    public void DatabaseOpenResult_DeserializesPhysicalKeysAndUnicodeDisplayNames()
    {
        const string json = """
            {"tables":["vt_t_01K0000000000000"],"views":[],"displayNames":{"vt_t_01K0000000000000":"客户清单 ✅"}}
            """;

        var result = JsonSerializer.Deserialize<DatabaseOpenResult>(json, Options);

        Assert.IsNotNull(result);
        Assert.AreEqual("vt_t_01K0000000000000", result!.Tables[0]);
        Assert.AreEqual("客户清单 ✅", result.DisplayNames![result.Tables[0]]);
    }

    [TestMethod]
    public void TablePageFixture_Deserializes_AllFields_IncludingRowKey()
    {
        var json = ReadFixture("table-page-contracts.json");
        var page = JsonSerializer.Deserialize<TablePage>(json, Options);

        Assert.IsNotNull(page);
        Assert.AreEqual("contracts", page!.Table);
        Assert.AreEqual(3, page.Columns.Count);

        // Column schema fields (camelCase wire).
        var idCol = page.Columns[0];
        Assert.AreEqual("id", idCol.Name);
        Assert.AreEqual("id", idCol.Title);
        Assert.AreEqual("integer", idCol.DataType);
        Assert.AreEqual(false, idCol.Editable);
        Assert.AreEqual(false, idCol.Nullable);

        // Rows carry column values PLUS the hidden transport rowKey. Values
        // deserialize as JsonElement (System.Text.Json's faithful representation
        // for object?) so the host can re-serialize them verbatim to the web.
        Assert.AreEqual(3, page.Rows.Count);
        Assert.AreEqual(1, GetInt(page.Rows[0], "id"));
        Assert.AreEqual("Alpha", GetString(page.Rows[0], "name"));
        Assert.AreEqual(12.5m, GetDecimal(page.Rows[0], "amount"));
        Assert.AreEqual(1, GetInt(page.Rows[0], "rowKey"));

        // Round-trip: re-serializing the deserialized page preserves all the
        // wire fields the web layer consumes (the host forwards DTOs verbatim).
        var roundTripped = JsonSerializer.Serialize(page, Options);
        Assert.IsTrue(roundTripped.Contains("\"table\":\"contracts\"", System.StringComparison.Ordinal));
        Assert.IsTrue(roundTripped.Contains("\"rowKey\":1", System.StringComparison.Ordinal));

        // Paging + mode metadata.
        Assert.AreEqual(0, page.Offset);
        Assert.AreEqual(500, page.Limit);
        Assert.AreEqual(3, page.TotalRows);
        Assert.AreEqual("client", page.Mode);
    }

    [TestMethod]
    public void ColumnSchema_DeserializesAndRoundTripsRelationLookupMetadata()
    {
        const string json = """
            {"table":"orders","columns":[
              {"name":"contract","title":"Contract","dataType":"text","editable":true,"nullable":true,
               "fieldId":"orders.contract","kind":"relation","relationId":"provider:7:m2o"},
              {"name":"contract_price","title":"Contract price","dataType":"decimal","editable":false,"nullable":true,
               "scale":2,"fieldId":"orders.contract_price","kind":"lookup","lookupId":"orders.contract_price"}
            ],"rows":[],"offset":0,"limit":100,"totalRows":0,"mode":"client"}
            """;

        var page = JsonSerializer.Deserialize<TablePage>(json, Options);

        Assert.IsNotNull(page);
        Assert.AreEqual("orders.contract", page!.Columns[0].FieldId);
        Assert.AreEqual("relation", page.Columns[0].Kind);
        Assert.AreEqual("provider:7:m2o", page.Columns[0].RelationId);
        Assert.AreEqual("lookup", page.Columns[1].Kind);
        Assert.AreEqual("orders.contract_price", page.Columns[1].LookupId);
        Assert.IsFalse(page.Columns[1].Editable);

        var roundTripped = JsonSerializer.Serialize(page, Options);
        StringAssert.Contains(roundTripped, "\"relationId\":\"provider:7:m2o\"");
        StringAssert.Contains(roundTripped, "\"lookupId\":\"orders.contract_price\"");
    }

    private static string ReadFixture(string name)
    {
        var path = Path.Combine(AppContext.BaseDirectory, "fixtures", name);
        return File.ReadAllText(path);
    }

    private static int GetInt(IReadOnlyDictionary<string, object?> row, string key)
        => GetElement(row, key).GetInt32();

    private static string GetString(IReadOnlyDictionary<string, object?> row, string key)
        => GetElement(row, key).GetString()!;

    private static decimal GetDecimal(IReadOnlyDictionary<string, object?> row, string key)
        => GetElement(row, key).GetDecimal();

    private static JsonElement GetElement(IReadOnlyDictionary<string, object?> row, string key)
    {
        var value = row[key];
        Assert.IsInstanceOfType(value, typeof(JsonElement));
        return (JsonElement)value!;
    }
}
