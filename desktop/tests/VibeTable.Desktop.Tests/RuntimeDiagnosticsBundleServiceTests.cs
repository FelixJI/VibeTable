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
}
