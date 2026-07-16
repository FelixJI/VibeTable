using System;
using System.IO;
using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class TestModeReadinessWriterTests
{
    [TestMethod]
    public void WriteShellReady_ReportsAllThreeStartupBoundaries()
    {
        string directory = Path.Combine(
            Path.GetTempPath(), "vibetable-readiness-" + Guid.NewGuid().ToString("N"));
        try
        {
            var writer = new TestModeReadinessWriter(directory);
            writer.WriteShellReady();

            using var document = JsonDocument.Parse(
                File.ReadAllText(writer.ReadinessPath));
            var root = document.RootElement;
            Assert.IsTrue(root.GetProperty("ready").GetBoolean());
            Assert.AreEqual("shell", root.GetProperty("mode").GetString());
            Assert.IsTrue(root.GetProperty("backendReady").GetBoolean());
            Assert.IsTrue(root.GetProperty("webViewReady").GetBoolean());
            Assert.IsTrue(root.GetProperty("rendererReady").GetBoolean());
            Assert.AreEqual(JsonValueKind.Null, root.GetProperty("error").ValueKind);
        }
        finally
        {
            if (Directory.Exists(directory))
            {
                Directory.Delete(directory, recursive: true);
            }
        }
    }
}
