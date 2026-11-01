using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class RuntimeDiagnosticsBundleServiceTests
{
    [TestMethod]
    public void Build_DropsUnstructuredContentAndProjectsClosedSummary()
    {
        const string safe = """
            {"timestamp":"2026-08-12T00:00:00Z","level":"error","module":"sidecar","event":"search.failed","errorCode":"workspace_search.storage_failed","requestId":null,"operationId":null,"workspaceId":null,"sessionEpoch":null,"jobId":null,"durationMs":12}
            """;
        using JsonDocument summary = JsonDocument.Parse("""
            {"contractVersion":"1.0","jobs":{"queued":1,"running":2,"succeeded":3,"failed":4,"cancelled":5},"index":{"state":"ready","generation":7,"processed":8,"total":9,"checkpoint":null,"errorCode":null},"recovery":{"pendingMutationRevision":10}}
            """);
        RuntimeDiagnosticsBundle bundle = RuntimeDiagnosticsBundleService.Build(
            new RuntimeDiagnosticsInput(
                "Windows", "1.0", "10.0", "0.31", 42,
                "ready", "Ready", "Ready",
                safe.Replace("sidecar", "desktop"),
                safe + "\n正文 password C:\\private\\workspace.db",
                "plugin output customer@example.com",
                summary.RootElement.Clone()));

        Assert.AreEqual(2, bundle.Logs.Count);
        Assert.AreEqual("search.failed", bundle.Logs[0].Event);
        Assert.AreEqual(1, bundle.RecentErrorCounts.Count);
        Assert.AreEqual(2, bundle.RecentErrorCounts[0].Count);
        Assert.AreEqual(2, bundle.Jobs.Running);
        Assert.AreEqual(7, bundle.Index.Generation);
        Assert.AreEqual(10, bundle.PendingMutationRevision);
        string serialized = JsonSerializer.Serialize(bundle);
        Assert.IsFalse(serialized.Contains("password", StringComparison.Ordinal));
        Assert.IsFalse(serialized.Contains("private", StringComparison.Ordinal));
        Assert.IsFalse(serialized.Contains("customer@", StringComparison.Ordinal));
    }

    [TestMethod]
    public void Build_PreservesValidatedAuditDrainDetailAndDropsInvalidLines()
    {
        const string valid = """
            {"timestamp":"2026-08-12T00:00:01Z","level":"info","module":"snapshot","event":"snapshot.freeze.audit.drain.returned","errorCode":null,"requestId":null,"operationId":null,"workspaceId":null,"sessionEpoch":null,"jobId":null,"durationMs":12.5,"auditDrain":{"source":"business","phase":"completed","appended":3,"duplicates":1,"acknowledged":2,"pendingCalls":4,"appendCalls":5,"ackCalls":6,"pbReadWaitCount":7,"pbWriteWaitCount":8,"pbReadInUseDelta":-2,"pbWriteInUseDelta":1,"pendingMs":0.25,"appendMs":1,"appendMaxMs":2.5,"ackMs":0,"pbReadWaitMs":0.5,"pbWriteWaitMs":1.5}}
            """;
        string invalidPhase = valid.Replace(
            "\"phase\":\"completed\"",
            "\"phase\":\"secret-phase\"",
            StringComparison.Ordinal);
        string unknownDetailKey = valid.Replace(
            "\"appended\":3",
            "\"appended\":3,\"query\":\"secret\"",
            StringComparison.Ordinal);
        string unknownTopKey = valid.Replace(
            "\"durationMs\":12.5",
            "\"durationMs\":12.5,\"query\":\"secret\"",
            StringComparison.Ordinal);
        RuntimeDiagnosticsBundle bundle = RuntimeDiagnosticsBundleService.Build(
            new RuntimeDiagnosticsInput(
                "Windows", "1.0", "10.0", "0.31", 42,
                "ready", "Ready", "Ready",
                string.Empty,
                valid + "\n" + invalidPhase + "\n" + unknownDetailKey
                    + "\n" + unknownTopKey,
                "not json customer@example.com",
                null));

        Assert.AreEqual(1, bundle.Logs.Count);
        JsonElement auditDrain = bundle.Logs[0].AuditDrain!.Value;
        Assert.AreEqual("business", auditDrain.GetProperty("source").GetString());
        Assert.AreEqual("completed", auditDrain.GetProperty("phase").GetString());
        Assert.AreEqual(3, auditDrain.GetProperty("appended").GetInt64());
        Assert.AreEqual(-2, auditDrain.GetProperty("pbReadInUseDelta").GetInt64());
        string serialized = JsonSerializer.Serialize(bundle);
        Assert.IsTrue(serialized.Contains("\"auditDrain\"", StringComparison.Ordinal));
        Assert.IsFalse(serialized.Contains("secret", StringComparison.Ordinal));
        Assert.IsFalse(serialized.Contains("customer@", StringComparison.Ordinal));
    }

    [TestMethod]
    public void Build_OmitsAuditDrainForLegacyClosedSchemaLines()
    {
        const string legacy = """
            {"timestamp":"2026-08-12T00:00:01Z","level":"info","module":"snapshot","event":"snapshot.freeze.audit.drain.returned","errorCode":null,"requestId":null,"operationId":null,"workspaceId":null,"sessionEpoch":null,"jobId":null,"durationMs":12.5}
            """;
        RuntimeDiagnosticsBundle bundle = RuntimeDiagnosticsBundleService.Build(
            new RuntimeDiagnosticsInput(
                "Windows", "1.0", "10.0", "0.31", 42,
                "ready", "Ready", "Ready",
                legacy,
                string.Empty,
                string.Empty,
                null));

        Assert.AreEqual(1, bundle.Logs.Count);
        Assert.IsNull(bundle.Logs[0].AuditDrain);
        string serialized = JsonSerializer.Serialize(bundle);
        Assert.IsFalse(serialized.Contains("auditDrain", StringComparison.Ordinal));
    }

    [TestMethod]
    public void Build_WorkspacePartFailureStillShipsLocalFactsWithExplicitFailure()
    {
        RuntimeDiagnosticsBundle bundle = RuntimeDiagnosticsBundleService.Build(
            new RuntimeDiagnosticsInput(
                "Windows", "1.0", "10.0", "0.31", 42,
                "ready", "Ready", "Ready",
                string.Empty,
                string.Empty,
                string.Empty,
                null,
                "workspace.session_inactive"));

        Assert.AreEqual("failed", bundle.WorkspaceDiagnostics.State);
        Assert.AreEqual("workspace.session_inactive", bundle.WorkspaceDiagnostics.ErrorCode);
        // The local runtime part stays usable instead of failing the bundle.
        Assert.AreEqual("Windows", bundle.OperatingSystem);
        Assert.AreEqual("unavailable", bundle.Index.State);
        Assert.AreEqual(0, bundle.Jobs.Running);
        Assert.AreEqual(0, bundle.PendingMutationRevision);
    }

    [TestMethod]
    public void Build_WorkspacePartReportsAvailabilityWithoutLeakingFreeFormErrors()
    {
        RuntimeDiagnosticsInput localOnly = new(
            "Windows", "1.0", "10.0", "0.31", 42,
            "ready", "Ready", "Ready",
            string.Empty, string.Empty, string.Empty, null);
        Assert.AreEqual(
            "unavailable",
            RuntimeDiagnosticsBundleService.Build(localOnly).WorkspaceDiagnostics.State);

        RuntimeDiagnosticsInput longCode = localOnly with
        {
            WorkspaceDiagnosticsError = new string('x', 200),
        };
        RuntimeDiagnosticsBundle bounded = RuntimeDiagnosticsBundleService.Build(longCode);
        Assert.AreEqual("failed", bounded.WorkspaceDiagnostics.State);
        Assert.AreEqual(160, bounded.WorkspaceDiagnostics.ErrorCode!.Length);

        using JsonDocument summary = JsonDocument.Parse(
            """{"jobs":{"queued":0,"running":0,"succeeded":0,"failed":0,"cancelled":0}}""");
        RuntimeDiagnosticsInput available = localOnly with
        {
            WorkspaceSummary = summary.RootElement.Clone(),
        };
        Assert.AreEqual(
            "available",
            RuntimeDiagnosticsBundleService.Build(available).WorkspaceDiagnostics.State);
    }

    [TestMethod]
    public void Build_KeepsErrorsAheadOfNewerRoutineLines()
    {
        string Line(int index, string level) =>
            "{\"timestamp\":\"2026-08-12T00:00:"
            + index.ToString("00")
            + "Z\",\"level\":\""
            + level
            + "\",\"module\":\"desktop\",\"event\":\"event."
            + index
            + "\",\"errorCode\":null,\"requestId\":null,\"operationId\":null,\"workspaceId\":null,\"sessionEpoch\":null,\"jobId\":null,\"durationMs\":null}";
        // 120 routine lines arrive after the only error so a pure newest-first
        // window would push the failure evidence out of the bundle.
        var lines = new List<string>();
        for (int index = 0; index < 120; index++)
        {
            lines.Add(Line(index, "info"));
        }
        lines.Add(Line(200, "error"));

        RuntimeDiagnosticsBundle bundle = RuntimeDiagnosticsBundleService.Build(
            new RuntimeDiagnosticsInput(
                "Windows", "1.0", "10.0", "0.31", 42,
                "ready", "Ready", "Ready",
                string.Join("\n", lines),
                string.Empty,
                string.Empty,
                null,
                DataRoot: @"C:\DataRoot"));

        Assert.AreEqual(RuntimeDiagnosticsBundleService.MaximumLogEntries, bundle.Logs.Count);
        Assert.AreEqual(
            1,
            bundle.Logs.Count(entry => entry.Level == "error"),
            "The lone error must survive the bounded window.");
        Assert.AreEqual("event.200", bundle.Logs.First(entry => entry.Level == "error").Event);
        Assert.AreEqual(@"C:\DataRoot", bundle.DataDirectory);
        Assert.AreEqual(@"C:\DataRoot\logs", bundle.LogsDirectory);
    }
}
