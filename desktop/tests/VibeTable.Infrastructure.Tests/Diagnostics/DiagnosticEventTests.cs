using System.Text.Json;
using VibeTable.Infrastructure.Diagnostics;

namespace VibeTable.Infrastructure.Tests.Diagnostics;

[TestClass]
public sealed class DiagnosticEventTests
{
    [TestMethod]
    [DataRow("secret-token-value")]
    [DataRow("customer-salary-9000")]
    [DataRow(@"C:\Users\customer\机密-report.docx")]
    [DataRow("token=eyJhbGciOi.issuer/laptop?redirect=https://evil.example")]
    [DataRow("update customers set salary=9000 where name='张三'")]
    [DataRow("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")]
    public void Failure_DropsRendererControlledRequestIds(string requestId)
    {
        string line = DiagnosticEvent.Failure(
            "VibeTable.Desktop.ProductDataRequestController",
            "productData.query.failed",
            "PRODUCT_DATA_TIMEOUT",
            requestId);

        Assert.IsTrue(DiagnosticLogLine.IsSafe(line), line);
        using JsonDocument document = JsonDocument.Parse(line);
        Assert.AreEqual(
            JsonValueKind.Null,
            document.RootElement.GetProperty("requestId").ValueKind,
            line);
        Assert.IsFalse(
            line.Contains(
                requestId[..Math.Min(16, requestId.Length)],
                StringComparison.Ordinal),
            line);
    }

    [TestMethod]
    [DataRow("rlzhp8xk-7-3f2a8d1e-0c4b-4f8a-9d2e-1a2b3c4d5e6f")]
    [DataRow("rlzhp8xk-42-khdmfi42")]
    [DataRow("rlzhp8xk-42-7")]
    [DataRow("rlzhp8xk-42-")]
    [DataRow("84f66100-ff7c-4fb4-b0c0-02cd7fb668fe")]
    [DataRow("84f66100ff7c4fb4b0c002cd7fb668fe")]
    [DataRow("e2e-84f66100-ff7c-4fb4-b0c0-02cd7fb668fe")]
    public void Failure_PreservesOpaqueProducerRequestIds(string requestId)
    {
        string line = DiagnosticEvent.Failure(
            "VibeTable.Desktop.ProductDataRequestController",
            "productData.query.failed",
            "PRODUCT_DATA_TIMEOUT",
            requestId);

        Assert.IsTrue(DiagnosticLogLine.IsSafe(line), line);
        using JsonDocument document = JsonDocument.Parse(line);
        Assert.AreEqual(
            requestId,
            document.RootElement.GetProperty("requestId").GetString(),
            line);
    }

    [TestMethod]
    public void Failure_SensitiveRequestIdNeverReachesThePersistedLogFile()
    {
        const string Sensitive = @"C:\Users\customer\机密-report.docx";
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
                    "productData.query.failed",
                    "PRODUCT_DATA_TIMEOUT",
                    Sensitive));
            }

            string[] lines = File.ReadAllLines(path);
            Assert.HasCount(1, lines);
            Assert.IsTrue(DiagnosticLogLine.IsSafe(lines[0]), lines[0]);
            using JsonDocument document = JsonDocument.Parse(lines[0]);
            Assert.AreEqual(
                JsonValueKind.Null,
                document.RootElement.GetProperty("requestId").ValueKind);
            Assert.IsFalse(
                lines[0].Contains(@"C:\Users\customer", StringComparison.Ordinal),
                lines[0]);
            Assert.IsFalse(
                lines[0].Contains("机密-report", StringComparison.Ordinal),
                lines[0]);
        }
        finally
        {
            try { Directory.Delete(root, recursive: true); }
            catch { }
        }
    }
}
