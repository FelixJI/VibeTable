using System.IO;
using System.Linq;
using System.Text.Json;

namespace VibeTable.Contracts.Tests;

/// <summary>
/// Fixture-driven deserialization tests for the B2 paste contracts.
/// </summary>
/// <remarks>
/// The fixture under <c>tests/contract/fixtures/table-b2-paste-contracts.json</c>
/// is generated verbatim by the Python backend's Pydantic serialization
/// (camelCase wire form). These tests pin the cross-language contract: the C#
/// records MUST deserialize the exact bytes the Python service emits.
/// </remarks>
[TestClass]
public sealed class B2ContractsFixtureTests
{
    private static readonly JsonSerializerOptions Options =
        new(JsonSerializerDefaults.Web);

    [TestMethod]
    public void PastePlanFixture_Deserializes_AllFields()
    {
        var json = ReadFixture("table-b2-paste-contracts.json");
        using var doc = JsonDocument.Parse(json);
        var planElement = doc.RootElement.GetProperty("preview").GetProperty("plan");
        var plan = JsonSerializer.Deserialize<PastePlan>(planElement.GetRawText(), Options);

        Assert.IsNotNull(plan);
        Assert.AreEqual("vibetable_demo", plan!.Collection);
        Assert.AreEqual(2, plan.Summary.UpdateRows);
        Assert.AreEqual(2, plan.Rows.Count);
        Assert.AreEqual("update", plan.Rows[0].Kind);
        // TargetRowKey deserializes as JsonElement; compare the string form.
        Assert.AreEqual("contract-1", plan.Rows[0].TargetRowKey?.ToString());
        Assert.AreEqual("B-1", plan.Rows[0].Changes["number"]["after"]!.ToString());
        Assert.IsFalse(plan.Overflow);
    }

    [TestMethod]
    public void ApplyPasteResultFixture_Deserializes_AllOutcomes()
    {
        var json = ReadFixture("table-b2-paste-contracts.json");
        using var doc = JsonDocument.Parse(json);

        var committed = JsonSerializer.Deserialize<ApplyPasteResult>(
            doc.RootElement.GetProperty("applyCommitted").GetProperty("result").GetRawText(), Options);
        Assert.IsNotNull(committed);
        Assert.AreEqual(ApplyOutcomes.Committed, committed!.Outcome);
        // Row keys deserialize as JsonElement; compare the string forms.
        var updated = committed.UpdatedRowKeys.Select(k => k.ToString()).ToList();
        CollectionAssert.AreEqual(new[] { "contract-1", "contract-2" }, updated);

        var conflict = JsonSerializer.Deserialize<ApplyPasteResult>(
            doc.RootElement.GetProperty("applyConflict").GetProperty("result").GetRawText(), Options);
        Assert.IsNotNull(conflict);
        Assert.AreEqual(ApplyOutcomes.Conflict, conflict!.Outcome);
        Assert.AreEqual(1, conflict.Conflicts.Count);

        var pending = JsonSerializer.Deserialize<ApplyPasteResult>(
            doc.RootElement.GetProperty("applyPending").GetProperty("result").GetRawText(), Options);
        Assert.IsNotNull(pending);
        Assert.AreEqual(ApplyOutcomes.Pending, pending!.Outcome);
    }

    [TestMethod]
    public void ValidationErrorsFixture_CarriesLocalizedDiagnostics()
    {
        var json = ReadFixture("table-b2-paste-contracts.json");
        using var doc = JsonDocument.Parse(json);
        var rowsElement = doc.RootElement.GetProperty("validationErrors").GetProperty("rows");
        var rows = JsonSerializer.Deserialize<PastePlanRow[]>(rowsElement.GetRawText(), Options);

        Assert.IsNotNull(rows);
        Assert.AreEqual(1, rows!.Length);
        Assert.AreEqual(PasteDiagnosticSeverities.Error, rows[0].Diagnostics[0].Severity);
        Assert.AreEqual("column_readonly", rows[0].Diagnostics[0].Code);
    }

    private static string ReadFixture(string name)
    {
        var path = Path.Combine(AppContext.BaseDirectory, "fixtures", name);
        return File.ReadAllText(path);
    }
}
