using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class HostLifecycleRequestControllerTests
{
    [TestMethod]
    public void DiagnosticsPayloadValidationStaysInsideHostController()
    {
        var reply = new FakeWebReplySink();
        var host = new FakeHost();
        var controller = new HostLifecycleRequestController(reply, host);

        controller.Dispatch(Request(
            "diagnostics.get",
            JsonSerializer.SerializeToElement(new { unexpected = true })));

        Assert.AreEqual(0, host.DiagnosticsCalls);
        Assert.AreEqual("operation.failed", reply.Replies.Single().Type);
        Assert.AreEqual(
            "DIAGNOSTICS_BAD_PAYLOAD",
            JsonSerializer.SerializeToElement(reply.Replies.Single().Payload)
                .GetProperty("code")
                .GetString());
    }

    [TestMethod]
    public void LifecycleRoutesInvokeOnlyTheirOwnedAction()
    {
        var host = new FakeHost();
        var controller = new HostLifecycleRequestController(
            new FakeWebReplySink(),
            host);

        controller.Dispatch(Request("app.ready", JsonSerializer.SerializeToElement(new { })));
        controller.Dispatch(Request(
            "host.startupRetryRequested",
            JsonSerializer.SerializeToElement(new { })));
        controller.Dispatch(Request(
            "host.startupCancelRequested",
            JsonSerializer.SerializeToElement(new { })));

        Assert.AreEqual(1, host.ReadyCalls);
        Assert.AreEqual(1, host.RetryCalls);
        Assert.AreEqual(1, host.ExitCalls);
    }

    private static RoutedWebRequest Request(string type, JsonElement payload) =>
        new(type, "request-1", payload, string.Empty);

    private sealed class FakeHost : IHostLifecycleActions
    {
        public int ReadyCalls { get; private set; }
        public int ExitCalls { get; private set; }
        public int RetryCalls { get; private set; }
        public int DiagnosticsCalls { get; private set; }

        public void RendererReady() => ReadyCalls++;
        public void RequestExit() => ExitCalls++;
        public void RetryStartup() => RetryCalls++;
        public bool OpenAdmin() => false;
        public Task BuildDiagnosticsAsync(RoutedWebRequest request)
        {
            DiagnosticsCalls++;
            return Task.CompletedTask;
        }
    }
}
