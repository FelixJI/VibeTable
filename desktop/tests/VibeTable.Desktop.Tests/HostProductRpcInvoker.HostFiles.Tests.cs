using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

public sealed partial class HostProductRpcInvokerTests
{
    [TestMethod]
    public async Task PickerResultCannotRegisterIntoGatewayActivatedDuringDialog()
    {
        await using var oldWorkspace = await HostFixture.OpenAsync();
        await using var nextWorkspace = await HostFixture.OpenAsync();
        using var oldGateway = oldWorkspace.Gateway(useGeneratedPolicy: true);
        using var nextGateway = nextWorkspace.Gateway(useGeneratedPolicy: true);
        oldGateway.EnableHostFiles();
        nextGateway.EnableHostFiles();
        JsonRpcProductDataGateway current = oldGateway;
        var sink = new FakeWebReplySink();
        var controller = new NativeProductFileRequestController(sink,
            new ProductFileRpcGatewayAdapter(() => current), new SwitchingFilePicker(() =>
            {
                oldWorkspace.Current = false;
                current = nextGateway;
            }));
        await controller.DispatchAsync(new RoutedWebRequest("data.exportTargetRequested", "selection", Json("{}"), ""));
        Assert.AreEqual("operation.failed", sink.Replies.Single().Type);
        Assert.AreEqual("PATH_GRANT_FAILED", JsonSerializer.SerializeToElement(sink.Replies.Single().Payload).GetProperty("code").GetString());
        Assert.AreEqual(0, oldWorkspace.Python.WriteCount);
        Assert.AreEqual(0, nextWorkspace.Python.WriteCount);
    }

    private sealed class SwitchingFilePicker(Action switchWorkspace) : INativeProductFileHost
    {
        public string? SelectExportTarget(string format, string defaultName)
        {
            switchWorkspace();
            return Path.Combine(Path.GetTempPath(), "retired-selection.csv");
        }
        public string? SelectImportSource() => throw new NotSupportedException();
        public NativeAttachmentSelection SelectAttachmentSources(bool replacement) => throw new NotSupportedException();
        public string? SelectAttachmentTarget(string suggestedName) => throw new NotSupportedException();
        public string CreateAttachmentPreviewPath(string suggestedName) => throw new NotSupportedException();
        public Task PreviewAttachmentAsync(string fullPath) => throw new NotSupportedException();
    }
}
