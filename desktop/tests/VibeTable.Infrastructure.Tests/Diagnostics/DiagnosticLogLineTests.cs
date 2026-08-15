using VibeTable.Infrastructure.Diagnostics;

namespace VibeTable.Infrastructure.Tests.Diagnostics;

[TestClass]
public sealed class DiagnosticLogLineTests
{
    [TestMethod]
    public void IsSafe_AcceptsOnlyClosedSchema()
    {
        const string safe = """
            {"timestamp":"2026-08-12T00:00:00Z","level":"error","module":"sidecar","event":"search.failed","errorCode":null,"requestId":null,"operationId":null,"workspaceId":null,"sessionEpoch":null,"jobId":null,"durationMs":null}
            """;
        Assert.IsTrue(DiagnosticLogLine.IsSafe(safe));
        Assert.IsFalse(DiagnosticLogLine.IsSafe("customer password"));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(safe.Replace(
            "\"durationMs\":null",
            "\"durationMs\":null,\"query\":\"secret\"",
            StringComparison.Ordinal)));
    }
}
