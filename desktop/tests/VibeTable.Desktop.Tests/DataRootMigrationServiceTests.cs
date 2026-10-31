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

    [TestMethod]
    public void MigrationRelocatesRegisteredWorkspacePathsAndKeepsExternalOnes()
    {
        (string source, string target) = CreateRoots("source", "target");
        string workspaceId = "11111111-1111-4111-8111-111111111111";
        string managedRoot = Path.Combine(
            source, "shell", "workspaces", workspaceId);
        Directory.CreateDirectory(Path.Combine(managedRoot, ".vibetable"));
        File.WriteAllText(
            Path.Combine(managedRoot, ".vibetable", "workspace.json"),
            $$"""{"workspaceId":"{{workspaceId}}"}""");
        Directory.CreateDirectory(Path.Combine(source, "shell"));
        File.WriteAllText(
            Path.Combine(source, "shell", "workspace-registry-v2.json"),
            $$"""
            {"formatVersion":2,"workspaces":[
              {"workspaceId":"{{workspaceId}}",
               "selectedRoot":"{{managedRoot.Replace("\\", "\\\\")}}",
               "activityRoot":null},
              {"workspaceId":"22222222-2222-4222-8222-222222222222",
               "selectedRoot":"D:\\External\\ws","activityRoot":null}]}
            """);
        DataRootMigrationService.Arm(source, target, true);

        var outcome = DataRootMigrationService.ApplyIfNeeded(target, source);

        Assert.AreEqual(
            DataRootMigrationService.MigrationOutcome.Migrated,
            outcome);
        string registry = File.ReadAllText(Path.Combine(
            target, "shell", "workspace-registry-v2.json"));
        StringAssert.Contains(
            registry,
            Path.Combine(target, "shell", "workspaces", workspaceId)
                .Replace("\\", "\\\\"),
            "The managed workspace root must follow the data.");
        StringAssert.Contains(registry, "D:\\\\External\\\\ws",
            "Externally rooted workspaces keep their registration.");
    }

    [TestMethod]
    public void MigrationFailsWhenARelocatedWorkspaceIdentityMismatches()
    {
        (string source, string target) = CreateRoots("source", "target");
        string workspaceId = "11111111-1111-4111-8111-111111111111";
        string managedRoot = Path.Combine(
            source, "shell", "workspaces", workspaceId);
        Directory.CreateDirectory(Path.Combine(managedRoot, ".vibetable"));
        File.WriteAllText(
            Path.Combine(managedRoot, ".vibetable", "workspace.json"),
            """{"workspaceId":"33333333-3333-4333-8333-333333333333"}""");
        Directory.CreateDirectory(Path.Combine(source, "shell"));
        File.WriteAllText(
            Path.Combine(source, "shell", "workspace-registry-v2.json"),
            $$"""{"formatVersion":2,"workspaces":[{"workspaceId":"{{workspaceId}}","selectedRoot":"{{managedRoot.Replace("\\", "\\\\")}}","activityRoot":null}]}""");
        DataRootMigrationService.Arm(source, target, true);

        DataRootMigrationService.DataRootMigrationException failure =
            Assert.ThrowsExactly<DataRootMigrationService.DataRootMigrationException>(
                () => DataRootMigrationService.ApplyIfNeeded(target, source));

        Assert.AreEqual(
            "data.migration_workspace_identity_mismatch",
            failure.ErrorCode);
        Assert.IsTrue(File.Exists(Path.Combine(
                source,
                "shell",
                "workspace-registry-v2.json")),
            "A failed relocation keeps the source untouched for retry.");
    }

    [TestMethod]
    public void RetryFinishesCleanupOfAnAlreadyCopiedCrossVolumeEntry()
    {
        (string source, string target) = CreateRoots("source", "target");
        Directory.CreateDirectory(Path.Combine(source, "activity", "ws"));
        File.WriteAllText(
            Path.Combine(source, "activity", "ws", "workspace.db"),
            "db payload");
        DataRootMigrationService.Arm(source, target, true);
        // Emulate a cross-volume copy that committed the target tree but died
        // before deleting the source directory.
        Directory.CreateDirectory(Path.Combine(target, "activity", "ws"));
        File.WriteAllText(
            Path.Combine(target, "activity", "ws", "workspace.db"),
            "db payload");

        var outcome = DataRootMigrationService.ApplyIfNeeded(target, source);

        Assert.AreEqual(
            DataRootMigrationService.MigrationOutcome.Migrated,
            outcome);
        Assert.IsFalse(Directory.Exists(source),
            "Recognizing the completed copy lets the retry clean the source.");
    }

    [TestMethod]
    public void StagingCleanupOnlyTouchesTopLevelGuidSuffixedEntries()
    {
        (string source, string target) = CreateRoots("source", "target");
        // The outgoing root only holds the pending marker, so the migration
        // itself is a no-op; the target root carries staging leftovers plus a
        // user file whose name merely looks like one.
        DataRootMigrationService.Arm(source, target, true);
        Directory.CreateDirectory(target);
        string staleStaging = Path.Combine(
            target,
            "shell.migrating-" + new string('a', 32));
        Directory.CreateDirectory(staleStaging);
        File.WriteAllText(
            Path.Combine(staleStaging, "partial.txt"),
            "partial");
        string userLookalike = Path.Combine(
            target,
            "report.migrating-backup.txt");
        File.WriteAllText(userLookalike, "user data");
        string nonHexLookalike = Path.Combine(
            target,
            "already-moved.migrating-not-a-guid");
        Directory.CreateDirectory(nonHexLookalike);

        DataRootMigrationService.ApplyIfNeeded(target, source);

        Assert.IsFalse(Directory.Exists(staleStaging),
            "A real staging leftover (top level, GUID suffix) is removed.");
        Assert.IsTrue(File.Exists(userLookalike),
            "User files with lookalike names are never touched.");
        Assert.IsTrue(Directory.Exists(nonHexLookalike),
            "Non-GUID-suffixed entries are never touched.");
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
