using System.Text.Json;
using VibeTable.Desktop.Services;
namespace VibeTable.Desktop.Tests;

public sealed partial class HostProductRpcInvokerTests
{
    [TestMethod]
    public async Task PluginIoPortUsesExistingBrokerTaskAdmissionReportsAndStickyTerminal()
    {
        var transport = new DelayedAdmissionTransport();
        await using var fixture = await HostFixture.OpenAsync(transport: transport);
        using var gateway = fixture.Gateway(useGeneratedPolicy: true);
        var broker = gateway.EnableHostFiles();
        var port = gateway.CapturePluginDataIo();
        Assert.AreSame(broker, port.Files);
        var starting = port.Start("data.export", Json("""{"grantId":"opaque","pluginScope":{"runId":"host-run","principal":{"installationId":"captured"}}}"""), CancellationToken.None);
        string taskId = await transport.StartQueued.Task.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.AreEqual("queued", port.Status(taskId).GetProperty("state").GetString());
        transport.AcknowledgeStart(committed: true);
        await starting;
        Assert.AreEqual("succeeded", port.Status(taskId).GetProperty("state").GetString());
        await port.Cancel(taskId, CancellationToken.None);
        Assert.AreEqual("succeeded", port.Status(taskId).GetProperty("state").GetString());
        Assert.AreEqual(0, transport.CancelCount);
    }
}
