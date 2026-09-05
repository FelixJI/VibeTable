using System.Text.Json;
using System.Text.Json.Nodes;
using Microsoft.Win32.SafeHandles;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class UpdateRecoveryWatchdogTests
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
    [DataRow("WorkspaceHealthProbeFailed")]
    [DataRow("UpdatedProcessExited")]
    [DataRow("HealthTimeout")]
    public async Task EveryFailurePathProvesOwnedGroupEmptyBeforeStartingWorker(
        string failureName)
    {
        UpdateActivationFailureCode failure = Enum.Parse<UpdateActivationFailureCode>(
            failureName);
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth(failure.ToString(), groupId);
        var processes = new RecordingProcessPort(watchdog);
        using UpdateOwnedProcessGroup failedGroup = RecordingProcessPort.Group(
            groupId,
            updated);
        var recovery = new UpdateRecoveryWatchdog(
            processes,
            healthBudget: TimeSpan.FromMilliseconds(50),
            processExitBudget: TimeSpan.FromMilliseconds(50));

        await recovery.RecoverAsync(
            plan,
            watchdog,
            failedGroup,
            groupId,
            failure,
            CancellationToken.None);

        CollectionAssert.AreEqual(
            new[]
            {
                "wait-failed-group",
                "terminate-failed-group",
                "start-worker",
                "wait-worker",
                "wait-worker-group",
                "start-restored",
            },
            processes.Events);
        Assert.AreEqual("old", File.ReadAllText(Path.Combine(
            plan.TargetRoot,
            "VibeTable.Next.exe")));
        Assert.IsFalse(File.Exists(PendingUpdateActivationJournal.GetPointerPath(
            plan.TargetRoot)));
        Assert.AreEqual("rollbackComplete", ReadOnlyReceiptState());
    }

    [TestMethod]
    public async Task WorkerIoFailureSurvivesWatchdogFinalization()
    {
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth("worker-io-failure", groupId);
        var processes = new RecordingProcessPort(watchdog, failWorkerRecovery: true);
        using UpdateOwnedProcessGroup failedGroup = RecordingProcessPort.Group(groupId, updated);
        var recovery = new UpdateRecoveryWatchdog(processes);

        await recovery.RecoverAsync(
            plan,
            watchdog,
            failedGroup,
            groupId,
            UpdateActivationFailureCode.HealthTimeout,
            CancellationToken.None);

        Assert.AreEqual("rollbackFailed", ReadPendingString(plan, "state"));
        Assert.AreEqual("UPDATE_ROLLBACK_IO_FAILED", ReadPendingString(plan, "rollbackErrorCode"));
        Assert.AreEqual(1, processes.Events.Count(value => value == "start-worker"));
        Assert.IsNull(processes.RestoredLaunch);
    }

    [TestMethod]
    public async Task FailedGroupWaitExceptionRequiresExplicitTerminationBeforeWorker()
    {
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth("failed-group-wait-exception", groupId);
        var processes = new RecordingProcessPort(
            watchdog,
            ownedGroupWaitFailure: new InvalidOperationException("simulated group wait failure"));
        using UpdateOwnedProcessGroup failedGroup = RecordingProcessPort.Group(groupId, updated);
        var recovery = new UpdateRecoveryWatchdog(processes);

        await recovery.RecoverAsync(
            plan,
            watchdog,
            failedGroup,
            groupId,
            UpdateActivationFailureCode.UpdatedProcessExited,
            CancellationToken.None);

        CollectionAssert.AreEqual(
            new[]
            {
                "wait-failed-group",
                "terminate-failed-group",
                "start-worker",
                "wait-worker",
                "wait-worker-group",
                "start-restored",
            },
            processes.Events);
        Assert.AreEqual("rollbackComplete", ReadOnlyReceiptState());
    }

    [TestMethod]
    public async Task FailedGroupTerminationExceptionRetainsOwnershipThenCompletesRollback()
    {
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth("failed-group-terminate-exception", groupId);
        var processes = new RecordingProcessPort(
            watchdog,
            terminationFailure: new InvalidOperationException("simulated terminate failure"));
        using UpdateOwnedProcessGroup failedGroup = RecordingProcessPort.Group(groupId, updated);
        var quarantine = new RecordingQuarantine();
        var recovery = new UpdateRecoveryWatchdog(processes, quarantine: quarantine);

        await recovery.RecoverAsync(
            plan,
            watchdog,
            failedGroup,
            groupId,
            UpdateActivationFailureCode.UpdatedProcessExited,
            CancellationToken.None);

        Assert.AreEqual(1, quarantine.RetainedGroups);
        Assert.IsTrue(quarantine.HandleWasOpenAtTransfer);
        Assert.AreEqual("rollbackComplete", ReadOnlyReceiptState());
        Assert.IsFalse(File.Exists(PendingUpdateActivationJournal.GetPointerPath(
            plan.TargetRoot)));
        Assert.IsTrue(processes.Events.Contains("start-worker"));
        Assert.IsTrue(processes.Events.Contains("start-restored"));
    }

    [TestMethod]
    public async Task RecoveryDoesNotMutateTargetOrLaunchWorkerWhileOwnershipIsRetained()
    {
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth("retained-blocks-recovery", groupId);
        var processes = new RecordingProcessPort(
            watchdog,
            terminationFailure: new InvalidOperationException("simulated terminate failure"));
        using UpdateOwnedProcessGroup failedGroup = RecordingProcessPort.Group(groupId, updated);
        var quarantine = new BlockingQuarantine();
        var recovery = new UpdateRecoveryWatchdog(processes, quarantine: quarantine);
        string targetBefore = File.ReadAllText(Path.Combine(
            plan.TargetRoot,
            "VibeTable.Next.exe"));

        Task<bool> pending = recovery.RecoverAsync(
            plan,
            watchdog,
            failedGroup,
            groupId,
            UpdateActivationFailureCode.UpdatedProcessExited,
            CancellationToken.None);
        await quarantine.Entered.Task.WaitAsync(TimeSpan.FromSeconds(5));

        Assert.IsFalse(pending.IsCompleted);
        Assert.IsFalse(processes.Events.Contains("start-worker"));
        Assert.IsFalse(processes.Events.Contains("start-restored"));
        Assert.AreEqual(targetBefore, File.ReadAllText(Path.Combine(
            plan.TargetRoot,
            "VibeTable.Next.exe")));
        Assert.AreEqual("rollbackRequested", ReadPendingString(plan, "state"));

        quarantine.Release();
        Assert.IsTrue(await pending.WaitAsync(TimeSpan.FromSeconds(5)));
        Assert.AreEqual("rollbackComplete", ReadOnlyReceiptState());
    }

    [TestMethod]
    public async Task RestoredLaunchFailureIsTerminalAndDoesNotRecreatePendingPointer()
    {
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth("restored-launch-failed", groupId);
        var processes = new RecordingProcessPort(watchdog, failRestoredLaunch: true);
        using UpdateOwnedProcessGroup failedGroup = RecordingProcessPort.Group(groupId, updated);
        var recovery = new UpdateRecoveryWatchdog(processes);

        await recovery.RecoverAsync(
            plan,
            watchdog,
            failedGroup,
            groupId,
            UpdateActivationFailureCode.HealthTimeout,
            CancellationToken.None);

        Assert.AreEqual("restoredLaunchFailed", ReadOnlyReceiptState());
        Assert.IsFalse(File.Exists(PendingUpdateActivationJournal.GetPointerPath(
            plan.TargetRoot)));
        Assert.AreEqual("old", File.ReadAllText(Path.Combine(
            plan.TargetRoot,
            "VibeTable.Next.exe")));
    }

    [TestMethod]
    public async Task SmokeRollbackStartsRestoredPackageWithIsolatedTestEnvelope()
    {
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth("restored-smoke-envelope", groupId, smokeTest: true);
        var processes = new RecordingProcessPort(watchdog);
        using UpdateOwnedProcessGroup failedGroup = RecordingProcessPort.Group(groupId, updated);
        var recovery = new UpdateRecoveryWatchdog(processes);

        await recovery.RecoverAsync(
            plan,
            watchdog,
            failedGroup,
            groupId,
            UpdateActivationFailureCode.WorkspaceHealthProbeFailed,
            CancellationToken.None);

        Assert.IsNotNull(processes.RestoredLaunch);
        string stageParent = Directory.GetParent(plan.StagingRoot.TrimEnd(
            Path.DirectorySeparatorChar,
            Path.AltDirectorySeparatorChar))!.FullName;
        CollectionAssert.AreEqual(
            new[]
            {
                "--test-mode",
                "--readiness-dir",
                Path.Combine(stageParent, "self-update-restored-readiness"),
                "--e2e-controls-dir",
                Path.Combine(stageParent, "self-update-restored-controls"),
            },
            processes.RestoredLaunch.Arguments.ToArray());
        Assert.IsFalse(processes.RestoredLaunch.Arguments.Contains("--self-update-smoke"));
    }

    [TestMethod]
    public async Task NonSmokeRollbackKeepsRestoredLaunchArgumentsEmpty()
    {
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth("restored-production-envelope", groupId);
        var processes = new RecordingProcessPort(watchdog);
        using UpdateOwnedProcessGroup failedGroup = RecordingProcessPort.Group(groupId, updated);
        var recovery = new UpdateRecoveryWatchdog(processes);

        await recovery.RecoverAsync(
            plan,
            watchdog,
            failedGroup,
            groupId,
            UpdateActivationFailureCode.WorkspaceHealthProbeFailed,
            CancellationToken.None);

        Assert.IsNotNull(processes.RestoredLaunch);
        Assert.AreEqual(0, processes.RestoredLaunch.Arguments.Count);
    }

    [TestMethod]
    public async Task ReceiptUpdateFailureAfterRestoredLaunchRemainsTerminal()
    {
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth("receipt-update-failed", groupId);
        var processes = new RecordingProcessPort(watchdog);
        using UpdateOwnedProcessGroup failedGroup = RecordingProcessPort.Group(groupId, updated);
        var recovery = new UpdateRecoveryWatchdog(
            processes,
            receiptLaunchRecorder: (_, _, _) =>
                throw new IOException("simulated receipt replacement failure"));

        await recovery.RecoverAsync(
            plan,
            watchdog,
            failedGroup,
            groupId,
            UpdateActivationFailureCode.HealthTimeout,
            CancellationToken.None);

        Assert.AreEqual(1, processes.Events.Count(entry => entry == "start-restored"));
        Assert.AreEqual("restoredLaunchPending", ReadOnlyReceiptState());
        Assert.IsFalse(File.Exists(PendingUpdateActivationJournal.GetPointerPath(
            plan.TargetRoot)));
    }

    [TestMethod]
    public async Task FirstWorkerStartFailureUsesSingleReplacement()
    {
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth("worker-start-replacement", groupId);
        var processes = new RecordingProcessPort(watchdog, workerStartFailures: 1);
        using UpdateOwnedProcessGroup failedGroup = RecordingProcessPort.Group(groupId, updated);
        var recovery = new UpdateRecoveryWatchdog(processes);

        await recovery.RecoverAsync(
            plan,
            watchdog,
            failedGroup,
            groupId,
            UpdateActivationFailureCode.UpdatedProcessExited,
            CancellationToken.None);

        Assert.AreEqual(2, processes.Events.Count(entry => entry == "start-worker"));
        Assert.AreEqual("rollbackComplete", ReadOnlyReceiptState());
        Assert.IsFalse(File.Exists(PendingUpdateActivationJournal.GetPointerPath(
            plan.TargetRoot)));
    }

    [TestMethod]
    public async Task SecondWorkerStartFailureBecomesStableRollbackFailure()
    {
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth("worker-start-terminal", groupId);
        var processes = new RecordingProcessPort(watchdog, workerStartFailures: 2);
        using UpdateOwnedProcessGroup failedGroup = RecordingProcessPort.Group(groupId, updated);
        var recovery = new UpdateRecoveryWatchdog(processes);

        await recovery.RecoverAsync(
            plan,
            watchdog,
            failedGroup,
            groupId,
            UpdateActivationFailureCode.UpdatedProcessExited,
            CancellationToken.None);

        Assert.AreEqual(2, processes.Events.Count(entry => entry == "start-worker"));
        Assert.AreEqual("rollbackFailed", ReadPendingString(plan, "state"));
        Assert.AreEqual(
            "UPDATE_ROLLBACK_WORKER_START_FAILED",
            ReadPendingString(plan, "rollbackErrorCode"));
        Assert.IsFalse(Directory.GetFiles(
            Root(),
            ".VibeTable.Next.update-rollback-*.json",
            SearchOption.TopDirectoryOnly).Any());
    }

    [TestMethod]
    public async Task UnprovenWorkerStartFailureNeverLaunchesReplacement()
    {
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth("worker-start-unproven", groupId);
        var processes = new RecordingProcessPort(
            watchdog,
            workerStartFailures: 1,
            workerStartGroupEmpty: false);
        using UpdateOwnedProcessGroup failedGroup = RecordingProcessPort.Group(groupId, updated);
        var quarantine = new RecordingQuarantine();
        var recovery = new UpdateRecoveryWatchdog(processes, quarantine: quarantine);

        await recovery.RecoverAsync(
            plan,
            watchdog,
            failedGroup,
            groupId,
            UpdateActivationFailureCode.UpdatedProcessExited,
            CancellationToken.None);

        Assert.AreEqual(1, processes.Events.Count(entry => entry == "start-worker"));
        Assert.AreEqual("rollbackFailed", ReadPendingString(plan, "state"));
        Assert.AreEqual(
            "UPDATE_WORKER_GROUP_NOT_QUIESCED",
            ReadPendingString(plan, "rollbackErrorCode"));
        Assert.AreEqual(1, quarantine.RetainedGroups);
        Assert.IsTrue(quarantine.HandleWasOpenAtTransfer);
    }

    [TestMethod]
    public async Task WorkerIdentityMismatchIsTerminatedBeforeSingleReplacement()
    {
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth("worker-identity-mismatch", groupId);
        var processes = new RecordingProcessPort(watchdog, workerIdentityMismatches: 1);
        using UpdateOwnedProcessGroup failedGroup = RecordingProcessPort.Group(groupId, updated);
        var recovery = new UpdateRecoveryWatchdog(processes);

        await recovery.RecoverAsync(
            plan,
            watchdog,
            failedGroup,
            groupId,
            UpdateActivationFailureCode.UpdatedProcessExited,
            CancellationToken.None);

        CollectionAssert.AreEqual(
            new[]
            {
                "wait-failed-group",
                "terminate-failed-group",
                "start-worker",
                "wait-worker",
                "terminate-worker-group",
                "start-worker",
                "wait-worker",
                "wait-worker-group",
                "start-restored",
            },
            processes.Events);
        Assert.AreEqual("rollbackComplete", ReadOnlyReceiptState());
    }

    [TestMethod]
    public async Task WorkerTerminationRequiresBothTerminationAndEmptyProof()
    {
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth("worker-termination-proof", groupId);
        var processes = new RecordingProcessPort(
            watchdog,
            workerIdentityMismatches: 1,
            termination: new ExactProcessTermination(false, true));
        using UpdateOwnedProcessGroup failedGroup = RecordingProcessPort.Group(groupId, updated);
        var quarantine = new RecordingQuarantine();
        var recovery = new UpdateRecoveryWatchdog(processes, quarantine: quarantine);

        await recovery.RecoverAsync(
            plan,
            watchdog,
            failedGroup,
            groupId,
            UpdateActivationFailureCode.UpdatedProcessExited,
            CancellationToken.None);

        Assert.AreEqual(1, processes.Events.Count(entry => entry == "start-worker"));
        Assert.AreEqual("rollbackFailed", ReadPendingString(plan, "state"));
        Assert.AreEqual(
            "UPDATE_WORKER_GROUP_NOT_QUIESCED",
            ReadPendingString(plan, "rollbackErrorCode"));
        Assert.AreEqual(1, quarantine.RetainedGroups);
        Assert.IsTrue(quarantine.HandleWasOpenAtTransfer);
    }

    [TestMethod]
    public async Task WorkerWaitExceptionTerminatesGroupBeforeUsingReceipt()
    {
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth("worker-wait-exception", groupId);
        var processes = new RecordingProcessPort(
            watchdog,
            workerExactWaitFailure: new InvalidOperationException("simulated worker wait failure"));
        using UpdateOwnedProcessGroup failedGroup = RecordingProcessPort.Group(groupId, updated);
        var recovery = new UpdateRecoveryWatchdog(processes);

        await recovery.RecoverAsync(
            plan,
            watchdog,
            failedGroup,
            groupId,
            UpdateActivationFailureCode.UpdatedProcessExited,
            CancellationToken.None);

        CollectionAssert.AreEqual(
            new[]
            {
                "wait-failed-group",
                "terminate-failed-group",
                "start-worker",
                "wait-worker",
                "terminate-worker-group",
                "start-restored",
            },
            processes.Events);
        Assert.AreEqual("rollbackComplete", ReadOnlyReceiptState());
    }

    [TestMethod]
    public async Task ReceiptDoesNotAuthorizeRestoredLaunchWhenTargetIdentityDrifts()
    {
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth("receipt-target-drift", groupId);
        var processes = new RecordingProcessPort(
            watchdog,
            afterRollbackWorker: _ => File.WriteAllText(
                Path.Combine(plan.TargetRoot, "release.json"),
                """{"product":"VibeTable","version":"9.9.9","platform":"windows","architecture":"x64"}"""));
        using UpdateOwnedProcessGroup failedGroup = RecordingProcessPort.Group(groupId, updated);
        var recovery = new UpdateRecoveryWatchdog(processes);

        await recovery.RecoverAsync(
            plan,
            watchdog,
            failedGroup,
            groupId,
            UpdateActivationFailureCode.UpdatedProcessExited,
            CancellationToken.None);

        Assert.IsFalse(processes.Events.Contains("start-restored"));
        Assert.AreEqual(1, processes.Events.Count(entry => entry == "start-worker"));
        Assert.IsTrue(File.Exists(PendingUpdateActivationJournal.GetRollbackReceiptPath(
            plan.TargetRoot,
            ReadOnlyRollbackAttempt())));
    }

    [TestMethod]
    public async Task PreexistingWorkerRootMustMatchTargetPackageIdentity()
    {
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth("worker-root-identity", groupId);
        CreatePackageTree(
            Path.Combine(plan.StagingRoot, "rollback-worker", "VibeTable"),
            "0.9.0",
            "untrusted-worker");
        var processes = new RecordingProcessPort(watchdog);
        using UpdateOwnedProcessGroup failedGroup = RecordingProcessPort.Group(groupId, updated);
        var recovery = new UpdateRecoveryWatchdog(processes);

        await recovery.RecoverAsync(
            plan,
            watchdog,
            failedGroup,
            groupId,
            UpdateActivationFailureCode.UpdatedProcessExited,
            CancellationToken.None);

        Assert.IsFalse(processes.Events.Contains("start-worker"));
        Assert.AreEqual("rollbackFailed", ReadPendingString(plan, "state"));
        Assert.AreEqual(
            "UPDATE_ROLLBACK_WORKER_INVALID",
            ReadPendingString(plan, "rollbackErrorCode"));
    }

    [TestMethod]
    public async Task WorkerRootJunctionIsRejectedBeforeWorkerLaunch()
    {
        const string groupId = "failed-owned-group";
        (UpdateApplyPlan plan, UpdateProcessIdentity watchdog, UpdateProcessIdentity updated) =
            PrepareAwaitingHealth("worker-root-junction", groupId);
        string outside = Path.Combine(Root(), "outside-worker-root");
        CreatePackageTree(outside, plan.TargetVersion, "outside-worker");
        string workerParent = Path.Combine(plan.StagingRoot, "rollback-worker");
        Directory.CreateDirectory(workerParent);
        string workerRoot = Path.Combine(workerParent, "VibeTable");
        if (!TryCreateJunction(workerRoot, outside))
        {
            Assert.Inconclusive("当前 Windows 环境无法创建目录 junction。");
        }
        try
        {
            var processes = new RecordingProcessPort(watchdog);
            using UpdateOwnedProcessGroup failedGroup =
                RecordingProcessPort.Group(groupId, updated);
            var recovery = new UpdateRecoveryWatchdog(processes);

            await recovery.RecoverAsync(
                plan,
                watchdog,
                failedGroup,
                groupId,
                UpdateActivationFailureCode.UpdatedProcessExited,
                CancellationToken.None);

            Assert.IsFalse(processes.Events.Contains("start-worker"));
            Assert.AreEqual("rollbackFailed", ReadPendingString(plan, "state"));
            Assert.AreEqual(
                "UPDATE_REPARSE_POINT_REJECTED",
                ReadPendingString(plan, "rollbackErrorCode"));
        }
        finally
        {
            if (Directory.Exists(workerRoot))
            {
                Directory.Delete(workerRoot);
            }
        }
    }

    [TestMethod]
    [DataRow(true, true, "UPDATE_GROUP_IDENTITY_MISMATCH")]
    [DataRow(false, true, "UPDATE_GROUP_NOT_QUIESCED")]
    [DataRow(true, false, "UPDATE_GROUP_NOT_QUIESCED")]
    public async Task UpdatedGroupIdentityMismatchIsTerminatedAndNeverStartsWorker(
        bool terminated,
        bool groupEmpty,
        string expectedError)
    {
        UpdateApplyPlan plan = PreparePublishedPlan(
            $"updated-group-mismatch-{terminated}-{groupEmpty}");
        var watchdog = new UpdateProcessIdentity(
            1101,
            new DateTimeOffset(2026, 8, 27, 10, 0, 0, TimeSpan.Zero));
        var processes = new RecordingProcessPort(
            watchdog,
            updatedGroupIdOverride: "unexpected-owned-group",
            termination: new ExactProcessTermination(terminated, groupEmpty));
        var quarantine = new RecordingQuarantine();
        var recovery = new UpdateRecoveryWatchdog(processes, quarantine: quarantine);

        await recovery.RunUpdatedPackageAsync(plan, CancellationToken.None);

        CollectionAssert.AreEqual(
            new[] { "start-updated", "terminate-updated-group" },
            processes.Events);
        Assert.AreEqual("rollbackFailed", ReadPendingString(plan, "state"));
        Assert.AreEqual(expectedError, ReadPendingString(plan, "rollbackErrorCode"));
        Assert.AreEqual(terminated && groupEmpty ? 0 : 1, quarantine.RetainedGroups);
    }

    [TestMethod]
    public async Task UpdatedMismatchTransfersLastHandleWhenTerminationIsUnproven()
    {
        UpdateApplyPlan plan = PreparePublishedPlan("updated-mismatch-retained");
        var watchdog = new UpdateProcessIdentity(
            1101,
            new DateTimeOffset(2026, 8, 27, 10, 0, 0, TimeSpan.Zero));
        var processes = new RecordingProcessPort(
            watchdog,
            updatedGroupIdOverride: "unexpected-owned-group",
            termination: new ExactProcessTermination(false, true));
        var quarantine = new RecordingQuarantine();
        var recovery = new UpdateRecoveryWatchdog(processes, quarantine: quarantine);

        await recovery.RunUpdatedPackageAsync(plan, CancellationToken.None);

        Assert.AreEqual(1, quarantine.RetainedGroups);
        Assert.IsTrue(quarantine.HandleWasOpenAtTransfer);
        Assert.AreEqual("rollbackFailed", ReadPendingString(plan, "state"));
    }

    [TestMethod]
    public async Task UnprovenUpdatedStartFailureNeverMarksGroupQuiescedOrStartsWorker()
    {
        UpdateApplyPlan plan = PreparePublishedPlan("updated-start-unproven");
        var watchdog = new UpdateProcessIdentity(
            1101,
            new DateTimeOffset(2026, 8, 27, 10, 0, 0, TimeSpan.Zero));
        var processes = new RecordingProcessPort(
            watchdog,
            updatedStartFailure: new AggregateException(
                "simulated native cleanup uncertainty"));
        var recovery = new UpdateRecoveryWatchdog(processes);

        await recovery.RunUpdatedPackageAsync(plan, CancellationToken.None);

        CollectionAssert.AreEqual(new[] { "start-updated" }, processes.Events);
        Assert.AreEqual("rollbackFailed", ReadPendingString(plan, "state"));
        Assert.AreEqual(
            "UPDATE_GROUP_NOT_QUIESCED",
            ReadPendingString(plan, "rollbackErrorCode"));
        Assert.IsNull(ReadPendingNullableString(plan, "ownedGroupQuiescedAtUtc"));
    }

    [TestMethod]
    public async Task UpdatedStartCleanupFailureTransfersRetainedNativeOwnership()
    {
        UpdateApplyPlan plan = PreparePublishedPlan("updated-start-retained");
        var watchdog = new UpdateProcessIdentity(
            1101,
            new DateTimeOffset(2026, 8, 27, 10, 0, 0, TimeSpan.Zero));
        UpdateOwnedProcessGroup retained = RecordingProcessPort.Group(
            "retained-start-group",
            new UpdateProcessIdentity(1301, watchdog.StartedAtUtc.AddSeconds(1)));
        var processes = new RecordingProcessPort(
            watchdog,
            updatedStartFailure: new UpdateOwnedProcessStartException(
                "simulated cleanup uncertainty",
                groupEmpty: false,
                new InvalidOperationException("simulated native cleanup failure"),
                retained));
        var quarantine = new RecordingQuarantine();
        var recovery = new UpdateRecoveryWatchdog(processes, quarantine: quarantine);

        await recovery.RunUpdatedPackageAsync(plan, CancellationToken.None);

        Assert.AreEqual(1, quarantine.RetainedGroups);
        Assert.IsTrue(quarantine.HandleWasOpenAtTransfer);
        Assert.AreEqual("rollbackFailed", ReadPendingString(plan, "state"));
        Assert.AreEqual(
            "UPDATE_GROUP_NOT_QUIESCED",
            ReadPendingString(plan, "rollbackErrorCode"));
    }

    [TestMethod]
    public async Task MalformedConfirmedStateTerminatesOwnedGroupAndFailsClosed()
    {
        UpdateApplyPlan plan = PreparePublishedPlan("malformed-confirmed");
        var watchdog = new UpdateProcessIdentity(
            1101,
            new DateTimeOffset(2026, 8, 27, 10, 0, 0, TimeSpan.Zero));
        var processes = new RecordingProcessPort(
            watchdog,
            afterUpdatedStart: _ => MutatePointer(plan, journal =>
            {
                journal["state"] = "confirmed";
                journal["confirmedAt"] = null;
            }));
        var recovery = new UpdateRecoveryWatchdog(processes);

        await recovery.RunUpdatedPackageAsync(plan, CancellationToken.None);

        CollectionAssert.AreEqual(
            new[] { "start-updated", "terminate-updated-group" },
            processes.Events);
        Assert.AreEqual("rollbackFailed", ReadPendingString(plan, "state"));
        Assert.AreEqual(
            "UPDATE_ACTIVATION_INVALID",
            ReadPendingString(plan, "rollbackErrorCode"));
    }

    [TestMethod]
    public async Task UpdatedWaitExceptionTerminatesOwnedGroupAndFailsClosed()
    {
        UpdateApplyPlan plan = PreparePublishedPlan("updated-wait-exception");
        var watchdog = new UpdateProcessIdentity(
            1101,
            new DateTimeOffset(2026, 8, 27, 10, 0, 0, TimeSpan.Zero));
        var processes = new RecordingProcessPort(
            watchdog,
            claimUpdated: true,
            exactWaitFailure: new InvalidOperationException("simulated wait failure"));
        var recovery = new UpdateRecoveryWatchdog(processes);

        await recovery.RunUpdatedPackageAsync(plan, CancellationToken.None);

        CollectionAssert.AreEqual(
            new[] { "start-updated", "wait-updated", "terminate-updated-group" },
            processes.Events);
        Assert.AreEqual("rollbackFailed", ReadPendingString(plan, "state"));
        Assert.AreEqual(
            "UPDATE_RECOVERY_WATCHDOG_FAILED",
            ReadPendingString(plan, "rollbackErrorCode"));
    }

    [TestMethod]
    public async Task SmokeUpdatedLaunchReceivesItsOwnTestControlDirectory()
    {
        UpdateApplyPlan plan = PreparePublishedPlan("updated-smoke-controls", smokeTest: true);
        var watchdog = new UpdateProcessIdentity(
            1101,
            new DateTimeOffset(2026, 8, 27, 10, 0, 0, TimeSpan.Zero));
        UpdateUpdatedPackageLaunch? launch = null;
        var processes = new RecordingProcessPort(
            watchdog,
            afterUpdatedStart: value => launch = value);
        var recovery = new UpdateRecoveryWatchdog(processes);

        await recovery.RunUpdatedPackageAsync(plan, CancellationToken.None);

        Assert.IsNotNull(launch);
        string stageParent = Directory.GetParent(plan.StagingRoot.TrimEnd(
            Path.DirectorySeparatorChar,
            Path.AltDirectorySeparatorChar))!.FullName;
        CollectionAssert.Contains(launch.Arguments.ToArray(), "--self-update-smoke");
        CollectionAssert.Contains(launch.Arguments.ToArray(), "--test-mode");
        int readinessIndex = launch.Arguments.IndexOf("--readiness-dir");
        int controlsIndex = launch.Arguments.IndexOf("--e2e-controls-dir");
        Assert.IsTrue(readinessIndex >= 0);
        Assert.IsTrue(controlsIndex >= 0);
        Assert.AreEqual(
            Path.Combine(stageParent, "self-update-readiness"),
            launch.Arguments[readinessIndex + 1]);
        Assert.AreEqual(
            1,
            launch.Arguments.Count(argument => argument == "--e2e-controls-dir"));
        Assert.AreEqual(
            Path.Combine(stageParent, "self-update-updated-controls"),
            launch.Arguments[controlsIndex + 1]);
        Assert.IsFalse(launch.Arguments.Contains(
            Path.Combine(stageParent, "self-update-restored-controls")));
    }

    [TestMethod]
    public async Task NonSmokeUpdatedLaunchDoesNotReceiveTestControls()
    {
        UpdateApplyPlan plan = PreparePublishedPlan("updated-production-controls");
        var watchdog = new UpdateProcessIdentity(
            1101,
            new DateTimeOffset(2026, 8, 27, 10, 0, 0, TimeSpan.Zero));
        UpdateUpdatedPackageLaunch? launch = null;
        var processes = new RecordingProcessPort(
            watchdog,
            afterUpdatedStart: value => launch = value);
        var recovery = new UpdateRecoveryWatchdog(processes);

        await recovery.RunUpdatedPackageAsync(plan, CancellationToken.None);

        Assert.IsNotNull(launch);
        Assert.IsFalse(launch.Arguments.Contains("--e2e-controls-dir"));
    }

    [TestMethod]
    public void ApplyFailureDoesNotRestartTargetFromUnsettledRecoveryState()
    {
        UpdateApplyPlan plan = PreparePublishedPlan("apply-failure-no-restart");
        var watchdog = new UpdateProcessIdentity(
            1101,
            new DateTimeOffset(2026, 8, 27, 10, 0, 0, TimeSpan.Zero));
        PendingUpdateActivationJournal.RecordUpdatedLaunch(
            plan,
            watchdog,
            "unsettled-owned-group",
            new string('d', 64));
        PendingUpdateActivationJournal.RecordRollbackFailed(
            plan,
            watchdog,
            "UPDATE_GROUP_NOT_QUIESCED");
        bool restarted = false;

        bool allowed = UpdateProcessCommand.TryRestartAfterApplyFailure(
            plan,
            watchdog,
            _ => restarted = true);

        Assert.IsFalse(allowed);
        Assert.IsFalse(restarted);
        Assert.IsTrue(File.Exists(PendingUpdateActivationJournal.GetPointerPath(
            plan.TargetRoot)));
    }

    [TestMethod]
    public void ApplyFailureRestartsOnlyAfterPreparedAttemptIsSafelyAbandoned()
    {
        UpdateApplyPlan plan = PreparePublishedPlan("apply-failure-safe-restart");
        CreatePackageTree(plan.TargetRoot, plan.CurrentVersion, "old");
        var updater = new UpdateProcessIdentity(
            1101,
            new DateTimeOffset(2026, 8, 27, 10, 0, 0, TimeSpan.Zero));
        string? restartedRoot = null;

        bool allowed = UpdateProcessCommand.TryRestartAfterApplyFailure(
            plan,
            updater,
            root => restartedRoot = root);

        Assert.IsTrue(allowed);
        Assert.AreEqual(plan.TargetRoot, restartedRoot);
        Assert.IsFalse(File.Exists(PendingUpdateActivationJournal.GetPointerPath(
            plan.TargetRoot)));
    }

    [TestMethod]
    public void ApplyFailureDoesNotRestartAnIncompleteCurrentPackage()
    {
        UpdateApplyPlan plan = PreparePublishedPlan("apply-failure-incomplete-target");
        CreatePackageTree(plan.TargetRoot, plan.CurrentVersion, "old");
        Directory.Delete(Path.Combine(plan.TargetRoot, "resources"), recursive: true);
        var updater = new UpdateProcessIdentity(
            1101,
            new DateTimeOffset(2026, 8, 27, 10, 0, 0, TimeSpan.Zero));
        bool restarted = false;

        bool allowed = UpdateProcessCommand.TryRestartAfterApplyFailure(
            plan,
            updater,
            _ => restarted = true);

        Assert.IsFalse(allowed);
        Assert.IsFalse(restarted);
        Assert.IsTrue(File.Exists(PendingUpdateActivationJournal.GetPointerPath(
            plan.TargetRoot)));
    }

    private (UpdateApplyPlan Plan, UpdateProcessIdentity Watchdog,
        UpdateProcessIdentity Updated) PrepareAwaitingHealth(
            string suffix,
            string groupId,
            bool smokeTest = false)
    {
        string root = Root();
        string target = Path.Combine(root, "VibeTable.Next");
        CreatePackageTree(target, "1.1.0", "new");
        string stage = Path.Combine(root, $".VibeTable.Next.update-{suffix}");
        string source = Path.Combine(stage, "package", "VibeTable");
        CreatePackageTree(source, "1.1.0", "new");
        CreatePackageTree(Path.Combine(stage, "backup"), "1.0.0", "old");
        var plan = new UpdateApplyPlan(
            1,
            target,
            source,
            stage,
            123,
            "1.0.0",
            "1.1.0",
            new string('b', 64),
            SmokeTest: smokeTest);
        File.WriteAllText(
            Path.Combine(stage, "update-plan.json"),
            JsonSerializer.Serialize(plan));
        var watchdog = new UpdateProcessIdentity(
            1101,
            new DateTimeOffset(2026, 8, 27, 10, 0, 0, TimeSpan.Zero));
        var updated = new UpdateProcessIdentity(1102, watchdog.StartedAtUtc.AddSeconds(1));
        string nonce = new('c', 64);
        PendingUpdateActivationJournal.Publish(plan, watchdog);
        PendingUpdateActivationJournal.RecordUpdatedLaunch(plan, watchdog, groupId, nonce);
        UpdateActivationStartupResolution startup = PendingUpdateActivationJournal.ResolveStartup(
            ["--claim-update", nonce],
            target,
            updated,
            new NoOpLifetime(),
            _ => Task.FromResult(true),
            _ => true);
        Assert.IsNotNull(startup.Settlement);
        return (plan, watchdog, updated);
    }

    private UpdateApplyPlan PreparePublishedPlan(string suffix, bool smokeTest = false)
    {
        string root = Root();
        string target = Path.Combine(root, "VibeTable.Next");
        CreatePackageTree(target, "1.1.0", "new");
        string stage = Path.Combine(root, $".VibeTable.Next.update-{suffix}");
        string source = Path.Combine(stage, "package", "VibeTable");
        CreatePackageTree(source, "1.1.0", "new");
        CreatePackageTree(Path.Combine(stage, "backup"), "1.0.0", "old");
        var plan = new UpdateApplyPlan(
            1,
            target,
            source,
            stage,
            123,
            "1.0.0",
            "1.1.0",
            new string('b', 64),
            SmokeTest: smokeTest);
        File.WriteAllText(
            Path.Combine(stage, "update-plan.json"),
            JsonSerializer.Serialize(plan));
        PendingUpdateActivationJournal.Publish(
            plan,
            new UpdateProcessIdentity(
                1101,
                new DateTimeOffset(2026, 8, 27, 10, 0, 0, TimeSpan.Zero)));
        return plan;
    }

    private static string ReadPendingString(UpdateApplyPlan plan, string propertyName)
    {
        using JsonDocument document = JsonDocument.Parse(File.ReadAllText(
            PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot)));
        return document.RootElement.GetProperty(propertyName).GetString()!;
    }

    private static string? ReadPendingNullableString(
        UpdateApplyPlan plan,
        string propertyName)
    {
        using JsonDocument document = JsonDocument.Parse(File.ReadAllText(
            PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot)));
        JsonElement property = document.RootElement.GetProperty(propertyName);
        return property.ValueKind == JsonValueKind.Null ? null : property.GetString();
    }

    private static void MutatePointer(
        UpdateApplyPlan plan,
        Action<JsonObject> mutation)
    {
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);
        JsonObject journal = JsonNode.Parse(File.ReadAllText(pointerPath))!.AsObject();
        mutation(journal);
        File.WriteAllText(pointerPath, journal.ToJsonString());
    }

    private string ReadOnlyReceiptState()
    {
        string receipt = Directory.GetFiles(
            Root(),
            ".VibeTable.Next.update-rollback-*.json",
            SearchOption.TopDirectoryOnly).Single();
        using JsonDocument document = JsonDocument.Parse(File.ReadAllText(receipt));
        return document.RootElement.GetProperty("state").GetString()!;
    }

    private string ReadOnlyRollbackAttempt()
    {
        string receipt = Directory.GetFiles(
            Root(),
            ".VibeTable.Next.update-rollback-*.json",
            SearchOption.TopDirectoryOnly).Single();
        using JsonDocument document = JsonDocument.Parse(File.ReadAllText(receipt));
        return document.RootElement.GetProperty("rollbackAttempt").GetString()!;
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
        foreach (string argument in new[] { "/d", "/c", "mklink", "/J", junction, target })
        {
            start.ArgumentList.Add(argument);
        }
        using System.Diagnostics.Process? process = System.Diagnostics.Process.Start(start);
        if (process is null)
        {
            return false;
        }
        process.WaitForExit();
        return process.ExitCode == 0;
    }

    private string Root()
    {
        _root ??= Path.Combine(
            Environment.CurrentDirectory,
            "build",
            "update-recovery-watchdog-tests",
            Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(_root);
        return _root;
    }

    private sealed class NoOpLifetime : IUpdateHostLifetimePort
    {
        public void RequestExit(int exitCode)
        {
        }
    }

    private sealed class RecordingQuarantine : IUpdateOwnedProcessQuarantine
    {
        public int RetainedGroups { get; private set; }

        public bool HandleWasOpenAtTransfer { get; private set; }

        public Task RetainUntilEmptyAsync(
            UpdateOwnedProcessGroup group,
            IUpdateRecoveryProcessPort processes,
            TimeSpan attemptBudget)
        {
            RetainedGroups++;
            HandleWasOpenAtTransfer = !group.Ownership.IsClosed;
            group.Dispose();
            return Task.CompletedTask;
        }
    }

    private sealed class BlockingQuarantine : IUpdateOwnedProcessQuarantine
    {
        private readonly TaskCompletionSource _release =
            new(TaskCreationOptions.RunContinuationsAsynchronously);

        internal TaskCompletionSource Entered { get; } =
            new(TaskCreationOptions.RunContinuationsAsynchronously);

        public async Task RetainUntilEmptyAsync(
            UpdateOwnedProcessGroup group,
            IUpdateRecoveryProcessPort processes,
            TimeSpan attemptBudget)
        {
            Entered.TrySetResult();
            await _release.Task.ConfigureAwait(false);
            group.Dispose();
        }

        internal void Release() => _release.TrySetResult();
    }

    private sealed class RecordingProcessPort(
        UpdateProcessIdentity watchdog,
        bool failRestoredLaunch = false,
        int workerStartFailures = 0,
        bool workerStartGroupEmpty = true,
        int workerIdentityMismatches = 0,
        string? updatedGroupIdOverride = null,
        ExactProcessTermination? termination = null,
        Exception? updatedStartFailure = null,
        Action<UpdateUpdatedPackageLaunch>? afterUpdatedStart = null,
        bool claimUpdated = false,
        Exception? exactWaitFailure = null,
        Exception? ownedGroupWaitFailure = null,
        Exception? terminationFailure = null,
        Exception? workerExactWaitFailure = null,
        Action<UpdateRollbackLaunch>? afterRollbackWorker = null,
        bool failWorkerRecovery = false)
        : IUpdateRecoveryProcessPort
    {
        private readonly UpdateProcessIdentity _watchdog = watchdog;
        private int _workerStartFailures = workerStartFailures;
        private int _workerIdentityMismatches = workerIdentityMismatches;
        private int? _identityMismatchProcessId;
        private string? _updatedGroupId;
        public List<string> Events { get; } = [];

        public UpdateRestoredPackageLaunch? RestoredLaunch { get; private set; }

        public UpdateProcessIdentity Current() => _watchdog;

        public UpdateOwnedProcessGroup StartUpdatedPackage(UpdateUpdatedPackageLaunch launch)
        {
            Events.Add("start-updated");
            if (updatedStartFailure is not null)
            {
                throw updatedStartFailure;
            }
            _updatedGroupId = updatedGroupIdOverride ?? launch.OwnedGroupId!;
            var updated = new UpdateProcessIdentity(
                1301,
                _watchdog.StartedAtUtc.AddSeconds(1));
            if (claimUpdated)
            {
                int nonceIndex = launch.Arguments.IndexOf("--claim-update");
                UpdateActivationStartupResolution resolution =
                    PendingUpdateActivationJournal.ResolveStartup(
                        ["--claim-update", launch.Arguments[nonceIndex + 1]],
                        launch.WorkingDirectory,
                        updated,
                        new NoOpLifetime(),
                        _ => Task.FromResult(true),
                        _ => true);
                Assert.AreEqual(
                    UpdateActivationStartupDisposition.Proceed,
                    resolution.Disposition);
            }
            afterUpdatedStart?.Invoke(launch);
            return Group(
                _updatedGroupId,
                updated);
        }

        public UpdateOwnedProcessGroup StartRollbackWorker(UpdateRollbackLaunch launch)
        {
            Events.Add("start-worker");
            if (_workerStartFailures > 0)
            {
                _workerStartFailures--;
                UpdateOwnedProcessGroup? retained = workerStartGroupEmpty
                    ? null
                    : Group(
                        launch.OwnedGroupId!,
                        new UpdateProcessIdentity(
                            1201,
                            _watchdog.StartedAtUtc.AddSeconds(2)));
                throw new UpdateOwnedProcessStartException(
                    "simulated worker start failure",
                    workerStartGroupEmpty,
                    new InvalidOperationException("simulated native start failure"),
                    retained);
            }
            int nonceIndex = launch.Arguments.IndexOf("--worker-nonce");
            int targetIndex = launch.Arguments.IndexOf("--rollback-update");
            var worker = new UpdateProcessIdentity(
                1201,
                _watchdog.StartedAtUtc.AddSeconds(2));
            if (_workerIdentityMismatches > 0)
            {
                _workerIdentityMismatches--;
                _identityMismatchProcessId = worker.ProcessId;
                return Group(launch.OwnedGroupId!, worker);
            }
            try
            {
                PendingUpdateActivationJournal.RunRollbackWorker(
                    launch.Arguments[targetIndex + 1],
                    launch.Arguments[nonceIndex + 1],
                    worker,
                    failWorkerRecovery
                        ? _ => throw new IOException("simulated worker file access failure")
                        : null);
            }
            catch (IOException) when (failWorkerRecovery)
            {
                // The child process exits after persisting its failure in the journal.
            }
            afterRollbackWorker?.Invoke(launch);
            return Group(launch.OwnedGroupId!, worker);
        }

        public Task<ExactProcessExit> WaitForExactExitAsync(
            UpdateProcessIdentity process,
            TimeSpan timeout,
            CancellationToken cancellationToken)
        {
            bool updated = process.ProcessId == 1301;
            Events.Add(updated ? "wait-updated" : "wait-worker");
            if (updated && exactWaitFailure is not null)
            {
                throw exactWaitFailure;
            }
            if (!updated && workerExactWaitFailure is not null)
            {
                throw workerExactWaitFailure;
            }
            if (_identityMismatchProcessId == process.ProcessId)
            {
                _identityMismatchProcessId = null;
                return Task.FromResult(new ExactProcessExit(true, false, 0));
            }
            return Task.FromResult(new ExactProcessExit(true, true, 0));
        }

        public Task<OwnedProcessGroupExit> WaitForOwnedProcessGroupExitAsync(
            UpdateOwnedProcessGroup processGroup,
            TimeSpan timeout,
            CancellationToken cancellationToken)
        {
            bool worker = processGroup.GroupId != "failed-owned-group";
            Events.Add(worker ? "wait-worker-group" : "wait-failed-group");
            if (!worker && ownedGroupWaitFailure is not null)
            {
                throw ownedGroupWaitFailure;
            }
            return Task.FromResult(new OwnedProcessGroupExit(worker));
        }

        public Task<ExactProcessTermination> TerminateOwnedProcessGroupAsync(
            UpdateOwnedProcessGroup processGroup,
            TimeSpan timeout,
            CancellationToken cancellationToken)
        {
            Events.Add(processGroup.GroupId switch
            {
                "failed-owned-group" => "terminate-failed-group",
                "unexpected-owned-group" => "terminate-updated-group",
                _ when processGroup.GroupId == _updatedGroupId => "terminate-updated-group",
                _ => "terminate-worker-group",
            });
            if (terminationFailure is not null)
            {
                throw terminationFailure;
            }
            return Task.FromResult(processGroup.GroupId == "failed-owned-group"
                ? new ExactProcessTermination(true, true)
                : termination ?? new ExactProcessTermination(true, true));
        }

        public void StartRestoredPackage(UpdateRestoredPackageLaunch launch)
        {
            Events.Add("start-restored");
            RestoredLaunch = launch;
            if (failRestoredLaunch)
            {
                throw new InvalidOperationException("simulated restored launch failure");
            }
        }

        internal static UpdateOwnedProcessGroup Group(
            string groupId,
            UpdateProcessIdentity root) => new(
            groupId,
            root,
            new SafeFileHandle(new IntPtr(-1), ownsHandle: false));
    }
}

internal static class UpdateArgumentListExtensions
{
    internal static int IndexOf(this IReadOnlyList<string> arguments, string value)
    {
        for (int index = 0; index < arguments.Count; index++)
        {
            if (arguments[index] == value)
            {
                return index;
            }
        }
        return -1;
    }
}
