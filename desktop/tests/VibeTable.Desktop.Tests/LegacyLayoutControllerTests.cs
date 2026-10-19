using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

public sealed partial class HostProductRpcInvokerTests
{
    private const string LegacySnapshot = """{"contractVersion":"1.0","interfaces":[{"logicalId":"legacy","rawPayload":"{broken","diagnostics":[{"code":"legacy.payload_unparseable","severity":"warning","message":"preserved"}]}],"dashboards":[],"panels":[],"collectedAt":"2026-10-19T00:00:00Z"}""";

    [TestMethod]
    [DataRow("{\"path\":\"C:/forged.json\"}")]
    [DataRow("{\"content\":{}}")]
    [DataRow("[]")]
    public async Task LegacyPreservationRejectsRendererContentBeforeAuthorityRead(string payload)
    {
        await using var fixture = await HostFixture.OpenAsync();
        using var gateway = fixture.Gateway(useGeneratedPolicy: true);
        var sink = new FakeWebReplySink();
        var controller = new LegacyLayoutRequestController(sink, fixture.Leases, () => gateway, () => null, () => throw new AssertFailedException("picker called"));
        await controller.DispatchAsync(new("legacyLayout.exportRequested", "forged", Json(payload), ""));
        Assert.AreEqual("BAD_PAYLOAD", JsonSerializer.SerializeToElement(sink.Replies.Single().Payload).GetProperty("code").GetString());
        Assert.AreEqual(0, fixture.Http.Calls.Count);
    }

    [TestMethod]
    public async Task LegacyPreservationExportsOnlyAuthorityAndCancellationPreservesExistingFile()
    {
        await using var fixture = await HostFixture.OpenAsync();
        fixture.Http.Result = Json(LegacySnapshot);
        using var gateway = fixture.Gateway(useGeneratedPolicy: true);
        var sink = new FakeWebReplySink();
        var directory = Path.Combine(Path.GetTempPath(), "legacy-export-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(directory);
        var target = Path.Combine(directory, "preserved.json");
        string? selected = null;
        var broker = new HostSessionFileBroker(() => {
            Assert.IsTrue(fixture.Leases.TryCaptureHost(fixture.Session.WorkspaceId!.Value, fixture.Session.SessionEpoch, Guid.NewGuid(), out var lease));
            return lease!;
        }, action => action());
        var controller = new LegacyLayoutRequestController(sink, fixture.Leases, () => gateway, () => broker, () => selected);
        RoutedWebRequest Request(string id) => new("legacyLayout.exportRequested", id, Json("{}"), "", new WorkspaceWireScope {
            Scope = "workspace", WorkspaceId = fixture.Session.WorkspaceId!.Value, SessionEpoch = fixture.Session.SessionEpoch,
            OperationId = Guid.NewGuid(), Sequence = fixture.Leases.ReserveHostSequence(fixture.Session.WorkspaceId!.Value, fixture.Session.SessionEpoch) + 100,
        });
        try
        {
            await File.WriteAllTextAsync(target, "existing material");
            await controller.DispatchAsync(Request("cancel"));
            Assert.AreEqual("existing material", await File.ReadAllTextAsync(target));
            Assert.IsTrue(JsonSerializer.SerializeToElement(sink.Replies.Single().Payload).GetProperty("cancelled").GetBoolean());
            selected = target;
            await controller.DispatchAsync(Request("export"));
            Assert.AreEqual(LegacySnapshot, await File.ReadAllTextAsync(target));
            Assert.AreEqual("preserved.json", JsonSerializer.SerializeToElement(sink.Replies.Last().Payload).GetProperty("outputDisplayName").GetString());
            Assert.HasCount(1, Directory.GetFiles(directory));
            selected = Path.Combine(directory, "missing", "denied.json");
            await controller.DispatchAsync(Request("denied"));
            Assert.AreEqual("operation.failed", sink.Replies.Last().Type);
            Assert.AreEqual(LegacySnapshot, await File.ReadAllTextAsync(target));
            Assert.HasCount(1, Directory.GetFiles(directory));
            Assert.IsTrue(fixture.Http.Calls.All(call => call.GetProperty("method").GetString() == "legacyLayout.snapshot" && call.GetProperty("params").GetRawText() == "{}"));
        }
        finally { File.Delete(target); Directory.Delete(directory); }
    }
    [TestMethod]
    [DataRow(true)]
    [DataRow(false)]
    public async Task LegacyPreservationCancellationAndCommitHaveOneTerminalOutcome(bool cancelBeforeCommit)
    {
        await using var fixture = await HostFixture.OpenAsync();
        fixture.Http.Result = Json(LegacySnapshot);
        using var gateway = fixture.Gateway(useGeneratedPolicy: true);
        var sink = new FakeWebReplySink();
        var directory = Path.Combine(Path.GetTempPath(), "legacy-settlement-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(directory);
        var target = Path.Combine(directory, "preserved.json");
        LegacyLayoutRequestController? controller = null;
        RoutedWebRequest Request(string type, string id, string payload) => new(type, id, Json(payload), "", new WorkspaceWireScope {
            Scope = "workspace", WorkspaceId = fixture.Session.WorkspaceId!.Value, SessionEpoch = fixture.Session.SessionEpoch,
            OperationId = Guid.NewGuid(), Sequence = fixture.Leases.ReserveHostSequence(fixture.Session.WorkspaceId!.Value, fixture.Session.SessionEpoch) + 100,
        });
        void Cancel() => controller!.DispatchAsync(Request("legacyLayout.cancelRequested", "cancel-settlement", """{"targetRequestId":"export-settlement"}""")).GetAwaiter().GetResult();
        int commitBoundaries = 0;
        var broker = new HostSessionFileBroker(() => {
            Assert.IsTrue(fixture.Leases.TryCaptureHost(fixture.Session.WorkspaceId!.Value, fixture.Session.SessionEpoch, Guid.NewGuid(), out var lease));
            return lease!;
        }, apply => {
            // Boundary one opens the target; boundary two atomically replaces it.
            bool settling = ++commitBoundaries == 2;
            if (settling && cancelBeforeCommit) Cancel();
            apply();
            if (settling && !cancelBeforeCommit) Cancel();
        });
        controller = new LegacyLayoutRequestController(sink, fixture.Leases, () => gateway, () => broker, () => target);
        try
        {
            await File.WriteAllTextAsync(target, "existing material");
            await controller.DispatchAsync(Request("legacyLayout.exportRequested", "export-settlement", "{}"));
            Assert.AreEqual(2, commitBoundaries);
            Assert.HasCount(1, sink.Replies);
            var reply = sink.Replies.Single();
            Assert.AreEqual(cancelBeforeCommit ? "operation.failed" : "legacyLayout.exportCompleted", reply.Type);
            var payload = JsonSerializer.SerializeToElement(reply.Payload);
            if (cancelBeforeCommit) Assert.AreEqual("CANCELLED", payload.GetProperty("code").GetString());
            else Assert.IsFalse(payload.GetProperty("cancelled").GetBoolean());
            Assert.AreEqual(cancelBeforeCommit ? "existing material" : LegacySnapshot, await File.ReadAllTextAsync(target));
            Assert.HasCount(1, Directory.GetFiles(directory));
        }
        finally { File.Delete(target); Directory.Delete(directory); }
    }

}
