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

    [TestMethod]
    public void WriteUpdateReady_ReportsWorkspaceHealthEvidence()
    {
        string directory = Path.Combine(
            Path.GetTempPath(), "vibetable-readiness-" + Guid.NewGuid().ToString("N"));
        try
        {
            Guid workspaceId = Guid.NewGuid();
            var writer = new TestModeReadinessWriter(directory);
            writer.WriteUpdateReady(new UpdateWorkspaceHealthProbeReceipt(
                UpdateWorkspaceHealthProbeStatus.Healthy,
                workspaceId,
                41,
                3));

            using var document = JsonDocument.Parse(
                File.ReadAllText(writer.ReadinessPath));
            JsonElement root = document.RootElement;
            Assert.IsTrue(root.GetProperty("ready").GetBoolean());
            Assert.AreEqual("shell", root.GetProperty("mode").GetString());
            JsonElement probe = root.GetProperty("workspaceProbe");
            Assert.AreEqual("healthy", probe.GetProperty("status").GetString());
            Assert.AreEqual(
                workspaceId.ToString("D"),
                probe.GetProperty("workspaceId").GetString());
            Assert.AreEqual(41UL, probe.GetProperty("sessionEpoch").GetUInt64());
            Assert.AreEqual(3, probe.GetProperty("tableCount").GetInt32());
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
