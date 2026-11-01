using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class DataRootSessionLockTests
{
    private string _root = null!;

    [TestInitialize]
    public void Initialize()
    {
        _root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-session-lock-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(_root);
    }

    [TestCleanup]
    public void Cleanup()
    {
        Directory.Delete(_root, recursive: true);
    }

    [TestMethod]
    public void AcquireIsExclusivePerDataRoot()
    {
        using DataRootSessionLock first = DataRootSessionLock.Acquire(_root)!;

        Assert.IsNotNull(first);
        Assert.IsNull(DataRootSessionLock.Acquire(_root),
            "A second holder on the same data root must be refused.");
        Assert.IsTrue(DataRootSessionLock.IsHeldByAnotherProcess(_root));
    }

    [TestMethod]
    public void ReleaseMakesTheRootAvailableAgain()
    {
        DataRootSessionLock? first = DataRootSessionLock.Acquire(_root);
        Assert.IsNotNull(first);
        first.Dispose();

        Assert.IsFalse(DataRootSessionLock.IsHeldByAnotherProcess(_root));
        using DataRootSessionLock second = DataRootSessionLock.Acquire(_root)!;
        Assert.IsNotNull(second);
    }

    [TestMethod]
    public void RootWithoutHolderIsNotLockedAndLeavesNoFile()
    {
        Assert.IsFalse(DataRootSessionLock.IsHeldByAnotherProcess(_root));
        Assert.IsFalse(File.Exists(Path.Combine(_root, ".session.lock")),
            "The probe must clean up after itself when nobody holds the lock.");
    }

    [TestMethod]
    public void DistinctRootsDoNotContend()
    {
        string other = Path.Combine(_root, "other-root");
        Directory.CreateDirectory(other);
        using DataRootSessionLock first = DataRootSessionLock.Acquire(_root)!;

        DataRootSessionLock? acquired = DataRootSessionLock.Acquire(other);
        Assert.IsNotNull(acquired);
        using DataRootSessionLock second = acquired;
    }

    [TestMethod]
    public void MigrationRefusesWhileTheOutgoingRootIsHeldByAnotherInstance()
    {
        string baseDirectory = Path.Combine(_root, "install");
        string localAppData = Path.Combine(_root, "local");
        Directory.CreateDirectory(baseDirectory);
        Directory.CreateDirectory(localAppData);
        // Standard mode resolves: current = localAppData/VibeTable,
        // outgoing = baseDirectory/data.
        string outgoing = Path.Combine(baseDirectory, "data");
        Directory.CreateDirectory(outgoing);
        string current = Path.Combine(localAppData, "VibeTable");
        DataRootMigrationService.Arm(outgoing, current, false);
        DataRootSessionLock? held = DataRootSessionLock.Acquire(outgoing);
        Assert.IsNotNull(held);
        using DataRootSessionLock oldInstance = held;

        DataRootMigrationService.DataRootMigrationException failure =
            Assert.ThrowsExactly<
                DataRootMigrationService.DataRootMigrationException>(
                () => DataRootMigrationService.ApplyForLaunch(
                    new HostStartupOptions(),
                    baseDirectory,
                    localAppData));

        Assert.AreEqual("data.migration_source_in_use", failure.ErrorCode);
        Assert.IsTrue(File.Exists(Path.Combine(
                outgoing,
                "mode-switch-pending.json")),
            "The pending marker survives for the retry after the old instance exits.");
        Assert.IsTrue(Directory.Exists(outgoing),
            "Nothing in the outgoing root was moved or deleted.");
    }
}
