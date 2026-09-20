using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WindowsHostCommandActionsTests
{
    [TestMethod]
    public async Task EveryExportSelectsAndRegistersFreshAuthorityBeforeExecution()
    {
        var gateway = new ExportGateway();
        var files = new ExportPicker();
        var actions = new WindowsHostCommandActions(null!, () => gateway, files, null);
        JsonElement parameters = JsonSerializer.SerializeToElement(new { collection = "orders", query = new { keyword = "chosen" }, format = "csv" });
        for (int index = 1; index <= 2; index++)
        {
            JsonElement result = await actions.ExportAsync(parameters, () => { }, CancellationToken.None);
            Assert.AreEqual(2, result.GetProperty("rowsWritten").GetInt32());
            Assert.AreEqual($"fresh-{index}", gateway.Executions[index - 1].GetProperty("grantId").GetString());
            Assert.AreEqual("chosen", gateway.Executions[index - 1].GetProperty("query").GetProperty("keyword").GetString());
            Assert.IsFalse(gateway.Executions[index - 1].TryGetProperty("path", out _));
        }
        Assert.AreEqual(2, files.Selections);
        Assert.AreEqual(2, gateway.Grants);
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task PickerCancellationOrWorkspaceChangeNeverRegistersAnExport(bool workspaceChanges)
    {
        var gateway = new ExportGateway();
        IHostCommandExportGateway? current = gateway;
        var files = new ExportPicker { Selected = workspaceChanges ? @"C:\selected.csv" : null,
            BeforeReturn = () => { if (workspaceChanges) current = new ExportGateway(); } };
        var actions = new WindowsHostCommandActions(null!, () => current, files, null);
        await Assert.ThrowsAsync<OperationCanceledException>(() => actions.ExportAsync(
            JsonSerializer.SerializeToElement(new { collection = "orders", query = new { }, format = "csv" }),
            () => { }, CancellationToken.None));
        Assert.AreEqual(0, gateway.Grants);
        Assert.IsEmpty(gateway.Executions);
    }

    private sealed class ExportGateway : IHostCommandExportGateway
    {
        internal int Grants { get; private set; }
        internal List<JsonElement> Executions { get; } = [];
        public Task<JsonElement> RegisterExportTargetAsync(JsonElement parameters, CancellationToken token)
        {
            Assert.AreEqual(@"C:\selected.csv", parameters.GetProperty("path").GetString());
            return Task.FromResult(JsonSerializer.SerializeToElement(new { grantId = $"fresh-{++Grants}" }));
        }
        public Task<JsonElement> ExecuteExportAsync(JsonElement parameters, CancellationToken token)
        {
            Executions.Add(parameters.Clone());
            return Task.FromResult(JsonSerializer.SerializeToElement(new { rowsWritten = 2, outputDisplayName = "selected.csv" }));
        }
    }
    private sealed class ExportPicker : INativeProductFileHost
    {
        internal string? Selected { get; init; } = @"C:\selected.csv";
        internal Action? BeforeReturn { get; init; }
        internal int Selections { get; private set; }
        public string? SelectExportTarget(string format, string defaultName)
        {
            Assert.AreEqual("csv", format); Assert.AreEqual("vibetable-export.csv", defaultName);
            Selections++; BeforeReturn?.Invoke(); return Selected;
        }
        public string? SelectImportSource() => throw new NotSupportedException();
        public NativeAttachmentSelection SelectAttachmentSources(bool replacement) => throw new NotSupportedException();
        public string? SelectAttachmentTarget(string suggestedName) => throw new NotSupportedException();
        public string CreateAttachmentPreviewPath(string suggestedName) => throw new NotSupportedException();
        public Task PreviewAttachmentAsync(string fullPath) => throw new NotSupportedException();
    }
}
