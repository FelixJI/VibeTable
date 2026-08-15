using VibeTable.Infrastructure.Diagnostics;

namespace VibeTable.Infrastructure.Tests.Diagnostics;

[TestClass]
public sealed class RotatingDiagnosticTraceListenerTests
{
    [TestMethod]
    public void Listener_PersistsOnlyClosedDiagnosticJson()
    {
        string root = Path.Combine(Path.GetTempPath(), "vibetable-trace-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        string path = Path.Combine(root, "desktop.log");
        try
        {
            using (var listener = new RotatingDiagnosticTraceListener(path))
            {
                listener.WriteLine("customer password C:\\private\\workspace.db");
                listener.WriteLine(DiagnosticEvent.Failure("desktop", "surface.failed", "surface.timeout"));
            }
            string persisted = File.ReadAllText(path);
            StringAssert.Contains(persisted, "surface.failed");
            Assert.IsFalse(persisted.Contains("password", StringComparison.Ordinal));
            Assert.IsTrue(DiagnosticLogLine.IsSafe(persisted.Trim()));
        }
        finally
        {
            if (Directory.Exists(root)) Directory.Delete(root, recursive: true);
        }
    }
}
