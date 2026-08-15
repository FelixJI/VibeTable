using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceBootstrapPublisherTests
{
    [TestMethod]
    public void PostProjectsRegistrySessionCapabilitiesAndMeteredStorage()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-bootstrap-");
        WorkspaceRegistryEntryV2 workspace = fixture.AddDirectWorkspace("Bootstrap");
        fixture.Session.CurrentWorkspace = workspace;
        fixture.Session.CurrentSession = OpenSession(workspace.WorkspaceId, 31);
        fixture.Session.CurrentCapabilities = new WorkspaceV2SidecarCapabilities(
            WorkspaceV2Json.ContractVersion,
            workspace.WorkspaceId.ToString("D"),
            31,
            1,
            Guid.NewGuid().ToString("D"),
            ["repository.verify"]);
        var reply = new RecordingReply();
        var meter = new FixedStorageMeter();
        IWorkspaceBootstrapPublisher publisher = new WorkspaceBootstrapPublisher(
            reply,
            new ReadyHost(),
            fixture.Session,
            fixture.Registry,
            fixture.Policy,
            meter);

        publisher.Post();

        Assert.IsTrue(reply.Notification.HasValue);
        JsonElement bootstrap = reply.Notification.Value;
        Assert.AreEqual("workspace.v2.bootstrap", reply.Type);
        Assert.AreEqual(1, bootstrap.GetProperty("workspaces").GetArrayLength());
        Assert.AreEqual(
            31UL,
            bootstrap.GetProperty("session").GetProperty("sessionEpoch").GetUInt64());
        Assert.IsTrue(bootstrap.GetProperty("capabilities")
            .EnumerateArray()
            .Any(item => item.GetString() == "repository.settings.v2"));
        Assert.AreEqual(
            23L,
            bootstrap.GetProperty("storage").GetProperty("logicalSize").GetInt64());
        Assert.AreEqual(
            29L,
            bootstrap.GetProperty("storage").GetProperty("physicalSize").GetInt64());
        Assert.AreEqual(1, meter.Calls);
    }

    private static WorkspaceSessionV2 OpenSession(Guid workspaceId, ulong epoch) => new()
    {
        ContractVersion = WorkspaceV2Json.ContractVersion,
        WorkspaceId = workspaceId,
        SessionEpoch = epoch,
        State = WorkspaceSessionState.OpenedWritable,
        OpenMode = WorkspaceOpenMode.Writable,
        Writable = true,
        Provisional = false,
        Phase = WorkspaceSessionPhase.Idle,
        ErrorCode = null,
    };

    private sealed class FixedStorageMeter : IWorkspaceStorageMeter
    {
        public int Calls { get; private set; }

        public WorkspaceStorageMeasurement Measure(WorkspaceRegistryEntryV2 workspace)
        {
            Calls++;
            return new WorkspaceStorageMeasurement(23, 29);
        }
    }

    private sealed class RecordingReply : IWorkspaceProductReplySink
    {
        public string? Type { get; private set; }
        public JsonElement? Notification { get; private set; }

        public void PostNotification(string type, object? payload)
        {
            Type = type;
            Notification = JsonSerializer.SerializeToElement(payload);
        }

        public void PostWorkspaceV2Response(
            string? requestId,
            object payload,
            JsonElement wire) => Assert.Fail("Unexpected response.");

        public void PostWorkspaceV2Event(object payload, JsonElement wire) =>
            Assert.Fail("Unexpected event.");
    }

    private sealed class ReadyHost : IWorkspaceProductHost
    {
        public bool IsRendererReady => true;
        public bool IsClosing => false;
        public bool HasDocumentWorkspace => false;
        public void Schedule(Action action) => action();
        public void OpenProductWorkspaceWhenReady() { }
        public void WriteError(string message) => Assert.Fail(message);
    }
}
