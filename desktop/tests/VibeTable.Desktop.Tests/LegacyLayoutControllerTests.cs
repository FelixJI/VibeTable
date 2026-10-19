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
        ConfigureLegacyPages(fixture, Json(LegacySnapshot));
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
            Assert.IsTrue(fixture.Http.Calls.All(call => call.GetProperty("method").GetString() == "legacyLayout.snapshot" && call.GetProperty("params").GetProperty("collection").GetString() is "interfaces" or "dashboards" or "panels"));
        }
        finally { File.Delete(target); Directory.Delete(directory); }
    }
    [TestMethod]
    [DataRow(true)]
    [DataRow(false)]
    public async Task LegacyPreservationCancellationAndCommitHaveOneTerminalOutcome(bool cancelBeforeCommit)
    {
        await using var fixture = await HostFixture.OpenAsync();
        ConfigureLegacyPages(fixture, Json(LegacySnapshot));
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

    private static void ConfigureLegacyPages(HostFixture fixture, JsonElement snapshot)
    {
        fixture.Http.BeforeReply = (call, _) =>
        {
            JsonElement parameters = call.GetProperty("params");
            string collection = parameters.GetProperty("collection").GetString()!;
            string after = parameters.GetProperty("afterRecordId").GetString()!;
            int offset = parameters.GetProperty("recordOffset").GetInt32();
            int index = after.Length == 0 ? 0 : int.Parse(after, System.Globalization.CultureInfo.InvariantCulture);
            JsonElement items = snapshot.GetProperty(collection);
            bool end = index >= items.GetArrayLength();
            JsonElement item = end ? default : items[index];
            byte[] bytes = end ? [] : System.Text.Encoding.UTF8.GetBytes(item.GetRawText());
            int count = end ? 0 : Math.Min(LegacyLayoutRequestController.PageBytes, bytes.Length - offset);
            bool done = !end && offset + count == bytes.Length;
            string id = end ? "" : (index + 1).ToString("D15", System.Globalization.CultureInfo.InvariantCulture);
            JsonElement diagnostics = !end && item.TryGetProperty("diagnostics", out var entries) ? entries : Json("[]");
            fixture.Http.Result = JsonSerializer.SerializeToElement(new {
                contractVersion = "2.0", collection, recordId = id,
                logicalId = end ? "" : item.GetProperty("logicalId").GetString(),
                revision = "fixture-revision", chunk = end ? [] : bytes.AsSpan(offset, count).ToArray(),
                recordDone = done, collectionDone = end,
                nextAfterRecordId = done ? id : after, nextRecordOffset = done || end ? 0 : offset + count,
                diagnosticCount = offset == 0 ? diagnostics.GetArrayLength() : 0, previewTruncated = false,
                diagnostics = offset == 0 ? diagnostics.EnumerateArray().Take(100).ToArray() : [],
                collectedAt = snapshot.GetProperty("collectedAt").GetString(),
            });
            Assert.IsLessThan(4 * 1024 * 1024, System.Text.Encoding.UTF8.GetByteCount(fixture.Http.Result.GetRawText()));
            return Task.CompletedTask;
        };
    }

    [TestMethod]
    public async Task LegacyPreservationStreamsMoreThanSixteenMiBThroughBoundedHttpPages()
    {
        await using var fixture = await HostFixture.OpenAsync();
        var items = Enumerable.Range(0, 20).Select(i => new {
            logicalId = "large-" + i, rawPayload = "{broken " + new string('x', 900000) + "中文😀",
            diagnostics = Enumerable.Range(0, 8).Select(n => new { code = "legacy.field_unknown", severity = "warning", message = "preserved", path = "$.field" + n }).ToArray(),
        }).ToArray();
        var snapshot = JsonSerializer.SerializeToElement(new { contractVersion = "1.0", interfaces = items,
            dashboards = Array.Empty<object>(), panels = Array.Empty<object>(), collectedAt = "2026-10-19T00:00:00Z" });
        ConfigureLegacyPages(fixture, snapshot);
        using var gateway = fixture.Gateway(useGeneratedPolicy: true);
        var sink = new FakeWebReplySink();
        string directory = Path.Combine(Path.GetTempPath(), "legacy-large-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(directory);
        string target = Path.Combine(directory, "preserved.json");
        var broker = new HostSessionFileBroker(() => {
            Assert.IsTrue(fixture.Leases.TryCaptureHost(fixture.Session.WorkspaceId!.Value, fixture.Session.SessionEpoch, Guid.NewGuid(), out var lease));
            return lease!;
        }, apply => apply());
        var controller = new LegacyLayoutRequestController(sink, fixture.Leases, () => gateway, () => broker, () => target);
        RoutedWebRequest Request(string type, string id) => new(type, id, Json("{}"), "", new WorkspaceWireScope {
            Scope = "workspace", WorkspaceId = fixture.Session.WorkspaceId!.Value, SessionEpoch = fixture.Session.SessionEpoch,
            OperationId = Guid.NewGuid(), Sequence = fixture.Leases.ReserveHostSequence(fixture.Session.WorkspaceId!.Value, fixture.Session.SessionEpoch) + 100,
        });
        try
        {
            await controller.DispatchAsync(Request("legacyLayout.reportRequested", "large-report"));
            var report = JsonSerializer.SerializeToElement(sink.Replies.Last().Payload);
            Assert.AreEqual(20, report.GetProperty("interfaces").GetInt32());
            Assert.AreEqual(160, report.GetProperty("diagnosticCount").GetInt32());
            Assert.AreEqual(100, report.GetProperty("diagnostics").GetArrayLength());
            Assert.IsTrue(report.GetProperty("diagnosticsTruncated").GetBoolean());
            await controller.DispatchAsync(Request("legacyLayout.exportRequested", "large-export"));
            Assert.AreEqual("legacyLayout.exportCompleted", sink.Replies.Last().Type);
            Assert.IsGreaterThan(16L * 1024 * 1024, new FileInfo(target).Length);
            using var saved = JsonDocument.Parse(await File.ReadAllTextAsync(target));
            Assert.AreEqual(20, saved.RootElement.GetProperty("interfaces").GetArrayLength());
            for (int index = 0; index < items.Length; index++)
            {
                var item = saved.RootElement.GetProperty("interfaces")[index];
                Assert.AreEqual(items[index].rawPayload, item.GetProperty("rawPayload").GetString());
                Assert.AreEqual(8, item.GetProperty("diagnostics").GetArrayLength());
            }
            Assert.IsGreaterThan(100, fixture.Http.Calls.Count);
            Assert.HasCount(1, Directory.GetFiles(directory));
        }
        finally { File.Delete(target); Directory.Delete(directory); }
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task LegacyPreservationMidPageCancelOrEpochRetirementLeavesTargetUntouched(bool retire)
    {
        await using var fixture = await HostFixture.OpenAsync();
        var snapshot = JsonSerializer.SerializeToElement(new { interfaces = new[] { new { logicalId = "large", rawPayload = new string('x', 900000) } },
            dashboards = Array.Empty<object>(), panels = Array.Empty<object>(), collectedAt = "2026-10-19T00:00:00Z" });
        ConfigureLegacyPages(fixture, snapshot);
        var produce = fixture.Http.BeforeReply!;
        var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Http.BeforeReply = async (call, token) => {
            if (call.GetProperty("params").GetProperty("recordOffset").GetInt32() != 0)
            {
                entered.TrySetResult();
                await Task.Delay(Timeout.Infinite, token);
            }
            await produce(call, token);
        };
        using var gateway = fixture.Gateway(useGeneratedPolicy: true);
        var sink = new FakeWebReplySink();
        string directory = Path.Combine(Path.GetTempPath(), "legacy-drain-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(directory);
        string target = Path.Combine(directory, "preserved.json");
        var broker = new HostSessionFileBroker(() => {
            Assert.IsTrue(fixture.Leases.TryCaptureHost(fixture.Session.WorkspaceId!.Value, fixture.Session.SessionEpoch, Guid.NewGuid(), out var lease));
            return lease!;
        }, apply => apply());
        var controller = new LegacyLayoutRequestController(sink, fixture.Leases, () => gateway, () => broker, () => target);
        RoutedWebRequest Request(string type, string id, string payload) => new(type, id, Json(payload), "", new WorkspaceWireScope {
            Scope = "workspace", WorkspaceId = fixture.Session.WorkspaceId!.Value, SessionEpoch = fixture.Session.SessionEpoch,
            OperationId = Guid.NewGuid(), Sequence = fixture.Leases.ReserveHostSequence(fixture.Session.WorkspaceId!.Value, fixture.Session.SessionEpoch) + 100,
        });
        try
        {
            await File.WriteAllTextAsync(target, "original");
            Task pending = controller.DispatchAsync(Request("legacyLayout.exportRequested", "mid-page", "{}"));
            await entered.Task.WaitAsync(TimeSpan.FromSeconds(5));
            if (retire) await fixture.CloseAsync().WaitAsync(TimeSpan.FromSeconds(5));
            else await controller.DispatchAsync(Request("legacyLayout.cancelRequested", "cancel-page", """{"targetRequestId":"mid-page"}"""));
            await pending.WaitAsync(TimeSpan.FromSeconds(5));
            Assert.AreEqual("original", await File.ReadAllTextAsync(target));
            Assert.HasCount(1, Directory.GetFiles(directory));
            if (retire) Assert.HasCount(0, sink.Replies);
            else Assert.AreEqual("CANCELLED", JsonSerializer.SerializeToElement(sink.Replies.Single().Payload).GetProperty("code").GetString());
        }
        finally { File.Delete(target); Directory.Delete(directory); }
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task LegacyPreservationRejectsNonProgressingOrPrematureTerminalPage(bool terminal)
    {
        await using var fixture = await HostFixture.OpenAsync();
        var snapshot = JsonSerializer.SerializeToElement(new { interfaces = new[] { new { logicalId = "large", rawPayload = new string('x', 900000) } },
            dashboards = Array.Empty<object>(), panels = Array.Empty<object>(), collectedAt = "2026-10-19T00:00:00Z" });
        ConfigureLegacyPages(fixture, snapshot);
        var produce = fixture.Http.BeforeReply!;
        fixture.Http.BeforeReply = async (call, token) => {
            await produce(call, token);
            var page = JsonSerializer.Deserialize<Dictionary<string, JsonElement>>(fixture.Http.Result)!;
            page[terminal ? "collectionDone" : "nextRecordOffset"] = terminal
                ? JsonSerializer.SerializeToElement(true) : JsonSerializer.SerializeToElement(0);
            fixture.Http.Result = JsonSerializer.SerializeToElement(page);
        };
        using var gateway = fixture.Gateway(useGeneratedPolicy: true);
        var sink = new FakeWebReplySink();
        string directory = Path.Combine(Path.GetTempPath(), "legacy-invalid-page-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(directory);
        string target = Path.Combine(directory, "preserved.json");
        var broker = new HostSessionFileBroker(() => {
            Assert.IsTrue(fixture.Leases.TryCaptureHost(fixture.Session.WorkspaceId!.Value, fixture.Session.SessionEpoch, Guid.NewGuid(), out var lease));
            return lease!;
        }, apply => apply());
        var controller = new LegacyLayoutRequestController(sink, fixture.Leases, () => gateway, () => broker, () => target);
        try
        {
            await File.WriteAllTextAsync(target, "original");
            await controller.DispatchAsync(new("legacyLayout.exportRequested", "invalid-page", Json("{}"), "", new WorkspaceWireScope {
                Scope = "workspace", WorkspaceId = fixture.Session.WorkspaceId!.Value, SessionEpoch = fixture.Session.SessionEpoch,
                OperationId = Guid.NewGuid(), Sequence = fixture.Leases.ReserveHostSequence(fixture.Session.WorkspaceId!.Value, fixture.Session.SessionEpoch) + 100,
            }));
            Assert.AreEqual("original", await File.ReadAllTextAsync(target));
            Assert.HasCount(1, fixture.Http.Calls);
            Assert.HasCount(1, Directory.GetFiles(directory));
            Assert.AreEqual("LEGACY_PRESERVATION_FAILED", JsonSerializer.SerializeToElement(sink.Replies.Single().Payload).GetProperty("code").GetString());
        }
        finally { File.Delete(target); Directory.Delete(directory); }
    }

}
