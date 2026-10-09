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
            Assert.IsTrue(root.GetProperty("hostReady").GetBoolean());
            Assert.IsFalse(root.TryGetProperty("backendReady", out _));
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
            Assert.IsTrue(root.GetProperty("hostReady").GetBoolean());
            Assert.IsFalse(root.TryGetProperty("backendReady", out _));
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

    [TestMethod]
    public void DesktopLogDirectoryRedirectsToReadinessDirOnlyForTestModeLaunch()
    {
        string readiness = Path.Combine(
            Path.GetTempPath(), "readiness-" + Guid.NewGuid().ToString("N"));
        string userDirectory = Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
            "VibeTable",
            "logs");

        Assert.AreEqual(
            Path.Combine(Path.GetFullPath(readiness), "desktop-logs"),
            App.DesktopLogDirectory(
                new HostStartupOptions { TestMode = true, ReadinessDir = readiness }),
            "Test mode keeps desktop.log in a dedicated readiness subdirectory "
                + "so log rotation can never prune readiness-root diagnostics.");
        Assert.AreEqual(
            userDirectory,
            App.DesktopLogDirectory(
                new HostStartupOptions { TestMode = true }),
            "Test mode without a readiness directory keeps the shared user directory.");
        Assert.AreEqual(
            userDirectory,
            App.DesktopLogDirectory(
                new HostStartupOptions { ReadinessDir = readiness }),
            "A non-test launch with a readiness argument keeps the user directory.");
        Assert.AreEqual(
            userDirectory,
            App.DesktopLogDirectory(new HostStartupOptions()),
            "A plain launch keeps the shared user directory.");
    }
}
