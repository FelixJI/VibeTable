using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Infrastructure.Tests.Workspace;

[TestClass]
public sealed class WorkspaceStorageManagerTests
{
    [TestMethod]
    public void MoveUsesPreviewVerifyAndLeavesSourceUntouched()
    {
        using var fixture = new StorageFixture();
        var source = Path.Combine(fixture.Root, "source");
        var target = Path.Combine(fixture.Root, "target");
        var created = WorkspaceLayout.Create(
            source,
            "Move me",
            WorkspaceStorageMode.Direct,
            WorkspaceEncryptionMode.Convenient);
        File.WriteAllText(Path.Combine(source, "files", "note.txt"), "content");
        var manager = new WorkspaceStorageManager();

        var plan = manager.PreviewMove(source, target);
        manager.ApplyMove(plan);

        Assert.AreEqual(created.Manifest.WorkspaceId, WorkspaceLayout.ReadManifest(target).WorkspaceId);
        Assert.AreEqual("content", File.ReadAllText(Path.Combine(target, "files", "note.txt")));
        Assert.IsTrue(Directory.Exists(source), "ApplyMove must not destructively delete source.");
    }

    [TestMethod]
    public void ChangedSourceInvalidatesMovePlan()
    {
        using var fixture = new StorageFixture();
        var source = Path.Combine(fixture.Root, "source");
        var target = Path.Combine(fixture.Root, "target");
        WorkspaceLayout.Create(
            source,
            "Move me",
            WorkspaceStorageMode.Direct,
            WorkspaceEncryptionMode.None);
        var manager = new WorkspaceStorageManager();
        var plan = manager.PreviewMove(source, target);
        File.WriteAllText(Path.Combine(source, "files", "late.txt"), "late");

        var error = Assert.ThrowsExactly<WorkspaceRegistryException>(
            () => manager.ApplyMove(plan));

        Assert.AreEqual("workspace.storage_plan_stale", error.Code);
        Assert.IsFalse(Directory.Exists(target));
    }

    [TestMethod]
    public void ReleaseCacheFailsClosedUntilReplicaWasIndependentlyReopened()
    {
        using var fixture = new StorageFixture();
        var activity = Path.Combine(fixture.Root, "activity");
        WorkspaceLayout.Create(
            activity,
            "Activity",
            WorkspaceStorageMode.Direct,
            WorkspaceEncryptionMode.Protected);
        var manager = new WorkspaceStorageManager();

        var error = Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
            manager.PreviewReleaseActivityCache(
                activity,
                new ReleaseActivityCacheContext(true, true, false, false)));

        Assert.AreEqual("workspace.release_cache_unsafe", error.Code);
    }

    [TestMethod]
    public void MoveRejectsOverlappingTrees()
    {
        using var fixture = new StorageFixture();
        var source = Path.Combine(fixture.Root, "source");
        WorkspaceLayout.Create(
            source,
            "Move me",
            WorkspaceStorageMode.Direct,
            WorkspaceEncryptionMode.Convenient);
        var manager = new WorkspaceStorageManager();

        WorkspaceRegistryException error =
            Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                manager.PreviewMove(
                    source,
                    Path.Combine(source, "nested-target")));

        Assert.AreEqual(
            "workspace.storage_target_overlaps_source",
            error.Code);
    }

    [TestMethod]
    public void MoveExcludesOnlyDeviceLocalCoordinationLocks()
    {
        using var fixture = new StorageFixture();
        string source = Path.Combine(fixture.Root, "source");
        string target = Path.Combine(fixture.Root, "target");
        WorkspaceLayout.Create(
            source,
            "Active workspace",
            WorkspaceStorageMode.Direct,
            WorkspaceEncryptionMode.Convenient);
        string coordination = WorkspaceLayout.Paths(source).Coordination;
        string writerLock = Path.Combine(
            coordination,
            "desktop-writer.lock");
        using var heldWriter = new FileStream(
            writerLock,
            FileMode.OpenOrCreate,
            FileAccess.ReadWrite,
            FileShare.None);
        File.WriteAllText(
            Path.Combine(source, "files", "business.txt"),
            "preserved");
        var manager = new WorkspaceStorageManager();

        WorkspaceStoragePlan plan = manager.PreviewMove(source, target);
        manager.ApplyMove(plan);

        Assert.AreEqual(
            "preserved",
            File.ReadAllText(Path.Combine(target, "files", "business.txt")));
        Assert.IsFalse(
            File.Exists(Path.Combine(
                WorkspaceLayout.Paths(target).Coordination,
                "desktop-writer.lock")));
    }

    [TestMethod]
    public void MoveDoesNotExcludeLockedBusinessFiles()
    {
        using var fixture = new StorageFixture();
        string source = Path.Combine(fixture.Root, "source");
        string target = Path.Combine(fixture.Root, "target");
        WorkspaceLayout.Create(
            source,
            "Locked business file",
            WorkspaceStorageMode.Direct,
            WorkspaceEncryptionMode.Convenient);
        string business = Path.Combine(source, "files", "business.txt");
        File.WriteAllText(business, "must be fingerprinted");
        using var heldBusinessFile = new FileStream(
            business,
            FileMode.Open,
            FileAccess.ReadWrite,
            FileShare.None);
        var manager = new WorkspaceStorageManager();

        Assert.ThrowsExactly<IOException>(
            () => manager.PreviewMove(source, target));
    }

    private sealed class StorageFixture : IDisposable
    {
        public StorageFixture()
        {
            Root = Path.Combine(
                Path.GetTempPath(),
                "VibeTable-StorageManagerTests",
                Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(Root);
        }

        public string Root { get; }

        public void Dispose()
        {
            if (Directory.Exists(Root))
                Directory.Delete(Root, recursive: true);
        }
    }
}
