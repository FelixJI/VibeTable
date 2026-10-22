using System.Text.Json;
using VibeTable.Infrastructure.Diagnostics;

namespace VibeTable.Infrastructure.Tests.Diagnostics;

[TestClass]
public sealed class DiagnosticLogLineTests
{
    private const string BaseLine = """
        {"timestamp":"2026-08-12T00:00:00Z","level":"info","module":"snapshot","event":"snapshot.freeze.audit.drain.returned","errorCode":null,"requestId":null,"operationId":null,"workspaceId":null,"sessionEpoch":null,"jobId":null,"durationMs":12.5}
        """;

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

    [TestMethod]
    public void IsSafe_AcceptsValidatedAuditDrainDetail()
    {
        Assert.IsTrue(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":"business","phase":"ack"}""")));
        Assert.IsTrue(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":"file","phase":"between_records","appended":2,"pbWriteInUseDelta":-3,"pendingMs":0}""")));
        Assert.IsTrue(DiagnosticLogLine.IsSafe(WithAuditDrain("""
            {"source":"business","phase":"completed","appended":3,"duplicates":1,"acknowledged":2,"pendingCalls":4,"appendCalls":5,"ackCalls":6,"pbReadWaitCount":7,"pbWriteWaitCount":8,"pbReadInUseDelta":-2,"pbWriteInUseDelta":1,"pendingMs":0.25,"appendMs":1,"appendMaxMs":2.5,"ackMs":0,"pbReadWaitMs":0.5,"pbWriteWaitMs":1.5}
            """)));
    }

    [TestMethod]
    public void IsSafe_RejectsInvalidAuditDrainDetail()
    {
        string valid = WithAuditDrain(
            """{"source":"business","phase":"ack","appended":3}""");
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain("null")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain("\"business\"")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain("12")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":"business"}""")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"phase":"ack"}""")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":"secret-source","phase":"ack"}""")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":"file","phase":"secret-phase"}""")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":"file","phase":"ack","query":"secret"}""")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":"business","source":"file","phase":"ack"}""")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":"business","phase":"ack","phase":"ack"}""")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":7,"phase":"ack"}""")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":"business","phase":"ack","appended":"3"}""")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":"business","phase":"ack","appended":1.5}""")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":"business","phase":"ack","appended":1.0}""")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":"business","phase":"ack","appended":-1}""")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":"business","phase":"ack","appended":99999999999999999999}""")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":"business","phase":"ack","pbReadInUseDelta":0.5}""")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":"business","phase":"ack","pendingMs":"0.5"}""")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":"business","phase":"ack","pendingMs":-0.5}""")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(WithAuditDrain(
            """{"source":"business","phase":"ack","pendingMs":1e999}""")));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(valid.Replace(
            "\"durationMs\":12.5",
            "\"durationMs\":12.5,\"event\":\"duplicated\"",
            StringComparison.Ordinal)));
    }

    [TestMethod]
    public void IsSafe_JsonElementOverload_ValidatesAlreadyParsedLine()
    {
        using JsonDocument valid = JsonDocument.Parse(WithAuditDrain(
            """{"source":"file","phase":"pending"}"""));
        using JsonDocument invalid = JsonDocument.Parse("[]");
        using JsonDocument invalidDetail = JsonDocument.Parse(WithAuditDrain(
            """{"source":"file","phase":"ack","extra":1}"""));

        Assert.IsTrue(DiagnosticLogLine.IsSafe(valid.RootElement.Clone()));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(invalid.RootElement.Clone()));
        Assert.IsFalse(DiagnosticLogLine.IsSafe(invalidDetail.RootElement.Clone()));
    }

    private static string WithAuditDrain(string detail) =>
        BaseLine[..^1] + ",\"auditDrain\":" + detail + "}";
}
