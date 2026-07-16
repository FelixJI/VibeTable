using System.IO;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Infrastructure.Tests.Workspace;

/// <summary>
/// Tests for the G2.4 per-machine workspace mount registry.
/// </summary>
[TestClass]
public sealed class WorkspaceMountStoreTests
{
    private static string MakeTempBase()
    {
        var dir = Path.Combine(Path.GetTempPath(), "vibetable-mount-" + Guid.NewGuid().ToString("N")[..8]);
        Directory.CreateDirectory(dir);
        return dir;
    }

    [TestMethod]
    public void Mount_Then_ResolveRoot_ReturnsPath()
    {
        var baseDir = MakeTempBase();
        try
        {
            var store = new WorkspaceMountStore(baseDir);
            store.Mount("ws-001", @"D:\Workspaces\ProjectA", "Project A");

            var root = store.ResolveRoot("ws-001");
            Assert.AreEqual(@"D:\Workspaces\ProjectA", root);
        }
        finally
        {
            CleanupDir(baseDir);
        }
    }

    [TestMethod]
    public void ResolveRoot_Unknown_ReturnsNull()
    {
        var baseDir = MakeTempBase();
        try
        {
            var store = new WorkspaceMountStore(baseDir);
            Assert.IsNull(store.ResolveRoot("nonexistent"));
        }
        finally
        {
            CleanupDir(baseDir);
        }
    }

    [TestMethod]
    public void Mount_Overwrite_UpdatesPath()
    {
        var baseDir = MakeTempBase();
        try
        {
            var store = new WorkspaceMountStore(baseDir);
            store.Mount("ws-001", @"D:\Old", "Old");
            store.Mount("ws-001", @"E:\New", "New");

            Assert.AreEqual(@"E:\New", store.ResolveRoot("ws-001"));
        }
        finally
        {
            CleanupDir(baseDir);
        }
    }

    [TestMethod]
    public void Unmount_RemovesEntry()
    {
        var baseDir = MakeTempBase();
        try
        {
            var store = new WorkspaceMountStore(baseDir);
            store.Mount("ws-001", @"D:\A", "A");
            store.Mount("ws-002", @"D:\B", "B");

            store.Unmount("ws-001");
            Assert.IsNull(store.ResolveRoot("ws-001"));
            Assert.AreEqual(@"D:\B", store.ResolveRoot("ws-002"));
        }
        finally
        {
            CleanupDir(baseDir);
        }
    }

    [TestMethod]
    public void Store_Persists_AcrossInstances()
    {
        var baseDir = MakeTempBase();
        try
        {
            var store1 = new WorkspaceMountStore(baseDir);
            store1.Mount("ws-persist", @"D:\Persist", "Persist");

            // Create a new store instance — it should read the persisted file.
            var store2 = new WorkspaceMountStore(baseDir);
            Assert.AreEqual(@"D:\Persist", store2.ResolveRoot("ws-persist"));
        }
        finally
        {
            CleanupDir(baseDir);
        }
    }

    [TestMethod]
    public void Store_File_HasCorrectFormat()
    {
        var baseDir = MakeTempBase();
        try
        {
            var store = new WorkspaceMountStore(baseDir);
            store.Mount("ws-1", @"D:\A", "A");

            var path = Path.Combine(baseDir, "VibeTable", "workspace-mounts.json");
            Assert.IsTrue(File.Exists(path));

            var json = File.ReadAllText(path);
            Assert.IsTrue(json.Contains("\"formatVersion\""));
            Assert.IsTrue(json.Contains("\"workspaceId\""));
        }
        finally
        {
            CleanupDir(baseDir);
        }
    }

    private static void CleanupDir(string dir)
    {
        try { if (Directory.Exists(dir)) Directory.Delete(dir, recursive: true); }
        catch { /* best effort */ }
    }
}
