using System.Diagnostics;
using System.Text;
using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class PendingUpdateActivationJournalTests
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
    public async Task RecoveryReadWaitsForJournalWriterBeforeReadingState()
    {
        UpdateApplyPlan plan = CreatePendingPlan("recovery-read-lock", '0');
        var watchdog = new UpdateProcessIdentity(
            123,
            new DateTimeOffset(2026, 8, 31, 1, 0, 0, TimeSpan.Zero));
        PendingUpdateActivationJournal.Publish(plan, watchdog);
        PendingUpdateActivationJournal.RecordUpdatedLaunch(
            plan,
            watchdog,
            "owned-group",
            new string('1', 64));
        var readStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        Task<UpdateRecoveryState> read;

        using (FileStream writer = ClaimJournalLock(plan))
        {
            read = Task.Run(() =>
            {
                readStarted.SetResult();
                return PendingUpdateActivationJournal.ReadRecoveryState(plan, watchdog);
            });
            await readStarted.Task.WaitAsync(TimeSpan.FromSeconds(5));

            Assert.IsFalse(
                read.Wait(TimeSpan.FromMilliseconds(250)),
                "Recovery read bypassed an active journal writer lock.");
        }

        Assert.AreEqual(
            UpdateRecoveryState.LaunchingUpdatedApp,
            await read.WaitAsync(TimeSpan.FromSeconds(5)));
    }

    [TestMethod]
    public async Task JournalWriterWaitsForActiveReaderBeforeReplacingState()
    {
        UpdateApplyPlan plan = CreatePendingPlan("journal-writer-lock", '2');
        var watchdog = new UpdateProcessIdentity(
            123,
            new DateTimeOffset(2026, 8, 31, 1, 1, 0, TimeSpan.Zero));
        PendingUpdateActivationJournal.Publish(plan, watchdog);
        var writeStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        Task write;

        using (FileStream reader = ClaimJournalLock(plan))
        {
            write = Task.Run(() =>
            {
                writeStarted.SetResult();
                PendingUpdateActivationJournal.RecordUpdatedLaunch(
                    plan,
                    watchdog,
                    "owned-group",
                    new string('3', 64));
            });
            await writeStarted.Task.WaitAsync(TimeSpan.FromSeconds(5));
            await Task.Delay(TimeSpan.FromMilliseconds(250));

            Assert.IsFalse(
                write.IsCompleted,
                "Journal writer failed or bypassed an active reader lock.");
        }

        await write.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.AreEqual(
            UpdateRecoveryState.LaunchingUpdatedApp,
            PendingUpdateActivationJournal.ReadRecoveryState(plan, watchdog));
    }

    [TestMethod]
    public async Task RecoveryReadFailsClosedWhenJournalLockOutlivesBudget()
    {
        UpdateApplyPlan plan = CreatePendingPlan("recovery-read-lock-timeout", '4');
        var watchdog = new UpdateProcessIdentity(
            123,
            new DateTimeOffset(2026, 8, 31, 1, 2, 0, TimeSpan.Zero));
        PendingUpdateActivationJournal.Publish(plan, watchdog);
        PendingUpdateActivationJournal.RecordUpdatedLaunch(
            plan,
            watchdog,
            "owned-group",
            new string('5', 64));
        var readStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        Task<UpdateRecoveryState> read;

        using (FileStream writer = ClaimJournalLock(plan))
        {
            read = Task.Run(() =>
            {
                readStarted.SetResult();
                return PendingUpdateActivationJournal.ReadRecoveryState(plan, watchdog);
            });
            await readStarted.Task.WaitAsync(TimeSpan.FromSeconds(5));
            await Task.Delay(TimeSpan.FromMilliseconds(5_500));

            Assert.IsTrue(
                read.IsCompleted,
                "Recovery read remained eligible to acquire the lock after its budget expired.");
            IOException contention = await Assert.ThrowsExactlyAsync<IOException>(async () =>
            {
                _ = await read;
            });
            Assert.IsTrue(
                (contention.HResult & 0xffff) is 32 or 33,
                $"Unexpected lock contention HRESULT: 0x{contention.HResult:x8}.");
        }
    }

    [TestMethod]
    public void UnconfirmedActivationResumesWithoutCleanupArgumentsAfterProcessExit()
    {
        UpdateApplyPlan plan = CreatePendingPlan("resume", 'a');
        var updater = new UpdateProcessIdentity(
            int.MaxValue,
            new DateTimeOffset(2026, 8, 27, 4, 0, 0, TimeSpan.Zero));
        PendingUpdateActivationJournal.Publish(plan, updater);

        Assert.IsTrue(UpdateProcessCommand.TryCreateActivationGate(
            CleanupArguments(plan, updater),
            out IUpdateActivationGate? first,
            runningRoot: plan.TargetRoot,
            waitForUpdaterExit: _ => Task.FromResult(true)));
        Assert.IsNotNull(first);

        Assert.IsTrue(UpdateProcessCommand.TryCreateActivationGate(
            [],
            out IUpdateActivationGate? resumed,
            runningRoot: plan.TargetRoot,
            waitForUpdaterExit: _ => Task.FromResult(true)));
        Assert.IsNotNull(resumed);
        Assert.IsTrue(Directory.Exists(plan.StagingRoot));
        Assert.IsTrue(File.Exists(PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot)));
        Assert.AreEqual(
            "old",
            File.ReadAllText(Path.Combine(plan.StagingRoot, "backup", "VibeTable.Next.exe")));
    }

    [TestMethod]
    public void CleanupArgumentsCannotBypassAMissingJournal()
    {
        UpdateApplyPlan plan = CreatePendingPlan("missing", 'b');
        var updater = new UpdateProcessIdentity(
            int.MaxValue,
            new DateTimeOffset(2026, 8, 27, 4, 1, 0, TimeSpan.Zero));

        Assert.IsFalse(UpdateProcessCommand.TryCreateActivationGate(
            CleanupArguments(plan, updater),
            out IUpdateActivationGate? gate,
            runningRoot: plan.TargetRoot,
            waitForUpdaterExit: _ => Task.FromResult(true)));
        Assert.IsNull(gate);
        Assert.IsTrue(Directory.Exists(plan.StagingRoot));
    }

    [TestMethod]
    public void MismatchedJournalIsRetainedWithoutFallingBackToCleanupArguments()
    {
        UpdateApplyPlan plan = CreatePendingPlan("mismatch", 'c');
        var updater = new UpdateProcessIdentity(
            int.MaxValue,
            new DateTimeOffset(2026, 8, 27, 4, 2, 0, TimeSpan.Zero));
        PendingUpdateActivationJournal.Publish(plan, updater);
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);
        string original = File.ReadAllText(pointerPath);
        string tampered = original.Replace(
            "\"targetVersion\":\"1.1.0\"",
            "\"targetVersion\":\"1.2.0\"",
            StringComparison.Ordinal);
        Assert.AreNotEqual(original, tampered);
        File.WriteAllText(pointerPath, tampered);

        Assert.IsFalse(UpdateProcessCommand.TryCreateActivationGate(
            CleanupArguments(plan, updater),
            out _,
            runningRoot: plan.TargetRoot,
            waitForUpdaterExit: _ => Task.FromResult(true)));
        Assert.AreEqual(tampered, File.ReadAllText(pointerPath));
        Assert.IsTrue(Directory.Exists(plan.StagingRoot));
        Assert.AreEqual(
            "old",
            File.ReadAllText(Path.Combine(plan.StagingRoot, "backup", "VibeTable.Next.exe")));
    }

    [TestMethod]
    public async Task ConfirmedActivationResumesCleanupAfterFailureWithoutAnotherConfirmation()
    {
        UpdateApplyPlan plan = CreatePendingPlan("confirmed", 'd');
        var updater = new UpdateProcessIdentity(
            int.MaxValue,
            new DateTimeOffset(2026, 8, 27, 4, 3, 0, TimeSpan.Zero));
        PendingUpdateActivationJournal.Publish(plan, updater);

        Assert.IsTrue(UpdateProcessCommand.TryCreateActivationGate(
            CleanupArguments(plan, updater),
            out IUpdateActivationGate? first,
            runningRoot: plan.TargetRoot,
            waitForUpdaterExit: _ => Task.FromResult(true),
            cleanupStage: _ => false));
        Assert.IsNotNull(first);
        first.ConfirmActivation();
        Assert.IsFalse(await first.Completion.WaitAsync(TimeSpan.FromSeconds(5)));

        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);
        using (JsonDocument pointer = JsonDocument.Parse(File.ReadAllText(pointerPath)))
        {
            Assert.AreEqual(
                "confirmed",
                pointer.RootElement.GetProperty("state").GetString());
        }
        Assert.IsTrue(Directory.Exists(plan.StagingRoot));

        Assert.IsTrue(UpdateProcessCommand.TryCreateActivationGate(
            [],
            out IUpdateActivationGate? resumed,
            runningRoot: plan.TargetRoot,
            waitForUpdaterExit: _ => Task.FromResult(true)));
        Assert.IsNotNull(resumed);
        Assert.IsTrue(await resumed.Completion.WaitAsync(TimeSpan.FromSeconds(5)));
        Assert.IsFalse(Directory.Exists(plan.StagingRoot));
        Assert.IsFalse(File.Exists(pointerPath));
    }

    [TestMethod]
    public async Task ConfirmedActivationFinishesWhenStageWasDeletedBeforePointer()
    {
        UpdateApplyPlan plan = CreatePendingPlan("stage-deleted", 'e');
        var updater = new UpdateProcessIdentity(
            int.MaxValue,
            new DateTimeOffset(2026, 8, 27, 4, 4, 0, TimeSpan.Zero));
        PendingUpdateActivationJournal.Publish(plan, updater);

        Assert.IsTrue(UpdateProcessCommand.TryCreateActivationGate(
            CleanupArguments(plan, updater),
            out IUpdateActivationGate? first,
            runningRoot: plan.TargetRoot,
            waitForUpdaterExit: _ => Task.FromResult(true),
            cleanupStage: current =>
            {
                Directory.Delete(current.StagingRoot, recursive: true);
                return false;
            }));
        Assert.IsNotNull(first);
        first.ConfirmActivation();
        Assert.IsFalse(await first.Completion.WaitAsync(TimeSpan.FromSeconds(5)));
        Assert.IsFalse(Directory.Exists(plan.StagingRoot));
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);
        Assert.IsTrue(File.Exists(pointerPath));

        Assert.IsTrue(UpdateProcessCommand.TryCreateActivationGate(
            [],
            out IUpdateActivationGate? resumed,
            runningRoot: plan.TargetRoot,
            waitForUpdaterExit: _ => Task.FromResult(true)));
        Assert.IsNotNull(resumed);
        Assert.IsTrue(await resumed.Completion.WaitAsync(TimeSpan.FromSeconds(5)));
        Assert.IsFalse(File.Exists(pointerPath));
    }

    [TestMethod]
    public async Task ConfirmedActivationFinishesWhenStageWasPartiallyDeleted()
    {
        UpdateApplyPlan plan = CreatePendingPlan("stage-partial", '4');
        var updater = new UpdateProcessIdentity(
            int.MaxValue,
            new DateTimeOffset(2026, 8, 27, 4, 5, 0, TimeSpan.Zero));
        PendingUpdateActivationJournal.Publish(plan, updater);

        Assert.IsTrue(UpdateProcessCommand.TryCreateActivationGate(
            CleanupArguments(plan, updater),
            out IUpdateActivationGate? first,
            runningRoot: plan.TargetRoot,
            waitForUpdaterExit: _ => Task.FromResult(true),
            cleanupStage: current =>
            {
                File.Delete(Path.Combine(current.StagingRoot, "update-plan.json"));
                Directory.Delete(Path.Combine(current.StagingRoot, "package"), recursive: true);
                return false;
            }));
        Assert.IsNotNull(first);
        first.ConfirmActivation();
        Assert.IsFalse(await first.Completion.WaitAsync(TimeSpan.FromSeconds(5)));
        Assert.IsTrue(Directory.Exists(plan.StagingRoot));
        Assert.IsFalse(File.Exists(Path.Combine(plan.StagingRoot, "update-plan.json")));
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);

        Assert.IsTrue(UpdateProcessCommand.TryCreateActivationGate(
            [],
            out IUpdateActivationGate? resumed,
            runningRoot: plan.TargetRoot,
            waitForUpdaterExit: _ => Task.FromResult(true)));
        Assert.IsNotNull(resumed);
        Assert.IsTrue(await resumed.Completion.WaitAsync(TimeSpan.FromSeconds(5)));
        Assert.IsFalse(Directory.Exists(plan.StagingRoot));
        Assert.IsFalse(File.Exists(pointerPath));
    }

    [TestMethod]
    public void ExistingPointerPreventsASecondUpdateFromPublishing()
    {
        UpdateApplyPlan first = CreatePendingPlan("first", 'f');
        var updater = new UpdateProcessIdentity(
            int.MaxValue,
            new DateTimeOffset(2026, 8, 27, 4, 5, 0, TimeSpan.Zero));
        PendingUpdateActivationJournal.Publish(first, updater);
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(first.TargetRoot);
        string original = File.ReadAllText(pointerPath);

        string secondStage = Path.Combine(Root(), ".VibeTable.Next.update-second");
        string secondSource = Path.Combine(secondStage, "package", "VibeTable");
        CreatePackageTree(secondSource, "1.2.0", "newer");
        var second = new UpdateApplyPlan(
            1,
            first.TargetRoot,
            secondSource,
            secondStage,
            456,
            "1.1.0",
            "1.2.0",
            new string('1', 64));
        File.WriteAllText(
            Path.Combine(secondStage, "update-plan.json"),
            JsonSerializer.Serialize(second));

        ReleaseUpdateException exception = Assert.ThrowsExactly<ReleaseUpdateException>(() =>
            PendingUpdateActivationJournal.Publish(second, updater));

        Assert.AreEqual("UPDATE_ACTIVATION_PENDING", exception.Code);
        Assert.AreEqual(original, File.ReadAllText(pointerPath));
        Assert.IsTrue(Directory.Exists(first.StagingRoot));
        Assert.IsTrue(Directory.Exists(second.StagingRoot));
    }

    [TestMethod]
    public async Task PreparedActivationFromFailedApplyReleasesPointerButRetainsStage()
    {
        UpdateApplyPlan plan = CreatePendingPlan("apply-failed", '1');
        var updater = new UpdateProcessIdentity(
            int.MaxValue,
            new DateTimeOffset(2026, 8, 27, 4, 6, 0, TimeSpan.Zero));
        PendingUpdateActivationJournal.Publish(plan, updater);
        File.WriteAllText(
            Path.Combine(plan.TargetRoot, "release.json"),
            ReleaseJson(plan.CurrentVersion));

        Assert.IsTrue(UpdateProcessCommand.TryCreateActivationGate(
            [],
            out IUpdateActivationGate? recovered,
            runningRoot: plan.TargetRoot,
            waitForUpdaterExit: _ => Task.FromResult(true)));
        Assert.IsNotNull(recovered);
        recovered.ConfirmActivation();

        Assert.IsTrue(await recovered.Completion.WaitAsync(TimeSpan.FromSeconds(5)));
        Assert.IsFalse(File.Exists(
            PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot)));
        Assert.IsTrue(Directory.Exists(plan.StagingRoot));
        Assert.AreEqual(
            "old",
            File.ReadAllText(Path.Combine(plan.StagingRoot, "backup", "VibeTable.Next.exe")));
    }

    [TestMethod]
    public void FailedApplyCanSynchronouslyAbandonOnlyItsExactPreparedPointer()
    {
        UpdateApplyPlan plan = CreatePendingPlan("apply-abandon", '5');
        var updater = new UpdateProcessIdentity(
            Environment.ProcessId,
            PendingUpdateActivationJournal.CurrentProcessIdentity().StartedAtUtc);
        PendingUpdateActivationJournal.Publish(plan, updater);
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);
        File.WriteAllText(
            Path.Combine(plan.TargetRoot, "release.json"),
            ReleaseJson(plan.CurrentVersion));

        Assert.IsFalse(PendingUpdateActivationJournal.TryAbandonPrepared(
            plan,
            updater with { StartedAtUtc = updater.StartedAtUtc.AddSeconds(1) }));
        Assert.IsTrue(File.Exists(pointerPath));

        File.Delete(Path.Combine(plan.StagingRoot, "update-plan.json"));
        Directory.Delete(Path.Combine(plan.StagingRoot, "package"), recursive: true);

        Assert.IsTrue(PendingUpdateActivationJournal.TryAbandonPrepared(plan, updater));
        Assert.IsFalse(File.Exists(pointerPath));
        Assert.IsTrue(Directory.Exists(plan.StagingRoot));
    }

    [TestMethod]
    public void PreparedActivationWithMissingStageIsRetainedAsInvalid()
    {
        UpdateApplyPlan plan = CreatePendingPlan("prepared-missing", '2');
        var updater = new UpdateProcessIdentity(
            int.MaxValue,
            new DateTimeOffset(2026, 8, 27, 4, 7, 0, TimeSpan.Zero));
        PendingUpdateActivationJournal.Publish(plan, updater);
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);
        Directory.Delete(plan.StagingRoot, recursive: true);

        Assert.IsFalse(UpdateProcessCommand.TryCreateActivationGate(
            [],
            out _,
            runningRoot: plan.TargetRoot,
            waitForUpdaterExit: _ => Task.FromResult(true)));
        Assert.IsTrue(File.Exists(pointerPath));
    }

    [TestMethod]
    [DataRow("unknown")]
    [DataRow("duplicate")]
    [DataRow("bom")]
    [DataRow("oversized")]
    public void MalformedActivationPointerIsRetained(string mutation)
    {
        UpdateApplyPlan plan = CreatePendingPlan($"malformed-{mutation}", '3');
        var updater = new UpdateProcessIdentity(
            int.MaxValue,
            new DateTimeOffset(2026, 8, 27, 4, 8, 0, TimeSpan.Zero));
        PendingUpdateActivationJournal.Publish(plan, updater);
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);
        string original = File.ReadAllText(pointerPath);
        byte[] bytes = mutation switch
        {
            "unknown" => Encoding.UTF8.GetBytes(original.Insert(
                original.Length - 1,
                ",\"unexpected\":true")),
            "duplicate" => Encoding.UTF8.GetBytes(original.Replace(
                "\"state\":\"prepared\"",
                "\"state\":\"prepared\",\"state\":\"prepared\"",
                StringComparison.Ordinal)),
            "bom" => [0xef, 0xbb, 0xbf, .. Encoding.UTF8.GetBytes(original)],
            "oversized" => Encoding.UTF8.GetBytes(new string(' ', 16 * 1024 + 1)),
            _ => throw new AssertFailedException($"Unknown mutation {mutation}"),
        };
        File.WriteAllBytes(pointerPath, bytes);

        Assert.IsFalse(UpdateProcessCommand.TryCreateActivationGate(
            [],
            out _,
            runningRoot: plan.TargetRoot,
            waitForUpdaterExit: _ => Task.FromResult(true)));
        CollectionAssert.AreEqual(bytes, File.ReadAllBytes(pointerPath));
        Assert.IsTrue(Directory.Exists(plan.StagingRoot));
    }

    [TestMethod]
    public async Task ReusedUpdaterPidDoesNotWaitForTheUnrelatedProcess()
    {
        using var process = new Process
        {
            StartInfo = new ProcessStartInfo
            {
                FileName = Environment.GetEnvironmentVariable("COMSPEC") ?? "cmd.exe",
                UseShellExecute = false,
                CreateNoWindow = true,
            },
        };
        process.StartInfo.ArgumentList.Add("/d");
        process.StartInfo.ArgumentList.Add("/c");
        process.StartInfo.ArgumentList.Add("ping -n 30 127.0.0.1 >nul");
        Assert.IsTrue(process.Start());
        try
        {
            var reused = new UpdateProcessIdentity(
                process.Id,
                new DateTimeOffset(process.StartTime.ToUniversalTime(), TimeSpan.Zero)
                    .AddSeconds(1));

            Assert.IsTrue(await PendingUpdateActivationJournal.WaitForUpdaterExitAsync(reused));
            Assert.IsFalse(process.HasExited);
        }
        finally
        {
            if (!process.HasExited)
            {
                process.Kill(entireProcessTree: true);
                await process.WaitForExitAsync();
            }
        }
    }

    [TestMethod]
    public void ActivationPointerLinkIsRejectedAndRetained()
    {
        UpdateApplyPlan plan = CreatePendingPlan("pointer-link", '6');
        var updater = new UpdateProcessIdentity(
            int.MaxValue,
            new DateTimeOffset(2026, 8, 27, 4, 9, 0, TimeSpan.Zero));
        PendingUpdateActivationJournal.Publish(plan, updater);
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);
        string actualPath = pointerPath + ".actual";
        File.Move(pointerPath, actualPath);
        try
        {
            try
            {
                File.CreateSymbolicLink(pointerPath, actualPath);
            }
            catch (Exception exception) when (exception is
                IOException or UnauthorizedAccessException)
            {
                Assert.Inconclusive("当前 Windows 环境不允许创建文件符号链接。");
            }

            Assert.IsFalse(UpdateProcessCommand.TryCreateActivationGate(
                [],
                out _,
                runningRoot: plan.TargetRoot,
                waitForUpdaterExit: _ => Task.FromResult(true)));
            Assert.IsTrue(File.Exists(pointerPath));
            Assert.IsTrue(File.Exists(actualPath));
        }
        finally
        {
            if (File.Exists(pointerPath))
            {
                File.Delete(pointerPath);
            }
        }
    }

    [TestMethod]
    public void PublishRejectsAncestorJunctionBeforeWritingOutsideControlledVolumePath()
    {
        string root = Root();
        string outside = Path.Combine(root, "outside-publish-ancestor");
        string installParent = Path.Combine(outside, "install-parent");
        string junction = Path.Combine(root, "controlled-publish-ancestor");
        Directory.CreateDirectory(installParent);
        string sentinel = Path.Combine(outside, "sentinel.txt");
        File.WriteAllText(sentinel, "outside");
        if (!TryCreateJunction(junction, outside))
        {
            Assert.Inconclusive("当前 Windows 环境无法创建目录 junction。");
        }
        string target = Path.Combine(junction, "install-parent", "VibeTable.Next");
        string stage = Path.Combine(
            junction,
            "install-parent",
            ".VibeTable.Next.update-ancestor");
        string source = Path.Combine(stage, "package", "VibeTable");
        CreatePackageTree(target, "1.0.0", "old");
        CreatePackageTree(source, "1.1.0", "new");
        var plan = new UpdateApplyPlan(
            1,
            target,
            source,
            stage,
            123,
            "1.0.0",
            "1.1.0",
            new string('7', 64));

        try
        {
            ReleaseUpdateException error = Assert.ThrowsExactly<ReleaseUpdateException>(() =>
                PendingUpdateActivationJournal.Publish(
                    plan,
                    new UpdateProcessIdentity(123, DateTimeOffset.UtcNow)));

            Assert.AreEqual("UPDATE_REPARSE_POINT_REJECTED", error.Code);
            Assert.AreEqual("outside", File.ReadAllText(sentinel));
            Assert.IsFalse(File.Exists(Path.Combine(
                installParent,
                ".VibeTable.Next.update-pending.json")));
        }
        finally
        {
            if (Directory.Exists(junction))
            {
                Directory.Delete(junction);
            }
        }
    }

    [TestMethod]
    public void StartupReadRejectsAncestorJunctionWithoutChangingExternalState()
    {
        string root = Root();
        string controlled = Path.Combine(root, "controlled-read-ancestor");
        string installParent = Path.Combine(controlled, "install-parent");
        string target = Path.Combine(installParent, "VibeTable.Next");
        string stage = Path.Combine(installParent, ".VibeTable.Next.update-read-ancestor");
        string source = Path.Combine(stage, "package", "VibeTable");
        CreatePackageTree(target, "1.0.0", "old");
        CreatePackageTree(source, "1.1.0", "new");
        var plan = new UpdateApplyPlan(
            1,
            target,
            source,
            stage,
            123,
            "1.0.0",
            "1.1.0",
            new string('8', 64));
        var updater = new UpdateProcessIdentity(123, DateTimeOffset.UtcNow);
        PendingUpdateActivationJournal.Publish(plan, updater);
        string outside = Path.Combine(root, "outside-read-ancestor");
        Directory.Move(controlled, outside);
        string sentinel = Path.Combine(outside, "sentinel.txt");
        File.WriteAllText(sentinel, "outside");
        if (!TryCreateJunction(controlled, outside))
        {
            Directory.Move(outside, controlled);
            Assert.Inconclusive("当前 Windows 环境无法创建目录 junction。");
        }

        try
        {
            UpdateActivationStartupResolution resolution =
                PendingUpdateActivationJournal.ResolveStartup(
                    [],
                    target,
                    new UpdateProcessIdentity(124, updater.StartedAtUtc.AddSeconds(1)),
                    new NoOpLifetime(),
                    _ => Task.FromResult(true),
                    _ => true);

            Assert.AreEqual(UpdateActivationStartupDisposition.Blocked, resolution.Disposition);
            Assert.AreEqual("UPDATE_ACTIVATION_INVALID", resolution.ErrorCode);
            Assert.AreEqual("outside", File.ReadAllText(sentinel));
        }
        finally
        {
            if (Directory.Exists(controlled))
            {
                Directory.Delete(controlled);
            }
            if (Directory.Exists(outside) && !Directory.Exists(controlled))
            {
                Directory.Move(outside, controlled);
            }
        }
    }

    private UpdateApplyPlan CreatePendingPlan(string suffix, char tokenCharacter)
    {
        string container = Root();
        string target = Path.Combine(container, "VibeTable.Next");
        CreatePackageTree(target, "1.1.0", "new");
        string stage = Path.Combine(container, $".VibeTable.Next.update-{suffix}");
        string source = Path.Combine(stage, "package", "VibeTable");
        CreatePackageTree(source, "1.1.0", "new");
        string backup = Path.Combine(stage, "backup");
        Directory.CreateDirectory(backup);
        File.WriteAllText(Path.Combine(backup, "VibeTable.Next.exe"), "old");
        var plan = new UpdateApplyPlan(
            1,
            target,
            source,
            stage,
            123,
            "1.0.0",
            "1.1.0",
            new string(tokenCharacter, 64));
        File.WriteAllText(
            Path.Combine(stage, "update-plan.json"),
            JsonSerializer.Serialize(plan));
        return plan;
    }

    private static FileStream ClaimJournalLock(UpdateApplyPlan plan)
    {
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);
        string lockPath = Path.Combine(
            Path.GetDirectoryName(pointerPath)!,
            ".VibeTable.Next.update-pending.lock");
        return new FileStream(
            lockPath,
            FileMode.OpenOrCreate,
            FileAccess.ReadWrite,
            FileShare.None);
    }

    private static string[] CleanupArguments(
        UpdateApplyPlan plan,
        UpdateProcessIdentity updater) =>
    [
        "--cleanup-update",
        plan.StagingRoot,
        "--updater-pid",
        updater.ProcessId.ToString(System.Globalization.CultureInfo.InvariantCulture),
        "--update-token",
        plan.Token,
    ];

    private static void CreatePackageTree(string root, string version, string executableContent)
    {
        Directory.CreateDirectory(Path.Combine(root, "resources"));
        File.WriteAllText(Path.Combine(root, "VibeTable.Next.exe"), executableContent);
        File.WriteAllText(Path.Combine(root, "release.json"), ReleaseJson(version));
        File.WriteAllText(Path.Combine(root, "resources", "publish-layout.json"), "{}");
    }

    private static string ReleaseJson(string version) =>
        $$"""{"product":"VibeTable","version":"{{version}}","platform":"windows","architecture":"x64"}""";

    private static bool TryCreateJunction(string junction, string target)
    {
        string command = Environment.GetEnvironmentVariable("COMSPEC") ?? "cmd.exe";
        var start = new ProcessStartInfo
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
        using Process? process = Process.Start(start);
        if (process is null)
        {
            return false;
        }
        process.WaitForExit();
        return process.ExitCode == 0;
    }

    private sealed class NoOpLifetime : IUpdateHostLifetimePort
    {
        public void RequestExit(int exitCode)
        {
        }
    }

    private string Root()
    {
        _root ??= Path.Combine(
            Environment.CurrentDirectory,
            "build",
            "pending-update-activation-tests",
            Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(_root);
        return _root;
    }
}
