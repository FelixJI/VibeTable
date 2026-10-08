using System.Text.Json;
using VibeTable.Infrastructure.Diagnostics;

namespace VibeTable.Infrastructure.Tests.Diagnostics;

[TestClass]
public sealed class DiagnosticEventTests
{
    [TestMethod]
    public void Failure_EmitsTheClosedSchemaWithoutAnyTrustedRequestId()
    {
        string line = DiagnosticEvent.Failure(
            "VibeTable.Desktop.ProductDataRequestController",
            "productData.query.dispatch",
            "PRODUCT_RPC_FAILED:TimeoutException",
            durationMs: 12.5);

        Assert.IsTrue(DiagnosticLogLine.IsSafe(line), line);
        using JsonDocument document = JsonDocument.Parse(line);
        JsonElement root = document.RootElement;
        Assert.AreEqual(11, root.EnumerateObject().Count());
        Assert.AreEqual("error", root.GetProperty("level").GetString());
        Assert.AreEqual(
            "productData.query.dispatch",
            root.GetProperty("event").GetString());
        Assert.AreEqual(
            "PRODUCT_RPC_FAILED:TimeoutException",
            root.GetProperty("errorCode").GetString());
        // No renderer-controlled identifier is ever persisted.
        Assert.AreEqual(JsonValueKind.Null, root.GetProperty("requestId").ValueKind);
        foreach (string closedField in new[]
                 {
                     "operationId", "workspaceId", "sessionEpoch", "jobId",
                 })
            Assert.AreEqual(
                JsonValueKind.Null,
                root.GetProperty(closedField).ValueKind,
                closedField);
        Assert.AreEqual(12.5, root.GetProperty("durationMs").GetDouble());
    }

    [TestMethod]
    public void Failure_NullDurationStaysNullInTheClosedSchema()
    {
        string line = DiagnosticEvent.Failure("product", "data.request.failed", "X");

        Assert.IsTrue(DiagnosticLogLine.IsSafe(line), line);
        using JsonDocument document = JsonDocument.Parse(line);
        Assert.AreEqual(
            JsonValueKind.Null,
            document.RootElement.GetProperty("durationMs").ValueKind);
        Assert.AreEqual(
            JsonValueKind.Null,
            document.RootElement.GetProperty("requestId").ValueKind);
    }

    [TestMethod]
    public void Failure_PersistsExactlyOnceThroughTheRealListener()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-diagnostic-event-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        string path = Path.Combine(root, "desktop.log");
        try
        {
            using (var listener = new RotatingDiagnosticTraceListener(path))
            {
                listener.WriteLine(DiagnosticEvent.Failure(
                    "VibeTable.Desktop.ProductDataRequestController",
                    "productData.query.dispatch",
                    "PRODUCT_RPC_FAILED:TimeoutException",
                    durationMs: 3.25));
            }

            string[] lines = File.ReadAllLines(path);
            Assert.HasCount(1, lines);
            Assert.IsTrue(DiagnosticLogLine.IsSafe(lines[0]), lines[0]);
            using JsonDocument document = JsonDocument.Parse(lines[0]);
            Assert.AreEqual(
                JsonValueKind.Null,
                document.RootElement.GetProperty("requestId").ValueKind);
            Assert.IsFalse(
                lines[0].Contains("requestId\":\"", StringComparison.Ordinal),
                lines[0]);
        }
        finally
        {
            try { Directory.Delete(root, recursive: true); }
            catch { }
        }
    }
}
