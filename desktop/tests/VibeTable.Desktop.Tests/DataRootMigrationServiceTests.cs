using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class DataRootMigrationServiceTests
{
    private string _root = null!;

    [TestInitialize]
    public void Initialize()
    {
        _root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-migration-" + Guid.NewGuid().ToString("N"));
    }

    [TestCleanup]
    public void Cleanup()
    {
        if (Directory.Exists(_root))
        {
            Directory.Delete(_root, recursive: true);
        }
    }

    [TestMethod]
    public void ArmThenApplyMovesEntriesAndRemovesTheOutgoingRoot()
    {
        (string source, string target) = CreateRoots("source", "target");
        Directory.CreateDirectory(Path.Combine(source, "shell"));
        File.WriteAllText(
            Path.Combine(source, "shell", "app-preferences.json"),
            "{}");
        Directory.CreateDirectory(Path.Combine(source, "activity", "ws-1"));
        File.WriteAllText(
            Path.Combine(source, "activity", "ws-1", "workspace.db"),
            "db");

        DataRootMigrationService.Arm(source, target, true);
        Assert.IsTrue(File.Exists(Path.Combine(
            source,
            "mode-switch-pending.json")));

        var outcome = DataRootMigrationService.ApplyIfNeeded(target, source);

        Assert.AreEqual(
            DataRootMigrationService.MigrationOutcome.Migrated,
            outcome);
        Assert.IsTrue(File.Exists(Path.Combine(
            target,
            "shell",
            "app-preferences.json")));
        Assert.IsTrue(File.Exists(Path.Combine(
            target,
            "activity",
            "ws-1",
            "workspace.db")));
        Assert.IsFalse(Directory.Exists(source), "A completed migration drains the source root.");
    }

    [TestMethod]
    public void InterruptedMigrationRetriesAndSkipsSettledEntries()
    {
        (string source, string target) = CreateRoots("source", "target");
        File.WriteAllText(Path.Combine(source, "a.txt"), "a");
        File.WriteAllText(Path.Combine(source, "b.txt"), "b");
        DataRootMigrationService.Arm(source, target, true);

        // First attempt moves only the first entry, emulating a crash.
        Directory.CreateDirectory(target);
        File.Move(Path.Combine(source, "a.txt"), Path.Combine(target, "a.txt"));
        Assert.IsTrue(File.Exists(Path.Combine(source, "mode-switch-pending.json")));

        var outcome = DataRootMigrationService.ApplyIfNeeded(target, source);

        Assert.AreEqual(
            DataRootMigrationService.MigrationOutcome.Migrated,
            outcome);
        Assert.IsTrue(File.Exists(Path.Combine(target, "a.txt")));
        Assert.IsTrue(File.Exists(Path.Combine(target, "b.txt")));
        Assert.IsFalse(Directory.Exists(source));
    }

    [TestMethod]
    public void ConflictingTargetEntryKeepsTheSourceIntact()
    {
        (string source, string target) = CreateRoots("source", "target");
        File.WriteAllText(Path.Combine(source, "a.txt"), "source copy");
        Directory.CreateDirectory(target);
        File.WriteAllText(Path.Combine(target, "a.txt"), "pre-existing target data");
        DataRootMigrationService.Arm(source, target, true);

        DataRootMigrationService.DataRootMigrationException failure =
            Assert.ThrowsExactly<DataRootMigrationService.DataRootMigrationException>(
                () => DataRootMigrationService.ApplyIfNeeded(target, source));

        Assert.AreEqual("data.migration_residual_entries", failure.ErrorCode);
        Assert.IsTrue(File.Exists(Path.Combine(source, "a.txt")),
            "A conflict keeps the source entry for manual resolution.");
        Assert.IsTrue(File.Exists(Path.Combine(source, "mode-switch-pending.json")),
            "The pending marker survives for the next retry.");
    }

    [TestMethod]
    public void StalePendingMarkerIsDiscardedWhenModeFlippedBack()
    {
        (string source, string target) = CreateRoots("source", "target");
        File.WriteAllText(Path.Combine(source, "a.txt"), "a");
        // The user flipped the switch twice before restarting: the pending
        // marker in the outgoing root targets a root that is no longer active.
        DataRootMigrationService.Arm(source, target, true);

        // The active root is `source` again (marker removed); applying against
        // itself must discard the stale pending file without moving anything.
        var outcome = DataRootMigrationService.ApplyIfNeeded(source, target);

        Assert.AreEqual(DataRootMigrationService.MigrationOutcome.None, outcome);
        Assert.IsTrue(File.Exists(Path.Combine(source, "a.txt")));
        Assert.IsFalse(File.Exists(Path.Combine(source, "mode-switch-pending.json")));
    }

    [TestMethod]
    public void LaunchApplicationSkipsRedirectedModes()
    {
        var testMode = new HostStartupOptions
        {
            TestMode = true,
            ReadinessDir = Path.Combine(_root, "readiness"),
        };
        var devMode = new HostStartupOptions
        {
            DevelopmentDataRoot = Path.Combine(_root, "dev"),
        };

        Assert.AreEqual(
            DataRootMigrationService.MigrationOutcome.None,
            DataRootMigrationService.ApplyForLaunch(
                testMode,
                AppContext.BaseDirectory,
                _root));
        Assert.AreEqual(
            DataRootMigrationService.MigrationOutcome.None,
            DataRootMigrationService.ApplyForLaunch(
                devMode,
                AppContext.BaseDirectory,
                _root));
    }

    private (string Source, string Target) CreateRoots(
        string sourceName,
        string targetName)
    {
        string source = Path.Combine(_root, sourceName);
        string target = Path.Combine(_root, targetName);
        Directory.CreateDirectory(source);
        return (source, target);
    }
}
