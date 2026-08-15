using System.IO;
using System.Text.Json;

namespace VibeTable.Contracts.Tests;

/// <summary>
/// Fixture-driven deserialization tests for the C1 data-IO contracts.
/// </summary>
/// <remarks>
/// The fixture under <c>tests/contract/fixtures/table-c1-data-io-contracts.json</c>
/// is generated verbatim by the Python backend's Pydantic serialization
/// (camelCase wire form). These tests pin the cross-language contract: the C#
/// records MUST deserialize the exact bytes the Python service emits.
/// </remarks>
[TestClass]
public sealed class C1DataIoContractsFixtureTests
{
    private static readonly JsonSerializerOptions Options =
        new(JsonSerializerDefaults.Web);

    [TestMethod]
    public void TaskStatusFixture_Deserializes_AllFields()
    {
        var json = ReadFixture("table-c1-data-io-contracts.json");
        using var doc = JsonDocument.Parse(json);
        var statusElement = doc.RootElement.GetProperty("taskRuntime").GetProperty("status");
        var status = JsonSerializer.Deserialize<TaskStatus>(statusElement.GetRawText(), Options);

        Assert.IsNotNull(status);
        Assert.AreEqual(TaskStates.Running, status!.State);
        Assert.AreEqual(42, status.Progress.Done);
        Assert.AreEqual(100, status.Progress.Total);
    }

    [TestMethod]
    public void ImportPlanFixture_Deserializes_AllFields()
    {
        var json = ReadFixture("table-c1-data-io-contracts.json");
        using var doc = JsonDocument.Parse(json);
        var planElement = doc.RootElement.GetProperty("importPlan").GetProperty("plan");
        var plan = JsonSerializer.Deserialize<ImportPlan>(planElement.GetRawText(), Options);

        Assert.IsNotNull(plan);
        Assert.AreEqual(3, plan!.Summary.TotalRows);
        Assert.AreEqual(1, plan.Summary.ErrorRows);
        Assert.AreEqual("imp-fixture-token", plan.Token.Token);
        // The error row carries a localized diagnostic.
        Assert.AreEqual("error", plan.Rows[2].Diagnostics[0].Severity);
    }

    [TestMethod]
    public void ApplyImportResultFixture_Deserializes_AllFields()
    {
        var json = ReadFixture("table-c1-data-io-contracts.json");
        using var doc = JsonDocument.Parse(json);
        var resultElement = doc.RootElement.GetProperty("applyImport").GetProperty("result");
        var result = JsonSerializer.Deserialize<ApplyImportResult>(resultElement.GetRawText(), Options);

        Assert.IsNotNull(result);
        Assert.AreEqual(2, result!.CreatedCount);
        CollectionAssert.Contains(result.FailedRows.ToList(), 4);
    }

    [TestMethod]
    public void ExportResultFixture_Deserializes_AllFields()
    {
        var json = ReadFixture("table-c1-data-io-contracts.json");
        using var doc = JsonDocument.Parse(json);
        var resultElement = doc.RootElement.GetProperty("export").GetProperty("result");
        var result = JsonSerializer.Deserialize<ExportResult>(resultElement.GetRawText(), Options);

        Assert.IsNotNull(result);
        Assert.AreEqual(ExportFormats.Csv, result!.Format);
        Assert.AreEqual(150, result.RowsWritten);
    }

    private static string ReadFixture(string name)
    {
        var path = Path.Combine(AppContext.BaseDirectory, "fixtures", name);
        return File.ReadAllText(path);
    }
}
