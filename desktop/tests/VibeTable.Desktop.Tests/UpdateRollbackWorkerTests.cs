using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class UpdateRollbackWorkerTests
{
    private string? _root;

    [TestCleanup]
    public void Cleanup()
    {
        if (_root is not null && Directory.Exists(_root))
        {
            Directory.Delete(_root, recursive: true);
        }
    }

    [TestMethod]
    public void OwnedEntryAuthorityKeepsExecutableLast()
    {
        CollectionAssert.AreEqual(
            new[] { "resources", "release.json", "VibeTable.Next.exe" },
            UpdatePackageOwnedEntries.InInstallOrder.ToArray());
    }

    [TestMethod]
    public void OwnedEntryAuthorityDefinesCompleteDiskAndLedgerShapes()
    {
        string package = Path.Combine(Root(), "authority-package");
        CreatePackageTree(package, "1.0.0", "old");
        var completeLedger = UpdatePackageOwnedEntries.InInstallOrder
            .Select(name => new UpdateOwnedEntryLedger(name, "restored"))
            .ToArray();

        Assert.IsTrue(UpdatePackageOwnedEntries.AllExistAt(package));
        Assert.IsTrue(UpdatePackageOwnedEntries.IsFullyRestored(completeLedger));

        File.Delete(Path.Combine(package, "VibeTable.Next.exe"));
        Assert.IsFalse(UpdatePackageOwnedEntries.AllExistAt(package));
        Assert.IsFalse(UpdatePackageOwnedEntries.IsFullyRestored(
            completeLedger.Skip(1).ToArray()));
    }

    [TestMethod]
    public void WorkerMustClaimNonceAndIdentityBeforeTargetMutation()
    {
        RollbackFixture fixture = Prepare("claim", '5');
        string targetBefore = File.ReadAllText(
            Path.Combine(fixture.Plan.TargetRoot, "VibeTable.Next.exe"));

        ReleaseUpdateException error = Assert.ThrowsExactly<ReleaseUpdateException>(() =>
            PendingUpdateActivationJournal.RunRollbackWorker(
                fixture.Plan.TargetRoot,
                new string('0', 64),
                fixture.Worker));

        Assert.AreEqual("UPDATE_ACTIVATION_INVALID", error.Code);
        Assert.AreEqual(
            targetBefore,
            File.ReadAllText(Path.Combine(fixture.Plan.TargetRoot, "VibeTable.Next.exe")));
        Assert.IsTrue(File.Exists(PendingUpdateActivationJournal.GetPointerPath(
            fixture.Plan.TargetRoot)));
    }

    [TestMethod]
    public void WorkerRestoresOnlyFixedOwnedEntriesWithExecutableLastAndCreatesReceipt()
    {
        RollbackFixture fixture = Prepare("complete", '6');
        var checkpoints = new List<string>();

        PendingUpdateActivationJournal.RunRollbackWorker(
            fixture.Plan.TargetRoot,
            fixture.WorkerNonce,
            fixture.Worker,
            checkpoints.Add);

        Assert.AreEqual("old", File.ReadAllText(Path.Combine(
            fixture.Plan.TargetRoot, "VibeTable.Next.exe")));
        Assert.AreEqual("unknown", File.ReadAllText(Path.Combine(
            fixture.Plan.TargetRoot, "user-data.db")));
        Assert.AreEqual("workspace", File.ReadAllText(fixture.WorkspaceFile));
        Assert.IsFalse(File.Exists(PendingUpdateActivationJournal.GetPointerPath(
            fixture.Plan.TargetRoot)));
        string receiptPath = PendingUpdateActivationJournal.GetRollbackReceiptPath(
            fixture.Plan.TargetRoot,
            fixture.RollbackAttempt);
        using JsonDocument receipt = JsonDocument.Parse(File.ReadAllText(receiptPath));
        Assert.AreEqual(
            "restoredLaunchPending",
            receipt.RootElement.GetProperty("state").GetString());
        CollectionAssert.AreEqual(
            new[]
            {
                "claim:completed",
                "resources:isolatePlanned", "resources:isolatedOnDisk",
                "resources:restorePlanned", "resources:restoredOnDisk",
                "release.json:isolatePlanned", "release.json:isolatedOnDisk",
                "release.json:restorePlanned", "release.json:restoredOnDisk",
                "VibeTable.Next.exe:isolatePlanned", "VibeTable.Next.exe:isolatedOnDisk",
                "VibeTable.Next.exe:restorePlanned", "VibeTable.Next.exe:restoredOnDisk",
                "finalize:rolledBack", "finalize:restoredLaunchPending",
            },
            checkpoints);
    }

    [TestMethod]
    [DataRow("resources:isolatePlanned")]
    [DataRow("resources:isolatedOnDisk")]
    [DataRow("resources:restorePlanned")]
    [DataRow("resources:restoredOnDisk")]
    [DataRow("release.json:isolatePlanned")]
    [DataRow("release.json:isolatedOnDisk")]
    [DataRow("release.json:restorePlanned")]
    [DataRow("release.json:restoredOnDisk")]
    [DataRow("VibeTable.Next.exe:isolatePlanned")]
    [DataRow("VibeTable.Next.exe:isolatedOnDisk")]
    [DataRow("VibeTable.Next.exe:restorePlanned")]
    [DataRow("VibeTable.Next.exe:restoredOnDisk")]
    [DataRow("finalize:rolledBack")]
    [DataRow("finalize:restoredLaunchPending")]
    public void ReplacementWorkerResumesEveryLedgerCrashWindow(string crashAt)
    {
        RollbackFixture fixture = Prepare($"resume-{crashAt.Replace(':', '-')}", '7');
        Assert.ThrowsExactly<SimulatedWorkerCrash>(() =>
            PendingUpdateActivationJournal.RunRollbackWorker(
                fixture.Plan.TargetRoot,
                fixture.WorkerNonce,
                fixture.Worker,
                checkpoint =>
                {
                    if (checkpoint == crashAt)
                    {
                        throw new SimulatedWorkerCrash();
                    }
                }));
        string replacementNonce = new('8', 64);
        var replacement = new UpdateProcessIdentity(
            fixture.Worker.ProcessId + 1,
            fixture.Worker.StartedAtUtc.AddSeconds(1));
        string sameAttempt = PendingUpdateActivationJournal.RecordRollbackWorkerLaunch(
            fixture.Plan,
            fixture.Watchdog,
            fixture.GroupId,
            replacementNonce,
            replacement: true);

        PendingUpdateActivationJournal.RunRollbackWorker(
            fixture.Plan.TargetRoot,
            replacementNonce,
            replacement);

        Assert.AreEqual(fixture.RollbackAttempt, sameAttempt);
        Assert.AreEqual("old", File.ReadAllText(Path.Combine(
            fixture.Plan.TargetRoot, "VibeTable.Next.exe")));
        Assert.IsTrue(File.Exists(PendingUpdateActivationJournal.GetRollbackReceiptPath(
            fixture.Plan.TargetRoot,
            fixture.RollbackAttempt)));
    }

    [TestMethod]
    [DataRow("finalize:rolledBack")]
    [DataRow("finalize:restoredLaunchPending")]
    public void FinalizeReplacementFailsClosedWhenReceiptShapeIsAmbiguous(string crashAt)
    {
        RollbackFixture fixture = Prepare(
            $"finalize-ambiguous-{crashAt.Replace(':', '-')}",
            'b');
        Assert.ThrowsExactly<SimulatedWorkerCrash>(() =>
            PendingUpdateActivationJournal.RunRollbackWorker(
                fixture.Plan.TargetRoot,
                fixture.WorkerNonce,
                fixture.Worker,
                checkpoint =>
                {
                    if (checkpoint == crashAt)
                    {
                        throw new SimulatedWorkerCrash();
                    }
                }));
        File.WriteAllText(
            PendingUpdateActivationJournal.GetRollbackReceiptPath(
                fixture.Plan.TargetRoot,
                fixture.RollbackAttempt),
            "{}");

        ReleaseUpdateException error = Assert.ThrowsExactly<ReleaseUpdateException>(() =>
            PendingUpdateActivationJournal.RecordRollbackWorkerLaunch(
                fixture.Plan,
                fixture.Watchdog,
                fixture.GroupId,
                new string('c', 64),
                replacement: true));

        Assert.AreEqual("UPDATE_ACTIVATION_INVALID", error.Code);
        using JsonDocument pointer = JsonDocument.Parse(File.ReadAllText(
            PendingUpdateActivationJournal.GetPointerPath(fixture.Plan.TargetRoot)));
        Assert.AreEqual(
            "rollbackFailed",
            pointer.RootElement.GetProperty("state").GetString());
        Assert.AreEqual(
            "UPDATE_ROLLBACK_SHAPE_AMBIGUOUS",
            pointer.RootElement.GetProperty("rollbackErrorCode").GetString());
    }

    [TestMethod]
    public void ReplacementFailsClosedWhenLedgerLostItsFailedPackageEvidence()
    {
        RollbackFixture fixture = Prepare("missing-failed-evidence", 'd');
        Assert.ThrowsExactly<SimulatedWorkerCrash>(() =>
            PendingUpdateActivationJournal.RunRollbackWorker(
                fixture.Plan.TargetRoot,
                fixture.WorkerNonce,
                fixture.Worker,
                checkpoint =>
                {
                    if (checkpoint == "resources:restoredOnDisk")
                    {
                        throw new SimulatedWorkerCrash();
                    }
                }));
        Directory.Delete(
            Path.Combine(fixture.Plan.StagingRoot, "failed-package", "resources"),
            recursive: true);
        string replacementNonce = new('e', 64);
        var replacement = new UpdateProcessIdentity(
            fixture.Worker.ProcessId + 1,
            fixture.Worker.StartedAtUtc.AddSeconds(1));
        PendingUpdateActivationJournal.RecordRollbackWorkerLaunch(
            fixture.Plan,
            fixture.Watchdog,
            fixture.GroupId,
            replacementNonce,
            replacement: true);

        ReleaseUpdateException error = Assert.ThrowsExactly<ReleaseUpdateException>(() =>
            PendingUpdateActivationJournal.RunRollbackWorker(
                fixture.Plan.TargetRoot,
                replacementNonce,
                replacement));

        Assert.AreEqual("UPDATE_ACTIVATION_INVALID", error.Code);
        using JsonDocument pointer = JsonDocument.Parse(File.ReadAllText(
            PendingUpdateActivationJournal.GetPointerPath(fixture.Plan.TargetRoot)));
        Assert.AreEqual(
            "rollbackFailed",
            pointer.RootElement.GetProperty("state").GetString());
    }

    [TestMethod]
    public void WorkerRejectsFailedPackageJunctionBeforeTargetMutation()
    {
        RollbackFixture fixture = Prepare("failed-package-junction", 'f');
        string outside = Path.Combine(Root(), "outside-failed-package");
        Directory.CreateDirectory(outside);
        string sentinel = Path.Combine(outside, "sentinel.txt");
        File.WriteAllText(sentinel, "outside");
        string failedRoot = Path.Combine(fixture.Plan.StagingRoot, "failed-package");
        if (!TryCreateJunction(failedRoot, outside))
        {
            Assert.Inconclusive("当前 Windows 环境无法创建目录 junction。");
        }
        try
        {
            ReleaseUpdateException error = Assert.ThrowsExactly<ReleaseUpdateException>(() =>
                PendingUpdateActivationJournal.RunRollbackWorker(
                    fixture.Plan.TargetRoot,
                    fixture.WorkerNonce,
                    fixture.Worker));

            Assert.AreEqual("UPDATE_REPARSE_POINT_REJECTED", error.Code);
            Assert.AreEqual("new", File.ReadAllText(Path.Combine(
                fixture.Plan.TargetRoot,
                "VibeTable.Next.exe")));
            Assert.AreEqual("outside", File.ReadAllText(sentinel));
        }
        finally
        {
            if (Directory.Exists(failedRoot))
            {
                Directory.Delete(failedRoot);
            }
        }
    }

    [TestMethod]
    [DataRow("target")]
    [DataRow("staging")]
    [DataRow("backup")]
    public void WorkerRevalidatesControlledRootsAfterClaimBeforeTargetMutation(string rootKind)
    {
        RollbackFixture fixture = Prepare($"claimed-root-{rootKind}", '0');
        string original = rootKind switch
        {
            "target" => fixture.Plan.TargetRoot,
            "staging" => fixture.Plan.StagingRoot,
            "backup" => Path.Combine(fixture.Plan.StagingRoot, "backup"),
            _ => throw new AssertFailedException("unknown root kind"),
        };
        string actual = Path.Combine(Root(), $"claimed-root-{rootKind}-actual");
        string targetExecutable = Path.Combine(fixture.Plan.TargetRoot, "VibeTable.Next.exe");

        try
        {
            ReleaseUpdateException error = Assert.ThrowsExactly<ReleaseUpdateException>(() =>
                PendingUpdateActivationJournal.RunRollbackWorker(
                    fixture.Plan.TargetRoot,
                    fixture.WorkerNonce,
                    fixture.Worker,
                    checkpoint =>
                    {
                        if (checkpoint != "claim:completed")
                        {
                            return;
                        }
                        Directory.Move(original, actual);
                        if (!TryCreateJunction(original, actual))
                        {
                            Directory.Move(actual, original);
                            Assert.Inconclusive("当前 Windows 环境无法创建目录 junction。");
                        }
                    }));

            Assert.AreEqual("UPDATE_REPARSE_POINT_REJECTED", error.Code);
            string effectiveTarget = rootKind == "target"
                ? Path.Combine(actual, "VibeTable.Next.exe")
                : targetExecutable;
            Assert.AreEqual("new", File.ReadAllText(effectiveTarget));
        }
        finally
        {
            if (Directory.Exists(original)
                && (File.GetAttributes(original) & FileAttributes.ReparsePoint) != 0)
            {
                Directory.Delete(original);
            }
            if (Directory.Exists(actual) && !Directory.Exists(original))
            {
                Directory.Move(actual, original);
            }
        }
    }

    [TestMethod]
    public void WorkerRejectsAncestorJunctionCreatedAfterClaimBeforeTargetMutation()
    {
        RollbackFixture fixture = Prepare("claimed-ancestor-junction", '1', nested: true);
        string installParent = Directory.GetParent(
            fixture.Plan.TargetRoot.TrimEnd(Path.DirectorySeparatorChar))!.FullName;
        string controlled = Directory.GetParent(installParent)!.FullName;
        string outside = Path.Combine(Root(), "outside-worker-ancestor");
        string sentinel = Path.Combine(controlled, "sentinel.txt");
        File.WriteAllText(sentinel, "outside");

        try
        {
            ReleaseUpdateException error = Assert.ThrowsExactly<ReleaseUpdateException>(() =>
                PendingUpdateActivationJournal.RunRollbackWorker(
                    fixture.Plan.TargetRoot,
                    fixture.WorkerNonce,
                    fixture.Worker,
                    checkpoint =>
                    {
                        if (checkpoint != "claim:completed")
                        {
                            return;
                        }
                        Directory.Move(controlled, outside);
                        if (!TryCreateJunction(controlled, outside))
                        {
                            Directory.Move(outside, controlled);
                            Assert.Inconclusive(
                                "当前 Windows 环境无法创建目录 junction。");
                        }
                    }));

            Assert.AreEqual("UPDATE_REPARSE_POINT_REJECTED", error.Code);
            Assert.AreEqual("new", File.ReadAllText(Path.Combine(
                outside,
                "install-parent",
                "VibeTable.Next",
                "VibeTable.Next.exe")));
            Assert.AreEqual("outside", File.ReadAllText(Path.Combine(
                outside,
                "sentinel.txt")));
            using JsonDocument pointer = JsonDocument.Parse(File.ReadAllText(Path.Combine(
                outside,
                "install-parent",
                ".VibeTable.Next.update-pending.json")));
            Assert.AreEqual(
                "rollbackRestoring",
                pointer.RootElement.GetProperty("state").GetString());
            Assert.AreEqual(
                0,
                pointer.RootElement.GetProperty("ownedEntryLedger").GetArrayLength());
        }
        finally
        {
            if (Directory.Exists(controlled)
                && (File.GetAttributes(controlled) & FileAttributes.ReparsePoint) != 0)
            {
                Directory.Delete(controlled);
            }
            if (Directory.Exists(outside) && !Directory.Exists(controlled))
            {
                Directory.Move(outside, controlled);
            }
        }
    }

    private RollbackFixture Prepare(
        string suffix,
        char tokenCharacter,
        bool nested = false)
    {
        string root = Root();
        string container = nested
            ? Path.Combine(root, $"controlled-{suffix}", "install-parent")
            : root;
        Directory.CreateDirectory(container);
        string target = Path.Combine(container, "VibeTable.Next");
        CreatePackageTree(target, "1.1.0", "new");
        File.WriteAllText(Path.Combine(target, "user-data.db"), "unknown");
        string stage = Path.Combine(container, $".VibeTable.Next.update-{suffix}");
        string source = Path.Combine(stage, "package", "VibeTable");
        CreatePackageTree(source, "1.1.0", "new");
        CreatePackageTree(Path.Combine(stage, "backup"), "1.0.0", "old");
        string workspace = Path.Combine(root, "workspace");
        Directory.CreateDirectory(workspace);
        string workspaceFile = Path.Combine(workspace, "records.db");
        File.WriteAllText(workspaceFile, "workspace");
        var plan = new UpdateApplyPlan(
            1, target, source, stage, 123, "1.0.0", "1.1.0", new string(tokenCharacter, 64));
        File.WriteAllText(
            Path.Combine(stage, "update-plan.json"),
            JsonSerializer.Serialize(plan));
        var watchdog = new UpdateProcessIdentity(
            1001,
            new DateTimeOffset(2026, 8, 27, 9, 0, 0, TimeSpan.Zero));
        var updated = new UpdateProcessIdentity(1002, watchdog.StartedAtUtc.AddSeconds(1));
        const string groupId = "owned-rollback";
        string launchNonce = new('9', 64);
        PendingUpdateActivationJournal.Publish(plan, watchdog);
        PendingUpdateActivationJournal.RecordUpdatedLaunch(
            plan, watchdog, groupId, launchNonce);
        var lifetime = new RecordingLifetime();
        UpdateActivationStartupResolution startup = PendingUpdateActivationJournal.ResolveStartup(
            ["--claim-update", launchNonce],
            target,
            updated,
            lifetime,
            _ => Task.FromResult(true),
            _ => true);
        startup.Settlement!.CompleteHealthCheckAsync(
            new UpdateActivationHealth.Failed(
                UpdateActivationFailureCode.WorkspaceHealthProbeFailed),
            CancellationToken.None).GetAwaiter().GetResult();
        PendingUpdateActivationJournal.RecordOwnedGroupQuiesced(plan, watchdog, groupId);
        string workerNonce = new('a', 64);
        string rollbackAttempt = PendingUpdateActivationJournal.RecordRollbackWorkerLaunch(
            plan, watchdog, groupId, workerNonce);
        return new RollbackFixture(
            plan,
            watchdog,
            groupId,
            workerNonce,
            new UpdateProcessIdentity(1003, watchdog.StartedAtUtc.AddSeconds(2)),
            rollbackAttempt,
            workspaceFile);
    }

    private static void CreatePackageTree(string root, string version, string executableContent)
    {
        Directory.CreateDirectory(Path.Combine(root, "resources"));
        File.WriteAllText(Path.Combine(root, "VibeTable.Next.exe"), executableContent);
        File.WriteAllText(
            Path.Combine(root, "release.json"),
            $$"""{"product":"VibeTable","version":"{{version}}","platform":"windows","architecture":"x64"}""");
        File.WriteAllText(Path.Combine(root, "resources", "publish-layout.json"), version);
    }

    private string Root()
    {
        _root ??= Path.Combine(
            Environment.CurrentDirectory,
            "build",
            "update-rollback-worker-tests",
            Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(_root);
        return _root;
    }

    private static bool TryCreateJunction(string junction, string target)
    {
        string command = Environment.GetEnvironmentVariable("COMSPEC") ?? "cmd.exe";
        var start = new System.Diagnostics.ProcessStartInfo
        {
            FileName = command,
            UseShellExecute = false,
            CreateNoWindow = true,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
        };
        start.ArgumentList.Add("/d");
        start.ArgumentList.Add("/c");
        start.ArgumentList.Add("mklink");
        start.ArgumentList.Add("/J");
        start.ArgumentList.Add(junction);
        start.ArgumentList.Add(target);
        using System.Diagnostics.Process? process = System.Diagnostics.Process.Start(start);
        if (process is null)
        {
            return false;
        }
        process.WaitForExit();
        return process.ExitCode == 0;
    }

    private sealed class RecordingLifetime : IUpdateHostLifetimePort
    {
        public void RequestExit(int exitCode)
        {
        }
    }

    private sealed class SimulatedWorkerCrash : Exception;

    private sealed record RollbackFixture(
        UpdateApplyPlan Plan,
        UpdateProcessIdentity Watchdog,
        string GroupId,
        string WorkerNonce,
        UpdateProcessIdentity Worker,
        string RollbackAttempt,
        string WorkspaceFile);
}
