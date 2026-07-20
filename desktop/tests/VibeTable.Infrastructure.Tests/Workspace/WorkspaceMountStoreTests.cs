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

    private static string MakeWorkspaceRoot(string baseDir, string name)
    {
        var root = Path.Combine(baseDir, name);
        Directory.CreateDirectory(root);
        return root;
    }

    [TestMethod]
    public void Mount_Then_ResolveRoot_ReturnsPath()
    {
        var baseDir = MakeTempBase();
        try
        {
            var store = new WorkspaceMountStore(baseDir);
            var workspaceRoot = MakeWorkspaceRoot(baseDir, "ProjectA");
            store.Mount("ws-001", workspaceRoot, "Project A");

            var root = store.ResolveRoot("ws-001");
            Assert.AreEqual(Path.GetFullPath(workspaceRoot), root);
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
    public void ResolveRoot_WithPartition_DoesNotCrossAccountBoundary()
    {
        var baseDir = MakeTempBase();
        try
        {
            var store = new WorkspaceMountStore(baseDir);
            var root = MakeWorkspaceRoot(baseDir, "Scoped");
            store.Mount("ws-001", root, "Scoped", "server-a|account-a");

            Assert.AreEqual(root, store.ResolveRoot("ws-001", "server-a|account-a"));
            Assert.IsNull(store.ResolveRoot("ws-001", "server-a|account-b"));
            Assert.IsNull(store.ResolveRoot("ws-001", "server-b|account-a"));
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
            var oldRoot = MakeWorkspaceRoot(baseDir, "Old");
            var newRoot = MakeWorkspaceRoot(baseDir, "New");
            store.Mount("ws-001", oldRoot, "Old");
            store.Mount("ws-001", newRoot, "New");

            Assert.AreEqual(Path.GetFullPath(newRoot), store.ResolveRoot("ws-001"));
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
            var rootA = MakeWorkspaceRoot(baseDir, "A");
            var rootB = MakeWorkspaceRoot(baseDir, "B");
            store.Mount("ws-001", rootA, "A");
            store.Mount("ws-002", rootB, "B");

            store.Unmount("ws-001");
            Assert.IsNull(store.ResolveRoot("ws-001"));
            Assert.AreEqual(Path.GetFullPath(rootB), store.ResolveRoot("ws-002"));
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
            var workspaceRoot = MakeWorkspaceRoot(baseDir, "Persist");
            store1.Mount("ws-persist", workspaceRoot, "Persist");

            // Create a new store instance — it should read the persisted file.
            var store2 = new WorkspaceMountStore(baseDir);
            Assert.AreEqual(Path.GetFullPath(workspaceRoot), store2.ResolveRoot("ws-persist"));
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
            store.Mount("ws-1", MakeWorkspaceRoot(baseDir, "A"), "A");

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

    [TestMethod]
    public void Mount_PersistsPartitionKey_AndReadsLegacyEntry()
    {
        var baseDir = MakeTempBase();
        try
        {
            var store = new WorkspaceMountStore(baseDir);
            var workspaceRoot = MakeWorkspaceRoot(baseDir, "Partitioned");
            store.Mount("ws-partitioned", workspaceRoot, "Partitioned", "remote:https://example.test|user@example.test");

            var entry = store.ReadAll().Single();
            Assert.AreEqual(
                "remote:https://example.test|user@example.test",
                entry.PartitionKey);

            string storePath = Path.Combine(baseDir, "VibeTable", "workspace-mounts.json");
            File.WriteAllText(
                storePath,
                $$"""
                {"formatVersion":1,"mounts":[{"workspaceId":"legacy","localRoot":"{{workspaceRoot.Replace("\\", "\\\\")}}","displayName":"Legacy","mountedAt":"2026-07-20T00:00:00Z"}]}
                """);
            Assert.IsNull(store.ReadAll().Single().PartitionKey);
        }
        finally
        {
            CleanupDir(baseDir);
        }
    }

    [TestMethod]
    public void Mount_MissingRoot_IsRejected()
    {
        var baseDir = MakeTempBase();
        try
        {
            var store = new WorkspaceMountStore(baseDir);
            var missing = Path.Combine(baseDir, "missing");

            Assert.Throws<DirectoryNotFoundException>(
                () => store.Mount("ws-missing", missing, "Missing"));
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
